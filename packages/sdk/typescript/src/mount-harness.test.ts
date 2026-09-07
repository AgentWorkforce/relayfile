import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  MountHarnessPermissionError,
  startMountHarness,
} from "./mount-harness.js";

const mockHelpersModule = "../scripts/agent-workspace-mocks.mjs";

describe("relayfile mount harness", () => {
  let tempRoot: string;
  let localDir: string;
  let helpers: Awaited<typeof loadHelpers>;
  let cloud: Awaited<ReturnType<Awaited<typeof loadHelpers>["createMockCloudServer"]>>;
  let relay: Awaited<ReturnType<Awaited<typeof loadHelpers>["createMockRelayfileServer"]>>;
  let relayBaseUrl: string;

  beforeEach(async () => {
    tempRoot = await mkdtemp(path.join(os.tmpdir(), "relayfile-mount-harness-test-"));
    localDir = path.join(tempRoot, "local");
    helpers = await loadHelpers();
    relay = helpers.createMockRelayfileServer();
    const relayBaseUrlRef = { current: "" };
    cloud = helpers.createMockCloudServer({
      relayfileBaseUrl: () => relayBaseUrlRef.current,
    });
    relayBaseUrl = await helpers.listenServer(relay);
    relayBaseUrlRef.current = relayBaseUrl;
    await helpers.listenServer(cloud.server);
  });

  afterEach(async () => {
    await cloud.close();
    await relay.close();
    await rm(tempRoot, { recursive: true, force: true });
  });

  it("maps RELAYFILE_REMOTE_PATH to RELAYFILE_LOCAL_DIR and reads seeded /notion files", async () => {
    relay.seedFile("/notion/research/brief.md", "# seeded notion file\n");

    const harness = await startMountHarness({
      env: {
        RELAYFILE_BASE_URL: relayBaseUrl,
        RELAYFILE_TOKEN: "rf_token_rw",
        RELAYFILE_WORKSPACE: "ws_harness",
        RELAYFILE_REMOTE_PATH: "/notion",
        RELAYFILE_LOCAL_DIR: localDir,
      },
      pollIntervalMs: 25,
    });

    expect(await readFile(path.join(localDir, "research/brief.md"), "utf8")).toBe(
      "# seeded notion file\n"
    );

    await harness.stop();
  });

  it("surfaces invited-agent read-only behavior as a permission-specific harness error", async () => {
    relay.seedFile("/notion/research/brief.md", "# seeded notion file\n");

    const harness = await startMountHarness({
      env: {
        RELAYFILE_BASE_URL: relayBaseUrl,
        RELAYFILE_TOKEN: "rf_token_ro",
        RELAYFILE_WORKSPACE: "ws_harness",
        RELAYFILE_REMOTE_PATH: "/notion",
        RELAYFILE_LOCAL_DIR: localDir,
      },
      pollIntervalMs: 25,
      scopes: ["fs:read"],
    });

    await expect(
      harness.writeLocalFile("review/notes.md", "no write access")
    ).rejects.toBeInstanceOf(MountHarnessPermissionError);

    await harness.stop();
  });

  it("stops cleanly and does not keep syncing after shutdown", async () => {
    relay.seedFile("/notion/research/brief.md", "# seeded notion file\n");

    const harness = await startMountHarness({
      env: {
        RELAYFILE_BASE_URL: relayBaseUrl,
        RELAYFILE_TOKEN: "rf_token_rw",
        RELAYFILE_WORKSPACE: "ws_harness",
        RELAYFILE_REMOTE_PATH: "/notion",
        RELAYFILE_LOCAL_DIR: localDir,
      },
      pollIntervalMs: 20,
    });

    await harness.stop();
    relay.seedFile("/notion/research/late-update.md", "should not sync after stop\n");
    await new Promise((resolve) => setTimeout(resolve, 80));

    await expect(
      readFile(path.join(localDir, "research/late-update.md"), "utf8")
    ).rejects.toMatchObject({ code: "ENOENT" });
  });

  it("follows opaque tree cursors across empty pages without repeating directories", async () => {
    const treeCalls: Array<{ path: string; cursor?: string }> = [];
    const readCalls: string[] = [];
    const client = {
      async listTree(
        _workspaceId: string,
        options: { path?: string; cursor?: string }
      ) {
        const remotePath = options.path ?? "/";
        treeCalls.push({ path: remotePath, cursor: options.cursor });
        if (remotePath === "/notion") {
          if (!options.cursor) {
            return { path: remotePath, entries: [], nextCursor: "root-empty" };
          }
          return {
            path: remotePath,
            entries: [
              { path: "/notion/research", type: "dir", revision: "dir" },
            ],
            nextCursor: null,
          };
        }
        if (options.cursor === undefined) {
          return {
            path: remotePath,
            entries: [
              {
                path: "/notion/research/brief.md",
                type: "file",
                revision: "rev_brief",
              },
            ],
            nextCursor: "child-empty",
          };
        }
        if (options.cursor === "child-empty") {
          return {
            path: remotePath,
            entries: [],
            nextCursor: "child-last",
          };
        }
        return {
          path: remotePath,
          entries: [
            {
              path: "/notion/research/second.md",
              type: "file",
              revision: "rev_second",
            },
          ],
          nextCursor: null,
        };
      },
      async readFile(_workspaceId: string, remotePath: string) {
        readCalls.push(remotePath);
        return {
          path: remotePath,
          revision: `rev_${readCalls.length}`,
          contentType: "text/markdown",
          content: `${remotePath}\n`,
          encoding: "utf-8" as const,
        };
      },
    };

    const harness = await startMountHarness({
      env: {
        RELAYFILE_BASE_URL: relayBaseUrl,
        RELAYFILE_TOKEN: "rf_token_rw",
        RELAYFILE_WORKSPACE: "ws_harness",
        RELAYFILE_REMOTE_PATH: "/notion",
        RELAYFILE_LOCAL_DIR: localDir,
      },
      pollIntervalMs: 60_000,
      client: client as never,
    });

    expect(treeCalls).toEqual([
      { path: "/notion", cursor: undefined },
      { path: "/notion", cursor: "root-empty" },
      { path: "/notion/research", cursor: undefined },
      { path: "/notion/research", cursor: "child-empty" },
      { path: "/notion/research", cursor: "child-last" },
    ]);
    expect(readCalls).toEqual([
      "/notion/research/brief.md",
      "/notion/research/second.md",
    ]);
    await expect(
      readFile(path.join(localDir, "research/brief.md"), "utf8")
    ).resolves.toBe("/notion/research/brief.md\n");
    await expect(
      readFile(path.join(localDir, "research/second.md"), "utf8")
    ).resolves.toBe("/notion/research/second.md\n");

    await harness.stop();
  });

  it("rejects a repeated tree cursor instead of looping", async () => {
    let calls = 0;
    const client = {
      async listTree(_workspaceId: string, options: { path?: string }) {
        calls += 1;
        return {
          path: options.path ?? "/",
          entries: [],
          nextCursor: "repeated-cursor",
        };
      },
    };

    await expect(
      startMountHarness({
        env: {
          RELAYFILE_BASE_URL: relayBaseUrl,
          RELAYFILE_TOKEN: "rf_token_rw",
          RELAYFILE_WORKSPACE: "ws_harness",
          RELAYFILE_REMOTE_PATH: "/notion",
          RELAYFILE_LOCAL_DIR: localDir,
        },
        pollIntervalMs: 60_000,
        client: client as never,
      })
    ).rejects.toThrow("Relayfile tree cursor repeated for /notion");
    expect(calls).toBe(2);
  });

  it("bounds pagination when every empty page returns a unique cursor", async () => {
    let calls = 0;
    const client = {
      async listTree(_workspaceId: string, options: { path?: string }) {
        calls += 1;
        return {
          path: options.path ?? "/",
          entries: [],
          nextCursor: `unique-cursor-${calls}`,
        };
      },
    };

    await expect(
      startMountHarness({
        env: {
          RELAYFILE_BASE_URL: relayBaseUrl,
          RELAYFILE_TOKEN: "rf_token_rw",
          RELAYFILE_WORKSPACE: "ws_harness",
          RELAYFILE_REMOTE_PATH: "/notion",
          RELAYFILE_LOCAL_DIR: localDir,
        },
        pollIntervalMs: 60_000,
        client: client as never,
      })
    ).rejects.toThrow(
      "Relayfile tree pagination exceeded 4096 pages for /notion"
    );
    expect(calls).toBe(4_096);
  });
});

async function loadHelpers() {
  return import(mockHelpersModule);
}
