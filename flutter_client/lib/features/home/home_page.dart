import 'package:flutter/material.dart';
import 'package:permission_handler/permission_handler.dart';

import '../../core/backup/auto_backup.dart';
import '../../core/storage/connection_manager.dart';
import '../../core/transfer/upload_queue.dart';
import '../files/files_page.dart';
import '../photos/photos_page.dart';
import '../settings/settings_page.dart';
import '../transfers/transfers_page.dart';

class HomePage extends StatefulWidget {
  const HomePage({super.key});

  @override
  State<HomePage> createState() => _HomePageState();
}

class _HomePageState extends State<HomePage> with WidgetsBindingObserver {
  int _index = 0;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    // serve per mostrare la notifica del trasferimento in secondo piano (Android 13+)
    Permission.notification.request();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) {
      // al ritorno nell'app: indirizzi aggiornati, nuove foto da copiare, upload da riprendere
      ConnectionManager.instance.refreshUrls();
      AutoBackup.instance.scanIfDue();
      UploadQueue.instance.kick();
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: IndexedStack(
        index: _index,
        children: const [
          FilesPage(),
          PhotosPage(),
          TransfersPage(),
          SettingsPage(),
        ],
      ),
      bottomNavigationBar: ListenableBuilder(
        listenable: UploadQueue.instance,
        builder: (context, _) {
          final pending = UploadQueue.instance.pendingCount;
          final failed = UploadQueue.instance.failedCount;
          return NavigationBar(
            selectedIndex: _index,
            onDestinationSelected: (i) => setState(() => _index = i),
            destinations: [
              const NavigationDestination(
                  icon: Icon(Icons.folder_outlined),
                  selectedIcon: Icon(Icons.folder),
                  label: 'File'),
              const NavigationDestination(
                  icon: Icon(Icons.photo_library_outlined),
                  selectedIcon: Icon(Icons.photo_library),
                  label: 'Foto'),
              NavigationDestination(
                icon: Badge(
                  isLabelVisible: pending + failed > 0,
                  label: Text('${pending + failed}'),
                  child: const Icon(Icons.swap_vert),
                ),
                label: 'Trasferimenti',
              ),
              const NavigationDestination(
                  icon: Icon(Icons.settings_outlined),
                  selectedIcon: Icon(Icons.settings),
                  label: 'Impostazioni'),
            ],
          );
        },
      ),
    );
  }
}
