import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  copyFileSync,
  mkdtempSync,
  rmSync,
  symlinkSync,
} from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

import {
  buildReleaseAttestation,
  parseChecksums,
  RELEASE_BINARY_NAMES,
  RELEASE_PACKAGE_NAMES,
} from "./create-release-attestation.mjs";

const VALID_INTEGRITY = `sha512-${"A".repeat(86)}==`;
const OTHER_INTEGRITY = `sha512-${"A".repeat(85)}Q==`;
const VALID_SHASUM = "a".repeat(40);
const OTHER_SHASUM = "b".repeat(40);
const SCRIPT = fileURLToPath(
  new URL("./create-release-attestation.mjs", import.meta.url),
);

function runCliFromSpacedPath(args) {
  const directory = mkdtempSync(join(tmpdir(), "relayfile attestation cli "));
  const entrypoint = join(directory, "create release attestation.mjs");
  copyFileSync(SCRIPT, entrypoint);
  try {
    return spawnSync(process.execPath, [entrypoint, ...args(directory)], {
      encoding: "utf8",
    });
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

function runCliThroughSymlink(args) {
  const directory = mkdtempSync(join(tmpdir(), "relayfile-attestation-symlink-"));
  const entrypoint = join(directory, "create-release-attestation.mjs");
  symlinkSync(SCRIPT, entrypoint);
  try {
    return spawnSync(
      process.execPath,
      ["--preserve-symlinks-main", entrypoint, ...args(directory)],
      { encoding: "utf8" },
    );
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

function packageRecords(
  sourceSha,
  version = "1.2.3",
  workflowRunId = 123,
  workflowRunAttempt = 1,
) {
  return RELEASE_PACKAGE_NAMES.map((name) => ({
    schemaVersion: 1,
    kind: "relayfileReleasePackage",
    sourceSha,
    workflowRunId: String(workflowRunId),
    workflowRunAttempt: String(workflowRunAttempt),
    package: {
      name,
      version,
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
        version,
        integrity: VALID_INTEGRITY,
        shasum: VALID_SHASUM,
      },
    },
  }));
}

test("attestation binds source, run, package digests, binaries and tag commit/tree", () => {
  const sourceSha = "a".repeat(40);
  const result = buildReleaseAttestation({
    sourceSha,
    runId: 123,
    runAttempt: 2,
    version: "1.2.3",
    tag: "v1.2.3",
    tagCommit: "b".repeat(40),
    tagTree: "d".repeat(40),
    repository: "AgentWorkforce/relayfile",
    packages: packageRecords(sourceSha, "1.2.3", 123, 2),
    binaries: RELEASE_BINARY_NAMES.map((file) => ({
      file,
      sha256: "c".repeat(64),
    })),
  });
  assert.equal(result.sourceSha, sourceSha);
  assert.equal(result.producer.workflowRunAttempt, "2");
  assert.equal(result.versions["@relayfile/core"], "1.2.3");
  assert.equal(result.tag.commit, "b".repeat(40));
  assert.equal(result.tag.tree, "d".repeat(40));
});

test("attestation accepts integrity-only npm identity", () => {
  const sourceSha = "a".repeat(40);
  const packages = packageRecords(sourceSha);
  for (const item of packages) {
    item.package.local.shasum = null;
    item.package.registry.shasum = null;
  }
  const result = buildReleaseAttestation({
    sourceSha,
    runId: 123,
    runAttempt: 1,
    version: "1.2.3",
    tag: "v1.2.3",
    tagCommit: "b".repeat(40),
    tagTree: "d".repeat(40),
    repository: "AgentWorkforce/relayfile",
    packages,
    binaries: RELEASE_BINARY_NAMES.map((file) => ({
      file,
      sha256: "c".repeat(64),
    })),
  });
  assert.equal(result.packages[0].package.local.shasum, null);
});

test("attestation rejects SHA-1-only npm identity", () => {
  const sourceSha = "a".repeat(40);
  const packages = packageRecords(sourceSha);
  for (const item of packages) {
    item.package.local.integrity = null;
    item.package.registry.integrity = null;
  }
  assert.throws(
    () =>
      buildReleaseAttestation({
        sourceSha,
        runId: 123,
        runAttempt: 1,
        version: "1.2.3",
        tag: "v1.2.3",
        tagCommit: "b".repeat(40),
        tagTree: "d".repeat(40),
        repository: "AgentWorkforce/relayfile",
        packages,
        binaries: RELEASE_BINARY_NAMES.map((file) => ({
          file,
          sha256: "c".repeat(64),
        })),
      }),
    /no common SHA-512 integrity|incomplete package attestation/,
  );
});

test("attestation CLI executes when its entrypoint path contains spaces", () => {
  const result = runCliFromSpacedPath((directory) => [
    "--package-dir",
    join(directory, "missing-package-attestations"),
    "--checksums",
    join(directory, "missing-checksums"),
  ]);
  assert.notEqual(result.status, 0);
  assert.match(`${result.stdout}${result.stderr}`, /ENOENT/);
});

test("attestation CLI executes through a preserved main-module symlink", () => {
  const result = runCliThroughSymlink((directory) => [
    "--package-dir",
    join(directory, "missing-package-attestations"),
    "--checksums",
    join(directory, "missing-checksums"),
  ]);
  assert.notEqual(result.status, 0);
  assert.match(`${result.stdout}${result.stderr}`, /ENOENT/);
});

test("attestation rejects a package from a different source", () => {
  const sourceSha = "a".repeat(40);
  assert.throws(
    () =>
      buildReleaseAttestation({
        sourceSha,
        runId: 123,
        runAttempt: 1,
        version: "1.2.3",
        tag: "v1.2.3",
        tagCommit: "b".repeat(40),
        tagTree: "d".repeat(40),
        repository: "AgentWorkforce/relayfile",
        packages: packageRecords("d".repeat(40)),
        binaries: RELEASE_BINARY_NAMES.map((file) => ({
          file,
          sha256: "c".repeat(64),
        })),
      }),
    /different source SHA|missing packages/,
  );
});

test("attestation rejects a missing package after validating the remaining set", () => {
  const sourceSha = "a".repeat(40);
  assert.throws(
    () =>
      buildReleaseAttestation({
        sourceSha,
        runId: 123,
        runAttempt: 1,
        version: "1.2.3",
        tag: "v1.2.3",
        tagCommit: "b".repeat(40),
        tagTree: "d".repeat(40),
        repository: "AgentWorkforce/relayfile",
        packages: packageRecords(sourceSha).slice(0, -1),
        binaries: RELEASE_BINARY_NAMES.map((file) => ({
          file,
          sha256: "c".repeat(64),
        })),
      }),
    /missing packages/,
  );
});

test("attestation rejects registry content that differs from the local tarball", () => {
  const sourceSha = "a".repeat(40);
  const packages = packageRecords(sourceSha);
  packages[0].package.registry.integrity = OTHER_INTEGRITY;
  assert.throws(
    () =>
      buildReleaseAttestation({
        sourceSha,
        runId: 123,
        runAttempt: 1,
        version: "1.2.3",
        tag: "v1.2.3",
        tagCommit: "b".repeat(40),
        tagTree: "d".repeat(40),
        repository: "AgentWorkforce/relayfile",
        packages,
        binaries: RELEASE_BINARY_NAMES.map((file) => ({
          file,
          sha256: "c".repeat(64),
        })),
      }),
    /registry integrity does not match/,
  );
});

test("attestation rejects incomparable mixed digest types", () => {
  const sourceSha = "a".repeat(40);
  const packages = packageRecords(sourceSha);
  packages[0].package.local.shasum = null;
  packages[0].package.registry = { shasum: OTHER_SHASUM };
  assert.throws(
    () =>
      buildReleaseAttestation({
        sourceSha,
        runId: 123,
        runAttempt: 1,
        version: "1.2.3",
        tag: "v1.2.3",
        tagCommit: "b".repeat(40),
        tagTree: "d".repeat(40),
        repository: "AgentWorkforce/relayfile",
        packages,
        binaries: RELEASE_BINARY_NAMES.map((file) => ({
          file,
          sha256: "c".repeat(64),
        })),
      }),
    /no common SHA-512 integrity/,
  );
});

test("attestation rejects unexpected package names", () => {
  const sourceSha = "a".repeat(40);
  const packages = packageRecords(sourceSha);
  packages.push({
    sourceSha,
    package: {
      name: "@relayfile/unexpected",
      version: "1.2.3",
      status: "already-published",
      local: {
        sha256: "a".repeat(64),
        integrity: VALID_INTEGRITY,
        shasum: VALID_SHASUM,
      },
      registry: { integrity: VALID_INTEGRITY, shasum: VALID_SHASUM },
    },
  });
  assert.throws(
    () =>
      buildReleaseAttestation({
        sourceSha,
        runId: 123,
        runAttempt: 1,
        version: "1.2.3",
        tag: "v1.2.3",
        tagCommit: "b".repeat(40),
        tagTree: "d".repeat(40),
        repository: "AgentWorkforce/relayfile",
        packages,
        binaries: RELEASE_BINARY_NAMES.map((file) => ({
          file,
          sha256: "c".repeat(64),
        })),
      }),
    /not a release package/,
  );
});

test("attestation rejects malformed equal digest strings", () => {
  const sourceSha = "a".repeat(40);
  const packages = packageRecords(sourceSha);
  packages[0].package.local.integrity = "sha512-not-a-digest";
  packages[0].package.registry.integrity = "sha512-not-a-digest";
  assert.throws(
    () =>
      buildReleaseAttestation({
        sourceSha,
        runId: 123,
        runAttempt: 1,
        version: "1.2.3",
        tag: "v1.2.3",
        tagCommit: "b".repeat(40),
        tagTree: "d".repeat(40),
        repository: "AgentWorkforce/relayfile",
        packages,
        binaries: RELEASE_BINARY_NAMES.map((file) => ({
          file,
          sha256: "c".repeat(64),
        })),
      }),
    /malformed package digest/,
  );
});

test("attestation rejects package children from another workflow attempt", () => {
  const sourceSha = "a".repeat(40);
  const packages = packageRecords(sourceSha, "1.2.3", 123, 1);
  assert.throws(
    () =>
      buildReleaseAttestation({
        sourceSha,
        runId: 123,
        runAttempt: 2,
        version: "1.2.3",
        tag: "v1.2.3",
        tagCommit: "b".repeat(40),
        tagTree: "d".repeat(40),
        repository: "AgentWorkforce/relayfile",
        packages,
        binaries: RELEASE_BINARY_NAMES.map((file) => ({
          file,
          sha256: "c".repeat(64),
        })),
      }),
    /incomplete package attestation/,
  );
});

test("attestation rejects a missing local package file", () => {
  const sourceSha = "a".repeat(40);
  const packages = packageRecords(sourceSha, "1.2.3", 123, 1);
  delete packages[0].package.local.file;
  assert.throws(
    () =>
      buildReleaseAttestation({
        sourceSha,
        runId: 123,
        runAttempt: 1,
        version: "1.2.3",
        tag: "v1.2.3",
        tagCommit: "b".repeat(40),
        tagTree: "d".repeat(40),
        repository: "AgentWorkforce/relayfile",
        packages,
        binaries: RELEASE_BINARY_NAMES.map((file) => ({
          file,
          sha256: "c".repeat(64),
        })),
      }),
    /incomplete package attestation/,
  );
});

test("attestation rejects a missing registry package name", () => {
  const sourceSha = "a".repeat(40);
  const packages = packageRecords(sourceSha, "1.2.3", 123, 1);
  delete packages[0].package.registry.name;
  assert.throws(
    () =>
      buildReleaseAttestation({
        sourceSha,
        runId: 123,
        runAttempt: 1,
        version: "1.2.3",
        tag: "v1.2.3",
        tagCommit: "b".repeat(40),
        tagTree: "d".repeat(40),
        repository: "AgentWorkforce/relayfile",
        packages,
        binaries: RELEASE_BINARY_NAMES.map((file) => ({
          file,
          sha256: "c".repeat(64),
        })),
      }),
    /incomplete package attestation/,
  );
});

test("binary checksum parser rejects malformed or non-SHA256 records", () => {
  assert.deepEqual(
    parseChecksums(`${"a".repeat(64)}  relayfile-mount-linux-amd64\n`),
    [{ file: "relayfile-mount-linux-amd64", sha256: "a".repeat(64) }],
  );
  assert.throws(
    () => parseChecksums("not-a-checksum relayfile"),
    /invalid binary checksum/,
  );
});

test("attestation rejects missing, duplicate, or unexpected binaries", () => {
  const sourceSha = "a".repeat(40);
  const valid = RELEASE_BINARY_NAMES.map((file) => ({
    file,
    sha256: "c".repeat(64),
  }));
  for (const binaries of [
    valid.slice(0, -1),
    [...valid.slice(0, -1), valid[0]],
    [...valid.slice(0, -1), { file: "unexpected", sha256: "c".repeat(64) }],
  ]) {
    assert.throws(
      () =>
        buildReleaseAttestation({
          sourceSha,
          runId: 123,
          runAttempt: 1,
          version: "1.2.3",
          tag: "v1.2.3",
          tagCommit: "b".repeat(40),
          tagTree: "d".repeat(40),
          repository: "AgentWorkforce/relayfile",
          packages: packageRecords(sourceSha),
          binaries,
        }),
      /exactly/,
    );
  }
});
