import 'package:sqflite/sqflite.dart';

import '../api/models.dart';

/// One queued emergency envelope: already sealed, waiting for the network.
/// [nonce] doubles as the id — retries reuse it so the server's dedup index
/// turns a retry-after-lost-response into a no-op instead of a duplicate.
class OutboxItem {
  const OutboxItem({
    required this.circleId,
    required this.kind,
    required this.ts,
    required this.nonce,
    required this.ciphertext,
    this.attempts = 0,
  });

  final String circleId;
  final String kind;
  final int ts;
  final String nonce;
  final String ciphertext;
  final int attempts;

  OutboxItem withAttempts(int n) =>
      OutboxItem(circleId: circleId, kind: kind, ts: ts, nonce: nonce,
          ciphertext: ciphertext, attempts: n);
}

/// On-device cache of relayed envelopes (ciphertext only — we never persist
/// plaintext locations), plus circle membership metadata for offline use.
class LocalDb {
  LocalDb._(this._db);

  final Database _db;

  static LocalDb? _instance;

  /// Local envelope cache window: matches the server default so the phone
  /// never grows without bound (ciphertext, but it is still disk).
  static const _retentionDays = 90;
  static const _maxPerCircle = 20000;

  /// [name] exists for tests: two test isolates sharing one file hit
  /// SQLITE_BUSY. Production always uses the default.
  static Future<LocalDb> open({String name = 'lodestar.db'}) async {
    if (_instance != null) return _instance!;
    final dir = await getDatabasesPath();
    final db = await openDatabase(
      '$dir/$name',
      version: 2,
      onCreate: (db, _) async {
        await _createOutbox(db);
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
        await db.execute(
          'CREATE INDEX idx_env_circle_ts ON envelopes(circle_id, ts)',
        );
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
            sharing_enabled INTEGER NOT NULL DEFAULT 1,          PRIMARY KEY (circle_id, device_id)
        )''');
      },
      onUpgrade: (db, oldVersion, _) async {
        // v2: emergency outbox — SOS/crash envelopes must survive an app kill.
        if (oldVersion < 2) await _createOutbox(db);
      },
    );
    _instance = LocalDb._(db);
    await _instance!.prune();
    return _instance!;
  }

  static Future<void> _createOutbox(Database db) async {
    await db.execute('''
      CREATE TABLE outbox (
        nonce TEXT PRIMARY KEY,
        circle_id TEXT NOT NULL,
        kind TEXT NOT NULL,
        ts INTEGER NOT NULL,
        ciphertext TEXT NOT NULL,
        attempts INTEGER NOT NULL DEFAULT 0,
        created_at INTEGER NOT NULL
      )''');
  }

  // -----------------------------------------------------------------------
  // Emergency outbox storage (policy lives in EmergencyOutbox).
  // -----------------------------------------------------------------------

  Future<void> putOutboxItem(OutboxItem item) async {
    await _db.insert(
      'outbox',
      {
        'nonce': item.nonce,
        'circle_id': item.circleId,
        'kind': item.kind,
        'ts': item.ts,
        'ciphertext': item.ciphertext,
        'attempts': item.attempts,
        'created_at': DateTime.now().millisecondsSinceEpoch,
      },
      conflictAlgorithm: ConflictAlgorithm.replace,
    );
  }

  /// All queued items, oldest first. An outbox is tiny (≤ 5) so a full scan
  /// is always the right query.
  Future<List<OutboxItem>> outboxItems() async {
    final rows = await _db.query('outbox', orderBy: 'created_at ASC, ts ASC');
    return rows
        .map(
          (r) => OutboxItem(
            circleId: r['circle_id'] as String,
            kind: r['kind'] as String,
            ts: r['ts'] as int,
            nonce: r['nonce'] as String,
            ciphertext: r['ciphertext'] as String,
            attempts: r['attempts'] as int,
          ),
        )
        .toList();
  }

  Future<void> deleteOutboxItem(String nonce) async {
    await _db.delete('outbox', where: 'nonce = ?', whereArgs: [nonce]);
  }

  /// Bounds the cache: drops envelopes older than 90 days and, per circle,
  /// everything beyond the newest 20k. Cheap at family scale; run at open
  /// and daily from housekeeping.
  Future<void> prune() async {
    final cutoff =
        DateTime.now()
            .subtract(const Duration(days: _retentionDays))
            .millisecondsSinceEpoch;
    await _db.delete('envelopes', where: 'ts < ?', whereArgs: [cutoff]);
    final circles = await _db.query(
      'envelopes',
      columns: ['circle_id'],
      distinct: true,
    );
    for (final row in circles) {
      final id = row['circle_id'] as String;
      await _db.rawDelete(
        'DELETE FROM envelopes WHERE circle_id = ? AND id NOT IN '
        '(SELECT id FROM envelopes WHERE circle_id = ? '
        'ORDER BY ts DESC LIMIT $_maxPerCircle)',
        [id, id],
      );
    }
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

  Future<List<Envelope>> cachedEnvelopes(
    String circleId, {
    String? kind,
    int limit = 500,
    bool ascending = false,
  }) async {
    // Always fetch the NEWEST [limit] (DESC + LIMIT), then reverse in
    // memory when the caller wants chronological order — ORDER BY ts ASC
    // LIMIT n would return the n OLDEST rows instead.
    final rows = await _db.query(
      'envelopes',
      where: kind == null ? 'circle_id = ?' : 'circle_id = ? AND kind = ?',
      whereArgs: kind == null ? [circleId] : [circleId, kind],
      orderBy: 'ts DESC',
      limit: limit,
    );
    final envelopes = rows
        .map(
          (r) => Envelope(
            id: r['id'] as String,
            circleId: r['circle_id'] as String,
            deviceId: r['device_id'] as String,
            kind: r['kind'] as String,
            ts: r['ts'] as int,
            nonce: r['nonce'] as String,
            ciphertext: r['ciphertext'] as String,
          ),
        )
        .toList();
    return ascending ? envelopes.reversed.toList() : envelopes;
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

  Future<void> upsertMembers(
    String circleId,
    List<CircleMember> members,
  ) async {
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
    final rows = await _db.query(
      'members',
      where: 'circle_id = ?',
      whereArgs: [circleId],
    );
    return rows
        .map(
          (r) => CircleMember(
            circleId: circleId,
            deviceId: r['device_id'] as String,
            role: r['role'] as String,
            displayName: r['display_name'] as String,
            avatarColor: r['avatar_color'] as String,
            ed25519Pub: r['ed25519_pub'] as String,
            x25519Pub: r['x25519_pub'] as String,
            sharingEnabled: (r['sharing_enabled'] as int) == 1,
            joinedAt: 0,
          ),
        )
        .toList();
  }
}
