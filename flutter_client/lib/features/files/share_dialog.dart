import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:share_plus/share_plus.dart';

import '../../core/api/fireshare_client.dart';
import '../../core/storage/connection_manager.dart';

/// Crea un link temporaneo per scaricare [path] anche da chi non ha FireShare.
Future<void> showCreateShareDialog(
  BuildContext context, {
  required String path,
  required String name,
}) {
  return showDialog<void>(
    context: context,
    builder: (_) => _CreateShareDialog(path: path, name: name),
  );
}

class _CreateShareDialog extends StatefulWidget {
  const _CreateShareDialog({required this.path, required this.name});
  final String path;
  final String name;

  @override
  State<_CreateShareDialog> createState() => _CreateShareDialogState();
}

class _CreateShareDialogState extends State<_CreateShareDialog> {
  static const _durations = <String, Duration>{
    '1 ora': Duration(hours: 1),
    '24 ore': Duration(hours: 24),
    '7 giorni': Duration(days: 7),
    '30 giorni': Duration(days: 30),
  };
  static const _limits = <String, int>{
    'Illimitati': 0,
    '1 download': 1,
    '5 download': 5,
    '10 download': 10,
  };

  String _duration = '24 ore';
  String _limit = 'Illimitati';
  final _password = TextEditingController();
  bool _busy = false;
  String? _error;
  ShareLink? _link;

  @override
  void dispose() {
    _password.dispose();
    super.dispose();
  }

  Future<void> _create() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final link = await ConnectionManager.instance.client.createShare(
        widget.path,
        expiresIn: _durations[_duration]!,
        maxDownloads: _limits[_limit]!,
        password: _password.text.isEmpty ? null : _password.text,
      );
      if (mounted) setState(() => _link = link);
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final link = _link;
    if (link != null) return _result(context, link);
    return AlertDialog(
      title: const Text('Condividi con un link'),
      content: SingleChildScrollView(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(widget.name, maxLines: 2, overflow: TextOverflow.ellipsis,
                style: Theme.of(context).textTheme.titleSmall),
            const SizedBox(height: 16),
            DropdownButtonFormField<String>(
              value: _duration,
              decoration: const InputDecoration(labelText: 'Il link scade dopo'),
              items: [for (final k in _durations.keys) DropdownMenuItem(value: k, child: Text(k))],
              onChanged: _busy ? null : (v) => setState(() => _duration = v!),
            ),
            const SizedBox(height: 8),
            DropdownButtonFormField<String>(
              value: _limit,
              decoration: const InputDecoration(labelText: 'Download consentiti'),
              items: [for (final k in _limits.keys) DropdownMenuItem(value: k, child: Text(k))],
              onChanged: _busy ? null : (v) => setState(() => _limit = v!),
            ),
            const SizedBox(height: 8),
            TextField(
              controller: _password,
              obscureText: true,
              enabled: !_busy,
              decoration: const InputDecoration(
                labelText: 'Password (facoltativa)',
                helperText: 'Comunicala a parte: non è dentro il link.',
              ),
            ),
            if (_error != null) ...[
              const SizedBox(height: 12),
              Text(_error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
            ],
          ],
        ),
      ),
      actions: [
        TextButton(onPressed: _busy ? null : () => Navigator.pop(context), child: const Text('Annulla')),
        FilledButton(
          onPressed: _busy ? null : _create,
          child: _busy
              ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2))
              : const Text('Crea link'),
        ),
      ],
    );
  }

  Widget _result(BuildContext context, ShareLink link) {
    final scheme = Theme.of(context).colorScheme;
    return AlertDialog(
      title: const Text('Link pronto'),
      content: SingleChildScrollView(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            SelectableText(link.url, style: const TextStyle(fontSize: 13)),
            const SizedBox(height: 12),
            Text('Scade $_duration dopo la creazione · $_limit'
                '${link.hasPassword ? ' · protetto da password' : ''}'),
            if (!link.trustedUrl) ...[
              const SizedBox(height: 12),
              Container(
                padding: const EdgeInsets.all(10),
                decoration: BoxDecoration(
                  color: scheme.errorContainer,
                  borderRadius: BorderRadius.circular(8),
                ),
                child: Text(
                  'Questo link usa il certificato autofirmato del tuo PC: chi lo apre vedrà un '
                  'avviso di sicurezza del browser e dovrà confermare per procedere. '
                  'Per evitarlo, nel programma sul PC imposta un indirizzo con certificato valido '
                  '(campo «Indirizzo base dei link»).',
                  style: TextStyle(fontSize: 12, color: scheme.onErrorContainer),
                ),
              ),
            ],
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () async {
            await Clipboard.setData(ClipboardData(text: link.url));
            if (context.mounted) {
              ScaffoldMessenger.of(context)
                ..hideCurrentSnackBar()
                ..showSnackBar(const SnackBar(content: Text('Link copiato')));
            }
          },
          child: const Text('Copia'),
        ),
        TextButton(
          onPressed: () => Share.share('${widget.name}: ${link.url}'),
          child: const Text('Condividi…'),
        ),
        FilledButton(onPressed: () => Navigator.pop(context), child: const Text('Chiudi')),
      ],
    );
  }
}
