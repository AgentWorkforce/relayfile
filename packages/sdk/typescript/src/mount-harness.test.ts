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
});

async function loadHelpers() {
  return import(mockHelpersModule);
}
