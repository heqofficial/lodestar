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
  bool _sent = false;
  bool _busy = false;

  Future<void> _trigger() async {
    setState(() => _busy = true);
    try {
      await context.read<AppState>().sendSos();
      if (mounted) {
        setState(() => _sent = true);
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('🚨 SOS sent to your circle'),
            backgroundColor: Color(0xFFE05D5D),
          ),
        );
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
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
                  onTap: _busy || _sent ? null : _trigger,
                  child: AnimatedContainer(
                    duration: const Duration(milliseconds: 300),
                    width: _sent ? 180 : 200,
                    height: _sent ? 180 : 200,
                    decoration: BoxDecoration(
                      shape: BoxShape.circle,
                      color: _sent ? const Color(0xFF2E9E5B) : const Color(0xFFE05D5D),
                      boxShadow: [
                        BoxShadow(
                          color: (_sent ? const Color(0xFF2E9E5B) : const Color(0xFFE05D5D)).withValues(alpha: 0.4),
                          blurRadius: 32,
                          spreadRadius: 4,
                        ),
                      ],
                    ),
                    child: Column(
                      mainAxisAlignment: MainAxisAlignment.center,
                      children: [
                        Icon(
                          _sent ? Icons.check_circle : Icons.emergency,
                          color: Colors.white,
                          size: 64,
                        ),
                        const SizedBox(height: 8),
                        Text(
                          _sent ? 'SOS SENT' : 'PRESS TO SEND',
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
                            child: CircularProgressIndicator(strokeWidth: 2, color: Colors.white),
                          ),
                        ],
                      ],
                    ),
                  ),
                ),
              ),
              const SizedBox(height: 24),
              Text(
                'Use for real emergencies only. Everyone in your circle will see your location immediately.',
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