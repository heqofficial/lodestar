import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:provider/provider.dart';

import '../state/app_state.dart';
import '../widgets/member_avatar.dart';

/// Circle roster. The owner can grant the encrypted circle key to members
/// who joined without one, and remove members.
class MembersScreen extends StatelessWidget {
  const MembersScreen({super.key});

  Future<void> _grantKey(BuildContext context, String memberId) async {
    final state = context.read<AppState>();
    final member = state.activeMembers
        .where((m) => m.deviceId == memberId)
        .firstOrNull;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Grant circle key?'),
        content: Text(
          'This seals the encrypted circle key so ${member?.displayName ?? 'this member'} can read '
          'locations, places and chat. Only the owner can do this.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text('Grant'),
          ),
        ],
      ),
    );
    if (confirmed == true) {
      try {
        await state.grantCircleKeyTo(memberId);
        if (context.mounted) {
          ScaffoldMessenger.of(context).showSnackBar(
            const SnackBar(
              content: Text('🔑 Key granted — they can now read the circle'),
            ),
          );
        }
      } catch (e) {
        if (context.mounted) {
          ScaffoldMessenger.of(
            context,
          ).showSnackBar(SnackBar(content: Text('Key grant failed: $e')));
        }
      }
    }
  }

  Future<void> _removeMember(BuildContext context, String memberId) async {
    final state = context.read<AppState>();
    final member = state.activeMembers
        .where((m) => m.deviceId == memberId)
        .firstOrNull;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Remove member?'),
        content: Text(
          '${member?.displayName ?? 'This member'} will lose access to the circle.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            style: FilledButton.styleFrom(backgroundColor: Colors.red),
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text('Remove'),
          ),
        ],
      ),
    );
    if (confirmed == true) {
      try {
        await state.api.leaveCircle(state.activeCircleIdSafe, memberId);
        await state.refreshCircle(state.activeCircleIdSafe);
      } catch (e) {
        if (context.mounted) {
          ScaffoldMessenger.of(
            context,
          ).showSnackBar(SnackBar(content: Text('$e')));
        }
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    final members = state.activeMembers;
    final isOwner = state.ownerId == state.deviceId;

    return Scaffold(
      appBar: AppBar(title: const Text('Members')),
      body: ListView.separated(
        padding: const EdgeInsets.all(16),
        itemCount: members.length + 1,
        separatorBuilder: (_, _) => const SizedBox(height: 8),
        itemBuilder: (context, i) {
          if (i == 0) {
            return Card(
              child: ListTile(
                leading: const Icon(Icons.history, color: Color(0xFF4F7CFF)),
                title: const Text('Invite someone'),
                subtitle: const Text(
                  'Share the code — owner must grant the key after',
                ),
                trailing: const Icon(Icons.chevron_right),
                onTap: () async {
                  final code = await state.api.createInvite(
                    state.activeCircleIdSafe,
                  );
                  if (context.mounted) {
                    ScaffoldMessenger.of(context).showSnackBar(
                      SnackBar(
                        content: Text('Invite code: $code'),
                        action: SnackBarAction(
                          label: 'Copy',
                          onPressed: () {
                            Clipboard.setData(ClipboardData(text: code));
                          },
                        ),
                      ),
                    );
                  }
                },
              ),
            );
          }
          final m = members[i - 1];
          final mine = m.deviceId == state.deviceId;
          return Card(
            child: ListTile(
              leading: MemberAvatar(
                name: m.displayName,
                color: m.avatarColor,
                size: 40,
                showPause: !m.sharingEnabled,
              ),
              title: Text(m.displayName),
              subtitle: Text(
                mine
                    ? 'This device · ${m.role}'
                    : '${m.role}${m.sharingEnabled ? '' : ' · sharing paused'}',
              ),
              trailing: isOwner && !mine
                  ? PopupMenuButton<String>(
                      onSelected: (v) {
                        if (v == 'grant') _grantKey(context, m.deviceId);
                        if (v == 'remove') _removeMember(context, m.deviceId);
                      },
                      itemBuilder: (_) => const [
                        PopupMenuItem(
                          value: 'grant',
                          child: Text('🔑 Grant circle key'),
                        ),
                        PopupMenuItem(value: 'remove', child: Text('Remove')),
                      ],
                    )
                  : null,
            ),
          );
        },
      ),
    );
  }
}
