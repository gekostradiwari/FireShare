import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:mobile_scanner/mobile_scanner.dart';

import '../../core/api/fireshare_client.dart';
import '../../core/api/pairing_payload.dart';
import '../../core/storage/connection_manager.dart';

/// Schermata iniziale: accoppia il telefono al PC inquadrando il QR di FireShare
/// (oppure incollando il link mostrato dall'app desktop).
class PairingPage extends StatefulWidget {
  const PairingPage({super.key});

  @override
  State<PairingPage> createState() => _PairingPageState();
}

class _PairingPageState extends State<PairingPage> {
  final _scanner = MobileScannerController(formats: const [BarcodeFormat.qrCode]);
  final _linkCtrl = TextEditingController();
  final _nameCtrl = TextEditingController(text: 'Telefono Android');
  bool _busy = false;
  String? _error;
  bool _manual = false;

  @override
  void dispose() {
    _scanner.dispose();
    _linkCtrl.dispose();
    _nameCtrl.dispose();
    super.dispose();
  }

  Future<void> _handle(String raw) async {
    if (_busy) return;
    final payload = PairingPayload.tryParse(raw);
    if (payload == null) {
      setState(() => _error = 'Questo non è un QR di FireShare.');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final name = _nameCtrl.text.trim().isEmpty ? 'Telefono Android' : _nameCtrl.text.trim();
      await ConnectionManager.instance.pair(payload, name);
      // il MaterialApp passa da solo alla schermata principale
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } catch (e) {
      if (mounted) setState(() => _error = 'Errore: $e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _paste() async {
    final data = await Clipboard.getData(Clipboard.kTextPlain);
    if (data?.text != null) _linkCtrl.text = data!.text!;
  }

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Scaffold(
      appBar: AppBar(title: const Text('Collega il tuo PC')),
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.all(16),
          children: [
            Text(
              'Apri FireShare sul PC, premi «Mostra QR di accoppiamento» e inquadralo.',
              style: Theme.of(context).textTheme.bodyLarge,
            ),
            const SizedBox(height: 16),
            TextField(
              controller: _nameCtrl,
              decoration: const InputDecoration(
                labelText: 'Nome di questo telefono',
                border: OutlineInputBorder(),
              ),
            ),
            const SizedBox(height: 16),
            if (!_manual)
              ClipRRect(
                borderRadius: BorderRadius.circular(16),
                child: SizedBox(
                  height: 320,
                  child: MobileScanner(
                    controller: _scanner,
                    onDetect: (capture) {
                      for (final b in capture.barcodes) {
                        final v = b.rawValue;
                        if (v != null) {
                          _handle(v);
                          break;
                        }
                      }
                    },
                    errorBuilder: (context, error, child) => Center(
                      child: Padding(
                        padding: const EdgeInsets.all(16),
                        child: Text(
                          'Fotocamera non disponibile (${error.errorCode.name}). '
                          'Consenti l\'accesso alla fotocamera oppure incolla il link.',
                          textAlign: TextAlign.center,
                        ),
                      ),
                    ),
                  ),
                ),
              ),
            if (_busy) ...[
              const SizedBox(height: 16),
              const LinearProgressIndicator(),
              const SizedBox(height: 8),
              const Center(child: Text('Collegamento in corso…')),
            ],
            if (_error != null) ...[
              const SizedBox(height: 16),
              Container(
                padding: const EdgeInsets.all(12),
                decoration: BoxDecoration(
                  color: scheme.errorContainer,
                  borderRadius: BorderRadius.circular(8),
                ),
                child: Text(_error!, style: TextStyle(color: scheme.onErrorContainer)),
              ),
            ],
            const SizedBox(height: 16),
            TextButton.icon(
              onPressed: () => setState(() => _manual = !_manual),
              icon: Icon(_manual ? Icons.qr_code_scanner : Icons.link),
              label: Text(_manual ? 'Usa la fotocamera' : 'Incolla il link invece del QR'),
            ),
            if (_manual) ...[
              TextField(
                controller: _linkCtrl,
                minLines: 2,
                maxLines: 4,
                decoration: InputDecoration(
                  labelText: 'Link fireshare://pair?…',
                  border: const OutlineInputBorder(),
                  suffixIcon: IconButton(icon: const Icon(Icons.paste), onPressed: _paste),
                ),
              ),
              const SizedBox(height: 8),
              FilledButton(
                onPressed: _busy ? null : () => _handle(_linkCtrl.text),
                child: const Text('Collega'),
              ),
            ],
          ],
        ),
      ),
    );
  }
}
