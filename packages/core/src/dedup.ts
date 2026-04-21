import { createHash } from "node:crypto";

export interface ContentIdentity {
  kind: string;
  key: string;
}

export interface DedupEntry {
  contentIdentity?: ContentIdentity;
  opId?: string;
  path?: string;
  revision?: string;
  correlationId?: string;
  createdAt?: string;
  metadata?: Record<string, string>;
}

export interface DedupStore {
  has(key: string): Promise<DedupEntry | null>;
  put(key: string, entry: DedupEntry, ttlSeconds: number): Promise<void>;
}

export function computeDedupHash(workspaceId: string, kind: string, key: string): string {
  return createHash("sha256")
    .update(workspaceId)
    .update("\0")
    .update(kind)
    .update("\0")
    .update(key)
    .digest("hex");
}
