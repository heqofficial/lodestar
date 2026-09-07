import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:lodestar/core/api/models.dart';
import 'package:lodestar/core/crypto/crypto_service.dart';
import 'package:lodestar/core/tracking/geofence_engine.dart';

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
      final tampered = Uint8List.fromList(raw)
        ..[raw.length - 1] ^= 0x01;
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
      final opened = await member.openCircleKeyForMember(blob, owner.x25519PubB64);
      expect(base64Decode(opened), circleKey);
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
      final engine = GeofenceEngine(places: [place], onEvent: (p, e) => events.add(e));

      engine.onPosition(Position(lat: 40.0, lng: -3.70001, accuracy: 10, speed: 0, ts: 1));
      expect(events, isEmpty); // initial state seeds, no event yet

      // Cross the boundary (outside).
      engine.onPosition(Position(lat: 40.1, lng: -3.8, accuracy: 10, speed: 0, ts: 2));
      expect(events, ['leave']);

      // Back inside.
      engine.onPosition(Position(lat: 40.0, lng: -3.70001, accuracy: 10, speed: 0, ts: 70000));
      expect(events, ['leave', 'enter']);
    });

    test('ignores inaccurate fixes', () {
      final place = Place(id: 'p', name: 'P', lat: 40.0, lng: -3.7, radiusM: 100, ts: 0);
      final events = <String>[];
      final engine = GeofenceEngine(places: [place], onEvent: (_, e) => events.add(e));
      engine.onPosition(Position(lat: 40.0, lng: -3.7, accuracy: 500, speed: 0, ts: 1));
      engine.onPosition(Position(lat: 40.1, lng: -3.8, accuracy: 500, speed: 0, ts: 2));
      expect(events, isEmpty);
    });

    test('debounces rapid transitions', () {
      final place = Place(id: 'p', name: 'P', lat: 40.0, lng: -3.7, radiusM: 100, ts: 0);
      final events = <String>[];
      final engine = GeofenceEngine(places: [place], onEvent: (_, e) => events.add(e));
      engine.onPosition(Position(lat: 40.0, lng: -3.7001, accuracy: 10, speed: 0, ts: 1));
      engine.onPosition(Position(lat: 40.1, lng: -3.8, accuracy: 10, speed: 0, ts: 2000));
      engine.onPosition(Position(lat: 40.0, lng: -3.7001, accuracy: 10, speed: 0, ts: 3000));
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