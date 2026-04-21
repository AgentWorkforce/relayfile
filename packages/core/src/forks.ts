import type { FileSemantics } from "./storage.js";
import type { WriteFileRequest } from "./files.js";
import { normalizePath } from "./files.js";

export const DEFAULT_FORK_TTL_SECONDS = 7 * 24 * 60 * 60;
export const MAX_FORK_TTL_SECONDS = 30 * 24 * 60 * 60;

export interface ForkOptions {
  workspaceId: string;
  proposalId: string;
  ttlSeconds?: number;
}

export interface ForkHandle {
  forkId: string;
  proposalId: string;
  workspaceId: string;
  expiresAt: string;
  parentRevision: string;
}

export interface ForkWriteFileRequest extends Omit<WriteFileRequest, "ifMatch"> {
  contentType?: string;
  content: string;
  encoding?: string;
  semantics?: FileSemantics;
}

export type ForkOverlayEntry =
  | {
      type: "write";
      file: ForkWriteFileRequest;
      revision: string;
    }
  | {
      type: "delete";
      deletedAt: string;
    };

export interface ForkState extends ForkHandle {
  createdAt: string;
  overlay: Record<string, ForkOverlayEntry>;
  overlayRevisionCounter: number;
}

export interface CommitForkResponse {
  revision: string;
  writtenCount: number;
  deletedCount: number;
}

export interface ParentMovedErrorPayload {
  error: "parent_moved";
  currentRevision: string;
}

export interface ForkExpiredErrorPayload {
  error: "fork_expired";
}

export function normalizeForkTTLSeconds(ttlSeconds?: number): number {
  if (ttlSeconds === undefined) {
    return DEFAULT_FORK_TTL_SECONDS;
  }
  if (!Number.isFinite(ttlSeconds) || ttlSeconds <= 0) {
    return DEFAULT_FORK_TTL_SECONDS;
  }
  return Math.min(Math.floor(ttlSeconds), MAX_FORK_TTL_SECONDS);
}

export function computeForkExpiresAt(createdAt: string | Date, ttlSeconds?: number): string {
  const created = typeof createdAt === "string" ? new Date(createdAt) : createdAt;
  const ttlMs = normalizeForkTTLSeconds(ttlSeconds) * 1000;
  return new Date(created.getTime() + ttlMs).toISOString();
}

export function isForkExpired(fork: Pick<ForkState, "expiresAt">, now: string | Date = new Date()): boolean {
  const nowMs = typeof now === "string" ? Date.parse(now) : now.getTime();
  return Number.isFinite(nowMs) && nowMs >= Date.parse(fork.expiresAt);
}

export function nextForkOverlayRevision(forkId: string, nextCounter: number): string {
  return `fork:${forkId}:${nextCounter}`;
}

export function createForkState(input: {
  forkId: string;
  workspaceId: string;
  proposalId: string;
  parentRevision: string;
  createdAt?: string | Date;
  ttlSeconds?: number;
}): ForkState {
  const createdAt = input.createdAt instanceof Date
    ? input.createdAt.toISOString()
    : input.createdAt ?? new Date().toISOString();
  return {
    forkId: input.forkId,
    workspaceId: input.workspaceId,
    proposalId: input.proposalId,
    parentRevision: input.parentRevision,
    createdAt,
    expiresAt: computeForkExpiresAt(createdAt, input.ttlSeconds),
    overlay: {},
    overlayRevisionCounter: 0,
  };
}

export function writeForkOverlay(
  fork: ForkState,
  path: string,
  file: ForkWriteFileRequest,
): ForkOverlayEntry {
  const nextCounter = fork.overlayRevisionCounter + 1;
  const revision = nextForkOverlayRevision(fork.forkId, nextCounter);
  const entry: ForkOverlayEntry = {
    type: "write",
    file: {
      ...file,
      path: normalizePath(path),
    },
    revision,
  };
  fork.overlayRevisionCounter = nextCounter;
  fork.overlay[normalizePath(path)] = entry;
  return entry;
}

export function deleteForkOverlay(
  fork: ForkState,
  path: string,
  deletedAt: string | Date = new Date(),
): ForkOverlayEntry {
  const entry: ForkOverlayEntry = {
    type: "delete",
    deletedAt: deletedAt instanceof Date ? deletedAt.toISOString() : deletedAt,
  };
  fork.overlay[normalizePath(path)] = entry;
  return entry;
}
