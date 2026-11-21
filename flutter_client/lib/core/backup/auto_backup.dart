import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:connectivity_plus/connectivity_plus.dart';
import 'package:flutter/widgets.dart';
import 'package:path/path.dart' as p;
import 'package:path_provider/path_provider.dart';
import 'package:photo_manager/photo_manager.dart';
import 'package:workmanager/workmanager.dart';

import '../api/fireshare_client.dart';
import '../storage/connection_manager.dart';
import '../transfer/background_keeper.dart';
import '../transfer/upload_queue.dart';

const _workName = 'fireshare-auto-backup';
const _workTask = 'fireshare.backup';

/// Impostazioni del backup automatico (salvate in `auto_backup.json`).
class BackupConfig {
  BackupConfig({
    this.enabled = false,
    List<String>? albumIds,
    this.wifiOnly = true,
    this.chargingOnly = false,
    this.includeExisting = false,
    this.since,
    this.lastRun,
    this.lastResult = '',
  }) : albumIds = albumIds ?? [];

  factory BackupConfig.fromJson(Map<String, dynamic> j) => BackupConfig(
        enabled: j['enabled'] as bool? ?? false,
        albumIds: (j['albumIds'] as List<dynamic>? ?? const []).whereType<String>().toList(),
        wifiOnly: j['wifiOnly'] as bool? ?? true,
        chargingOnly: j['chargingOnly'] as bool? ?? false,
        includeExisting: j['includeExisting'] as bool? ?? false,
        since: DateTime.tryParse(j['since'] as String? ?? ''),
        lastRun: DateTime.tryParse(j['lastRun'] as String? ?? ''),
        lastResult: j['lastResult'] as String? ?? '',
      );

  bool enabled;

  /// Cartelle (album) da copiare; vuoto = tutta la galleria.
  List<String> albumIds;
  bool wifiOnly;
  bool chargingOnly;

  /// Se falso si copiano solo le foto create dopo [since].
  bool includeExisting;
  DateTime? since;
  DateTime? lastRun;
  String lastResult;

  Map<String, dynamic> toJson() => {
        'enabled': enabled,
        'albumIds': albumIds,
        'wifiOnly': wifiOnly,
        'chargingOnly': chargingOnly,
        'includeExisting': includeExisting,
        'since': since?.toIso8601String(),
        'lastRun': lastRun?.toIso8601String(),
        'lastResult': lastResult,
      };
}

/// Esito di un controllo della galleria.
class BackupScanResult {
  const BackupScanResult(this.message, {this.queued = 0, this.alreadyOnPc = 0, this.ran = true});
  final String message;
  final int queued;
  final int alreadyOnPc;

  /// Falso se il controllo non è partito (backup spento, niente Wi-Fi…).
  final bool ran;
}

/// Backup automatico delle cartelle di foto e video del telefono.
///
/// Ogni elemento nuovo viene messo nella coda di upload con la stessa chiave
/// usata dalla galleria (`ph:<id>:<modifica>`): il PC riconosce ciò che ha già
/// ricevuto e verificato, quindi niente viene caricato due volte, nemmeno se
/// il controllo parte più volte o da due processi diversi.
///
/// Il controllo avviene all'apertura dell'app, quando ci si torna, e ogni
/// circa 6 ore in background tramite WorkManager (se il sistema lo consente).
class AutoBackup extends ChangeNotifier {
  AutoBackup._();
  static final AutoBackup instance = AutoBackup._();

  BackupConfig config = BackupConfig();
  bool _scanning = false;
  DateTime? _lastScan;
  File? _file;

  bool get scanning => _scanning;

  Future<void> load() async {
    final dir = await getApplicationSupportDirectory();
    _file = File(p.join(dir.path, 'auto_backup.json'));
    try {
      if (await _file!.exists()) {
        config = BackupConfig.fromJson(
            jsonDecode(await _file!.readAsString()) as Map<String, dynamic>);
      }
    } catch (_) {
      config = BackupConfig();
    }
    notifyListeners();
  }

  Future<void> _save() async {
    final f = _file;
    if (f == null) return;
    try {
      final tmp = File('${f.path}.tmp');
      await tmp.writeAsString(jsonEncode(config.toJson()), flush: true);
      await tmp.rename(f.path);
    } catch (_) {}
  }

  /// Registra il lavoro periodico (o lo annulla se il backup è spento).
  Future<void> applySchedule() async {
    try {
      if (!config.enabled) {
        await Workmanager().cancelByUniqueName(_workName);
        return;
      }
      await Workmanager().registerPeriodicTask(
        _workName,
        _workTask,
        frequency: const Duration(hours: 6),
        initialDelay: const Duration(minutes: 15),
        constraints: Constraints(
          networkType: config.wifiOnly ? NetworkType.unmetered : NetworkType.connected,
          requiresCharging: config.chargingOnly,
        ),
        existingWorkPolicy: ExistingWorkPolicy.replace,
      );
    } catch (_) {
      // senza WorkManager resta il controllo all'apertura dell'app
    }
  }

  /// Modifica le impostazioni, le salva e aggiorna la pianificazione.
  Future<void> update(void Function(BackupConfig c) edit) async {
    edit(config);
    await _save();
    notifyListeners();
    await applySchedule();
  }

  /// Accende il backup. Con [includeExisting] copia anche ciò che c'è già in galleria,
  /// altrimenti solo le foto e i video creati da questo momento in poi.
  Future<void> enable({required bool includeExisting}) async {
    await update((c) {
      c.enabled = true;
      c.includeExisting = includeExisting;
      c.since = DateTime.now();
    });
    _lastScan = null;
    unawaited(scan());
  }

  Future<void> disable() => update((c) => c.enabled = false);

  /// Elenco delle cartelle della galleria (per la scelta nell'interfaccia).
  Future<List<AssetPathEntity>> listAlbums() async {
    final perm = await PhotoManager.requestPermissionExtend();
    if (!perm.hasAccess) return [];
    return PhotoManager.getAssetPathList(hasAll: false, type: RequestType.common);
  }

  /// Controllo periodico dall'app in primo piano: al massimo ogni 10 minuti.
  Future<void> scanIfDue() async {
    if (!config.enabled) return;
    final last = _lastScan;
    if (last != null && DateTime.now().difference(last) < const Duration(minutes: 10)) return;
    await scan();
  }

  Future<bool> _onUnmeteredNetwork() async {
    try {
      final r = await Connectivity().checkConnectivity();
      return r == ConnectivityResult.wifi || r == ConnectivityResult.ethernet;
    } catch (_) {
      return true; // se non si riesce a stabilirlo non si blocca il backup
    }
  }

  static String _dirFor(AssetEntity a) {
    var folder = '';
    final rel = a.relativePath;
    if (rel != null) {
      final parts = rel.split('/').where((s) => s.trim().isNotEmpty).toList();
      if (parts.isNotEmpty) folder = parts.last;
    }
    folder = folder.replaceAll(RegExp(r'[\\/:*?"<>|]'), '_').trim();
    if (folder.isEmpty || folder == '.' || folder == '..') folder = 'Galleria';
    return 'Backup telefono/$folder';
  }

  /// Cerca in galleria ciò che il PC non ha ancora e lo mette in coda.
  Future<BackupScanResult> scan({bool background = false}) async {
    final cfg = config;
    if (!cfg.enabled) return const BackupScanResult('Backup automatico spento.', ran: false);
    if (!ConnectionManager.instance.isPaired) {
      return const BackupScanResult('Telefono non collegato a un PC.', ran: false);
    }
    if (_scanning) return const BackupScanResult('Controllo già in corso.', ran: false);
    if (cfg.wifiOnly && !await _onUnmeteredNetwork()) {
      return const BackupScanResult('In attesa del Wi-Fi.', ran: false);
    }

    _scanning = true;
    _lastScan = DateTime.now();
    notifyListeners();
    var result = const BackupScanResult('');
    try {
      if (!background) {
        final perm = await PhotoManager.requestPermissionExtend();
        if (!perm.hasAccess) {
          result = const BackupScanResult('Manca il permesso di leggere la galleria.');
          return result;
        }
      }
      final since = cfg.includeExisting
          ? DateTime.fromMillisecondsSinceEpoch(0)
          : (cfg.since ?? DateTime.now());
      final filter = FilterOptionGroup(
        createTimeCond: DateTimeCond(
          min: since,
          max: DateTime.now().add(const Duration(days: 1)),
        ),
      );
      final all = cfg.albumIds.isEmpty;
      final paths = await PhotoManager.getAssetPathList(
        hasAll: all,
        onlyAll: all,
        type: RequestType.common,
        filterOption: filter,
      );
      final albums = all ? paths : paths.where((a) => cfg.albumIds.contains(a.id)).toList();

      final seen = <String>{};
      final candidates = <AssetEntity>[];
      for (final album in albums) {
        var page = 0;
        while (true) {
          final list = await album.getAssetListPaged(page: page, size: 200);
          for (final a in list) {
            if (seen.add(a.id)) candidates.add(a);
          }
          if (list.length < 200) break;
          page++;
        }
      }
      if (candidates.isEmpty) {
        result = const BackupScanResult('Nessun elemento nuovo.');
        return result;
      }

      final onPc = await ConnectionManager.instance.client.checkComplete(candidates.map(assetKey));
      final todo = candidates.where((a) => !onPc.contains(assetKey(a))).take(1500).toList();
      final groups = <String, List<AssetEntity>>{};
      for (final a in todo) {
        groups.putIfAbsent(_dirFor(a), () => []).add(a);
      }
      for (final e in groups.entries) {
        UploadQueue.instance.enqueueAssets(
          e.value
              .map((a) => (assetId: a.id, clientKey: assetKey(a), created: a.createDateTime))
              .toList(),
          dir: e.key,
        );
      }
      result = BackupScanResult(
        todo.isEmpty
            ? 'Tutto già sul PC.'
            : '${todo.length} elementi messi in coda.',
        queued: todo.length,
        alreadyOnPc: candidates.length - todo.length,
      );
      return result;
    } on ApiException catch (e) {
      result = BackupScanResult('PC non raggiungibile: ${e.message}');
      return result;
    } catch (e) {
      result = BackupScanResult('Controllo non riuscito: $e');
      return result;
    } finally {
      _scanning = false;
      cfg.lastRun = DateTime.now();
      cfg.lastResult = result.message;
      await _save();
      notifyListeners();
    }
  }
}

/// Punto di ingresso del lavoro periodico in background (WorkManager).
@pragma('vm:entry-point')
void backupCallbackDispatcher() {
  Workmanager().executeTask((task, inputData) async {
    WidgetsFlutterBinding.ensureInitialized();
    BackgroundKeeper.disabled = true;
    try {
      await ConnectionManager.instance.load();
      if (!ConnectionManager.instance.isPaired) return true;
      await UploadQueue.instance.load(fileName: 'upload_queue_bg.json');
      await AutoBackup.instance.load();
      if (!AutoBackup.instance.config.enabled) return true;
      UploadQueue.instance.kick(); // riprende ciò che era rimasto a metà
      await AutoBackup.instance.scan(background: true);
      await UploadQueue.instance.waitIdle();
    } catch (_) {
      // il prossimo giro riprova
    }
    return true;
  });
}
