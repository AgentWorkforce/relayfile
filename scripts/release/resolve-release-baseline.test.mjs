import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import {
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

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

const VALID_INTEGRITY = `sha512-${"A".repeat(86)}==`;
const VALID_SHASUM = "a".repeat(40);
const SCRIPTS_DIRECTORY = fileURLToPath(new URL(".", import.meta.url));

function runCliFromSpacedPath() {
  const directory = mkdtempSync(join(tmpdir(), "relayfile baseline cli "));
  const entrypoint = join(directory, "resolve release baseline.mjs");
  copyFileSync(
    join(SCRIPTS_DIRECTORY, "resolve-release-baseline.mjs"),
    entrypoint,
  );
  copyFileSync(
    join(SCRIPTS_DIRECTORY, "create-release-attestation.mjs"),
    join(directory, "create-release-attestation.mjs"),
  );
  try {
    return spawnSync(
      process.execPath,
      [
        entrypoint,
        "--source-sha",
        "a".repeat(40),
        "--current-version",
        "not-a-version",
      ],
      { encoding: "utf8" },
    );
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

function runCliThroughSymlink() {
  const directory = mkdtempSync(join(tmpdir(), "relayfile-baseline-symlink-"));
  const entrypoint = join(directory, "resolve-release-baseline.mjs");
  symlinkSync(
    join(SCRIPTS_DIRECTORY, "resolve-release-baseline.mjs"),
    entrypoint,
  );
  symlinkSync(
    join(SCRIPTS_DIRECTORY, "create-release-attestation.mjs"),
    join(directory, "create-release-attestation.mjs"),
  );
  try {
    return spawnSync(
      process.execPath,
      [
        "--preserve-symlinks-main",
        entrypoint,
        "--source-sha",
        "a".repeat(40),
        "--current-version",
        "not-a-version",
      ],
      { encoding: "utf8" },
    );
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

function attestationFor(candidate, overrides = {}) {
  const packages = RELEASE_PACKAGE_NAMES.map((name) => ({
    schemaVersion: 1,
    kind: "relayfileReleasePackage",
    sourceSha: candidate.parent,
    workflowRunId: candidate.metadata?.workflowRunId ?? "123",
    workflowRunAttempt: candidate.metadata?.workflowRunAttempt ?? "1",
    package: {
      name,
      version: candidate.version.raw,
      status: "already-published",
      local: {
        file: "package.tgz",
        size: 1,
        sha256: "a".repeat(64),
        integrity: VALID_INTEGRITY,
        shasum: VALID_SHASUM,
      },
      registry: {
        name,
        version: candidate.version.raw,
        integrity: VALID_INTEGRITY,
        shasum: VALID_SHASUM,
      },
    },
  }));
  const attestation = {
    schemaVersion: 1,
    kind: "relayfileRelease",
    sourceSha: candidate.parent,
    version: candidate.version.raw,
    producer: {
      repository: RELEASE_REPOSITORY,
      workflow: "Publish Package",
      workflowPath: RELEASE_WORKFLOW_PATH,
      workflowRunId: candidate.metadata?.workflowRunId ?? "123",
      workflowRunAttempt: candidate.metadata?.workflowRunAttempt ?? "1",
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
  };
  return {
    ...attestation,
    ...overrides,
    producer: { ...attestation.producer, ...overrides.producer },
    tag: { ...attestation.tag, ...overrides.tag },
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
  const releaseTree = git(cwd, "rev-parse", "HEAD^{tree}");
  git(
    cwd,
    "tag",
    "-a",
    "v1.2.4",
    releaseCommit,
    "-m",
    "Release v1.2.4",
    "-m",
    `source-sha=${sourceSha}`,
    "-m",
    `tag-tree=${releaseTree}`,
    "-m",
    "workflow-run-id=123",
    "-m",
    "workflow-run-attempt=1",
  );
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

test("baseline CLI executes when its entrypoint path contains spaces", () => {
  const result = runCliFromSpacedPath();
  assert.notEqual(result.status, 0);
  assert.match(
    `${result.stdout}${result.stderr}`,
    /current package version is not strict SemVer/,
  );
});

test("baseline CLI executes through a preserved main-module symlink", () => {
  const result = runCliThroughSymlink();
  assert.notEqual(result.status, 0);
  assert.match(
    `${result.stdout}${result.stderr}`,
    /current package version is not strict SemVer/,
  );
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
  assert.equal(result.resumableRunId, "12345");
  assert.equal(result.resumableRunAttempt, "1");
  rmSync(cwd, { recursive: true, force: true });
});

test("a signed attestation from another canonical run cannot authorize recovery", () => {
  const { cwd, sourceSha } = sandboxWithPriorRelease();
  const result = resolveReleaseBaseline({
    cwd,
    sourceSha,
    currentVersion: "1.2.3",
    currentRunId: "123",
    currentRunAttempt: "2",
    releaseAttestationVerifier: ({ candidate }) => {
      const attestation = attestationFor(candidate);
      attestation.producer.workflowRunId = "999";
      return attestation;
    },
  });
  assert.equal(result.baselineVersion, "1.2.3");
  assert.equal(result.latestTag, "");
  assert.equal(result.resumableVersion, "");
  rmSync(cwd, { recursive: true, force: true });
});

test("external attestations reject malformed digest strings even when equal", () => {
  for (const [field, value] of [
    ["integrity", "sha512-not-a-digest"],
    ["shasum", "sha1-not-a-digest"],
  ]) {
    const { cwd, sourceSha } = sandboxWithPriorRelease();
    const result = resolveReleaseBaseline({
      cwd,
      sourceSha,
      currentVersion: "1.2.3",
      releaseAttestationVerifier: ({ candidate }) => {
        const attestation = attestationFor(candidate);
        for (const item of attestation.packages) {
          item.package.local[field] = value;
          item.package.registry[field] = value;
        }
        return attestation;
      },
    });
    assert.equal(result.baselineVersion, "1.2.3", field);
    assert.equal(result.latestTag, "", field);
    rmSync(cwd, { recursive: true, force: true });
  }
});

test("external attestations accept one matching canonical npm digest", () => {
  const { cwd, sourceSha } = sandboxWithPriorRelease();
  const result = resolveReleaseBaseline({
    cwd,
    sourceSha,
    currentVersion: "1.2.3",
    releaseAttestationVerifier: ({ candidate }) => {
      const attestation = attestationFor(candidate);
      for (const item of attestation.packages) {
        item.package.local.shasum = null;
        item.package.registry.shasum = null;
      }
      return attestation;
    },
  });
  assert.equal(result.baselineVersion, "1.2.4");
  assert.equal(result.latestTag, "v1.2.4");
  rmSync(cwd, { recursive: true, force: true });
});

test("external attestations reject SHA-1-only package identity", () => {
  const { cwd, sourceSha } = sandboxWithPriorRelease();
  const result = resolveReleaseBaseline({
    cwd,
    sourceSha,
    currentVersion: "1.2.3",
    releaseAttestationVerifier: ({ candidate }) => {
      const attestation = attestationFor(candidate);
      for (const item of attestation.packages) {
        item.package.local.integrity = null;
        item.package.registry.integrity = null;
      }
      return attestation;
    },
  });
  assert.equal(result.baselineVersion, "1.2.3");
  assert.equal(result.latestTag, "");
  rmSync(cwd, { recursive: true, force: true });
});

test("a rerun attestation with a new attempt cannot replace immutable tag metadata", () => {
  const { cwd, sourceSha } = sandboxWithPriorRelease();
  const result = resolveReleaseBaseline({
    cwd,
    sourceSha,
    currentVersion: "1.2.3",
    currentRunId: "123",
    currentRunAttempt: "2",
    releaseAttestationVerifier: ({ candidate }) => {
      const attestation = attestationFor(candidate);
      attestation.producer.workflowRunAttempt = "2";
      for (const item of attestation.packages) {
        item.workflowRunAttempt = "2";
      }
      return attestation;
    },
  });
  assert.equal(result.latestTag, "");
  assert.equal(result.resumableVersion, "");
  rmSync(cwd, { recursive: true, force: true });
});
