import 'dart:async';
import 'dart:convert';
import 'dart:math';

import 'package:flutter/foundation.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../core/api/api_client.dart';
import '../core/api/models.dart';
import '../core/crypto/crypto_service.dart';
import '../core/store/local_db.dart';
import '../core/tracking/adaptive_tracker.dart';
import '../core/tracking/geofence_engine.dart';

/// Single source of truth for the UI: identity, circles, live positions,
/// chat, places, SOS/check-in state, and the tracking loop.
class AppState extends ChangeNotifier {
  AppState({CryptoService? crypto, LocalDb? db})
      : crypto = crypto ?? CryptoService(SecureKeyStore()) {
    _dbFuture = db != null ? Future.value(db) : LocalDb.open();
  }

  final CryptoService crypto;
  late final Future<LocalDb> _dbFuture;

  // --- identity / settings ---
  String serverUrl = '';
  String deviceId = '';
  String deviceName = '';
  String token = '';
  bool registered = false;

  // --- circles ---
  final List<Circle> circles = [];
  final Map<String, List<CircleMember>> membersByCircle = {};
  String? activeCircleId;

  // --- live state ---
  final Map<String, Position> positionsByDevice = {};
  final Map<String, Place> places = {};
  final List<({String deviceId, String kind, String text, int ts})> events = [];
  final Map<String, List<({String deviceId, String text, int ts})>> chatByCircle = {};

  // --- status ---
  bool tracking = false;
  bool sharing = true;
  String? lastError;

  ApiClient? _api;
  LocalDb? _db;
  AdaptiveTracker? _tracker;
  GeofenceEngine? _geofence;
  StreamSubscription<Envelope>? _wsSub;
  bool _disposed = false;
  final List<Envelope> _pending = [];

  ApiClient get api {
    final a = _api;
    if (a == null) throw StateError('not registered');
    return a;
  }

  bool get hasActiveCircle => activeCircleId != null;
  String get activeCircleIdSafe => activeCircleId ?? '';
  List<CircleMember> get activeMembers =>
      activeCircleId == null ? [] : membersByCircle[activeCircleId] ?? [];

  CircleMember? get selfMember {
    final m = activeMembers.where((m) => m.deviceId == deviceId).firstOrNull;
    return m;
  }

  String? get ownerId => activeCircleId == null ? null : _ownerId(activeCircleId!);
  String? _ownerId(String circleId) {
    final m = membersByCircle[circleId]?.where((m) => m.role == 'owner').firstOrNull;
    return m?.deviceId;
  }

  // -------------------------------------------------------------------------
  // bootstrap
  // -------------------------------------------------------------------------

  Future<void> init() async {
    final prefs = await SharedPreferences.getInstance();
    serverUrl = prefs.getString('server_url') ?? '';
    deviceId = prefs.getString('device_id') ?? '';
    deviceName = prefs.getString('device_name') ?? '';
    token = prefs.getString('token') ?? '';
    registered = deviceId.isNotEmpty && token.isNotEmpty;
    await crypto.init();
    notifyListeners();
  }

  Future<void> register({required String baseUrl, required String name}) async {
    serverUrl = _normalizeBase(baseUrl);
    deviceName = name.trim().isEmpty ? 'My device' : name.trim();
    final res = await ApiClient.registerDevice(
      baseUrl: serverUrl,
      name: deviceName,
      ed25519Pub: crypto.ed25519PubB64,
      x25519Pub: crypto.x25519PubB64,
    );
    deviceId = res.device.id;
    token = res.token;
    registered = true;
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString('server_url', serverUrl);
    await prefs.setString('device_id', deviceId);
    await prefs.setString('device_name', deviceName);
    await prefs.setString('token', token);
    _api = ApiClient(baseUrl: serverUrl, token: token);
    notifyListeners();
  }

  Future<void> load() async {
    _db = await _dbFuture;
    if (!registered) return;
    _api = ApiClient(baseUrl: serverUrl, token: token);
    try {
      final list = await api.listCircles();
      circles
        ..clear()
        ..addAll(list);
      for (final c in list) {
        await _db!.upsertCircle(c);
        final (circle, members) = await api.circleDetail(c.id);
        membersByCircle[c.id] = members;
        await _db!.upsertMembers(c.id, members);
        if (_ownerId(c.id) == deviceId) {
          await _ensureOwnedCircleKey(c.id, circle);
        } else {
          await _tryFetchCircleKey(c.id);
        }
      }
      if (circles.isNotEmpty && activeCircleId == null) {
        activeCircleId = circles.first.id;
      }
      if (hasActiveCircle) {
        await _loadCircleState(activeCircleId!);
      }
      notifyListeners();
    } catch (e) {
      lastError = '$e';
      notifyListeners();
    }
  }

  Future<void> _ensureOwnedCircleKey(String circleId, Circle circle) async {
    if (await crypto.circleKey(circleId) != null) return;
    // We created (or own) this circle but have no key — create and hold it.
    final key = await crypto.newCircleKey();
    await crypto.saveCircleKey(circleId, key);
  }

  Future<void> _tryFetchCircleKey(String circleId) async {
    final blob = await api.getKeyBlob(circleId);
    if (blob == null) return; // owner hasn't granted yet
    final owner = _ownerId(circleId);
    if (owner == null) return;
    final ownerXPub = membersByCircle[circleId]
        ?.where((m) => m.deviceId == owner)
        .firstOrNull
        ?.x25519Pub;
    if (ownerXPub == null || ownerXPub.isEmpty) return;
    try {
      final keyB64 = await crypto.openCircleKeyForMember(blob, ownerXPub);
      await crypto.saveCircleKey(circleId, Uint8List.fromList(base64Decode(keyB64)));
    } catch (_) {/* wrong key or tampered — owner will re-grant */}
  }

  // -------------------------------------------------------------------------
  // circles
  // -------------------------------------------------------------------------

  Future<void> createCircle(String name, String color) async {
    final c = await api.createCircle(name, color);
    final key = await crypto.newCircleKey();
    await crypto.saveCircleKey(c.id, key);
    circles.add(c);
    membersByCircle[c.id] = [CircleMember(
          circleId: c.id,
          deviceId: deviceId,
          role: 'owner',
          displayName: deviceName,
          avatarColor: color,
          ed25519Pub: crypto.ed25519PubB64,
          x25519Pub: crypto.x25519PubB64,
          sharingEnabled: true,
          joinedAt: 0,
        )];
    await _db?.upsertCircle(c);
    await _selectCircle(c.id);
    notifyListeners();
  }

  Future<void> joinCircle(String code) async {
    final c = await api.joinCircle(code.trim().toUpperCase());
    circles.add(c);
    await _db?.upsertCircle(c);
    await _selectCircle(c.id);
    await refreshCircle(c.id);
    // Request the circle key from the owner.
    await _tryFetchCircleKey(c.id);
    notifyListeners();
  }

  Future<void> refreshCircle(String circleId) async {
    try {
      final (circle, members) = await api.circleDetail(circleId);
      membersByCircle[circleId] = members;
      await _db?.upsertMembers(circleId, members);
      if (_ownerId(circleId) == deviceId && await crypto.circleKey(circleId) == null) {
        await _ensureOwnedCircleKey(circleId, circle);
      } else if (_ownerId(circleId) != deviceId) {
        await _tryFetchCircleKey(circleId);
      }
      notifyListeners();
    } catch (e) {
      lastError = '$e';
      notifyListeners();
    }
  }

  Future<void> grantCircleKeyTo(String memberId) async {
    final circleId = activeCircleId;
    final member = activeMembers.where((m) => m.deviceId == memberId).firstOrNull;
    if (circleId == null || member == null) return;
    final key = await crypto.circleKey(circleId);
    if (key == null) return;
    final blob = await crypto.sealCircleKeyForMember(base64Encode(key), member.x25519Pub);
    await api.putKeyBlob(circleId, memberId, blob);
  }

  Future<void> selectCircle(String circleId) async {
    await _selectCircle(circleId);
    notifyListeners();
  }

  Future<void> _selectCircle(String circleId) async {
    activeCircleId = circleId;
    await _wsSub?.cancel();
    _wsSub = null;
    positionsByDevice.clear();
    places.clear();
    await _loadCircleState(circleId);
    _startLiveStream(circleId);
  }

  Future<void> _loadCircleState(String circleId) async {
    // Latest positions + places from the server cache, then local cache.
    try {
      final envs = await api.latestEnvelopes(circleId);
      for (final e in envs) {
        await _ingest(e);
      }
    } catch (_) {
      final cached = await _db?.cachedEnvelopes(circleId) ?? [];
      for (final e in cached) {
        await _ingest(e);
      }
    }
  }

  // -------------------------------------------------------------------------
  // envelope processing
  // -------------------------------------------------------------------------

  Future<void> _ingest(Envelope env) async {
    await _db?.upsertEnvelope(env);
    final key = await crypto.circleKey(env.circleId);
    if (key == null) {
      _pending.add(env);
      return;
    }
    final sender = membersByCircle[env.circleId]?.where((m) => m.deviceId == env.deviceId).firstOrNull;
    if (sender == null) return;
    try {
      final open = await crypto.openEnvelope(
        circleId: env.circleId,
        nonceB64: env.nonce,
        ciphertextB64: env.ciphertext,
        senderPubEd25519B64: sender.ed25519Pub,
      );
      _dispatch(env.circleId, open);
    } catch (_) {
      // Tampered or not decryptable with current key — ignore.
    }
  }

  void _dispatch(String circleId, Map<String, dynamic> open) {
    final data = (open['data'] as Map<String, dynamic>? ?? {});
    final kind = open['kind'] as String? ?? '';
    final sender = open['sender'] as String? ?? '';
    final ts = (open['ts'] as num?)?.toInt() ?? 0;
    switch (kind) {
      case 'location':
        positionsByDevice[sender] = Position.fromData(data, ts: ts);
      case 'place':
        places[data['id'] as String? ?? sender] = Place.fromData(data);
      case 'place_del':
        places.remove(data['id']);
      case 'message':
        chatByCircle.putIfAbsent(circleId, () => []).add((
          deviceId: sender,
          text: data['text'] as String? ?? '',
          ts: ts,
        ));
      case 'checkin':
        events.insert(0, (deviceId: sender, kind: 'checkin', text: data['text'] as String? ?? 'checked in', ts: ts));
      case 'sos':
        events.insert(0, (deviceId: sender, kind: 'sos', text: data['text'] as String? ?? 'SOS', ts: ts));
      case 'geofence':
        events.insert(0, (
          deviceId: sender,
          kind: 'geofence',
          text: '${data['event'] == 'enter' ? 'arrived at' : 'left'} ${data['place_name'] ?? 'a place'}',
          ts: ts,
        ));
    }
    notifyListeners();
  }

  void _startLiveStream(String circleId) {
    unawaited(_wsLoop(circleId));
  }

  Future<void> _wsLoop(String circleId) async {
    while (!_disposed && activeCircleId == circleId) {
      try {
        _wsSub = api.liveStream(circleId).listen((env) {
          unawaited(_ingest(env));
        });
        await _wsSub!.asFuture<void>().catchError((_) => null);
      } catch (_) {
        // fallthrough to reconnect
      }
      if (_disposed || activeCircleId != circleId) return;
      await Future<void>.delayed(const Duration(seconds: 5));
    }
  }

  // -------------------------------------------------------------------------
  // sharing
  // -------------------------------------------------------------------------

  Future<void> startTracking() async {
    if (tracking) return;
    if (!hasActiveCircle) return;
    try {
      _tracker ??= AdaptiveTracker(onFix: _onTrackerFix);
      final key = await crypto.circleKey(activeCircleId!);
      if (key == null) throw StateError('circle key missing — ask the owner to grant access');
      await _tracker!.start();
      _geofence = GeofenceEngine(places: places.values.toList(), onEvent: _onGeofenceEvent);
      tracking = true;
      notifyListeners();
    } catch (e) {
      lastError = '$e';
      notifyListeners();
    }
  }

  Future<void> setSharing(bool enabled) async {
    sharing = enabled;
    await _tracker?.setPaused(!enabled);
    try {
      await api.setSharingFor(activeCircleIdSafe, deviceId, enabled);
    } catch (_) {}
    notifyListeners();
  }

  void _onTrackerFix(Position fix) {
    unawaited(_shareFix(fix));
  }

  Future<void> _shareFix(Position fix) async {
    final circleId = activeCircleId;
    if (circleId == null || !sharing) return;
    try {
      final sealed = await crypto.sealEnvelope(
        circleId: circleId,
        deviceId: deviceId,
        kind: 'location',
        ts: fix.ts,
        data: fix.toData(),
      );
      final env = await api.postEnvelope(
        circleId: circleId,
        kind: 'location',
        ts: fix.ts,
        nonce: sealed.nonce,
        ciphertext: sealed.ciphertext,
      );
      await _db?.upsertEnvelope(env);
      // On-device geofence evaluation happens on our own fixes.
      _geofence?.onPosition(fix);
      // Refresh place list occasionally (geofence engine snapshot).
      if (places.isNotEmpty && (_geofence == null || _geofence!.places.length != places.length)) {
        _geofence = GeofenceEngine(places: places.values.toList(), onEvent: _onGeofenceEvent);
      }
      notifyListeners();
    } catch (_) {
      // Network hiccup — next fix retries.
    }
  }

  void _onGeofenceEvent(Place place, String event) {
    final circleId = activeCircleId;
    if (circleId == null) return;
    unawaited(() async {
      final now = DateTime.now().millisecondsSinceEpoch;
      final sealed = await crypto.sealEnvelope(
        circleId: circleId,
        deviceId: deviceId,
        kind: 'geofence',
        ts: now,
        data: {'place_id': place.id, 'place_name': place.name, 'event': event},
      );
      await api.postEnvelope(
        circleId: circleId,
        kind: 'geofence',
        ts: now,
        nonce: sealed.nonce,
        ciphertext: sealed.ciphertext,
      );
    }());
  }

  // -------------------------------------------------------------------------
  // actions
  // -------------------------------------------------------------------------

  Future<void> addPlace(String name, double lat, double lng, double radiusM) async {
    final circleId = activeCircleId;
    if (circleId == null) return;
    final id = _genId();
    final place = Place(id: id, name: name, lat: lat, lng: lng, radiusM: radiusM, ts: DateTime.now().millisecondsSinceEpoch);
    places[id] = place;
    await _postDataEnvelope(circleId, 'place', place.toData());
    _geofence = GeofenceEngine(places: places.values.toList(), onEvent: _onGeofenceEvent);
    notifyListeners();
  }

  Future<void> deletePlace(String id) async {
    final circleId = activeCircleId;
    if (circleId == null) return;
    places.remove(id);
    await _postDataEnvelope(circleId, 'place_del', {'id': id});
    _geofence = GeofenceEngine(places: places.values.toList(), onEvent: _onGeofenceEvent);
    notifyListeners();
  }

  Future<void> sendMessage(String text) async {
    final circleId = activeCircleId;
    if (circleId == null || text.trim().isEmpty) return;
    final now = DateTime.now().millisecondsSinceEpoch;
    chatByCircle.putIfAbsent(circleId, () => []).add((
      deviceId: deviceId,
      text: text.trim(),
      ts: now,
    ));
    await _postDataEnvelope(circleId, 'message', {'text': text.trim()});
    notifyListeners();
  }

  Future<void> checkIn({String note = ''}) async {
    final circleId = activeCircleId;
    if (circleId == null) return;
    final now = DateTime.now().millisecondsSinceEpoch;
    events.insert(0, (deviceId: deviceId, kind: 'checkin', text: note.isEmpty ? 'checked in' : note, ts: now));
    await _postDataEnvelope(circleId, 'checkin', {'text': note});
    notifyListeners();
  }

  Future<void> sendSos({String note = ''}) async {
    final circleId = activeCircleId;
    if (circleId == null) return;
    final now = DateTime.now().millisecondsSinceEpoch;
    final pos = positionsByDevice[deviceId];
    events.insert(0, (deviceId: deviceId, kind: 'sos', text: note.isEmpty ? 'SOS' : note, ts: now));
    await _postDataEnvelope(circleId, 'sos', {
      'text': note,
      if (pos != null) 'lat': pos.lat,
      if (pos != null) 'lng': pos.lng,
    });
    notifyListeners();
  }

  Future<void> _postDataEnvelope(String circleId, String kind, Map<String, dynamic> data) async {
    final now = DateTime.now().millisecondsSinceEpoch;
    final sealed = await crypto.sealEnvelope(
      circleId: circleId,
      deviceId: deviceId,
      kind: kind,
      ts: now,
      data: data,
    );
    await api.postEnvelope(
      circleId: circleId,
      kind: kind,
      ts: now,
      nonce: sealed.nonce,
      ciphertext: sealed.ciphertext,
    );
  }

  // -------------------------------------------------------------------------

  String _genId() {
    final rand = Random.secure();
    final b = List<int>.generate(16, (_) => rand.nextInt(256));
    return b.map((x) => x.toRadixString(16).padLeft(2, '0')).join();
  }

  static String _normalizeBase(String url) {
    var u = url.trim();
    if (!u.startsWith('http://') && !u.startsWith('https://')) {
      u = 'http://$u';
    }
    return u.endsWith('/') ? u.substring(0, u.length - 1) : u;
  }

  @override
  void dispose() {
    _disposed = true;
    _wsSub?.cancel();
    unawaited(_tracker?.stop());
    super.dispose();
  }
}