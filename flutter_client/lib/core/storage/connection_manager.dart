import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

import '../api/fireshare_client.dart';
import '../api/pairing_payload.dart';

/// Il server a cui il telefono è accoppiato.
class ServerConnection {
  ServerConnection({
    required this.serverName,
    required this.fingerprint,
    required this.urls,
    required this.token,
    required this.deviceId,
  });

  factory ServerConnection.fromJson(Map<String, dynamic> j) => ServerConnection(
        serverName: j['serverName'] as String,
        fingerprint: j['fingerprint'] as String,
        urls: (j['urls'] as List<dynamic>).cast<String>(),
        token: j['token'] as String,
        deviceId: j['deviceId'] as String,
      );

  final String serverName;
  final String fingerprint;
  final List<String> urls;
  final String token;
  final String deviceId;

  Map<String, dynamic> toJson() => {
        'serverName': serverName,
        'fingerprint': fingerprint,
        'urls': urls,
        'token': token,
        'deviceId': deviceId,
      };
}

/// Gestisce l'accoppiamento: token e impronta del certificato sono salvati
/// nell'archivio cifrato di Android (Keystore).
class ConnectionManager extends ChangeNotifier {
  ConnectionManager._();
  static final ConnectionManager instance = ConnectionManager._();

  static const _key = 'fireshare.connection';
  static const _storage = FlutterSecureStorage(
    aOptions: AndroidOptions(encryptedSharedPreferences: true),
  );

  ServerConnection? _connection;
  FireShareClient? _client;

  ServerConnection? get connection => _connection;
  bool get isPaired => _connection != null;

  /// Client autenticato; lancia se il telefono non è accoppiato.
  FireShareClient get client {
    final c = _client;
    if (c == null) throw ApiException(401, 'not_paired', 'Telefono non accoppiato.');
    return c;
  }

  Future<void> load() async {
    try {
      final raw = await _storage.read(key: _key);
      if (raw != null) _set(ServerConnection.fromJson(jsonDecode(raw) as Map<String, dynamic>));
    } catch (_) {
      // archivio illeggibile (es. backup ripristinato su un altro telefono)
      await _storage.delete(key: _key);
    }
  }

  void _set(ServerConnection? c) {
    _client?.close();
    _connection = c;
    _client = c == null
        ? null
        : FireShareClient(fingerprint: c.fingerprint, urls: c.urls, token: c.token);
    notifyListeners();
  }

  /// Accoppia il telefono usando il contenuto del QR.
  Future<void> pair(PairingPayload payload, String deviceName) async {
    final anon = FireShareClient(fingerprint: payload.fingerprint, urls: payload.urls);
    try {
      final r = await anon.pair(payload.code, deviceName);
      final conn = ServerConnection(
        serverName: r.serverName.isEmpty ? payload.serverName : r.serverName,
        fingerprint: payload.fingerprint,
        urls: payload.urls,
        token: r.token,
        deviceId: r.deviceId,
      );
      await _storage.write(key: _key, value: jsonEncode(conn.toJson()));
      _set(conn);
    } finally {
      anon.close();
    }
  }

  static String? _cleanUrl(String raw) {
    final u = raw.trim().replaceAll(RegExp(r'/+$'), '');
    final uri = Uri.tryParse(u);
    if (uri == null || uri.scheme != 'https' || uri.host.isEmpty) return null;
    return u;
  }

  /// Aggiunge [incoming] agli indirizzi noti (senza mai toglierne). Il client viene
  /// ricreato solo se qualcosa è cambiato, per non interrompere trasferimenti in corso.
  Future<bool> mergeUrls(Iterable<String> incoming) async {
    final c = _connection;
    if (c == null) return false;
    final merged = [...c.urls];
    for (final raw in incoming) {
      final u = _cleanUrl(raw);
      if (u != null && !merged.contains(u)) merged.add(u);
    }
    if (merged.length > 8) merged.removeRange(8, merged.length);
    if (merged.length == c.urls.length) return false;
    await _replaceUrls(merged);
    return true;
  }

  /// Rimuove un indirizzo (ne resta sempre almeno uno).
  Future<void> removeUrl(String url) async {
    final c = _connection;
    if (c == null || c.urls.length <= 1) return;
    await _replaceUrls(c.urls.where((u) => u != url).toList());
  }

  Future<void> _replaceUrls(List<String> urls) async {
    final c = _connection!;
    final updated = ServerConnection(
      serverName: c.serverName,
      fingerprint: c.fingerprint,
      urls: urls,
      token: c.token,
      deviceId: c.deviceId,
    );
    await _storage.write(key: _key, value: jsonEncode(updated.toJson()));
    _set(updated);
  }

  /// Chiede al PC i suoi indirizzi attuali (rete locale, DuckDNS, IP pubblico…)
  /// e li aggiunge a quelli salvati. Se il PC non risponde non succede nulla.
  Future<void> refreshUrls() async {
    final cl = _client;
    if (cl == null) return;
    try {
      final info = await cl.info();
      final urls = (info['urls'] as List<dynamic>? ?? const []).whereType<String>();
      await mergeUrls(urls);
    } catch (_) {}
  }

  /// Scollega il telefono. Se [notifyServer] è vero prova anche a revocare il
  /// token sul PC (se il PC non risponde, lo scollegamento locale avviene comunque).
  Future<void> unpair({bool notifyServer = true}) async {
    if (notifyServer && _client != null) {
      try {
        await _client!.unpair();
      } catch (_) {}
    }
    await _storage.delete(key: _key);
    _set(null);
  }
}
