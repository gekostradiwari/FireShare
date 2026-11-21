import 'package:flutter/material.dart';
import 'package:photo_manager/photo_manager.dart';

import '../../core/backup/auto_backup.dart';
import '../../core/transfer/upload_queue.dart';
import '../../core/util/format.dart';

/// Impostazioni del backup automatico delle cartelle di foto e video.
class BackupPage extends StatefulWidget {
  const BackupPage({super.key});

  @override
  State<BackupPage> createState() => _BackupPageState();
}

class _BackupPageState extends State<BackupPage> {
  final _backup = AutoBackup.instance;
  List<AssetPathEntity>? _albums;
  Map<String, int> _counts = {};
  bool _loadingAlbums = false;

  @override
  void initState() {
    super.initState();
    _backup.addListener(_refresh);
    _loadAlbums();
  }

  @override
  void dispose() {
    _backup.removeListener(_refresh);
    super.dispose();
  }

  void _refresh() {
    if (mounted) setState(() {});
  }

  Future<void> _loadAlbums() async {
    setState(() => _loadingAlbums = true);
    try {
      final list = await _backup.listAlbums();
      final counts = <String, int>{};
      for (final a in list) {
        counts[a.id] = await a.assetCountAsync;
      }
      list.sort((a, b) => (counts[b.id] ?? 0).compareTo(counts[a.id] ?? 0));
      if (!mounted) return;
      setState(() {
        _albums = list;
        _counts = counts;
      });
    } catch (_) {
      if (mounted) setState(() => _albums = []);
    } finally {
      if (mounted) setState(() => _loadingAlbums = false);
    }
  }

  Future<void> _toggle(bool on) async {
    if (!on) {
      await _backup.disable();
      return;
    }
    final perm = await PhotoManager.requestPermissionExtend();
    if (!perm.hasAccess) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Serve il permesso di leggere la galleria.')),
      );
      return;
    }
    if (!mounted) return;
    final existing = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Cosa copiare sul PC?'),
        content: const Text(
          'Puoi copiare anche tutto ciò che c\'è già nelle cartelle scelte, oppure '
          'solo le foto e i video che scatti da adesso in poi.\n\n'
          'Il PC riconosce ciò che ha già ricevuto: niente viene caricato due volte.',
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Annulla')),
          OutlinedButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Solo i nuovi')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Anche i già presenti')),
        ],
      ),
    );
    if (existing == null) return;
    await _backup.enable(includeExisting: existing);
  }

  Future<void> _toggleAlbum(String id, bool on) async {
    await _backup.update((c) {
      if (on) {
        if (!c.albumIds.contains(id)) c.albumIds.add(id);
      } else {
        c.albumIds.remove(id);
      }
    });
  }

  Future<void> _runNow() async {
    final r = await _backup.scan();
    if (!mounted) return;
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(r.message.isEmpty ? 'Fatto.' : r.message)));
  }

  @override
  Widget build(BuildContext context) {
    final cfg = _backup.config;
    final scheme = Theme.of(context).colorScheme;
    return Scaffold(
      appBar: AppBar(title: const Text('Backup automatico')),
      body: ListView(
        children: [
          SwitchListTile(
            secondary: const Icon(Icons.backup_outlined),
            title: const Text('Backup automatico delle foto e dei video'),
            subtitle: Text(cfg.enabled
                ? 'Attivo: le novità vengono copiate sul PC senza che tu faccia nulla.'
                : 'Spento'),
            value: cfg.enabled,
            onChanged: _toggle,
          ),
          if (cfg.enabled) ...[
            const Divider(),
            SwitchListTile(
              secondary: const Icon(Icons.wifi),
              title: const Text('Solo con il Wi-Fi'),
              subtitle: const Text('Evita di consumare i dati del piano telefonico.'),
              value: cfg.wifiOnly,
              onChanged: (v) => _backup.update((c) => c.wifiOnly = v),
            ),
            SwitchListTile(
              secondary: const Icon(Icons.battery_charging_full),
              title: const Text('Solo in carica (in background)'),
              subtitle: const Text('Vale per i controlli periodici, non quando apri l\'app.'),
              value: cfg.chargingOnly,
              onChanged: (v) => _backup.update((c) => c.chargingOnly = v),
            ),
            const Divider(),
            const ListTile(
              title: Text('Cartelle da copiare'),
              subtitle: Text('Se non ne scegli nessuna viene copiata tutta la galleria.'),
            ),
            if (_loadingAlbums && _albums == null)
              const Padding(padding: EdgeInsets.all(24), child: Center(child: CircularProgressIndicator()))
            else if ((_albums ?? const []).isEmpty)
              const ListTile(title: Text('Nessuna cartella trovata.'))
            else
              for (final a in _albums!)
                CheckboxListTile(
                  secondary: const Icon(Icons.folder_outlined),
                  title: Text(a.name),
                  subtitle: Text('${_counts[a.id] ?? 0} elementi'),
                  value: cfg.albumIds.contains(a.id),
                  onChanged: (v) => _toggleAlbum(a.id, v ?? false),
                ),
            const Divider(),
            ListTile(
              leading: _backup.scanning
                  ? const SizedBox(width: 24, height: 24, child: CircularProgressIndicator(strokeWidth: 2))
                  : const Icon(Icons.sync),
              title: const Text('Controlla ora'),
              subtitle: Text(cfg.lastRun == null
                  ? 'Nessun controllo ancora eseguito.'
                  : 'Ultimo controllo: ${formatDate(cfg.lastRun)}'
                      '${cfg.lastResult.isEmpty ? '' : ' · ${cfg.lastResult}'}'),
              onTap: _backup.scanning ? null : _runNow,
            ),
            ListenableBuilder(
              listenable: UploadQueue.instance,
              builder: (context, _) {
                final n = UploadQueue.instance.pendingCount;
                return n == 0
                    ? const SizedBox.shrink()
                    : ListTile(
                        leading: const Icon(Icons.cloud_upload_outlined),
                        title: Text('$n elementi in coda'),
                        subtitle: const Text('Segui l\'avanzamento in «Trasferimenti».'),
                      );
              },
            ),
            Padding(
              padding: const EdgeInsets.all(16),
              child: Text(
                'Android decide quando eseguire i controlli in background (circa ogni 6 ore) e '
                'può rimandarli se la batteria è in risparmio energetico. Per non perdere nulla, '
                'apri FireShare di tanto in tanto: al ritorno nell\'app il controllo parte subito.\n\n'
                'Le foto vengono copiate, non spostate: restano anche sul telefono. '
                'Per liberare spazio usa «Carica e libera» nella scheda Telefono.',
                style: TextStyle(fontSize: 12, color: scheme.onSurfaceVariant),
              ),
            ),
          ],
        ],
      ),
    );
  }
}
