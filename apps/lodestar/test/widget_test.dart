import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:lodestar/core/api/models.dart';
import 'package:lodestar/core/crypto/crypto_service.dart';
import 'package:lodestar/core/geo.dart';
import 'package:lodestar/core/tracking/geofence_engine.dart';
import 'package:lodestar/state/app_state.dart';

/// In-memory key store so tests never touch the OS keystore.
class _FakeKeyStore implements KeyStore {
  final Map<String, String> _store = {};

  @override
  Future<String?> read(String key) async => _store[key];

  @override
  Future<void> write(String key, String value) async {
    _store[key] = value;
  }
}

CryptoService newTestCrypto() => CryptoService(_FakeKeyStore());

void main() {
  group('CryptoService', () {
    test('identity survives init round-trip', () async {
      final c = newTestCrypto();
      await c.init();
      final ed1 = c.ed25519PubB64;
      final x1 = c.x25519PubB64;
      await c.init(); // re-init must load, not regenerate
      expect(c.ed25519PubB64, ed1);
      expect(c.x25519PubB64, x1);
    });

    test('envelope seals, relays and opens', () async {
      final alice = newTestCrypto();
      await alice.init();
      final bob = newTestCrypto();
      await bob.init();

      final circleKey = await alice.newCircleKey();
      await alice.saveCircleKey('c1', circleKey);
      await bob.saveCircleKey('c1', circleKey);

      final sealed = await alice.sealEnvelope(
        circleId: 'c1',
        deviceId: 'alice-dev',
        kind: 'location',
        ts: 1700000000000,
        data: {'lat': 40.41, 'lng': -3.7, 'acc': 12, 'speed': 0},
      );

      final opened = await bob.openEnvelope(
        circleId: 'c1',
        nonceB64: sealed.nonce,
        ciphertextB64: sealed.ciphertext,
        senderPubEd25519B64: alice.ed25519PubB64,
      );
      expect(opened['sender'], 'alice-dev');
      expect(opened['kind'], 'location');
      expect((opened['data'] as Map)['lat'], 40.41);
    });

    test('tampered envelope is rejected', () async {
      final alice = newTestCrypto();
      await alice.init();
      final bob = newTestCrypto();
      await bob.init();
      final key = await alice.newCircleKey();
      await alice.saveCircleKey('c1', key);
      await bob.saveCircleKey('c1', key);

      final sealed = await alice.sealEnvelope(
        circleId: 'c1',
        deviceId: 'alice',
        kind: 'message',
        ts: 1,
        data: {'text': 'hi'},
      );
      // Flip a bit in the ciphertext.
      final raw = base64Decode(sealed.ciphertext);
      final tampered = Uint8List.fromList(raw)..[raw.length - 1] ^= 0x01;
      await expectLater(
        bob.openEnvelope(
          circleId: 'c1',
          nonceB64: sealed.nonce,
          ciphertextB64: base64Encode(tampered),
          senderPubEd25519B64: alice.ed25519PubB64,
        ),
        throwsA(anything),
      );
    });

    test('circle key distribution: owner seals, member opens', () async {
      final owner = newTestCrypto();
      await owner.init();
      final member = newTestCrypto();
      await member.init();

      final circleKey = await owner.newCircleKey();
      final blob = await owner.sealCircleKeyForMember(
        base64Encode(circleKey),
        member.x25519PubB64,
      );
      final opened = await member.openCircleKeyForMember(
        blob,
        owner.x25519PubB64,
      );
      expect(base64Decode(opened), circleKey);
    });

    test('no nonce reuse across seals', () async {
      final c = newTestCrypto();
      await c.init();
      final key = await c.newCircleKey();
      await c.saveCircleKey('c1', key);

      final seen = <String>{};
      for (var i = 0; i < 50; i++) {
        final sealed = await c.sealEnvelope(
          circleId: 'c1',
          deviceId: 'd',
          kind: 'location',
          ts: i,
          data: {'lat': 1.0, 'lng': 2.0},
        );
        expect(seen.add(sealed.nonce), isTrue, reason: 'nonce reused');
      }
    });

    test('wrong circle key cannot decrypt', () async {
      final alice = newTestCrypto();
      await alice.init();
      final bob = newTestCrypto();
      await bob.init();
      await alice.saveCircleKey('c1', await alice.newCircleKey());
      await bob.saveCircleKey('c1', await bob.newCircleKey()); // different key

      final sealed = await alice.sealEnvelope(
        circleId: 'c1',
        deviceId: 'alice',
        kind: 'location',
        ts: 1,
        data: {'lat': 1.0, 'lng': 2.0},
      );
      await expectLater(
        bob.openEnvelope(
          circleId: 'c1',
          nonceB64: sealed.nonce,
          ciphertextB64: sealed.ciphertext,
          senderPubEd25519B64: alice.ed25519PubB64,
        ),
        throwsA(anything),
      );
    });

    test('wrong sender key fails signature check', () async {
      final alice = newTestCrypto();
      await alice.init();
      final bob = newTestCrypto();
      await bob.init();
      final mallory = newTestCrypto();
      await mallory.init();
      final key = await alice.newCircleKey();
      await alice.saveCircleKey('c1', key);
      await bob.saveCircleKey('c1', key);

      final sealed = await alice.sealEnvelope(
        circleId: 'c1',
        deviceId: 'alice',
        kind: 'message',
        ts: 1,
        data: {'text': 'hi'},
      );
      // Bob tries to verify with Mallory's key instead of Alice's.
      await expectLater(
        bob.openEnvelope(
          circleId: 'c1',
          nonceB64: sealed.nonce,
          ciphertextB64: sealed.ciphertext,
          senderPubEd25519B64: mallory.ed25519PubB64,
        ),
        throwsA(anything),
      );
    });

    test('truncated envelope is rejected as malformed', () async {
      final alice = newTestCrypto();
      await alice.init();
      final bob = newTestCrypto();
      await bob.init();
      final key = await alice.newCircleKey();
      await alice.saveCircleKey('c1', key);
      await bob.saveCircleKey('c1', key);

      await expectLater(
        bob.openEnvelope(
          circleId: 'c1',
          nonceB64: 'AAAA',
          ciphertextB64: base64Encode([1, 2, 3]),
          senderPubEd25519B64: alice.ed25519PubB64,
        ),
        throwsA(isA<FormatException>()),
      );
    });
  });

  group('isStaleAlert (replay policy)', () {
    final now = 1_700_000_000_000;

    test('fresh sos/crash passes', () {
      expect(isStaleAlert('sos', now - 1000, now), isFalse);
      expect(isStaleAlert('crash', now - 5 * 60 * 1000, now), isFalse);
    });

    test('replayed emergency is stale', () {
      expect(isStaleAlert('sos', now - 11 * 60 * 1000, now), isTrue);
      expect(isStaleAlert('crash', now - 60 * 60 * 1000, now), isTrue);
    });

    test('future-dated emergency is stale (clock skew)', () {
      expect(isStaleAlert('sos', now + 10 * 60 * 1000, now), isTrue);
    });

    test('routine kinds are never stale', () {
      expect(
        isStaleAlert('location', now - 30 * 24 * 3600 * 1000, now),
        isFalse,
      );
      expect(isStaleAlert('trip', now - 30 * 24 * 3600 * 1000, now), isFalse);
      expect(
        isStaleAlert('message', now - 30 * 24 * 3600 * 1000, now),
        isFalse,
      );
    });
  });

  group('GeofenceEngine', () {
    test('fires enter and leave events', () {
      final place = Place(
        id: 'home',
        name: 'Home',
        lat: 40.0,
        lng: -3.7,
        radiusM: 100,
        ts: 0,
      );
      final events = <String>[];
      final engine = GeofenceEngine(
        places: [place],
        onEvent: (p, e) => events.add(e),
      );

      engine.onPosition(
        Position(lat: 40.0, lng: -3.70001, accuracy: 10, speed: 0, ts: 1),
      );
      expect(events, isEmpty); // initial state seeds, no event yet

      // Cross the boundary (outside).
      engine.onPosition(
        Position(lat: 40.1, lng: -3.8, accuracy: 10, speed: 0, ts: 2),
      );
      expect(events, ['leave']);

      // Back inside.
      engine.onPosition(
        Position(lat: 40.0, lng: -3.70001, accuracy: 10, speed: 0, ts: 70000),
      );
      expect(events, ['leave', 'enter']);
    });

    test('ignores inaccurate fixes', () {
      final place = Place(
        id: 'p',
        name: 'P',
        lat: 40.0,
        lng: -3.7,
        radiusM: 100,
        ts: 0,
      );
      final events = <String>[];
      final engine = GeofenceEngine(
        places: [place],
        onEvent: (_, e) => events.add(e),
      );
      engine.onPosition(
        Position(lat: 40.0, lng: -3.7, accuracy: 500, speed: 0, ts: 1),
      );
      engine.onPosition(
        Position(lat: 40.1, lng: -3.8, accuracy: 500, speed: 0, ts: 2),
      );
      expect(events, isEmpty);
    });

    test('debounces rapid transitions', () {
      final place = Place(
        id: 'p',
        name: 'P',
        lat: 40.0,
        lng: -3.7,
        radiusM: 100,
        ts: 0,
      );
      final events = <String>[];
      final engine = GeofenceEngine(
        places: [place],
        onEvent: (_, e) => events.add(e),
      );
      engine.onPosition(
        Position(lat: 40.0, lng: -3.7001, accuracy: 10, speed: 0, ts: 1),
      );
      engine.onPosition(
        Position(lat: 40.1, lng: -3.8, accuracy: 10, speed: 0, ts: 2000),
      );
      engine.onPosition(
        Position(lat: 40.0, lng: -3.7001, accuracy: 10, speed: 0, ts: 3000),
      );
      // Leave fires, but the quick re-enter is debounced (within 60 s).
      expect(events, ['leave']);
    });
  });

  group('haversine', () {
    test('distance between known points', () {
      // Paris → London ≈ 344 km.
      final d = haversineM(48.8566, 2.3522, 51.5074, -0.1278);
      expect(d, closeTo(343000, 20000));
    });
  });
}
