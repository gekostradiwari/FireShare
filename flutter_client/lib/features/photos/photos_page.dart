import 'package:flutter/material.dart';

import '../backup/backup_page.dart';
import 'cloud_tab.dart';
import 'gallery_tab.dart';
import 'google_tab.dart';
import '../../core/ui/app_logo_title.dart';

/// Tre schede per le foto:
///  * «Telefono»: la galleria del dispositivo (originali, eliminazione dal telefono);
///  * «Sul PC»: la galleria del cloud, cioè ciò che è già al sicuro sul PC;
///  * «Google Foto»: le foto che vivono solo nel cloud di Google.
class PhotosPage extends StatelessWidget {
  const PhotosPage({super.key});

  @override
  Widget build(BuildContext context) {
    return DefaultTabController(
      length: 3,
      child: Scaffold(
        appBar: AppBar(
          title: const AppLogoTitle('Foto e video'),
          actions: [
            IconButton(
              tooltip: 'Backup automatico',
              icon: const Icon(Icons.backup_outlined),
              onPressed: () => Navigator.of(context)
                  .push(MaterialPageRoute<void>(builder: (_) => const BackupPage())),
            ),
          ],
          bottom: const TabBar(tabs: [
            Tab(icon: Icon(Icons.phone_android), text: 'Telefono'),
            Tab(icon: Icon(Icons.computer), text: 'Sul PC'),
            Tab(icon: Icon(Icons.cloud_outlined), text: 'Google Foto'),
          ]),
        ),
        body: const TabBarView(
          physics: NeverScrollableScrollPhysics(),
          children: [GalleryTab(), CloudTab(), GoogleTab()],
        ),
      ),
    );
  }
}
