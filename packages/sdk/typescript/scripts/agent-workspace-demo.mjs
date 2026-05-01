import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  createMockCloudServer,
  createMockRelayfileServer,
  listenServer,
} from "./agent-workspace-mocks.mjs";

const scriptPath = fileURLToPath(import.meta.url);
const scriptsDir = path.dirname(scriptPath);
const packageDir = path.resolve(scriptsDir, "..");

const { RelayfileSetup } = await import(path.join(packageDir, "dist/index.js"));
const {
  MountHarnessPermissionError,
  startMountHarness,
} = await import(path.join(packageDir, "dist/mount-harness.js"));

async function main() {
  const tempRoot = await mkdtemp(path.join(tmpdir(), "relayfile-agent-workspace-demo-"));
  const leadLocalDir = path.join(tempRoot, "lead-notion");
  const invitedLocalDir = path.join(tempRoot, "review-notion");
  const relayBaseUrlRef = { current: "" };

  const relay = createMockRelayfileServer();
  const cloud = createMockCloudServer({
    relayfileBaseUrl: () => relayBaseUrlRef.current,
  });

  try {
    relayBaseUrlRef.current = await listenServer(relay.server);
    const cloudBaseUrl = await listenServer(cloud.server);
    relay.seedFile("/notion/research/brief.md", "# Mock Notion brief\n");

    const setup = new RelayfileSetup({
      cloudApiUrl: cloudBaseUrl,
      requestTimeoutMs: 1_000,
      retry: { maxRetries: 0, baseDelayMs: 25 },
    });

    const workspace = await setup.createWorkspace({
      name: "agent-workspace-demo",
      agentName: "lead-agent",
      scopes: ["fs:read", "fs:write", "relaycast:write"],
    });

    console.log(`workspace=${workspace.workspaceId}`);

    const notion = await workspace.connectNotion();
    console.log(`connectLink=${notion.connectLink}`);
    await workspace.waitForNotion({
      connectionId: notion.connectionId,
      pollIntervalMs: 25,
      timeoutMs: 1_000,
    });
    console.log("notionReady=true");

    const leadHarness = await startMountHarness({
      env: workspace.mountEnv({
        localDir: leadLocalDir,
        remotePath: "/notion",
      }),
      pollIntervalMs: 25,
    });

    const seededContent = await readFile(
      path.join(leadLocalDir, "research/brief.md"),
      "utf8"
    );
    console.log(`leadRead=${JSON.stringify(seededContent.trim())}`);

    const invite = workspace.agentInvite({
      agentName: "review-agent",
      scopes: ["fs:read"],
    });

    const invitedHarness = await startMountHarness({
      env: workspace.mountEnv({
        localDir: invitedLocalDir,
        remotePath: "/notion",
      }),
      pollIntervalMs: 25,
      scopes: invite.scopes,
    });

    const invitedRead = await readFile(
      path.join(invitedLocalDir, "research/brief.md"),
      "utf8"
    );
    assert.equal(invitedRead, seededContent);
    console.log(`invitedRead=${JSON.stringify(invitedRead.trim())}`);

    try {
      await invitedHarness.writeLocalFile("review/notes.md", "should fail");
      throw new Error("Expected read-only invited harness write to fail.");
    } catch (error) {
      if (!(error instanceof MountHarnessPermissionError)) {
        throw error;
      }
      console.log(`readOnlyDenied=${error.code}`);
    }

    await invitedHarness.stop();
    await leadHarness.stop();
    console.log("shutdown=clean");
  } finally {
    await cloud.close();
    await relay.close();
    await rm(tempRoot, { recursive: true, force: true });
  }
}

await main();
