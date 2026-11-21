import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:crypto/crypto.dart';

/// Errore restituito dal server (o di rete, se [status] == 0).
class ApiException implements Exception {
  ApiException(this.status, this.code, this.message, {this.offset});

  factory ApiException.network(String message) =>
      ApiException(0, 'network', message);

  final int status;
  final String code;
  final String message;

  /// Valorizzato nelle risposte 409 degli upload: offset corretto lato server.
  final int? offset;

  bool get isNetwork => status == 0 && code == 'network';
  bool get isCertificate => code == 'certificate';
  bool get isUnauthorized => status == 401;

  @override
  String toString() => message;
}

/// Un file o una cartella nello spazio cloud.
class RemoteEntry {
  RemoteEntry({
    required this.name,
    required this.path,
    required this.isDir,
    required this.size,
    required this.modifiedAt,
  });

  factory RemoteEntry.fromJson(Map<String, dynamic> j) => RemoteEntry(
        name: j['name'] as String,
        path: j['path'] as String,
        isDir: j['isDir'] as bool? ?? false,
        size: (j['size'] as num?)?.toInt() ?? 0,
        modifiedAt: DateTime.tryParse(j['modifiedAt'] as String? ?? '')?.toLocal(),
      );

  final String name;
  final String path;
  final bool isDir;
  final int size;
  final DateTime? modifiedAt;
}

/// Stato di un upload sul server.
class UploadStatus {
  UploadStatus({
    required this.id,
    required this.complete,
    required this.offset,
    required this.size,
    this.filePath,
  });

  factory UploadStatus.fromJson(Map<String, dynamic> j) => UploadStatus(
        id: j['id'] as String,
        complete: j['status'] == 'complete',
        offset: (j['offset'] as num?)?.toInt() ?? 0,
        size: (j['size'] as num?)?.toInt() ?? 0,
        filePath: (j['file'] as Map<String, dynamic>?)?['path'] as String?,
      );

  final String id;
  final bool complete;
  final int offset;
  final int size;
  final String? filePath;
}

/// Un elemento della galleria cloud (foto o video sul PC).
class MediaItem {
  MediaItem({
    required this.name,
    required this.path,
    required this.size,
    required this.modifiedAt,
    required this.isVideo,
  });

  factory MediaItem.fromJson(Map<String, dynamic> j) => MediaItem(
        name: j['name'] as String,
        path: j['path'] as String,
        size: (j['size'] as num?)?.toInt() ?? 0,
        modifiedAt: DateTime.tryParse(j['modifiedAt'] as String? ?? '')?.toLocal(),
        isVideo: j['kind'] == 'video',
      );

  final String name;
  final String path;
  final int size;
  final DateTime? modifiedAt;
  final bool isVideo;
}

/// Una pagina di risultati della galleria cloud.
class MediaPage {
  MediaPage(this.total, this.items);
  final int total;
  final List<MediaItem> items;
}

/// Un link di condivisione temporaneo.
class ShareLink {
  ShareLink({
    required this.id,
    required this.path,
    required this.name,
    required this.url,
    required this.expiresAt,
    required this.maxDownloads,
    required this.downloads,
    required this.hasPassword,
    required this.trustedUrl,
  });

  factory ShareLink.fromJson(Map<String, dynamic> j) => ShareLink(
        id: j['id'] as String,
        path: j['path'] as String? ?? '',
        name: j['name'] as String? ?? '',
        url: j['url'] as String? ?? '',
        expiresAt: DateTime.tryParse(j['expiresAt'] as String? ?? '')?.toLocal(),
        maxDownloads: (j['maxDownloads'] as num?)?.toInt() ?? 0,
        downloads: (j['downloads'] as num?)?.toInt() ?? 0,
        hasPassword: j['hasPassword'] as bool? ?? false,
        trustedUrl: j['trustedUrl'] as bool? ?? false,
      );

  final String id;
  final String path;
  final String name;
  final String url;
  final DateTime? expiresAt;
  final int maxDownloads;
  final int downloads;
  final bool hasPassword;

  /// Falso se il link usa il certificato autofirmato del PC (il browser mostrerà un avviso).
  final bool trustedUrl;
}

/// Esito del test di un indirizzo del server.
class UrlProbe {
  UrlProbe(this.url, {required this.ok, this.millis, this.error});
  final String url;
  final bool ok;
  final int? millis;
  final String? error;
}

/// Limita le richieste simultanee (le miniature di una griglia sono decine).
class _Gate {
  _Gate(this.max);
  final int max;
  int _active = 0;
  final _waiting = <Completer<void>>[];

  Future<T> run<T>(Future<T> Function() job) async {
    if (_active >= max) {
      final c = Completer<void>();
      _waiting.add(c);
      await c.future;
    }
    _active++;
    try {
      return await job();
    } finally {
      _active--;
      if (_waiting.isNotEmpty) _waiting.removeAt(0).complete();
    }
  }
}

/// Risultato dell'accoppiamento.
class PairResult {
  PairResult(this.token, this.deviceId, this.serverName);
  final String token;
  final String deviceId;
  final String serverName;
}

/// Client dell'API HTTPS di FireShare.
///
/// Il certificato del PC è autofirmato: non ci si fida di nessuna CA, ma
/// solo del certificato la cui impronta SHA-256 coincide con quella ricevuta
/// nel QR di accoppiamento ("certificate pinning").
///
/// Con più indirizzi (LAN, DDNS...) vengono provati tutti in parallelo e
/// vince il primo che risponde.
class FireShareClient {
  FireShareClient({
    required this.fingerprint,
    required List<String> urls,
    this.token,
  }) : urls = urls.map((u) => u.replaceAll(RegExp(r'/+$'), '')).toList();

  final String fingerprint;
  final List<String> urls;
  final String? token;

  String? _base;
  HttpClient? _shared;
  final _thumbGate = _Gate(6);

  HttpClient _newClient({Duration? connectTimeout}) {
    final c = HttpClient(context: SecurityContext(withTrustedRoots: false));
    c.badCertificateCallback = (X509Certificate cert, String host, int port) =>
        sha256.convert(cert.der).toString() == fingerprint.toLowerCase();
    c.connectionTimeout = connectTimeout ?? const Duration(seconds: 10);
    c.idleTimeout = const Duration(seconds: 30);
    c.autoUncompress = false;
    return c;
  }

  HttpClient get _http => _shared ??= _newClient();

  /// Chiude le connessioni aperte.
  void close() {
    _shared?.close(force: true);
    _shared = null;
  }

  // --- scelta dell'indirizzo ---------------------------------------------------

  Future<bool> _probe(String baseUrl) async {
    final c = _newClient(connectTimeout: const Duration(seconds: 5));
    try {
      final req = await c
          .getUrl(Uri.parse('$baseUrl/api/v1/health'))
          .timeout(const Duration(seconds: 6));
      final resp = await req.close().timeout(const Duration(seconds: 6));
      await resp.drain<void>();
      return resp.statusCode == 200;
    } catch (_) {
      return false;
    } finally {
      c.close(force: true);
    }
  }

  /// Prova tutti gli indirizzi in parallelo e memorizza il primo che risponde.
  Future<String> resolveBase({bool force = false}) async {
    if (!force && _base != null) return _base!;
    final done = Completer<String>();
    var pending = urls.length;
    for (final u in urls) {
      _probe(u).then((ok) {
        pending--;
        if (done.isCompleted) return;
        if (ok) {
          done.complete(u);
        } else if (pending == 0) {
          done.completeError(ApiException.network(
              'Il server non è raggiungibile. Controlla che il PC sia acceso, '
              'che FireShare sia in esecuzione e che il telefono sia sulla stessa rete '
              '(o che l\'indirizzo pubblico sia configurato).'));
        }
      });
    }
    _base = await done.future;
    return _base!;
  }

  // --- richieste di basso livello ------------------------------------------------

  Future<HttpClientRequest> _open(
    String method,
    String path, {
    Map<String, String>? query,
    bool auth = true,
  }) async {
    for (var attempt = 0;; attempt++) {
      final base = await resolveBase(force: attempt > 0);
      final uri = Uri.parse('$base$path').replace(queryParameters: query);
      try {
        final req = await _http.openUrl(method, uri).timeout(const Duration(seconds: 15));
        if (auth && token != null) {
          req.headers.set(HttpHeaders.authorizationHeader, 'Bearer $token');
        }
        req.headers.set(HttpHeaders.acceptHeader, 'application/json');
        return req;
      } on HandshakeException {
        throw ApiException(0, 'certificate',
            'Il certificato del server non corrisponde a quello dell\'accoppiamento. '
            'Se hai reinstallato FireShare sul PC, collega di nuovo il telefono.');
      } on Exception {
        _shared?.close(force: true);
        _shared = null;
        _base = null;
        if (attempt >= 1) {
          throw ApiException.network('Connessione al server interrotta.');
        }
      }
    }
  }

  Future<ApiException> _errorFrom(HttpClientResponse resp) async {
    final text = await utf8.decoder.bind(resp).join();
    String msg = 'Errore del server (${resp.statusCode})';
    String code = 'http_${resp.statusCode}';
    int? offset;
    try {
      final j = jsonDecode(text) as Map<String, dynamic>;
      msg = (j['error'] as String?) ?? msg;
      code = (j['code'] as String?) ?? code;
      offset = (j['offset'] as num?)?.toInt();
    } catch (_) {}
    return ApiException(resp.statusCode, code, msg, offset: offset);
  }

  Future<dynamic> _json(
    String method,
    String path, {
    Map<String, String>? query,
    Object? body,
    bool auth = true,
  }) async {
    try {
      final req = await _open(method, path, query: query, auth: auth);
      if (body != null) {
        final bytes = utf8.encode(jsonEncode(body));
        req.headers.contentType = ContentType.json;
        req.contentLength = bytes.length;
        req.add(bytes);
      }
      final resp = await req.close().timeout(const Duration(seconds: 60));
      if (resp.statusCode < 200 || resp.statusCode >= 300) {
        throw await _errorFrom(resp);
      }
      final text = await utf8.decoder.bind(resp).join();
      return text.isEmpty ? null : jsonDecode(text);
    } on ApiException {
      rethrow;
    } on TimeoutException {
      throw ApiException.network('Il server non risponde.');
    } on SocketException {
      _base = null;
      throw ApiException.network('Connessione al server interrotta.');
    } on HttpException {
      _base = null;
      throw ApiException.network('Connessione al server interrotta.');
    }
  }

  // --- API -----------------------------------------------------------------------

  /// Accoppia questo telefono con il server usando il codice del QR.
  Future<PairResult> pair(String code, String deviceName) async {
    final j = await _json('POST', '/api/v1/pair',
        body: {'code': code, 'deviceName': deviceName}, auth: false) as Map<String, dynamic>;
    return PairResult(j['token'] as String, j['deviceId'] as String,
        (j['serverName'] as String?) ?? 'FireShare');
  }

  Future<Map<String, dynamic>> info() async =>
      await _json('GET', '/api/v1/info') as Map<String, dynamic>;

  Future<void> unpair() async => _json('DELETE', '/api/v1/me');

  Future<List<RemoteEntry>> list(String path) async {
    final j = await _json('GET', '/api/v1/files', query: {'path': path}) as Map<String, dynamic>;
    return (j['entries'] as List<dynamic>? ?? const [])
        .map((e) => RemoteEntry.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<void> delete(String path) async => _json('DELETE', '/api/v1/files',
      query: {'path': path, 'recursive': 'true'});

  Future<void> mkdir(String path) async =>
      _json('POST', '/api/v1/files/mkdir', body: {'path': path});

  Future<void> move(String from, String to) async =>
      _json('POST', '/api/v1/files/move', body: {'from': from, 'to': to});

  /// Dato un insieme di chiavi, restituisce quelle già caricate e verificate.
  Future<Set<String>> checkComplete(Iterable<String> clientKeys) async {
    final keys = clientKeys.toList();
    final result = <String>{};
    for (var i = 0; i < keys.length; i += 2000) {
      final chunk = keys.sublist(i, i + 2000 > keys.length ? keys.length : i + 2000);
      final j = await _json('POST', '/api/v1/uploads/check', body: {'clientKeys': chunk})
          as Map<String, dynamic>;
      result.addAll((j['complete'] as List<dynamic>? ?? const []).cast<String>());
    }
    return result;
  }

  // --- galleria cloud ---------------------------------------------------------------

  Future<MediaPage> media({int offset = 0, int limit = 120, String? kind, String? dir}) async {
    final j = await _json('GET', '/api/v1/media', query: {
      'offset': '$offset',
      'limit': '$limit',
      if (kind != null) 'kind': kind,
      if (dir != null && dir.isNotEmpty) 'dir': dir,
    }) as Map<String, dynamic>;
    return MediaPage(
      (j['total'] as num?)?.toInt() ?? 0,
      (j['items'] as List<dynamic>? ?? const [])
          .map((e) => MediaItem.fromJson(e as Map<String, dynamic>))
          .toList(),
    );
  }

  /// Scarica un piccolo contenuto (miniatura) in memoria.
  Future<Uint8List> getBytes(String path, Map<String, String> query, {int maxBytes = 8 << 20}) async {
    try {
      final req = await _open('GET', path, query: query);
      req.headers.set(HttpHeaders.acceptHeader, '*/*');
      final resp = await req.close().timeout(const Duration(seconds: 60));
      if (resp.statusCode != 200) throw await _errorFrom(resp);
      final b = BytesBuilder(copy: false);
      await for (final chunk in resp) {
        b.add(chunk);
        if (b.length > maxBytes) {
          throw ApiException(0, 'too_big', 'Il file è troppo grande per l\'anteprima.');
        }
      }
      return b.takeBytes();
    } on ApiException {
      rethrow;
    } on TimeoutException {
      throw ApiException.network('Il server non risponde.');
    } on SocketException {
      _base = null;
      throw ApiException.network('Connessione al server interrotta.');
    } on HttpException {
      _base = null;
      throw ApiException.network('Connessione al server interrotta.');
    }
  }

  /// Miniatura JPEG (lato massimo [size]: 160, 320 o 640). Lancia con codice
  /// `no_thumb` se il PC non riesce a produrla (es. video senza ffmpeg).
  Future<Uint8List> thumb(String remotePath, {int size = 320}) => _thumbGate.run(
        () => getBytes('/api/v1/files/thumb', {'path': remotePath, 'size': '$size'}),
      );

  /// Contenuto completo di un file in memoria (solo per anteprime di dimensioni contenute).
  Future<Uint8List> content(String remotePath, {int maxBytes = 30 << 20}) =>
      getBytes('/api/v1/files/content', {'path': remotePath}, maxBytes: maxBytes);

  // --- link di condivisione --------------------------------------------------------------

  Future<ShareLink> createShare(
    String path, {
    required Duration expiresIn,
    int maxDownloads = 0,
    String? password,
  }) async {
    final j = await _json('POST', '/api/v1/shares', body: {
      'path': path,
      'expiresInSeconds': expiresIn.inSeconds,
      if (maxDownloads > 0) 'maxDownloads': maxDownloads,
      if (password != null && password.isNotEmpty) 'password': password,
    }) as Map<String, dynamic>;
    return ShareLink.fromJson(j);
  }

  Future<List<ShareLink>> listShares() async {
    final j = await _json('GET', '/api/v1/shares') as Map<String, dynamic>;
    return (j['shares'] as List<dynamic>? ?? const [])
        .map((e) => ShareLink.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<void> revokeShare(String id) async => _json('DELETE', '/api/v1/shares/$id');

  // --- diagnostica della connessione ----------------------------------------------------

  /// Prova ogni indirizzo separatamente (senza usare l'indirizzo già scelto).
  Future<List<UrlProbe>> probeAll() => Future.wait(urls.map(_probeDetailed));

  Future<UrlProbe> _probeDetailed(String baseUrl) async {
    final c = _newClient(connectTimeout: const Duration(seconds: 6));
    final sw = Stopwatch()..start();
    try {
      final req = await c
          .getUrl(Uri.parse('$baseUrl/api/v1/health'))
          .timeout(const Duration(seconds: 8));
      final resp = await req.close().timeout(const Duration(seconds: 8));
      await resp.drain<void>();
      if (resp.statusCode != 200) {
        return UrlProbe(baseUrl, ok: false, error: 'Risposta inattesa (${resp.statusCode})');
      }
      return UrlProbe(baseUrl, ok: true, millis: sw.elapsedMilliseconds);
    } on HandshakeException {
      return UrlProbe(baseUrl,
          ok: false, error: 'Certificato diverso da quello dell\'accoppiamento');
    } on TimeoutException {
      return UrlProbe(baseUrl, ok: false, error: 'Nessuna risposta (timeout)');
    } on SocketException catch (e) {
      final m = e.message.toLowerCase();
      return UrlProbe(baseUrl,
          ok: false,
          error: m.contains('refused')
              ? 'Connessione rifiutata: porta chiusa o server spento'
              : m.contains('lookup') || m.contains('resolve')
                  ? 'Nome non risolto: controlla il dominio'
                  : 'Irraggiungibile');
    } catch (_) {
      return UrlProbe(baseUrl, ok: false, error: 'Errore di connessione');
    } finally {
      c.close(force: true);
    }
  }

  // --- upload ripristinabile --------------------------------------------------------

  Future<UploadStatus> createUpload({
    required String name,
    required String dir,
    required int size,
    required String sha256Hex,
    required String clientKey,
    DateTime? modifiedAt,
  }) async {
    final j = await _json('POST', '/api/v1/uploads', body: {
      'name': name,
      'dir': dir,
      'size': size,
      'sha256': sha256Hex,
      'clientKey': clientKey,
      if (modifiedAt != null) 'modifiedAt': modifiedAt.toUtc().toIso8601String(),
    }) as Map<String, dynamic>;
    return UploadStatus.fromJson(j);
  }

  Future<UploadStatus> uploadStatus(String id) async =>
      UploadStatus.fromJson(await _json('GET', '/api/v1/uploads/$id') as Map<String, dynamic>);

  Future<void> discardUpload(String id) async => _json('DELETE', '/api/v1/uploads/$id');

  /// Invia [bytes] a partire da [offset]. In caso di offset errato (409) lancia
  /// un [ApiException] con `offset` valorizzato.
  Future<UploadStatus> patchUpload(String id, int offset, Uint8List bytes) async {
    try {
      final req = await _open('PATCH', '/api/v1/uploads/$id');
      req.headers.set('Upload-Offset', offset.toString());
      req.headers.contentType = ContentType('application', 'offset+octet-stream');
      req.contentLength = bytes.length;
      req.add(bytes);
      final resp = await req.close().timeout(const Duration(minutes: 3));
      if (resp.statusCode != 200) throw await _errorFrom(resp);
      final text = await utf8.decoder.bind(resp).join();
      return UploadStatus.fromJson(jsonDecode(text) as Map<String, dynamic>);
    } on ApiException {
      rethrow;
    } on TimeoutException {
      throw ApiException.network('Il server non risponde.');
    } on SocketException {
      _base = null;
      throw ApiException.network('Connessione al server interrotta.');
    } on HttpException {
      _base = null;
      throw ApiException.network('Connessione al server interrotta.');
    }
  }

  // --- download ripristinabile --------------------------------------------------------

  /// Scarica [remotePath] in [dest]. Se [dest] esiste già, riprende da dove era
  /// arrivato (Range). Restituisce a download completato.
  Future<void> download(
    String remotePath,
    File dest, {
    void Function(int received, int total)? onProgress,
    bool Function()? isCancelled,
  }) async {
    var existing = await dest.exists() ? await dest.length() : 0;
    try {
      final req = await _open('GET', '/api/v1/files/content', query: {'path': remotePath});
      if (existing > 0) req.headers.set(HttpHeaders.rangeHeader, 'bytes=$existing-');
      final resp = await req.close();
      if (resp.statusCode == 416) {
        await resp.drain<void>();
        return; // il file locale è già completo
      }
      if (resp.statusCode != 200 && resp.statusCode != 206) throw await _errorFrom(resp);
      final append = resp.statusCode == 206;
      if (!append) existing = 0;
      final total = resp.contentLength >= 0 ? existing + resp.contentLength : 0;
      final raf = await dest.open(mode: append ? FileMode.append : FileMode.write);
      var received = existing;
      try {
        await for (final chunk in resp) {
          if (isCancelled?.call() ?? false) {
            throw ApiException(0, 'cancelled', 'Download annullato.');
          }
          await raf.writeFrom(chunk);
          received += chunk.length;
          onProgress?.call(received, total);
        }
      } finally {
        await raf.close();
      }
    } on ApiException {
      rethrow;
    } on SocketException {
      _base = null;
      throw ApiException.network('Connessione al server interrotta.');
    } on HttpException {
      _base = null;
      throw ApiException.network('Connessione al server interrotta.');
    }
  }
}
