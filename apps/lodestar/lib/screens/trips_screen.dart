import 'package:flutter/material.dart';
import 'package:intl/intl.dart';
import 'package:provider/provider.dart';

import '../core/tracking/trip_detector.dart';
import '../state/app_state.dart';

/// Encrypted driving reports: end-of-drive summaries computed on-device
/// and shared as sealed `trip` envelopes. The server never sees routes —
/// only the apps can decrypt them.
class TripsScreen extends StatefulWidget {
  const TripsScreen({super.key, this.initialMemberId});

  final String? initialMemberId;

  @override
  State<TripsScreen> createState() => _TripsScreenState();
}

class _TripsScreenState extends State<TripsScreen> {
  String? _memberId;
  List<TripSummary> _trips = [];
  bool _loading = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    _memberId = widget.initialMemberId;
  }

  Future<void> _load(AppState state) async {
    final circleId = state.activeCircleId;
    final member = _memberId;
    if (circleId == null || member == null) return;
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final sender = state.membersByCircle[circleId]
          ?.where((m) => m.deviceId == member)
          .firstOrNull;
      final out = <TripSummary>[];
      if (sender != null) {
        var since = 0;
        while (true) {
          final envs = await state.api.getEnvelopes(
            circleId,
            since: since,
            kind: 'trip',
            limit: 1000,
          );
          if (envs.isEmpty) break;
          for (final e in envs) {
            if (e.deviceId != member) continue;
            final open = await state.crypto.openEnvelope(
              circleId: circleId,
              nonceB64: e.nonce,
              ciphertextB64: e.ciphertext,
              senderPubEd25519B64: sender.ed25519Pub,
            );
            out.add(TripSummary.fromData(open['data'] as Map<String, dynamic>));
          }
          final oldest = envs.map((e) => e.ts).reduce((a, b) => a < b ? a : b);
          if (envs.length < 1000) break;
          since = oldest;
        }
      }
      out.sort((a, b) => b.endTs.compareTo(a.endTs));
      setState(() {
        _trips = out;
        _loading = false;
      });
    } catch (e) {
      setState(() {
        _error = '$e';
        _loading = false;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    final members = state.activeMembers;

    return Scaffold(
      appBar: AppBar(
        title: const Text('Driving trips'),
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            onPressed: () => _load(state),
          ),
        ],
      ),
      body: Column(
        children: [
          Padding(
            padding: const EdgeInsets.all(12),
            child: DropdownButton<String?>(
              value: _memberId,
              isExpanded: true,
              hint: const Text('Choose a member'),
              items: [
                for (final m in members)
                  DropdownMenuItem(
                    value: m.deviceId,
                    child: Text(m.displayName),
                  ),
              ],
              onChanged: (v) {
                setState(() => _memberId = v);
                if (v != null) _load(state);
              },
            ),
          ),
          Expanded(
            child: _loading
                ? const Center(child: CircularProgressIndicator())
                : _error != null
                ? Center(
                    child: Text(
                      _error!,
                      style: const TextStyle(color: Colors.red),
                    ),
                  )
                : _trips.isEmpty
                ? Center(
                    child: Column(
                      mainAxisAlignment: MainAxisAlignment.center,
                      children: [
                        const Icon(
                          Icons.route_outlined,
                          size: 56,
                          color: Colors.grey,
                        ),
                        const SizedBox(height: 8),
                        Text(
                          _memberId == null
                              ? 'Pick a member above'
                              : 'No trips yet',
                          style: Theme.of(context).textTheme.titleMedium,
                        ),
                        const SizedBox(height: 4),
                        const Text(
                          'Trip summaries are encrypted — the server never sees routes.\n'
                          'Speeding and hard-braking are counted on-device.',
                          textAlign: TextAlign.center,
                          style: TextStyle(fontSize: 12, color: Colors.grey),
                        ),
                      ],
                    ),
                  )
                : ListView.separated(
                    padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
                    itemCount: _trips.length,
                    separatorBuilder: (_, _) => const SizedBox(height: 8),
                    itemBuilder: (context, i) {
                      final t = _trips[i];
                      return _TripCard(trip: t);
                    },
                  ),
          ),
        ],
      ),
    );
  }
}

class _TripCard extends StatelessWidget {
  const _TripCard({required this.trip});

  final TripSummary trip;

  @override
  Widget build(BuildContext context) {
    final date = DateFormat(
      'EEE, MMM d · HH:mm',
    ).format(DateTime.fromMillisecondsSinceEpoch(trip.endTs));
    final mins = (trip.durationS / 60).round();
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                const Icon(Icons.route, size: 18, color: Color(0xFF4F7CFF)),
                const SizedBox(width: 8),
                Text(date, style: Theme.of(context).textTheme.titleSmall),
                const Spacer(),
                Text(
                  '${mins < 1 ? '<1' : mins} min',
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              ],
            ),
            const SizedBox(height: 10),
            Row(
              children: [
                _Stat(
                  icon: Icons.straighten,
                  label: '${(trip.distanceM / 1000).toStringAsFixed(1)} km',
                ),
                _Stat(
                  icon: Icons.speed,
                  label: 'max ${trip.maxSpeedKmh.round()} km/h',
                ),
                _Stat(
                  icon: Icons.trending_up,
                  label: 'avg ${trip.avgSpeedKmh.round()} km/h',
                ),
              ],
            ),
            const SizedBox(height: 6),
            Row(
              children: [
                if (trip.speedingCount > 0)
                  _Warn(label: '⚠ ${trip.speedingCount} speeding'),
                if (trip.hardBrakingCount > 0) ...[
                  const SizedBox(width: 10),
                  _Warn(label: '🛑 ${trip.hardBrakingCount} hard brake'),
                ],
                if (trip.speedingCount == 0 && trip.hardBrakingCount == 0)
                  const Text(
                    'No incidents — smooth driving 👏',
                    style: TextStyle(fontSize: 12, color: Color(0xFF2E9E5B)),
                  ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

class _Stat extends StatelessWidget {
  const _Stat({required this.icon, required this.label});

  final IconData icon;
  final String label;

  @override
  Widget build(BuildContext context) {
    return Expanded(
      child: Row(
        children: [
          Icon(icon, size: 15, color: Theme.of(context).colorScheme.outline),
          const SizedBox(width: 4),
          Text(label, style: const TextStyle(fontSize: 13)),
        ],
      ),
    );
  }
}

class _Warn extends StatelessWidget {
  const _Warn({required this.label});

  final String label;

  @override
  Widget build(BuildContext context) {
    return Text(
      label,
      style: const TextStyle(fontSize: 12, color: Color(0xFFB8551E)),
    );
  }
}
