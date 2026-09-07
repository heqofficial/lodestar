import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;
import 'package:web_socket_channel/io.dart';

import 'models.dart';

/// Thin typed client for the lodestard REST + WebSocket API.
///
/// The client never parses envelope contents — those are opaque ciphertext.
class ApiClient {
  ApiClient({required this.baseUrl, required this.token, http.Client? httpClient})
      : _http = httpClient ?? http.Client();

  final String baseUrl;
  final String token;
  final http.Client _http;

  Uri _u(String path, [Map<String, String>? query]) =>
      Uri.parse('$baseUrl$path').replace(queryParameters: query);

  Map<String, String> get _headers => {
        'Authorization': 'Bearer $token',
        'Content-Type': 'application/json',
      };

  Future<Map<String, dynamic>> _send(String method, String path,
      {Object? body, Map<String, String>? query}) async {
    final req = http.Request(method, _u(path, query))..headers.addAll(_headers);
    if (body != null) {
      req.body = jsonEncode(body);
    }
    final streamed = await _http.send(req);
    final resp = await http.Response.fromStream(streamed);
    final decoded = resp.body.isEmpty ? <String, dynamic>{} : jsonDecode(resp.body) as Map<String, dynamic>;
    if (resp.statusCode >= 400) {
      throw ApiException(resp.statusCode, decoded['error'] as String? ?? 'HTTP ${resp.statusCode}');
    }
    return decoded;
  }

  // --- devices -------------------------------------------------------------

  static Future<({Device device, String token})> registerDevice({
    required String baseUrl,
    required String name,
    required String ed25519Pub,
    required String x25519Pub,
  }) async {
    final client = http.Client();
    try {
      final resp = await client.post(
        Uri.parse('$baseUrl/api/v1/devices'),
        headers: {'Content-Type': 'application/json'},
        body: jsonEncode({'name': name, 'ed25519_pub': ed25519Pub, 'x25519_pub': x25519Pub}),
      );
      if (resp.statusCode != 201) {
        throw ApiException(resp.statusCode, 'Registration failed (${resp.statusCode})');
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
    return (j['circles'] as List<dynamic>).map((e) => Circle.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<Circle> createCircle(String name, String color) async {
    final j = await _send('POST', '/api/v1/circles', body: {'name': name, 'color': color});
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
      (j['members'] as List<dynamic>).map((e) => CircleMember.fromJson(e as Map<String, dynamic>)).toList(),
    );
  }

  Future<String> createInvite(String circleId, {int ttlHours = 168}) async {
    final j = await _send('POST', '/api/v1/circles/$circleId/invites', body: {'ttl_hours': ttlHours});
    return j['code'] as String;
  }

  Future<void> setSharingFor(String circleId, String deviceId, bool enabled) async {
    await _send('POST', '/api/v1/circles/$circleId/members/$deviceId/sharing', body: {'enabled': enabled});
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
    final j = await _send('POST', '/api/v1/circles/$circleId/envelopes',
        body: {'kind': kind, 'ts': ts, 'nonce': nonce, 'ciphertext': ciphertext});
    return Envelope.fromJson(j);
  }

  Future<List<Envelope>> getEnvelopes(String circleId, {int since = 0, String? kind, int limit = 200}) async {
    final j = await _send('GET', '/api/v1/circles/$circleId/envelopes',
        query: {'since': '$since', 'kind': ?kind, 'limit': '$limit'});
    return (j['envelopes'] as List<dynamic>).map((e) => Envelope.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<List<Envelope>> latestEnvelopes(String circleId) async {
    final j = await _send('GET', '/api/v1/circles/$circleId/envelopes/latest');
    return (j['envelopes'] as List<dynamic>).map((e) => Envelope.fromJson(e as Map<String, dynamic>)).toList();
  }

  // --- circle keys ---------------------------------------------------------

  Future<void> putKeyBlob(String circleId, String forDevice, String ciphertext) async {
    await _send('POST', '/api/v1/circles/$circleId/keys', body: {'for_device': forDevice, 'ciphertext': ciphertext});
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
  Stream<Envelope> liveStream(String circleId) {
    final wsBase = baseUrl.replaceFirst(RegExp(r'^http'), 'ws');
    final channel = IOWebSocketChannel.connect(
      Uri.parse('$wsBase/api/v1/ws?circle=$circleId'),
      headers: {'Authorization': 'Bearer $token'},
    );
    return channel.stream.map((raw) {
      final j = jsonDecode(raw as String) as Map<String, dynamic>;
      return Envelope.fromJson(j);
    });
  }
}

class ApiException implements Exception {
  ApiException(this.statusCode, this.message);

  final int statusCode;
  final String message;

  @override
  String toString() => message;
}