import 'dart:async';

import 'package:flutter/widgets.dart';
import 'package:flutter_background_service/flutter_background_service.dart';

/// Punto di ingresso del servizio in primo piano. Non fa nulla da solo: serve
/// a tenere vivo il processo (e quindi gli upload) quando l'app va in secondo
/// piano, mostrando la notifica "FireShare sta trasferendo file".
@pragma('vm:entry-point')
void fireShareServiceMain(ServiceInstance service) {
  WidgetsFlutterBinding.ensureInitialized();
  service.on('stop').listen((_) => service.stopSelf());
  service.on('update').listen((event) {
    if (service is AndroidServiceInstance && event != null) {
      service.setForegroundNotificationInfo(
        title: (event['title'] as String?) ?? 'FireShare',
        content: (event['content'] as String?) ?? '',
      );
    }
  });
}

class BackgroundKeeper {
  BackgroundKeeper._();

  static final _service = FlutterBackgroundService();
  static bool _configured = false;

  static Future<void> init() async {
    if (_configured) return;
    await _service.configure(
      androidConfiguration: AndroidConfiguration(
        onStart: fireShareServiceMain,
        autoStart: false,
        isForegroundMode: true,
        initialNotificationTitle: 'FireShare',
        initialNotificationContent: 'Trasferimento in corso…',
      ),
      iosConfiguration: IosConfiguration(autoStart: false),
    );
    _configured = true;
  }

  // Il servizio resta attivo finché c'è almeno un "possessore" (coda di upload,
  // importazione da Google Foto…). Ogni start() va chiuso da uno stop().
  static int _holders = 0;

  /// Vero nel processo di lavoro in background (WorkManager): lì il sistema
  /// tiene già vivo il processo e non serve il servizio in primo piano.
  static bool disabled = false;

  static Future<void> start() async {
    if (disabled) return;
    _holders++;
    try {
      if (!await _service.isRunning()) await _service.startService();
    } catch (_) {
      // senza servizio in primo piano gli upload continuano finché l'app è aperta
    }
  }

  static void update(String title, String content) {
    if (disabled) return;
    try {
      _service.invoke('update', {'title': title, 'content': content});
    } catch (_) {}
  }

  static Future<void> stop() async {
    if (disabled) return;
    if (_holders > 0) _holders--;
    if (_holders > 0) return;
    try {
      if (await _service.isRunning()) _service.invoke('stop');
    } catch (_) {}
  }
}
