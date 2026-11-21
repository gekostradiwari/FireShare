import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:google_sign_in/google_sign_in.dart';
import 'package:http/http.dart' as http;
import 'package:path/path.dart' as p;
import 'package:path_provider/path_provider.dart';

import '../../core/api/fireshare_client.dart';
import '../../core/storage/connection_manager.dart';
import '../../core/transfer/background_keeper.dart';
import '../../core/transfer/upload_queue.dart';

/// Errore delle API di Google Foto.
class GooglePhotosException implements Exception {
  GooglePhotosException(this.message, {this.status});
  final String message;
  final int? status;
  @override
  String toString() => message;
}

/// Sessione di selezione creata con la Picker API.
class PickerSession {
  PickerSession({
    required this.id,
    required this.pickerUri,
    required this.pollInterval,
    required this.timeout,
    required this.mediaItemsSet,
  });

  factory PickerSession.fromJson(Map<String, dynamic> j) {
    final polling = j['pollingConfig'] as Map<String, dynamic>?;
    return PickerSession(
      id: j['id'] as String,
      pickerUri: j['pickerUri'] as String? ?? '',
      pollInterval: _parseDuration(polling?['pollInterval'] as String?, const Duration(seconds: 5)),
      timeout: _parseDuration(polling?['timeoutIn'] as String?, const Duration(minutes: 10)),
      mediaItemsSet: j['mediaItemsSet'] as bool? ?? false,
    );
  }

  final String id;
  final String pickerUri;
  final Duration pollInterval;
  final Duration timeout;
  final bool mediaItemsSet;

  static Duration _parseDuration(String? s, Duration fallback) {
    if (s == null || !s.endsWith('s')) return fallback;
    final secs = double.tryParse(s.substring(0, s.length - 1));
    if (secs == null || secs <= 0) return fallback;
    return Duration(milliseconds: (secs * 1000).round());
  }
}

/// Elemento scelto dall'utente nel selettore di Google Foto.
class PickedItem {
  PickedItem({
    required this.id,
    required this.isVideo,
    required this.baseUrl,
    required this.filename,
    required this.mimeType,
    required this.createTime,
  });

  factory PickedItem.fromJson(Map<String, dynamic> j) {
    final f = (j['mediaFile'] as Map<String, dynamic>?) ?? const {};
    return PickedItem(
      id: j['id'] as String,
      isVideo: j['type'] == 'VIDEO',
      baseUrl: f['baseUrl'] as String? ?? '',
      filename: f['filename'] as String? ?? '',
      mimeType: f['mimeType'] as String? ?? '',
      createTime: DateTime.tryParse(j['createTime'] as String? ?? '')?.toLocal(),
    );
  }

  final String id;
  final bool isVideo;
  final String baseUrl;
  final String filename;
  final String mimeType;
  final DateTime? createTime;

  String get clientKey => 'gp:$id';

  /// Nome file garantito con estensione.
  String get safeName {
    var n = filename.trim().isEmpty ? id : filename.trim();
    if (p.extension(n).isEmpty) {
      final ext = switch (mimeType) {
        'image/jpeg' => '.jpg',
        'image/png' => '.png',
        'image/heic' => '.heic',
        'image/webp' => '.webp',
        'image/gif' => '.gif',
        'video/mp4' => '.mp4',
        'video/quicktime' => '.mov',
        _ => isVideo ? '.mp4' : '.jpg',
      };
      n += ext;
    }
    return n;
  }
}

/// Accesso a Google Foto tramite la **Picker API** ufficiale: l'utente sceglie
/// le foto nell'interfaccia di Google Foto e l'app riceve solo quelle scelte.
///
/// Limiti imposti da Google (non aggirabili con le API ufficiali):
///  * le API non permettono di eliminare foto da Google Foto;
///  * le foto scaricate con `=d` non contengono la posizione GPS;
///  * i video scaricati con `=dv` sono una versione transcodificata "alta qualità".
class GooglePhotosPicker {
  GooglePhotosPicker._();
  static final GooglePhotosPicker instance = GooglePhotosPicker._();

  static const scope = 'https://www.googleapis.com/auth/photospicker.mediaitems.readonly';
  static const _api = 'https://photospicker.googleapis.com/v1';

  final GoogleSignIn _signIn = GoogleSignIn(scopes: [scope]);
  final http.Client _http = http.Client();

  GoogleSignInAccount? get account => _signIn.currentUser;

  Future<GoogleSignInAccount?> signInSilently() => _signIn.signInSilently();

  Future<GoogleSignInAccount?> signIn() => _signIn.signIn();

  Future<void> signOut() => _signIn.signOut();

  Future<String> _token({bool refresh = false}) async {
    final acc = _signIn.currentUser ?? await _signIn.signInSilently();
    if (acc == null) throw GooglePhotosException('Accedi con Google per continuare.', status: 401);
    if (refresh) await acc.clearAuthCache();
    final auth = await acc.authentication;
    final t = auth.accessToken;
    if (t == null) throw GooglePhotosException('Google non ha fornito un token di accesso.', status: 401);
    return t;
  }

  Future<dynamic> _call(String method, String path, {Map<String, String>? query, Object? body}) async {
    for (var attempt = 0; attempt < 2; attempt++) {
      final token = await _token(refresh: attempt > 0);
      final uri = Uri.parse('$_api$path').replace(queryParameters: query);
      final req = http.Request(method, uri)..headers['Authorization'] = 'Bearer $token';
      if (body != null) {
        req.headers['Content-Type'] = 'application/json';
        req.body = jsonEncode(body);
      }
      final resp = await http.Response.fromStream(await _http.send(req).timeout(const Duration(seconds: 30)));
      if (resp.statusCode == 401 && attempt == 0) continue;
      if (resp.statusCode < 200 || resp.statusCode >= 300) {
        var msg = 'Google Foto ha risposto con errore ${resp.statusCode}.';
        try {
          msg = ((jsonDecode(resp.body) as Map<String, dynamic>)['error'] as Map<String, dynamic>)['message'] as String? ?? msg;
        } catch (_) {}
        throw GooglePhotosException(msg, status: resp.statusCode);
      }
      return resp.body.isEmpty ? null : jsonDecode(resp.body);
    }
    throw GooglePhotosException('Autorizzazione Google non valida.', status: 401);
  }

  Future<PickerSession> createSession() async {
    final j = await _call('POST', '/sessions', body: {
      'pickingConfig': {'maxItemCount': '2000'},
    }) as Map<String, dynamic>;
    return PickerSession.fromJson(j);
  }

  Future<PickerSession> getSession(String id) async =>
      PickerSession.fromJson(await _call('GET', '/sessions/$id') as Map<String, dynamic>);

  Future<void> deleteSession(String id) async {
    try {
      await _call('DELETE', '/sessions/$id');
    } catch (_) {}
  }

  Future<List<PickedItem>> listItems(String sessionId) async {
    final items = <PickedItem>[];
    String? pageToken;
    do {
      final j = await _call('GET', '/mediaItems', query: {
        'sessionId': sessionId,
        'pageSize': '100',
        if (pageToken != null) 'pageToken': pageToken,
      }) as Map<String, dynamic>;
      for (final m in (j['mediaItems'] as List<dynamic>? ?? const [])) {
        final item = PickedItem.fromJson(m as Map<String, dynamic>);
        if (item.baseUrl.isNotEmpty) items.add(item);
      }
      pageToken = j['nextPageToken'] as String?;
    } while (pageToken != null && pageToken.isNotEmpty);
    return items;
  }

  /// Intestazione per caricare anteprime (`baseUrl=w200-h200-c`).
  Future<Map<String, String>> imageHeaders() async => {'Authorization': 'Bearer ${await _token()}'};

  /// Scarica un elemento in un file temporaneo e lo restituisce.
  Future<File> download(PickedItem item, Directory dir) async {
    final suffix = item.isVideo ? '=dv' : '=d';
    GooglePhotosException? last;
    for (var attempt = 0; attempt < 4; attempt++) {
      try {
        final token = await _token(refresh: attempt == 2);
        final req = http.Request('GET', Uri.parse('${item.baseUrl}$suffix'))
          ..headers['Authorization'] = 'Bearer $token';
        final resp = await _http.send(req).timeout(const Duration(minutes: 2));
        if (resp.statusCode == 200) {
          final file = File(p.join(dir.path, '${item.id.hashCode.toRadixString(16)}_${item.safeName}'));
          final sink = file.openWrite();
          try {
            await resp.stream.pipe(sink);
          } catch (_) {
            try {
              await file.delete();
            } catch (_) {}
            rethrow;
          }
          return file;
        }
        await resp.stream.drain<void>();
        last = GooglePhotosException(
          resp.statusCode == 403 || resp.statusCode == 404
              ? 'Il link di download è scaduto (dura 60 minuti): ripeti la selezione.'
              : 'Download da Google Foto fallito (${resp.statusCode}).',
          status: resp.statusCode,
        );
        if (resp.statusCode == 403 || resp.statusCode == 404) break;
      } on GooglePhotosException catch (e) {
        last = e;
      } on TimeoutException {
        last = GooglePhotosException('Google Foto non risponde.');
      } on SocketException {
        last = GooglePhotosException('Connessione a Google Foto interrotta.');
      } on http.ClientException {
        last = GooglePhotosException('Connessione a Google Foto interrotta.');
      }
      // i video possono impiegare qualche secondo prima di essere pronti
      await Future<void>.delayed(Duration(seconds: 2 * (attempt + 1)));
    }
    throw last ?? GooglePhotosException('Download fallito.');
  }
}

enum ImportPhase { idle, checking, transferring, finished, cancelled, failed }

/// Scarica gli elementi scelti da Google Foto e li passa alla coda di upload,
/// senza accumulare gigabyte sul telefono (al massimo 2 file alla volta).
class GoogleImportJob extends ChangeNotifier {
  GoogleImportJob._();
  static final GoogleImportJob instance = GoogleImportJob._();

  static const targetDir = 'Google Foto';

  ImportPhase phase = ImportPhase.idle;
  String? error;
  int total = 0;
  int alreadyOnPc = 0;
  int handedOver = 0;
  final List<String> failedNames = [];
  List<PickedItem> items = [];
  bool _cancel = false;

  bool get isActive => phase == ImportPhase.checking || phase == ImportPhase.transferring;

  void cancel() => _cancel = true;

  /// Torna allo stato iniziale (per una nuova selezione).
  void reset() {
    if (isActive) return;
    phase = ImportPhase.idle;
    error = null;
    items = [];
    total = alreadyOnPc = handedOver = 0;
    failedNames.clear();
    notifyListeners();
  }

  Future<void> start(List<PickedItem> picked) async {
    if (isActive) return;
    items = picked;
    total = picked.length;
    alreadyOnPc = 0;
    handedOver = 0;
    failedNames.clear();
    error = null;
    _cancel = false;
    phase = ImportPhase.checking;
    notifyListeners();
    await BackgroundKeeper.start();
    Directory? tmp;
    try {
      final client = ConnectionManager.instance.client;
      final done = await client.checkComplete(picked.map((i) => i.clientKey));
      alreadyOnPc = done.length;
      final todo = picked.where((i) => !done.contains(i.clientKey)).toList();
      phase = ImportPhase.transferring;
      notifyListeners();

      tmp = Directory(p.join((await getTemporaryDirectory()).path, 'google_import'));
      if (await tmp.exists()) await tmp.delete(recursive: true);
      await tmp.create(recursive: true);

      for (final item in todo) {
        if (_cancel) break;
        while (UploadQueue.instance.pendingCount >= 2 && !_cancel) {
          await Future<void>.delayed(const Duration(milliseconds: 500));
        }
        if (_cancel) break;
        try {
          final file = await GooglePhotosPicker.instance.download(item, tmp);
          await UploadQueue.instance.enqueueFiles(
            [(path: file.path, name: item.safeName, clientKey: item.clientKey, deleteSource: true)],
            dir: targetDir,
          );
          handedOver++;
        } on GooglePhotosException catch (e) {
          failedNames.add(item.safeName);
          if (e.status == 401 || e.status == 403 || e.status == 404) {
            error = e.message;
            break; // inutile proseguire: token o link scaduti
          }
        }
        notifyListeners();
      }
      phase = _cancel ? ImportPhase.cancelled : (error != null ? ImportPhase.failed : ImportPhase.finished);
    } on ApiException catch (e) {
      error = e.message;
      phase = ImportPhase.failed;
    } catch (e) {
      error = 'Errore imprevisto: $e';
      phase = ImportPhase.failed;
    } finally {
      notifyListeners();
      UploadQueue.instance.kick();
      await BackgroundKeeper.stop();
    }
  }
}
