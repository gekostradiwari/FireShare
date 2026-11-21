String formatSize(int bytes) {
  if (bytes < 1024) return '$bytes B';
  const units = ['KB', 'MB', 'GB', 'TB'];
  var v = bytes / 1024;
  var i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return '${v.toStringAsFixed(v >= 100 ? 0 : 1)} ${units[i]}';
}

String two(int n) => n.toString().padLeft(2, '0');

String formatDate(DateTime? d) {
  if (d == null) return '';
  return '${two(d.day)}/${two(d.month)}/${d.year} ${two(d.hour)}:${two(d.minute)}';
}

/// «scaduto», «tra 3 giorni», «tra 5 ore», «tra 12 minuti».
String formatRemaining(DateTime? d, {DateTime? now}) {
  if (d == null) return '';
  final diff = d.difference(now ?? DateTime.now());
  if (diff.isNegative) return 'scaduto';
  if (diff.inDays >= 2) return 'tra ${diff.inDays} giorni';
  if (diff.inHours >= 2) return 'tra ${diff.inHours} ore';
  if (diff.inMinutes >= 2) return 'tra ${diff.inMinutes} minuti';
  return 'tra meno di un minuto';
}

/// Tipo di rete a cui appartiene un indirizzo del server, per l'interfaccia.
enum NetworkKind { home, tailscale, internet }

NetworkKind networkKindOf(String url) {
  final host = Uri.tryParse(url)?.host ?? '';
  final m = RegExp(r'^(\d+)\.(\d+)\.\d+\.\d+$').firstMatch(host);
  if (m != null) {
    final a = int.parse(m.group(1)!);
    final b = int.parse(m.group(2)!);
    if (a == 10 || (a == 192 && b == 168) || (a == 172 && b >= 16 && b <= 31) || (a == 169 && b == 254)) {
      return NetworkKind.home;
    }
    if (a == 100 && b >= 64 && b <= 127) return NetworkKind.tailscale;
    return NetworkKind.internet;
  }
  if (host.endsWith('.local') || !host.contains('.')) return NetworkKind.home;
  if (host.endsWith('.ts.net')) return NetworkKind.tailscale;
  return NetworkKind.internet;
}
