import { DatabaseSync } from "node:sqlite";
import type { ActorSql, SqlValue } from "../src/runtime/db.js";

/**
 * node:sqlite stand-in for the durable-actors ActorDatabase with the runtime's
 * contract: one statement per exec, autocommit outside transactions, nested
 * transactions on savepoints, and transaction statements rejected.
 */
export class FakeActorDb implements ActorSql {
  readonly db: DatabaseSync;
  private depth = 0;
  private savepoint = 0;

  constructor(path = ":memory:") {
    this.db = new DatabaseSync(path);
  }

  exec<Row extends object = Record<string, SqlValue>>(sql: string, ...bindings: SqlValue[]): Row[] {
    if (/^\s*(BEGIN|COMMIT|END|ROLLBACK|SAVEPOINT|RELEASE|ATTACH|DETACH|VACUUM)\b/i.test(sql)) {
      throw new Error(`statement not allowed in actors: ${sql.split(/\s+/)[0]}`);
    }
    const statement = this.db.prepare(sql);
    const params = bindings as Array<string | number | bigint | null | Uint8Array>;
    if (/^\s*(SELECT|WITH|PRAGMA)\b/i.test(sql) || /\bRETURNING\b/i.test(sql)) {
      return statement.all(...params) as Row[];
    }
    statement.run(...params);
    return [];
  }

  async transaction<T>(operation: () => Promise<T>): Promise<T> {
    const name = `sp_${++this.savepoint}`;
    this.db.exec(`SAVEPOINT ${name}`);
    this.depth += 1;
    try {
      const result = await operation();
      this.db.exec(`RELEASE ${name}`);
      return result;
    } catch (error) {
      this.db.exec(`ROLLBACK TO ${name}`);
      this.db.exec(`RELEASE ${name}`);
      throw error;
    } finally {
      this.depth -= 1;
    }
  }
}
