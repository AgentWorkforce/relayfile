export interface ForkOptions {
  proposalId: string;
  /** Optional time-to-live in seconds. The server default is 604800 (7 days). */
  ttlSeconds?: number;
}

export interface ForkHandle {
  forkId: string;
  proposalId: string;
  workspaceId: string;
  /** Expiration timestamp in ISO 8601 format. */
  expiresAt: string;
}
