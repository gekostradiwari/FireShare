import 'dart:io';

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:open_filex/open_filex.dart';
import 'package:path/path.dart' as p;
import 'package:share_plus/share_plus.dart';

import '../../core/api/fireshare_client.dart';
import '../../core/storage/connection_manager.dart';
import '../../core/transfer/upload_queue.dart';
import '../../core/util/format.dart';
import 'remote_download.dart';
import 'share_dialog.dart';
import 'shares_page.dart';
import '../../core/ui/app_logo_title.dart';

/// Esplora lo spazio cloud sul PC: cartelle, download, upload, rinomina, elimina.
class FilesPage extends StatefulWidget {
  const FilesPage({super.key});

  @override
  State<FilesPage> createState() => _FilesPageState();
}

class _FilesPageState extends State<FilesPage> {
  String _path = '';
  List<RemoteEntry> _entries = [];
  bool _loading = true;
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
    UploadQueue.instance.addListener(_onQueue);
  }

  @override
  void dispose() {
    UploadQueue.instance.removeListener(_onQueue);
    super.dispose();
  }

  int _doneSeen = 0;
  void _onQueue() {
    // a ogni upload concluso si aggiorna l'elenco
    final done = UploadQueue.instance.tasks.where((t) => t.state == TaskState.done).length;
    if (done != _doneSeen) {
      _doneSeen = done;
      if (mounted) _load(silent: true);
    }
  }

  Future<void> _load({bool silent = false}) async {
    if (!silent) setState(() => _loading = true);
    try {
      final list = await ConnectionManager.instance.client.list(_path);
      list.sort((a, b) {
        if (a.isDir != b.isDir) return a.isDir ? -1 : 1;
        return a.name.toLowerCase().compareTo(b.name.toLowerCase());
      });
      if (!mounted) return;
      setState(() {
        _entries = list;
        _error = null;
        _loading = false;
      });
    } on ApiException catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e.message;
        _loading = false;
      });
    }
  }

  void _open(String path) {
    _path = path;
    _load();
  }

  String get _parent {
    final i = _path.lastIndexOf('/');
    return i < 0 ? '' : _path.substring(0, i);
  }

  void _snack(String msg) {
    if (!mounted) return;
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(msg)));
  }

  // --- azioni ------------------------------------------------------------------------

  Future<void> _upload() async {
    final res = await FilePicker.platform.pickFiles(allowMultiple: true);
    if (res == null) return;
    final files = res.files
        .where((f) => f.path != null)
        .map((f) => (path: f.path!, name: f.name, clientKey: null as String?, deleteSource: false))
        .toList();
    if (files.isEmpty) return;
    await UploadQueue.instance.enqueueFiles(files, dir: _path);
    _snack('${files.length} file in coda: segui l\'avanzamento in «Trasferimenti».');
  }

  Future<void> _mkdir() async {
    final name = await _askText('Nuova cartella', 'Nome della cartella');
    if (name == null || name.isEmpty) return;
    try {
      await ConnectionManager.instance.client.mkdir(_path.isEmpty ? name : '$_path/$name');
      await _load(silent: true);
    } on ApiException catch (e) {
      _snack(e.message);
    }
  }

  Future<void> _rename(RemoteEntry e) async {
    final name = await _askText('Rinomina', 'Nuovo nome', initial: e.name);
    if (name == null || name.isEmpty || name == e.name) return;
    final to = _path.isEmpty ? name : '$_path/$name';
    try {
      await ConnectionManager.instance.client.move(e.path, to);
      await _load(silent: true);
    } on ApiException catch (err) {
      _snack(err.message);
    }
  }

  Future<void> _delete(RemoteEntry e) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: Text(e.isDir ? 'Eliminare la cartella?' : 'Eliminare il file?'),
        content: Text(
            '«${e.name}» verrà eliminato dal PC${e.isDir ? ' insieme a tutto il suo contenuto' : ''}. '
            'L\'operazione non si può annullare.'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Annulla')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Elimina')),
        ],
      ),
    );
    if (ok != true) return;
    try {
      await ConnectionManager.instance.client.delete(e.path);
      await _load(silent: true);
    } on ApiException catch (err) {
      _snack(err.message);
    }
  }

  Future<String?> _askText(String title, String label, {String initial = ''}) {
    final ctrl = TextEditingController(text: initial);
    return showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: Text(title),
        content: TextField(
          controller: ctrl,
          autofocus: true,
          decoration: InputDecoration(labelText: label),
          onSubmitted: (v) => Navigator.pop(ctx, v.trim()),
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Annulla')),
          FilledButton(onPressed: () => Navigator.pop(ctx, ctrl.text.trim()), child: const Text('OK')),
        ],
      ),
    );
  }

  /// Scarica il file in una cartella temporanea (con ripresa) e restituisce il percorso locale.
  Future<File?> _download(RemoteEntry e) =>
      downloadRemoteToTemp(context, path: e.path, name: e.name, size: e.size);

  Future<void> _openFile(RemoteEntry e) async {
    final f = await _download(e);
    if (f == null) return;
    final r = await OpenFilex.open(f.path);
    if (r.type != ResultType.done) {
      _snack('Nessuna app per aprire questo file: usa «Condividi / salva».');
    }
  }

  Future<void> _shareFile(RemoteEntry e) async {
    final f = await _download(e);
    if (f == null) return;
    await Share.shareXFiles([XFile(f.path)]);
  }

  void _menu(RemoteEntry e) {
    showModalBottomSheet<void>(
      context: context,
      builder: (ctx) => SafeArea(
        child: Wrap(
          children: [
            ListTile(title: Text(e.name, maxLines: 1, overflow: TextOverflow.ellipsis), subtitle: Text(e.isDir ? 'Cartella' : formatSize(e.size))),
            if (!e.isDir)
              ListTile(
                leading: const Icon(Icons.open_in_new),
                title: const Text('Apri'),
                onTap: () {
                  Navigator.pop(ctx);
                  _openFile(e);
                },
              ),
            if (!e.isDir)
              ListTile(
                leading: const Icon(Icons.ios_share),
                title: const Text('Condividi / salva sul telefono'),
                onTap: () {
                  Navigator.pop(ctx);
                  _shareFile(e);
                },
              ),
            if (!e.isDir)
              ListTile(
                leading: const Icon(Icons.link),
                title: const Text('Crea link di condivisione'),
                onTap: () {
                  Navigator.pop(ctx);
                  showCreateShareDialog(context, path: e.path, name: e.name);
                },
              ),
            ListTile(
              leading: const Icon(Icons.drive_file_rename_outline),
              title: const Text('Rinomina'),
              onTap: () {
                Navigator.pop(ctx);
                _rename(e);
              },
            ),
            ListTile(
              leading: const Icon(Icons.delete_outline),
              title: const Text('Elimina dal PC'),
              onTap: () {
                Navigator.pop(ctx);
                _delete(e);
              },
            ),
          ],
        ),
      ),
    );
  }

  IconData _iconFor(RemoteEntry e) {
    if (e.isDir) return Icons.folder;
    final ext = p.extension(e.name).toLowerCase();
    if (const ['.jpg', '.jpeg', '.png', '.gif', '.webp', '.heic', '.bmp'].contains(ext)) return Icons.image_outlined;
    if (const ['.mp4', '.mov', '.mkv', '.avi', '.webm', '.3gp'].contains(ext)) return Icons.movie_outlined;
    if (const ['.mp3', '.m4a', '.wav', '.flac', '.ogg'].contains(ext)) return Icons.audiotrack_outlined;
    if (ext == '.pdf') return Icons.picture_as_pdf_outlined;
    return Icons.insert_drive_file_outlined;
  }

  @override
  Widget build(BuildContext context) {
    final conn = ConnectionManager.instance.connection;
    return PopScope(
      canPop: _path.isEmpty,
      onPopInvoked: (didPop) {
        if (!didPop) _open(_parent);
      },
      child: Scaffold(
        appBar: AppBar(
          title: _path.isEmpty
              ? AppLogoTitle(conn?.serverName ?? 'FireShare')
              : Text(p.basename(_path)),
          leading: _path.isEmpty
              ? null
              : IconButton(icon: const Icon(Icons.arrow_back), onPressed: () => _open(_parent)),
          actions: [
            IconButton(
              icon: const Icon(Icons.link),
              tooltip: 'Link di condivisione',
              onPressed: () => Navigator.of(context)
                  .push(MaterialPageRoute<void>(builder: (_) => const SharesPage())),
            ),
            IconButton(icon: const Icon(Icons.create_new_folder_outlined), tooltip: 'Nuova cartella', onPressed: _mkdir),
            IconButton(icon: const Icon(Icons.refresh), tooltip: 'Aggiorna', onPressed: _load),
          ],
        ),
        floatingActionButton: FloatingActionButton.extended(
          onPressed: _upload,
          icon: const Icon(Icons.upload_file),
          label: const Text('Carica file'),
        ),
        body: _loading
            ? const Center(child: CircularProgressIndicator())
            : _error != null
                ? Center(
                    child: Padding(
                      padding: const EdgeInsets.all(24),
                      child: Column(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          Text(_error!, textAlign: TextAlign.center),
                          const SizedBox(height: 12),
                          FilledButton(onPressed: _load, child: const Text('Riprova')),
                        ],
                      ),
                    ),
                  )
                : RefreshIndicator(
                    onRefresh: _load,
                    child: _entries.isEmpty
                        ? ListView(children: const [
                            SizedBox(height: 120),
                            Center(child: Text('Cartella vuota')),
                          ])
                        : ListView.builder(
                            padding: const EdgeInsets.only(bottom: 88),
                            itemCount: _entries.length,
                            itemBuilder: (context, i) {
                              final e = _entries[i];
                              return ListTile(
                                leading: Icon(_iconFor(e)),
                                title: Text(e.name, maxLines: 1, overflow: TextOverflow.ellipsis),
                                subtitle: Text(e.isDir
                                    ? formatDate(e.modifiedAt)
                                    : '${formatSize(e.size)} · ${formatDate(e.modifiedAt)}'),
                                trailing: IconButton(
                                  icon: const Icon(Icons.more_vert),
                                  onPressed: () => _menu(e),
                                ),
                                onTap: () => e.isDir ? _open(e.path) : _openFile(e),
                                onLongPress: () => _menu(e),
                              );
                            },
                          ),
                  ),
      ),
    );
  }
}
