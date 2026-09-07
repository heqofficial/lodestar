import 'dart:async';

import 'package:location/location.dart';

import '../api/models.dart';

/// Battery-aware background location tracker.
///
/// Strategy (target: <5% battery/day):
///  * Moving fast (driving): high accuracy, tight distance filter.
///  * Moving slowly (walking): medium cadence.
///  * Stationary: back off to a coarse filter so the OS delivers far fewer
///    fixes; we still wake to re-check location so leaving home is detected
///    promptly even without OS geofencing.
///
/// Android runs a visible foreground service (honest tracking); iOS uses
/// the standard "Always" background permission flow.
class AdaptiveTracker {
  AdaptiveTracker({required this.onFix});

  /// Called with each fix worth sharing.
  final void Function(Position fix) onFix;

  final Location _location = Location.instance;

  StreamSubscription<LocationData>? _sub;
  bool _paused = false;
  bool _running = false;
  Mode _mode = Mode.walk;

  int _lastPostedTs = 0;

  /// Requests permissions and starts the adaptive loop.
  Future<void> start() async {
    if (_running) return;
    _running = true;

    var perm = await _location.requestPermission();
    if (perm == PermissionStatus.deniedForever) {
      throw StateError('Location permission denied permanently');
    }
    if (perm == PermissionStatus.denied) {
      perm = await _location.requestPermission();
    }
    if (perm != PermissionStatus.granted && perm != PermissionStatus.grantedLimited) {
      throw StateError('Location permission required');
    }

    final serviceEnabled = await _location.serviceEnabled();
    if (!serviceEnabled) {
      await _location.requestService();
    }

    await _location.enableBackgroundMode();
    await _location.changeNotificationOptions(
      channelName: 'Lodestar tracking',
      title: 'Lodestar',
      subtitle: 'Sharing your location with your circle',
      iconName: '@mipmap/ic_launcher',
      description: 'Keeps your family informed while Lodestar runs in the background',
    );

    await _applyMode(_mode);
    _sub = _location.onLocationChanged.listen(_handleFix);
  }

  Future<void> _applyMode(Mode mode) async {
    await _location.changeSettings(
      accuracy: mode.accuracy,
      distanceFilter: mode.distanceFilter,
      interval: mode.intervalMs,
      pausesLocationUpdatesAutomatically: false,
    );
  }

  void _handleFix(LocationData data) {
    if (_paused || data.latitude == null || data.longitude == null) return;

    final fix = Position(
      lat: data.latitude!,
      lng: data.longitude!,
      accuracy: data.accuracy ?? 0,
      speed: (data.speed ?? 0).clamp(0, 100),
      ts: DateTime.now().millisecondsSinceEpoch,
    );

    // Switch sampling cadence based on movement.
    final speed = fix.speed;
    final next = speed > 5.0
        ? Mode.drive
        : speed > 0.7
            ? Mode.walk
            : Mode.stationary;
    if (next != _mode) {
      _mode = next;
      unawaited(_applyMode(_mode));
    }

    // Throttle stationary posts: one per 5 minutes is plenty.
    if (_mode == Mode.stationary && fix.ts - _lastPostedTs < 5 * 60 * 1000) {
      return;
    }
    _lastPostedTs = fix.ts;
    onFix(fix);
  }

  /// Pauses or resumes sharing. Pausing is visible to the circle (the
  /// server tracks sharing_enabled) — transparency builds trust.
  Future<void> setPaused(bool paused) async {
    _paused = paused;
    if (paused) {
      await _sub?.cancel();
      _sub = null;
    } else if (_running && _sub == null) {
      await _applyMode(_mode);
      _sub = _location.onLocationChanged.listen(_handleFix);
    }
  }

  bool get paused => _paused;

  Future<void> stop() async {
    _running = false;
    await _sub?.cancel();
    _sub = null;
  }
}

enum Mode {
  drive,
  walk,
  stationary;

  LocationAccuracy get accuracy => switch (this) {
        Mode.drive => LocationAccuracy.high,
        Mode.walk => LocationAccuracy.high,
        Mode.stationary => LocationAccuracy.balanced,
      };

  double get distanceFilter => switch (this) {
        Mode.drive => 25,
        Mode.walk => 10,
        Mode.stationary => 250,
      };

  int get intervalMs => switch (this) {
        Mode.drive => 15000,
        Mode.walk => 30000,
        Mode.stationary => 300000,
      };
}