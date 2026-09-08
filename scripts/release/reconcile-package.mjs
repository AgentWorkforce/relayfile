#!/usr/bin/env node
/**
 * Reconcile one npm package version before publishing it.
 *
 * A release may be retried after a matrix job has published some packages.
 * npm versions are immutable, so the only safe choices are:
 *
 *   absent     -> publish the local tarball
 *   identical  -> skip (the previous attempt published this exact tarball)
 *   conflict   -> fail closed
 *   ambiguous  -> fail closed (the registry could not be queried reliably)
 *
 * The local tarball is packed once and that exact file is passed to npm
 * publish.  The resulting record is used by the release attestation.
 */

import { createHash } from "node:crypto";
import { execFile } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { basename, resolve } from "node:path";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";
import {
  isSha1Shasum,
  isSha512Integrity,
} from "./create-release-attestation.mjs";

export const DEFAULT_ATTEMPTS = 10;
export const DEFAULT_DELAY_MS = 5000;
// Match lockfile propagation retries: no individual pause exceeds 30 seconds.
export const MAX_DELAY_MS = 30000;
// Bound each npm view, including npm's own retry loop, so a stalled registry
// command cannot sit outside the post-publish retry budget.
export const REGISTRY_QUERY_TIMEOUT_MS = 30000;
export const REGISTRY_FETCH_RETRIES = 1;
export const REGISTRY_FETCH_RETRY_MIN_TIMEOUT_MS = 1000;
export const REGISTRY_FETCH_RETRY_MAX_TIMEOUT_MS = 5000;
// Keep post-publish registry verification bounded when npm stays unavailable.
export const MAX_TOTAL_RETRY_DELAY_MS = 5 * 60 * 1000;

export function backoffDelay({
  attempt,
  baseDelayMs = DEFAULT_DELAY_MS,
  maxDelayMs = MAX_DELAY_MS,
}) {
  return Math.min(baseDelayMs * 2 ** (attempt - 1), maxDelayMs);
}

function run(
  command,
  args,
  { cwd = process.cwd(), env = process.env, timeout } = {},
) {
  return new Promise((resolveResult) => {
    execFile(
      command,
      args,
      { cwd, env, maxBuffer: 32 * 1024 * 1024, timeout },
      (error, stdout, stderr) => {
        resolveResult({
          code: error ? (typeof error.code === "number" ? error.code : 1) : 0,
          stdout: String(stdout ?? ""),
          stderr: String(stderr ?? ""),
        });
      },
    );
  });
}

function parseJsonOutput(output, description) {
  const text = String(output ?? "").trim();
  try {
    return JSON.parse(text);
  } catch {
    // npm can print warnings before JSON even with --json. Parse the last JSON
    // array/object so a warning cannot turn an otherwise valid pack into an
    // ambiguous release record.
    for (let start = text.length - 1; start >= 0; start -= 1) {
      if (text[start] !== "[" && text[start] !== "{") continue;
      try {
        return JSON.parse(text.slice(start));
      } catch {
        // Keep searching for an earlier JSON boundary.
      }
    }
  }
  throw new Error(`could not parse ${description}; refusing to release`);
}

export function sha256File(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

export function normalizePackRecord(raw, packageDir) {
  const record = Array.isArray(raw)
    ? raw[0]
    : raw?.filename
      ? raw
      : raw && typeof raw === "object"
        ? Object.values(raw)[0]
        : raw;
  if (
    !record ||
    typeof record !== "object" ||
    typeof record.filename !== "string"
  ) {
    throw new Error("npm pack returned no tarball; refusing to release");
  }
  const filename = resolve(packageDir, record.filename);
  if (!existsSync(filename))
    throw new Error(`npm pack output is missing: ${basename(filename)}`);
  const integrity =
    typeof record.integrity === "string" ? record.integrity : null;
  const shasum = typeof record.shasum === "string" ? record.shasum : null;
  if (!integrity && !shasum) {
    throw new Error(
      "npm pack returned no integrity or shasum; refusing to release",
    );
  }
  if (integrity && !isSha512Integrity(integrity)) {
    throw new Error(
      "npm pack returned malformed SHA-512 integrity; refusing to release",
    );
  }
  if (shasum && !isSha1Shasum(shasum)) {
    throw new Error(
      "npm pack returned malformed SHA-1 shasum; refusing to release",
    );
  }
  return {
    filename,
    name: record.name,
    version: record.version,
    size: Number(record.size) || readFileSync(filename).byteLength,
    integrity,
    shasum,
    sha256: sha256File(filename),
  };
}

export function registryErrorKind(result) {
  const text = `${result.stdout}\n${result.stderr}`;
  const npmCodes = [
    ...text.matchAll(/(?:^|\n)\s*npm\s+error\s+code\s+(E\d{3})\b/gi),
  ].map((match) => match[1].toUpperCase());
  if (npmCodes.includes("E404") && npmCodes.every((code) => code === "E404")) {
    return "absent";
  }
  return "ambiguous";
}

export function normalizeRegistryRecord(raw, { name, version }) {
  if (Array.isArray(raw) && raw.length !== 1) return null;
  const value = Array.isArray(raw) ? raw[0] : raw;
  const record =
    value?.dist && typeof value.dist === "object" ? value.dist : value;
  if (!record || typeof record !== "object") return null;
  if (
    (record.name !== undefined && record.name !== name) ||
    (record.version !== undefined && record.version !== version)
  ) {
    return null;
  }
  const integrity =
    typeof record.integrity === "string" ? record.integrity : null;
  const shasum = typeof record.shasum === "string" ? record.shasum : null;
  const tarball = typeof record.tarball === "string" ? record.tarball : null;
  if (!integrity && !shasum) return null;
  if (
    (integrity && !isSha512Integrity(integrity)) ||
    (shasum && !isSha1Shasum(shasum))
  ) {
    return null;
  }
  return { name, version, integrity, shasum, tarball };
}

export function comparePackageContent(local, registry) {
  if (!registry)
    return {
      kind: "ambiguous",
      reason: "registry returned no package digests",
    };
  if (
    (registry.integrity && !isSha512Integrity(registry.integrity)) ||
    (local.integrity && !isSha512Integrity(local.integrity))
  ) {
    return {
      kind: "ambiguous",
      reason:
        "registry and local content do not share a canonical SHA-512 integrity",
    };
  }
  if (
    registry.integrity &&
    local.integrity &&
    registry.integrity !== local.integrity
  ) {
    return {
      kind: "conflict",
      reason: "registry integrity differs from the local tarball",
    };
  }
  if (registry.shasum && local.shasum && registry.shasum !== local.shasum) {
    return {
      kind: "conflict",
      reason: "registry shasum differs from the local tarball",
    };
  }
  if (!registry.integrity || !local.integrity) {
    return {
      kind: "ambiguous",
      reason:
        "registry and local content do not share a comparable SHA-512 integrity",
    };
  }
  return { kind: "identical" };
}

async function queryRegistry({
  name,
  version,
  cwd,
  npm = run,
  timeoutMs = REGISTRY_QUERY_TIMEOUT_MS,
}) {
  const result = await npm(
    "npm",
    [
      "view",
      `${name}@${version}`,
      "dist",
      "--json",
      "--prefer-online",
      "--no-fund",
      `--fetch-retries=${REGISTRY_FETCH_RETRIES}`,
      `--fetch-retry-mintimeout=${REGISTRY_FETCH_RETRY_MIN_TIMEOUT_MS}`,
      `--fetch-retry-maxtimeout=${REGISTRY_FETCH_RETRY_MAX_TIMEOUT_MS}`,
    ],
    { cwd, timeout: timeoutMs },
  );
  if (result.code !== 0) {
    const kind = registryErrorKind(result);
    if (kind === "absent") return { kind: "absent" };
    throw new Error(
      `registry query for ${name}@${version} was ambiguous; refusing to release`,
    );
  }
  let raw;
  try {
    raw = parseJsonOutput(
      result.stdout,
      `registry metadata for ${name}@${version}`,
    );
  } catch (error) {
    throw error;
  }
  const record = normalizeRegistryRecord(raw, { name, version });
  if (!record)
    throw new Error(
      `registry metadata for ${name}@${version} has no usable digest; refusing to release`,
    );
  return { kind: "present", record };
}

async function packPackage({ packageDir, npm = run }) {
  const result = await npm("npm", ["pack", "--json", "--ignore-scripts"], {
    cwd: packageDir,
  });
  if (result.code !== 0) {
    throw new Error("npm pack failed; refusing to release");
  }
  return normalizePackRecord(
    parseJsonOutput(result.stdout, "npm pack output"),
    packageDir,
  );
}

function parseArgs(argv) {
  const values = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith("--")) throw new Error(`unexpected argument: ${arg}`);
    const key = arg.slice(2).replaceAll("-", "_");
    values[key] = argv[++i];
  }
  return values;
}

export async function reconcilePackage({
  packageDir,
  tag,
  dryRun = false,
  preflight = false,
  sourceSha,
  runId,
  runAttempt,
  output,
  npm = run,
  sleep = (ms) => new Promise((resolveSleep) => setTimeout(resolveSleep, ms)),
  attempts = DEFAULT_ATTEMPTS,
  delayMs = DEFAULT_DELAY_MS,
  maxDelayMs = MAX_DELAY_MS,
  maxTotalRetryDelayMs = MAX_TOTAL_RETRY_DELAY_MS,
  registryQueryTimeoutMs = REGISTRY_QUERY_TIMEOUT_MS,
  now = () => performance.now(),
}) {
  const manifest = JSON.parse(
    readFileSync(resolve(packageDir, "package.json"), "utf8"),
  );
  const name = manifest.name;
  const version = manifest.version;
  if (!name || !version)
    throw new Error(
      "package.json is missing name/version; refusing to release",
    );

  const local = await packPackage({ packageDir, npm });
  if (local.name && local.name !== name)
    throw new Error("npm pack name disagrees with package.json");
  if (local.version && local.version !== version)
    throw new Error("npm pack version disagrees with package.json");

  let status = "dry-run";
  let registry = null;
  if (!dryRun) {
    const current = await queryRegistry({
      name,
      version,
      cwd: packageDir,
      npm,
      timeoutMs: registryQueryTimeoutMs,
    });
    if (current.kind === "present") {
      const comparison = comparePackageContent(local, current.record);
      if (comparison.kind === "conflict") {
        throw new Error(
          `${name}@${version} conflicts with the local release tarball: ${comparison.reason}`,
        );
      }
      if (comparison.kind === "ambiguous") {
        throw new Error(
          `${name}@${version} cannot be reconciled: ${comparison.reason}`,
        );
      }
      status = "already-published";
      registry = current.record;
    } else if (preflight) {
      // A preflight is deliberately read-only.  The workflow runs every
      // package through this branch before it permits any publish job to
      // start, so one conflict cannot race an absent package into a partial
      // release.  The normal invocation re-queries immediately before its
      // publish, retaining the race-safe check after the barrier.
      status = "absent";
    } else {
      const published = await npm(
        "npm",
        [
          "publish",
          local.filename,
          "--access",
          "public",
          "--provenance",
          "--tag",
          tag,
          "--ignore-scripts",
        ],
        { cwd: packageDir },
      );
      if (published.code !== 0) {
        throw new Error(
          `npm publish failed for ${name}@${version}; refusing to continue`,
        );
      }
      status = "published";

      let lastError;
      let consumedRetryBudgetMs = 0;
      for (let attempt = 1; attempt <= attempts; attempt += 1) {
        const remainingQueryBudgetMs = Math.max(
          0,
          maxTotalRetryDelayMs - consumedRetryBudgetMs,
        );
        if (remainingQueryBudgetMs === 0) {
          lastError = new Error("registry retry budget exhausted");
          break;
        }
        const queryStartedAt = now();
        let after;
        let queryError;
        try {
          after = await queryRegistry({
            name,
            version,
            cwd: packageDir,
            npm,
            timeoutMs: Math.min(registryQueryTimeoutMs, remainingQueryBudgetMs),
          });
        } catch (error) {
          queryError = error;
        }
        consumedRetryBudgetMs += Math.min(
          remainingQueryBudgetMs,
          Math.max(0, now() - queryStartedAt),
        );
        try {
          if (queryError) throw queryError;
          if (after.kind !== "present")
            throw new Error("registry still reports the package as absent");
          const comparison = comparePackageContent(local, after.record);
          if (comparison.kind === "identical") {
            registry = after.record;
            lastError = null;
            break;
          }
          const error = new Error(comparison.reason);
          error.fatal = comparison.kind === "conflict";
          throw error;
        } catch (error) {
          lastError = error;
          if (error.fatal || attempt === attempts) break;
          const remainingDelayMs = Math.max(
            0,
            maxTotalRetryDelayMs - consumedRetryBudgetMs,
          );
          const delay = Math.min(
            backoffDelay({ attempt, baseDelayMs: delayMs, maxDelayMs }),
            remainingDelayMs,
          );
          if (delay === 0) break;
          const sleepStartedAt = now();
          await sleep(delay);
          consumedRetryBudgetMs += Math.min(
            remainingDelayMs,
            Math.max(delay, now() - sleepStartedAt),
          );
        }
      }
      if (!registry) {
        throw new Error(
          `post-publish verification failed for ${name}@${version}: ${lastError?.message ?? "unknown registry response"}`,
        );
      }
    }
  }

  const attestation = {
    schemaVersion: 1,
    kind: "relayfileReleasePackage",
    package: {
      name,
      version,
      status,
      local: {
        file: basename(local.filename),
        size: local.size,
        integrity: local.integrity,
        shasum: local.shasum,
        sha256: local.sha256,
      },
      registry,
    },
    sourceSha,
    workflowRunId: String(runId ?? ""),
    workflowRunAttempt: String(runAttempt ?? ""),
  };
  if (output) {
    mkdirSync(resolve(output, ".."), { recursive: true });
    writeFileSync(output, `${JSON.stringify(attestation, null, 2)}\n`);
  }
  rmSync(local.filename, { force: true });
  return attestation;
}

let entrypoint = "";
try {
  entrypoint = process.argv[1] ? realpathSync(resolve(process.argv[1])) : "";
} catch {
  // Import contexts such as `node -` do not name a filesystem entrypoint.
}
if (entrypoint && fileURLToPath(import.meta.url) === entrypoint) {
  const args = parseArgs(process.argv.slice(2));
  try {
    await reconcilePackage({
      packageDir: resolve(args.package_dir ?? process.cwd()),
      tag: args.tag,
      dryRun: args.dry_run === "true",
      sourceSha: args.source_sha,
      runId: args.run_id,
      runAttempt: args.run_attempt,
      preflight: args.preflight === "true",
      output: args.output,
    });
  } catch (error) {
    console.error(
      error instanceof Error ? error.message : "release reconciliation failed",
    );
    process.exitCode = 1;
  }
}
