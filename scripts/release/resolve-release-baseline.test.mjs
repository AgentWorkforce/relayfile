import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";

import {
  RELEASE_BINARY_NAMES,
  RELEASE_PACKAGE_NAMES,
} from "./create-release-attestation.mjs";
import {
  RELEASE_PACKAGE_PATHS,
  RELEASE_REPOSITORY,
  RELEASE_WORKFLOW_PATH,
  sameAttemptRecoveryAllowed,
  resolveReleaseBaseline,
} from "./resolve-release-baseline.mjs";

function attestationFor(candidate, overrides = {}) {
  const packages = RELEASE_PACKAGE_NAMES.map((name) => ({
    sourceSha: candidate.parent,
    package: {
      name,
      version: candidate.version.raw,
      status: "already-published",
      local: {
        file: "package.tgz",
        size: 1,
        sha256: "a".repeat(64),
        integrity: "sha512-local",
        shasum: "sha1-local",
      },
      registry: {
        name,
        version: candidate.version.raw,
        integrity: "sha512-local",
        shasum: "sha1-local",
      },
    },
  }));
  return {
    schemaVersion: 1,
    kind: "relayfileRelease",
    sourceSha: candidate.parent,
    version: candidate.version.raw,
    producer: {
      repository: RELEASE_REPOSITORY,
      workflow: "Publish Package",
      workflowPath: RELEASE_WORKFLOW_PATH,
      workflowRunId: "123",
      workflowRunAttempt: "1",
    },
    tag: {
      name: candidate.tag,
      commit: candidate.commit,
      tree: candidate.tree,
    },
    versions: Object.fromEntries(
      RELEASE_PACKAGE_NAMES.map((name) => [name, candidate.version.raw]),
    ),
    packages,
    binaries: RELEASE_BINARY_NAMES.map((file) => ({
      file,
      sha256: "b".repeat(64),
    })),
    ...overrides,
  };
}

function verifierFor({ accepted = new Map(), overrides = {} } = {}) {
  return ({ candidate }) => {
    if (!accepted.has(candidate.tag)) return null;
    return attestationFor(candidate, overrides[candidate.tag] ?? {});
  };
}

function git(cwd, ...args) {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

function writePackages(cwd, version) {
  for (const path of RELEASE_PACKAGE_PATHS) {
    const file = join(cwd, path);
    mkdirSync(join(file, ".."), { recursive: true });
    writeFileSync(file, JSON.stringify({ name: path, version }) + "\n");
  }
}

function sandboxWithPriorRelease() {
  const cwd = mkdtempSync(join(tmpdir(), "relayfile-baseline-"));
  git(cwd, "init", "-q");
  git(cwd, "config", "user.name", "Release Test");
  git(cwd, "config", "user.email", "release-test@example.invalid");
  writePackages(cwd, "1.2.3");
  git(cwd, "add", ".");
  git(cwd, "commit", "-qm", "source");
  const sourceSha = git(cwd, "rev-parse", "HEAD");
  writePackages(cwd, "1.2.4");
  git(cwd, "commit", "-qam", "chore(release): v1.2.4");
  const releaseCommit = git(cwd, "rev-parse", "HEAD");
  git(cwd, "tag", "-a", "v1.2.4", releaseCommit, "-m", "Release v1.2.4");
  return { cwd, sourceSha, releaseCommit };
}

test("next dispatch advances from a trusted release tag not merged to source", () => {
  const { cwd, sourceSha } = sandboxWithPriorRelease();
  const result = resolveReleaseBaseline({
    cwd,
    sourceSha,
    currentVersion: "1.2.3",
    releaseAttestationVerifier: verifierFor({
      accepted: new Map([["v1.2.4", true]]),
    }),
  });
  assert.equal(result.baselineVersion, "1.2.4");
  assert.equal(result.latestTag, "v1.2.4");
  assert.equal(result.resumableVersion, "");
  rmSync(cwd, { recursive: true, force: true });
});

test("sibling-branch release tags do not become a version baseline", () => {
  const { cwd, sourceSha } = sandboxWithPriorRelease();
  git(cwd, "checkout", "-qb", "dispatch-main", sourceSha);
  writePackages(cwd, "1.2.3");
  writeFileSync(join(cwd, "main.txt"), "main\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-qm", "main advances");
  const currentSourceSha = git(cwd, "rev-parse", "HEAD");

  git(cwd, "checkout", "-q", "-b", "sibling", sourceSha);
  writePackages(cwd, "9.9.9");
  writeFileSync(join(cwd, "sibling.txt"), "forged side branch\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-qm", "chore(release): v9.9.9");
  const siblingCommit = git(cwd, "rev-parse", "HEAD");
  git(cwd, "tag", "-a", "v9.9.9", siblingCommit, "-m", "Release v9.9.9");

  const result = resolveReleaseBaseline({
    cwd,
    sourceSha: currentSourceSha,
    currentVersion: "1.2.3",
    releaseAttestationVerifier: verifierFor({
      accepted: new Map([["v1.2.4", true]]),
    }),
  });
  assert.equal(result.baselineVersion, "1.2.4");
  assert.equal(result.latestTag, "v1.2.4");
  rmSync(cwd, { recursive: true, force: true });
});

test("exact-shape sibling release tags fail closed without external attestation", () => {
  const { cwd, sourceSha } = sandboxWithPriorRelease();
  git(cwd, "checkout", "-qb", "dispatch-main", sourceSha);
  writePackages(cwd, "1.2.3");
  writeFileSync(join(cwd, "main.txt"), "main\n");
  git(cwd, "add", "main.txt");
  git(cwd, "commit", "-qam", "main advances");
  const currentSourceSha = git(cwd, "rev-parse", "HEAD");

  git(cwd, "checkout", "-q", "-b", "sibling", sourceSha);
  writePackages(cwd, "99.99.99");
  git(cwd, "add", ".");
  git(cwd, "commit", "-qm", "chore(release): v99.99.99");
  const siblingCommit = git(cwd, "rev-parse", "HEAD");
  git(cwd, "tag", "-a", "v99.99.99", siblingCommit, "-m", "Release v99.99.99");

  const result = resolveReleaseBaseline({
    cwd,
    sourceSha: currentSourceSha,
    currentVersion: "1.2.3",
    releaseAttestationVerifier: verifierFor({
      accepted: new Map([["v1.2.4", true]]),
    }),
  });
  assert.equal(result.baselineVersion, "1.2.4");
  assert.equal(result.latestTag, "v1.2.4");
  rmSync(cwd, { recursive: true, force: true });
});

test("missing, invalid, and mismatched external attestations fail closed", () => {
  const failures = [
    ["absent", null],
    ["invalid", { kind: "not-a-relayfile-release" }],
    [
      "wrong repository",
      {
        producer: {
          repository: "evil/example",
          workflowPath: RELEASE_WORKFLOW_PATH,
        },
      },
    ],
    [
      "wrong workflow",
      {
        producer: {
          repository: RELEASE_REPOSITORY,
          workflowPath: ".github/workflows/other.yml",
        },
      },
    ],
    ["wrong source", { sourceSha: "f".repeat(40) }],
    ["wrong tag", { tag: { name: "v9.9.9" } }],
    ["missing packages", { packages: [] }],
    ["missing binaries", { binaries: [] }],
  ];
  for (const [name, value] of failures) {
    const { cwd, sourceSha } = sandboxWithPriorRelease();
    const result = resolveReleaseBaseline({
      cwd,
      sourceSha,
      currentVersion: "1.2.3",
      releaseAttestationVerifier: ({ candidate }) =>
        value === null ? null : attestationFor(candidate, value),
    });
    assert.equal(result.baselineVersion, "1.2.3", name);
    assert.equal(result.latestTag, "", name);
    rmSync(cwd, { recursive: true, force: true });
  }
});

test("same-workflow reruns accept only exact run/source/tree metadata", () => {
  const { cwd, sourceSha } = sandboxWithPriorRelease();
  git(cwd, "checkout", "-q", sourceSha);
  const tag = "v2.0.0";
  writePackages(cwd, "2.0.0");
  git(cwd, "commit", "-qam", "chore(release): v2.0.0");
  const releaseCommit = git(cwd, "rev-parse", "HEAD");
  const tree = git(cwd, "rev-parse", "HEAD^{tree}");
  git(
    cwd,
    "tag",
    "-a",
    tag,
    releaseCommit,
    "-m",
    "Release v2.0.0",
    "-m",
    `source-sha=${sourceSha}`,
    "-m",
    `tag-tree=${tree}`,
    "-m",
    "workflow-run-id=12345",
    "-m",
    "workflow-run-attempt=1",
  );
  const result = resolveReleaseBaseline({
    cwd,
    sourceSha,
    currentVersion: "1.2.3",
    currentRunId: "12345",
    currentRunAttempt: "2",
    releaseAttestationVerifier: ({
      candidate,
      currentRunId,
      currentRunAttempt,
    }) =>
      sameAttemptRecoveryAllowed(candidate, {
        sourceSha,
        currentRunId,
        currentRunAttempt,
      })
        ? attestationFor(candidate)
        : null,
  });
  assert.equal(result.baselineVersion, "2.0.0");
  assert.equal(result.resumableVersion, "2.0.0");
  rmSync(cwd, { recursive: true, force: true });
});
