import 'dart:async';
import 'dart:typed_data';

import 'package:flutter/material.dart';

import '../../core/api/fireshare_client.dart';
import '../../core/storage/connection_manager.dart';
import '../../core/transfer/upload_queue.dart';
import 'cloud_viewer.dart';

/// Piccola cache in memoria delle miniature già scaricate.
class ThumbCache {
  ThumbCache._();
  static final _map = <String, Uint8List>{}; // letterale = LinkedHashMap (ordine di inserimento)
  static const _max = 400;

  static String keyOf(MediaItem m, int size) =>
      '${m.path}|$size|${m.modifiedAt?.millisecondsSinceEpoch ?? 0}';

  static Uint8List? get(String key) {
    final v = _map.remove(key);
    if (v != null) _map[key] = v; // la rende "recente"
    return v;
  }

  static void put(String key, Uint8List bytes) {
    _map[key] = bytes;
    while (_map.length > _max) {
      _map.remove(_map.keys.first);
    }
  }

  static void clear() => _map.clear();
}

/// Miniatura di un elemento del cloud, prodotta e messa in cache dal PC.
class CloudThumb extends StatefulWidget {
  const CloudThumb({super.key, required this.item, this.size = 320, this.fit = BoxFit.cover});
  final MediaItem item;
  final int size;
  final BoxFit fit;

  @override
  State<CloudThumb> createState() => _CloudThumbState();
}

class _CloudThumbState extends State<CloudThumb> {
  Uint8List? _bytes;
  bool _failed = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final key = ThumbCache.keyOf(widget.item, widget.size);
    final hit = ThumbCache.get(key);
    if (hit != null) {
      _bytes = hit;
      return;
    }
    try {
      final data = await ConnectionManager.instance.client.thumb(widget.item.path, size: widget.size);
      ThumbCache.put(key, data);
      if (mounted) setState(() => _bytes = data);
    } catch (_) {
      if (mounted) setState(() => _failed = true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final b = _bytes;
    if (b != null) return Image.memory(b, fit: widget.fit, gaplessPlayback: true);
    return ColoredBox(
      color: scheme.surfaceVariant,
      child: _failed
          ? Center(
              child: Icon(widget.item.isVideo ? Icons.movie_outlined : Icons.broken_image_outlined,
                  color: scheme.onSurfaceVariant),
            )
          : null,
    );
  }
}

/// Galleria del cloud: le foto e i video che sono sul PC, più recenti per primi.
class CloudTab extends StatefulWidget {
  const CloudTab({super.key});

  @override
  State<CloudTab> createState() => _CloudTabState();
}

class _CloudTabState extends State<CloudTab> with AutomaticKeepAliveClientMixin {
  static const _pageSize = 120;

  final List<MediaItem> _items = [];
  int _total = 0;
  bool _loading = false;
  bool _initialDone = false;
  String? _error;
  String _kind = ''; // '', 'image', 'video'
  final Set<String> _selected = {};
  final ScrollController _scroll = ScrollController();
  int _doneSeen = 0;

  @override
  bool get wantKeepAlive => true;

  @override
  void initState() {
    super.initState();
    _scroll.addListener(() {
      if (_scroll.position.pixels > _scroll.position.maxScrollExtent - 800) _more();
    });
    UploadQueue.instance.addListener(_onQueue);
    _reload();
  }

  @override
  void dispose() {
    UploadQueue.instance.removeListener(_onQueue);
    _scroll.dispose();
    super.dispose();
  }

  void _onQueue() {
    // quando finisce un upload la galleria si aggiorna da sola
    final done = UploadQueue.instance.tasks.where((t) => t.state == TaskState.done).length;
    if (done != _doneSeen) {
      _doneSeen = done;
      if (mounted && _initialDone && UploadQueue.instance.pendingCount == 0) _reload(silent: true);
    }
  }

  bool _reloadAgain = false;

  Future<void> _reload({bool silent = false}) async {
    if (_loading) {
      _reloadAgain = true; // il filtro o i dati sono cambiati mentre si caricava
      return;
    }
    _loading = true;
    if (!silent) setState(() => _error = null);
    try {
      final page = await ConnectionManager.instance.client
          .media(offset: 0, limit: _pageSize, kind: _kind.isEmpty ? null : _kind);
      if (!mounted) return;
      setState(() {
        _items
          ..clear()
          ..addAll(page.items);
        _total = page.total;
        _error = null;
        _initialDone = true;
        _selected.removeWhere((p) => !_items.any((m) => m.path == p));
      });
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } finally {
      _loading = false;
      if (_reloadAgain && mounted) {
        _reloadAgain = false;
        unawaited(_reload(silent: true));
      }
    }
  }

  Future<void> _more() async {
    if (_loading || _items.length >= _total || !_initialDone) return;
    _loading = true;
    try {
      final page = await ConnectionManager.instance.client
          .media(offset: _items.length, limit: _pageSize, kind: _kind.isEmpty ? null : _kind);
      if (!mounted) return;
      setState(() {
        final known = _items.map((m) => m.path).toSet();
        _items.addAll(page.items.where((m) => !known.contains(m.path)));
        _total = page.total;
      });
    } on ApiException {
      // si riprova allo scorrimento successivo
    } finally {
      _loading = false;
    }
  }

  void _snack(String msg) {
    if (!mounted) return;
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(msg)));
  }

  Future<void> _open(int index) async {
    final removed = await Navigator.of(context).push<List<String>>(
      MaterialPageRoute(builder: (_) => CloudViewerPage(items: List.of(_items), initialIndex: index)),
    );
    if (removed != null && removed.isNotEmpty && mounted) {
      setState(() {
        _items.removeWhere((m) => removed.contains(m.path));
        _total = (_total - removed.length).clamp(0, 1 << 31).toInt();
      });
    }
  }

  Future<void> _deleteSelected() async {
    final n = _selected.length;
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Eliminare dal PC?'),
        content: Text(
            '$n ${n == 1 ? 'elemento verrà eliminato' : 'elementi verranno eliminati'} dal PC. '
            'L\'operazione non si può annullare.'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Annulla')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Elimina')),
        ],
      ),
    );
    if (ok != true) return;
    var failed = 0;
    final gone = <String>[];
    for (final path in _selected.toList()) {
      try {
        await ConnectionManager.instance.client.delete(path);
        gone.add(path);
      } on ApiException {
        failed++;
      }
    }
    if (!mounted) return;
    setState(() {
      _items.removeWhere((m) => gone.contains(m.path));
      _selected.removeAll(gone);
      _total = (_total - gone.length).clamp(0, 1 << 31).toInt();
    });
    _snack(failed == 0 ? '${gone.length} eliminati dal PC.' : '${gone.length} eliminati, $failed non riusciti.');
  }

  void _setKind(String k) {
    if (_kind == k) return;
    setState(() {
      _kind = k;
      _selected.clear();
      _items.clear();
      _total = 0;
      _initialDone = false;
    });
    _reload();
  }

  @override
  Widget build(BuildContext context) {
    super.build(context);
    if (_error != null && _items.isEmpty) {
      return Center(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(mainAxisSize: MainAxisSize.min, children: [
            Text(_error!, textAlign: TextAlign.center),
            const SizedBox(height: 12),
            FilledButton(onPressed: _reload, child: const Text('Riprova')),
          ]),
        ),
      );
    }
    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 8, 12, 0),
          child: Row(children: [
            Expanded(
              child: Text(
                _selected.isEmpty ? '$_total elementi sul PC' : '${_selected.length} selezionati',
                style: Theme.of(context).textTheme.titleSmall,
              ),
            ),
            if (_selected.isNotEmpty) ...[
              IconButton(
                tooltip: 'Deseleziona',
                icon: const Icon(Icons.close),
                onPressed: () => setState(_selected.clear),
              ),
              IconButton(
                tooltip: 'Elimina dal PC',
                icon: const Icon(Icons.delete_outline),
                onPressed: _deleteSelected,
              ),
            ] else
              SegmentedButton<String>(
                showSelectedIcon: false,
                style: const ButtonStyle(visualDensity: VisualDensity.compact),
                segments: const [
                  ButtonSegment(value: '', label: Text('Tutti')),
                  ButtonSegment(value: 'image', icon: Icon(Icons.image_outlined)),
                  ButtonSegment(value: 'video', icon: Icon(Icons.videocam_outlined)),
                ],
                selected: {_kind},
                onSelectionChanged: (s) => _setKind(s.first),
              ),
          ]),
        ),
        Expanded(
          child: RefreshIndicator(
            onRefresh: () => _reload(),
            child: !_initialDone
                ? ListView(children: const [
                    SizedBox(height: 160),
                    Center(child: CircularProgressIndicator()),
                  ])
                : _items.isEmpty
                    ? ListView(children: const [
                        SizedBox(height: 120),
                        Center(
                          child: Padding(
                            padding: EdgeInsets.symmetric(horizontal: 32),
                            child: Text(
                              'Ancora nessuna foto o video sul PC.\n'
                              'Caricane dalla scheda «Telefono» o attiva il backup automatico.',
                              textAlign: TextAlign.center,
                            ),
                          ),
                        ),
                      ])
                    : GridView.builder(
                        controller: _scroll,
                        physics: const AlwaysScrollableScrollPhysics(),
                        padding: const EdgeInsets.all(2),
                        gridDelegate: const SliverGridDelegateWithFixedCrossAxisCount(
                          crossAxisCount: 4,
                          mainAxisSpacing: 2,
                          crossAxisSpacing: 2,
                        ),
                        itemCount: _items.length,
                        itemBuilder: (context, i) {
                          final m = _items[i];
                          final sel = _selected.contains(m.path);
                          return GestureDetector(
                            onTap: () {
                              if (_selected.isEmpty) {
                                _open(i);
                              } else {
                                setState(() => sel ? _selected.remove(m.path) : _selected.add(m.path));
                              }
                            },
                            onLongPress: () => setState(() => sel ? _selected.remove(m.path) : _selected.add(m.path)),
                            child: Stack(
                              fit: StackFit.expand,
                              children: [
                                CloudThumb(key: ValueKey(m.path), item: m, size: 160),
                                if (m.isVideo)
                                  const Positioned(
                                    left: 4,
                                    bottom: 4,
                                    child: Icon(Icons.videocam, size: 18, color: Colors.white),
                                  ),
                                if (sel) ...[
                                  ColoredBox(color: Theme.of(context).colorScheme.primary.withOpacity(0.35)),
                                  const Positioned(
                                    top: 4,
                                    left: 4,
                                    child: Icon(Icons.check_circle, color: Colors.white),
                                  ),
                                ],
                              ],
                            ),
                          );
                        },
                      ),
          ),
        ),
      ],
    );
  }
}
