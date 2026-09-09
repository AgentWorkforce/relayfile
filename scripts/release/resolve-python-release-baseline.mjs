#!/usr/bin/env node
/** Resolve a Python SDK release baseline from attested, completed releases. */

import { execFileSync } from "node:child_process";

export const PYTHON_RELEASE_TAG_PREFIX = "sdk-python-v";

// The Python workflow emits this canonical subset of PEP 440.  In
// particular, leading-zero release/prerelease numbers and non-canonical
// separators are rejected instead of allowing a forged high tag to win.
const PEP440 = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:(a|b|rc)(0|[1-9]\d*))?$/;
const SHA = /^[0-9a-f]{40}$/;

export function parseStrictPep440(value) {
  const match = String(value ?? "").match(PEP440);
  if (!match) return null;
  return {
    raw: match[0],
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
    phase: match[4] ?? "final",
    phaseNumber: match[5] === undefined ? Number.POSITIVE_INFINITY : Number(match[5]),
  };
}

const PHASE_ORDER = { a: 0, b: 1, rc: 2, final: 3 };

export function comparePep440(a, b) {
  const left = typeof a === "string" ? parseStrictPep440(a) : a;
  const right = typeof b === "string" ? parseStrictPep440(b) : b;
  if (!left || !right) throw new Error("cannot compare invalid PEP 440 versions");
  for (const key of ["major", "minor", "patch"]) {
    if (left[key] !== right[key]) return left[key] - right[key];
  }
  const phase = PHASE_ORDER[left.phase] - PHASE_ORDER[right.phase];
  return phase || left.phaseNumber - right.phaseNumber;
}

function git(cwd, args, allowFailure = false) {
  try {
    return execFileSync("git", ["-C", cwd, ...args], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", allowFailure ? "pipe" : "pipe"],
    }).trim();
  } catch (error) {
    if (allowFailure) return null;
    throw error;
  }
}

function tagMetadata(cwd, tag) {
  const raw = git(cwd, ["cat-file", "-p", `refs/tags/${tag}`], true);
  if (!raw) return null;
  const separator = raw.indexOf("\n\n");
  if (separator < 0) return null;
  const fields = {};
  for (const line of raw.slice(separator + 2).split(/\r?\n/)) {
    const match = line.match(/^([a-z][a-z-]+)=(.+)$/);
    if (match) fields[match[1]] = match[2].trim();
  }
  return fields;
}

function completedRelease({ repository, tag, env }) {
  try {
    const json = execFileSync(
      "gh",
      ["api", `repos/${repository}/releases/tags/${tag}`],
      { encoding: "utf8", env: { ...process.env, ...env } },
    );
    const release = JSON.parse(json);
    return release.draft !== true && typeof release.published_at === "string";
  } catch (error) {
    const stderr = String(error.stderr ?? "");
    if (/HTTP 404/.test(stderr)) return false;
    throw error;
  }
}

function trustedTag(cwd, tag, sourceSha) {
  const version = parseStrictPep440(tag.slice(PYTHON_RELEASE_TAG_PREFIX.length));
  if (!version) return null;
  if (git(cwd, ["cat-file", "-t", `refs/tags/${tag}`], true) !== "tag") return null;
  const commit = git(cwd, ["rev-parse", `refs/tags/${tag}^{commit}`], true);
  if (!commit || !SHA.test(commit)) return null;
  if (git(cwd, ["merge-base", "--is-ancestor", commit, sourceSha], true) === null) {
    return null;
  }
  const firstParentLineage = git(cwd, ["rev-list", "--first-parent", sourceSha], true);
  if (!firstParentLineage || !firstParentLineage.split(/\s+/).includes(commit)) return null;
  const metadata = tagMetadata(cwd, tag);
  if (!metadata || metadata["source-sha"] !== commit) return null;
  if (!SHA.test(metadata["source-sha"])) return null;
  if (!/^[0-9a-f]{40}$/.test(metadata["tag-tree"] ?? "")) return null;
  if (git(cwd, ["rev-parse", `${commit}^{tree}`], true) !== metadata["tag-tree"]) return null;
  if (!/^\d+$/.test(metadata["workflow-run-id"] ?? "")) return null;
  if (!/^\d+$/.test(metadata["workflow-run-attempt"] ?? "")) return null;
  return { tag, version, commit, metadata };
}

export function findTrustedPythonReleaseTags({ cwd = process.cwd(), sourceSha }) {
  if (!SHA.test(sourceSha ?? "")) return [];
  const tags = git(cwd, [
    "for-each-ref",
    "--format=%(refname:strip=2)",
    "refs/tags/sdk-python-v*",
  ], true);
  if (!tags) return [];
  return tags
    .split(/\s+/)
    .map((tag) => trustedTag(cwd, tag, sourceSha))
    .filter(Boolean)
    .sort((a, b) => comparePep440(a.version, b.version));
}

export function resolvePythonReleaseBaseline({
  cwd = process.cwd(),
  sourceSha,
  currentVersion,
  repository = "AgentWorkforce/relayfile",
  releaseVerifier = completedRelease,
  verifierEnv = {},
}) {
  const current = parseStrictPep440(currentVersion);
  if (!current) throw new Error("current Python package version is not strict PEP 440");
  const candidates = findTrustedPythonReleaseTags({ cwd, sourceSha });
  let latest = null;
  for (const candidate of candidates.slice().reverse()) {
    let complete = false;
    try {
      complete = releaseVerifier({
        repository,
        tag: candidate.tag,
        candidate,
        env: verifierEnv,
      });
    } catch {
      complete = false;
    }
    if (complete) {
      latest = candidate;
      break;
    }
  }
  return {
    baselineVersion: latest && comparePep440(latest.version, current) > 0
      ? latest.version.raw
      : current.raw,
    latestTag: latest?.tag ?? "",
  };
}

function parseArgs(argv) {
  const args = {};
  for (let i = 0; i < argv.length; i += 1) {
    const value = argv[i];
    if (!value.startsWith("--")) throw new Error(`unexpected argument: ${value}`);
    args[value.slice(2).replaceAll("-", "_")] = argv[++i];
  }
  return args;
}

if (process.argv[1]?.endsWith("resolve-python-release-baseline.mjs")) {
  try {
    const args = parseArgs(process.argv.slice(2));
    const result = resolvePythonReleaseBaseline({
      cwd: args.cwd ?? process.cwd(),
      sourceSha: args.source_sha,
      currentVersion: args.current_version,
      repository: args.repository,
      verifierEnv: { GH_TOKEN: process.env.GH_TOKEN ?? process.env.GITHUB_TOKEN ?? "" },
    });
    console.log(`baseline_version=${result.baselineVersion}`);
    console.log(`latest_tag=${result.latestTag}`);
  } catch (error) {
    console.error(error instanceof Error ? error.message : "Python release baseline resolution failed");
    process.exitCode = 1;
  }
}
