import 'package:flutter/material.dart';

import '../../core/transfer/upload_queue.dart';
import '../../core/util/format.dart';
import '../../core/ui/app_logo_title.dart';

class TransfersPage extends StatelessWidget {
  const TransfersPage({super.key});

  @override
  Widget build(BuildContext context) {
    final queue = UploadQueue.instance;
    return ListenableBuilder(
      listenable: queue,
      builder: (context, _) {
        final tasks = queue.tasks.reversed.toList();
        final pending = queue.pendingCount;
        return Scaffold(
          appBar: AppBar(
            title: const AppLogoTitle('Trasferimenti'),
            actions: [
              PopupMenuButton<String>(
                onSelected: (v) {
                  if (v == 'clear') queue.clearFinished();
                  if (v == 'retry') queue.retryAllFailed();
                  if (v == 'cancel') queue.cancelAll();
                },
                itemBuilder: (_) => [
                  if (queue.failedCount > 0)
                    const PopupMenuItem(value: 'retry', child: Text('Riprova quelli falliti')),
                  if (pending > 0)
                    const PopupMenuItem(value: 'cancel', child: Text('Annulla tutto')),
                  const PopupMenuItem(value: 'clear', child: Text('Rimuovi i completati')),
                ],
              ),
            ],
          ),
          body: tasks.isEmpty
              ? const Center(child: Text('Nessun trasferimento'))
              : ListView.separated(
                  itemCount: tasks.length,
                  separatorBuilder: (_, __) => const Divider(height: 1),
                  itemBuilder: (context, i) => _TaskTile(task: tasks[i], queue: queue),
                ),
        );
      },
    );
  }
}

class _TaskTile extends StatelessWidget {
  const _TaskTile({required this.task, required this.queue});
  final UploadTask task;
  final UploadQueue queue;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final t = task;
    final name = t.name.isEmpty ? (t.assetId != null ? 'Elemento della galleria' : 'File') : t.name;

    final IconData icon;
    final Color color;
    String subtitle;
    switch (t.state) {
      case TaskState.done:
        icon = Icons.check_circle;
        color = Colors.green;
        subtitle = 'Completato · ${formatSize(t.size)}';
      case TaskState.failed:
        icon = Icons.error;
        color = scheme.error;
        subtitle = t.error ?? 'Errore';
      case TaskState.cancelled:
        icon = Icons.cancel_outlined;
        color = scheme.outline;
        subtitle = 'Annullato';
      case TaskState.running:
        icon = Icons.sync;
        color = scheme.primary;
        subtitle = t.error ?? (t.phase.isNotEmpty && t.size == 0 ? t.phase : '${formatSize(t.sent)} / ${formatSize(t.size)}');
      case TaskState.queued:
        icon = Icons.schedule;
        color = scheme.outline;
        subtitle = 'In attesa';
    }

    return ListTile(
      leading: Icon(icon, color: color),
      title: Text(name, maxLines: 1, overflow: TextOverflow.ellipsis),
      subtitle: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(subtitle, maxLines: 2, overflow: TextOverflow.ellipsis),
          if (t.state == TaskState.running)
            Padding(
              padding: const EdgeInsets.only(top: 6),
              child: LinearProgressIndicator(value: t.size > 0 ? t.progress : null),
            ),
        ],
      ),
      trailing: switch (t.state) {
        TaskState.running || TaskState.queued => IconButton(
            icon: const Icon(Icons.close),
            tooltip: 'Annulla',
            onPressed: () => queue.cancel(t.id),
          ),
        TaskState.failed => Row(mainAxisSize: MainAxisSize.min, children: [
            IconButton(icon: const Icon(Icons.refresh), tooltip: 'Riprova', onPressed: () => queue.retry(t.id)),
            IconButton(icon: const Icon(Icons.delete_outline), tooltip: 'Rimuovi', onPressed: () => queue.remove(t.id)),
          ]),
        TaskState.cancelled => IconButton(
            icon: const Icon(Icons.refresh), tooltip: 'Riprova', onPressed: () => queue.retry(t.id)),
        TaskState.done => IconButton(
            icon: const Icon(Icons.delete_outline), tooltip: 'Rimuovi dall\'elenco', onPressed: () => queue.remove(t.id)),
      },
    );
  }
}
