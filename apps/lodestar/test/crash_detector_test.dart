import 'package:flutter_test/flutter_test.dart';
import 'package:lodestar/core/api/models.dart';
import 'package:lodestar/core/tracking/crash_detector.dart';

Position fix(double lat, double lng, double speedMs, int ts) =>
    Position(lat: lat, lng: lng, accuracy: 10, speed: speedMs, ts: ts);

void main() {
  group('CrashDetector', () {
    test('jolt followed by immobility raises a crash alert', () {
      final crashes = <Map<String, dynamic>>[];
      final d = CrashDetector(
        onCrash: ({required lat, required lng, required ts}) =>
            crashes.add({'lat': lat, 'lng': lng, 'ts': ts}),
      );
      // Driving normally.
      var t = 1000;
      for (var i = 0; i < 10; i++) {
        d.onPosition(fix(40.0 + i * 0.0001, -3.7, 25.0, t));
        t += 1000;
      }
      // Hard jolt.
      d.onAcceleration(5, 5, 28, ts: t);
      // Still moving right after impact.
      d.onPosition(fix(40.001, -3.7, 8.0, t));
      t += 5000;
      // Vehicle stops and stays stopped.
      d.onPosition(fix(40.0011, -3.7, 0.2, t));
      t += 21 * 1000;
      d.onPosition(fix(40.0011, -3.7, 0.1, t));
      expect(crashes, hasLength(1));
      // Impact location = where the speed drop happened (the 8 m/s fix).
      expect(crashes.single['lat'], 40.001);
    });

    test('no crash when the vehicle keeps moving after a jolt', () {
      final crashes = <Map<String, dynamic>>[];
      final d = CrashDetector(
        onCrash: ({required lat, required lng, required ts}) =>
            crashes.add({'lat': lat, 'lng': lng, 'ts': ts}),
      );
      var t = 1000;
      d.onPosition(fix(40.0, -3.7, 20.0, t));
      t += 1000;
      d.onAcceleration(4, 6, 30, ts: t); // pothole
      // Keeps driving normally afterwards.
      for (var i = 0; i < 20; i++) {
        d.onPosition(fix(40.0 + i * 0.0001, -3.7, 20.0, t));
        t += 1000;
      }
      expect(crashes, isEmpty);
    });

    test('sudden GPS speed drop plus stop confirms a crash', () {
      final crashes = <Map<String, dynamic>>[];
      final d = CrashDetector(
        onCrash: ({required lat, required lng, required ts}) =>
            crashes.add({'lat': lat, 'lng': lng, 'ts': ts}),
      );
      var t = 1000;
      d.onPosition(fix(40.0, -3.7, 22.0, t)); // baseline
      t += 1000;
      d.onPosition(fix(40.0001, -3.7, 3.0, t)); // 19 m/s drop in 1 s
      t += 1000;
      d.onPosition(fix(40.00015, -3.7, 0.3, t));
      t += 21 * 1000;
      d.onPosition(fix(40.00015, -3.7, 0.2, t));
      expect(crashes, hasLength(1));
    });

    test('hard braking without stopping never raises a crash', () {
      final crashes = <Map<String, dynamic>>[];
      final d = CrashDetector(
        onCrash: ({required lat, required lng, required ts}) =>
            crashes.add({'lat': lat, 'lng': lng, 'ts': ts}),
      );
      var t = 1000;
      for (var i = 0; i < 10; i++) {
        d.onPosition(fix(40.0 + i * 0.0001, -3.7, 22.0, t));
        t += 1000;
      }
      // Emergency stop at a red light (8 m/s drop) then rolling again.
      d.onPosition(fix(40.001, -3.7, 2.0, t));
      t += 3000;
      d.onPosition(fix(40.0011, -3.7, 12.0, t));
      for (var i = 0; i < 10; i++) {
        d.onPosition(fix(40.0011 + i * 0.0001, -3.7, 12.0, t));
        t += 1000;
      }
      expect(crashes, isEmpty);
    });
  });
}
