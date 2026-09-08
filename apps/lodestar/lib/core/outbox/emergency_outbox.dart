import '../api/api_client.dart';
import '../store/local_db.dart';

/// Persistent queue for emergency envelopes (SOS/crash) that failed to post
/// because the network was down.
///
/// Contract:
///  * [enqueue] is called BEFORE the first network attempt, so a killed app
///    can never lose an unsent emergency; [drain] retries what's left and
///    removes an item only on a definite 2xx (or permanent rejection).
///  * Items older than 10 minutes are dropped at drain time — receivers
///    ignore anything staler than that, so retrying is pure noise.
///  * The queue is bounded (5): a stuck network must never grow disk use.
///
/// Retries reuse the original ts+nonce, so a retry that races the first
/// attempt's landing is absorbed by the server's dedup index.
class EmergencyOutbox {
  EmergencyOutbox(this._db, {int Function()? now})
    : _now = now ?? _defaultNow;

  final LocalDb _db;

  /// Injectable clock so drain's staleness decisions are testable.
  final int Function() _now;

  static int _defaultNow() => DateTime.now().millisecondsSinceEpoch;

  /// Receivers ignore emergencies staler than this, so retrying is noise.
  static const stalenessMs = 10 * 60 * 1000;

  /// Maximum queued emergencies. Oldest are dropped by enqueue itself.
  static const capacity = 5;

  /// Persist before any network attempt. Capacity enforced here: if the
  /// queue is full the OLDEST item is dropped — emergencies are ordered,
  /// so the newest SOS is always the one that survives.
  Future<void> enqueue(OutboxItem item) async {
    final items = await _db.outboxItems();
    while (items.length >= capacity) {
      await _db.deleteOutboxItem(items.removeAt(0).nonce);
    }
    await _db.putOutboxItem(item);
  }

  /// Called after a live send succeeded so the row doesn't linger until the
  /// next drain.
  Future<void> remove(String nonce) => _db.deleteOutboxItem(nonce);

  /// Try to deliver every queued item that is not yet stale.
  ///
  /// Returns the number of items delivered. Failed items stay queued (with
  /// an incremented attempt count); stale items are dropped.
  Future<int> drain(ApiClient api) async {
    var delivered = 0;
    for (final item in await _db.outboxItems()) {
      if (_now() - item.ts > stalenessMs) {
        // Stale: receivers would ignore it anyway.
        await _db.deleteOutboxItem(item.nonce);
        continue;
      }
      try {
        await api.postEnvelope(
          circleId: item.circleId,
          kind: item.kind,
          ts: item.ts,
          nonce: item.nonce,
          ciphertext: item.ciphertext,
        );
        await _db.deleteOutboxItem(item.nonce);
        delivered++;
      } on ApiException catch (e) {
        if (e.statusCode == 403) {
          // Membership revoked while offline: this envelope will never be
          // accepted again. Drop instead of retrying forever.
          await _db.deleteOutboxItem(item.nonce);
          continue;
        }
        await _db.putOutboxItem(item.withAttempts(item.attempts + 1));
      } catch (_) {
        // Offline / timeout / malformed response — stay queued.
        await _db.putOutboxItem(item.withAttempts(item.attempts + 1));
      }
    }
    return delivered;
  }
}
