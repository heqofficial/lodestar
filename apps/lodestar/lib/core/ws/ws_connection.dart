import 'dart:async';

import '../api/api_client.dart';

/// Live circle socket with reconnect, capped exponential backoff, and
/// catch-up fetches after every drop.
///
/// Extracted from AppState's `_wsLoop` with two seams so the previously
/// untested reconnect behavior is now unit-testable:
///  * [delayFn] replaces `Future.delayed` (tests run instantly),
///  * [catchUp] is the app's "fetch what I missed" policy (latest per
///    device+kind + offline chat + emergency drain).
///
/// Backoff: 2s → 4 → 8 → 16 → 32 → 60s cap on consecutive failures.
/// Healthy round-trips reset it. A 403 during catch-up stops the loop —
/// membership is revoked; retrying would hammer the server with requests
/// that can never succeed.
class WsConnection {
  WsConnection({
    required this.connect,
    required this.catchUp,
    this.delayFn = Future.delayed,
    this.baseDelay = const Duration(seconds: 2),
    this.maxDelay = const Duration(seconds: 60),
  });

  /// Opens the live stream (attaching the app's own listener) and resolves
  /// when the socket drops — cleanly or with an error.
  final Future<void> Function() connect;

  /// Runs after every drop, before reconnecting. Throws 403 ApiException
  /// to stop the loop when membership was revoked.
  final Future<void> Function() catchUp;

  /// Injectable delay for tests.
  final Future<void> Function(Duration, [FutureOr<void> Function()?]) delayFn;

  final Duration baseDelay;
  final Duration maxDelay;

  Future<void>? _done;
  bool _stopped = false;

  /// Stops the loop. Idempotent; safe to call from any callback.
  void stop() => _stopped = true;

  /// Runs the reconnect loop until [stop] or a 403 from [catchUp].
  Future<void> run() async {
    if (_done != null) return _done;
    _done = _loop();
    try {
      await _done;
    } finally {
      _done = null;
    }
  }

  Future<void> _loop() async {
    var delay = baseDelay;
    while (!_stopped) {
      var healthy = false;
      try {
        await connect();
        // The socket delivered (possibly for a long time) before ending:
        // reset the backoff, per "healthy round-trips reset it".
        healthy = true;
        delay = baseDelay;
      } catch (_) {
        // fallthrough to reconnect; the catch-up fetch below decides
        // whether the server still accepts us (403 = membership revoked).
      }
      if (_stopped) return;
      // Catch up on anything missed while the socket was down before
      // reconnecting, so the map never goes stale after a drop.
      try {
        await catchUp();
      } on ApiException catch (e) {
        // 403: membership revoked — stop the reconnect loop.
        if (e.statusCode == 403) return;
      } catch (_) {
        // offline — reconnect and retry
      }
      await delayFn(delay);
      if (_stopped) return;
      if (!healthy) {
        delay = delay * 2;
        if (delay > maxDelay) delay = maxDelay;
      }
    }
  }
}
