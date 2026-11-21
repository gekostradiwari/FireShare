import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:photo_manager/photo_manager.dart';

import '../../core/api/fireshare_client.dart';
import '../../core/storage/connection_manager.dart';
import '../../core/transfer/upload_queue.dart';

const _galleryDir = 'Foto telefono';

String _keyOf(AssetEntity a) => assetKey(a);

/// Galleria del telefono: scegli cosa caricare sul PC e, solo dopo che il PC ha
/// confermato (con verifica SHA-256) di averlo ricevuto, eliminalo dal telefono.
class GalleryTab extends StatefulWidget {
  const GalleryTab({super.key});

  @override
  State<GalleryTab> createState() => _GalleryTabState();
}

class _GalleryTabState extends State<GalleryTab> with AutomaticKeepAliveClientMixin {
  static const _pageSize = 120;

  PermissionState? _perm;
  AssetPathEntity? _album;
  final List<AssetEntity> _assets = [];
  int _total = 0;
  int _page = 0;
  bool _loadingPage = false;
  bool _allLoaded = false;
  String? _error;

  final Set<String> _selected = {};
  final Set<String> _uploaded = {}; // chiavi confermate dal PC
  final ScrollController _scroll = ScrollController();

  /// Elementi per cui l'utente ha chiesto «Carica e libera spazio».
  List<AssetEntity>? _cleanupAfterUpload;

  @override
  bool get wantKeepAlive => true;

  @override
  void initState() {
    super.initState();
    _scroll.addListener(() {
      if (_scroll.position.pixels > _scroll.position.maxScrollExtent - 600) _loadMore();
    });
    UploadQueue.instance.addListener(_onQueue);
    _init();
  }

  @override
  void dispose() {
    UploadQueue.instance.removeListener(_onQueue);
    _scroll.dispose();
    super.dispose();
  }

  Future<void> _init() async {
    final perm = await PhotoManager.requestPermissionExtend();
    if (!mounted) return;
    setState(() => _perm = perm);
    if (!perm.hasAccess) return;
    try {
      final paths = await PhotoManager.getAssetPathList(onlyAll: true, type: RequestType.common);
      if (paths.isEmpty) {
        if (mounted) setState(() => _allLoaded = true);
        return;
      }
      _album = paths.first;
      _total = await _album!.assetCountAsync;
      if (mounted) setState(() {});
      await _loadMore();
    } catch (e) {
      if (mounted) setState(() => _error = 'Impossibile leggere la galleria: $e');
    }
  }

  Future<void> _loadMore() async {
    if (_loadingPage || _allLoaded || _album == null) return;
    _loadingPage = true;
    try {
      final list = await _album!.getAssetListPaged(page: _page, size: _pageSize);
      _page++;
      if (list.length < _pageSize) _allLoaded = true;
      _assets.addAll(list);
      if (mounted) setState(() {});
      await _refreshUploaded(list);
    } finally {
      _loadingPage = false;
    }
  }

  Future<void> _loadAll() async {
    while (!_allLoaded && mounted) {
      await _loadMore();
    }
  }

  /// Chiede al PC quali di questi elementi ha già ricevuto e verificato.
  Future<void> _refreshUploaded(List<AssetEntity> list) async {
    try {
      final keys = list.map(_keyOf).toList();
      final ok = await ConnectionManager.instance.client.checkComplete(keys);
      if (ok.isNotEmpty && mounted) setState(() => _uploaded.addAll(ok));
    } on ApiException {
      // PC non raggiungibile: lo stato "già caricato" non è disponibile
    }
  }

  void _onQueue() {
    final done = UploadQueue.instance.completedKeys;
    final fresh = done.where((k) => k.startsWith('ph:') && !_uploaded.contains(k)).toList();
    if (fresh.isNotEmpty && mounted) setState(() => _uploaded.addAll(fresh));
    final pending = _cleanupAfterUpload;
    if (pending != null && UploadQueue.instance.pendingCount == 0) {
      _cleanupAfterUpload = null;
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) _deleteFromPhone(pending, askFirst: true);
      });
    }
  }

  // --- selezione --------------------------------------------------------------------

  Future<void> _selectMode(String mode) async {
    if (mode == 'none') {
      setState(_selected.clear);
      return;
    }
    await _withBusyDialog('Leggo la galleria…', () async {
      await _loadAll();
      await _refreshUploaded(_assets);
    });
    if (!mounted) return;
    setState(() {
      _selected
        ..clear()
        ..addAll(_assets.where((a) {
          final up = _uploaded.contains(_keyOf(a));
          return mode == 'all' || (mode == 'new' && !up) || (mode == 'uploaded' && up);
        }).map((a) => a.id));
    });
  }

  Future<void> _withBusyDialog(String text, Future<void> Function() job) async {
    showDialog<void>(
      context: context,
      barrierDismissible: false,
      builder: (_) => AlertDialog(
        content: Row(children: [
          const CircularProgressIndicator(),
          const SizedBox(width: 20),
          Expanded(child: Text(text)),
        ]),
      ),
    );
    try {
      await job();
    } finally {
      if (mounted) Navigator.of(context, rootNavigator: true).pop();
    }
  }

  List<AssetEntity> get _selectedAssets => _assets.where((a) => _selected.contains(a.id)).toList();

  // --- azioni -----------------------------------------------------------------------

  void _snack(String msg) {
    if (!mounted) return;
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(msg)));
  }

  void _enqueue(List<AssetEntity> assets) {
    UploadQueue.instance.enqueueAssets(
      assets
          .where((a) => !_uploaded.contains(_keyOf(a)))
          .map((a) => (assetId: a.id, clientKey: _keyOf(a), created: a.createDateTime))
          .toList(),
      dir: _galleryDir,
    );
  }

  Future<void> _upload({required bool thenFreeSpace}) async {
    final sel = _selectedAssets;
    final todo = sel.where((a) => !_uploaded.contains(_keyOf(a))).toList();
    if (todo.isEmpty) {
      _snack('Tutto ciò che hai selezionato è già sul PC.');
      return;
    }
    if (thenFreeSpace) {
      final ok = await showDialog<bool>(
        context: context,
        builder: (ctx) => AlertDialog(
          title: const Text('Carica e libera spazio'),
          content: Text(
              'Verranno caricati ${todo.length} elementi sul PC. Quando il PC avrà confermato '
              'di averli ricevuti e verificati, l\'app ti chiederà di eliminarli dal telefono.\n\n'
              'Non verrà eliminato nulla che il PC non abbia confermato.'),
          actions: [
            TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Annulla')),
            FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Avvia')),
          ],
        ),
      );
      if (ok != true) return;
      _cleanupAfterUpload = sel;
    }
    _enqueue(todo);
    setState(_selected.clear);
    _snack('${todo.length} elementi in coda: segui l\'avanzamento in «Trasferimenti».');
  }

  /// Elimina dal telefono SOLO gli elementi che il PC conferma di avere.
  Future<void> _deleteFromPhone(List<AssetEntity> assets, {bool askFirst = false}) async {
    if (assets.isEmpty) return;
    Set<String> confirmed;
    try {
      confirmed = await ConnectionManager.instance.client.checkComplete(assets.map(_keyOf));
    } on ApiException catch (e) {
      _snack('Non riesco a verificare con il PC: ${e.message}');
      return;
    }
    final safe = assets.where((a) => confirmed.contains(_keyOf(a))).toList();
    final missing = assets.length - safe.length;
    if (safe.isEmpty) {
      _snack('Il PC non ha ancora confermato nessuno degli elementi selezionati: non elimino nulla.');
      return;
    }
    if (!mounted) return;
    final go = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Eliminare dal telefono?'),
        content: Text(
          '${safe.length} elementi sono al sicuro sul PC (ricezione verificata con SHA-256).'
          '${missing > 0 ? '\n\n$missing elementi NON sono stati confermati dal PC e verranno lasciati sul telefono.' : ''}'
          '\n\nAndroid ti mostrerà una conferma di sistema. '
          'Attenzione: se il backup di Google Foto è attivo, eliminare dal telefono può rimuovere '
          'la copia anche dal tuo account Google (controlla nell\'app Google Foto).',
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Non ora')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('Elimina dal telefono')),
        ],
      ),
    );
    if (go != true) return;
    final deleted = await PhotoManager.editor.deleteWithIds(safe.map((a) => a.id).toList());
    if (!mounted) return;
    setState(() {
      _assets.removeWhere((a) => deleted.contains(a.id));
      _selected.removeAll(deleted);
      _total = (_total - deleted.length).clamp(0, 1 << 31).toInt();
    });
    _snack(deleted.isEmpty
        ? 'Nessun elemento eliminato.'
        : '${deleted.length} elementi eliminati dal telefono.');
  }

  // --- interfaccia ---------------------------------------------------------------------

  @override
  Widget build(BuildContext context) {
    super.build(context);
    if (_perm == null) return const Center(child: CircularProgressIndicator());
    if (!_perm!.hasAccess) {
      return Center(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(mainAxisSize: MainAxisSize.min, children: [
            const Text('Per caricare foto e video serve l\'accesso alla galleria.', textAlign: TextAlign.center),
            const SizedBox(height: 12),
            const FilledButton(onPressed: PhotoManager.openSetting, child: Text('Apri le impostazioni')),
            TextButton(onPressed: _init, child: const Text('Riprova')),
          ]),
        ),
      );
    }
    if (_error != null) return Center(child: Text(_error!));

    final uploadedCount = _uploaded.length;
    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 8, 4, 0),
          child: Row(children: [
            Expanded(
              child: Text(
                _selected.isEmpty
                    ? '$_total elementi · $uploadedCount già sul PC'
                    : '${_selected.length} selezionati',
                style: Theme.of(context).textTheme.titleSmall,
              ),
            ),
            PopupMenuButton<String>(
              icon: const Icon(Icons.checklist),
              tooltip: 'Seleziona',
              onSelected: _selectMode,
              itemBuilder: (_) => const [
                PopupMenuItem(value: 'all', child: Text('Seleziona tutto')),
                PopupMenuItem(value: 'new', child: Text('Solo non ancora caricati')),
                PopupMenuItem(value: 'uploaded', child: Text('Solo già sul PC')),
                PopupMenuItem(value: 'none', child: Text('Deseleziona')),
              ],
            ),
          ]),
        ),
        Expanded(
          child: _assets.isEmpty
              ? Center(child: _allLoaded ? const Text('Nessuna foto o video') : const CircularProgressIndicator())
              : GridView.builder(
                  controller: _scroll,
                  padding: const EdgeInsets.all(2),
                  gridDelegate: const SliverGridDelegateWithFixedCrossAxisCount(
                    crossAxisCount: 4,
                    mainAxisSpacing: 2,
                    crossAxisSpacing: 2,
                  ),
                  itemCount: _assets.length,
                  itemBuilder: (context, i) {
                    final a = _assets[i];
                    final sel = _selected.contains(a.id);
                    return GestureDetector(
                      onTap: () => setState(() => sel ? _selected.remove(a.id) : _selected.add(a.id)),
                      child: _Thumb(
                        key: ValueKey(a.id),
                        asset: a,
                        selected: sel,
                        uploaded: _uploaded.contains(_keyOf(a)),
                      ),
                    );
                  },
                ),
        ),
        if (_selected.isNotEmpty)
          SafeArea(
            top: false,
            child: Padding(
              padding: const EdgeInsets.all(8),
              child: Row(children: [
                Expanded(
                  child: OutlinedButton.icon(
                    onPressed: () => _upload(thenFreeSpace: false),
                    icon: const Icon(Icons.cloud_upload_outlined),
                    label: const Text('Carica'),
                  ),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: FilledButton.icon(
                    onPressed: () => _upload(thenFreeSpace: true),
                    icon: const Icon(Icons.auto_delete_outlined),
                    label: const Text('Carica e libera'),
                  ),
                ),
                PopupMenuButton<String>(
                  onSelected: (v) {
                    if (v == 'delete') _deleteFromPhone(_selectedAssets);
                  },
                  itemBuilder: (_) => const [
                    PopupMenuItem(value: 'delete', child: Text('Elimina dal telefono quelli già sul PC')),
                  ],
                ),
              ]),
            ),
          ),
      ],
    );
  }
}

class _Thumb extends StatefulWidget {
  const _Thumb({super.key, required this.asset, required this.selected, required this.uploaded});
  final AssetEntity asset;
  final bool selected;
  final bool uploaded;

  @override
  State<_Thumb> createState() => _ThumbState();
}

class _ThumbState extends State<_Thumb> {
  late final Future<Uint8List?> _data =
      widget.asset.thumbnailDataWithSize(const ThumbnailSize.square(220));

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Stack(
      fit: StackFit.expand,
      children: [
        FutureBuilder<Uint8List?>(
          future: _data,
          builder: (context, snap) => snap.data == null
              ? ColoredBox(color: scheme.surfaceVariant)
              : Image.memory(snap.data!, fit: BoxFit.cover, gaplessPlayback: true),
        ),
        if (widget.asset.type == AssetType.video)
          const Positioned(left: 4, bottom: 4, child: Icon(Icons.videocam, size: 18, color: Colors.white)),
        if (widget.uploaded)
          const Positioned(
            right: 4,
            bottom: 4,
            child: Icon(Icons.cloud_done, size: 18, color: Colors.lightGreenAccent),
          ),
        if (widget.selected) ...[
          ColoredBox(color: scheme.primary.withOpacity(0.35)),
          const Positioned(top: 4, left: 4, child: Icon(Icons.check_circle, color: Colors.white)),
        ],
      ],
    );
  }
}
