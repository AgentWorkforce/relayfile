/**
 * The slice of the durable-actors `ActorDatabase` the workspace runtime uses.
 *
 * Keeping this structural (instead of importing `durable-actors`) lets the
 * runtime run unchanged inside the actor and under Node tests, where
 * `test/fake-actor-db.ts` backs it with `node:sqlite`.
 *
 * Contract (durable-actors 0.14.6):
 * - `exec` runs exactly one statement and returns rows keyed by column name.
 * - Statements autocommit unless they run inside `transaction`, which nests
 *   (runtime savepoints) and rolls back its SQL when the callback throws.
 * - BEGIN/COMMIT/SAVEPOINT/ATTACH/VACUUM are rejected; names starting with
 *   `__terse_` or `_litestream_` are reserved.
 */
export type SqlValue = string | number | bigint | Uint8Array | null;

export interface ActorSql {
  exec<Row extends object = Record<string, SqlValue>>(sql: string, ...bindings: SqlValue[]): Row[];
  transaction<T>(operation: () => Promise<T>): Promise<T>;
}

/** Read one integer-ish column, tolerating bigint results. */
export function num(value: unknown, fallback = 0): number {
  if (typeof value === "number") return value;
  if (typeof value === "bigint") return Number(value);
  if (typeof value === "string" && value.trim() !== "") {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : fallback;
  }
  return fallback;
}

export function str(value: unknown, fallback = ""): string {
  if (value === null || value === undefined) return fallback;
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "bigint") return String(value);
  if (value instanceof Uint8Array) return new TextDecoder().decode(value);
  return fallback;
}

export function nullableStr(value: unknown): string | null {
  if (value === null || value === undefined) return null;
  return str(value);
}
