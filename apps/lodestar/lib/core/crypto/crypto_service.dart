import 'dart:convert';
import 'dart:math';
import 'dart:typed_data';

import 'package:cryptography/cryptography.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// Minimal key-value storage abstraction so the crypto layer is testable
/// without the OS keystore.
abstract class KeyStore {
  Future<String?> read(String key);
  Future<void> write(String key, String value);
}

/// KeyStore backed by the OS keystore/keychain.
class SecureKeyStore implements KeyStore {
  final FlutterSecureStorage _storage = const FlutterSecureStorage();

  @override
  Future<String?> read(String key) => _storage.read(key: key);

  @override
  Future<void> write(String key, String value) => _storage.write(key: key, value: value);
}

/// End-to-end encryption for everything a member shares.
///
/// Threat model: the server must never learn locations, places, messages,
/// or circle keys. Every payload is sealed with the circle key
/// (Chacha20-Poly1305) and signed by the sender (Ed25519), so the server
/// can neither read nor forge envelopes. Circle keys travel only inside
/// key blobs sealed to individual members' X25519 public keys.
class CryptoService {
  CryptoService(this._store);

  final KeyStore _store;

  static const _edPriv = 'ls_ed25519_priv';
  static const _edPub = 'ls_ed25519_pub';
  static const _xPriv = 'ls_x25519_priv';
  static const _xPub = 'ls_x25519_pub';
  static const _circleKeyPrefix = 'ls_circle_key_';

  bool _initialized = false;
  SimpleKeyPair? _edPair;
  SimpleKeyPair? _xPair;
  Uint8List? _edPubBytes;
  Uint8List? _xPubBytes;

  final Ed25519 _ed = Ed25519();
  final X25519 _x25519 = X25519();
  final Chacha20 _aead = Chacha20.poly1305Aead();

  /// Loads existing keys or creates a fresh identity.
  ///
  /// Private halves live in the OS keystore; the public halves are stored
  /// alongside (they are public by definition) so the pair can be rebuilt
  /// after an app restart.
  Future<void> init() async {
    if (_initialized) return;
    SimpleKeyPair edPair;
    SimpleKeyPair xPair;
    final edPrivB64 = await _store.read(_edPriv);
    final edPubB64 = await _store.read(_edPub);
    final xPrivB64 = await _store.read(_xPriv);
    final xPubB64 = await _store.read(_xPub);
    if (edPrivB64 != null && xPrivB64 != null && edPubB64 != null && xPubB64 != null) {
      edPair = SimpleKeyPairData(
        base64Decode(edPrivB64),
        publicKey: SimplePublicKey(base64Decode(edPubB64), type: KeyPairType.ed25519),
        type: KeyPairType.ed25519,
      );
      xPair = SimpleKeyPairData(
        base64Decode(xPrivB64),
        publicKey: SimplePublicKey(base64Decode(xPubB64), type: KeyPairType.x25519),
        type: KeyPairType.x25519,
      );
    } else {
      edPair = await _ed.newKeyPair();
      xPair = await _x25519.newKeyPair();
      final edPub = (await edPair.extractPublicKey()).bytes;
      final xPub = (await xPair.extractPublicKey()).bytes;
      await _store.write(_edPriv, base64Encode(await edPair.extractPrivateKeyBytes()));
      await _store.write(_edPub, base64Encode(edPub));
      await _store.write(_xPriv, base64Encode(await xPair.extractPrivateKeyBytes()));
      await _store.write(_xPub, base64Encode(xPub));
    }
    _edPair = edPair;
    _xPair = xPair;
    _edPubBytes = Uint8List.fromList((await edPair.extractPublicKey()).bytes);
    _xPubBytes = Uint8List.fromList((await xPair.extractPublicKey()).bytes);
    _initialized = true;
  }

  /// Public keys as base64 (for device registration).
  String get ed25519PubB64 => _requireBytes(_edPubBytes);
  String get x25519PubB64 => _requireBytes(_xPubBytes);

  Future<Uint8List> sign(Uint8List message) async {
    _ensure();
    final sig = await _ed.sign(message, keyPair: _edPair!);
    return Uint8List.fromList(sig.bytes);
  }

  Future<bool> verify(Uint8List message, Uint8List signature, Uint8List publicKey) async {
    return _ed.verify(
      message,
      signature: Signature(
        signature,
        publicKey: SimplePublicKey(publicKey, type: KeyPairType.ed25519),
      ),
    );
  }

  // ---------------------------------------------------------------------
  // Circle keys
  // ---------------------------------------------------------------------

  /// Creates a fresh random 32-byte circle key.
  Future<Uint8List> newCircleKey() async {
    final rand = Random.secure();
    return Uint8List.fromList(List<int>.generate(32, (_) => rand.nextInt(256)));
  }

  Future<void> saveCircleKey(String circleId, Uint8List key) async {
    await _store.write(_circleKeyPrefix + circleId, base64Encode(key));
  }

  Future<Uint8List?> circleKey(String circleId) async {
    final b64 = await _store.read(_circleKeyPrefix + circleId);
    return b64 == null ? null : Uint8List.fromList(base64Decode(b64));
  }

  /// Seals [circleKeyB64] so only the member with [memberX25519PubB64] can
  /// open it. Used by the circle owner when a member joins.
  Future<String> sealCircleKeyForMember(String circleKeyB64, String memberX25519PubB64) async {
    _ensure();
    final shared = await _deriveShared(
      _xPair!,
      SimplePublicKey(base64Decode(memberX25519PubB64), type: KeyPairType.x25519),
    );
    final nonce = _randomNonce();
    final box = await _aead.encrypt(
      base64Decode(circleKeyB64),
      secretKey: SecretKey(shared),
      nonce: nonce,
    );
    return base64Encode(_boxToBytesWithNonce(box));
  }

  /// Opens a circle-key blob produced by the owner ([ownerX25519PubB64]).
  Future<String> openCircleKeyForMember(String blobB64, String ownerX25519PubB64) async {
    _ensure();
    final shared = await _deriveShared(
      _xPair!,
      SimplePublicKey(base64Decode(ownerX25519PubB64), type: KeyPairType.x25519),
    );
    final box = _boxFromBytesWithNonce(base64Decode(blobB64));
    final clear = await _aead.decrypt(box, secretKey: SecretKey(shared));
    return base64Encode(clear);
  }

  Future<Uint8List> _deriveShared(SimpleKeyPair ourPair, SimplePublicKey theirPub) async {
    final secret = await _x25519.sharedSecretKey(keyPair: ourPair, remotePublicKey: theirPub);
    final hkdf = Hkdf(hmac: Hmac.sha256(), outputLength: 32);
    final key = await hkdf.deriveKey(
      secretKey: secret,
      nonce: utf8.encode('lodestar-circle-key-v1'),
    );
    return Uint8List.fromList(await key.extractBytes());
  }

  // ---------------------------------------------------------------------
  // Envelopes
  // ---------------------------------------------------------------------

  /// Seals and signs an envelope payload.
  ///
  /// Returns (nonce, ciphertext) for the server to relay verbatim.
  /// ciphertext layout: [Ed25519 signature (64)] [Chacha20 ciphertext+mac].
  Future<({String nonce, String ciphertext})> sealEnvelope({
    required String circleId,
    required String deviceId,
    required String kind,
    required int ts,
    required Map<String, dynamic> data,
  }) async {
    _ensure();
    final key = await circleKey(circleId);
    if (key == null) {
      throw StateError('no circle key for $circleId');
    }
    final plain = jsonEncode({
      'v': 1,
      'sender': deviceId,
      'kind': kind,
      'ts': ts,
      'data': data,
    });
    final nonce = _randomNonce();
    final box = await _aead.encrypt(
      utf8.encode(plain),
      secretKey: SecretKey(key),
      nonce: nonce,
    );
    final sealed = _boxToBytes(box);
    final sig = await sign(sealed);
    return (
      nonce: base64Url.encode(nonce),
      ciphertext: base64Encode(Uint8List.fromList([...sig, ...sealed])),
    );
  }

  /// Opens and verifies an envelope. Returns the plaintext map, or
  /// throws on tampering / wrong key.
  Future<Map<String, dynamic>> openEnvelope({
    required String circleId,
    required String nonceB64,
    required String ciphertextB64,
    required String senderPubEd25519B64,
  }) async {
    _ensure();
    final key = await circleKey(circleId);
    if (key == null) {
      throw StateError('no circle key for $circleId');
    }
    final raw = base64Decode(ciphertextB64);
    if (raw.length < 64 + 12 + 16) {
      throw const FormatException('envelope too short');
    }
    final sig = Uint8List.fromList(raw.sublist(0, 64));
    final sealed = Uint8List.fromList(raw.sublist(64));
    final senderPub = SimplePublicKey(
      base64Decode(senderPubEd25519B64),
      type: KeyPairType.ed25519,
    );
    final ok = await _ed.verify(sealed, signature: Signature(sig, publicKey: senderPub));
    if (!ok) {
      throw const FormatException('bad envelope signature');
    }
    final box = SecretBox(
      sealed.sublist(0, sealed.length - 16),
      nonce: base64Url.decode(nonceB64),
      mac: Mac(Uint8List.fromList(sealed.sublist(sealed.length - 16))),
    );
    final clear = await _aead.decrypt(box, secretKey: SecretKey(key));
    return jsonDecode(utf8.decode(clear)) as Map<String, dynamic>;
  }

  // ---------------------------------------------------------------------
  // helpers
  // ---------------------------------------------------------------------

  /// Serializes ciphertext+mac (nonce travels separately for envelopes).
  static Uint8List _boxToBytes(SecretBox box) {
    return Uint8List.fromList([...box.cipherText, ...box.mac.bytes]);
  }

  /// Key-blob format: [nonce (16)] [ciphertext+mac].
  static Uint8List _boxToBytesWithNonce(SecretBox box) {
    return Uint8List.fromList([...box.nonce, ...box.cipherText, ...box.mac.bytes]);
  }

  static SecretBox _boxFromBytesWithNonce(Uint8List b) {
    if (b.length < 12 + 16) {
      throw const FormatException('box too short');
    }
    return SecretBox(
      b.sublist(12, b.length - 16),
      nonce: Uint8List.fromList(b.sublist(0, 12)),
      mac: Mac(Uint8List.fromList(b.sublist(b.length - 16))),
    );
  }

  /// IETF-standard 12-byte ChaCha20-Poly1305 nonce.
  static Uint8List _randomNonce() {
    final rand = Random.secure();
    return Uint8List.fromList(List<int>.generate(12, (_) => rand.nextInt(256)));
  }

  void _ensure() {
    if (!_initialized) throw StateError('CryptoService.init() must be called first');
  }

  static String _requireBytes(Uint8List? b) {
    if (b == null) throw StateError('identity not initialized');
    return base64Encode(b);
  }
}