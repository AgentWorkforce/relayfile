#!/usr/bin/env node
// Two-arm end-to-end proof for relayfile#455 / PR#457, run against a REAL
// Daytona sandbox.
//
// #455 was a 100% failure rate on JIT sandbox provisioning. The mechanism the
// fix claims is structural, not a race:
//
//   1. one Reconcile mirrors at most defaultBootstrapMaxFilesPerCycle = 2000
//      files (internal/mountsync/syncer.go), persists a resume cursor, and
//      yields with traversal_complete=false;
//   2. markSyncSuccess() still stamps lastSuccessfulReconcileAt and run()
//      returns nil, so `--once` exits 0;
//   3. but .relay/state.json still carries a non-null `bootstrap` block;
//   4. the sandbox readiness guard requires bootstrap == null, so it exits 75.
//
// Therefore any workspace larger than 2000 files could never satisfy the
// barrier. This harness proves that, and proves the fix cures it, by running
// the SAME provision twice against the same remote subtree:
//
//   arm A (must fail 75): the mount binary already baked into the snapshot.
//   arm B (must pass 0):  a relayfile-mount built from the checkout under test.
//
// A proof that only passes is worthless: if arm A does not fail, the fixture
// never reached the mechanism (almost certainly a subtree under the 2000-file
// budget) and the run is reported INCONCLUSIVE rather than green.
//
// The readiness guard is NOT reimplemented here. It is imported from the
// installed @agent-relay/sandbox package and invoked exactly the way
// provisioning invokes it, via
// buildRelayfileMountInitialSyncBackgroundShell (the detached launcher —
// Daytona's exec proxy read-times-out around 120s, so a single long-running
// foreground exec cannot host a real initial sync).
//
// RUNTIME REQUIREMENT — read this before filing a bug that it does not run.
// This harness deliberately imports the readiness guard from the SAME package
// production uses rather than vendoring a copy, so it needs three modules that
// are NOT dependencies of this repo:
//
//   @daytonaio/sdk
//   @agent-relay/sandbox            (0.1.14 — the version cloud pins)
//   @cloud/core/relayfile/client.js (only for minting a workspace token)
//
// Run it from a checkout where those resolve (the `cloud` repo), or set
// NODE_PATH to that repo's node_modules. Vendoring the guard here instead
// would defeat the whole point: a copy proves nothing about the shell the
// orchestrator actually emits.
//
// Usage:
//   DAYTONA_API_KEY=... WEB_RELAYAUTH_API_KEY=... \
//   RELAYFILE_PROOF_WORKSPACE_ID=rw_xxxxxxxx \
//   RELAYFILE_PROOF_REMOTE_PATH=/github/repos/Org/repo \
//   node <relayfile>/scripts/test-sandbox-initial-sync-readiness-e2e.mjs
//
// Exit codes: 0 proof held (A=75, B=0); 1 proof refuted; 2 INCONCLUSIVE /
// UNKNOWN (could not provision, could not reach the mechanism, timed out).
// A timeout is always UNKNOWN, never a pass.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import { Daytona } from "@daytonaio/sdk";
import { buildRelayfileMountInitialSyncBackgroundShell } from "@agent-relay/sandbox/relayfile/mount-script.js";
import { mintRelayfileTokenPair, MAX_RETAINED_REFRESH_TOKEN_TTL_SECONDS } from "@cloud/core/relayfile/client.js";

const EXIT_PROOF_HELD = 0;
const EXIT_PROOF_REFUTED = 1;
const EXIT_UNKNOWN = 2;

/** sysexits.h TEMPFAIL — what the readiness guard returns when incomplete. */
const READINESS_INCOMPLETE = 75;
/** One Reconcile's file budget. The whole mechanism is this number. */
const BOOTSTRAP_MAX_FILES_PER_CYCLE = 2000;

const cfg = {
  snapshot: process.env.RELAYFILE_PROOF_SNAPSHOT
    ?? "relay-orchestrator-sdk-11.8.2-relayfile-v0.10.50-runtime-4.1.52",
  baseUrl: process.env.RELAYFILE_PROOF_BASE_URL ?? "https://file.agentrelay.com",
  workspaceId: required("RELAYFILE_PROOF_WORKSPACE_ID"),
  remotePath: required("RELAYFILE_PROOF_REMOTE_PATH"),
  relayAuthUrl: process.env.WEB_RELAYAUTH_URL ?? "https://api.relayauth.dev",
  relayAuthApiKey: required("WEB_RELAYAUTH_API_KEY"),
  // Generous: arm B must run as many resume cycles as the subtree needs.
  armTimeoutMs: Number(process.env.RELAYFILE_PROOF_ARM_TIMEOUT_MS ?? 45 * 60 * 1000),
  evidenceDir: process.env.RELAYFILE_PROOF_EVIDENCE_DIR ?? "",
};

function required(name) {
  const v = process.env[name]?.trim();
  if (!v) {
    console.error(`UNKNOWN: ${name} is required; refusing to substitute a local reproduction for a sandbox run`);
    process.exit(EXIT_UNKNOWN);
  }
  return v;
}

const repoRoot = path.resolve(import.meta.dirname, "..");

/** Build relayfile-mount for the sandbox's platform from the checkout under test. */
function buildCandidateMount(scratch) {
  const out = path.join(scratch, "relayfile-mount-candidate");
  execFileSync("go", ["build", "-o", out, "./cmd/relayfile-mount"], {
    cwd: repoRoot,
    env: { ...process.env, CGO_ENABLED: "0", GOOS: "linux", GOARCH: "amd64" },
    stdio: "inherit",
  });
  return out;
}

async function exec(sandbox, command, timeoutSec = 180) {
  const r = await sandbox.process.executeCommand(command, undefined, undefined, timeoutSec);
  return { exitCode: r.exitCode, output: String(r.result ?? "") };
}

/**
 * Run one arm to completion and return its guard exit code plus the raw
 * .relay/state.json bytes. `binDir`, when set, is prepended to PATH so the
 * identical generated command resolves to a different relayfile-mount — the
 * two arms differ ONLY in which binary runs.
 */
async function runArm(sandbox, { arm, token, binDir }) {
  const localDir = `/home/daytona/ws-${arm}`;
  const credsFilePath = `/home/daytona/.relayfile-mount-creds-${arm}.json`;
  const opts = {
    baseUrl: cfg.baseUrl,
    workspaceId: cfg.workspaceId,
    localDir,
    stateDir: `/home/daytona/.mountstate-${arm}`,
    token,
    tokenIngress: "creds-file",
    credsFilePath,
    paths: [cfg.remotePath],
    idleTimeoutSeconds: 300,
  };
  // The REAL launcher + readiness guard from @agent-relay/sandbox.
  const launcher = buildRelayfileMountInitialSyncBackgroundShell(opts, { runId: `arm${arm}` });
  const b64 = (s) => Buffer.from(s, "utf8").toString("base64");

  await exec(sandbox, [
    "set -u",
    `rm -rf ${localDir} /home/daytona/.mountstate-${arm}`,
    `rm -f /tmp/relayfile-initial-sync.*.arm${arm}`,
    `mkdir -p ${localDir} /home/daytona/.mountstate-${arm}`,
    "umask 077",
    `echo ${b64(JSON.stringify({ token }))} | base64 -d > ${credsFilePath}`,
    `chmod 600 ${credsFilePath}`,
    `echo ${b64(launcher)} | base64 -d > /home/daytona/bg-${arm}.sh`,
    `${binDir ? `PATH=${binDir}:$PATH ` : ""}sh /home/daytona/bg-${arm}.sh`,
  ].join("\n"));

  const exitSentinel = `/tmp/relayfile-initial-sync.exit.arm${arm}`;
  const deadline = Date.now() + cfg.armTimeoutMs;
  while (Date.now() < deadline) {
    const s = await exec(sandbox, `if [ -f ${exitSentinel} ]; then cat ${exitSentinel}; else echo running; fi`, 60);
    const v = s.output.trim();
    if (v !== "running" && v !== "") {
      return { ...(await collect(sandbox, arm, localDir)), guardExit: Number(v) };
    }
    await new Promise((r) => setTimeout(r, 20_000));
  }
  // A timeout is UNKNOWN, never a pass.
  return { ...(await collect(sandbox, arm, localDir)), guardExit: null, timedOut: true };
}

async function collect(sandbox, arm, localDir) {
  const stateFile = `${localDir}${cfg.remotePath}/.relay/state.json`;
  const [state, files, log] = await Promise.all([
    exec(sandbox, `cat ${stateFile} 2>/dev/null || echo '{"__missing":true}'`, 120),
    exec(sandbox, `find ${localDir} -type f -not -path '*/.relay/*' | wc -l`, 120),
    exec(sandbox, `tail -40 /tmp/relayfile-initial-sync.log.arm${arm} 2>/dev/null`, 60),
  ]);
  let parsed = null;
  try { parsed = JSON.parse(state.output); } catch { /* keep raw */ }
  return {
    arm,
    stateRaw: state.output,
    state: parsed,
    mirroredFiles: Number(files.output.trim()) || 0,
    log: log.output,
  };
}

function summarize(r) {
  const b = r.state?.bootstrap;
  return {
    arm: r.arm,
    guardExit: r.guardExit,
    timedOut: Boolean(r.timedOut),
    mirroredFiles: r.mirroredFiles,
    bootstrapNull: b == null,
    filesSynced: b?.filesSynced ?? null,
    lastSuccessfulReconcileAt: r.state?.lastSuccessfulReconcileAt ?? null,
    stateBytes: r.stateRaw.length,
  };
}

async function main() {
  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), "relayfile-455-proof-"));
  const candidate = buildCandidateMount(scratch);

  const daytona = new Daytona({ apiKey: required("DAYTONA_API_KEY") });
  let sandbox;
  try {
    sandbox = await daytona.create({ snapshot: cfg.snapshot });
  } catch (e) {
    // Cannot provision => UNKNOWN. Do NOT fall back to a local reproduction.
    console.error("UNKNOWN: could not provision a Daytona sandbox:", e?.message ?? e);
    process.exit(EXIT_UNKNOWN);
  }

  try {
    const { accessToken } = await mintRelayfileTokenPair({
      workspaceId: cfg.workspaceId,
      agentName: "relayfile-455-proof",
      scopes: ["fs:read", "fs:write", "sync:read", "sync:trigger"],
      relayAuthUrl: cfg.relayAuthUrl,
      relayAuthApiKey: cfg.relayAuthApiKey,
      ttlSeconds: 3600,
      refreshTokenTtlSeconds: MAX_RETAINED_REFRESH_TOKEN_TTL_SECONDS,
    });

    await sandbox.fs.uploadFile(fs.readFileSync(candidate), "/home/daytona/relayfile-mount-candidate");
    await exec(sandbox, [
      "mkdir -p /home/daytona/binB",
      "cp /home/daytona/relayfile-mount-candidate /home/daytona/binB/relayfile-mount",
      "chmod +x /home/daytona/binB/relayfile-mount",
    ].join("\n"));

    // arm A: the snapshot's own binary, on the default PATH.
    const a = await runArm(sandbox, { arm: "A", token: accessToken });
    // arm B: the candidate, resolved first on PATH. Same command otherwise.
    const b = await runArm(sandbox, { arm: "B", token: accessToken, binDir: "/home/daytona/binB" });

    const A = summarize(a);
    const B = summarize(b);
    console.log(JSON.stringify({ armA: A, armB: B }, null, 2));

    if (cfg.evidenceDir) {
      fs.mkdirSync(cfg.evidenceDir, { recursive: true });
      for (const r of [a, b]) {
        fs.writeFileSync(path.join(cfg.evidenceDir, `arm${r.arm}-state.json`), r.stateRaw);
        fs.writeFileSync(path.join(cfg.evidenceDir, `arm${r.arm}-initial-sync.log`), r.log);
      }
    }

    if (A.timedOut || B.timedOut) {
      console.error("UNKNOWN: an arm timed out; a timeout is not a pass");
      return EXIT_UNKNOWN;
    }
    // The must-fail control is the whole point. If arm A passed, the fixture
    // never reached the 2000-file budget and neither arm proves anything.
    if (A.guardExit !== READINESS_INCOMPLETE) {
      console.error(
        `INCONCLUSIVE: arm A exited ${A.guardExit}, expected ${READINESS_INCOMPLETE}. `
        + `It mirrored ${A.mirroredFiles} files; the mechanism needs a subtree larger than `
        + `${BOOTSTRAP_MAX_FILES_PER_CYCLE}. Point RELAYFILE_PROOF_REMOTE_PATH at a bigger tree.`,
      );
      return EXIT_UNKNOWN;
    }
    if (B.guardExit !== 0 || !B.bootstrapNull) {
      console.error(
        `REFUTED: arm B exited ${B.guardExit} with bootstrap${B.bootstrapNull ? " null" : " still non-null"}. `
        + "The fix does not cure #455 on the real path.",
      );
      return EXIT_PROOF_REFUTED;
    }
    console.log(
      `PROOF HELD: arm A exit ${A.guardExit} (bootstrap non-null at ${A.filesSynced} files), `
      + `arm B exit ${B.guardExit} (bootstrap null, ${B.mirroredFiles} files mirrored).`,
    );
    return EXIT_PROOF_HELD;
  } finally {
    await sandbox.delete().catch(() => {});
    fs.rmSync(scratch, { recursive: true, force: true });
  }
}

process.exit(await main());
