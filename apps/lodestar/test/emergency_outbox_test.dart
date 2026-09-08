import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:lodestar/core/api/api_client.dart';
import 'package:lodestar/core/api/models.dart';
import 'package:lodestar/core/outbox/emergency_outbox.dart';
import 'package:lodestar/core/store/local_db.dart';
import 'package:sqflite_common_ffi/sqflite_ffi.dart';

/// Fake ApiClient that records posts and can be told to fail.
class FakeApi implements ApiClient {
  FakeApi({this.failWith});

  /// When non-null, every postEnvelope throws this.
  Object? failWith;

  final posts = <({String circleId, String kind, int ts, String nonce})>[];

  @override
  Future<Envelope> postEnvelope({
    required String circleId,
    required String kind,
    required int ts,
    required String nonce,
    required String ciphertext,
  }) async {
    final err = failWith;
    if (err != null) throw err;
    posts.add((circleId: circleId, kind: kind, ts: ts, nonce: nonce));
    return Envelope(
      id: 'srv-$nonce',
      circleId: circleId,
      deviceId: 'd1',
      kind: kind,
      ts: ts,
      nonce: nonce,
      ciphertext: ciphertext,
    );
  }

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw StateError('unexpected call: ${invocation.memberName}');
}

OutboxItem _item({String nonce = 'n1', int? ts, int attempts = 0}) =>
    OutboxItem(
      circleId: 'c1',
      kind: 'sos',
      ts: ts ?? DateTime.now().millisecondsSinceEpoch,
      nonce: nonce,
      ciphertext: 'ct',
      attempts: attempts,
    );

void main() {
  sqfliteFfiInit();
  databaseFactory = databaseFactoryFfi;

  setUp(() async {
    // LocalDb is a process-wide singleton; start each test with an empty
    // outbox so rows never leak between tests.
    final db = await LocalDb.open(name: 'outbox-test.db');
    for (final item in await db.outboxItems()) {
      await db.deleteOutboxItem(item.nonce);
    }
  });

  group('EmergencyOutbox', () {    test('enqueue persists before any network attempt (kill-safe)', () async {
      final db = await LocalDb.open();
      final outbox = EmergencyOutbox(db);

      await outbox.enqueue(_item());
      // No network call has happened; the item must already be on disk.
      expect(await db.outboxItems(), hasLength(1));
      expect((await db.outboxItems()).first.nonce, 'n1');
    });

    test('drain delivers and removes on success', () async {
      final db = await LocalDb.open();
      final outbox = EmergencyOutbox(db);
      final api = FakeApi();
      await outbox.enqueue(_item());

      final delivered = await outbox.drain(api);

      expect(delivered, 1);
      expect(api.posts.single.nonce, 'n1');
      expect(await db.outboxItems(), isEmpty);
    });

    test('drain keeps item queued when the server is unreachable', () async {
      final db = await LocalDb.open();
      final outbox = EmergencyOutbox(db);
      final api = FakeApi()..failWith = const SocketException('offline');
      await outbox.enqueue(_item());

      final delivered = await outbox.drain(api);

      expect(delivered, 0);
      final left = await db.outboxItems();
      expect(left, hasLength(1));
      expect(left.first.attempts, 1); // failure recorded
    });

    test('drain retries with the SAME nonce (server dedup absorbs a race)',
        () async {
      final db = await LocalDb.open();
      final outbox = EmergencyOutbox(db);
      final api = FakeApi()..failWith = const SocketException('offline');
      await outbox.enqueue(_item(nonce: 'fixed-nonce'));
      await outbox.drain(api);

      api
        ..failWith = null
        ..posts.clear();
      await outbox.drain(api);

      expect(api.posts.single.nonce, 'fixed-nonce');
    });

    test('stale items are dropped at drain time, not retried', () async {
      final db = await LocalDb.open();
      var clock = DateTime.now().millisecondsSinceEpoch;
      final outbox = EmergencyOutbox(db, now: () => clock);
      final api = FakeApi()..failWith = const SocketException('offline');
      await outbox.enqueue(_item(ts: clock));
      await outbox.drain(api); // fails, stays queued

      clock += EmergencyOutbox.stalenessMs + 1;
      api
        ..failWith = null
        ..posts.clear();
      final delivered = await outbox.drain(api);

      expect(delivered, 0);
      expect(api.posts, isEmpty); // never even attempted
      expect(await db.outboxItems(), isEmpty); // dropped, not retried forever
    });

    test('capacity is bounded: enqueue drops the OLDEST item', () async {
      final db = await LocalDb.open();
      final outbox = EmergencyOutbox(db);
      for (var i = 0; i < EmergencyOutbox.capacity + 2; i++) {
        await outbox.enqueue(_item(nonce: 'n$i'));
      }
      final items = await db.outboxItems();
      expect(items, hasLength(EmergencyOutbox.capacity));
      // Oldest two (n0, n1) are gone; newest survived.
      expect(items.first.nonce, 'n2');
      expect(items.last.nonce, 'n${EmergencyOutbox.capacity + 1}');
    });

    test('403 (membership revoked) drops the item instead of retrying',
        () async {
      final db = await LocalDb.open();
      final outbox = EmergencyOutbox(db);
      final api = FakeApi()..failWith = ApiException(403, 'revoked');
      await outbox.enqueue(_item());

      await outbox.drain(api);

      expect(await db.outboxItems(), isEmpty);
    });

    test('kill/restart simulation: enqueue -> new outbox instance -> drain',
        () async {
      final db = await LocalDb.open();
      final first = EmergencyOutbox(db);
      final api = FakeApi()..failWith = const SocketException('offline');
      await first.enqueue(_item(nonce: 'sos-42'));
      // "App killed" — the queue lives in the db, not in the instance.

      final restarted = EmergencyOutbox(db); // fresh process, same db
      final freshApi = FakeApi();
      final delivered = await restarted.drain(freshApi);

      expect(delivered, 1);
      expect(freshApi.posts.single.nonce, 'sos-42');
      expect(api.posts, isEmpty); // first session never delivered
    });
  });
}
