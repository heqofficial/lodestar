import 'dart:async';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:lodestar/core/api/api_client.dart';
import 'package:lodestar/core/ws/ws_connection.dart';

/// A resolving connect() means "the socket ended" — the loop always
/// reconnects until stop() or a 403 catch-up. Every test therefore
/// terminates via stop() or 403, never by letting the loop run forever.

/// Records every delay request instead of actually waiting.
final delays = <Duration>[];

Future<void> fakeDelay(Duration d, [FutureOr<void> Function()? c]) async {
  delays.add(d);
}

void main() {
  setUp(delays.clear);

  group('WsConnection reconnect loop', () {
    test('failures double the backoff, capped at maxDelay', () async {
      var attempts = 0;
      late WsConnection ws;
      ws = WsConnection(
        connect: () async {
          attempts++;
          if (attempts >= 7) ws.stop(); // end the loop deterministically
          throw const SocketException('down');
        },
        catchUp: () async {},
        delayFn: fakeDelay,
        baseDelay: const Duration(seconds: 2),
        maxDelay: const Duration(seconds: 60),
      );
      await ws.run();

      expect(attempts, 7);
      // base, 2*base, 4*base, 8*base, 16*base, then 32*2=64 capped to 60.
      expect(
        delays.map((d) => d.inSeconds).toList(),
        [2, 4, 8, 16, 32, 60],
      );
    });

    test('healthy round-trip resets the next delay to base', () async {
      var attempts = 0;
      late WsConnection ws;
      ws = WsConnection(
        connect: () async {
          attempts++;
          if (attempts == 2) return; // healthy: socket lived, then ended
          if (attempts >= 4) ws.stop();
          throw const SocketException('down');
        },
        catchUp: () async {},
        delayFn: fakeDelay,
        baseDelay: const Duration(seconds: 2),
        maxDelay: const Duration(seconds: 60),
      );
      await ws.run();

      expect(attempts, 4);
      // Without the reset after the healthy round this would be [2, 4, 8].
      expect(delays.map((d) => d.inSeconds).toList(), [2, 2, 2]);
    });

    test('403 from catch-up stops the loop (revoked membership)', () async {
      var connects = 0;
      final ws = WsConnection(
        connect: () async {
          connects++;
          throw const SocketException('socket dropped');
        },
        catchUp: () async => throw ApiException(403, 'revoked'),
        delayFn: fakeDelay,
      );
      await ws.run();
      expect(connects, 1); // one drop, then catch-up said: stop.
      expect(delays, isEmpty);
    });

    test('non-403 catch-up errors keep the loop alive', () async {
      var attempts = 0;
      late WsConnection ws;
      ws = WsConnection(
        connect: () async {
          attempts++;
          if (attempts >= 3) ws.stop();
          throw const SocketException('drop');
        },
        catchUp: () async => throw const SocketException('offline'),
        delayFn: fakeDelay,
      );
      await ws.run();
      expect(attempts, 3);
      expect(delays, hasLength(2)); // retried after each offline catch-up
    });

    test('stop() from inside connect ends cleanly without a delay',
        () async {
      var connects = 0;
      late WsConnection ws;
      ws = WsConnection(
        connect: () async {
          connects++;
          ws.stop();
        },
        catchUp: () async {},
        delayFn: fakeDelay,
      );
      await ws.run();
      expect(connects, 1);
      expect(delays, isEmpty);
    });
  });
}
