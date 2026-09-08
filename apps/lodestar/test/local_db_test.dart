import 'package:flutter_test/flutter_test.dart';
import 'package:lodestar/core/api/models.dart';
import 'package:lodestar/core/store/local_db.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

void main() {
  sqfliteFfiInit();
  databaseFactory = databaseFactoryFfi;

  group('LocalDb envelope cache', () {
    test('newest window is returned, ascending reverses in chronological order',
        () async {
      final db = await LocalDb.open(name: 'cache-test.db');
      // Unique circle id per run so rows never leak between test runs.
      final circle = 'c${DateTime.now().millisecondsSinceEpoch}';
      final base = DateTime.now().millisecondsSinceEpoch;
      for (var i = 0; i < 20; i++) {
        await db.upsertEnvelope(
          Envelope(
            id: 'e$i',
            circleId: circle,
            deviceId: 'd1',
            kind: i.isEven ? 'message' : 'location',
            ts: base + i * 1000,
            nonce: 'n$i',
            ciphertext: 'c',
          ),
        );
      }

      // Default: the NEWEST [limit], newest first.
      final newest = await db.cachedEnvelopes(circle, limit: 10);
      expect(newest, hasLength(10));
      expect(newest.first.id, 'e19');
      expect(newest.last.id, 'e10');

      // ascending: still the NEWEST window, but oldest-first for
      // chronological hydration — ORDER BY ts ASC LIMIT n would wrongly
      // return the n OLDEST rows.
      final chronological = await db.cachedEnvelopes(
        circle,
        limit: 10,
        ascending: true,
      );
      expect(chronological.first.id, 'e10');
      expect(chronological.last.id, 'e19');
      expect(
        chronological.map((e) => e.ts).toList(),
        orderedEquals([...chronological.map((e) => e.ts)]..sort()),
      );
    });
  });
}