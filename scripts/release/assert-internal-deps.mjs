#!/usr/bin/env node
/**
 * Fail-closed publish gate for internal @relayfile/* dependencies.
 *
 * The publish matrix runs in parallel, so nothing orders a dependent after the
 * package it pins.  On 2026-10-06 (run 37396747701, attempt 1) @relayfile/sdk
 * and every other dependent published while @relayfile/core@0.10.73 failed its
 * post-publish verification, leaving `npm install` of sdk@latest unresolvable
 * (ETARGET) until core was re-run.
 *
 * Before a package may be published, every internal dependency it pins
 * (dependencies, optionalDependencies, peerDependencies) must either
 *
 *   - already resolve on the registry (`npm view name@spec version`), or
 *   - be produced by this same release (a pin on a release-set package in
 *     bare, ^, ~ or = form of the release version, per rangeTargetsVersion),
 *     in which case we WAIT for it to appear on the registry, because
 *     publishing first would reopen the gap.
 *
 * Anything else, or a wait that times out, is a hard failure: a dependent is
 * never published unless its dependencies are verifiably installable.
 */

import { execFile } from "node:child_process";
import { existsSync, readFileSync, realpathSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { RELEASE_PACKAGE_PATHS } from "./resolve-release-baseline.mjs";
import { rangeTargetsVersion } from "./regenerate-release-lockfiles.mjs";

export const INTERNAL_SCOPE = "@relayfile/";
export const DEP_FIELDS = [
  "dependencies",
  "optionalDependencies",
  "peerDependencies",
];
export const DEFAULT_TIMEOUT_MS = 20 * 60 * 1000;
export const DEFAULT_POLL_MS = 15 * 1000;

/** @returns {{name:string, spec:string, field:string}[]} */
export function internalDeps(pkg) {
  const out = [];
  for (const field of DEP_FIELDS) {
    for (const [name, spec] of Object.entries(pkg[field] ?? {})) {
      if (name.startsWith(INTERNAL_SCOPE)) out.push({ name, spec, field });
    }
  }
  return out;
}

/** name -> version for every package this release publishes. */
export function loadReleaseSet(repoRoot) {
  const set = new Map();
  for (const rel of RELEASE_PACKAGE_PATHS) {
    if (rel === "package.json") continue;
    const file = join(repoRoot, rel);
    if (!existsSync(file)) continue;
    const pkg = JSON.parse(readFileSync(file, "utf8"));
    if (pkg.name) set.set(pkg.name, pkg.version);
  }
  return set;
}

export function npmViewResolves(name, spec) {
  return new Promise((done) => {
    execFile(
      "npm",
      ["view", `${name}@${spec}`, "version", "--json"],
      { timeout: 30000, env: { ...process.env, NPM_CONFIG_FETCH_RETRIES: "1" } },
      (error, stdout) => done(!error && stdout.trim().length > 0),
    );
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/**
 * @returns {Promise<{ok:boolean, problems:string[]}>}
 */
export async function assertInternalDeps({
  pkg,
  releaseSet,
  dryRun = false,
  resolves = npmViewResolves,
  timeoutMs = DEFAULT_TIMEOUT_MS,
  pollMs = DEFAULT_POLL_MS,
  now = () => Date.now(),
  wait = sleep,
  log = () => {},
}) {
  const problems = [];
  for (const { name, spec, field } of internalDeps(pkg)) {
    const inSet = rangeTargetsVersion(spec, releaseSet.get(name) ?? "");
    if (await resolves(name, spec)) {
      log(`ok ${name}@${spec} (${field}) is on the registry`);
      continue;
    }
    if (!inSet) {
      problems.push(
        `${pkg.name}@${pkg.version} pins ${name}@${spec} (${field}), which is not on the registry and is not published by this release`,
      );
      continue;
    }
    if (dryRun) {
      log(`ok ${name}@${spec} (${field}) is in this release set (dry run, not waiting)`);
      continue;
    }
    const deadline = now() + timeoutMs;
    let found = false;
    while (now() < deadline) {
      log(`waiting for ${name}@${spec} to appear on the registry`);
      await wait(pollMs);
      if (await resolves(name, spec)) {
        found = true;
        break;
      }
    }
    if (found) log(`ok ${name}@${spec} (${field}) appeared on the registry`);
    else
      problems.push(
        `${pkg.name}@${pkg.version} must not publish: ${name}@${spec} (${field}) did not appear on the registry within ${Math.round(timeoutMs / 1000)}s`,
      );
  }
  return { ok: problems.length === 0, problems };
}

function parseArgs(argv) {
  const values = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith("--")) throw new Error(`unexpected argument ${arg}`);
    values[arg.slice(2)] = argv[++i];
  }
  return values;
}

let entrypoint = "";
try {
  entrypoint = process.argv[1] ? realpathSync(resolve(process.argv[1])) : "";
} catch {
  entrypoint = "";
}
if (entrypoint && entrypoint === realpathSync(fileURLToPath(import.meta.url))) {
  const args = parseArgs(process.argv.slice(2));
  if (!args["package-dir"]) throw new Error("--package-dir is required");
  const repoRoot = resolve(args["repo-root"] ?? process.cwd());
  const pkg = JSON.parse(
    readFileSync(join(resolve(args["package-dir"]), "package.json"), "utf8"),
  );
  const result = await assertInternalDeps({
    pkg,
    releaseSet: loadReleaseSet(repoRoot),
    dryRun: args["dry-run"] === "true",
    timeoutMs: args["timeout-ms"]
      ? Number(args["timeout-ms"])
      : DEFAULT_TIMEOUT_MS,
    log: (m) => console.log(m),
  });
  if (!result.ok) {
    for (const p of result.problems) console.error(`::error::${p}`);
    process.exit(1);
  }
}
