import 'dart:async';
import 'dart:convert';
import 'dart:math';

import 'package:flutter/foundation.dart';
import 'package:sensors_plus/sensors_plus.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../core/api/api_client.dart';
import '../core/api/models.dart';
import '../core/crypto/crypto_service.dart';
import '../core/platform/battery.dart';
import '../core/platform/push_tokens.dart';
import '../core/store/local_db.dart';
import '../core/tracking/adaptive_tracker.dart';
import '../core/tracking/crash_detector.dart';
import '../core/tracking/geofence_engine.dart';
import '../core/tracking/trip_detector.dart';

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
const Set<String> optimisticKinds = {'message', 'checkin', 'sos', 'trip', 'crash'};

/// True when [envNonce] is one this device already sent AND [kind] is an
/// optimistically-displayed kind — i.e. [envNonce] is our own echo. The
/// match consumes the nonce so a single echo suppresses exactly once.
bool isOwnEcho(Set<String> sentNonces, String envNonce, String kind) =>
    optimisticKinds.contains(kind) && sentNonces.remove(envNonce);

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
  final Map<String, List<({String deviceId, String text, int ts})>>
  chatByCircle = {};

  // --- status ---
  bool tracking = false;
  bool sharing = true;
  String? lastError;

  /// True while the initial sync (circles, members, keys) is running after
  /// an app restart. UI shows a loading state instead of a blank screen.
  bool booting = false;

  /// True once this device holds the active circle's key (sync mirror of the
  /// keystore, kept current by the key-management paths).
  bool hasCircleKey = false;

  /// Speeding alert threshold for driving reports (km/h).
  double speedingLimitKmh = 90;

  ApiClient? _api;
  LocalDb? _db;
  AdaptiveTracker? _tracker;
  GeofenceEngine? _geofence;
  TripDetector? _trips;
  CrashDetector? _crash;
  StreamSubscription<Envelope>? _wsSub;
  StreamSubscription<dynamic>? _accelSub;
  Timer? _housekeepingTimer;
  bool _disposed = false;

  /// Envelopes received while we had no circle key yet; flushed after grant.
  final List<Envelope> _pending = [];

  /// Envelope ids already processed (replay protection).
  final Set<String> _seenIds = {};

  /// Nonces of envelopes this device just sent. The server echoes every
  /// envelope back to the sender's own socket, so optimistic inserts
  /// (chat, check-in, SOS, trip, crash) would otherwise double up.
  final Set<String> _sentNonces = {};

  /// Emergency envelopes (SOS/crash) that failed to post while offline.
  /// Retried on every reconnect/housekeeping tick until delivered or
  /// stale (>10 min, matching the receivers' staleness window) — an
  /// emergency that silently vanishes is worse than a late one.
  final List<({String circleId, String kind, int ts, String nonce, String ciphertext})>
  _emergencyOutbox = [];

  void _rememberSent(String nonce) {
    _sentNonces.add(nonce);
    if (_sentNonces.length > 1000) {
      _sentNonces.remove(_sentNonces.first);
    }
  }
  int _memberCount = 0; // last known member count of the active circle

  /// Newest chat message ts per circle — cursor for catch-up after a socket
  /// drop (reconnect would otherwise lose messages relayed while offline).
  final Map<String, int> _lastChatTsByCircle = {};

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

  String? get ownerId =>
      activeCircleId == null ? null : _ownerId(activeCircleId!);
  String? _ownerId(String circleId) {
    final m = membersByCircle[circleId]
        ?.where((m) => m.role == 'owner')
        .firstOrNull;
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
    speedingLimitKmh = prefs.getDouble('speeding_limit_kmh') ?? 90;
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
    unawaited(_syncPushToken());
    notifyListeners();
  }

  Future<void> load() async {
    _db = await _dbFuture;
    if (!registered) return;
    booting = true;
    notifyListeners();
    _api = ApiClient(baseUrl: serverUrl, token: token);
    try {
      final list = await api.listCircles();
      circles
        ..clear()
        ..addAll(list);
      // Per-circle sync runs concurrently: N circles with a slow link must
      // not cost N sequential round trips.
      await Future.wait(
        list.map((c) async {
          await _db!.upsertCircle(c);
          final (circle, members) = await api.circleDetail(c.id);
          membersByCircle[c.id] = members;
          await _db!.upsertMembers(c.id, members);
          if (_ownerId(c.id) == deviceId) {
            await _ensureOwnedCircleKey(c.id, circle);
          } else {
            await _tryFetchCircleKey(c.id);
          }
        }),
      );
      if (circles.isNotEmpty && activeCircleId == null) {
        activeCircleId = circles.first.id;
      }
      if (hasActiveCircle) {
        await _loadCircleState(activeCircleId!);
      }
      _startHousekeeping();
      unawaited(_syncPushToken());
    } catch (e) {
      lastError = '$e';
    } finally {
      booting = false;
      notifyListeners();
    }
  }

  /// Registers the iOS APNs device token with the server. Android is a
  /// no-op (push rides the ntfy app there), and a failed sync must never
  /// break the app — push is a bonus, not a core path.
  Future<void> _syncPushToken() async {
    if (!registered || _api == null) return;
    final token = await PushTokenService.apnsToken();
    if (token.isNotEmpty) {
      await _api!.setPushToken(token);
    }
  }

  /// Periodic background jobs while the app lives:
  ///  * joiner without a key: keep asking the server until the owner grants one
  ///  * owner: notice new members and remind to grant keys
  ///
  /// One minute cadence: joining a circle is a human-paced action, so 60s
  /// is plenty of latency while costing a fraction of the network traffic
  /// of a 20s poll (the owner path fetches the full member list each tick).
  void _startHousekeeping() {
    var lastPruneDay = DateTime.now().day;
    _housekeepingTimer ??= Timer.periodic(const Duration(seconds: 60), (
      _,
    ) async {
      // Bound the on-device envelope cache once per day, and keep the
      // APNs token fresh (it rotates; a stale one silently stops push).
      final now = DateTime.now();
      if (now.day != lastPruneDay) {
        lastPruneDay = now.day;
        try {
          await _db?.prune();
        } catch (_) {}
        try {
          await _syncPushToken();
        } catch (_) {}
      }
      // Retry queued emergencies on a slow cadence (they also flush on
      // every WebSocket reconnect, which is the fast path).
      try {
        await _flushEmergencyOutbox();
      } catch (_) {}
      final circleId = activeCircleId;
      if (circleId == null) return;
      final key = await crypto.circleKey(circleId);
      final owner = _ownerId(circleId);
      try {
        if (owner != deviceId && key == null) {
          await _tryFetchCircleKey(circleId);
          if (await crypto.circleKey(circleId) != null) {
            _flushPending();
            notifyListeners();
          }
        } else if (owner == deviceId) {
          final members = await api.circleDetail(circleId);
          final count = members.$2.length;
          if (_memberCount != 0 && count > _memberCount) {
            final newcomers = members.$2
                .where((m) => m.deviceId != deviceId)
                .where(
                  (m) => !(membersByCircle[circleId] ?? const []).any(
                    (old) => old.deviceId == m.deviceId,
                  ),
                )
                .toList();
            for (final m in newcomers) {
              events.insert(0, (
                deviceId: m.deviceId,
                kind: 'member_join',
                text:
                    '${m.displayName} joined — grant the circle key in Members',
                ts: DateTime.now().millisecondsSinceEpoch,
              ));
            }
          }
          _memberCount = count;
          membersByCircle[circleId] = members.$2;
          await _db?.upsertMembers(circleId, members.$2);
          notifyListeners();
        }
      } catch (_) {
        // transient network error — retry next tick
      }
    });
  }

  Future<void> _ensureOwnedCircleKey(String circleId, Circle circle) async {
    if (await crypto.circleKey(circleId) != null) {
      _syncKeyFlag(circleId);
      return;
    }
    // We created (or own) this circle but have no key — create and hold it.
    final key = await crypto.newCircleKey();
    await crypto.saveCircleKey(circleId, key);
    _syncKeyFlag(circleId);
  }

  void _syncKeyFlag(String? circleId) {
    // Async read; flag flips when the read completes.
    unawaited(() async {
      hasCircleKey =
          circleId != null && await crypto.circleKey(circleId) != null;
      notifyListeners();
    }());
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
      await crypto.saveCircleKey(
        circleId,
        Uint8List.fromList(base64Decode(keyB64)),
      );
      _syncKeyFlag(circleId);
      _flushPending();
    } catch (_) {
      /* wrong key or tampered — owner will re-grant */
    }
  }

  // -------------------------------------------------------------------------
  // circles
  // -------------------------------------------------------------------------

  Future<void> createCircle(String name, String color) async {
    final c = await api.createCircle(name, color);
    final key = await crypto.newCircleKey();
    await crypto.saveCircleKey(c.id, key);
    hasCircleKey = true;
    circles.add(c);
    membersByCircle[c.id] = [
      CircleMember(
        circleId: c.id,
        deviceId: deviceId,
        role: 'owner',
        displayName: deviceName,
        avatarColor: color,
        ed25519Pub: crypto.ed25519PubB64,
        x25519Pub: crypto.x25519PubB64,
        sharingEnabled: true,
        joinedAt: 0,
      ),
    ];
    await _db?.upsertCircle(c);
    await _selectCircle(c.id);
    notifyListeners();
  }

  Future<void> joinCircle(String code) async {
    final c = await api.joinCircle(code.trim().toUpperCase());
    circles.add(c);
    hasCircleKey = false;
    await _db?.upsertCircle(c);
    // Membership (and with it the sender public keys) must be known BEFORE
    // the circle state is loaded: _ingest silently drops envelopes whose
    // sender it cannot identify, so the map would come up empty.
    await refreshCircle(c.id);
    await _selectCircle(c.id);
    // Request the circle key from the owner.
    await _tryFetchCircleKey(c.id);
    notifyListeners();
  }

  Future<void> refreshCircle(String circleId) async {
    try {
      final (circle, members) = await api.circleDetail(circleId);
      membersByCircle[circleId] = members;
      await _db?.upsertMembers(circleId, members);
      if (_ownerId(circleId) == deviceId &&
          await crypto.circleKey(circleId) == null) {
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
    final member = activeMembers
        .where((m) => m.deviceId == memberId)
        .firstOrNull;
    if (circleId == null || member == null) return;
    final key = await crypto.circleKey(circleId);
    if (key == null) return;
    final blob = await crypto.sealCircleKeyForMember(
      base64Encode(key),
      member.x25519Pub,
    );
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
    // 1) Hydrate from the on-device cache first, chronologically. The
    //    server's "latest per device+kind" endpoint carries only the single
    //    newest envelope of each kind — refreshing from it alone would
    //    wipe chat, places and events from the UI on every launch even
    //    though they are all cached locally.
    try {
      final cached =
          await _db?.cachedEnvelopes(circleId, ascending: true) ?? [];
      for (final e in cached) {
        await _ingest(e);
      }
    } catch (_) {
      // Cache unreadable — start empty rather than crash.
    }
    // 2) Freshen with the server's latest per device+kind (deduped by id
    //    against the cached batch above).
    try {
      final envs = await api.latestEnvelopes(circleId);
      for (final e in envs) {
        await _ingest(e);
      }
    } catch (_) {
      // Offline — the cache hydration above is what we have.
    }
  }

  // -------------------------------------------------------------------------
  // envelope processing
  // -------------------------------------------------------------------------

  Future<void> _ingest(Envelope env) async {
    // Replay protection: each envelope is processed at most once.
    if (!_seenIds.add(env.id)) return;
    if (_seenIds.length > 5000) {
      _seenIds.remove(_seenIds.first);
    }
    await _db?.upsertEnvelope(env);
    final key = await crypto.circleKey(env.circleId);
    if (key == null) {
      // Bounded queue: a chatty circle must not be able to exhaust memory
      // on a joiner who has no key yet (drop the oldest).
      if (_pending.length >= 1000) {
        _pending.removeAt(0);
      }
      _pending.add(env);
      return;
    }
    final sender = membersByCircle[env.circleId]
        ?.where((m) => m.deviceId == env.deviceId)
        .firstOrNull;
    if (sender == null) return;
    try {
      final open = await crypto.openEnvelope(
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
      // [_rememberSent]) — already in the UI, don't add it twice.
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
  void _flushPending() {
    final queued = List<Envelope>.from(_pending);
    _pending.clear();
    for (final env in queued) {
      unawaited(_ingest(env));
    }
  }

  /// Re-fetch the circle key from the server (used by the "Retry" button
  /// and by the housekeeping timer when the owner has granted access).
  Future<bool> retryCircleKey() async {
    final circleId = activeCircleId;
    if (circleId == null) return false;
    await refreshCircle(circleId);
    final key = await crypto.circleKey(circleId);
    if (key != null) {
      _flushPending();
      notifyListeners();
      return true;
    }
    return false;
  }

  void _dispatch(String circleId, Map<String, dynamic> open) {
    final data = (open['data'] as Map<String, dynamic>? ?? {});
    final kind = open['kind'] as String? ?? '';
    final sender = open['sender'] as String? ?? '';
    final ts = (open['ts'] as num?)?.toInt() ?? 0;
    switch (kind) {
      case 'location':
        // Replay guard: ignore stale fixes (older than the latest we hold).
        final current = positionsByDevice[sender];
        if (current != null && ts <= current.ts) return;
        positionsByDevice[sender] = Position.fromData(data, ts: ts);
      case 'place':
        places[data['id'] as String? ?? sender] = Place.fromData(data);
      case 'place_del':
        places.remove(data['id']);
      case 'message':
        final list = chatByCircle.putIfAbsent(
          circleId,
          () => [],
        )..add((deviceId: sender, text: data['text'] as String? ?? '', ts: ts));
        if (list.length > 500) {
          list.removeRange(0, list.length - 500);
        }
        _lastChatTsByCircle[circleId] = max(
          _lastChatTsByCircle[circleId] ?? 0,
          ts,
        );
      case 'checkin':
        events.insert(0, (
          deviceId: sender,
          kind: 'checkin',
          text: data['text'] as String? ?? 'checked in',
          ts: ts,
        ));
      case 'sos':
        events.insert(0, (
          deviceId: sender,
          kind: 'sos',
          text: data['text'] as String? ?? 'SOS',
          ts: ts,
        ));
      case 'geofence':
        events.insert(0, (
          deviceId: sender,
          kind: 'geofence',
          text:
              '${data['event'] == 'enter' ? 'arrived at' : 'left'} ${data['place_name'] ?? 'a place'}',
          ts: ts,
        ));
      case 'trip':
        events.insert(0, (
          deviceId: sender,
          kind: 'trip',
          text: _tripSummary(data),
          ts: ts,
        ));
      case 'crash':
        events.insert(0, (
          deviceId: sender,
          kind: 'crash',
          text: 'possible crash detected at ${_fmtLatLng(data)}',
          ts: ts,
        ));
    }
    if (events.length > 200) {
      events.removeRange(200, events.length);
    }
    notifyListeners();
  }

  static String _tripSummary(Map<String, dynamic> d) {
    final km = ((d['distance_m'] as num?)?.toDouble() ?? 0) / 1000;
    final maxKmh = ((d['max_speed_kmh'] as num?)?.toDouble() ?? 0).round();
    final speeding = (d['speeding_count'] as num?)?.toInt() ?? 0;
    return 'drove ${km.toStringAsFixed(1)} km · max $maxKmh km/h'
        '${speeding > 0 ? ' · $speeding speeding event${speeding == 1 ? '' : 's'}' : ''}';
  }

  static String _fmtLatLng(Map<String, dynamic> d) {
    final lat = (d['lat'] as num?)?.toDouble();
    final lng = (d['lng'] as num?)?.toDouble();
    if (lat == null || lng == null) return 'unknown location';
    return '${lat.toStringAsFixed(4)}, ${lng.toStringAsFixed(4)}';
  }

  void _startLiveStream(String circleId) {
    unawaited(_wsLoop(circleId));
  }

  Future<void> _wsLoop(String circleId) async {
    // Reconnect with capped exponential backoff: against a dead server,
    // a flat 5s cadence burns battery and hammers the network. Healthy
    // round-trips reset the backoff.
    var delay = const Duration(seconds: 2);
    while (!_disposed && activeCircleId == circleId) {
      try {
        _wsSub = api.liveStream(circleId).listen((env) {
          // Ignore stragglers from a previous circle's socket.
          if (env.circleId != circleId) return;
          unawaited(_ingest(env));
        });
        await _wsSub!.asFuture<void>().catchError((_) => null);
        delay = const Duration(seconds: 2);
      } catch (_) {
        // fallthrough to reconnect; the catch-up fetch below decides
        // whether the server still accepts us (403 = membership revoked).
      }
      if (_disposed || activeCircleId != circleId) return;
      // Catch up on anything missed while the socket was down before
      // reconnecting, so the map never goes stale after a drop. The 403
      // check here is what stops the loop when membership is revoked.
      try {
        final envs = await api.latestEnvelopes(circleId);
        for (final env in envs) {
          if (env.circleId == circleId) await _ingest(env);
        }
        // Chat is not part of "latest per device": pull any messages
        // relayed while we were disconnected.
        final since = (_lastChatTsByCircle[circleId] ?? 0) - 1;
        if (since >= 0) {
          final msgs = await api.getEnvelopes(
            circleId,
            since: since,
            kind: 'message',
            limit: 500,
          );
          for (final env in msgs) {
            if (env.circleId == circleId) await _ingest(env);
          }
        }
        // We are online again: deliver any queued emergencies.
        await _flushEmergencyOutbox();
      } on ApiException catch (e) {
        // 403: membership revoked — stop the reconnect loop.
        if (e.statusCode == 403) return;
      } catch (_) {
        // offline — reconnect and retry
      }
      await Future<void>.delayed(delay);
      if (delay < const Duration(seconds: 60)) {
        delay = delay * 2;
      }
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
      if (key == null) {
        throw StateError('circle key missing — ask the owner to grant access');
      }
      await _tracker!.start();
      // Background location dies on most phones without the battery
      // whitelist (Doze + OEM battery managers). One system dialog, then
      // tracking survives for days instead of hours.
      await requestBatteryOptimizationExemption();
      _geofence = GeofenceEngine(
        places: places.values.toList(),
        onEvent: _onGeofenceEvent,
      );
      _trips = TripDetector(
        speedLimitKmh: speedingLimitKmh,
        onTripEnded: _onTripEnded,
      );
      _crash = CrashDetector(onCrash: _onCrashConfirmed);
      // The accelerometer subscription is gated on movement by
      // _syncAccel (see below) — it starts out off.
      tracking = true;
      notifyListeners();
    } catch (e) {
      lastError = '$e';
      notifyListeners();
    }
  }

  /// Persists the speeding threshold (applies to trips started afterwards).
  Future<void> setSpeedingLimit(double kmh) async {
    speedingLimitKmh = kmh;
    final prefs = await SharedPreferences.getInstance();
    await prefs.setDouble('speeding_limit_kmh', kmh);
    notifyListeners();
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
    _trips?.onPosition(fix);
    _crash?.onPosition(fix);
    _syncAccel(fix);
    // Geofence evaluation must not depend on the network: a fix that fails
    // to post (offline) still needs to produce enter/leave events locally.
    _geofence?.onPosition(fix);
    unawaited(_shareFix(fix));
  }

  int? _slowSince;

  /// The accelerometer is only worth listening to while moving — a parked
  /// phone can't crash. Gating the subscription on movement keeps the
  /// sensor and its CPU wakes off during the hours a phone sits still,
  /// which matters against the <5% battery/day budget. The GPS speed-drop
  /// crash path works without the sensor either way.
  void _syncAccel(Position fix) {
    if (_crash == null) return;
    final active = _accelSub != null;
    if (fix.speed >= 6.0) {
      _slowSince = null;
      if (!active) {
        _accelSub = accelerometerEventStream().listen(
          (e) => _crash?.onAcceleration(e.x, e.y, e.z),
          onError: (_) {}, // no accelerometer: GPS-only detection
        );
      }
    } else if (fix.speed < 1.0) {
      _slowSince ??= fix.ts;
      // Stop listening after 2 stationary minutes; resume on next movement.
      if (active && fix.ts - _slowSince! > 2 * 60 * 1000) {
        _accelSub!.cancel();
        _accelSub = null;
      }
    } else {
      _slowSince = null;
    }
  }

  /// Posts the encrypted end-of-drive summary (only while sharing).
  void _onTripEnded(TripSummary trip) {
    if (_disposed) return; // fired from dispose()'s finish() — never notify after dispose
    final circleId = activeCircleId;
    if (circleId == null || !sharing) return;
    events.insert(0, (
      deviceId: deviceId,
      kind: 'trip',
      text:
          'drove ${(trip.distanceM / 1000).toStringAsFixed(1)} km'
          ' · max ${trip.maxSpeedKmh.round()} km/h'
          '${trip.speedingCount > 0 ? ' · ${trip.speedingCount} speeding' : ''}'
          '${trip.hardBrakingCount > 0 ? ' · ${trip.hardBrakingCount} hard brake' : ''}',
      ts: trip.endTs,
    ));
    if (events.length > 200) {
      events.removeRange(200, events.length);
    }
    unawaited(_postDataEnvelopeSafe(circleId, 'trip', trip.toData()));
    notifyListeners();
  }

  /// Confirmed crash: alert the circle even if location sharing is paused —
  /// this is an emergency, not routine tracking.
  void _onCrashConfirmed({
    required double lat,
    required double lng,
    required int ts,
  }) {
    if (_disposed) return;
    final circleId = activeCircleId;
    if (circleId == null) return;
    events.insert(0, (
      deviceId: deviceId,
      kind: 'crash',
      text: 'possible crash detected — sending alert to circle',
      ts: ts,
    ));
    if (events.length > 200) {
      events.removeRange(200, events.length);
    }
    // Crash alerts go through the emergency outbox too: if the first
    // attempt fails offline, it retries on reconnect (not silently lost).
    unawaited(_postEmergency(circleId, 'crash', ts, {'lat': lat, 'lng': lng}));
    notifyListeners();
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
      // Refresh the geofence engine when the place set changes (compare
      // ids, not just count — add+delete can keep the count identical).
      final engineIds = _geofence?.places.map((p) => p.id).toSet() ?? {};
      if (places.isNotEmpty &&
          (_geofence == null ||
              engineIds.length != places.length ||
              !engineIds.containsAll(places.keys))) {
        _geofence = GeofenceEngine(
          places: places.values.toList(),
          onEvent: _onGeofenceEvent,
        );
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
      try {
        final now = DateTime.now().millisecondsSinceEpoch;
        final sealed = await crypto.sealEnvelope(
          circleId: circleId,
          deviceId: deviceId,
          kind: 'geofence',
          ts: now,
          data: {
            'place_id': place.id,
            'place_name': place.name,
            'event': event,
          },
        );
        await api.postEnvelope(
          circleId: circleId,
          kind: 'geofence',
          ts: now,
          nonce: sealed.nonce,
          ciphertext: sealed.ciphertext,
        );
      } catch (_) {
        // Offline — the next crossing will re-announce.
      }
    }());
  }

  // -------------------------------------------------------------------------
  // actions
  // -------------------------------------------------------------------------

  Future<void> addPlace(
    String name,
    double lat,
    double lng,
    double radiusM,
  ) async {
    final circleId = activeCircleId;
    if (circleId == null) return;
    final id = _genId();
    final place = Place(
      id: id,
      name: name,
      lat: lat,
      lng: lng,
      radiusM: radiusM,
      ts: DateTime.now().millisecondsSinceEpoch,
    );
    places[id] = place;
    try {
      await _postDataEnvelope(circleId, 'place', place.toData());
    } catch (_) {
      // Offline: the place still applies locally; other members sync it
      // when it eventually posts.
    }
    _geofence = GeofenceEngine(
      places: places.values.toList(),
      onEvent: _onGeofenceEvent,
    );
    notifyListeners();
  }

  Future<void> deletePlace(String id) async {
    final circleId = activeCircleId;
    if (circleId == null) return;
    places.remove(id);
    try {
      await _postDataEnvelope(circleId, 'place_del', {'id': id});
    } catch (_) {}
    _geofence = GeofenceEngine(
      places: places.values.toList(),
      onEvent: _onGeofenceEvent,
    );
    notifyListeners();
  }

  Future<void> sendMessage(String text) async {
    final circleId = activeCircleId;
    if (circleId == null || text.trim().isEmpty) return;
    final now = DateTime.now().millisecondsSinceEpoch;
    final list = chatByCircle.putIfAbsent(circleId, () => []);
    list.add((deviceId: deviceId, text: text.trim(), ts: now));
    // Same bound as the receive path: an outgoing message backlog must not
    // grow the in-memory chat without limit.
    if (list.length > 500) {
      list.removeRange(0, list.length - 500);
    }
    try {
      await _postDataEnvelope(circleId, 'message', {'text': text.trim()});
    } catch (_) {
      // Offline: the message stays visible locally; retry on next send.
    }
    notifyListeners();
  }

  Future<void> checkIn({String note = ''}) async {
    final circleId = activeCircleId;
    if (circleId == null) return;
    final now = DateTime.now().millisecondsSinceEpoch;
    events.insert(0, (
      deviceId: deviceId,
      kind: 'checkin',
      text: note.isEmpty ? 'checked in' : note,
      ts: now,
    ));
    try {
      await _postDataEnvelope(circleId, 'checkin', {'text': note});
    } catch (_) {}
    notifyListeners();
  }

  /// Sends an SOS. Returns the honest delivery outcome:
  ///  * `'sent'`   — posted to the server
  ///  * `'queued'` — offline; kept and retried automatically until it
  ///    posts or goes stale (>10 min, matching the receivers' staleness
  ///    window)
  ///  * `'failed'` — could not even seal it (no circle key)
  Future<String> sendSos({String note = ''}) async {
    final circleId = activeCircleId;
    if (circleId == null) return 'failed';
    final now = DateTime.now().millisecondsSinceEpoch;
    final pos = positionsByDevice[deviceId];
    events.insert(0, (
      deviceId: deviceId,
      kind: 'sos',
      text: note.isEmpty ? 'SOS' : note,
      ts: now,
    ));
    if (events.length > 200) {
      events.removeRange(200, events.length);
    }
    final outcome = await _postEmergency(circleId, 'sos', now, {
      'text': note,
      if (pos != null) 'lat': pos.lat,
      if (pos != null) 'lng': pos.lng,
    });
    notifyListeners();
    return outcome;
  }

  /// Seals and posts an emergency envelope (SOS/crash). On network failure
  /// the sealed envelope is queued for automatic retry. Returns
  /// `'sent'` / `'queued'` / `'failed'`.
  Future<String> _postEmergency(
    String circleId,
    String kind,
    int ts,
    Map<String, dynamic> data,
  ) async {
    try {
      final sealed = await crypto.sealEnvelope(
        circleId: circleId,
        deviceId: deviceId,
        kind: kind,
        ts: ts,
        data: data,
      );
      _rememberSent(sealed.nonce);
      try {
        await api.postEnvelope(
          circleId: circleId,
          kind: kind,
          ts: ts,
          nonce: sealed.nonce,
          ciphertext: sealed.ciphertext,
        );
        return 'sent';
      } catch (_) {
        _enqueueEmergency(
          circleId,
          kind,
          ts,
          sealed.nonce,
          sealed.ciphertext,
        );
        return 'queued';
      }
    } catch (_) {
      return 'failed'; // no circle key or crypto failure
    }
  }

  void _enqueueEmergency(
    String circleId,
    String kind,
    int ts,
    String nonce,
    String ciphertext,
  ) {
    _emergencyOutbox.add((
      circleId: circleId,
      kind: kind,
      ts: ts,
      nonce: nonce,
      ciphertext: ciphertext,
    ));
    if (_emergencyOutbox.length > 5) {
      _emergencyOutbox.removeAt(0); // oldest stale items drop first anyway
    }
  }

  /// Retries queued emergencies whenever connectivity returns. Items past
  /// the receivers' staleness window are dropped — a receiver would ignore
  /// them anyway. Retries reuse the original ts+nonce, so if the first
  /// attempt actually landed but the response was lost, the server's
  /// dedup index makes the retry a no-op instead of a duplicate.
  Future<void> _flushEmergencyOutbox() async {
    if (_emergencyOutbox.isEmpty) return;
    final now = DateTime.now().millisecondsSinceEpoch;
    final kept = <({
      String circleId,
      String kind,
      int ts,
      String nonce,
      String ciphertext,
    })>[];
    for (final item in _emergencyOutbox) {
      if (now - item.ts > 10 * 60 * 1000) continue;
      try {
        await api.postEnvelope(
          circleId: item.circleId,
          kind: item.kind,
          ts: item.ts,
          nonce: item.nonce,
          ciphertext: item.ciphertext,
        );
      } catch (_) {
        kept.add(item);
      }
    }
    if (kept.length != _emergencyOutbox.length) {
      _emergencyOutbox
        ..clear()
        ..addAll(kept);
    }
  }

  Future<void> _postDataEnvelope(
    String circleId,
    String kind,
    Map<String, dynamic> data,
  ) async {
    final now = DateTime.now().millisecondsSinceEpoch;
    final sealed = await crypto.sealEnvelope(
      circleId: circleId,
      deviceId: deviceId,
      kind: kind,
      ts: now,
      data: data,
    );
    if (optimisticKinds.contains(kind)) _rememberSent(sealed.nonce);
    await api.postEnvelope(
      circleId: circleId,
      kind: kind,
      ts: now,
      nonce: sealed.nonce,
      ciphertext: sealed.ciphertext,
    );
  }

  /// Fire-and-forget variant for background paths (trip end): failures
  /// must never become unhandled async errors.
  Future<void> _postDataEnvelopeSafe(
    String circleId,
    String kind,
    Map<String, dynamic> data,
  ) async {
    try {
      await _postDataEnvelope(circleId, kind, data);
    } catch (_) {
      // Best-effort; the next one will retry.
    }
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
    _housekeepingTimer?.cancel();
    _accelSub?.cancel();
    _tracker?.stop();
    _trips?.finish();
    super.dispose();
  }
}
