import '../api/models.dart';
import '../geo.dart';

/// A completed driving trip summary.
class TripSummary {
  final int startTs;
  final int endTs;
  final double distanceM;
  final double maxSpeedKmh;
  final double avgSpeedKmh;
  final int speedingCount;
  final int hardBrakingCount;
  final double startLat;
  final double startLng;
  final double endLat;
  final double endLng;

  TripSummary({
    required this.startTs,
    required this.endTs,
    required this.distanceM,
    required this.maxSpeedKmh,
    required this.avgSpeedKmh,
    required this.speedingCount,
    required this.hardBrakingCount,
    required this.startLat,
    required this.startLng,
    required this.endLat,
    required this.endLng,
  });

  factory TripSummary.fromData(Map<String, dynamic> d) => TripSummary(
    startTs: (d['start_ts'] as num).toInt(),
    endTs: (d['end_ts'] as num).toInt(),
    distanceM: (d['distance_m'] as num).toDouble(),
    maxSpeedKmh: (d['max_speed_kmh'] as num).toDouble(),
    avgSpeedKmh: (d['avg_speed_kmh'] as num).toDouble(),
    speedingCount: (d['speeding_count'] as num?)?.toInt() ?? 0,
    hardBrakingCount: (d['hard_braking_count'] as num?)?.toInt() ?? 0,
    startLat: (d['start_lat'] as num).toDouble(),
    startLng: (d['start_lng'] as num).toDouble(),
    endLat: (d['end_lat'] as num).toDouble(),
    endLng: (d['end_lng'] as num).toDouble(),
  );

  Map<String, dynamic> toData() => {
    'start_ts': startTs,
    'end_ts': endTs,
    'distance_m': distanceM.round(),
    'max_speed_kmh': maxSpeedKmh,
    'avg_speed_kmh': avgSpeedKmh,
    'speeding_count': speedingCount,
    'hard_braking_count': hardBrakingCount,
    'start_lat': startLat,
    'start_lng': startLng,
    'end_lat': endLat,
    'end_lng': endLng,
  };

  int get durationS => ((endTs - startTs) / 1000).round();
}

/// On-device driving-trip detection.
///
/// Pure logic, fed by [AdaptiveTracker] fixes — no sensor required beyond
/// GPS. A trip starts when the member is clearly moving (≥21 km/h), ends
/// after 90 s of near-stationary movement, and summarizes distance, top
/// speed, and counts of speeding episodes (≥5 s over the limit) and hard
/// braking events (deceleration ≥2.5 m/s²). The summary travels as an
/// encrypted `trip` envelope — the server never sees the route.
class TripDetector {
  TripDetector({required this.speedLimitKmh, this.onTripEnded});

  final double speedLimitKmh;
  final void Function(TripSummary trip)? onTripEnded;

  // Detection thresholds (SI units).
  static const _startSpeed = 6.0; // m/s ≈ 21.6 km/h
  static const _endSpeed = 2.0; // m/s
  static const _endStillMs = 90 * 1000;
  static const _speedingMinMs = 5 * 1000;
  static const _brakeDecel = 2.5; // m/s²
  static const _brakeDebounceMs = 3 * 1000;

  bool _inTrip = false;
  int _startTs = 0;
  double _distanceM = 0;
  double _maxSpeedKmh = 0;
  double _startLat = 0;
  double _startLng = 0;
  double? _prevLat;
  double? _prevLng;
  double _prevSpeedMs = 0;
  int? _prevTs;
  int? _stillSince;
  int? _speedingSince;
  bool _speedingCounted = false;
  int _speedingCount = 0;
  int _brakingCount = 0;
  int _lastBrakeTs = 0;

  bool get inTrip => _inTrip;

  /// Feeds a GPS fix. May synchronously fire [onTripEnded].
  void onPosition(Position fix) {
    if (!_inTrip) {
      if (fix.speed >= _startSpeed) {
        _beginTrip(fix);
      }
      return;
    }

    _updateDistance(fix);

    final kmh = fix.speed * 3.6;
    if (kmh > _maxSpeedKmh) _maxSpeedKmh = kmh;

    // Speeding episode tracking.
    if (kmh > speedLimitKmh) {
      _speedingSince ??= fix.ts;
      if (!_speedingCounted && fix.ts - _speedingSince! >= _speedingMinMs) {
        _speedingCount++;
        _speedingCounted = true;
      }
    } else {
      _speedingSince = null;
      _speedingCounted = false;
    }

    // Hard braking: strong deceleration between consecutive fixes. The
    // guard checks the pre-brake speed (not the post-brake one, which may
    // already be low on a genuine emergency stop). dt is clamped to a sane
    // window: sub-second fixes can't be inferred (avoid div-by-zero) and
    // multi-minute gaps don't prove braking (speed could have dropped at
    // any point in between).
    if (_prevTs != null) {
      final dt = (fix.ts - _prevTs!).clamp(500, 15000) / 1000.0;
      final decel = (_prevSpeedMs - fix.speed) / dt;
      if (decel >= _brakeDecel &&
          _prevSpeedMs > 8.0 &&
          fix.ts - _lastBrakeTs >= _brakeDebounceMs) {
        _brakingCount++;
        _lastBrakeTs = fix.ts;
      }
    }

    // End conditions: still for 90 s, or a fix gap suggesting the trip is over.
    if (fix.speed < _endSpeed) {
      _stillSince ??= fix.ts;
      if (fix.ts - _stillSince! >= _endStillMs) {
        _endTrip(fix);
        return;
      }
    } else {
      _stillSince = null;
    }
    if (_prevTs != null && fix.ts - _prevTs! > 120 * 1000) {
      _endTrip(fix);
      return;
    }

    _prevLat = fix.lat;
    _prevLng = fix.lng;
    _prevSpeedMs = fix.speed;
    _prevTs = fix.ts;
  }

  /// Ends the current trip early (e.g. app stop) and fires the summary.
  void finish({Position? lastFix}) {
    if (!_inTrip) return;
    if (lastFix != null) _updateDistance(lastFix);
    _endTrip(lastFix);
  }

  void _beginTrip(Position fix) {
    _inTrip = true;
    _startTs = fix.ts;
    _distanceM = 0;
    _maxSpeedKmh = fix.speed * 3.6;
    _startLat = fix.lat;
    _startLng = fix.lng;
    _prevLat = fix.lat;
    _prevLng = fix.lng;
    _prevSpeedMs = fix.speed;
    _prevTs = fix.ts;
    _stillSince = null;
    _speedingSince = null;
    _speedingCounted = false;
    _speedingCount = 0;
    _brakingCount = 0;
  }

  void _updateDistance(Position fix) {
    if (_prevLat != null && _prevLng != null) {
      _distanceM += haversineM(_prevLat!, _prevLng!, fix.lat, fix.lng);
    }
  }

  void _endTrip(Position? fix) {
    _inTrip = false;
    final endTs = fix?.ts ?? _prevTs ?? _startTs;
    final endLat = fix?.lat ?? _prevLat ?? _startLat;
    final endLng = fix?.lng ?? _prevLng ?? _startLng;
    final durationS = ((endTs - _startTs) / 1000).clamp(1, double.infinity);
    final summary = TripSummary(
      startTs: _startTs,
      endTs: endTs,
      distanceM: _distanceM,
      maxSpeedKmh: _maxSpeedKmh,
      avgSpeedKmh: _distanceM / durationS * 3.6,
      speedingCount: _speedingCount,
      hardBrakingCount: _brakingCount,
      startLat: _startLat,
      startLng: _startLng,
      endLat: endLat,
      endLng: endLng,
    );
    _prevLat = null;
    _prevLng = null;
    _prevTs = null;
    _stillSince = null;
    onTripEnded?.call(summary);
  }
}
