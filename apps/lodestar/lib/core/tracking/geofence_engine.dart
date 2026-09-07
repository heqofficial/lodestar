import '../api/models.dart';
import '../geo.dart';

/// On-device geofence evaluation. Places are shared inside encrypted
/// envelopes, so the server never learns their coordinates; transitions
/// are announced as encrypted `geofence` envelopes to the circle.
class GeofenceEngine {
  GeofenceEngine({required this.places, required this.onEvent});

  final List<Place> places;
  final void Function(Place place, String event) onEvent;

  final Map<String, bool> _inside = {};
  final Map<String, int> _lastEventTs = {};

  static const minEventGapMs = 60 * 1000;
  static const maxAccuracyM = 150.0;

  /// Evaluates [fix] against all places; fires [onEvent] on transitions.
  void onPosition(Position fix) {
    if (fix.accuracy > maxAccuracyM) return; // unreliable fix
    for (final place in places) {
      final d = haversineM(fix.lat, fix.lng, place.lat, place.lng);
      final nowInside = d <= place.radiusM;
      final prev = _inside[place.id];
      if (prev == null) {
        _inside[place.id] = nowInside;
        continue;
      }
      if (prev != nowInside) {
        _inside[place.id] = nowInside;
        final last = _lastEventTs[place.id];
        if (last != null && fix.ts - last < minEventGapMs) continue; // debounce
        _lastEventTs[place.id] = fix.ts;
        onEvent(place, nowInside ? 'enter' : 'leave');
      }
    }
  }
}
