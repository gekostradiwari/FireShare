import 'package:flutter_client/core/backup/auto_backup.dart';
import 'package:flutter_client/core/util/format.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('riconosce il tipo di rete di un indirizzo', () {
    expect(networkKindOf('https://192.168.1.20:8443'), NetworkKind.home);
    expect(networkKindOf('https://10.0.0.5:8443'), NetworkKind.home);
    expect(networkKindOf('https://172.20.1.1:8443'), NetworkKind.home);
    expect(networkKindOf('https://172.32.1.1:8443'), NetworkKind.internet);
    expect(networkKindOf('https://100.100.1.2:8443'), NetworkKind.tailscale);
    expect(networkKindOf('https://pc.tail1234.ts.net'), NetworkKind.tailscale);
    expect(networkKindOf('https://miopc.duckdns.org:8443'), NetworkKind.internet);
    expect(networkKindOf('https://93.184.216.34:8443'), NetworkKind.internet);
    expect(networkKindOf('https://miopc.local:8443'), NetworkKind.home);
  });

  test('descrive il tempo che manca alla scadenza', () {
    final now = DateTime(2026, 11, 15, 12);
    expect(formatRemaining(now.subtract(const Duration(minutes: 1)), now: now), 'scaduto');
    expect(formatRemaining(now.add(const Duration(days: 7)), now: now), 'tra 7 giorni');
    expect(formatRemaining(now.add(const Duration(hours: 5)), now: now), 'tra 5 ore');
    expect(formatRemaining(now.add(const Duration(minutes: 12)), now: now), 'tra 12 minuti');
    expect(formatRemaining(now.add(const Duration(seconds: 30)), now: now), 'tra meno di un minuto');
    expect(formatRemaining(null), '');
  });

  test('le impostazioni del backup sopravvivono al salvataggio', () {
    final c = BackupConfig(
      enabled: true,
      albumIds: ['a1', 'b2'],
      wifiOnly: false,
      chargingOnly: true,
      includeExisting: true,
      since: DateTime.utc(2026, 11, 1),
      lastResult: 'ok',
    );
    final back = BackupConfig.fromJson(c.toJson());
    expect(back.enabled, isTrue);
    expect(back.albumIds, ['a1', 'b2']);
    expect(back.wifiOnly, isFalse);
    expect(back.chargingOnly, isTrue);
    expect(back.includeExisting, isTrue);
    expect(back.since, DateTime.utc(2026, 11, 1));
    expect(back.lastResult, 'ok');
    // un file vuoto o corrotto dà i valori predefiniti (Wi-Fi sì, backup spento)
    final d = BackupConfig.fromJson({});
    expect(d.enabled, isFalse);
    expect(d.wifiOnly, isTrue);
    expect(d.albumIds, isEmpty);
  });
}
