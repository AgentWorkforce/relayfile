import { describe, expect, it, vi } from "vitest";
import { exportWorkspaceJson } from "./export.js";
import { queryFiles } from "./query.js";
import type { FileRow, StorageAdapter } from "./storage.js";
import { listTree } from "./tree.js";
import { filePermissionAllows, type TokenClaims } from "./acl.js";

function claimsWith(scopes: string[]): TokenClaims {
  return {
    workspaceId: "ws_test",
    agentName: "agent_test",
    scopes: new Set(scopes),
  };
}

function file(path: string, permissions: string[] = []): FileRow {
  return {
    path,
    revision: "rev_1",
    contentType: "text/markdown",
    content: "body",
    encoding: "utf-8",
    provider: "github",
    lastEditedAt: "2026-05-31T00:00:00.000Z",
    semantics: {
      permissions,
    },
  };
}

function storage(files: FileRow[]): StorageAdapter {
  const byPath = new Map(files.map((entry) => [entry.path, entry]));
  return {
    getFile: (path) => byPath.get(path) ?? null,
    listFiles: () => files,
    putFile: () => undefined,
    deleteFile: () => undefined,
    appendEvent: () => undefined,
    listEvents: () => ({ items: [], nextCursor: null }),
    getRecentEvents: () => [],
    getOperation: () => null,
    putOperation: () => undefined,
    listOperations: () => ({ items: [], nextCursor: null }),
    nextRevision: () => "rev_2",
    nextOperationId: () => "op_1",
    nextEventId: () => "evt_1",
    enqueueWriteback: () => undefined,
    getPendingWritebacks: () => [],
    getWorkspaceId: () => "ws_test",
  };
}

describe("ACL scope matching", () => {
  it("keeps exact scope matching as the default", () => {
    const claims = claimsWith(["relayfile:fs:read:/github/*"]);

    expect(
      filePermissionAllows(["scope:relayfile:fs:read:/github/LAYOUT.md"], "ws_test", claims),
    ).toBe(false);
    expect(
      filePermissionAllows(["scope:relayfile:fs:read:/github/*"], "ws_test", claims),
    ).toBe(true);
  });

  it("lets callers inject path-aware scope matching for tree, query, and export", () => {
    const claims = claimsWith(["relayfile:fs:read:/github/*"]);
    const rows = [
      file("/.relayfile.acl", ["scope:relayfile:fs:read:/github/LAYOUT.md"]),
      file("/github/LAYOUT.md"),
    ];
    const repo = storage(rows);
    const scopeMatches = vi.fn(
      (scope: string, tokenClaims: TokenClaims | null, context) =>
        scope === "relayfile:fs:read:/github/LAYOUT.md" &&
        tokenClaims?.scopes.has("relayfile:fs:read:/github/*") === true &&
        context.action === "read" &&
        context.requestedPath === "/github/LAYOUT.md",
    );

    expect(listTree(repo, { path: "/github", depth: 1 }, claims).entries).toEqual(
      [],
    );

    const aclOptions = { scopeMatches };
    expect(
      listTree(repo, { path: "/github", depth: 1 }, claims, aclOptions).entries.map(
        (entry) => entry.path,
      ),
    ).toEqual(["/github/LAYOUT.md"]);
    expect(
      queryFiles(repo, { path: "/github" }, claims, aclOptions).items.map(
        (entry) => entry.path,
      ),
    ).toEqual(["/github/LAYOUT.md"]);
    expect(exportWorkspaceJson(repo, claims, aclOptions).map((entry) => entry.path)).toEqual([
      "/github/LAYOUT.md",
    ]);
    expect(scopeMatches).toHaveBeenCalled();
  });
});
