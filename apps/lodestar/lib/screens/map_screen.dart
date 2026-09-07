import 'package:flutter/material.dart';
import 'package:flutter_map/flutter_map.dart';
import 'package:latlong2/latlong.dart';
import 'package:provider/provider.dart';

import '../core/api/models.dart';
import '../state/app_state.dart';
import '../widgets/member_avatar.dart';

/// The family map: live member positions, places, and quick actions
/// (check-in, SOS, chat, history, places, settings).
class MapScreen extends StatefulWidget {
  const MapScreen({super.key});

  @override
  State<MapScreen> createState() => _MapScreenState();
}

class _MapScreenState extends State<MapScreen> {
  final MapController _mapController = MapController();
  LatLng? _myLatLng;

  static final _osmLayer = TileLayer(
    urlTemplate: 'https://tile.openstreetmap.org/{z}/{x}/{y}.png',
    userAgentPackageName: 'dev.lodestar.app',
    maxNativeZoom: 19,
  );

  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    final circle = state.circles
        .where((c) => c.id == state.activeCircleId)
        .firstOrNull;

    // Center on our own latest position if we have one.
    final myPos = state.positionsByDevice[state.deviceId];
    if (myPos != null) {
      _myLatLng = LatLng(myPos.lat, myPos.lng);
    }

    return Scaffold(
      appBar: AppBar(
        title: Text(circle?.name ?? 'Lodestar'),
        actions: [
          IconButton(
            icon: const Icon(Icons.task_alt),
            tooltip: 'Check in',
            onPressed: () => _checkIn(context),
          ),
          IconButton(
            icon: const Icon(Icons.chat_bubble_outline),
            tooltip: 'Circle chat',
            onPressed: () => Navigator.of(context).pushNamed('/chat'),
          ),
          IconButton(
            icon: const Icon(Icons.history),
            tooltip: 'Location history',
            onPressed: () => Navigator.of(context).pushNamed('/history'),
          ),
          IconButton(
            icon: const Icon(Icons.location_on_outlined),
            tooltip: 'Places',
            onPressed: () => Navigator.of(context).pushNamed('/places'),
          ),
          IconButton(
            icon: const Icon(Icons.route_outlined),
            tooltip: 'Driving trips',
            onPressed: () => Navigator.of(context).pushNamed('/trips'),
          ),
          IconButton(
            icon: const Icon(Icons.settings_outlined),
            tooltip: 'Settings',
            onPressed: () => Navigator.of(context).pushNamed('/settings'),
          ),
        ],
      ),
      body: Stack(
        children: [
          FlutterMap(
            mapController: _mapController,
            options: MapOptions(
              initialCenter: _myLatLng ?? const LatLng(40.0, -3.7),
              initialZoom: 14,
              interactionOptions: const InteractionOptions(
                flags: InteractiveFlag.all & ~InteractiveFlag.rotate,
              ),
            ),
            children: [
              _osmLayer,
              // Places as translucent circles.
              if (state.places.isNotEmpty)
                for (final place in state.places.values)
                  CircleLayer(
                    circles: [
                      CircleMarker(
                        point: LatLng(place.lat, place.lng),
                        radius: place.radiusM.clamp(5, 5000),
                        useRadiusInMeter: true,
                        color: const Color(0x334F7CFF),
                        borderColor: const Color(0x884F7CFF),
                        borderStrokeWidth: 2,
                      ),
                    ],
                  ),
              // Member markers.
              MarkerLayer(
                markers: [
                  for (final m in state.activeMembers)
                    if (m.deviceId == state.deviceId && _myLatLng != null)
                      _selfMarker(state, m, _myLatLng!)
                    else if (m.deviceId != state.deviceId)
                      if (state.positionsByDevice[m.deviceId] case final pos?)
                        _memberMarker(state, m, LatLng(pos.lat, pos.lng)),
                ],
              ),
              // My accuracy circle.
              if (_myLatLng != null && myPos != null && myPos.accuracy > 0)
                CircleLayer(
                  circles: [
                    CircleMarker(
                      point: _myLatLng!,
                      radius: myPos.accuracy.clamp(5, 5000),
                      useRadiusInMeter: true,
                      color: const Color(0x1F4F7CFF),
                      borderColor: const Color(0x334F7CFF),
                    ),
                  ],
                ),
            ],
          ),
          // Status stack: optional waiting-for-key banner + status pill.
          Positioned(
            top: 12,
            left: 12,
            right: 12,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                if (!state.hasCircleKey)
                  _KeyWaitingBanner(onRetry: () => state.retryCircleKey()),
                if (!state.hasCircleKey) const SizedBox(height: 6),
                _StatusPill(
                  tracking: state.tracking,
                  sharing: state.sharing,
                  onTap: () => Navigator.of(context).pushNamed('/settings'),
                ),
              ],
            ),
          ),
          // Zoom controls.
          Positioned(
            right: 12,
            bottom: 120,
            child: Column(
              children: [
                FloatingActionButton.small(
                  heroTag: 'z-in',
                  onPressed: () => _mapController.move(
                    _mapController.camera.center,
                    _mapController.camera.zoom + 1,
                  ),
                  child: const Icon(Icons.add),
                ),
                const SizedBox(height: 8),
                FloatingActionButton.small(
                  heroTag: 'z-out',
                  onPressed: () => _mapController.move(
                    _mapController.camera.center,
                    _mapController.camera.zoom - 1,
                  ),
                  child: const Icon(Icons.remove),
                ),
                const SizedBox(height: 8),
                FloatingActionButton.small(
                  heroTag: 'locate',
                  onPressed: _myLatLng == null
                      ? null
                      : () => _mapController.move(_myLatLng!, 15),
                  child: const Icon(Icons.my_location),
                ),
              ],
            ),
          ),
        ],
      ),
      bottomSheet: _MembersSheet(),
      floatingActionButton: _SosButton(),
    );
  }

  Marker _selfMarker(AppState state, CircleMember m, LatLng pos) {
    return Marker(
      point: pos,
      width: 48,
      height: 48,
      child: _MemberMarker(
        avatar: MemberAvatar(
          name: m.displayName,
          color: m.avatarColor,
          size: 44,
        ),
      ),
    );
  }

  Marker _memberMarker(AppState state, CircleMember m, LatLng pos) {
    return Marker(
      point: pos,
      width: 48,
      height: 48,
      child: GestureDetector(
        onTap: () => _showMemberSheet(context, state, m),
        child: _MemberMarker(
          avatar: MemberAvatar(
            name: m.displayName,
            color: m.avatarColor,
            size: 40,
            showPause: !m.sharingEnabled,
          ),
        ),
      ),
    );
  }

  void _showMemberSheet(BuildContext context, AppState state, CircleMember m) {
    final pos = state.positionsByDevice[m.deviceId];
    final memberEvents = state.events
        .where((e) => e.deviceId == m.deviceId)
        .take(3)
        .toList();
    showModalBottomSheet<void>(
      context: context,
      builder: (ctx) => SafeArea(
        child: Padding(
          padding: const EdgeInsets.all(20),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  MemberAvatar(
                    name: m.displayName,
                    color: m.avatarColor,
                    size: 48,
                  ),
                  const SizedBox(width: 12),
                  Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        m.displayName,
                        style: Theme.of(context).textTheme.titleMedium,
                      ),
                      Text(
                        pos == null
                            ? 'No recent position'
                            : 'Updated ${_timeAgo(pos.ts)}',
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ],
                  ),
                  const Spacer(),
                  IconButton(
                    icon: const Icon(Icons.history),
                    tooltip: 'History',
                    onPressed: () {
                      Navigator.pop(ctx);
                      Navigator.of(
                        context,
                      ).pushNamed('/history', arguments: m.deviceId);
                    },
                  ),
                  IconButton(
                    icon: const Icon(Icons.route_outlined),
                    tooltip: 'Trips',
                    onPressed: () {
                      Navigator.pop(ctx);
                      Navigator.of(
                        context,
                      ).pushNamed('/trips', arguments: m.deviceId);
                    },
                  ),
                ],
              ),
              const SizedBox(height: 12),
              if (!m.sharingEnabled)
                const Text(
                  '🔇 Sharing paused — this member chose privacy right now.',
                )
              else if (pos != null)
                Text(
                  'Accuracy ±${pos.accuracy.round()} m · '
                  '${pos.speed > 1 ? 'moving ${(pos.speed * 3.6).round()} km/h' : 'stationary'}',
                  style: Theme.of(context).textTheme.bodySmall,
                ),
              if (memberEvents.isNotEmpty) ...[
                const SizedBox(height: 16),
                Text(
                  'Recent activity',
                  style: Theme.of(context).textTheme.labelLarge,
                ),
                const SizedBox(height: 4),
                for (final e in memberEvents)
                  Padding(
                    padding: const EdgeInsets.symmetric(vertical: 2),
                    child: Row(
                      children: [
                        Icon(
                          _eventIcon(e.kind),
                          size: 16,
                          color: Theme.of(context).colorScheme.outline,
                        ),
                        const SizedBox(width: 8),
                        Expanded(
                          child: Text(
                            e.text,
                            style: Theme.of(context).textTheme.bodySmall,
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                        Text(
                          _timeAgo(e.ts),
                          style: Theme.of(context).textTheme.bodySmall
                              ?.copyWith(
                                color: Theme.of(context).colorScheme.outline,
                              ),
                        ),
                      ],
                    ),
                  ),
              ],
            ],
          ),
        ),
      ),
    );
  }

  void _checkIn(BuildContext context) {
    showDialog<void>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Check in?'),
        content: const Text('Let your circle know you\u2019re OK right now.'),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () {
              Navigator.pop(ctx);
              context.read<AppState>().checkIn();
            },
            child: const Text('I\u2019m OK'),
          ),
        ],
      ),
    );
  }
}

IconData _eventIcon(String kind) => switch (kind) {
  'sos' => Icons.emergency,
  'checkin' => Icons.task_alt,
  'geofence' => Icons.location_on,
  'trip' => Icons.route,
  'crash' => Icons.car_crash,
  _ => Icons.notifications,
};

class _KeyWaitingBanner extends StatelessWidget {
  const _KeyWaitingBanner({required this.onRetry});

  final Future<bool> Function() onRetry;

  @override
  Widget build(BuildContext context) {
    return Material(
      color: const Color(0xFFFFF3E0),
      borderRadius: BorderRadius.circular(12),
      elevation: 2,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
        child: Row(
          children: [
            const Icon(Icons.key_off, size: 18, color: Color(0xFFB8551E)),
            const SizedBox(width: 8),
            const Expanded(
              child: Text(
                'Waiting for the owner to grant you the circle key…',
                style: TextStyle(fontSize: 12, color: Color(0xFF7A3B12)),
              ),
            ),
            TextButton(
              onPressed: () => onRetry(),
              child: const Text('Retry', style: TextStyle(fontSize: 12)),
            ),
          ],
        ),
      ),
    );
  }
}

class _MemberMarker extends StatelessWidget {
  const _MemberMarker({required this.avatar});

  final Widget avatar;

  @override
  Widget build(BuildContext context) {
    return Center(child: avatar);
  }
}

class _StatusPill extends StatelessWidget {
  const _StatusPill({
    required this.tracking,
    required this.sharing,
    required this.onTap,
  });

  final bool tracking;
  final bool sharing;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final color = !sharing
        ? Colors.orange
        : tracking
        ? const Color(0xFF2E9E5B)
        : Colors.grey;
    return Material(
      color: Colors.white.withValues(alpha: 0.95),
      borderRadius: BorderRadius.circular(20),
      elevation: 2,
      child: InkWell(
        onTap: onTap,
        borderRadius: BorderRadius.circular(20),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(Icons.circle, size: 10, color: color),
              const SizedBox(width: 6),
              Text(
                !sharing
                    ? 'Paused'
                    : tracking
                    ? 'Sharing location'
                    : 'Not sharing yet',
                style: const TextStyle(
                  fontSize: 12,
                  fontWeight: FontWeight.w600,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _MembersSheet extends StatelessWidget {
  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    final members = state.activeMembers;
    if (members.isEmpty) {
      return const SizedBox.shrink();
    }
    return DraggableScrollableSheet(
      initialChildSize: 0.16,
      minChildSize: 0.1,
      maxChildSize: 0.45,
      builder: (context, scrollController) => Container(
        decoration: const BoxDecoration(
          color: Colors.white,
          borderRadius: BorderRadius.vertical(top: Radius.circular(20)),
        ),
        child: ListView(
          controller: scrollController,
          padding: const EdgeInsets.symmetric(vertical: 8),
          children: [
            const Center(child: _GrabHandle()),
            for (final m in members) _MemberTile(member: m),
          ],
        ),
      ),
    );
  }
}

class _MemberTile extends StatelessWidget {
  const _MemberTile({required this.member});

  final CircleMember member;

  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    final pos = state.positionsByDevice[member.deviceId];
    return ListTile(
      dense: true,
      leading: MemberAvatar(
        name: member.displayName,
        color: member.avatarColor,
        size: 36,
        showPause: !member.sharingEnabled,
      ),
      title: Text(
        member.displayName,
        style: const TextStyle(fontSize: 14, fontWeight: FontWeight.w600),
      ),
      subtitle: Text(
        pos == null
            ? (member.sharingEnabled
                  ? 'Waiting for first fix…'
                  : 'Sharing paused')
            : _timeAgo(pos.ts),
        style: const TextStyle(fontSize: 12),
      ),
      trailing: member.role == 'owner'
          ? const Icon(Icons.star, size: 16, color: Color(0xFFF2A03D))
          : null,
      onTap: () => Navigator.of(
        context,
      ).pushNamed('/history', arguments: member.deviceId),
    );
  }
}

class _GrabHandle extends StatelessWidget {
  const _GrabHandle();

  @override
  Widget build(BuildContext context) {
    return Container(
      width: 40,
      height: 4,
      margin: const EdgeInsets.only(bottom: 6),
      decoration: BoxDecoration(
        color: Colors.grey.shade300,
        borderRadius: BorderRadius.circular(2),
      ),
    );
  }
}

class _SosButton extends StatelessWidget {
  @override
  Widget build(BuildContext context) {
    return FloatingActionButton.extended(
      heroTag: 'sos',
      backgroundColor: const Color(0xFFE05D5D),
      onPressed: () => Navigator.of(context).pushNamed('/sos'),
      icon: const Icon(Icons.emergency),
      label: const Text('SOS'),
    );
  }
}

String _timeAgo(int ts) {
  final diff = DateTime.now().millisecondsSinceEpoch - ts;
  if (diff < 60 * 1000) return 'just now';
  if (diff < 3600 * 1000) return '${(diff / 60000).floor()} min ago';
  if (diff < 24 * 3600 * 1000) return '${(diff / 3600000).floor()} h ago';
  return '${(diff / 86400000).floor()} d ago';
}
