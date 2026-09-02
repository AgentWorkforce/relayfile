#!/usr/bin/env node

import { createHash, randomBytes } from "node:crypto";
import { spawn } from "node:child_process";
import { pathToFileURL } from "node:url";

const DEFAULT_TIMEOUT_SECONDS = 180;
const POLL_INTERVAL_MS = 200;
const MINIMUM_AGENT_RELAY_VERSION = "11.10.1";
const MINIMUM_RELAYFILE_VERSION = "0.10.52";

export function parseArgs(argv) {
  const options = {
    provider: "claude",
    timeoutSeconds: DEFAULT_TIMEOUT_SECONDS,
    workspace: undefined,
    node: undefined,
    mountPath: undefined,
    preflight: false,
    dryRun: false
  };

  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === "--dry-run") {
      options.dryRun = true;
      continue;
    }
    if (argument === "--preflight") {
      options.preflight = true;
      continue;
    }
    if (["--provider", "--timeout", "--workspace", "--node", "--mount-path"].includes(argument)) {
      const value = argv[index + 1];
      if (!value || value.startsWith("--")) throw new Error(`${argument} requires a value`);
      index += 1;
      if (argument === "--provider") options.provider = value;
      if (argument === "--workspace") options.workspace = value;
      if (argument === "--node") options.node = value;
      if (argument === "--mount-path") options.mountPath = value;
      if (argument === "--timeout") {
        options.timeoutSeconds = Number(value);
        if (!Number.isFinite(options.timeoutSeconds) || options.timeoutSeconds <= 0) {
          throw new Error("--timeout must be a positive number of seconds");
        }
      }
      continue;
    }
    throw new Error(`unknown option: ${argument}`);
  }

  if (options.node && !options.mountPath) {
    throw new Error("--node requires --mount-path so the agent starts in the real Relayfile mount");
  }
  if (!options.node && options.mountPath) {
    throw new Error("--mount-path is only valid with --node");
  }
  return options;
}

export function buildRemoteScript({ nonce, remotePath }) {
  return `set -euo pipefail
MOUNT="$(pwd -P)"
test -f "$MOUNT/.relay/state.json"
pgrep -f '[r]elayfile-mount' >/dev/null
TARGET="$MOUNT${remotePath}"
mkdir -p "$(dirname "$TARGET")"
HOST="$(hostname)"
WRITTEN_AT_MS="$(date +%s%3N)"
printf '{"proof_version":1,"origin":"agent-relay-daytona-sandbox","nonce":"%s","hostname":"%s","relayfile_mount":"%s","written_at_ms":%s}\\n' "${nonce}" "$HOST" "$MOUNT" "$WRITTEN_AT_MS" > "$TARGET"
sha256sum "$TARGET" | awk '{print $1}' > "$TARGET.sha256"
printf 'RELAYFILE_PROOF_WRITTEN\\npath=%s\\nsha256=' "$TARGET"
cat "$TARGET.sha256"`;
}

export function buildTask({ nonce, remotePath }) {
  const command = buildRemoteScript({ nonce, remotePath }).replaceAll("\n", "; ");
  return [
    "Run this exact bash command now from the current directory; do not substitute another path.",
    "If it exits nonzero, report FAIL. Otherwise report its output, then wait:",
    command
  ].join(" ");
}

export function extractJsonObject(output) {
  for (let index = output.indexOf("{"); index !== -1; index = output.indexOf("{", index + 1)) {
    try {
      return JSON.parse(output.slice(index).trim());
    } catch {
      // Agent Relay may print a note before its JSON object. Try the next brace.
    }
  }
  throw new Error("agent-relay did not return a JSON result");
}

export function parseWorkspaceLabel(output) {
  const current = output.trim().split("\n").at(-1)?.replace(/\s+\([^)]*\)$/, "").trim();
  if (!current) throw new Error("Relayfile did not report a current workspace");
  return current;
}

export function selectWorkspace(activeWorkspace, workspaceOverride) {
  if (workspaceOverride && workspaceOverride.toLowerCase() !== activeWorkspace.toLowerCase()) {
    throw new Error(
      `--workspace ${workspaceOverride} does not match the active Agent Relay Cloud workspace ${activeWorkspace}`
    );
  }
  return activeWorkspace;
}

export function parseCliVersion(output) {
  const match = output.match(/(?:^|\s)v?(\d+\.\d+\.\d+)(?:[-+\s]|$)/);
  if (!match) throw new Error(`could not parse CLI version from: ${output.trim()}`);
  return match[1];
}

export function compareVersions(left, right) {
  const leftParts = left.split(".").map(Number);
  const rightParts = right.split(".").map(Number);
  for (let index = 0; index < 3; index += 1) {
    if (leftParts[index] !== rightParts[index]) return leftParts[index] - rightParts[index];
  }
  return 0;
}

export function portableAgentRelayCommand(command) {
  return command.replace(/^agent-relay\b/, `npx agent-relay@${MINIMUM_AGENT_RELAY_VERSION}`);
}

export function verifyProof({ body, sidecar, expectedNonce }) {
  const digest = createHash("sha256").update(body).digest("hex");
  const remoteDigest = sidecar.trim();
  if (!/^[a-f0-9]{64}$/.test(remoteDigest)) throw new Error("remote SHA-256 sidecar is invalid");
  if (digest !== remoteDigest) throw new Error(`SHA-256 mismatch: local ${digest}, remote ${remoteDigest}`);

  let payload;
  try {
    payload = JSON.parse(body);
  } catch {
    throw new Error("proof artifact is not valid JSON");
  }
  if (payload.proof_version !== 1) throw new Error("unexpected proof version");
  if (payload.origin !== "agent-relay-daytona-sandbox") throw new Error("unexpected proof origin");
  if (payload.nonce !== expectedNonce) throw new Error("proof nonce does not match this run");
  if (typeof payload.hostname !== "string" || payload.hostname.length === 0) throw new Error("missing sandbox hostname");
  if (typeof payload.relayfile_mount !== "string" || !payload.relayfile_mount.startsWith("/")) {
    throw new Error("missing absolute Relayfile mount path");
  }
  if (!Number.isFinite(payload.written_at_ms)) throw new Error("missing remote write timestamp");
  return { digest, payload };
}

export function run(command, args, { timeoutMs } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { env: process.env, stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    let settled = false;
    let killTimer;
    const timeout = Number.isFinite(timeoutMs) && timeoutMs > 0
      ? setTimeout(() => {
          if (settled) return;
          settled = true;
          child.kill("SIGTERM");
          killTimer = setTimeout(() => child.kill("SIGKILL"), 1_000);
          killTimer.unref();
          const error = new Error(`${command} timed out after ${Math.ceil(timeoutMs)}ms`);
          error.code = "COMMAND_TIMEOUT";
          reject(error);
        }, timeoutMs)
      : undefined;
    timeout?.unref();
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk;
    });
    child.on("error", (error) => {
      if (settled) return;
      settled = true;
      if (timeout) clearTimeout(timeout);
      reject(error);
    });
    child.on("close", (code) => {
      if (killTimer) clearTimeout(killTimer);
      if (settled) return;
      settled = true;
      if (timeout) clearTimeout(timeout);
      resolve({ code: code ?? 1, stdout, stderr });
    });
  });
}

async function requireCommand(command, minimumVersion) {
  const result = await run(command, ["--version"]);
  if (result.code !== 0) throw new Error(`${command} is not installed or not runnable`);
  const version = parseCliVersion(result.stdout || result.stderr);
  if (compareVersions(version, minimumVersion) < 0) {
    throw new Error(`${command} ${version} is too old; this proof requires ${minimumVersion} or newer`);
  }
  return version;
}

async function requireCloudSession() {
  const result = await run("agent-relay", ["cloud", "whoami"]);
  if (result.code !== 0) {
    throw new Error("Agent Relay Cloud is not authenticated; run `agent-relay cloud login`");
  }
}

async function detectWorkspace() {
  const result = await run("relayfile", ["workspace", "current", "--verbose"]);
  if (result.code !== 0) throw new Error(`could not detect Relayfile workspace: ${result.stderr.trim()}`);
  return parseWorkspaceLabel(result.stdout);
}

async function preflight(workspaceOverride) {
  const [agentRelayVersion, relayfileVersion] = await Promise.all([
    requireCommand("agent-relay", MINIMUM_AGENT_RELAY_VERSION),
    requireCommand("relayfile", MINIMUM_RELAYFILE_VERSION)
  ]);
  await requireCloudSession();
  const activeWorkspace = await detectWorkspace();
  const workspace = selectWorkspace(activeWorkspace, workspaceOverride);
  return { agentRelayVersion, relayfileVersion, workspace };
}

async function readRemote(workspace, remotePath, timeoutMs) {
  try {
    const result = await run("relayfile", ["read", workspace, remotePath], { timeoutMs });
    return result.code === 0 ? result.stdout : undefined;
  } catch (error) {
    if (error.code === "COMMAND_TIMEOUT") return undefined;
    throw error;
  }
}

async function waitForProof({ workspace, remotePath, expectedNonce, timeoutSeconds }) {
  const deadline = Date.now() + timeoutSeconds * 1000;
  let lastValidationError;
  while (Date.now() < deadline) {
    const remainingMs = Math.max(1, deadline - Date.now());
    const [body, sidecar] = await Promise.all([
      readRemote(workspace, remotePath, remainingMs),
      readRemote(workspace, `${remotePath}.sha256`, remainingMs)
    ]);
    if (body !== undefined && sidecar !== undefined) {
      try {
        return { ...verifyProof({ body, sidecar, expectedNonce }), receivedAtMs: Date.now(), body };
      } catch (error) {
        lastValidationError = error;
      }
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
  }
  const suffix = lastValidationError ? ` Last validation error: ${lastValidationError.message}` : "";
  throw new Error(`timed out after ${timeoutSeconds}s waiting for ${remotePath}.${suffix}`);
}

function printPlan({ provider, agentName, nodeName, task, remotePath, workspace, fresh }) {
  console.log("\nRelayfile cross-machine proof");
  console.log("─────────────────────────────");
  console.log(`Workspace:  ${workspace ?? "<auto-detect at runtime>"}`);
  console.log(`Surface:    ${fresh ? "fresh Cloud Daytona sandbox" : nodeName}`);
  console.log(`Agent:      ${agentName} (${provider})`);
  console.log(`Artifact:   ${remotePath}`);
  if (process.env.RELAYFILE_PROOF_SHOW_TASK === "1") console.log(`\nAgent task:\n${task}`);
}

export async function main(argv = process.argv.slice(2)) {
  const options = parseArgs(argv);
  const suffix = `${Date.now().toString(36)}-${randomBytes(3).toString("hex")}`;
  const agentName = `relayfile-proof-${suffix}`;
  const requestedNodeName = options.node ?? `relayfile-proof-daytona-${suffix}`;
  const nonce = `RF-${randomBytes(12).toString("hex")}`;
  const remotePath = `/relayfile-wow/proofs/${suffix}.json`;
  const task = buildTask({ nonce, remotePath });

  printPlan({
    provider: options.provider,
    agentName,
    nodeName: requestedNodeName,
    task,
    remotePath,
    workspace: options.workspace,
    fresh: !options.node
  });

  if (options.dryRun) {
    console.log("\nDRY RUN — no sandbox or agent was created.");
    return;
  }

  const { agentRelayVersion, relayfileVersion, workspace } = await preflight(options.workspace);
  console.log(`CLIs:       agent-relay ${agentRelayVersion}; relayfile ${relayfileVersion}`);
  console.log("Cloud:      authenticated");
  console.log(`Workspace:  ${workspace}`);
  if (options.preflight) {
    console.log("\nPREFLIGHT PASS — tooling, Cloud auth, and the Relayfile workspace are ready.");
    console.log(`Provider:   ensure \`${options.provider}\` is connected with \`npx agent-relay@${MINIMUM_AGENT_RELAY_VERSION} cloud connect ${options.provider}\``);
    return;
  }
  console.log(options.node ? "\nStarting proof agent on the existing mounted node…" : "\nProvisioning a fresh mounted Daytona sandbox…");

  const spawnArgs = [
    "fleet", "spawn", options.provider,
    "--name", agentName,
    "--task", task,
    "--confirm-timeout", "120000"
  ];
  if (options.node) {
    // In reuse mode the cryptographic artifact is the launch confirmation. This
    // avoids waiting on older brokers that launch correctly but never return a
    // placement result.
    spawnArgs.push("--node", options.node, "--cwd", options.mountPath, "--no-confirm");
  }
  else spawnArgs.push("--sandbox", "--sandbox-name", requestedNodeName);

  const startedAtMs = Date.now();
  const spawned = await run("agent-relay", spawnArgs);
  if (spawned.code !== 0) {
    process.stderr.write(spawned.stderr);
    process.stderr.write(spawned.stdout);
    throw new Error("agent-relay could not provision and launch the proof agent");
  }

  const result = extractJsonObject(spawned.stdout);
  const actualNodeName = result.sandbox?.nodeName ?? options.node;
  const mountPath = result.sandbox?.relayfileMountPath ?? options.mountPath;
  if (!actualNodeName) throw new Error("spawn result did not identify the target node");
  if (!mountPath) throw new Error("spawn result did not identify a Relayfile mount path");
  if (result.sandbox && result.sandbox.relayfileMounted !== true) {
    throw new Error("Cloud provisioned a sandbox without a verified Relayfile mount");
  }

  const attachCommand = portableAgentRelayCommand(result.attachCommand ??
    `agent-relay node agent attach ${agentName} --node ${actualNodeName} --mode drive`);
  console.log("\nAgent is live inside the Relayfile mount.");
  console.log(`Mount:      ${mountPath}`);
  console.log(`Attach:     ${attachCommand}`);
  console.log("Detach:     Ctrl+C (the agent keeps running)");
  console.log(`\nWaiting up to ${options.timeoutSeconds}s for the remote bytes and SHA-256 sidecar…`);

  const proof = await waitForProof({
    workspace,
    remotePath,
    expectedNonce: nonce,
    timeoutSeconds: options.timeoutSeconds
  });
  const endToEndMs = proof.receivedAtMs - startedAtMs;
  const propagationMs = proof.receivedAtMs - proof.payload.written_at_ms;

  console.log("\nPASS — a real file crossed the machine boundary through Relayfile.");
  console.log(`Remote host: ${proof.payload.hostname}`);
  console.log(`Remote mount:${proof.payload.relayfile_mount}`);
  console.log(`Local read:  relayfile read ${workspace} ${remotePath}`);
  console.log(`SHA-256:     ${proof.digest} (remote sidecar = local computation)`);
  const startupScope = result.sandbox ? "sandbox provisioning and agent startup" : "agent startup";
  console.log(`End to end:  ${endToEndMs}ms including ${startupScope}`);
  if (propagationMs >= 0 && propagationMs < 600_000) {
    console.log(`Observation: ${propagationMs}ms from remote write timestamp to verified local read`);
  } else {
    console.log(`Observation: unavailable because the two machine clocks differ by ${propagationMs}ms`);
  }
  console.log("Git commits: 0");
  console.log("Host paths:  0");
  console.log("Agent-side transfer commands: filesystem only");
  console.log(`\nAttach now:  ${attachCommand}`);
  console.log(`Release:     npx agent-relay@${MINIMUM_AGENT_RELAY_VERSION} fleet release ${agentName}`);
  if (result.sandbox?.sandboxId) console.log(`Sandbox ID:  ${result.sandbox.sandboxId}`);
}

const isEntryPoint = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isEntryPoint) {
  main().catch((error) => {
    console.error(`\nFAIL — ${error.message}`);
    process.exitCode = 1;
  });
}
