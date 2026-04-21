import { describe, expect, it } from "vitest";

import {
  DEFAULT_FORK_TTL_SECONDS,
  MAX_FORK_TTL_SECONDS,
  computeForkExpiresAt,
  createForkState,
  deleteForkOverlay,
  isForkExpired,
  nextForkOverlayRevision,
  normalizeForkTTLSeconds,
  writeForkOverlay,
} from "./forks.js";

describe("fork helpers", () => {
  it("normalizes ttl seconds with the documented default and max", () => {
    expect(normalizeForkTTLSeconds()).toBe(DEFAULT_FORK_TTL_SECONDS);
    expect(normalizeForkTTLSeconds(0)).toBe(DEFAULT_FORK_TTL_SECONDS);
    expect(normalizeForkTTLSeconds(MAX_FORK_TTL_SECONDS + 1)).toBe(MAX_FORK_TTL_SECONDS);
  });

  it("computes expiresAt from creation time", () => {
    expect(computeForkExpiresAt("2026-04-21T00:00:00.000Z", 60)).toBe(
      "2026-04-21T00:01:00.000Z",
    );
  });

  it("tracks overlay revisions without colliding with parent revisions", () => {
    const fork = createForkState({
      forkId: "fork_123",
      workspaceId: "ws_1",
      proposalId: "prop_1",
      parentRevision: "rev_1",
      createdAt: "2026-04-21T00:00:00.000Z",
    });

    const first = writeForkOverlay(fork, "docs/a.md", {
      path: "docs/a.md",
      content: "# a",
    });
    const second = writeForkOverlay(fork, "/docs/b.md", {
      path: "/docs/b.md",
      content: "# b",
    });

    expect(first).toMatchObject({ type: "write", revision: "fork:fork_123:1" });
    expect(second).toMatchObject({ type: "write", revision: "fork:fork_123:2" });
    expect(nextForkOverlayRevision("fork_123", 3)).toBe("fork:fork_123:3");
    expect(Object.keys(fork.overlay)).toEqual(["/docs/a.md", "/docs/b.md"]);
  });

  it("stores delete tombstones by normalized path", () => {
    const fork = createForkState({
      forkId: "fork_123",
      workspaceId: "ws_1",
      proposalId: "prop_1",
      parentRevision: "rev_1",
    });

    deleteForkOverlay(fork, "docs/a.md", "2026-04-21T00:00:00.000Z");

    expect(fork.overlay["/docs/a.md"]).toEqual({
      type: "delete",
      deletedAt: "2026-04-21T00:00:00.000Z",
    });
  });

  it("detects expired forks", () => {
    const fork = createForkState({
      forkId: "fork_123",
      workspaceId: "ws_1",
      proposalId: "prop_1",
      parentRevision: "rev_1",
      createdAt: "2026-04-21T00:00:00.000Z",
      ttlSeconds: 60,
    });

    expect(isForkExpired(fork, "2026-04-21T00:00:59.000Z")).toBe(false);
    expect(isForkExpired(fork, "2026-04-21T00:01:00.000Z")).toBe(true);
  });
});
