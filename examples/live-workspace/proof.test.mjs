import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";

import {
  buildRemoteScript,
  buildTask,
  compareVersions,
  extractJsonObject,
  parseArgs,
  parseCliVersion,
  parseWorkspaceLabel,
  portableAgentRelayCommand,
  verifyProof
} from "./proof.mjs";

test("parseArgs requires the real mount path when reusing a node", () => {
  assert.throws(() => parseArgs(["--node", "daytona-1"]), /requires --mount-path/);
  assert.deepEqual(
    parseArgs(["--node", "daytona-1", "--mount-path", "/workspace", "--timeout", "30"]),
    {
      provider: "claude",
      timeoutSeconds: 30,
      workspace: undefined,
      node: "daytona-1",
      mountPath: "/workspace",
      preflight: false,
      dryRun: false
    }
  );
});

test("parseArgs supports a read-only preflight", () => {
  assert.equal(parseArgs(["--preflight"]).preflight, true);
});

test("CLI versions are parsed and compared without dependencies", () => {
  assert.equal(parseCliVersion("relayfile v0.10.51\n"), "0.10.51");
  assert.equal(parseCliVersion("11.8.7\n"), "11.8.7");
  assert.ok(compareVersions("11.8.7", "11.8.6") > 0);
  assert.equal(compareVersions("0.10.51", "0.10.51"), 0);
  assert.ok(compareVersions("0.10.44", "0.10.51") < 0);
});

test("attach commands work when the CLIs are installed only in the example", () => {
  assert.equal(
    portableAgentRelayCommand("agent-relay node agent attach proof --node sandbox --mode drive"),
    "npx agent-relay@11.8.7 node agent attach proof --node sandbox --mode drive"
  );
});

test("remote script gates writes on mount metadata and a live mount process", () => {
  const script = buildRemoteScript({ nonce: "RF-test", remotePath: "/proof/test.json" });
  assert.match(script, /test -f "\$MOUNT\/\.relay\/state\.json"/);
  assert.match(script, /pgrep -f '\[r\]elayfile-mount'/);
  assert.match(script, /sha256sum/);
  assert.doesNotMatch(script, /(^|\n)(git|curl|sudo)\s|\/AgentWorkforce/);

  const task = buildTask({ nonce: "RF-test", remotePath: "/proof/test.json" });
  assert.match(task, /Run this exact bash command now/);
  assert.doesNotMatch(task, /base64/);
  assert.ok(task.length < 1_000, `task should be quick to inject into a PTY, got ${task.length} bytes`);
});

test("extractJsonObject accepts Agent Relay notes before JSON", () => {
  const parsed = extractJsonObject('Note: pinned workspace\n{"sandbox":{"nodeName":"daytona"}}\n');
  assert.equal(parsed.sandbox.nodeName, "daytona");
});

test("workspace labels from old and new Relayfile CLIs reduce to the workspace name", () => {
  assert.equal(parseWorkspaceLabel("Default (source: Agent Relay Cloud session)\n"), "Default");
  assert.equal(parseWorkspaceLabel("default (id: rw_123, source: agent-relay)\n"), "default");
});

test("verifyProof requires matching bytes, nonce, and origin", () => {
  const body = `${JSON.stringify({
    proof_version: 1,
    origin: "agent-relay-daytona-sandbox",
    nonce: "RF-test",
    hostname: "sandbox-123",
    relayfile_mount: "/home/daytona/workspace",
    written_at_ms: 1
  }, null, 2)}\n`;
  const sidecar = `${createHash("sha256").update(body).digest("hex")}\n`;
  const proof = verifyProof({ body, sidecar, expectedNonce: "RF-test" });
  assert.equal(proof.payload.hostname, "sandbox-123");
  assert.throws(() => verifyProof({ body, sidecar, expectedNonce: "RF-other" }), /nonce/);
  assert.throws(() => verifyProof({ body: `${body}x`, sidecar, expectedNonce: "RF-test" }), /mismatch/);
});
