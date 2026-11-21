import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../core/api/fireshare_client.dart';
import '../../core/storage/connection_manager.dart';
import '../../core/transfer/upload_queue.dart';
import '../../core/util/format.dart';
import 'google_photos_service.dart';

/// Google Foto dentro FireShare: accedi, scegli nel selettore ufficiale di
/// Google quali foto/video portare sul PC e FireShare li scarica e li carica.
class GoogleTab extends StatefulWidget {
  const GoogleTab({super.key});

  @override
  State<GoogleTab> createState() => _GoogleTabState();
}

class _GoogleTabState extends State<GoogleTab> with AutomaticKeepAliveClientMixin {
  final _picker = GooglePhotosPicker.instance;
  final _job = GoogleImportJob.instance;

  bool _signedIn = false;
  bool _busy = false;
  bool _waiting = false; // in attesa che l'utente scelga in Google Foto
  String? _error;
  PickerSession? _session;
  List<PickedItem> _items = [];
  Map<String, String>? _headers;

  // risultato verificato dopo l'importazione
  int _verified = 0;
  DateTime? _oldest;
  DateTime? _newest;
  bool _verifying = false;

  @override
  bool get wantKeepAlive => true;

  @override
  void initState() {
    super.initState();
    _job.addListener(_onJob);
    UploadQueue.instance.addListener(_onQueue);
    _picker.signInSilently().then((a) {
      if (mounted) setState(() => _signedIn = a != null);
    }).catchError((_) {});
  }

  @override
  void dispose() {
    _waiting = false;
    _job.removeListener(_onJob);
    UploadQueue.instance.removeListener(_onQueue);
    super.dispose();
  }

  void _onJob() {
    if (!mounted) return;
    setState(() {});
    if (!_job.isActive && _session != null) {
      final id = _session!.id;
      _session = null;
      _picker.deleteSession(id);
    }
  }

  bool _wasPending = false;
  void _onQueue() {
    final pending = UploadQueue.instance.pendingCount > 0;
    if (_wasPending && !pending && _job.phase == ImportPhase.finished) _verifyResult();
    _wasPending = pending;
  }

  // --- accesso ----------------------------------------------------------------------

  Future<void> _signIn() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final acc = await _picker.signIn();
      if (mounted) setState(() => _signedIn = acc != null);
    } on PlatformException catch (e) {
      if (mounted) {
        setState(() => _error = 'Accesso a Google non riuscito (${e.code}). '
            'Di solito dipende dalla configurazione OAuth del progetto Google Cloud '
            '(nome del pacchetto + impronta SHA-1): vedi la guida nel file AI_HANDOFF_FIRESHARE.md.');
      }
    } catch (e) {
      if (mounted) setState(() => _error = 'Accesso a Google non riuscito: $e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _signOut() async {
    await _picker.signOut();
    if (mounted) {
      setState(() {
        _signedIn = false;
        _items = [];
      });
    }
  }

  // --- selezione --------------------------------------------------------------------

  Future<void> _pick() async {
    _job.reset();
    setState(() {
      _busy = true;
      _error = null;
      _items = [];
      _verified = 0;
    });
    try {
      var session = await _picker.createSession();
      _session = session;
      final ok = await launchUrl(Uri.parse(session.pickerUri), mode: LaunchMode.externalApplication);
      if (!ok) throw GooglePhotosException('Impossibile aprire Google Foto.');
      setState(() => _waiting = true);
      final deadline = DateTime.now().add(session.timeout);
      while (mounted && _waiting && DateTime.now().isBefore(deadline)) {
        await Future<void>.delayed(session.pollInterval);
        if (!mounted || !_waiting) return;
        session = await _picker.getSession(session.id);
        if (session.mediaItemsSet) {
          final items = await _picker.listItems(session.id);
          final headers = await _picker.imageHeaders();
          if (!mounted) return;
          setState(() {
            _items = items;
            _headers = headers;
          });
          if (items.isEmpty) _error = 'Non hai scelto nessun elemento.';
          break;
        }
      }
      if (_waiting && _items.isEmpty && _error == null) {
        _error = 'Tempo scaduto: nessuna selezione ricevuta.';
      }
    } on GooglePhotosException catch (e) {
      _error = e.message;
    } on PlatformException catch (e) {
      _error = 'Errore Google (${e.code}).';
    } finally {
      if (mounted) {
        setState(() {
          _waiting = false;
          _busy = false;
        });
      }
    }
  }

  void _cancelPick() {
    final id = _session?.id;
    setState(() {
      _waiting = false;
      _session = null;
    });
    if (id != null) _picker.deleteSession(id);
  }

  // --- importazione -------------------------------------------------------------------

  Future<void> _startImport() async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Prima di caricare'),
        content: const Text(
          'Google consegna alle app copie «di servizio» degli elementi:\n\n'
          '• le foto non contengono la posizione GPS;\n'
          '• i video sono una versione ricodificata in alta qualità, non il file originale.\n\n'
          'Gli originali restano su Google Foto. Se ti servono identici (posizione, video originali) '
          'usa Google Takeout, oppure non eliminare nulla da Google Foto.',
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Annulla')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Ho capito, carica')),
        ],
      ),
    );
    if (ok != true) return;
    setState(() {
      _verified = 0;
      _error = null;
    });
    unawaited(_job.start(_items));
  }

  Future<void> _verifyResult() async {
    if (_verifying || _job.items.isEmpty) return;
    _verifying = true;
    try {
      final done = await ConnectionManager.instance.client.checkComplete(_job.items.map((i) => i.clientKey));
      final ok = _job.items.where((i) => done.contains(i.clientKey)).toList();
      final dates = ok.map((i) => i.createTime).whereType<DateTime>().toList()..sort();
      if (mounted) {
        setState(() {
          _verified = ok.length;
          _oldest = dates.isEmpty ? null : dates.first;
          _newest = dates.isEmpty ? null : dates.last;
        });
      }
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } finally {
      _verifying = false;
    }
  }

  // --- interfaccia ----------------------------------------------------------------------

  @override
  Widget build(BuildContext context) {
    super.build(context);
    final scheme = Theme.of(context).colorScheme;
    final acc = _picker.account;
    return ListView(
      padding: const EdgeInsets.all(16),
      children: [
        if (!_signedIn) ...[
          const Text(
            'Accedi con il tuo account Google per scegliere, dentro FireShare, quali foto e video '
            'portare sul PC.',
          ),
          const SizedBox(height: 12),
          FilledButton.icon(
            onPressed: _busy ? null : _signIn,
            icon: const Icon(Icons.login),
            label: const Text('Accedi con Google'),
          ),
        ] else ...[
          Row(children: [
            const Icon(Icons.account_circle),
            const SizedBox(width: 8),
            Expanded(child: Text(acc?.email ?? 'Account Google', overflow: TextOverflow.ellipsis)),
            TextButton(onPressed: _signOut, child: const Text('Esci')),
          ]),
          const SizedBox(height: 8),
          if (_waiting)
            Card(
              child: Padding(
                padding: const EdgeInsets.all(16),
                child: Column(children: [
                  const LinearProgressIndicator(),
                  const SizedBox(height: 12),
                  const Text(
                    'Scegli le foto e i video nella schermata di Google Foto, poi torna qui.',
                    textAlign: TextAlign.center,
                  ),
                  TextButton(onPressed: _cancelPick, child: const Text('Annulla')),
                ]),
              ),
            )
          else if (!_job.isActive)
            FilledButton.icon(
              onPressed: _busy ? null : _pick,
              icon: const Icon(Icons.photo_library),
              label: Text(_items.isEmpty ? 'Scegli foto e video' : 'Cambia selezione'),
            ),
        ],
        if (_error != null) ...[
          const SizedBox(height: 12),
          Container(
            padding: const EdgeInsets.all(12),
            decoration: BoxDecoration(color: scheme.errorContainer, borderRadius: BorderRadius.circular(8)),
            child: Text(_error!, style: TextStyle(color: scheme.onErrorContainer)),
          ),
        ],
        if (_items.isNotEmpty && !_job.isActive && _job.phase == ImportPhase.idle) ..._pickedSection(),
        if (_job.phase != ImportPhase.idle) ..._jobSection(scheme),
        const SizedBox(height: 24),
        const _LimitsNote(),
      ],
    );
  }

  List<Widget> _pickedSection() {
    final photos = _items.where((i) => !i.isVideo).length;
    final videos = _items.length - photos;
    return [
      const SizedBox(height: 16),
      Text('Selezionati: $photos foto, $videos video', style: Theme.of(context).textTheme.titleSmall),
      const SizedBox(height: 8),
      SizedBox(
        height: 72,
        child: ListView(
          scrollDirection: Axis.horizontal,
          children: [
            for (final i in _items.take(12))
              Padding(
                padding: const EdgeInsets.only(right: 4),
                child: ClipRRect(
                  borderRadius: BorderRadius.circular(6),
                  child: Image.network(
                    '${i.baseUrl}=w160-h160-c',
                    headers: _headers,
                    width: 72,
                    height: 72,
                    fit: BoxFit.cover,
                    errorBuilder: (_, __, ___) => const SizedBox(width: 72, height: 72, child: Icon(Icons.image)),
                  ),
                ),
              ),
          ],
        ),
      ),
      const SizedBox(height: 12),
      FilledButton.icon(
        onPressed: _startImport,
        icon: const Icon(Icons.cloud_download_outlined),
        label: Text('Carica ${_items.length} elementi sul PC'),
      ),
    ];
  }

  List<Widget> _jobSection(ColorScheme scheme) {
    final j = _job;
    final queue = UploadQueue.instance;
    final failed = queue.tasks.where((t) => t.state == TaskState.failed && t.clientKey.startsWith('gp:')).length;
    final String title;
    switch (j.phase) {
      case ImportPhase.checking:
        title = 'Controllo cosa è già sul PC…';
      case ImportPhase.transferring:
        title = 'Scarico da Google Foto e carico sul PC…';
      case ImportPhase.finished:
        title = queue.pendingCount > 0 ? 'Carico sul PC…' : 'Trasferimento terminato';
      case ImportPhase.cancelled:
        title = 'Interrotto';
      case ImportPhase.failed:
        title = 'Interrotto per un errore';
      case ImportPhase.idle:
        title = '';
    }
    return [
      const SizedBox(height: 16),
      Card(
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(title, style: Theme.of(context).textTheme.titleSmall),
              const SizedBox(height: 8),
              if (j.isActive || queue.pendingCount > 0)
                LinearProgressIndicator(value: j.total > 0 ? (j.alreadyOnPc + j.handedOver) / j.total : null),
              const SizedBox(height: 8),
              Text('Già sul PC: ${j.alreadyOnPc} · Consegnati alla coda: ${j.handedOver} · Totale: ${j.total}'),
              if (failed > 0 || j.failedNames.isNotEmpty)
                Text('Con errori: ${failed + j.failedNames.length} (vedi «Trasferimenti»)',
                    style: TextStyle(color: scheme.error)),
              if (j.error != null) Text(j.error!, style: TextStyle(color: scheme.error)),
              if (j.isActive)
                TextButton(onPressed: j.cancel, child: const Text('Interrompi')),
              if (!j.isActive && queue.pendingCount == 0)
                TextButton(onPressed: _verifyResult, child: const Text('Verifica con il PC')),
            ],
          ),
        ),
      ),
      if (_verified > 0) ...[
        const SizedBox(height: 12),
        Card(
          color: scheme.secondaryContainer,
          child: Padding(
            padding: const EdgeInsets.all(16),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text('$_verified elementi sono al sicuro sul PC',
                    style: Theme.of(context).textTheme.titleSmall),
                const SizedBox(height: 8),
                const Text(
                  'Per liberare spazio su Google Foto devi eliminarli tu nell\'app Google Foto: '
                  'Google non permette alle altre app di cancellare foto dal tuo account.',
                ),
                if (_oldest != null && _newest != null) ...[
                  const SizedBox(height: 8),
                  Text('Questi elementi sono stati scattati tra ${formatDate(_oldest)} e ${formatDate(_newest)}: '
                      'in Google Foto puoi cercarli per data, selezionarli e toccare «Sposta nel cestino».'),
                ],
                const SizedBox(height: 8),
                const Text(
                  'Controlla prima di aver trovato i file sul PC (cartella «Google Foto»). '
                  'Il cestino di Google Foto li conserva per 60 giorni.',
                ),
                const SizedBox(height: 8),
                OutlinedButton.icon(
                  onPressed: () => launchUrl(Uri.parse('https://photos.google.com'), mode: LaunchMode.externalApplication),
                  icon: const Icon(Icons.open_in_new),
                  label: const Text('Apri Google Foto'),
                ),
              ],
            ),
          ),
        ),
      ],
    ];
  }
}

class _LimitsNote extends StatelessWidget {
  const _LimitsNote();

  @override
  Widget build(BuildContext context) {
    return Text(
      'Come funziona: FireShare usa il selettore ufficiale di Google Foto. Vedi solo ciò che scegli '
      'tu, e l\'app non può cancellare nulla dal tuo account Google.',
      style: Theme.of(context).textTheme.bodySmall,
    );
  }
}
