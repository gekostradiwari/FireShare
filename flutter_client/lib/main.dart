import 'dart:async';

import 'package:flutter/material.dart';
import 'package:workmanager/workmanager.dart';

import 'core/backup/auto_backup.dart';
import 'core/storage/connection_manager.dart';
import 'core/transfer/background_keeper.dart';
import 'core/transfer/upload_queue.dart';
import 'features/home/home_page.dart';
import 'features/pairing/pairing_page.dart';
import 'features/splash/splash_gate.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  await BackgroundKeeper.init();
  try {
    await Workmanager()
        .initialize(backupCallbackDispatcher, isInDebugMode: false);
  } catch (_) {
    // senza WorkManager il backup parte comunque all'apertura dell'app
  }
  await ConnectionManager.instance.load();
  await UploadQueue.instance.load();
  await AutoBackup.instance.load();
  runApp(const FireShareApp());
  // riprende gli upload rimasti in sospeso dall'ultima volta
  UploadQueue.instance.kick();
  // aggiorna gli indirizzi del PC (DuckDNS, IP pubblico…) e cerca foto nuove da copiare
  unawaited(ConnectionManager.instance.refreshUrls());
  unawaited(AutoBackup.instance.scanIfDue());
}

class FireShareApp extends StatelessWidget {
  const FireShareApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'FireShare',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: Colors.amberAccent),
        useMaterial3: true,
      ),
      darkTheme: ThemeData(
        colorScheme: ColorScheme.fromSeed(
          seedColor: Colors.amberAccent,
          brightness: Brightness.dark,
        ),
        useMaterial3: true,
      ),
      home: SplashGate(
        child: ListenableBuilder(
          listenable: ConnectionManager.instance,
          builder: (context, _) => ConnectionManager.instance.isPaired
              ? const HomePage()
              : const PairingPage(),
        ),
      ),
    );
  }
}
