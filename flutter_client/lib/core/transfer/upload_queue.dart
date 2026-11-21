import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math';

import 'package:crypto/crypto.dart';
import 'package:flutter/foundation.dart';
import 'package:path/path.dart' as p;
import 'package:path_provider/path_provider.dart';
import 'package:photo_manager/photo_manager.dart';

import '../api/fireshare_client.dart';
import '../storage/connection_manager.dart';
import 'background_keeper.dart';

/// Chiave stabile con cui il PC riconosce un elemento della galleria già caricato
/// (cambia solo se l'elemento viene modificato).
String assetKey(AssetEntity a) => 'ph:${a.id}:${a.modifiedDateSecond ?? 0}';

enum TaskState { queued, running, done, failed, cancelled }

/// Un file in coda per essere caricato sul PC.
class UploadTask {
  UploadTask({
    required this.id,
    required this.name,
    required this.dir,
    required this.clientKey,
    this.path,
    this.assetId,
    this.size = 0,
    this.sent = 0,
    this.sha256,
    this.serverId,
    this.modifiedAt,
    this.deleteSource = false,
    this.state = TaskState.queued,
    this.error,
    this.remotePath,
  });

  factory UploadTask.fromJson(Map<String, dynamic> j) => UploadTask(
        id: j['id'] as String,
        name: j['name'] as String? ?? '',
        dir: j['dir'] as String? ?? '',
        clientKey: j['clientKey'] as String,
        path: j['path'] as String?,
        assetId: j['assetId'] as String?,
        size: (j['size'] as num?)?.toInt() ?? 0,
        sent: (j['sent'] as num?)?.toInt() ?? 0,
        sha256: j['sha256'] as String?,
        serverId: j['serverId'] as String?,
        modifiedAt: DateTime.tryParse(j['modifiedAt'] as String? ?? ''),
        deleteSource: j['deleteSource'] as bool? ?? false,
        state: TaskState.values.firstWhere(
          (s) => s.name == j['state'],
          orElse: () => TaskState.queued,
        ),
        error: j['error'] as String?,
        remotePath: j['remotePath'] as String?,
      );

  final String id;
  String name;
  final String dir;
  final String clientKey;

  /// File sul telefono (selezionato dall'utente o scaricato da Google Foto)…
  final String? path;

  /// …oppure elemento della galleria del telefono (photo_manager).
  final String? assetId;

  int size;
  int sent;
  String? sha256;
  String? serverId;
  DateTime? modifiedAt;

  /// Se vero, [path] è un file temporaneo da cancellare a upload concluso.
  final bool deleteSource;

  TaskState state;
  String? error;
  String? remotePath;

  /// Descrizione della fase in corso (non salvata su disco).
  String phase = '';

  Map<String, dynamic> toJson() => {
        'id': id,
        'name': name,
        'dir': dir,
        'clientKey': clientKey,
        'path': path,
        'assetId': assetId,
        'size': size,
        'sent': sent,
        'sha256': sha256,
        'serverId': serverId,
        'modifiedAt': modifiedAt?.toIso8601String(),
        'deleteSource': deleteSource,
        'state': state.name,
        'error': error,
        'remotePath': remotePath,
      };

  double get progress => size <= 0 ? (state == TaskState.done ? 1 : 0) : (sent / size).clamp(0, 1);
}

class _Cancelled implements Exception {}

class _SourceMissing implements Exception {
  _SourceMissing(this.message);
  final String message;
}

/// Coda di upload persistente: sopravvive alla chiusura dell'app e riprende
/// dall'offset confermato dal server.
class UploadQueue extends ChangeNotifier {
  UploadQueue._();
  static final UploadQueue instance = UploadQueue._();

  static const _chunkSize = 4 * 1024 * 1024;
  static const _maxFinishedKept = 300;

  final List<UploadTask> _tasks = [];
  final Set<String> _cancelRequested = {};
  final _rng = Random();
  File? _file;
  Timer? _saveTimer;
  bool _running = false;
  DateTime _lastNotif = DateTime.fromMillisecondsSinceEpoch(0);

  List<UploadTask> get tasks => List.unmodifiable(_tasks);
  int get pendingCount =>
      _tasks.where((t) => t.state == TaskState.queued || t.state == TaskState.running).length;
  int get failedCount => _tasks.where((t) => t.state == TaskState.failed).length;
  Set<String> get completedKeys =>
      _tasks.where((t) => t.state == TaskState.done).map((t) => t.clientKey).toSet();

  // --- persistenza ---------------------------------------------------------------

  /// [fileName] diverso per il processo di lavoro in background, così non si
  /// scrive sullo stesso file dell'app aperta.
  Future<void> load({String fileName = 'upload_queue.json'}) async {
    final dir = await getApplicationSupportDirectory();
    _file = File(p.join(dir.path, fileName));
    try {
      if (await _file!.exists()) {
        final list = jsonDecode(await _file!.readAsString()) as List<dynamic>;
        for (final j in list) {
          final t = UploadTask.fromJson(j as Map<String, dynamic>);
          if (t.state == TaskState.running) t.state = TaskState.queued; // interrotto da una chiusura
          _tasks.add(t);
        }
      }
    } catch (_) {
      // coda illeggibile: si riparte vuoti
    }
    notifyListeners();
  }

  void _changed({bool immediate = false}) {
    notifyListeners();
    _saveTimer?.cancel();
    if (immediate) {
      unawaited(_save());
    } else {
      _saveTimer = Timer(const Duration(seconds: 1), () => unawaited(_save()));
    }
  }

  Future<void> _save() async {
    final f = _file;
    if (f == null) return;
    try {
      final tmp = File('${f.path}.tmp');
      await tmp.writeAsString(jsonEncode(_tasks.map((t) => t.toJson()).toList()), flush: true);
      await tmp.rename(f.path);
    } catch (_) {}
  }

  void _trim() {
    final finished = _tasks.where((t) => t.state == TaskState.done).toList();
    if (finished.length > _maxFinishedKept) {
      for (final t in finished.take(finished.length - _maxFinishedKept)) {
        _tasks.remove(t);
      }
    }
  }

  // --- API pubblica --------------------------------------------------------------

  String _newId() =>
      '${DateTime.now().microsecondsSinceEpoch.toRadixString(36)}${_rng.nextInt(1 << 32).toRadixString(36)}';

  /// Mette in coda un file del telefono.
  Future<void> enqueueFiles(
    List<({String path, String name, String? clientKey, bool deleteSource})> files, {
    required String dir,
  }) async {
    for (final f in files) {
      final id = _newId();
      _tasks.add(UploadTask(
        id: id,
        name: f.name,
        dir: dir,
        clientKey: f.clientKey ?? 'f:$id',
        path: f.path,
        deleteSource: f.deleteSource,
      ));
    }
    _changed(immediate: true);
    kick();
  }

  /// Mette in coda elementi della galleria. La chiave identifica l'elemento
  /// in modo stabile, così non viene caricato due volte.
  void enqueueAssets(List<({String assetId, String clientKey, DateTime? created})> assets,
      {required String dir}) {
    final wanted = assets.map((a) => a.clientKey).toSet();
    // un elemento fallito o annullato in precedenza viene riproposto da capo
    _tasks.removeWhere((t) =>
        (t.state == TaskState.failed || t.state == TaskState.cancelled) && wanted.contains(t.clientKey));
    final known = _tasks.map((t) => t.clientKey).toSet();
    for (final a in assets) {
      if (known.contains(a.clientKey)) continue;
      _tasks.add(UploadTask(
        id: _newId(),
        name: '',
        dir: dir,
        clientKey: a.clientKey,
        assetId: a.assetId,
        modifiedAt: a.created,
      ));
    }
    _changed(immediate: true);
    kick();
  }

  void retry(String id) {
    final t = _tasks.where((t) => t.id == id).firstOrNull;
    if (t == null || t.state == TaskState.running || t.state == TaskState.done) return;
    t.state = TaskState.queued;
    t.error = null;
    _changed(immediate: true);
    kick();
  }

  void retryAllFailed() {
    for (final t in _tasks.where((t) => t.state == TaskState.failed)) {
      t.state = TaskState.queued;
      t.error = null;
    }
    _changed(immediate: true);
    kick();
  }

  void cancel(String id) {
    final t = _tasks.where((t) => t.id == id).firstOrNull;
    if (t == null) return;
    if (t.state == TaskState.running) {
      _cancelRequested.add(id);
    } else if (t.state == TaskState.queued) {
      t.state = TaskState.cancelled;
      _changed(immediate: true);
    }
  }

  void cancelAll() {
    for (final t in _tasks) {
      if (t.state == TaskState.running) {
        _cancelRequested.add(t.id);
      } else if (t.state == TaskState.queued) {
        t.state = TaskState.cancelled;
      }
    }
    _changed(immediate: true);
  }

  void remove(String id) {
    final t = _tasks.where((t) => t.id == id).firstOrNull;
    if (t == null || t.state == TaskState.running) return;
    _tasks.remove(t);
    _changed(immediate: true);
  }

  void clearFinished() {
    _tasks.removeWhere((t) =>
        t.state == TaskState.done || t.state == TaskState.cancelled);
    _changed(immediate: true);
  }

  /// Attende che la coda si svuoti (o che passi [timeout]). Restituisce vero se è vuota.
  Future<bool> waitIdle({Duration timeout = const Duration(minutes: 8)}) async {
    final end = DateTime.now().add(timeout);
    while (DateTime.now().isBefore(end)) {
      if (!_running && !_tasks.any((t) => t.state == TaskState.queued)) return true;
      await Future<void>.delayed(const Duration(seconds: 2));
    }
    return false;
  }

  /// Avvia (o riprende) l'elaborazione della coda.
  void kick() {
    if (_running || !ConnectionManager.instance.isPaired) return;
    if (!_tasks.any((t) => t.state == TaskState.queued)) return;
    unawaited(_run());
  }

  // --- esecuzione ------------------------------------------------------------------

  Future<void> _run() async {
    if (_running) return;
    _running = true;
    await BackgroundKeeper.start();
    try {
      while (true) {
        final t = _tasks.where((t) => t.state == TaskState.queued).firstOrNull;
        if (t == null) break;
        await _process(t);
        _trim();
      }
    } finally {
      _running = false;
      _changed(immediate: true);
    }
    // se nel frattempo è arrivato altro lavoro si riparte (il nuovo giro prende il
    // proprio "possesso" del servizio prima che questo lo rilasci)
    if (ConnectionManager.instance.isPaired && _tasks.any((t) => t.state == TaskState.queued)) {
      unawaited(_run());
    }
    await BackgroundKeeper.stop();
  }

  void _publishNotification(UploadTask t) {
    final now = DateTime.now();
    if (now.difference(_lastNotif) < const Duration(seconds: 2)) return;
    _lastNotif = now;
    final left = pendingCount;
    BackgroundKeeper.update(
      'FireShare · $left in coda',
      t.name.isEmpty ? 'Preparazione…' : '${t.name} (${(t.progress * 100).round()}%)',
    );
  }

  Future<void> _sleep(Duration d, String id) async {
    var left = d.inMilliseconds;
    while (left > 0 && !_cancelRequested.contains(id)) {
      await Future<void>.delayed(const Duration(milliseconds: 500));
      left -= 500;
    }
  }

  Future<void> _process(UploadTask t) async {
    t.state = TaskState.running;
    t.error = null;
    _changed();
    var failures = 0;
    while (true) {
      final before = t.sent;
      try {
        if (_cancelRequested.contains(t.id)) throw _Cancelled();
        await _attempt(t);
        t.state = TaskState.done;
        t.sent = t.size;
        t.error = null;
        t.phase = '';
        if (t.deleteSource && t.path != null) {
          try {
            await File(t.path!).delete();
          } catch (_) {}
        }
        _changed(immediate: true);
        return;
      } on _Cancelled {
        _cancelRequested.remove(t.id);
        final id = t.serverId;
        t.state = TaskState.cancelled;
        t.error = null;
        t.phase = '';
        t.serverId = null;
        _changed(immediate: true);
        if (id != null) {
          try {
            await ConnectionManager.instance.client.discardUpload(id);
          } catch (_) {}
        }
        return;
      } on _SourceMissing catch (e) {
        _fail(t, e.message);
        return;
      } on FileSystemException catch (e) {
        _fail(t, 'Impossibile leggere il file: ${e.message}');
        return;
      } on ApiException catch (e) {
        if (t.sent > before) failures = 0;
        if (e.isUnauthorized || e.isCertificate) {
          _fail(t, e.message);
          return;
        }
        if (e.isNetwork) {
          failures++;
          if (failures > 8) {
            _fail(t, e.message);
            return;
          }
          t.error = 'Connessione assente, nuovo tentativo…';
          t.phase = '';
          _changed();
          await _sleep(Duration(seconds: min(60, 2 << failures)), t.id);
          continue;
        }
        if (e.status == 404 || e.code == 'hash_mismatch' || e.status == 423) {
          // upload sparito dal server, file modificato durante l'invio o upload
          // occupato da un'altra connessione: si ricomincia da capo
          failures++;
          if (failures > 3) {
            _fail(t, e.message);
            return;
          }
          if (e.code == 'hash_mismatch') t.sha256 = null;
          t.serverId = null;
          t.sent = 0;
          await _sleep(Duration(seconds: 2 * failures), t.id);
          continue;
        }
        _fail(t, e.message);
        return;
      } catch (e) {
        _fail(t, 'Errore imprevisto: $e');
        return;
      }
    }
  }

  void _fail(UploadTask t, String message) {
    t.state = TaskState.failed;
    t.error = message;
    t.phase = '';
    _cancelRequested.remove(t.id);
    _changed(immediate: true);
  }

  Future<File> _resolveFile(UploadTask t) async {
    if (t.path != null) {
      final f = File(t.path!);
      if (!await f.exists()) {
        throw _SourceMissing('Il file non è più disponibile sul telefono.');
      }
      if (t.name.isEmpty) t.name = p.basename(f.path);
      return f;
    }
    final asset = await AssetEntity.fromId(t.assetId!);
    if (asset == null) {
      throw _SourceMissing('L\'elemento non è più nella galleria del telefono.');
    }
    final f = await asset.originFile;
    if (f == null || !await f.exists()) {
      throw _SourceMissing('Impossibile leggere l\'originale dalla galleria.');
    }
    if (t.name.isEmpty) {
      final title = await asset.titleAsync;
      t.name = title.isNotEmpty ? title : p.basename(f.path);
    }
    t.modifiedAt ??= asset.createDateTime;
    return f;
  }

  Future<void> _attempt(UploadTask t) async {
    final client = ConnectionManager.instance.client;
    final file = await _resolveFile(t);
    final length = await file.length();
    if (length != t.size) {
      t.size = length;
      t.sha256 = null;
      t.serverId = null;
      t.sent = 0;
    }
    t.modifiedAt ??= await file.lastModified();

    if (t.sha256 == null) {
      t.phase = 'Calcolo impronta…';
      _changed();
      t.sha256 = (await sha256.bind(file.openRead()).first).toString();
      if (_cancelRequested.contains(t.id)) throw _Cancelled();
    }

    t.phase = 'Invio…';
    int offset;
    if (t.serverId == null) {
      final st = await client.createUpload(
        name: t.name,
        dir: t.dir,
        size: t.size,
        sha256Hex: t.sha256!,
        clientKey: t.clientKey,
        modifiedAt: t.modifiedAt,
      );
      t.serverId = st.id;
      if (st.complete) {
        t.remotePath = st.filePath;
        return;
      }
      offset = st.offset;
    } else {
      final st = await client.uploadStatus(t.serverId!);
      if (st.complete) {
        t.remotePath = st.filePath;
        return;
      }
      offset = st.offset;
    }
    t.sent = offset;
    _changed();

    final raf = await file.open();
    try {
      var first = true;
      while (first || offset < t.size) {
        first = false;
        if (_cancelRequested.contains(t.id)) throw _Cancelled();
        await raf.setPosition(offset);
        final Uint8List bytes = await raf.read(min(_chunkSize, t.size - offset));
        try {
          final st = await client.patchUpload(t.serverId!, offset, bytes);
          offset = st.offset;
          if (st.complete) {
            t.remotePath = st.filePath;
            t.sent = t.size;
            return;
          }
        } on ApiException catch (e) {
          if (e.status == 409 && e.offset != null) {
            offset = e.offset!; // il server è rimasto indietro/avanti: si riallinea
            t.sent = offset;
            continue;
          }
          rethrow;
        }
        t.sent = offset;
        _changed();
        _publishNotification(t);
      }
    } finally {
      await raf.close();
    }
    throw ApiException(500, 'incomplete', 'Il server non ha confermato la fine dell\'upload.');
  }
}
