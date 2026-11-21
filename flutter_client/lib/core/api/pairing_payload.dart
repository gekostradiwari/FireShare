/// Contenuto del QR di accoppiamento mostrato dal PC:
/// `fireshare://pair?v=1&c=CODICE&fp=IMPRONTA&n=NOME&u=URL1&u=URL2`
class PairingPayload {
  const PairingPayload({
    required this.code,
    required this.fingerprint,
    required this.serverName,
    required this.urls,
  });

  final String code;
  final String fingerprint;
  final String serverName;
  final List<String> urls;

  /// Restituisce `null` se il testo non è un link FireShare valido.
  static PairingPayload? tryParse(String raw) {
    final uri = Uri.tryParse(raw.trim());
    if (uri == null || uri.scheme != 'fireshare' || uri.host != 'pair') {
      return null;
    }
    final q = uri.queryParametersAll;
    final code = q['c']?.first ?? '';
    final fp = (q['fp']?.first ?? '').toLowerCase();
    final urls = (q['u'] ?? const <String>[])
        .map((u) => u.trim().replaceAll(RegExp(r'/+$'), ''))
        .where((u) => u.startsWith('https://'))
        .toList();
    if (code.isEmpty || !RegExp(r'^[0-9a-f]{64}$').hasMatch(fp) || urls.isEmpty) {
      return null;
    }
    return PairingPayload(
      code: code,
      fingerprint: fp,
      serverName: q['n']?.first ?? 'FireShare',
      urls: urls,
    );
  }
}
