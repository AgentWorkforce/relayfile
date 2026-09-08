#!/usr/bin/env node
/**
 * Resolve the version baseline for a release started from a source branch.
 *
 * A release commit is deliberately tagged but is not pushed back to the
 * source branch.  Therefore package.json on the next dispatch can lag behind
 * the latest release.  Only annotated v<strict-semver> tags whose single
 * release parent is on the dispatch source's first-parent lineage, whose
 * commit has the exact release-only shape produced by publish.yml, and whose
 * complete package tree carries the tag version are trusted as a baseline.
 */

import { execFileSync } from "node:child_process";

export const RELEASE_PACKAGE_PATHS = [
  "package.json",
  "packages/core/package.json",
  "packages/sdk/typescript/package.json",
  "packages/client/package.json",
  "packages/agents/package.json",
  "packages/cli/package.json",
  "packages/file-observer/package.json",
  "packages/local-mount/package.json",
  "packages/mount-darwin-arm64/package.json",
  "packages/mount-darwin-x64/package.json",
  "packages/mount-linux-arm64/package.json",
  "packages/mount-linux-x64/package.json",
];

// The release workflow creates one commit from SOURCE_SHA after npm version,
// changelog finalization, and lockfile regeneration.  Keep this allowlist in
// sync with the explicit `git add` in publish.yml: a baseline tag is not
// trusted when its commit also carries an arbitrary side-branch change.
export const RELEASE_COMMIT_PATHS = new Set([
  "package.json",
  "package-lock.json",
  ...RELEASE_PACKAGE_PATHS,
  "packages/core/CHANGELOG.md",
  "packages/sdk/typescript/CHANGELOG.md",
  "packages/sdk/typescript/package-lock.json",
  "packages/client/CHANGELOG.md",
  "packages/agents/CHANGELOG.md",
  "packages/cli/CHANGELOG.md",
  "packages/file-observer/CHANGELOG.md",
  "packages/local-mount/CHANGELOG.md",
]);

const VERSION =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/;
const SHA = /^[0-9a-f]{40}$/;

export function parseStrictVersion(value) {
  const match = String(value ?? "").match(VERSION);
  if (!match) return null;
  return {
    raw: match[0],
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
    prerelease: match[4] ? match[4].split(".") : [],
    build: match[5] ?? "",
  };
}

function compareIdentifiers(a, b) {
  const numericA = /^(0|[1-9]\d*)$/.test(a);
  const numericB = /^(0|[1-9]\d*)$/.test(b);
  if (numericA && numericB) {
    if (a.length !== b.length) return a.length - b.length;
    return a < b ? -1 : a > b ? 1 : 0;
  }
  if (numericA) return -1;
  if (numericB) return 1;
  return a < b ? -1 : a > b ? 1 : 0;
}

export function compareVersions(a, b) {
  const left = typeof a === "string" ? parseStrictVersion(a) : a;
  const right = typeof b === "string" ? parseStrictVersion(b) : b;
  if (!left || !right) throw new Error("cannot compare invalid release versions");
  for (const key of ["major", "minor", "patch"]) {
    if (left[key] !== right[key]) return left[key] - right[key];
  }
  if (!left.prerelease.length && !right.prerelease.length) return 0;
  if (!left.prerelease.length) return 1;
  if (!right.prerelease.length) return -1;
  const length = Math.max(left.prerelease.length, right.prerelease.length);
  for (let i = 0; i < length; i += 1) {
    if (i === left.prerelease.length) return -1;
    if (i === right.prerelease.length) return 1;
    const result = compareIdentifiers(left.prerelease[i], right.prerelease[i]);
    if (result) return result;
  }
  return 0;
}

function git(cwd, args, { allowFailure = false } = {}) {
  try {
    return execFileSync("git", ["-C", cwd, ...args], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", allowFailure ? "pipe" : "pipe"],
    }).trim();
  } catch (error) {
    if (allowFailure) return null;
    const detail = String(error.stderr ?? "").trim();
    throw new Error(`git ${args.join(" ")} failed${detail ? `: ${detail}` : ""}`);
  }
}

function packageVersionAt(cwd, commit, path) {
  try {
    const text = git(cwd, ["show", `${commit}:${path}`]);
    const value = JSON.parse(text).version;
    return typeof value === "string" ? value : null;
  } catch {
    return null;
  }
}

function commitSubjectAt(cwd, commit) {
  return git(cwd, ["show", "-s", "--format=%s", commit], { allowFailure: true });
}

function changedPathsAt(cwd, parent, commit) {
  const output = git(cwd, ["diff-tree", "--no-commit-id", "--name-only", "-r", parent, commit], {
    allowFailure: true,
  });
  return output ? output.split(/\s+/).filter(Boolean) : [];
}

function trustedTag(cwd, tag, sourceSha) {
  const version = parseStrictVersion(tag.slice(1));
  if (!version) return null;
  const ref = `refs/tags/${tag}`;
  if (git(cwd, ["cat-file", "-t", ref], { allowFailure: true }) !== "tag") {
    return null;
  }
  const commit = git(cwd, ["rev-parse", `${ref}^{commit}`], { allowFailure: true });
  if (!commit || !SHA.test(commit)) return null;
  const parents = git(cwd, ["rev-list", "--parents", "-n", "1", commit], {
    allowFailure: true,
  });
  if (!parents || parents.split(/\s+/).length !== 2) return null;
  const parent = parents.split(/\s+/)[1];
  if (!SHA.test(parent)) return null;
  if (
    git(cwd, ["merge-base", "--is-ancestor", parent, sourceSha], {
      allowFailure: true,
    }) === null
  ) {
    return null;
  }
  // A release commit that is already on the dispatch source's exact lineage
  // is directly tied by its commit ancestry.  A prior release commit is often
  // intentionally not merged back to the source branch; for that stale case,
  // its parent must be on the source's first-parent line and the commit itself
  // must retain the exact release-commit shape.  The subject and changed-path
  // attestation prevent an arbitrary sibling branch rooted at an old source
  // commit from becoming the monotonic baseline.
  const commitOnSourceLineage =
    git(cwd, ["merge-base", "--is-ancestor", commit, sourceSha], {
      allowFailure: true,
    }) === "";
  const sourceLineage = git(cwd, ["rev-list", "--first-parent", sourceSha], {
    allowFailure: true,
  });
  if (
    !commitOnSourceLineage &&
    (!sourceLineage || !sourceLineage.split(/\s+/).includes(parent))
  ) {
    return null;
  }
  if (commitSubjectAt(cwd, commit) !== `chore(release): v${version.raw}`) return null;
  const changedPaths = changedPathsAt(cwd, parent, commit);
  if (
    changedPaths.length === 0 ||
    changedPaths.some((path) => !RELEASE_COMMIT_PATHS.has(path))
  ) {
    return null;
  }
  for (const path of RELEASE_PACKAGE_PATHS) {
    if (packageVersionAt(cwd, commit, path) !== version.raw) return null;
  }
  return { tag, version, commit, parent };
}

export function findTrustedReleaseTags({ cwd = process.cwd(), sourceSha }) {
  if (!SHA.test(sourceSha ?? "")) return [];
  const tags = git(cwd, ["for-each-ref", "--format=%(refname:strip=2)", "refs/tags/v*"])
    .split(/\s+/)
    .filter(Boolean);
  return tags
    .map((tag) => trustedTag(cwd, tag, sourceSha))
    .filter(Boolean)
    .sort((a, b) => {
      const result = compareVersions(a.version, b.version);
      return result || a.tag.localeCompare(b.tag);
    });
}

export function resolveReleaseBaseline({
  cwd = process.cwd(),
  sourceSha,
  currentVersion,
}) {
  const current = parseStrictVersion(currentVersion);
  if (!current) throw new Error("current package version is not strict SemVer");
  const tags = findTrustedReleaseTags({ cwd, sourceSha });
  const latest = tags.at(-1) ?? null;
  const baseline = latest && compareVersions(latest.version, current) > 0
    ? latest.version.raw
    : current.raw;
  const resumable = latest && latest.parent === sourceSha ? latest.version.raw : "";
  return {
    baselineVersion: baseline,
    resumableVersion: resumable,
    latestTag: latest?.tag ?? "",
  };
}

function parseArgs(argv) {
  const values = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith("--")) throw new Error(`unexpected argument: ${arg}`);
    values[arg.slice(2).replaceAll("-", "_")] = argv[++i];
  }
  return values;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  try {
    const args = parseArgs(process.argv.slice(2));
    const result = resolveReleaseBaseline({
      cwd: args.cwd ?? process.cwd(),
      sourceSha: args.source_sha,
      currentVersion: args.current_version,
    });
    console.log(`baseline_version=${result.baselineVersion}`);
    console.log(`resumable_version=${result.resumableVersion}`);
    console.log(`latest_tag=${result.latestTag}`);
  } catch (error) {
    console.error(error instanceof Error ? error.message : "release baseline resolution failed");
    process.exitCode = 1;
  }
}
