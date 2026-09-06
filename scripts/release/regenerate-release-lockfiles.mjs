#!/usr/bin/env node
/**
 * Regenerate the release lockfiles, tolerating npm registry read-after-write lag.
 *
 * Context — why this exists
 * ------------------------
 * `Create Release` runs immediately after the publish matrix goes green. But
 * `npm publish` returning success only means the registry accepted the write;
 * the packument that `npm install` reads is served from a CDN that catches up
 * seconds to minutes later. `packages/sdk/typescript` keeps its own lockfile
 * and resolves five `@relayfile/*` packages straight from the registry, so
 * regenerating it during that window fails:
 *
 *     npm error code ETARGET
 *     npm error notarget No matching version found for @relayfile/core@0.10.54.
 *
 * That is not a bad artifact — the version resolves fine minutes later. It has
 * killed `Create Release` three times (0.10.51, 0.10.53, 0.10.54), each time
 * *after* the packages were already public, leaving npm ahead of the repo with
 * no tag and no GitHub release.
 *
 * The rule this module enforces
 * -----------------------------
 * Wait for propagation, but never paper over a version that was genuinely never
 * published. A retry only happens when the unresolvable spec is an internal
 * `@relayfile/*` package pinned at *this* release version — i.e. something the
 * publish jobs just reported as published. Anything else (an external package,
 * or an internal package at some other version) is fatal on the first failure.
 * And if a version never appears within the budget, this exits non-zero with a
 * message that says so explicitly. There is no path here that succeeds without
 * every required version actually resolving.
 */

import { execFile } from 'node:child_process';
import { readFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

export const INTERNAL_SCOPE = '@relayfile/';

/** Manifests that pin the internal packages a release lockfile must resolve. */
export const RELEASE_MANIFESTS = [
  'package.json',
  'packages/core/package.json',
  'packages/sdk/typescript/package.json',
  'packages/client/package.json',
  'packages/agents/package.json',
  'packages/cli/package.json',
  'packages/file-observer/package.json',
  'packages/local-mount/package.json',
];

/** The four commands the release previously ran inline, in order. */
export const LOCKFILE_COMMANDS = [
  ['npm', ['install', '--package-lock-only', '--ignore-scripts']],
  ['npm', ['install', '--prefix', 'packages/sdk/typescript', '--package-lock-only', '--ignore-scripts']],
  ['npm', ['ci', '--dry-run']],
  ['npm', ['ci', '--prefix', 'packages/sdk/typescript', '--dry-run']],
];

const DEP_TYPES = ['dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies'];

/**
 * Every internal package that some release manifest pins at exactly `version`
 * (bare or caret). These are the specs that must be resolvable before a
 * lockfile can be regenerated.
 */
export function collectRequiredPackages({ version, manifests }) {
  const required = new Set();
  for (const manifest of manifests) {
    for (const depType of DEP_TYPES) {
      for (const [name, range] of Object.entries(manifest?.[depType] ?? {})) {
        if (!name.startsWith(INTERNAL_SCOPE)) continue;
        if (range === version || range === `^${version}`) required.add(name);
      }
    }
  }
  return [...required].sort();
}

export function readReleaseManifests({ paths = RELEASE_MANIFESTS, cwd = process.cwd() } = {}) {
  const manifests = [];
  for (const rel of paths) {
    const full = `${cwd}/${rel}`;
    if (!existsSync(full)) continue;
    manifests.push(JSON.parse(readFileSync(full, 'utf8')));
  }
  return manifests;
}

/** Every `name@version` npm reported as unresolvable in this output. */
export function parseUnresolvedSpecs(output) {
  const specs = [];
  const re = /No matching version found for (@?[^\s@]+(?:\/[^\s@]+)?)@([^\s.]+(?:\.[^\s.]+)*?)\.?(?:\s|$)/g;
  for (const match of String(output ?? '').matchAll(re)) {
    specs.push({ name: match[1], version: match[2] });
  }
  return specs;
}

/**
 * Decide whether an install failure is registry propagation lag (retryable) or
 * a genuinely missing version (fatal). Defaults to fatal — an unrecognised
 * failure must never be retried into a silent pass.
 */
export function classifyInstallFailure({ output, version, requiredPackages }) {
  const text = String(output ?? '');
  if (!/ETARGET|No matching version found for/.test(text)) {
    return { kind: 'fatal', reason: 'not an ETARGET failure' };
  }

  const unresolved = parseUnresolvedSpecs(text);
  if (unresolved.length === 0) {
    return { kind: 'fatal', reason: 'ETARGET reported but no unresolved spec could be parsed' };
  }

  const required = new Set(requiredPackages);
  const offenders = unresolved.filter(
    (spec) => !(required.has(spec.name) && spec.version === version),
  );
  if (offenders.length > 0) {
    const list = offenders.map((s) => `${s.name}@${s.version}`).join(', ');
    return {
      kind: 'fatal',
      reason:
        `${list} is not an internal package published by this release ` +
        `(expected one of ${[...required].join(', ')} at ${version}) — ` +
        'this version was never published, it is not propagation lag',
    };
  }

  return { kind: 'propagation', specs: unresolved };
}

const defaultSleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

export function backoffDelay({ attempt, baseDelayMs = 5000, maxDelayMs = 30000 }) {
  return Math.min(baseDelayMs * 2 ** (attempt - 1), maxDelayMs);
}

/**
 * Poll until every required package resolves at `version`. Throws — never
 * returns — if any package is still unresolvable when the budget runs out.
 */
export async function waitForRegistryPropagation({
  version,
  packages,
  probe,
  sleep = defaultSleep,
  attempts = 10,
  baseDelayMs = 5000,
  maxDelayMs = 30000,
  log = console.log,
}) {
  let pending = [...packages];
  if (pending.length === 0) return { attempts: 0 };

  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const stillPending = [];
    for (const name of pending) {
      // eslint-disable-next-line no-await-in-loop
      if (!(await probe(name, version))) stillPending.push(name);
    }
    pending = stillPending;

    if (pending.length === 0) {
      log(`[release] all ${packages.length} internal package(s) resolve at ${version} (attempt ${attempt})`);
      return { attempts: attempt };
    }

    if (attempt === attempts) break;
    const delay = backoffDelay({ attempt, baseDelayMs, maxDelayMs });
    log(
      `[release] waiting for registry propagation of ${pending.join(', ')} at ${version} ` +
        `(attempt ${attempt}/${attempts}, retrying in ${delay}ms)`,
    );
    // eslint-disable-next-line no-await-in-loop
    await sleep(delay);
  }

  throw new Error(
    `Registry never served ${pending.map((n) => `${n}@${version}`).join(', ')} after ${attempts} attempts. ` +
      'These versions appear to have never been published — this is not propagation lag. ' +
      'Check whether the publish jobs actually succeeded before re-running Create Release.',
  );
}

/**
 * Run the lockfile commands, retrying a command only when it failed because an
 * internal package of this release is not yet visible.
 */
export async function regenerateReleaseLockfiles({
  version,
  requiredPackages,
  commands = LOCKFILE_COMMANDS,
  run,
  probe,
  sleep = defaultSleep,
  attempts = 10,
  baseDelayMs = 5000,
  maxDelayMs = 30000,
  log = console.log,
}) {
  await waitForRegistryPropagation({
    version, packages: requiredPackages, probe, sleep, attempts, baseDelayMs, maxDelayMs, log,
  });

  for (const [command, args] of commands) {
    let lastFailure;
    let succeeded = false;

    for (let attempt = 1; attempt <= attempts; attempt += 1) {
      log(`[release] $ ${command} ${args.join(' ')} (attempt ${attempt}/${attempts})`);
      // eslint-disable-next-line no-await-in-loop
      const result = await run(command, args);
      if (result.code === 0) { succeeded = true; break; }

      const output = `${result.stdout ?? ''}\n${result.stderr ?? ''}`;
      const verdict = classifyInstallFailure({ output, version, requiredPackages });
      if (verdict.kind === 'fatal') {
        throw new Error(
          `${command} ${args.join(' ')} failed and will not be retried: ${verdict.reason}\n${output.trim()}`,
        );
      }

      lastFailure = output;
      if (attempt === attempts) break;
      const delay = backoffDelay({ attempt, baseDelayMs, maxDelayMs });
      log(
        `[release] ${verdict.specs.map((s) => `${s.name}@${s.version}`).join(', ')} not visible yet; ` +
          `retrying in ${delay}ms`,
      );
      // eslint-disable-next-line no-await-in-loop
      await sleep(delay);
    }

    if (!succeeded) {
      throw new Error(
        `${command} ${args.join(' ')} still failed with ETARGET after ${attempts} attempts. ` +
          `The required versions of ${requiredPackages.join(', ')} at ${version} never became installable.\n` +
          String(lastFailure ?? '').trim(),
      );
    }
  }
}

/* ------------------------------- real I/O -------------------------------- */

function execCapture(command, args, { cwd = process.cwd() } = {}) {
  return new Promise((resolve) => {
    const child = execFile(command, args, { cwd, maxBuffer: 32 * 1024 * 1024 }, (error, stdout, stderr) => {
      resolve({ code: error ? (error.code ?? 1) : 0, stdout, stderr });
    });
    child.stdout?.pipe(process.stdout);
    child.stderr?.pipe(process.stderr);
  });
}

/**
 * `--prefer-online` revalidates the runner's local npm cache, which otherwise
 * happily serves the pre-publish packument it warmed during the build job.
 */
async function npmProbe(name, version) {
  const { code } = await execCapture('npm', [
    'view', `${name}@${version}`, 'version', '--prefer-online', '--no-fund',
  ]);
  return code === 0;
}

async function main() {
  const version = process.argv[2] || process.env.RELEASE_VERSION;
  if (!version) {
    console.error('usage: regenerate-release-lockfiles.mjs <version>   (or set RELEASE_VERSION)');
    process.exit(2);
  }

  const manifests = readReleaseManifests();
  const requiredPackages = collectRequiredPackages({ version, manifests });
  console.log(
    `[release] v${version}; internal packages that must resolve: ` +
      `${requiredPackages.length ? requiredPackages.join(', ') : '(none)'}`,
  );

  await regenerateReleaseLockfiles({
    version,
    requiredPackages,
    run: execCapture,
    probe: npmProbe,
  });
  console.log('[release] lockfiles regenerated');
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  main().catch((error) => {
    console.error(`[release] ${error.message}`);
    process.exit(1);
  });
}
