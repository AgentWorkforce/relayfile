#!/usr/bin/env node
/** Build the machine-verifiable release record attached to a GitHub Release. */

import {
  readFileSync,
  realpathSync,
  writeFileSync,
  readdirSync,
} from "node:fs";
import { basename, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SHA256 = /^[0-9a-f]{64}$/;
const GIT_SHA = /^[0-9a-f]{40}$/;

/** npm's integrity field must contain the complete SHA-512 SRI digest. */
export function isSha512Integrity(value) {
  if (typeof value !== "string") return false;
  const match = /^sha512-([A-Za-z0-9+/]{86}==)$/.exec(value);
  if (!match) return false;
  const digest = Buffer.from(match[1], "base64");
  return digest.length === 64 && digest.toString("base64") === match[1];
}

/** npm's legacy shasum field is the lowercase hexadecimal SHA-1 digest. */
export function isSha1Shasum(value) {
  return typeof value === "string" && /^[0-9a-f]{40}$/.test(value);
}

/** Optional npm digest fields may be omitted/null, but never malformed. */
export function isOptionalSha512Integrity(value) {
  return value === null || value === undefined || isSha512Integrity(value);
}

/** Optional npm digest fields may be omitted/null, but never malformed. */
export function isOptionalSha1Shasum(value) {
  return value === null || value === undefined || isSha1Shasum(value);
}

// npm may omit its legacy SHA-1 shasum, but package identity must always be
// established by a canonical SHA-512 SRI value shared by the local tarball and
// registry response.  A shared shasum is retained as an auxiliary check only.
function hasValidPackageDigest(record) {
  return (
    !!record &&
    isOptionalSha512Integrity(record.integrity) &&
    isOptionalSha1Shasum(record.shasum) &&
    !!(record.integrity || record.shasum)
  );
}

function hasMatchingPackageDigest(local, registry) {
  return (
    !!local.integrity &&
    !!registry.integrity &&
    local.integrity === registry.integrity &&
    (!local.shasum || !registry.shasum || local.shasum === registry.shasum)
  );
}

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

/**
 * Validate one package-level release attestation, including its provenance
 * identity. Package records are composed into a top-level attestation, so a
 * valid digest alone is not enough: the child must describe this exact
 * release source and workflow attempt.
 */
export function isCompletePackageAttestation(
  item,
  { sourceSha, version, workflowRunId, workflowRunAttempt } = {},
) {
  const record = item?.package;
  const local = record?.local;
  const registry = record?.registry;
  if (
    item?.schemaVersion !== 1 ||
    item.kind !== "relayfileReleasePackage" ||
    item.sourceSha !== sourceSha ||
    item.workflowRunId !== String(workflowRunId) ||
    item.workflowRunAttempt !== String(workflowRunAttempt) ||
    !record ||
    !RELEASE_PACKAGE_NAMES.includes(record.name) ||
    record.version !== version ||
    !["published", "already-published"].includes(record.status) ||
    !local ||
    typeof local.file !== "string" ||
    !local.file ||
    !Number.isInteger(local.size) ||
    local.size < 0 ||
    !SHA256.test(String(local.sha256 ?? "")) ||
    !hasValidPackageDigest(local) ||
    !registry ||
    registry.name !== record.name ||
    registry.version !== record.version ||
    !hasValidPackageDigest(registry)
  ) {
    return false;
  }
  return hasMatchingPackageDigest(local, registry);
}

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
  tagTree,
  packages,
  binaries,
  repository,
}) {
  if (!GIT_SHA.test(sourceSha ?? ""))
    throw new Error("attestation source SHA is invalid");
  if (!GIT_SHA.test(tagCommit ?? ""))
    throw new Error("attestation tag commit is invalid");
  if (!GIT_SHA.test(tagTree ?? ""))
    throw new Error("attestation tag tree is invalid");
  if (!String(version ?? "").trim())
    throw new Error("attestation version is missing");
  if (tag !== `v${version}`)
    throw new Error("attestation tag/version mismatch");
  if (!/^\d+$/.test(String(runId)) || !/^\d+$/.test(String(runAttempt)))
    throw new Error("attestation workflow run identity is invalid");
  if (!Array.isArray(packages) || packages.length === 0)
    throw new Error("attestation has no package records");
  const names = new Set();
  for (const item of packages) {
    const record = item.package;
    if (!record?.name || !record.version || names.has(record.name)) {
      throw new Error("attestation package records are missing or duplicated");
    }
    if (!RELEASE_PACKAGE_NAMES.includes(record.name)) {
      throw new Error(`${record.name} is not a release package`);
    }
    if (record.version !== version)
      throw new Error(`${record.name} has the wrong release version`);
    if (!["published", "already-published"].includes(record.status)) {
      throw new Error(`${record.name} was not published or reconciled`);
    }
    if (!record.local?.sha256 || !SHA256.test(record.local.sha256)) {
      throw new Error(`${record.name} has no local tarball SHA-256`);
    }
    if (
      !isOptionalSha512Integrity(record.local.integrity) ||
      !isOptionalSha1Shasum(record.local.shasum) ||
      !isOptionalSha512Integrity(record.registry?.integrity) ||
      !isOptionalSha1Shasum(record.registry?.shasum)
    ) {
      throw new Error(`${record.name} has a malformed package digest`);
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
    const sharedIntegrity = record.registry.integrity && record.local.integrity;
    const sharedShasum = record.registry.shasum && record.local.shasum;
    if (!sharedIntegrity) {
      throw new Error(
        `${record.name} registry and local records have no common SHA-512 integrity`,
      );
    }
    if (
      sharedIntegrity &&
      record.registry.integrity !== record.local.integrity
    ) {
      throw new Error(
        `${record.name} registry integrity does not match the local tarball`,
      );
    }
    if (sharedShasum && record.registry.shasum !== record.local.shasum) {
      throw new Error(
        `${record.name} registry shasum does not match the local tarball`,
      );
    }
    if (item.sourceSha !== sourceSha)
      throw new Error(`${record.name} has a different source SHA`);
    if (
      !isCompletePackageAttestation(item, {
        sourceSha,
        version,
        workflowRunId: runId,
        workflowRunAttempt: runAttempt,
      })
    ) {
      throw new Error(`${record.name} has an incomplete package attestation`);
    }
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
    tag: { name: tag, commit: tagCommit, tree: tagTree },
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

let entrypoint = "";
try {
  entrypoint = process.argv[1] ? realpathSync(resolve(process.argv[1])) : "";
} catch {
  // Import contexts such as `node -` do not name a filesystem entrypoint.
}
if (entrypoint && fileURLToPath(import.meta.url) === entrypoint) {
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
      tagTree: args.tag_tree,
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
