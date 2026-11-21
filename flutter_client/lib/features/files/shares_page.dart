import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:share_plus/share_plus.dart';

import '../../core/api/fireshare_client.dart';
import '../../core/storage/connection_manager.dart';
import '../../core/util/format.dart';

/// I link di condivisione attivi: copiali, inviali di nuovo o revocali.
class SharesPage extends StatefulWidget {
  const SharesPage({super.key});

  @override
  State<SharesPage> createState() => _SharesPageState();
}

class _SharesPageState extends State<SharesPage> {
  List<ShareLink>? _shares;
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final list = await ConnectionManager.instance.client.listShares();
      list.sort((a, b) => (b.expiresAt ?? DateTime(0)).compareTo(a.expiresAt ?? DateTime(0)));
      if (!mounted) return;
      setState(() {
        _shares = list;
        _error = null;
      });
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    }
  }

  void _snack(String msg) {
    if (!mounted) return;
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(msg)));
  }

  Future<void> _revoke(ShareLink s) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Revocare il link?'),
        content: Text('Il link a «${s.name}» smetterà di funzionare subito. Il file resta sul PC.'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Annulla')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Revoca')),
        ],
      ),
    );
    if (ok != true) return;
    try {
      await ConnectionManager.instance.client.revokeShare(s.id);
      await _load();
    } on ApiException catch (e) {
      _snack(e.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    final shares = _shares;
    return Scaffold(
      appBar: AppBar(title: const Text('Link di condivisione')),
      body: _error != null
          ? Center(child: Padding(padding: const EdgeInsets.all(24), child: Text(_error!)))
          : shares == null
              ? const Center(child: CircularProgressIndicator())
              : RefreshIndicator(
                  onRefresh: _load,
                  child: shares.isEmpty
                      ? ListView(children: const [
                          SizedBox(height: 120),
                          Center(
                            child: Padding(
                              padding: EdgeInsets.symmetric(horizontal: 32),
                              child: Text(
                                'Nessun link attivo.\nApri il menu di un file e scegli «Crea link di condivisione».',
                                textAlign: TextAlign.center,
                              ),
                            ),
                          ),
                        ])
                      : ListView.separated(
                          itemCount: shares.length,
                          separatorBuilder: (_, __) => const Divider(height: 1),
                          itemBuilder: (context, i) {
                            final s = shares[i];
                            final expired = s.expiresAt != null && s.expiresAt!.isBefore(DateTime.now());
                            final left = s.maxDownloads > 0
                                ? '${s.downloads}/${s.maxDownloads} download'
                                : '${s.downloads} download';
                            return ListTile(
                              leading: Icon(s.hasPassword ? Icons.lock_outline : Icons.link,
                                  color: expired ? Theme.of(context).disabledColor : null),
                              title: Text(s.name, maxLines: 1, overflow: TextOverflow.ellipsis),
                              subtitle: Text('${expired ? 'Scaduto' : 'Scade ${formatRemaining(s.expiresAt)}'}'
                                  ' · $left'),
                              trailing: PopupMenuButton<String>(
                                onSelected: (v) async {
                                  if (v == 'copy') {
                                    await Clipboard.setData(ClipboardData(text: s.url));
                                    _snack('Link copiato');
                                  } else if (v == 'share') {
                                    await Share.share('${s.name}: ${s.url}');
                                  } else if (v == 'revoke') {
                                    await _revoke(s);
                                  }
                                },
                                itemBuilder: (_) => [
                                  if (!expired) const PopupMenuItem(value: 'copy', child: Text('Copia link')),
                                  if (!expired) const PopupMenuItem(value: 'share', child: Text('Invia…')),
                                  const PopupMenuItem(value: 'revoke', child: Text('Revoca')),
                                ],
                              ),
                            );
                          },
                        ),
                ),
    );
  }
}
