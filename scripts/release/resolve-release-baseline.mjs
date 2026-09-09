#!/usr/bin/env node
/**
 * Resolve the version baseline for a release started from a source branch.
 *
 * A release commit is deliberately tagged but is not pushed back to the
 * source branch.  Therefore package.json on the next dispatch can lag behind
 * the latest release.  Only annotated v<strict-semver> tags whose single
 * release parent is on the dispatch source's first-parent lineage, whose
 * commit has the exact release-only shape produced by publish.yml, whose
 * complete package tree carries the tag version, and whose release metadata
 * is backed by a GitHub Actions artifact attestation are trusted as a
 * baseline. Git shape is only a candidate filter, never provenance.
 */

import { execFileSync } from "node:child_process";
import {
  existsSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
} from "node:fs";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import {
  isCompletePackageAttestation,
  isOptionalSha1Shasum,
  isOptionalSha512Integrity,
  RELEASE_BINARY_NAMES,
  RELEASE_PACKAGE_NAMES,
} from "./create-release-attestation.mjs";

export const RELEASE_REPOSITORY = "AgentWorkforce/relayfile";
export const RELEASE_WORKFLOW_PATH = ".github/workflows/publish.yml";

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
const SHA256 = /^[0-9a-f]{64}$/;
const RUN_ID = /^\d+$/;

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
  if (!left || !right)
    throw new Error("cannot compare invalid release versions");
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
    throw new Error(
      `git ${args.join(" ")} failed${detail ? `: ${detail}` : ""}`,
    );
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
  return git(cwd, ["show", "-s", "--format=%s", commit], {
    allowFailure: true,
  });
}

function changedPathsAt(cwd, parent, commit) {
  const output = git(
    cwd,
    ["diff-tree", "--no-commit-id", "--name-only", "-r", parent, commit],
    {
      allowFailure: true,
    },
  );
  return output ? output.split(/\s+/).filter(Boolean) : [];
}

function releaseTagMetadata(cwd, ref) {
  const raw = git(cwd, ["cat-file", "-p", ref], { allowFailure: true });
  if (!raw) return null;
  const separator = raw.indexOf("\n\n");
  if (separator < 0) return null;
  const fields = {};
  for (const line of raw.slice(separator + 2).split(/\r?\n/)) {
    const match = line.match(/^([a-z][a-z-]+)=(.+)$/);
    if (match) fields[match[1]] = match[2].trim();
  }
  return {
    sourceSha: fields["source-sha"] ?? "",
    tree: fields["tag-tree"] ?? "",
    workflowRunId: fields["workflow-run-id"] ?? "",
    workflowRunAttempt: fields["workflow-run-attempt"] ?? "",
  };
}

function trustedTag(cwd, tag, sourceSha) {
  const version = parseStrictVersion(tag.slice(1));
  if (!version) return null;
  const ref = `refs/tags/${tag}`;
  if (git(cwd, ["cat-file", "-t", ref], { allowFailure: true }) !== "tag") {
    return null;
  }
  const commit = git(cwd, ["rev-parse", `${ref}^{commit}`], {
    allowFailure: true,
  });
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
  if (commitSubjectAt(cwd, commit) !== `chore(release): v${version.raw}`)
    return null;
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
  const tree = git(cwd, ["rev-parse", `${commit}^{tree}`], {
    allowFailure: true,
  });
  if (!tree || !SHA.test(tree)) return null;
  return {
    tag,
    version,
    commit,
    parent,
    tree,
    metadata: releaseTagMetadata(cwd, ref),
  };
}

export function findTrustedReleaseTags({ cwd = process.cwd(), sourceSha }) {
  if (!SHA.test(sourceSha ?? "")) return [];
  const tags = git(cwd, [
    "for-each-ref",
    "--format=%(refname:strip=2)",
    "refs/tags/v*",
  ])
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

function parseJson(text, description) {
  try {
    return JSON.parse(String(text ?? ""));
  } catch {
    throw new Error(`could not parse ${description}`);
  }
}

function runGh(args, { cwd, env }) {
  return execFileSync("gh", args, {
    cwd,
    encoding: "utf8",
    env: { ...process.env, ...env },
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

function findDownloadedAttestation(directory) {
  const direct = join(directory, "release-attestation.json");
  if (existsSync(direct)) return direct;
  const files = readdirSync(directory, { recursive: true });
  const match = files.find(
    (file) =>
      typeof file === "string" && file.endsWith("/release-attestation.json"),
  );
  return match ? join(directory, match) : null;
}

function downloadReleaseAttestation({ cwd, repository, tag, directory, env }) {
  try {
    runGh(
      [
        "release",
        "download",
        tag,
        "--repo",
        repository,
        "--pattern",
        "release-attestation.json",
        "--dir",
        directory,
        "--clobber",
      ],
      { cwd, env },
    );
    return findDownloadedAttestation(directory);
  } catch {
    return null;
  }
}

function downloadRunAttestation({
  cwd,
  repository,
  runId,
  runAttempt,
  directory,
  env,
}) {
  try {
    runGh(
      [
        "run",
        "download",
        runId,
        "--repo",
        repository,
        "--name",
        `release-attestation-${runAttempt}`,
        "--dir",
        directory,
      ],
      { cwd, env },
    );
    return findDownloadedAttestation(directory);
  } catch {
    return null;
  }
}

export function validateReleaseAttestation(
  attestation,
  candidate,
  {
    repository = RELEASE_REPOSITORY,
    workflowPath = RELEASE_WORKFLOW_PATH,
  } = {},
) {
  if (!attestation || attestation.kind !== "relayfileRelease") return false;
  if (attestation.schemaVersion !== 1) return false;
  if (attestation.sourceSha !== candidate.parent) return false;
  if (attestation.version !== candidate.version.raw) return false;
  if (attestation.tag?.name !== candidate.tag) return false;
  if (attestation.tag?.commit !== candidate.commit) return false;
  if (attestation.tag?.tree !== candidate.tree) return false;
  if (attestation.producer?.repository !== repository) return false;
  if (attestation.producer?.workflow !== "Publish Package") return false;
  if (attestation.producer?.workflowPath !== workflowPath) return false;
  if (!RUN_ID.test(String(attestation.producer?.workflowRunId ?? "")))
    return false;
  if (!RUN_ID.test(String(attestation.producer?.workflowRunAttempt ?? "")))
    return false;
  // The signed artifact must describe the exact annotated tag metadata.  The
  // tag metadata is the only trusted binding for a prior canonical workflow
  // run, so a validly signed artifact from another run cannot authorize a
  // same-attempt recovery.
  const metadata = candidate.metadata;
  if (
    !metadata ||
    metadata.sourceSha !== candidate.parent ||
    metadata.tree !== candidate.tree ||
    !RUN_ID.test(metadata.workflowRunId) ||
    !RUN_ID.test(metadata.workflowRunAttempt) ||
    attestation.sourceSha !== metadata.sourceSha ||
    attestation.tag?.tree !== metadata.tree ||
    attestation.producer.workflowRunId !== metadata.workflowRunId ||
    attestation.producer.workflowRunAttempt !== metadata.workflowRunAttempt
  ) {
    return false;
  }
  if (
    !attestation.versions ||
    typeof attestation.versions !== "object" ||
    Array.isArray(attestation.versions) ||
    Object.keys(attestation.versions).length !== RELEASE_PACKAGE_NAMES.length
  ) {
    return false;
  }
  const packageNames = new Set();
  if (
    !Array.isArray(attestation.packages) ||
    attestation.packages.length !== RELEASE_PACKAGE_NAMES.length
  ) {
    return false;
  }
  for (const item of attestation.packages) {
    const record = item?.package;
    const name = record?.name;
    const local = record?.local;
    const registry = record?.registry;
    if (
      !isCompletePackageAttestation(item, {
        sourceSha: candidate.parent,
        version: candidate.version.raw,
        workflowRunId: metadata?.workflowRunId,
        workflowRunAttempt: metadata?.workflowRunAttempt,
      })
    ) {
      return false;
    }
    const digestFieldsAreValid =
      isOptionalSha512Integrity(local?.integrity) &&
      isOptionalSha1Shasum(local?.shasum) &&
      isOptionalSha512Integrity(registry?.integrity) &&
      isOptionalSha1Shasum(registry?.shasum);
    if (
      !RELEASE_PACKAGE_NAMES.includes(name) ||
      packageNames.has(name) ||
      item.sourceSha !== candidate.parent ||
      record.version !== candidate.version.raw ||
      !["published", "already-published"].includes(record.status) ||
      !local ||
      typeof local.file !== "string" ||
      !local.file ||
      !Number.isInteger(local.size) ||
      local.size < 0 ||
      !SHA256.test(String(local.sha256 ?? "")) ||
      !registry ||
      registry.name !== name ||
      registry.version !== record.version ||
      !digestFieldsAreValid ||
      (!registry.integrity && !registry.shasum)
    ) {
      return false;
    }
    const sharedIntegrity = record.registry.integrity && record.local.integrity;
    const sharedShasum = record.registry.shasum && record.local.shasum;
    if (
      !sharedIntegrity ||
      (sharedIntegrity &&
        record.registry.integrity !== record.local.integrity) ||
      (sharedShasum && record.registry.shasum !== record.local.shasum)
    ) {
      return false;
    }
    packageNames.add(name);
  }
  if (
    RELEASE_PACKAGE_NAMES.some((name) => !packageNames.has(name)) ||
    Object.entries(attestation.versions).some(
      ([name, version]) =>
        !RELEASE_PACKAGE_NAMES.includes(name) ||
        version !== candidate.version.raw,
    )
  ) {
    return false;
  }
  if (
    !Array.isArray(attestation.binaries) ||
    attestation.binaries.length !== RELEASE_BINARY_NAMES.length
  ) {
    return false;
  }
  const binaryNames = new Set();
  for (const binary of attestation.binaries) {
    if (
      !RELEASE_BINARY_NAMES.includes(binary?.file) ||
      binaryNames.has(binary?.file) ||
      !SHA256.test(String(binary?.sha256 ?? ""))
    ) {
      return false;
    }
    binaryNames.add(binary.file);
  }
  if (binaryNames.size !== RELEASE_BINARY_NAMES.length) return false;
  return true;
}

export function sameAttemptRecoveryAllowed(
  candidate,
  { sourceSha, currentRunId = "", currentRunAttempt = "" } = {},
) {
  const metadata = candidate.metadata;
  if (!metadata || candidate.parent !== sourceSha) return false;
  if (metadata.sourceSha !== sourceSha || metadata.tree !== candidate.tree)
    return false;
  if (!RUN_ID.test(currentRunId) || metadata.workflowRunId !== currentRunId)
    return false;
  if (
    !RUN_ID.test(metadata.workflowRunAttempt) ||
    !RUN_ID.test(currentRunAttempt)
  ) {
    return false;
  }
  // A tag is immutable, so repeated retries retain the attempt that first
  // created it; any earlier attempt of this exact run is valid only with the
  // same source/tree/parent constraints above.
  return Number(metadata.workflowRunAttempt) < Number(currentRunAttempt);
}

export function verifyReleaseTagAttestation({
  cwd,
  candidate,
  repository = RELEASE_REPOSITORY,
  workflowPath = RELEASE_WORKFLOW_PATH,
  sourceSha,
  currentRunId = "",
  currentRunAttempt = "",
  env = {},
}) {
  if (!repository || !candidate) return null;
  const directory = mkdtempSync(
    join(tmpdir(), "relayfile-release-attestation-"),
  );
  try {
    let artifact = downloadReleaseAttestation({
      cwd,
      repository,
      tag: candidate.tag,
      directory,
      env,
    });
    if (
      !artifact &&
      sameAttemptRecoveryAllowed(candidate, {
        sourceSha,
        currentRunId,
        currentRunAttempt,
      })
    ) {
      artifact = downloadRunAttestation({
        cwd,
        repository,
        runId: candidate.metadata.workflowRunId,
        runAttempt: candidate.metadata.workflowRunAttempt,
        directory,
        env,
      });
    }
    if (!artifact) return null;
    const verification = parseJson(
      runGh(
        [
          "attestation",
          "verify",
          artifact,
          "--repo",
          repository,
          "--signer-repo",
          repository,
          "--signer-workflow",
          `${repository}/${workflowPath}`,
          "--source-digest",
          candidate.parent,
          "--format",
          "json",
        ],
        { cwd, env },
      ),
      "GitHub artifact attestation verification",
    );
    if (
      !Array.isArray(verification) ||
      !verification.some(
        (entry) => entry?.verificationResult?.signature?.certificate,
      )
    ) {
      return null;
    }
    const attestation = parseJson(
      readFileSync(artifact, "utf8"),
      "release attestation",
    );
    return validateReleaseAttestation(attestation, candidate, {
      repository,
      workflowPath,
    })
      ? attestation
      : null;
  } catch {
    return null;
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

export function resolveReleaseBaseline({
  cwd = process.cwd(),
  sourceSha,
  currentVersion,
  repository = RELEASE_REPOSITORY,
  workflowPath = RELEASE_WORKFLOW_PATH,
  currentRunId = "",
  currentRunAttempt = "",
  releaseAttestationVerifier = verifyReleaseTagAttestation,
  verifierEnv = {},
}) {
  const current = parseStrictVersion(currentVersion);
  if (!current) throw new Error("current package version is not strict SemVer");
  const tags = findTrustedReleaseTags({ cwd, sourceSha });
  let latest = null;
  // Check newest candidates first so a normal dispatch performs one external
  // verification. If an untrusted high tag is present, continue downward to
  // the newest lower release whose signed attestation is valid.
  for (const candidate of [...tags].reverse()) {
    let attestation = null;
    try {
      attestation = releaseAttestationVerifier({
        cwd,
        candidate,
        repository,
        workflowPath,
        sourceSha,
        currentRunId,
        currentRunAttempt,
        env: verifierEnv,
      });
    } catch {
      attestation = null;
    }
    if (
      attestation &&
      validateReleaseAttestation(attestation, candidate, {
        repository,
        workflowPath,
      })
    ) {
      latest = candidate;
      break;
    }
  }
  const baseline =
    latest && compareVersions(latest.version, current) > 0
      ? latest.version.raw
      : current.raw;
  const resumable =
    latest &&
    sameAttemptRecoveryAllowed(latest, {
      sourceSha,
      currentRunId,
      currentRunAttempt,
    })
      ? latest.version.raw
      : "";
  const resumableRunId = resumable ? latest.metadata.workflowRunId : "";
  const resumableRunAttempt = resumable
    ? latest.metadata.workflowRunAttempt
    : "";
  return {
    baselineVersion: baseline,
    resumableVersion: resumable,
    resumableRunId,
    resumableRunAttempt,
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

let entrypoint = "";
let modulePath = "";
try {
  entrypoint = process.argv[1] ? realpathSync(resolve(process.argv[1])) : "";
  modulePath = realpathSync(fileURLToPath(import.meta.url));
} catch {
  // Import contexts such as `node -` do not name a filesystem entrypoint.
}
if (entrypoint && modulePath === entrypoint) {
  try {
    const args = parseArgs(process.argv.slice(2));
    const result = resolveReleaseBaseline({
      cwd: args.cwd ?? process.cwd(),
      sourceSha: args.source_sha,
      currentVersion: args.current_version,
      repository: args.repository,
      workflowPath: args.workflow_path ?? RELEASE_WORKFLOW_PATH,
      currentRunId: args.run_id ?? process.env.GITHUB_RUN_ID ?? "",
      currentRunAttempt:
        args.run_attempt ?? process.env.GITHUB_RUN_ATTEMPT ?? "",
      verifierEnv: {
        GH_TOKEN: process.env.GH_TOKEN ?? process.env.GITHUB_TOKEN ?? "",
      },
    });
    console.log(`baseline_version=${result.baselineVersion}`);
    console.log(`resumable_version=${result.resumableVersion}`);
    console.log(`resumable_run_id=${result.resumableRunId}`);
    console.log(`resumable_run_attempt=${result.resumableRunAttempt}`);
    console.log(`latest_tag=${result.latestTag}`);
  } catch (error) {
    console.error(
      error instanceof Error
        ? error.message
        : "release baseline resolution failed",
    );
    process.exitCode = 1;
  }
}
