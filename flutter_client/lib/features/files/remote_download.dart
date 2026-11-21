import 'dart:io';

import 'package:flutter/material.dart';
import 'package:path/path.dart' as p;
import 'package:path_provider/path_provider.dart';

import '../../core/api/fireshare_client.dart';
import '../../core/storage/connection_manager.dart';

/// Scarica un file del PC in una cartella temporanea (con ripresa e barra di
/// avanzamento) e restituisce il percorso locale, oppure `null` se annullato o fallito.
Future<File?> downloadRemoteToTemp(
  BuildContext context, {
  required String path,
  required String name,
  required int size,
}) async {
  final tmp = await getTemporaryDirectory();
  final dir = Directory(p.join(tmp.path, 'downloads', path.hashCode.toRadixString(16)));
  await dir.create(recursive: true);
  final dest = File(p.join(dir.path, name));
  final part = File('${dest.path}.part');
  if (await dest.exists()) {
    // già scaricato: se la dimensione coincide lo si riusa
    if (await dest.length() == size) return dest;
    await dest.delete();
  }
  if (!context.mounted) return null;

  var cancelled = false;
  final progress = ValueNotifier<double?>(0);
  final messenger = ScaffoldMessenger.of(context);
  final navigator = Navigator.of(context, rootNavigator: true);
  showDialog<void>(
    context: context,
    barrierDismissible: false,
    builder: (ctx) => AlertDialog(
      title: Text(name, maxLines: 1, overflow: TextOverflow.ellipsis),
      content: ValueListenableBuilder<double?>(
        valueListenable: progress,
        builder: (_, v, __) => Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            LinearProgressIndicator(value: v),
            const SizedBox(height: 8),
            Text(v == null ? 'Download…' : '${(v * 100).round()}%'),
          ],
        ),
      ),
      actions: [
        TextButton(onPressed: () => cancelled = true, child: const Text('Annulla')),
      ],
    ),
  );
  try {
    await ConnectionManager.instance.client.download(
      path,
      part,
      isCancelled: () => cancelled,
      onProgress: (got, total) => progress.value = total > 0 ? got / total : null,
    );
    await part.rename(dest.path);
    return dest;
  } on ApiException catch (err) {
    if (err.code != 'cancelled') {
      messenger
        ..hideCurrentSnackBar()
        ..showSnackBar(SnackBar(content: Text(err.message)));
    }
    return null;
  } finally {
    navigator.pop();
  }
}
