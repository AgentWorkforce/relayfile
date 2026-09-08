import test from "node:test";
import assert from "node:assert/strict";

import {
  buildReleaseAttestation,
  parseChecksums,
  RELEASE_BINARY_NAMES,
  RELEASE_PACKAGE_NAMES,
} from "./create-release-attestation.mjs";

function packageRecords(sourceSha, version = "1.2.3") {
  return RELEASE_PACKAGE_NAMES.map((name) => ({
    sourceSha,
    package: {
      name,
      version,
      status: "already-published",
      local: {
        sha256: "a".repeat(64),
        integrity: "sha512-local",
        shasum: "sha1-local",
      },
      registry: { integrity: "sha512-local" },
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
    packages: packageRecords(sourceSha),
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

test("attestation rejects a package from a different source or missing package", () => {
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
        packages: packageRecords("d".repeat(40)).slice(0, -1),
        binaries: RELEASE_BINARY_NAMES.map((file) => ({
          file,
          sha256: "c".repeat(64),
        })),
      }),
    /different source SHA|missing packages/,
  );
});

test("attestation rejects registry content that differs from the local tarball", () => {
  const sourceSha = "a".repeat(40);
  const packages = packageRecords(sourceSha);
  packages[0].package.registry.integrity = "sha512-conflict";
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
