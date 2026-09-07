import 'package:flutter/material.dart';
import 'package:flutter_map/flutter_map.dart';
import 'package:intl/intl.dart';
import 'package:latlong2/latlong.dart';
import 'package:provider/provider.dart';

import '../core/api/models.dart';
import '../state/app_state.dart';

/// Encrypted location history: fetch envelopes for a member + day, decrypt
/// on-device, and draw the route. Nothing leaves the phone in plaintext.
class HistoryScreen extends StatefulWidget {
  const HistoryScreen({super.key, this.initialMemberId});

  final String? initialMemberId;

  @override
  State<HistoryScreen> createState() => _HistoryScreenState();
}

class _HistoryScreenState extends State<HistoryScreen> {
  String? _memberId;
  DateTime _day = DateTime.now();
  List<LatLng> _track = [];
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
      final dayStart = DateTime(
        _day.year,
        _day.month,
        _day.day,
      ).millisecondsSinceEpoch;
      final dayEnd = dayStart + 24 * 3600 * 1000;
      final sender = state.membersByCircle[circleId]
          ?.where((m) => m.deviceId == member)
          .firstOrNull;
      final track = <LatLng>[];
      if (sender != null) {
        // The server caps pages at 1000 envelopes; page backwards in time
        // (oldest page first) until we reach the start of the day.
        // The server cursor is exclusive on ts, so rows sharing the page
        // boundary's ts would be skipped forever: step back 1ms and dedupe
        // by envelope id instead.
        var since = 0;
        final seen = <String>{};
        while (true) {
          final envs = await state.api.getEnvelopes(
            circleId,
            since: since,
            kind: 'location',
            device: member,
            limit: 1000,
          );
          if (envs.isEmpty) break;
          // Server returns newest first; pageOldest drives the next cursor.
          var pageOldest = envs.first.ts;
          for (final e in envs) {
            if (e.ts < pageOldest) pageOldest = e.ts;
            if (!seen.add(e.id) || e.ts < dayStart || e.ts >= dayEnd) {
              continue;
            }
            final open = await state.crypto.openEnvelope(
              circleId: circleId,
              nonceB64: e.nonce,
              ciphertextB64: e.ciphertext,
              senderPubEd25519B64: sender.ed25519Pub,
            );
            final d = open['data'] as Map<String, dynamic>;
            track.add(
              LatLng(
                (d['lat'] as num).toDouble(),
                (d['lng'] as num).toDouble(),
              ),
            );
          }
          if (envs.length < 1000 || pageOldest <= dayStart) break;
          since = pageOldest - 1;
        }
      }
      setState(() {
        _track = track.reversed.toList();
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
    final member = members.where((m) => m.deviceId == _memberId).firstOrNull;

    return Scaffold(
      appBar: AppBar(
        title: const Text('Location history'),
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
            child: Row(
              children: [
                Expanded(
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
                const SizedBox(width: 8),
                OutlinedButton.icon(
                  icon: const Icon(Icons.calendar_today, size: 16),
                  label: Text(DateFormat('MMM d').format(_day)),
                  onPressed: () async {
                    final picked = await showDatePicker(
                      context: context,
                      initialDate: _day,
                      firstDate: DateTime.now().subtract(
                        const Duration(days: 365),
                      ),
                      lastDate: DateTime.now(),
                    );
                    if (picked != null) setState(() => _day = picked);
                  },
                ),
              ],
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
                : _track.isEmpty
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
                          member == null
                              ? 'Pick a member above'
                              : 'No locations on ${DateFormat('MMM d').format(_day)}',
                          style: Theme.of(context).textTheme.titleMedium,
                        ),
                        const SizedBox(height: 4),
                        const Text(
                          'History is encrypted — the server never sees these routes.',
                          style: TextStyle(fontSize: 12, color: Colors.grey),
                        ),
                      ],
                    ),
                  )
                : _TrackMap(track: _track, member: member),
          ),
        ],
      ),
    );
  }
}

class _TrackMap extends StatelessWidget {
  const _TrackMap({required this.track, required this.member});

  final List<LatLng> track;
  final CircleMember? member;

  @override
  Widget build(BuildContext context) {
    final start = track.first;
    final end = track.last;
    return FlutterMap(
      options: MapOptions(
        initialCenter: start,
        initialZoom: 14,
        interactionOptions: const InteractionOptions(
          flags: InteractiveFlag.all & ~InteractiveFlag.rotate,
        ),
      ),
      children: [
        TileLayer(
          urlTemplate: 'https://tile.openstreetmap.org/{z}/{x}/{y}.png',
          userAgentPackageName: 'dev.lodestar.app',
          maxNativeZoom: 19,
        ),
        if (track.length > 1)
          PolylineLayer(
            polylines: [
              Polyline(
                points: track,
                strokeWidth: 4,
                color: Color(
                  int.tryParse(
                        member?.avatarColor.replaceAll('#', 'FF') ?? '',
                        radix: 16,
                      ) ??
                      0xFF4F7CFF,
                ),
              ),
            ],
          ),
        MarkerLayer(
          markers: [
            Marker(
              point: start,
              width: 36,
              height: 36,
              child: const Icon(
                Icons.trip_origin,
                color: Color(0xFF2E9E5B),
                size: 24,
              ),
            ),
            Marker(
              point: end,
              width: 36,
              height: 36,
              child: const Icon(
                Icons.place,
                color: Color(0xFFE05D5D),
                size: 32,
              ),
            ),
          ],
        ),
      ],
    );
  }
}
