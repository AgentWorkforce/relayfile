import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  assertInternalDeps,
  internalDeps,
  loadReleaseSet,
} from "./assert-internal-deps.mjs";

const REPO = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const WORKFLOW = readFileSync(join(REPO, ".github/workflows/publish.yml"), "utf8");

const sdk = {
  name: "@relayfile/sdk",
  version: "0.10.73",
  dependencies: { "@relayfile/core": "0.10.73", zod: "^3" },
};
const releaseSet = new Map([
  ["@relayfile/core", "0.10.73"],
  ["@relayfile/sdk", "0.10.73"],
]);

function clock() {
  let t = 0;
  return { now: () => t, wait: async (ms) => { t += ms; } };
}

test("a dependent is refused when its in-release dependency never reaches the registry", async () => {
  const result = await assertInternalDeps({
    pkg: sdk,
    releaseSet,
    resolves: async () => false, // core@0.10.73 failed to publish
    timeoutMs: 60_000,
    pollMs: 10_000,
    ...clock(),
  });
  assert.equal(result.ok, false);
  assert.match(result.problems[0], /@relayfile\/core@0\.10\.73.*did not appear/);
});

test("a dependent waits for its in-release dependency and then proceeds", async () => {
  let calls = 0;
  const result = await assertInternalDeps({
    pkg: sdk,
    releaseSet,
    resolves: async () => ++calls > 3,
    timeoutMs: 600_000,
    pollMs: 10_000,
    ...clock(),
  });
  assert.equal(result.ok, true);
});

test("a dependency outside the release that is not on the registry fails immediately", async () => {
  const result = await assertInternalDeps({
    pkg: { ...sdk, dependencies: { "@relayfile/ghost": "9.9.9" } },
    releaseSet,
    resolves: async () => false,
    ...clock(),
  });
  assert.equal(result.ok, false);
  assert.match(result.problems[0], /not published by this release/);
});

test("dry runs accept an in-release dependency without waiting", async () => {
  const result = await assertInternalDeps({
    pkg: sdk,
    releaseSet,
    dryRun: true,
    resolves: async () => false,
    ...clock(),
  });
  assert.equal(result.ok, true);
});

test("every internal dependency in the repo pins a release-set package", () => {
  const set = loadReleaseSet(REPO);
  assert.ok(set.has("@relayfile/core"));
  const sdkPkg = JSON.parse(
    readFileSync(join(REPO, "packages/sdk/typescript/package.json"), "utf8"),
  );
  const deps = internalDeps(sdkPkg).map((d) => d.name);
  assert.ok(deps.includes("@relayfile/core"));
  for (const name of deps) assert.ok(set.has(name), `${name} not in release set`);
});

test("both publish jobs run the dependency gate before reconciling/publishing", () => {
  const publishStart = WORKFLOW.indexOf("\n  publish-packages:");
  const createRelease = WORKFLOW.indexOf("\n  create-release:");
  const singleStart = WORKFLOW.indexOf("\n  publish-single:");
  for (const [start, end] of [
    [publishStart, singleStart],
    [singleStart, createRelease],
  ]) {
    const job = WORKFLOW.slice(start, end);
    const gate = job.indexOf("scripts/release/assert-internal-deps.mjs");
    const reconcile = job.indexOf("scripts/release/reconcile-package.mjs");
    assert.ok(gate > 0, "dependency gate missing from publish job");
    assert.ok(reconcile > gate, "gate must run before reconcile-package");
  }
});

test("publish matrix lists every internal dependency before its dependents", () => {
  const start = WORKFLOW.indexOf("\n  publish-packages:");
  const end = WORKFLOW.indexOf("\n  publish-single:");
  const paths = [...WORKFLOW.slice(start, end).matchAll(/path: (packages\/[^\n]+)/g)].map((m) => m[1]);
  const names = paths.map((p) =>
    JSON.parse(readFileSync(join(REPO, p, "package.json"), "utf8")).name,
  );
  names.forEach((name, i) => {
    const pkg = JSON.parse(readFileSync(join(REPO, paths[i], "package.json"), "utf8"));
    for (const dep of internalDeps(pkg)) {
      const j = names.indexOf(dep.name);
      if (j >= 0) assert.ok(j < i, `${name} is scheduled before its dependency ${dep.name}`);
    }
  });
});
