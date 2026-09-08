#!/usr/bin/env node
/** Build the machine-verifiable release record attached to a GitHub Release. */

import { readFileSync, writeFileSync, readdirSync } from "node:fs";
import { basename, resolve } from "node:path";

const SHA256 = /^[0-9a-f]{64}$/;
const GIT_SHA = /^[0-9a-f]{40}$/;

export const RELEASE_PACKAGE_NAMES = [
  "@relayfile/core",
  "@relayfile/sdk",
  "@relayfile/client",
  "@relayfile/agents",
  "relayfile",
  "@relayfile/file-observer",
  "@relayfile/local-mount",
  "@relayfile/mount-darwin-arm64",
  "@relayfile/mount-darwin-x64",
  "@relayfile/mount-linux-arm64",
  "@relayfile/mount-linux-x64",
];

export const RELEASE_BINARY_NAMES = [
  "relayfile-mount-linux-amd64",
  "relayfile-mount-linux-arm64",
  "relayfile-mount-darwin-amd64",
  "relayfile-mount-darwin-arm64",
  "relayfile-cli-linux-amd64",
  "relayfile-cli-linux-arm64",
  "relayfile-cli-darwin-amd64",
  "relayfile-cli-darwin-arm64",
  "relayfile-cli-windows-amd64.exe",
  "relayfile-cli-windows-arm64.exe",
];

export function readPackageAttestations(directory) {
  return readdirSync(directory)
    .filter((file) => file.endsWith(".json"))
    .sort()
    .map((file) => JSON.parse(readFileSync(resolve(directory, file), "utf8")));
}

export function parseChecksums(text) {
  return String(text ?? "")
    .trim()
    .split(/\r?\n/)
    .filter(Boolean)
    .map((line) => {
      const match = line.match(/^([0-9a-f]{64})\s+(.+)$/);
      if (!match) throw new Error(`invalid binary checksum line: ${line}`);
      return { file: basename(match[2]), sha256: match[1] };
    });
}

export function buildReleaseAttestation({
  sourceSha,
  runId,
  runAttempt,
  version,
  tag,
  tagCommit,
  packages,
  binaries,
  repository,
}) {
  if (!GIT_SHA.test(sourceSha ?? ""))
    throw new Error("attestation source SHA is invalid");
  if (!GIT_SHA.test(tagCommit ?? ""))
    throw new Error("attestation tag commit is invalid");
  if (!String(version ?? "").trim())
    throw new Error("attestation version is missing");
  if (tag !== `v${version}`)
    throw new Error("attestation tag/version mismatch");
  if (!Array.isArray(packages) || packages.length === 0)
    throw new Error("attestation has no package records");
  const names = new Set();
  for (const item of packages) {
    const record = item.package;
    if (!record?.name || !record.version || names.has(record.name)) {
      throw new Error("attestation package records are missing or duplicated");
    }
    if (record.version !== version)
      throw new Error(`${record.name} has the wrong release version`);
    if (!["published", "already-published"].includes(record.status)) {
      throw new Error(`${record.name} was not published or reconciled`);
    }
    if (!record.local?.sha256 || !SHA256.test(record.local.sha256)) {
      throw new Error(`${record.name} has no local tarball SHA-256`);
    }
    if (!record.registry?.integrity && !record.registry?.shasum) {
      throw new Error(`${record.name} has no registry digest`);
    }
    if (record.registry.name && record.registry.name !== record.name) {
      throw new Error(
        `${record.name} registry name does not match the package`,
      );
    }
    if (record.registry.version && record.registry.version !== record.version) {
      throw new Error(
        `${record.name} registry version does not match the package`,
      );
    }
    if (
      record.registry.integrity &&
      record.local.integrity &&
      record.registry.integrity !== record.local.integrity
    ) {
      throw new Error(
        `${record.name} registry integrity does not match the local tarball`,
      );
    }
    if (
      record.registry.shasum &&
      record.local.shasum &&
      record.registry.shasum !== record.local.shasum
    ) {
      throw new Error(
        `${record.name} registry shasum does not match the local tarball`,
      );
    }
    if (item.sourceSha !== sourceSha)
      throw new Error(`${record.name} has a different source SHA`);
    names.add(record.name);
  }
  const missing = RELEASE_PACKAGE_NAMES.filter((name) => !names.has(name));
  if (missing.length)
    throw new Error(`attestation is missing packages: ${missing.join(", ")}`);
  const actualBinaries = (binaries ?? []).map((binary) => binary.file);
  const expectedBinaries = [...RELEASE_BINARY_NAMES].sort();
  const actualUniqueBinaries = [...new Set(actualBinaries)].sort();
  if (
    actualBinaries.length !== RELEASE_BINARY_NAMES.length ||
    actualUniqueBinaries.length !== actualBinaries.length ||
    actualUniqueBinaries.some((name, index) => name !== expectedBinaries[index])
  ) {
    throw new Error(
      `attestation binary set must contain exactly: ${RELEASE_BINARY_NAMES.join(", ")}`,
    );
  }
  for (const binary of binaries ?? []) {
    if (!binary.file || !SHA256.test(binary.sha256))
      throw new Error("attestation binary checksum is invalid");
  }

  const versionMap = Object.fromEntries(
    [...names].sort().map((name) => {
      const record = packages.find(
        (item) => item.package.name === name,
      ).package;
      return [name, record.version];
    }),
  );
  return {
    schemaVersion: 1,
    kind: "relayfileRelease",
    producer: {
      repository,
      workflow: "Publish Package",
      workflowPath: ".github/workflows/publish.yml",
      workflowRunId: String(runId),
      workflowRunAttempt: String(runAttempt),
    },
    sourceSha,
    version,
    versions: versionMap,
    packages,
    binaries: binaries ?? [],
    tag: { name: tag, commit: tagCommit },
  };
}

function parseArgs(argv) {
  const values = {};
  for (let i = 0; i < argv.length; i += 1) {
    const key = argv[i].replace(/^--/, "").replaceAll("-", "_");
    values[key] = argv[++i];
  }
  return values;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const args = parseArgs(process.argv.slice(2));
  try {
    const packages = readPackageAttestations(args.package_dir);
    const binaries = parseChecksums(readFileSync(args.checksums, "utf8"));
    const attestation = buildReleaseAttestation({
      sourceSha: args.source_sha,
      runId: args.run_id,
      runAttempt: args.run_attempt,
      version: args.version,
      tag: args.tag,
      tagCommit: args.tag_commit,
      packages,
      binaries,
      repository: args.repository,
    });
    writeFileSync(args.output, `${JSON.stringify(attestation, null, 2)}\n`);
  } catch (error) {
    console.error(
      error instanceof Error ? error.message : "release attestation failed",
    );
    process.exitCode = 1;
  }
}
