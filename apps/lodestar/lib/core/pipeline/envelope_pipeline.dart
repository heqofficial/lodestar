import 'dart:async';

import '../api/models.dart';
import '../crypto/crypto_service.dart';

/// Replay staleness policy for emergency alerts.
///
/// The server relays signed envelopes but can replay old ones; a replayed
/// SOS or crash would otherwise alarm the circle for nothing. Anything
/// outside a 10-minute window (or more than 5 minutes in the future, i.e.
/// clock skew) is dropped. Routine kinds (location, chat, trips) are
/// deliberately not time-limited — history and late messages are normal.
bool isStaleAlert(String kind, int ts, int nowMs) {
  if (kind != 'sos' && kind != 'crash') return false;
  final age = nowMs - ts;
  return age > 10 * 60 * 1000 || age < -5 * 60 * 1000;
}

/// Envelope attribution check: the signed inner sender must equal the
/// server-routed device id. The inner sender is part of the sender's own
/// signed plaintext, so without this cross-check a member could post an
/// envelope that the circle displays under another member's name (fake
/// SOS, fake chat, fake check-in).
bool isSenderSpoof(Envelope env, Map<String, dynamic> open) {
  final inner = open['sender'];
  return inner is! String || inner != env.deviceId;
}

/// Kinds this device inserts into its own UI *before* the server round-
/// trip completes (optimistic inserts). The server echoes every envelope
/// back to the sender's own socket, so echoes of these must be suppressed
/// or every self-sent message/check-in/SOS/trip would appear twice.
/// Location, geofence and place echoes are NOT suppressed: the sender's
/// own marker and geofence events only render via the echo.
const Set<String> optimisticKinds = {
  'message',
  'checkin',
  'sos',
  'trip',
  'crash',
};

/// True when [envNonce] is one this device already sent AND [kind] is an
/// optimistically-displayed kind — i.e. [envNonce] is our own echo. The
/// match consumes the nonce so a single echo suppresses exactly once.
bool isOwnEcho(Set<String> sentNonces, String envNonce, String kind) =>
    optimisticKinds.contains(kind) && sentNonces.remove(envNonce);

/// The inbound trust pipeline: dedup → persist → decrypt → attribute →
/// route. Every security property of inbound envelopes lives here:
///
///  * each envelope id is processed at most once (replay protection),
///  * envelopes arriving before the circle key are queued (bounded) and
///    re-processed once the key arrives — the id is re-added so the flush
///    cannot be silently swallowed by the replay guard,
///  * the signed inner sender must match the server-routed device id
///    (anti-spoofing, see [isSenderSpoof]),
///  * own echoes of optimistic sends are suppressed exactly once,
///  * stale emergency alerts are dropped before they can alarm anyone.
///
/// UI concerns (where decrypted data lands) are injected as [dispatch];
/// everything above that line is pure logic and unit-testable.
class EnvelopePipeline {
  EnvelopePipeline({
    required CryptoService crypto,
    required Future<void> Function(Envelope envelope) persist,
    required CircleMember? Function(String circleId, String deviceId) memberOf,
    required void Function(String circleId, Map<String, dynamic> open)
    dispatch,
  }) : _crypto = crypto,
       _persist = persist,
       _memberOf = memberOf,
       _dispatch = dispatch;

  final CryptoService _crypto;
  final Future<void> Function(Envelope envelope) _persist;
  final CircleMember? Function(String circleId, String deviceId) _memberOf;
  final void Function(String circleId, Map<String, dynamic> open) _dispatch;

  /// Envelope ids already processed (replay protection). Insertion-ordered
  /// so eviction drops the oldest.
  final Set<String> _seenIds = {};

  /// Envelopes received while we had no circle key yet; flushed after grant.
  final List<Envelope> _pending = [];

  /// Nonces of envelopes this device just sent (echo suppression).
  final Set<String> _sentNonces = {};

  /// Record a nonce we optimistically displayed (see [optimisticKinds]).
  void markSent(String nonce) {
    _sentNonces.add(nonce);
    if (_sentNonces.length > 1000) {
      _sentNonces.remove(_sentNonces.first);
    }
  }

  Future<void> ingest(Envelope env) async {
    // Replay protection: each envelope is processed at most once.
    if (!_seenIds.add(env.id)) return;
    if (_seenIds.length > 5000) {
      _seenIds.remove(_seenIds.first);
    }
    await _persist(env);
    final key = await _crypto.circleKey(env.circleId);
    if (key == null) {
      // Bounded queue: a chatty circle must not be able to exhaust memory
      // on a joiner who has no key yet (drop the oldest). NB: forget the
      // replay-guard id here — flushPending re-ingests the queued
      // envelopes, and a still-seen id would skip every one of them (the
      // flush was a silent no-op before this was fixed). The id is
      // re-added the moment the envelope is actually processed, so a
      // duplicate arriving pre-key only queues twice, never processes
      // twice.
      _seenIds.remove(env.id);
      if (_pending.length >= 1000) {
        _pending.removeAt(0);
      }
      _pending.add(env);
      return;
    }
    final sender = _memberOf(env.circleId, env.deviceId);
    if (sender == null) return;
    try {
      final open = await _crypto.openEnvelope(
        circleId: env.circleId,
        nonceB64: env.nonce,
        ciphertextB64: env.ciphertext,
        senderPubEd25519B64: sender.ed25519Pub,
      );
      // The inner sender is part of the signed plaintext; the server's
      // device_id comes from the bearer token. They must agree — otherwise
      // a member could sign an envelope claiming to be someone else and it
      // would display under the victim's name (fake SOS, fake chat, ...).
      if (isSenderSpoof(env, open)) return;
      // Our own echo of an optimistically-displayed send (see
      // [markSent]) — already in the UI, don't add it twice.
      if (isOwnEcho(_sentNonces, env.nonce, env.kind)) return;
      // The inner kind/ts are signed by the sender; the server could replay
      // an old emergency alert, so drop stale ones before they alarm anyone.
      final kind = open['kind'] as String? ?? '';
      final ts = (open['ts'] as num?)?.toInt() ?? 0;
      if (isStaleAlert(kind, ts, DateTime.now().millisecondsSinceEpoch)) {
        return;
      }
      _dispatch(env.circleId, open);
    } catch (_) {
      // Tampered or not decryptable with current key — ignore.
    }
  }

  /// Processes envelopes queued while the circle key was missing.
  void flushPending() {
    final queued = List<Envelope>.from(_pending);
    _pending.clear();
    for (final env in queued) {
      unawaited(ingest(env));
    }
  }
}
