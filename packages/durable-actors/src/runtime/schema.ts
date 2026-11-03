import type { ActorSql } from "./db.js";
import { num } from "./db.js";

/**
 * Forward-only, idempotent schema migrations for a workspace actor's SQLite.
 * Each entry runs once, in order, and is recorded in `rf_migrations`.
 * Never edit or reorder a shipped migration; append a new one.
 */
const MIGRATIONS: ReadonlyArray<{ id: number; statements: readonly string[] }> = [
  {
    id: 1,
    statements: [
      `CREATE TABLE IF NOT EXISTS rf_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
      `CREATE TABLE IF NOT EXISTS rf_counters (name TEXT PRIMARY KEY, value INTEGER NOT NULL)`,
      `CREATE TABLE IF NOT EXISTS rf_files (
        path TEXT PRIMARY KEY,
        revision TEXT NOT NULL,
        content_type TEXT NOT NULL,
        content TEXT,
        blob_sha256 TEXT,
        size INTEGER NOT NULL DEFAULT 0,
        encoding TEXT NOT NULL DEFAULT 'utf-8',
        provider TEXT NOT NULL DEFAULT '',
        provider_object_id TEXT NOT NULL DEFAULT '',
        node_type TEXT NOT NULL DEFAULT 'file',
        target TEXT NOT NULL DEFAULT '',
        mode INTEGER NOT NULL DEFAULT 0,
        content_hash TEXT NOT NULL DEFAULT '',
        last_edited_at TEXT NOT NULL,
        semantics TEXT NOT NULL DEFAULT '{}'
      )`,
      `CREATE TABLE IF NOT EXISTS rf_events (
        seq INTEGER PRIMARY KEY AUTOINCREMENT,
        event_id TEXT NOT NULL UNIQUE,
        type TEXT NOT NULL,
        path TEXT NOT NULL,
        revision TEXT NOT NULL DEFAULT '',
        content_hash TEXT NOT NULL DEFAULT '',
        origin TEXT NOT NULL DEFAULT '',
        provider TEXT NOT NULL DEFAULT '',
        correlation_id TEXT NOT NULL DEFAULT '',
        timestamp TEXT NOT NULL,
        type_metadata TEXT
      )`,
      `CREATE INDEX IF NOT EXISTS rf_events_provider ON rf_events (provider, seq)`,
      `CREATE TABLE IF NOT EXISTS rf_operations (
        seq INTEGER PRIMARY KEY AUTOINCREMENT,
        op_id TEXT NOT NULL UNIQUE,
        path TEXT NOT NULL,
        revision TEXT NOT NULL,
        action TEXT NOT NULL,
        provider TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL,
        attempt_count INTEGER NOT NULL DEFAULT 0,
        last_error TEXT,
        next_attempt_at TEXT,
        correlation_id TEXT NOT NULL DEFAULT '',
        provider_result TEXT,
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL,
        completed_at TEXT
      )`,
      `CREATE INDEX IF NOT EXISTS rf_operations_status ON rf_operations (status, seq)`,
      `CREATE TABLE IF NOT EXISTS rf_envelopes (
        seq INTEGER PRIMARY KEY AUTOINCREMENT,
        envelope_id TEXT NOT NULL UNIQUE,
        provider TEXT NOT NULL,
        delivery_id TEXT NOT NULL,
        received_at TEXT NOT NULL,
        headers TEXT NOT NULL DEFAULT '{}',
        payload TEXT NOT NULL DEFAULT '{}',
        correlation_id TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL,
        attempt_count INTEGER NOT NULL DEFAULT 0,
        last_error TEXT
      )`,
      `CREATE TABLE IF NOT EXISTS rf_envelope_aliases (
        provider TEXT NOT NULL,
        delivery_id TEXT NOT NULL,
        envelope_id TEXT NOT NULL,
        PRIMARY KEY (provider, delivery_id)
      )`,
      `CREATE TABLE IF NOT EXISTS rf_dedup (
        kind TEXT NOT NULL,
        key TEXT NOT NULL,
        op_id TEXT NOT NULL,
        result TEXT NOT NULL,
        expires_at INTEGER NOT NULL,
        PRIMARY KEY (kind, key)
      )`,
    ],
  },
  {
    // Effective file permissions captured when a file is deleted, so the
    // file.deleted event stays hidden from readers the file denied (Go:
    // Event.ACLPermissions). NULL on a file.deleted event = fail closed.
    id: 2,
    statements: [`ALTER TABLE rf_events ADD COLUMN acl_permissions TEXT`],
  },
];

export const SCHEMA_VERSION = MIGRATIONS[MIGRATIONS.length - 1]!.id;

/** Apply pending migrations. Cheap when current: one SELECT. */
export function migrate(sql: ActorSql): void {
  sql.exec(`CREATE TABLE IF NOT EXISTS rf_migrations (id INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`);
  const rows = sql.exec<{ id: unknown }>(`SELECT MAX(id) AS id FROM rf_migrations`);
  const current = num(rows[0]?.id, 0);
  for (const migration of MIGRATIONS) {
    if (migration.id <= current) continue;
    for (const statement of migration.statements) sql.exec(statement);
    sql.exec(`INSERT OR IGNORE INTO rf_migrations (id, applied_at) VALUES (?, ?)`, migration.id, new Date().toISOString());
  }
}
