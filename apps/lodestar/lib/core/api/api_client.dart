import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;
import 'package:web_socket_channel/io.dart';

import 'models.dart';

/// Thin typed client for the lodestard REST + WebSocket API.
///
/// The client never parses envelope contents — those are opaque ciphertext.
class ApiClient {
  ApiClient({
    required this.baseUrl,
    required this.token,
    http.Client? httpClient,
  }) : _http = httpClient ?? http.Client();

  final String baseUrl;
  final String token;
  final http.Client _http;

  /// Every request must answer (or fail) within this window; a hung server
  /// must never hang the app.
  static const timeout = Duration(seconds: 15);

  Uri _u(String path, [Map<String, String>? query]) =>
      Uri.parse('$baseUrl$path').replace(queryParameters: query);

  Map<String, String> get _headers => {
    'Authorization': 'Bearer $token',
    'Content-Type': 'application/json',
  };

  Future<Map<String, dynamic>> _send(
    String method,
    String path, {
    Object? body,
    Map<String, String>? query,
  }) async {
    final req = http.Request(method, _u(path, query))..headers.addAll(_headers);
    if (body != null) {
      req.body = jsonEncode(body);
    }
    final streamed = await _http.send(req).timeout(timeout);
    final resp = await http.Response.fromStream(streamed).timeout(timeout);
    final Map<String, dynamic> decoded;
    try {
      decoded = resp.body.isEmpty
          ? <String, dynamic>{}
          : jsonDecode(resp.body) as Map<String, dynamic>;
    } on FormatException {
      // Non-JSON error body (proxy error page, etc.) — surface the status.
      throw ApiException(resp.statusCode, 'HTTP ${resp.statusCode}');
    }
    if (resp.statusCode >= 400) {
      throw ApiException(
        resp.statusCode,
        decoded['error'] as String? ?? 'HTTP ${resp.statusCode}',
      );
    }
    return decoded;
  }

  // --- devices -------------------------------------------------------------

  /// Registers (or, with an empty token, clears) this device's APNs token.
  /// Tokens rotate and die; the server drops ones Apple rejects.
  Future<void> setPushToken(String apnsToken) =>
      _send('PUT', '/api/v1/devices/push', body: {'apns_token': apnsToken});

  /// Revokes this device server-side: token, memberships, key blobs, live
  /// sockets. After a 204 the token is dead — sign out locally and wipe.
  Future<void> deleteSelf() => _send('DELETE', '/api/v1/devices/self');

  static Future<({Device device, String token})> registerDevice({
    required String baseUrl,
    required String name,
    required String ed25519Pub,
    required String x25519Pub,
  }) async {
    final client = http.Client();
    try {
      final resp = await client
          .post(
            Uri.parse('$baseUrl/api/v1/devices'),
            headers: {'Content-Type': 'application/json'},
            body: jsonEncode({
              'name': name,
              'ed25519_pub': ed25519Pub,
              'x25519_pub': x25519Pub,
            }),
          )
          .timeout(timeout);
      if (resp.statusCode != 201) {
        throw ApiException(
          resp.statusCode,
          'Registration failed (${resp.statusCode})',
        );
      }
      final j = jsonDecode(resp.body) as Map<String, dynamic>;
      return (
        device: Device.fromJson(j['device'] as Map<String, dynamic>),
        token: j['token'] as String,
      );
    } finally {
      client.close();
    }
  }

  // --- circles -------------------------------------------------------------

  Future<List<Circle>> listCircles() async {
    final j = await _send('GET', '/api/v1/circles');
    return (j['circles'] as List<dynamic>)
        .map((e) => Circle.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<Circle> createCircle(String name, String color) async {
    final j = await _send(
      'POST',
      '/api/v1/circles',
      body: {'name': name, 'color': color},
    );
    return Circle.fromJson(j);
  }

  Future<Circle> joinCircle(String code) async {
    final j = await _send('POST', '/api/v1/circles/join', body: {'code': code});
    return Circle.fromJson(j);
  }

  Future<(Circle, List<CircleMember>)> circleDetail(String circleId) async {
    final j = await _send('GET', '/api/v1/circles/$circleId');
    return (
      Circle.fromJson(j['circle'] as Map<String, dynamic>),
      (j['members'] as List<dynamic>)
          .map((e) => CircleMember.fromJson(e as Map<String, dynamic>))
          .toList(),
    );
  }

  Future<String> createInvite(String circleId, {int ttlHours = 168}) async {
    final j = await _send(
      'POST',
      '/api/v1/circles/$circleId/invites',
      body: {'ttl_hours': ttlHours},
    );
    return j['code'] as String;
  }

  Future<void> setSharingFor(
    String circleId,
    String deviceId,
    bool enabled,
  ) async {
    await _send(
      'POST',
      '/api/v1/circles/$circleId/members/$deviceId/sharing',
      body: {'enabled': enabled},
    );
  }

  Future<void> leaveCircle(String circleId, String deviceId) async {
    await _send('DELETE', '/api/v1/circles/$circleId/members/$deviceId');
  }

  // --- envelopes -----------------------------------------------------------

  Future<Envelope> postEnvelope({
    required String circleId,
    required String kind,
    required int ts,
    required String nonce,
    required String ciphertext,
  }) async {
    final j = await _send(
      'POST',
      '/api/v1/circles/$circleId/envelopes',
      body: {'kind': kind, 'ts': ts, 'nonce': nonce, 'ciphertext': ciphertext},
    );
    return Envelope.fromJson(j);
  }

  Future<List<Envelope>> getEnvelopes(
    String circleId, {
    int since = 0,
    String? kind,
    String? device,
    int limit = 200,
  }) async {
    final j = await _send(
      'GET',
      '/api/v1/circles/$circleId/envelopes',
      query: {'since': '$since', 'kind': ?kind, 'device': ?device, 'limit': '$limit'},
    );
    return (j['envelopes'] as List<dynamic>)
        .map((e) => Envelope.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<List<Envelope>> latestEnvelopes(String circleId) async {
    final j = await _send('GET', '/api/v1/circles/$circleId/envelopes/latest');
    return (j['envelopes'] as List<dynamic>)
        .map((e) => Envelope.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  // --- circle keys ---------------------------------------------------------

  Future<void> putKeyBlob(
    String circleId,
    String forDevice,
    String ciphertext,
  ) async {
    await _send(
      'POST',
      '/api/v1/circles/$circleId/keys',
      body: {'for_device': forDevice, 'ciphertext': ciphertext},
    );
  }

  Future<String?> getKeyBlob(String circleId) async {
    try {
      final j = await _send('GET', '/api/v1/circles/$circleId/keys/mine');
      return j['ciphertext'] as String?;
    } on ApiException catch (e) {
      if (e.statusCode == 404) return null;
      rethrow;
    }
  }

  // --- websocket -----------------------------------------------------------

  /// Opens a live envelope stream for [circleId].
  ///
  /// Keepalive: protocol-level pings alone are not enough — coder/websocket
  /// (server) only re-arms its 90s read deadline when a *data* message
  /// completes a read. So we also send a tiny JSON keepalive every 25s;
  /// the server discards it. Both are cancelled when the stream closes.
  Stream<Envelope> liveStream(String circleId) {
    final wsBase = baseUrl.replaceFirst(RegExp(r'^http'), 'ws');
    final channel = IOWebSocketChannel.connect(
      Uri.parse('$wsBase/api/v1/ws?circle=$circleId'),
      headers: {'Authorization': 'Bearer $token'},
      pingInterval: const Duration(seconds: 25),
      connectTimeout: timeout,
    );
    late final Timer keepalive;
    keepalive = Timer.periodic(const Duration(seconds: 25), (_) {
      try {
        channel.sink.add(jsonEncode({'type': 'ping'}));
      } catch (_) {
        // Socket already gone (e.g. the subscription was cancelled, which
        // does not fire handleDone) — stop pinging.
        keepalive.cancel();
      }
    });
    return channel.stream
        .map((raw) {
          final j = jsonDecode(raw as String) as Map<String, dynamic>;
          return Envelope.fromJson(j);
        })
        .handleError((Object _) {})
        .transform(
          StreamTransformer.fromHandlers(handleDone: (sink) {
            // Errors were swallowed above, so done fires on both a clean
            // close and a failed socket — cancel the keepalive either way.
            keepalive.cancel();
            channel.sink.close();
            sink.close();
          }),
        );
  }
}

class ApiException implements Exception {
  ApiException(this.statusCode, this.message);

  final int statusCode;
  final String message;

  @override
  String toString() => message;
}
