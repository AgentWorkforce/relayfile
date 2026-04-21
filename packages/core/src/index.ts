/**
 * @relayfile/core — shared business logic for relayfile
 *
 * Pure functions. No I/O. No runtime dependencies.
 * Storage is injected via the StorageAdapter interface.
 */

// Storage interface (implementors provide this)
export type {
  StorageAdapter,
  FileRow,
  FileSemantics,
  EventRow,
  OperationRow,
  WritebackItem,
  EnvelopeRow,
  PaginationOptions,
  Paginated,
  EnvelopeQueryOptions,
} from "./storage.js";

// Business logic modules (agent-3 will extract these from workspace.ts)
export * from "./files.js";
export * from "./acl.js";
export * from "./semantics.js";
export * from "./query.js";
export * from "./tree.js";
export * from "./events.js";
export * from "./operations.js";
export * from "./writeback.js";
export * from "./webhooks.js";
export * from "./export.js";
export * from "./dedup.js";
export * from "./forks.js";
