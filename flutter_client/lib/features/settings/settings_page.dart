import 'package:flutter/material.dart';

import '../../core/api/fireshare_client.dart';
import '../../core/backup/auto_backup.dart';
import '../../core/storage/connection_manager.dart';
import '../../core/transfer/upload_queue.dart';
import '../../core/util/format.dart';
import '../backup/backup_page.dart';
import '../files/shares_page.dart';
import '../../core/ui/app_logo_title.dart';

class SettingsPage extends StatefulWidget {
  const SettingsPage({super.key});

  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage> {
  String _status = 'Verifica in corso…';
  Color? _statusColor;
  String _maxFile = '';
  Map<String, UrlProbe> _probes = {};
  bool _testing = false;

  @override
  void initState() {
    super.initState();
    ConnectionManager.instance.addListener(_onConnection);
    AutoBackup.instance.addListener(_onConnection);
    _check();
  }

  @override
  void dispose() {
    ConnectionManager.instance.removeListener(_onConnection);
    AutoBackup.instance.removeListener(_onConnection);
    super.dispose();
  }

  void _onConnection() {
    if (mounted) setState(() {});
  }

  Future<void> _check() async {
    setState(() {
      _status = 'Verifica in corso…';
      _statusColor = null;
    });
    try {
      final info = await ConnectionManager.instance.client.info();
      final max = (info['maxFileSize'] as num?)?.toInt() ?? 0;
      // il PC comunica i suoi indirizzi aggiornati (rete di casa, DuckDNS, IP pubblico…)
      await ConnectionManager.instance
          .mergeUrls((info['urls'] as List<dynamic>? ?? const []).whereType<String>());
      if (!mounted) return;
      setState(() {
        _status = 'Connesso a ${info['name']} (versione ${info['version']})';
        _statusColor = Colors.green;
        _maxFile = max > 0 ? formatSize(max) : 'nessun limite';
      });
    } on ApiException catch (e) {
      if (!mounted) return;
      setState(() {
        _status = e.message;
        _statusColor = Theme.of(context).colorScheme.error;
      });
    }
  }

  Future<void> _testAll() async {
    setState(() {
      _testing = true;
      _probes = {};
    });
    try {
      final results = await ConnectionManager.instance.client.probeAll();
      if (!mounted) return;
      setState(() => _probes = {for (final r in results) r.url: r});
    } finally {
      if (mounted) setState(() => _testing = false);
    }
  }

  Future<void> _removeUrl(String url) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Rimuovere questo indirizzo?'),
        content: Text('$url\n\nSe il PC lo comunica di nuovo, tornerà nell\'elenco.'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Annulla')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Rimuovi')),
        ],
      ),
    );
    if (ok == true) await ConnectionManager.instance.removeUrl(url);
  }

  Future<void> _unpair() async {
    final pending = UploadQueue.instance.pendingCount;
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Scollegare il PC?'),
        content: Text(pending > 0
            ? 'Ci sono $pending trasferimenti in coda: verranno annullati. I file già sul PC restano al loro posto.'
            : 'I file già sul PC restano al loro posto. Per ricollegarti dovrai inquadrare un nuovo QR.'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Annulla')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Scollega')),
        ],
      ),
    );
    if (ok != true) return;
    UploadQueue.instance.cancelAll();
    await AutoBackup.instance.disable();
    await ConnectionManager.instance.unpair();
  }

  static String _kindLabel(NetworkKind k) => switch (k) {
        NetworkKind.home => 'Rete di casa',
        NetworkKind.tailscale => 'Tailscale',
        NetworkKind.internet => 'Internet',
      };

  Widget _urlTile(String url, int urlCount) {
    final probe = _probes[url];
    final scheme = Theme.of(context).colorScheme;
    Widget leading;
    String subtitle = _kindLabel(networkKindOf(url));
    if (_testing) {
      leading = const SizedBox(width: 24, height: 24, child: CircularProgressIndicator(strokeWidth: 2));
    } else if (probe == null) {
      leading = const Icon(Icons.link);
    } else if (probe.ok) {
      leading = const Icon(Icons.check_circle, color: Colors.green);
      subtitle += ' · risponde in ${probe.millis} ms';
    } else {
      leading = Icon(Icons.error_outline, color: scheme.error);
      subtitle += ' · ${probe.error}';
    }
    return ListTile(
      dense: true,
      leading: leading,
      title: Text(url, maxLines: 2, overflow: TextOverflow.ellipsis),
      subtitle: Text(subtitle),
      trailing: urlCount > 1
          ? IconButton(
              icon: const Icon(Icons.close, size: 18),
              tooltip: 'Rimuovi',
              onPressed: () => _removeUrl(url),
            )
          : null,
    );
  }

  @override
  Widget build(BuildContext context) {
    final conn = ConnectionManager.instance.connection;
    if (conn == null) return const SizedBox.shrink();
    final backup = AutoBackup.instance.config;
    final anyOutside = conn.urls.any((u) => networkKindOf(u) != NetworkKind.home);
    return Scaffold(
      appBar: AppBar(title: const AppLogoTitle('Impostazioni')),
      body: ListView(
        children: [
          ListTile(
            leading: Icon(Icons.computer, color: _statusColor),
            title: Text(conn.serverName),
            subtitle: Text(_status),
            trailing: IconButton(icon: const Icon(Icons.refresh), onPressed: _check),
          ),
          if (_maxFile.isNotEmpty)
            ListTile(
              leading: const Icon(Icons.sd_storage_outlined),
              title: const Text('Dimensione massima per file'),
              subtitle: Text(_maxFile),
            ),
          const Divider(),
          ListTile(
            leading: const Icon(Icons.backup_outlined),
            title: const Text('Backup automatico'),
            subtitle: Text(backup.enabled ? 'Attivo' : 'Spento'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () =>
                Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => const BackupPage())),
          ),
          ListTile(
            leading: const Icon(Icons.link),
            title: const Text('Link di condivisione'),
            subtitle: const Text('Quelli attivi, da copiare o revocare'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () =>
                Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => const SharesPage())),
          ),
          const Divider(),
          ListTile(
            title: const Text('Indirizzi del server'),
            subtitle: const Text('Il telefono li prova tutti insieme e usa il primo che risponde.'),
            trailing: FilledButton.tonalIcon(
              onPressed: _testing ? null : _testAll,
              icon: const Icon(Icons.network_check),
              label: const Text('Testa'),
            ),
          ),
          for (final u in conn.urls) _urlTile(u, conn.urls.length),
          if (!anyOutside)
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 4, 16, 8),
              child: Text(
                'Per usare FireShare fuori casa, sul PC apri «Accesso da fuori casa»: '
                'attiva l\'apertura automatica della porta e/o un indirizzo DuckDNS. '
                'Gli indirizzi nuovi arrivano qui da soli, senza rifare l\'accoppiamento.',
                style: TextStyle(fontSize: 12, color: Theme.of(context).colorScheme.onSurfaceVariant),
              ),
            ),
          const Divider(),
          ListTile(
            leading: const Icon(Icons.verified_user_outlined),
            title: const Text('Impronta del certificato (SHA-256)'),
            subtitle: SelectableText(conn.fingerprint, style: const TextStyle(fontFamily: 'monospace', fontSize: 12)),
          ),
          const Padding(
            padding: EdgeInsets.symmetric(horizontal: 16, vertical: 4),
            child: Text(
              'Il telefono si fida solo del certificato con questa impronta: '
              'nessun altro server può spacciarsi per il tuo PC.',
              style: TextStyle(fontSize: 12),
            ),
          ),
          const Divider(),
          ListTile(
            leading: Icon(Icons.link_off, color: Theme.of(context).colorScheme.error),
            title: const Text('Scollega questo PC'),
            onTap: _unpair,
          ),
        ],
      ),
    );
  }
}
