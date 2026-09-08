/// Wire models matching the lodestard API (server/internal/api).
library;

class Device {
  final String id;
  final String name;
  final String ed25519Pub;
  final String x25519Pub;
  final int createdAt;

  Device({
    required this.id,
    required this.name,
    required this.ed25519Pub,
    required this.x25519Pub,
    required this.createdAt,
  });

  factory Device.fromJson(Map<String, dynamic> j) => Device(
    id: j['id'] as String,
    name: j['name'] as String? ?? '',
    ed25519Pub: j['ed25519_pub'] as String? ?? '',
    x25519Pub: j['x25519_pub'] as String? ?? '',
    createdAt: (j['created_at'] as num?)?.toInt() ?? 0,
  );
}

class Circle {
  final String id;
  final String name;
  final String color;
  final String ownerDeviceId;
  final String inviteCode;
  final int createdAt;

  Circle({
    required this.id,
    required this.name,
    required this.color,
    required this.ownerDeviceId,
    required this.inviteCode,
    required this.createdAt,
  });

  factory Circle.fromJson(Map<String, dynamic> j) => Circle(
    id: j['id'] as String,
    name: j['name'] as String? ?? 'Family',
    color: j['color'] as String? ?? '#4f7cff',
    ownerDeviceId: j['owner_device_id'] as String? ?? '',
    inviteCode: j['invite_code'] as String? ?? '',
    createdAt: (j['created_at'] as num?)?.toInt() ?? 0,
  );
}

class CircleMember {
  final String circleId;
  final String deviceId;
  final String role;
  final String displayName;
  final String avatarColor;
  final String ed25519Pub;
  final String x25519Pub;
  final bool sharingEnabled;
  final int joinedAt;

  CircleMember({
    required this.circleId,
    required this.deviceId,
    required this.role,
    required this.displayName,
    required this.avatarColor,
    required this.ed25519Pub,
    required this.x25519Pub,
    required this.sharingEnabled,
    required this.joinedAt,
  });

  factory CircleMember.fromJson(Map<String, dynamic> j) => CircleMember(
    circleId: j['circle_id'] as String? ?? '',
    deviceId: j['device_id'] as String,
    role: j['role'] as String? ?? 'member',
    displayName: j['display_name'] as String? ?? 'Member',
    avatarColor: j['avatar_color'] as String? ?? '#4f7cff',
    ed25519Pub: j['ed25519_pub'] as String? ?? '',
    x25519Pub: j['x25519_pub'] as String? ?? '',
    sharingEnabled: j['sharing_enabled'] as bool? ?? true,
    joinedAt: (j['joined_at'] as num?)?.toInt() ?? 0,
  );
}

/// A relayed envelope. [nonce] and [ciphertext] are opaque to the server.
class Envelope {
  final String id;
  final String circleId;
  final String deviceId;
  final String kind;
  final int ts;
  final String nonce;
  final String ciphertext;

  Envelope({
    required this.id,
    required this.circleId,
    required this.deviceId,
    required this.kind,
    required this.ts,
    required this.nonce,
    required this.ciphertext,
  });

  factory Envelope.fromJson(Map<String, dynamic> j) => Envelope(
    id: j['id'] as String? ?? '',
    circleId: j['circle_id'] as String? ?? '',
    deviceId: j['device_id'] as String? ?? '',
    kind: j['kind'] as String? ?? '',
    ts: (j['ts'] as num?)?.toInt() ?? 0,
    nonce: j['nonce'] as String? ?? '',
    ciphertext: j['ciphertext'] as String? ?? '',
  );
}

/// A decrypted location payload.
class Position {
  final double lat;
  final double lng;
  final double accuracy;
  final double speed;
  final int ts;

  Position({
    required this.lat,
    required this.lng,
    required this.accuracy,
    required this.speed,
    required this.ts,
  });

  factory Position.fromData(Map<String, dynamic> d, {required int ts}) =>
      Position(
        lat: (d['lat'] as num).toDouble(),
        lng: (d['lng'] as num).toDouble(),
        accuracy: (d['acc'] as num?)?.toDouble() ?? 0,
        speed: (d['speed'] as num?)?.toDouble() ?? 0,
        ts: (d['ts'] as num?)?.toInt() ?? ts,
      );

  Map<String, dynamic> toData() => {
    'lat': lat,
    'lng': lng,
    'acc': accuracy,
    'speed': speed,
    'ts': ts,
  };
}

/// A decrypted geofence place.
class Place {
  final String id;
  final String name;
  final double lat;
  final double lng;
  final double radiusM;
  final int ts;

  Place({
    required this.id,
    required this.name,
    required this.lat,
    required this.lng,
    required this.radiusM,
    required this.ts,
  });

  factory Place.fromData(Map<String, dynamic> d) => Place(
    id: d['id'] as String? ?? '',
    name: d['name'] as String? ?? 'Place',
    lat: (d['lat'] as num).toDouble(),
    lng: (d['lng'] as num).toDouble(),
    radiusM: (d['radius_m'] as num?)?.toDouble() ?? 100,
    ts: (d['ts'] as num?)?.toInt() ?? 0,
  );

  Map<String, dynamic> toData() => {
    'id': id,
    'name': name,
    'lat': lat,
    'lng': lng,
    'radius_m': radiusM,
    'ts': ts,
  };
}
