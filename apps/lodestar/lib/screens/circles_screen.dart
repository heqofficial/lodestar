import 'package:flutter/material.dart';
import 'package:provider/provider.dart';

import '../state/app_state.dart';

/// Circle list + create/join flows. Tapping a circle opens the map.
class CirclesScreen extends StatelessWidget {
  const CirclesScreen({super.key});

  Future<void> _createCircle(BuildContext context) async {
    final name = await showDialog<String>(
      context: context,
      builder: (ctx) =>
          const _NameDialog(title: 'New circle', hint: 'e.g. The Nelsons'),
    );
    if (!context.mounted || name == null || name.trim().isEmpty) return;
    final state = context.read<AppState>();
    await state.createCircle(name.trim(), _randomColor());
    if (context.mounted) {
      Navigator.of(context).pushNamed('/map');
    }
  }

  Future<void> _joinCircle(BuildContext context) async {
    final code = await showDialog<String>(
      context: context,
      builder: (ctx) => const _NameDialog(
        title: 'Join a circle',
        hint: 'Enter the invite code',
        uppercase: true,
      ),
    );
    if (!context.mounted || code == null || code.trim().isEmpty) return;
    try {
      final state = context.read<AppState>();
      await state.joinCircle(code.trim());
      if (context.mounted) {
        Navigator.of(context).pushNamed('/map');
      }
    } catch (e) {
      if (context.mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(SnackBar(content: Text('$e')));
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    return Scaffold(
      appBar: AppBar(title: const Text('Your circles')),
      body: state.circles.isEmpty
          ? const _EmptyState()
          : ListView.separated(
              padding: const EdgeInsets.all(16),
              itemCount: state.circles.length,
              separatorBuilder: (_, _) => const SizedBox(height: 10),
              itemBuilder: (context, i) {
                final circle = state.circles[i];
                final members = state.membersByCircle[circle.id] ?? const [];
                return Card(
                  clipBehavior: Clip.antiAlias,
                  child: ListTile(
                    leading: CircleAvatar(
                      backgroundColor: _hex(circle.color),
                      child: const Icon(
                        Icons.family_restroom,
                        color: Colors.white,
                      ),
                    ),
                    title: Text(circle.name),
                    subtitle: Text(
                      members.isEmpty
                          ? 'Tap to open'
                          : '${members.length} member${members.length == 1 ? '' : 's'}',
                    ),
                    trailing: const Icon(Icons.chevron_right),
                    onTap: () {
                      context.read<AppState>().selectCircle(circle.id);
                      Navigator.of(context).pushNamed('/map');
                    },
                    onLongPress: () => _showCircleMenu(context, circle.id),
                  ),
                );
              },
            ),
      floatingActionButton: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.end,
        children: [
          FloatingActionButton.extended(
            heroTag: 'join',
            onPressed: () => _joinCircle(context),
            icon: const Icon(Icons.group_add_outlined),
            label: const Text('Join'),
          ),
          const SizedBox(height: 10),
          FloatingActionButton.extended(
            heroTag: 'create',
            onPressed: () => _createCircle(context),
            icon: const Icon(Icons.add),
            label: const Text('New circle'),
          ),
        ],
      ),
    );
  }

  void _showCircleMenu(BuildContext context, String circleId) {
    final state = context.read<AppState>();
    showModalBottomSheet<void>(
      context: context,
      builder: (ctx) => SafeArea(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            ListTile(
              leading: const Icon(Icons.refresh),
              title: const Text('Refresh members & keys'),
              onTap: () {
                Navigator.pop(ctx);
                state.refreshCircle(circleId);
              },
            ),
            ListTile(
              leading: const Icon(Icons.share),
              title: const Text('Copy invite code'),
              subtitle: Text(
                state.circles
                        .where((c) => c.id == circleId)
                        .firstOrNull
                        ?.inviteCode ??
                    '',
              ),
              onTap: () {
                Navigator.pop(ctx);
                final code =
                    state.circles
                        .where((c) => c.id == circleId)
                        .firstOrNull
                        ?.inviteCode ??
                    '';
                if (code.isNotEmpty) {
                  ScaffoldMessenger.of(
                    context,
                  ).showSnackBar(SnackBar(content: Text('Invite code: $code')));
                }
              },
            ),
            const SizedBox(height: 8),
          ],
        ),
      ),
    );
  }
}

class _EmptyState extends StatelessWidget {
  const _EmptyState();

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            const Icon(Icons.family_restroom, size: 64, color: Colors.grey),
            const SizedBox(height: 16),
            Text(
              'No circles yet',
              style: Theme.of(context).textTheme.titleLarge,
            ),
            const SizedBox(height: 8),
            Text(
              'Create a circle for your family, then share the invite code.\nEveryone gets their own encrypted key on their own phone.',
              textAlign: TextAlign.center,
              style: Theme.of(context).textTheme.bodyMedium,
            ),
          ],
        ),
      ),
    );
  }
}

class _NameDialog extends StatelessWidget {
  const _NameDialog({
    required this.title,
    required this.hint,
    this.uppercase = false,
  });

  final String title;
  final String hint;
  final bool uppercase;

  @override
  Widget build(BuildContext context) {
    final ctrl = TextEditingController();
    return AlertDialog(
      title: Text(title),
      content: TextField(
        controller: ctrl,
        autofocus: true,
        textCapitalization: uppercase
            ? TextCapitalization.characters
            : TextCapitalization.words,
        decoration: InputDecoration(hintText: hint),
        onSubmitted: (_) => Navigator.pop(context, ctrl.text),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: const Text('Cancel'),
        ),
        FilledButton(
          onPressed: () => Navigator.pop(context, ctrl.text),
          child: const Text('OK'),
        ),
      ],
    );
  }
}

const _palette = [
  '#4f7cff',
  '#e05d5d',
  '#2e9e5b',
  '#f2a03d',
  '#9b59b6',
  '#1abc9c',
  '#e67e22',
  '#3498db',
];

String _randomColor() =>
    _palette[DateTime.now().millisecondsSinceEpoch % _palette.length];

Color _hex(String hex) {
  var h = hex.replaceAll('#', '');
  if (h.length == 6) h = 'FF$h';
  return Color(int.tryParse(h, radix: 16) ?? 0xFF4F7CFF);
}
