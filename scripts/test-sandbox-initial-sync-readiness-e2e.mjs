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
// The readiness guard is NOT reimplemented here. It is imported from the
// installed @agent-relay/sandbox package and invoked exactly the way
// provisioning invokes it, via
// buildRelayfileMountInitialSyncBackgroundShell (the detached launcher —
// Daytona's exec proxy read-times-out around 120s, so a single long-running
// foreground exec cannot host a real initial sync).
//
// ---------------------------------------------------------------------------
// THIS HARNESS MUST FAIL LIKE THE THING IT TESTS.
//
// It gates future merges, so a weak check here becomes a false green for
// everyone later. Three rules follow, and each is enforced below:
//
//   * exit 75 from arm A is NOT sufficient. The guard returns 75 for ANY
//     incomplete-or-unreadable state, including an auth failure before a
//     single file syncs (observed during the original run: `403 missing
//     required scope: fs:read` with filesSynced 0 produced a textbook-looking
//     75). The control is accepted only when the parsed state also shows a
//     non-null bootstrap block at >= the per-cycle budget AND arm A's log
//     reports traversal_complete=false.
//   * infrastructure failure is UNKNOWN, never REFUTED. Only a candidate that
//     actually ran and left an incomplete bootstrap may be called refuted.
//   * a timeout is UNKNOWN, never a pass.
// ---------------------------------------------------------------------------
//
// MODULE RESOLUTION. The guard is imported from the SAME package production
// uses rather than vendored, so this needs three modules that are NOT
// dependencies of this repo:
//
//   @daytonaio/sdk
//   @agent-relay/sandbox/core        (0.1.14 — the version cloud pins)
//   @cloud/core/relayfile/client.js  (only to mint a workspace token)
//
// Bare-specifier ESM resolution starts at THIS file and walks up, and ESM
// ignores NODE_PATH, so running from another checkout does not help. Point
// RELAYFILE_PROOF_MODULE_BASE at a directory whose node_modules contains them
// (e.g. the `cloud` checkout) and they are loaded from there; otherwise the
// bare specifiers are tried and a clear UNKNOWN is reported if they miss.
// Vendoring a copy of the guard instead would defeat the whole point.
//
// Usage:
//   DAYTONA_API_KEY=... WEB_RELAYAUTH_API_KEY=... \
//   RELAYFILE_PROOF_WORKSPACE_ID=rw_xxxxxxxx \
//   RELAYFILE_PROOF_REMOTE_PATH=/github/repos/Org/repo \
//   RELAYFILE_PROOF_MODULE_BASE=/path/to/cloud \
//   node <relayfile>/scripts/test-sandbox-initial-sync-readiness-e2e.mjs
//
// Exit codes: 0 proof held (A=75 having reached the budget, B=0); 1 proof
// refuted (arm B genuinely ran and did not converge); 2 UNKNOWN — could not
// provision, could not resolve modules, an arm timed out, credentials
// expired, or the control did not reach the mechanism.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { pathToFileURL, fileURLToPath } from "node:url";

export const EXIT_PROOF_HELD = 0;
export const EXIT_PROOF_REFUTED = 1;
export const EXIT_UNKNOWN = 2;

/** sysexits.h TEMPFAIL — what the readiness guard returns when incomplete. */
export const READINESS_INCOMPLETE = 75;
/** One Reconcile's file budget. The whole mechanism is this number. */
export const BOOTSTRAP_MAX_FILES_PER_CYCLE = 2000;

/** Reported as UNKNOWN: the run was interrupted, it did not answer the question. */
class HarnessUnknown extends Error {}

function unknown(msg) {
  throw new HarnessUnknown(msg);
}

function requiredEnv(name) {
  const v = process.env[name]?.trim();
  if (!v) unknown(`${name} is required; refusing to substitute a local reproduction for a sandbox run`);
  return v;
}

/**
 * Resolve modules that are not dependencies of this repo.
 *
 * Bare-specifier ESM resolution starts at THIS file and walks up, and ESM
 * ignores NODE_PATH, so neither running from another checkout nor exporting
 * NODE_PATH makes these resolvable. `createRequire(base).resolve()` does not
 * work either: it resolves under the CJS "require" condition, and
 * @agent-relay/sandbox exports only "import"/"types"
 * (ERR_PACKAGE_PATH_NOT_EXPORTED).
 *
 * So write a throwaway ESM shim inside RELAYFILE_PROOF_MODULE_BASE and import
 * that by file URL: the bare specifiers inside it resolve against the base's
 * own node_modules using Node's real resolver, with the right conditions and
 * no hardcoded package internals.
 */
async function externalLoader() {
  const base = process.env.RELAYFILE_PROOF_MODULE_BASE?.trim();
  if (!base) {
    return {
      dispose: () => {},
      load: async (specifier) => {
        try {
          return await import(specifier);
        } catch (e) {
          unknown(
            `cannot resolve ${specifier} (${e?.code ?? "error"}). It is not a dependency of this `
            + "repo; set RELAYFILE_PROOF_MODULE_BASE to a checkout whose node_modules provides it "
            + "(ESM ignores NODE_PATH, so exporting that will not work).",
          );
        }
      },
    };
  }
  const baseDir = path.resolve(base);
  if (!fs.existsSync(path.join(baseDir, "node_modules"))) {
    unknown(`RELAYFILE_PROOF_MODULE_BASE=${baseDir} has no node_modules directory`);
  }
  const shim = path.join(baseDir, `.relayfile-455-proof-resolver-${process.pid}.mjs`);
  fs.writeFileSync(shim, "export const resolve = (s) => import(s);\n");
  const dispose = () => { try { fs.rmSync(shim, { force: true }); } catch { /* best effort */ } };
  let resolver;
  try {
    resolver = await import(pathToFileURL(shim).href);
  } catch (e) {
    dispose();
    unknown(`could not initialise the module resolver in ${baseDir}: ${e?.message ?? e}`);
  }
  return {
    dispose,
    load: async (specifier) => {
      try {
        return await resolver.resolve(specifier);
      } catch (e) {
        unknown(
          `cannot resolve ${specifier} from RELAYFILE_PROOF_MODULE_BASE=${baseDir} `
          + `(${e?.code ?? "error"}): ${e?.message ?? e}`,
        );
      }
    },
  };
}

const cfg = {
  snapshot: process.env.RELAYFILE_PROOF_SNAPSHOT
    ?? "relay-orchestrator-sdk-11.8.2-relayfile-v0.10.50-runtime-4.1.52",
  baseUrl: process.env.RELAYFILE_PROOF_BASE_URL ?? "https://file.agentrelay.com",
  relayAuthUrl: process.env.WEB_RELAYAUTH_URL ?? "https://api.relayauth.dev",
  armTimeoutMs: Number(process.env.RELAYFILE_PROOF_ARM_TIMEOUT_MS ?? 45 * 60 * 1000),
  evidenceDir: process.env.RELAYFILE_PROOF_EVIDENCE_DIR ?? "",
  // Filled by validateEnv() before any expensive work happens.
  workspaceId: "", remotePath: "", relayAuthApiKey: "", daytonaApiKey: "",
};

/**
 * Validate every credential up front, before mkdtemp and before the go build,
 * so a missing variable costs nothing and leaves no litter in os.tmpdir().
 */
function validateEnv() {
  cfg.workspaceId = requiredEnv("RELAYFILE_PROOF_WORKSPACE_ID");
  cfg.remotePath = requiredEnv("RELAYFILE_PROOF_REMOTE_PATH");
  cfg.relayAuthApiKey = requiredEnv("WEB_RELAYAUTH_API_KEY");
  cfg.daytonaApiKey = requiredEnv("DAYTONA_API_KEY");
  if (!Number.isFinite(cfg.armTimeoutMs) || cfg.armTimeoutMs <= 0) {
    unknown("RELAYFILE_PROOF_ARM_TIMEOUT_MS must be a positive number of milliseconds");
  }
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

const shq = (s) => `'${String(s).replace(/'/g, `'\\''`)}'`;

/**
 * Where THIS arm's public state file lives. Derived from the sandbox
 * package's own layout resolver — the same computation the guard uses to pick
 * the file it reads — so the harness cannot drift from the guard. Under the
 * exact layout a scoped mount's root is localDir + remoteRoot, NOT localDir,
 * so a plain `${localDir}/.relay/state.json` would read a non-existent file
 * and score the arm on a missing bootstrap key.
 */
function stateFileFor(sandboxPkg, localDir) {
  const { mountLocalDirs } = sandboxPkg.resolveRelayfileMountExactLayout({
    localDir,
    paths: [cfg.remotePath],
  });
  if (mountLocalDirs.length !== 1) {
    unknown(`expected exactly one mount root for ${cfg.remotePath}, got ${mountLocalDirs.length}`);
  }
  return `${mountLocalDirs[0].replace(/\/+$/u, "")}/.relay/state.json`;
}

/** Credential failures make an arm's exit code meaningless. */
const AUTH_FAILURE = /\b(401|403)\b|missing required scope|token is required|unauthor/i;

/**
 * Run one arm to completion and return its guard exit code plus the raw
 * .relay/state.json bytes. `binDir`, when set, is prepended to PATH so the
 * same generated command resolves to a different relayfile-mount.
 *
 * Each arm gets a FRESHLY MINTED token. The arms run sequentially and each may
 * take the full arm timeout, so one shared one-hour credential could expire
 * mid-bootstrap in arm B and turn an interrupted run into a false REFUTED.
 */
async function runArm(sandbox, sandboxPkg, mintToken, { arm, binDir }) {
  const token = await mintToken(arm);
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
  const launcher = sandboxPkg.buildRelayfileMountInitialSyncBackgroundShell(opts, { runId: `arm${arm}` });
  const b64 = (s) => Buffer.from(s, "utf8").toString("base64");

  await exec(sandbox, [
    "set -u",
    `rm -rf ${shq(localDir)} ${shq(opts.stateDir)}`,
    `rm -f /tmp/relayfile-initial-sync.*.arm${arm}`,
    `mkdir -p ${shq(localDir)} ${shq(opts.stateDir)}`,
    "umask 077",
    `echo ${b64(JSON.stringify({ token }))} | base64 -d > ${shq(credsFilePath)}`,
    `chmod 600 ${shq(credsFilePath)}`,
    `echo ${b64(launcher)} | base64 -d > /home/daytona/bg-${arm}.sh`,
    `${binDir ? `PATH=${shq(binDir)}:$PATH ` : ""}sh /home/daytona/bg-${arm}.sh`,
  ].join("\n"));

  const exitSentinel = `/tmp/relayfile-initial-sync.exit.arm${arm}`;
  const deadline = Date.now() + cfg.armTimeoutMs;
  while (Date.now() < deadline) {
    const s = await exec(sandbox, `if [ -f ${exitSentinel} ]; then cat ${exitSentinel}; else echo running; fi`, 60);
    const v = s.output.trim();
    if (v !== "running" && v !== "") {
      return { ...(await collect(sandbox, sandboxPkg, arm, localDir)), guardExit: Number(v) };
    }
    await new Promise((r) => setTimeout(r, 20_000));
  }
  // A timeout is UNKNOWN, never a pass.
  return { ...(await collect(sandbox, sandboxPkg, arm, localDir)), guardExit: null, timedOut: true };
}

async function collect(sandbox, sandboxPkg, arm, localDir) {
  const stateFile = stateFileFor(sandboxPkg, localDir);
  const [state, files, log] = await Promise.all([
    exec(sandbox, `cat ${shq(stateFile)} 2>/dev/null || echo '{"__missing":true}'`, 120),
    exec(sandbox, `find ${shq(localDir)} -type f -not -path '*/.relay/*' | wc -l`, 120),
    exec(sandbox, `cat /tmp/relayfile-initial-sync.log.arm${arm} 2>/dev/null`, 120),
  ]);
  let parsed = null;
  try { parsed = JSON.parse(state.output); } catch { /* keep raw */ }
  return {
    arm,
    stateFile,
    stateRaw: state.output,
    state: parsed,
    stateMissing: Boolean(parsed?.__missing),
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
    stateFile: r.stateFile,
    stateMissing: r.stateMissing,
    mirroredFiles: r.mirroredFiles,
    bootstrapNull: b == null,
    filesSynced: b?.filesSynced ?? null,
    traversalIncomplete: /traversal_complete=false/.test(r.log),
    budgetReached: /bootstrap file budget reached/.test(r.log),
    authFailed: AUTH_FAILURE.test(r.log),
    lastSuccessfulReconcileAt: r.state?.lastSuccessfulReconcileAt ?? null,
    stateBytes: r.stateRaw.length,
  };
}

/**
 * Decide the verdict. Deliberately conservative: everything that is not a
 * clean pass or a clean refutation is UNKNOWN.
 */
export function verdict(A, B) {
  if (A.timedOut || B.timedOut) {
    return [EXIT_UNKNOWN, "UNKNOWN: an arm timed out; a timeout is not a pass"];
  }
  if (A.authFailed || B.authFailed) {
    return [EXIT_UNKNOWN,
      "UNKNOWN: an arm hit a credential failure (401/403/expired token), so its exit code says nothing about the bootstrap budget"];
  }
  if (A.stateMissing) {
    return [EXIT_UNKNOWN, `UNKNOWN: arm A left no state file at ${A.stateFile}`];
  }
  // The must-fail control is the whole point, and exit 75 alone does not
  // establish it: the guard returns 75 for any incomplete or unreadable
  // state, including one where nothing ever synced.
  if (A.guardExit !== READINESS_INCOMPLETE) {
    return [EXIT_UNKNOWN,
      `INCONCLUSIVE: arm A exited ${A.guardExit}, expected ${READINESS_INCOMPLETE}. `
      + `It mirrored ${A.mirroredFiles} files; the mechanism needs a subtree larger than `
      + `${BOOTSTRAP_MAX_FILES_PER_CYCLE}. Point RELAYFILE_PROOF_REMOTE_PATH at a bigger tree.`];
  }
  if (A.bootstrapNull) {
    return [EXIT_UNKNOWN,
      "INCONCLUSIVE: arm A exited 75 but its state has no bootstrap block, so the 75 came from "
      + "something other than an incomplete bootstrap (an unreadable state or a failure before "
      + "the first cycle). This is not a valid must-fail control."];
  }
  if (!(A.filesSynced >= BOOTSTRAP_MAX_FILES_PER_CYCLE)) {
    return [EXIT_UNKNOWN,
      `INCONCLUSIVE: arm A's bootstrap stopped at filesSynced=${A.filesSynced}, below the `
      + `${BOOTSTRAP_MAX_FILES_PER_CYCLE}-file per-cycle budget. It yielded for some other reason, `
      + "so it does not demonstrate the budget mechanism."];
  }
  if (!A.traversalIncomplete || !A.budgetReached) {
    return [EXIT_UNKNOWN,
      "INCONCLUSIVE: arm A's log does not report both 'bootstrap file budget reached' and "
      + "traversal_complete=false, so the control did not provably hit the per-cycle budget."];
  }
  // Only now is arm A a valid control, and only now may arm B be judged.
  if (B.stateMissing) {
    return [EXIT_UNKNOWN, `UNKNOWN: arm B left no state file at ${B.stateFile}`];
  }
  if (B.guardExit !== 0 || !B.bootstrapNull) {
    return [EXIT_PROOF_REFUTED,
      `REFUTED: arm B exited ${B.guardExit} with bootstrap${B.bootstrapNull ? " null" : " still non-null"} `
      + `after mirroring ${B.mirroredFiles} files. The fix does not cure #455 on the real path.`];
  }
  return [EXIT_PROOF_HELD,
    `PROOF HELD: arm A exit ${A.guardExit} (bootstrap non-null at filesSynced ${A.filesSynced}, `
    + `budget reached, traversal_complete=false), arm B exit ${B.guardExit} `
    + `(bootstrap null, ${B.mirroredFiles} files mirrored).`];
}

async function main() {
  let scratch;
  let sandbox;
  let disposeResolver = () => {};
  try {
    // Cheap and total: validate every credential before spending a build on it.
    validateEnv();

    const [sandboxPkg, daytonaMod, relayfileClient] = await (async () => {
      const loader = await externalLoader();
      disposeResolver = loader.dispose;
      return Promise.all([
        loader.load("@agent-relay/sandbox/core"),
        loader.load("@daytonaio/sdk"),
        loader.load("@cloud/core/relayfile/client.js"),
      ]);
    })();
    const { Daytona } = daytonaMod;

  const mintToken = async (arm) => {
    const { accessToken } = await relayfileClient.mintRelayfileTokenPair({
      workspaceId: cfg.workspaceId,
      agentName: `relayfile-455-proof-${arm}`,
      scopes: ["fs:read", "fs:write", "sync:read", "sync:trigger"],
      relayAuthUrl: cfg.relayAuthUrl,
      relayAuthApiKey: cfg.relayAuthApiKey,
      ttlSeconds: 3600,
      refreshTokenTtlSeconds: relayfileClient.MAX_RETAINED_REFRESH_TOKEN_TTL_SECONDS,
    });
    return accessToken;
  };

    scratch = fs.mkdtempSync(path.join(os.tmpdir(), "relayfile-455-proof-"));
    const candidate = buildCandidateMount(scratch);

    const daytona = new Daytona({ apiKey: cfg.daytonaApiKey });
    try {
      sandbox = await daytona.create({ snapshot: cfg.snapshot });
    } catch (e) {
      unknown(`could not provision a Daytona sandbox: ${e?.message ?? e}`);
    }

    await sandbox.fs.uploadFile(fs.readFileSync(candidate), "/home/daytona/relayfile-mount-candidate");
    await exec(sandbox, [
      "mkdir -p /home/daytona/binB",
      "cp /home/daytona/relayfile-mount-candidate /home/daytona/binB/relayfile-mount",
      "chmod +x /home/daytona/binB/relayfile-mount",
    ].join("\n"));

    // arm A: the snapshot's own binary, on the default PATH.
    const a = await runArm(sandbox, sandboxPkg, mintToken, { arm: "A" });
    // arm B: the candidate, resolved first on PATH.
    const b = await runArm(sandbox, sandboxPkg, mintToken, { arm: "B", binDir: "/home/daytona/binB" });

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

    const [code, message] = verdict(A, B);
    (code === EXIT_PROOF_HELD ? console.log : console.error)(message);
    return code;
  } catch (e) {
    // Infrastructure failure is UNKNOWN, never REFUTED. Exit 1 means "the fix
    // does not work"; a network blip or a failed upload must never claim that.
    if (e instanceof HarnessUnknown) console.error(`UNKNOWN: ${e.message}`);
    else console.error(`UNKNOWN: harness failed before reaching a verdict: ${e?.stack ?? e}`);
    return EXIT_UNKNOWN;
  } finally {
    if (sandbox) await sandbox.delete().catch(() => {});
    if (scratch) fs.rmSync(scratch, { recursive: true, force: true });
    disposeResolver();
  }
}

// Only run when executed directly; importing this module (e.g. from the
// verdict test) must not provision anything.
if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  process.exit(await main());
}
