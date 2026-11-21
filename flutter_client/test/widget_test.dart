import 'package:flutter_client/core/api/pairing_payload.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  const fp = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';

  test('legge il link di accoppiamento mostrato dal PC', () {
    final p = PairingPayload.tryParse(
      'fireshare://pair?v=1&c=ABCD2345&fp=$fp&n=Mio+PC'
      '&u=https%3A%2F%2F192.168.1.20%3A8443&u=https%3A%2F%2Fmiopc.duckdns.org%3A8443%2F',
    );
    expect(p, isNotNull);
    expect(p!.code, 'ABCD2345');
    expect(p.fingerprint, fp);
    expect(p.serverName, 'Mio PC');
    expect(p.urls, ['https://192.168.1.20:8443', 'https://miopc.duckdns.org:8443']);
  });

  test('rifiuta link non validi o non sicuri', () {
    expect(PairingPayload.tryParse('https://example.com'), isNull);
    expect(PairingPayload.tryParse('fireshare://pair?c=A&fp=zz&u=https%3A%2F%2Fx'), isNull);
    // niente HTTP in chiaro
    expect(PairingPayload.tryParse('fireshare://pair?v=1&c=ABCD2345&fp=$fp&u=http%3A%2F%2F10.0.0.1%3A8443'), isNull);
    // senza codice
    expect(PairingPayload.tryParse('fireshare://pair?v=1&fp=$fp&u=https%3A%2F%2F10.0.0.1%3A8443'), isNull);
  });
}
