import 'dart:math';

import '../api/models.dart';

/// On-device crash detection — conservative by design.
///
/// Evidence of an impact: a strong accelerometer jolt (≥25 m/s² magnitude)
/// or free-fall (<2 m/s²), or a GPS speed drop of >8 m/s within 3 s while
/// moving at speed. After an impact, the detector confirms the vehicle
/// stopped (speed <0.5 m/s for 20 consecutive seconds) before raising a
/// `crash` alert with the crash location. If the member keeps moving,
/// nothing is raised — no false alarms from potholes or hard stops.
class CrashDetector {
  CrashDetector({required this.onCrash});

  /// Fired once a crash is confirmed.
  final void Function({
    required double lat,
    required double lng,
    required int ts,
  })
  onCrash;

  static const _joltMs2 = 25.0;
  static const _freeFallMs2 = 2.0;
  static const _speedDropMs = 8.0;
  static const _minSpeedForDropMs = 10.0;
  static const _dropWindowMs = 3000;
  static const _stopSpeedMs = 0.5;
  static const _confirmMs = 20 * 1000;
  static const _giveUpMs = 60 * 1000;

  bool _armed = false;
  int? _impactTs;
  int? _stillSince;
  double _impactLat = 0;
  double _impactLng = 0;
  double _prevSpeedMs = 0;
  int? _prevTs;

  /// Feeds an accelerometer sample (m/s², includes gravity).
  void onAcceleration(double x, double y, double z, {int? ts}) {
    final mag = sqrt(x * x + y * y + z * z);
    if (mag > _joltMs2 || mag < _freeFallMs2) {
      _arm(ts ?? DateTime.now().millisecondsSinceEpoch);
    }
  }

  /// Feeds a GPS fix (speed in m/s).
  void onPosition(Position fix) {
    // Sudden speed drop while driving fast: strong crash evidence.
    if (_prevTs != null &&
        _prevSpeedMs > _minSpeedForDropMs &&
        fix.ts - _prevTs! <= _dropWindowMs) {
      final drop = _prevSpeedMs - fix.speed;
      if (drop > _speedDropMs) {
        _arm(fix.ts, lat: fix.lat, lng: fix.lng);
      }
    }
    _prevSpeedMs = fix.speed;
    _prevTs = fix.ts;

    if (!_armed) return;

    if (fix.speed < _stopSpeedMs) {
      _stillSince ??= fix.ts;
      if (fix.ts - _stillSince! >= _confirmMs) {
        final lat = _impactLat == 0 ? fix.lat : _impactLat;
        final lng = _impactLng == 0 ? fix.lng : _impactLng;
        _armed = false;
        onCrash(lat: lat, lng: lng, ts: fix.ts);
      }
    } else {
      _stillSince = null;
      if (_impactTs != null && fix.ts - _impactTs! > _giveUpMs) {
        _armed = false; // moved on — not a crash
      }
    }
  }

  void _arm(int ts, {double? lat, double? lng}) {
    _armed = true;
    _impactTs = ts;
    _stillSince = null;
    if (lat != null) _impactLat = lat;
    if (lng != null) _impactLng = lng;
  }
}
