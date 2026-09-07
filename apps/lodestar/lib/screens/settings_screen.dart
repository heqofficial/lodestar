import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:provider/provider.dart';

import '../state/app_state.dart';
import '../widgets/member_avatar.dart';

/// Privacy-first settings: sharing control, key management, and circle
/// membership. Consent is the product — pausing is visible to the circle.
class SettingsScreen extends StatelessWidget {
  const SettingsScreen({super.key});

  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    final self = state.selfMember;
    final keyGrantNeeded = state.activeMembers
        .where((m) => m.deviceId != state.deviceId)
        .length;

    return Scaffold(
      appBar: AppBar(title: const Text('Settings')),
      body: ListView(
        padding: const EdgeInsets.all(16),
        children: [
          // --- sharing card ---
          Card(
            child: Column(
              children: [
                SwitchListTile(
                  secondary: const Icon(Icons.share_location),
                  title: const Text('Share my location'),
                  subtitle: Text(
                    state.sharing
                        ? 'Your circle can see you in real time'
                        : 'Paused — your circle can see that you paused',
                  ),
                  value: state.sharing,
                  onChanged: (v) => state.setSharing(v),
                ),
                ListTile(
                  leading: const Icon(Icons.play_circle_outline),
                  title: const Text('Start background tracking'),
                  subtitle: const Text(
                    'Runs with a visible notification, battery-aware',
                  ),
                  trailing: state.tracking
                      ? const Icon(Icons.check_circle, color: Color(0xFF2E9E5B))
                      : const Icon(Icons.chevron_right),
                  onTap: () => state.startTracking(),
                ),
                Padding(
                  padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        children: [
                          const Icon(Icons.speed, size: 16),
                          const SizedBox(width: 8),
                          Text(
                            'Speeding alert: ${state.speedingLimitKmh.round()} km/h',
                            style: Theme.of(context).textTheme.bodyMedium,
                          ),
                        ],
                      ),
                      Slider(
                        value: state.speedingLimitKmh.clamp(60, 130),
                        min: 60,
                        max: 130,
                        divisions: 14,
                        label: '${state.speedingLimitKmh.round()} km/h',
                        onChanged: (v) => state.setSpeedingLimit(v),
                      ),
                      Text(
                        'Applied to new trips. Hard braking and crash detection run automatically.',
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ],
                  ),
                ),
              ],
            ),
          ),

          // --- circle card ---
          Card(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Padding(
                  padding: const EdgeInsets.fromLTRB(16, 14, 16, 4),
                  child: Text(
                    'Circle',
                    style: Theme.of(context).textTheme.labelLarge,
                  ),
                ),
                ListTile(
                  leading: const Icon(Icons.people_outline),
                  title: Text('${state.activeMembers.length} members'),
                  subtitle: Text(
                    state.activeCircleIdSafe.isEmpty
                        ? 'No active circle'
                        : 'Tap a member to grant the key',
                  ),
                  onTap: () => Navigator.of(context).pushNamed('/members'),
                ),
                if (keyGrantNeeded > 0 && state.ownerId == state.deviceId)
                  Padding(
                    padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
                    child: Text(
                      'You own this circle. New members need you to grant them the encrypted key '
                      '(Members → grant). Without it they cannot read anything.',
                      style: TextStyle(
                        fontSize: 12,
                        color: Theme.of(context).colorScheme.outline,
                      ),
                    ),
                  ),
              ],
            ),
          ),

          // --- identity card ---
          Card(
            child: Column(
              children: [
                ListTile(
                  leading: self == null
                      ? const Icon(Icons.person_outline)
                      : MemberAvatar(
                          name: self.displayName,
                          color: self.avatarColor,
                          size: 36,
                        ),
                  title: Text(
                    state.deviceName.isEmpty ? 'This device' : state.deviceName,
                  ),
                  subtitle: Text('Server: ${state.serverUrl}'),
                ),
                ListTile(
                  leading: const Icon(Icons.key_outlined),
                  title: const Text('My keys'),
                  subtitle: const Text(
                    'Ed25519 + X25519, stored in the OS keystore',
                  ),
                  onTap: () => _showKeys(context, state),
                ),
                ListTile(
                  leading: const Icon(Icons.verified_user_outlined),
                  title: const Text('Privacy & threat model'),
                  subtitle: const Text(
                    'End-to-end encryption — even the server can\u2019t read locations',
                  ),
                  onTap: () => _showPrivacy(context),
                ),
              ],
            ),
          ),

          const SizedBox(height: 24),
          Text(
            'Lodestar · open source (AGPL-3.0) · no tracking · no data selling\nYour family\u2019s guiding star ⭐',
            textAlign: TextAlign.center,
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ],
      ),
    );
  }

  void _showKeys(BuildContext context, AppState state) {
    showDialog<void>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Device keys'),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text(
              'Identity keys are generated on this device and never leave it. '
              'Back them up by copying the fingerprints:',
            ),
            const SizedBox(height: 12),
            SelectableText('Ed25519:  ${state.crypto.ed25519PubB64}'),
            const SizedBox(height: 8),
            SelectableText('X25519:   ${state.crypto.x25519PubB64}'),
            const SizedBox(height: 12),
            const Text(
              'If this device is lost, reinstall the app and rejoin with the invite code — '
              'the owner will re-grant the circle key.',
            ),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Close'),
          ),
        ],
      ),
    );
  }

  void _showPrivacy(BuildContext context) {
    showDialog<void>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Privacy by architecture'),
        content: const SingleChildScrollView(
          child: Text(
            '• Locations, places, chat, check-ins and SOS are sealed with the circle key\n'
            '• The server stores ciphertext only — even the operator cannot read it\n'
            '• Geofences are evaluated on your phone\n'
            '• No accounts, no phone number, no analytics, no trackers\n'
            '• Pausing is visible to your circle: trust, not surveillance\n'
            '• You can self-host everything, so no third party is ever involved\n\n'
            'Full details: docs/threat-model.md in the repository.',
          ),
        ),
        actions: [
          TextButton(
            onPressed: () {
              Clipboard.setData(
                const ClipboardData(
                  text: 'https://github.com/heqofficial/lodestar',
                ),
              );
              Navigator.pop(ctx);
            },
            child: const Text('Copy repo link'),
          ),
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Close'),
          ),
        ],
      ),
    );
  }
}
