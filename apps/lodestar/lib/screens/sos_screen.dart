import 'package:flutter/material.dart';
import 'package:provider/provider.dart';

import '../state/app_state.dart';

/// Panic button: broadcasts an encrypted SOS with the latest known location
/// to the whole circle (and, if configured, to the circle's ntfy topic).
class SosScreen extends StatefulWidget {
  const SosScreen({super.key});

  @override
  State<SosScreen> createState() => _SosScreenState();
}

class _SosScreenState extends State<SosScreen> {
  String _outcome = ''; // '', 'sent', 'queued', 'failed'
  bool _busy = false;

  bool get _done => _outcome != '';

  Future<void> _trigger() async {
    setState(() => _busy = true);
    try {
      final outcome = await context.read<AppState>().sendSos();
      if (mounted) {
        setState(() => _outcome = outcome);
        final (text, color) = switch (outcome) {
          'sent' => ('🚨 SOS sent to your circle', const Color(0xFF2E9E5B)),
          'queued' => (
            '⚠️ Offline — SOS queued, retrying automatically',
            const Color(0xFFB8551E),
          ),
          _ => ('SOS could not be sent — no circle key granted',
              const Color(0xFFE05D5D)),
        };
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text(text), backgroundColor: color),
        );
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final color = switch (_outcome) {
      'sent' => const Color(0xFF2E9E5B),
      'queued' => const Color(0xFFB8551E),
      _ => const Color(0xFFE05D5D),
    };
    final label = switch (_outcome) {
      'sent' => 'SOS SENT',
      'queued' => 'SOS QUEUED',
      _ => 'PRESS TO SEND',
    };
    final icon = switch (_outcome) {
      'sent' => Icons.check_circle,
      'queued' => Icons.schedule,
      'failed' => Icons.error_outline,
      _ => Icons.emergency,
    };
    return Scaffold(
      appBar: AppBar(title: const Text('SOS')),
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisAlignment: MainAxisAlignment.center,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const Text(
                'Send an emergency alert with your live location to everyone in your circle.',
                textAlign: TextAlign.center,
                style: TextStyle(fontSize: 16),
              ),
              const SizedBox(height: 32),
              Center(
                child: GestureDetector(
                  onTap: _busy || _done ? null : _trigger,
                  child: AnimatedContainer(
                    duration: const Duration(milliseconds: 300),
                    width: _done ? 180 : 200,
                    height: _done ? 180 : 200,
                    decoration: BoxDecoration(
                      shape: BoxShape.circle,
                      color: color,
                      boxShadow: [
                        BoxShadow(
                          color: color.withValues(alpha: 0.4),
                          blurRadius: 32,
                          spreadRadius: 4,
                        ),
                      ],
                    ),
                    child: Column(
                      mainAxisAlignment: MainAxisAlignment.center,
                      children: [
                        Icon(icon, color: Colors.white, size: 64),
                        const SizedBox(height: 8),
                        Text(
                          label,
                          style: const TextStyle(
                            color: Colors.white,
                            fontSize: 18,
                            fontWeight: FontWeight.w800,
                            letterSpacing: 1.2,
                          ),
                        ),
                        if (_busy) ...[
                          const SizedBox(height: 8),
                          const SizedBox(
                            width: 20,
                            height: 20,
                            child: CircularProgressIndicator(
                              strokeWidth: 2,
                              color: Colors.white,
                            ),
                          ),
                        ],
                      ],
                    ),
                  ),
                ),
              ),
              const SizedBox(height: 24),
              Text(
                _outcome == 'queued'
                    ? 'You appear to be offline. Lodestar is retrying automatically — the alert will go out as soon as you reconnect.'
                    : 'Use for real emergencies only. Everyone in your circle will see your location immediately.',
                textAlign: TextAlign.center,
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ],
          ),
        ),
      ),
    );
  }
}
