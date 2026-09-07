import 'package:flutter_test/flutter_test.dart';
import 'package:lodestar/core/api/models.dart';
import 'package:lodestar/core/tracking/trip_detector.dart';

Position fix(double lat, double lng, double speedMs, int ts) =>
    Position(lat: lat, lng: lng, accuracy: 10, speed: speedMs, ts: ts);

void main() {
  group('TripDetector', () {
    test('does not start a trip from walking speeds', () {
      final summaries = <TripSummary>[];
      final d = TripDetector(speedLimitKmh: 90, onTripEnded: summaries.add);
      d.onPosition(fix(40.0, -3.7, 1.2, 1000)); // brisk walk
      d.onPosition(fix(40.0001, -3.7, 1.4, 1030));
      expect(summaries, isEmpty);
      expect(d.inTrip, isFalse);
    });

    test('starts a trip while driving and ends it after standing still', () {
      final summaries = <TripSummary>[];
      final d = TripDetector(speedLimitKmh: 90, onTripEnded: summaries.add);
      var t = 0;
      // Drive ~50 km/h for 60 s: 1 km ≈ 14 m per fix every 1 s.
      var lat = 40.0;
      for (var i = 0; i < 60; i++) {
        lat += 14.0 / 111320.0; // 14 m north per second
        t += 1000;
        d.onPosition(fix(lat, -3.7, 13.9, t));
      }
      expect(d.inTrip, isTrue);
      // Stop: first still fix starts the clock, a second one 90 s later
      // confirms the trip is over.
      t += 90 * 1000;
      d.onPosition(fix(lat, -3.7, 0.0, t));
      expect(summaries, isEmpty); // needs a second confirming fix
      t += 90 * 1000;
      d.onPosition(fix(lat, -3.7, 0.0, t));
      expect(summaries, hasLength(1));
      final s = summaries.single;
      expect(s.distanceM, greaterThan(600));
      expect(s.distanceM, lessThan(1100));
      expect(s.maxSpeedKmh, closeTo(50, 1));
      expect(s.durationS, greaterThan(60));
      expect(s.speedingCount, 0);
      expect(s.hardBrakingCount, 0);
    });

    test('counts one speeding episode for a long over-limit stretch', () {
      final summaries = <TripSummary>[];
      final d = TripDetector(speedLimitKmh: 90, onTripEnded: summaries.add);
      var t = 0;
      var lat = 40.0;
      // Drive 110 km/h (30.5 m/s) for 15 s — one continuous episode.
      for (var i = 0; i < 15; i++) {
        lat += 30.5 / 111320.0;
        t += 1000;
        d.onPosition(fix(lat, -3.7, 30.5, t));
      }
      // Then legal speed for a while.
      for (var i = 0; i < 10; i++) {
        lat += 13.9 / 111320.0;
        t += 1000;
        d.onPosition(fix(lat, -3.7, 13.9, t));
      }
      // Then speeding again.
      for (var i = 0; i < 10; i++) {
        lat += 30.5 / 111320.0;
        t += 1000;
        d.onPosition(fix(lat, -3.7, 30.5, t));
      }
      t += 90 * 1000;
      d.onPosition(fix(lat, -3.7, 0.0, t));
      t += 90 * 1000;
      d.onPosition(fix(lat, -3.7, 0.0, t));
      expect(summaries, hasLength(1));
      expect(summaries.single.speedingCount, 2);
    });

    test('counts hard braking when decelerating sharply', () {
      final summaries = <TripSummary>[];
      final d = TripDetector(speedLimitKmh: 90, onTripEnded: summaries.add);
      var t = 1000;
      var lat = 40.0;
      for (var i = 0; i < 10; i++) {
        lat += 25.0 / 111320.0;
        d.onPosition(fix(lat, -3.7, 25.0, t)); // 90 km/h
        t += 1000;
      }
      // Slam the brakes: 25 m/s → 3 m/s in 1 s (22 m/s²).
      lat += 3.0 / 111320.0;
      d.onPosition(fix(lat, -3.7, 3.0, t));
      t += 1000;
      // Still moving slowly for a bit, then stop (two confirming fixes).
      for (var i = 0; i < 5; i++) {
        lat += 1.0 / 111320.0;
        d.onPosition(fix(lat, -3.7, 1.0, t));
        t += 1000;
      }
      t += 90 * 1000;
      d.onPosition(fix(lat, -3.7, 0.0, t));
      t += 90 * 1000;
      d.onPosition(fix(lat, -3.7, 0.0, t));
      expect(summaries, hasLength(1));
      expect(summaries.single.hardBrakingCount, greaterThanOrEqualTo(1));
    });

    test('finish() ends an open trip with a summary', () {
      final summaries = <TripSummary>[];
      final d = TripDetector(speedLimitKmh: 90, onTripEnded: summaries.add);
      var t = 1000;
      for (var i = 0; i < 10; i++) {
        d.onPosition(fix(40.0 + i * 0.0001, -3.7, 15.0, t));
        t += 1000;
      }
      expect(d.inTrip, isTrue);
      d.finish();
      expect(summaries, hasLength(1));
      expect(summaries.single.distanceM, greaterThan(0));
    });
  });
}
