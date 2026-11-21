import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:open_filex/open_filex.dart';
import 'package:photo_manager/photo_manager.dart';
import 'package:share_plus/share_plus.dart';

import '../../core/api/fireshare_client.dart';
import '../../core/storage/connection_manager.dart';
import '../../core/util/format.dart';
import '../files/remote_download.dart';
import '../files/share_dialog.dart';
import 'cloud_tab.dart';

/// Visualizzatore a schermo intero delle foto e dei video sul PC.
/// Chiudendolo restituisce i percorsi eliminati, così la griglia si aggiorna.
class CloudViewerPage extends StatefulWidget {
  const CloudViewerPage({super.key, required this.items, required this.initialIndex});
  final List<MediaItem> items;
  final int initialIndex;

  @override
  State<CloudViewerPage> createState() => _CloudViewerPageState();
}

class _CloudViewerPageState extends State<CloudViewerPage> {
  late final PageController _pages = PageController(initialPage: widget.initialIndex);
  late final List<MediaItem> _items = List.of(widget.items);
  final List<String> _removed = [];
  late int _index = widget.initialIndex;
  bool _zoomed = false;

  @override
  void dispose() {
    _pages.dispose();
    super.dispose();
  }

  MediaItem get _current => _items[_index];

  void _snack(String msg) {
    if (!mounted) return;
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(msg)));
  }

  Future<void> _share() async {
    final m = _current;
    final f = await downloadRemoteToTemp(context, path: m.path, name: m.name, size: m.size);
    if (f != null) await Share.shareXFiles([XFile(f.path)]);
  }

  Future<void> _saveToPhone() async {
    final m = _current;
    final f = await downloadRemoteToTemp(context, path: m.path, name: m.name, size: m.size);
    if (f == null) return;
    try {
      final perm = await PhotoManager.requestPermissionExtend();
      if (!perm.hasAccess) {
        _snack('Serve il permesso di scrivere nella galleria.');
        return;
      }
      if (m.isVideo) {
        await PhotoManager.editor.saveVideo(f, title: m.name);
      } else {
        await PhotoManager.editor.saveImageWithPath(f.path, title: m.name);
      }
      _snack('Salvato nella galleria del telefono.');
    } catch (e) {
      _snack('Impossibile salvare: $e');
    }
  }

  Future<void> _playVideo() async {
    final m = _current;
    final f = await downloadRemoteToTemp(context, path: m.path, name: m.name, size: m.size);
    if (f == null) return;
    final r = await OpenFilex.open(f.path);
    if (r.type != ResultType.done) _snack('Nessuna app per riprodurre questo video.');
  }

  Future<void> _delete() async {
    final m = _current;
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Eliminare dal PC?'),
        content: Text('«${m.name}» verrà eliminato dal PC. L\'operazione non si può annullare.'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Annulla')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Elimina')),
        ],
      ),
    );
    if (ok != true) return;
    try {
      await ConnectionManager.instance.client.delete(m.path);
    } on ApiException catch (e) {
      _snack(e.message);
      return;
    }
    _removed.add(m.path);
    if (_items.length == 1) {
      if (mounted) Navigator.pop(context, _removed);
      return;
    }
    setState(() {
      _items.removeAt(_index);
      if (_index >= _items.length) _index = _items.length - 1;
    });
  }

  @override
  Widget build(BuildContext context) {
    final m = _current;
    return PopScope(
      canPop: false,
      onPopInvoked: (didPop) {
        if (!didPop) Navigator.pop(context, _removed);
      },
      child: Scaffold(
        backgroundColor: Colors.black,
        appBar: AppBar(
          backgroundColor: Colors.black.withOpacity(0.6),
          foregroundColor: Colors.white,
          title: Text(m.name, maxLines: 1, overflow: TextOverflow.ellipsis, style: const TextStyle(fontSize: 16)),
          actions: [
            IconButton(
              tooltip: 'Crea link',
              icon: const Icon(Icons.link),
              onPressed: () => showCreateShareDialog(context, path: m.path, name: m.name),
            ),
            PopupMenuButton<String>(
              onSelected: (v) {
                switch (v) {
                  case 'share':
                    _share();
                  case 'save':
                    _saveToPhone();
                  case 'delete':
                    _delete();
                }
              },
              itemBuilder: (_) => const [
                PopupMenuItem(value: 'share', child: Text('Condividi…')),
                PopupMenuItem(value: 'save', child: Text('Salva sul telefono')),
                PopupMenuItem(value: 'delete', child: Text('Elimina dal PC')),
              ],
            ),
          ],
        ),
        extendBodyBehindAppBar: true,
        body: PageView.builder(
          controller: _pages,
          physics: _zoomed ? const NeverScrollableScrollPhysics() : const PageScrollPhysics(),
          itemCount: _items.length,
          onPageChanged: (i) => setState(() {
            _index = i;
            _zoomed = false;
          }),
          itemBuilder: (context, i) => _ViewerPage(
            key: ValueKey(_items[i].path),
            item: _items[i],
            onPlay: _playVideo,
            onZoomChanged: (z) {
              if (z != _zoomed && mounted) setState(() => _zoomed = z);
            },
          ),
        ),
        bottomNavigationBar: BottomAppBar(
          color: Colors.black.withOpacity(0.6),
          height: 48,
          child: Center(
            child: Text(
              '${_index + 1} di ${_items.length} · ${formatSize(m.size)} · ${formatDate(m.modifiedAt)}',
              style: const TextStyle(color: Colors.white70, fontSize: 12),
            ),
          ),
        ),
      ),
    );
  }
}

class _ViewerPage extends StatefulWidget {
  const _ViewerPage({super.key, required this.item, required this.onPlay, required this.onZoomChanged});
  final MediaItem item;
  final VoidCallback onPlay;
  final ValueChanged<bool> onZoomChanged;

  @override
  State<_ViewerPage> createState() => _ViewerPageState();
}

class _ViewerPageState extends State<_ViewerPage> {
  static const _fullMaxBytes = 25 << 20;

  final _transform = TransformationController();
  Uint8List? _full;
  String? _fullError;

  @override
  void initState() {
    super.initState();
    _transform.addListener(() => widget.onZoomChanged(_transform.value.getMaxScaleOnAxis() > 1.05));
    if (!widget.item.isVideo) _loadFull();
  }

  @override
  void dispose() {
    _transform.dispose();
    super.dispose();
  }

  Future<void> _loadFull() async {
    final m = widget.item;
    if (m.size > _fullMaxBytes) return; // resta la miniatura grande
    try {
      final data = await ConnectionManager.instance.client.content(m.path, maxBytes: _fullMaxBytes);
      if (mounted) setState(() => _full = data);
    } on ApiException catch (e) {
      if (mounted) setState(() => _fullError = e.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    final m = widget.item;
    final preview = CloudThumb(item: m, size: 640, fit: BoxFit.contain);
    if (m.isVideo) {
      return Stack(
        fit: StackFit.expand,
        children: [
          preview,
          Center(
            child: IconButton.filled(
              iconSize: 56,
              icon: const Icon(Icons.play_arrow),
              tooltip: 'Riproduci',
              onPressed: widget.onPlay,
            ),
          ),
        ],
      );
    }
    final full = _full;
    return InteractiveViewer(
      transformationController: _transform,
      minScale: 1,
      maxScale: 6,
      child: Stack(
        fit: StackFit.expand,
        children: [
          if (full == null) preview,
          if (full != null)
            Image.memory(
              full,
              fit: BoxFit.contain,
              gaplessPlayback: true,
              errorBuilder: (_, __, ___) => preview, // formati che Flutter non decodifica (es. HEIC)
            ),
          if (full == null && _fullError == null && m.size <= _fullMaxBytes)
            const Positioned(
              right: 12,
              bottom: 64,
              child: SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2)),
            ),
        ],
      ),
    );
  }
}
