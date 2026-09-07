import 'package:sqflite/sqflite.dart';

import '../api/models.dart';

/// On-device cache of relayed envelopes (ciphertext only — we never persist
/// plaintext locations), plus circle membership metadata for offline use.
class LocalDb {
  LocalDb._(this._db);

  final Database _db;

  static LocalDb? _instance;

  static Future<LocalDb> open() async {
    if (_instance != null) return _instance!;
    final dir = await getDatabasesPath();
    final db = await openDatabase(
      '$dir/lodestar.db',
      version: 1,
      onCreate: (db, _) async {
        await db.execute('''
          CREATE TABLE envelopes (
            id TEXT PRIMARY KEY,
            circle_id TEXT NOT NULL,
            device_id TEXT NOT NULL,
            kind TEXT NOT NULL,
            ts INTEGER NOT NULL,
            nonce TEXT NOT NULL,
            ciphertext TEXT NOT NULL,
            created_at INTEGER NOT NULL
          )''');
        await db.execute('CREATE INDEX idx_env_circle_ts ON envelopes(circle_id, ts)');
        await db.execute('''
          CREATE TABLE circles (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            color TEXT NOT NULL,
            owner_device_id TEXT NOT NULL,
            invite_code TEXT NOT NULL
          )''');
        await db.execute('''
          CREATE TABLE members (
            circle_id TEXT NOT NULL,
            device_id TEXT NOT NULL,
            role TEXT NOT NULL,
            display_name TEXT NOT NULL,
            avatar_color TEXT NOT NULL,
            ed25519_pub TEXT NOT NULL,
            x25519_pub TEXT NOT NULL,
            sharing_enabled INTEGER NOT NULL DEFAULT 1,
            PRIMARY KEY (circle_id, device_id)
          )''');
      },
    );
    _instance = LocalDb._(db);
    return _instance!;
  }

  Future<void> upsertEnvelope(Envelope e) async {
    await _db.insert('envelopes', {
      'id': e.id,
      'circle_id': e.circleId,
      'device_id': e.deviceId,
      'kind': e.kind,
      'ts': e.ts,
      'nonce': e.nonce,
      'ciphertext': e.ciphertext,
      'created_at': e.ts,
    }, conflictAlgorithm: ConflictAlgorithm.replace);
  }

  Future<List<Envelope>> cachedEnvelopes(String circleId, {String? kind, int limit = 500}) async {
    final rows = await _db.query('envelopes',
        where: kind == null ? 'circle_id = ?' : 'circle_id = ? AND kind = ?',
        whereArgs: kind == null ? [circleId] : [circleId, kind],
        orderBy: 'ts DESC',
        limit: limit);
    return rows.map((r) => Envelope(
          id: r['id'] as String,
          circleId: r['circle_id'] as String,
          deviceId: r['device_id'] as String,
          kind: r['kind'] as String,
          ts: r['ts'] as int,
          nonce: r['nonce'] as String,
          ciphertext: r['ciphertext'] as String,
        )).toList();
  }

  Future<void> upsertCircle(Circle c) async {
    await _db.insert('circles', {
      'id': c.id,
      'name': c.name,
      'color': c.color,
      'owner_device_id': c.ownerDeviceId,
      'invite_code': c.inviteCode,
    }, conflictAlgorithm: ConflictAlgorithm.replace);
  }

  Future<void> upsertMembers(String circleId, List<CircleMember> members) async {
    await _db.delete('members', where: 'circle_id = ?', whereArgs: [circleId]);
    final batch = _db.batch();
    for (final m in members) {
      batch.insert('members', {
        'circle_id': circleId,
        'device_id': m.deviceId,
        'role': m.role,
        'display_name': m.displayName,
        'avatar_color': m.avatarColor,
        'ed25519_pub': m.ed25519Pub,
        'x25519_pub': m.x25519Pub,
        'sharing_enabled': m.sharingEnabled ? 1 : 0,
      });
    }
    await batch.commit(noResult: true);
  }

  Future<List<CircleMember>> cachedMembers(String circleId) async {
    final rows = await _db.query('members', where: 'circle_id = ?', whereArgs: [circleId]);
    return rows.map((r) => CircleMember(
          circleId: circleId,
          deviceId: r['device_id'] as String,
          role: r['role'] as String,
          displayName: r['display_name'] as String,
          avatarColor: r['avatar_color'] as String,
          ed25519Pub: r['ed25519_pub'] as String,
          x25519Pub: r['x25519_pub'] as String,
          sharingEnabled: (r['sharing_enabled'] as int) == 1,
          joinedAt: 0,
        )).toList();
  }
}