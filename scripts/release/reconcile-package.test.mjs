import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";

import {
  comparePackageContent,
  normalizePackRecord,
  registryErrorKind,
  reconcilePackage,
} from "./reconcile-package.mjs";

function sandbox() {
  const dir = mkdtempSync(join(tmpdir(), "relayfile-reconcile-"));
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify({ name: "@relayfile/test", version: "1.2.3" }),
  );
  return dir;
}

function fakeNpm({ state, registry, viewError }) {
  return async (command, args, { cwd }) => {
    assert.equal(command, "npm");
    if (args[0] === "pack") {
      const filename = "relayfile-test-1.2.3.tgz";
      writeFileSync(join(cwd, filename), "immutable package content");
      return {
        code: 0,
        stdout: JSON.stringify([
          {
            filename,
            name: "@relayfile/test",
            version: "1.2.3",
            size: 25,
            integrity: "sha512-local",
            shasum: "sha1-local",
          },
        ]),
        stderr: "",
      };
    }
    if (args[0] === "view") {
      state.views += 1;
      if (viewError) return { code: 1, stdout: "", stderr: viewError };
      return { code: 0, stdout: JSON.stringify(registry), stderr: "" };
    }
    if (args[0] === "publish") {
      state.publishes += 1;
      return { code: 0, stdout: "", stderr: "" };
    }
    throw new Error(`unexpected npm command: ${args.join(" ")}`);
  };
}

test("reconciliation publishes an absent version and verifies it afterwards", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  let first = true;
  const npm = async (command, args, options) => {
    if (args[0] === "view" && first) {
      first = false;
      state.views += 1;
      return { code: 1, stdout: "", stderr: "npm error code E404" };
    }
    return fakeNpm({
      state,
      registry: { integrity: "sha512-local", shasum: "sha1-local" },
    })(command, args, options);
  };
  const result = await reconcilePackage({
    packageDir: dir,
    tag: "next",
    sourceSha: "a".repeat(40),
    runId: 1,
    runAttempt: 1,
    npm,
    attempts: 1,
    sleep: async () => {},
  });
  assert.equal(result.package.status, "published");
  assert.equal(state.publishes, 1);
  assert.equal(state.views, 2);
  rmSync(dir, { recursive: true, force: true });
});

test("post-publish propagation retries an absent registry response", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const npm = async (command, args, { cwd }) => {
    assert.equal(command, "npm");
    if (args[0] === "pack") {
      writeFileSync(
        join(cwd, "relayfile-test-1.2.3.tgz"),
        "immutable package content",
      );
      return {
        code: 0,
        stdout: JSON.stringify([
          {
            filename: "relayfile-test-1.2.3.tgz",
            name: "@relayfile/test",
            version: "1.2.3",
            integrity: "sha512-local",
            shasum: "sha1-local",
          },
        ]),
        stderr: "",
      };
    }
    if (args[0] === "view") {
      state.views += 1;
      if (state.views <= 2)
        return { code: 1, stdout: "", stderr: "npm error code E404" };
      return {
        code: 0,
        stdout: JSON.stringify({
          integrity: "sha512-local",
          shasum: "sha1-local",
        }),
        stderr: "",
      };
    }
    if (args[0] === "publish") {
      state.publishes += 1;
      return { code: 0, stdout: "", stderr: "" };
    }
    throw new Error(`unexpected npm command: ${args.join(" ")}`);
  };
  const result = await reconcilePackage({
    packageDir: dir,
    tag: "next",
    sourceSha: "a".repeat(40),
    runId: 1,
    runAttempt: 1,
    npm,
    attempts: 2,
    sleep: async () => {},
  });
  assert.equal(result.package.status, "published");
  assert.equal(state.publishes, 1);
  assert.equal(state.views, 3);
  rmSync(dir, { recursive: true, force: true });
});

test("post-publish digest conflict fails closed without retrying", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  let firstView = true;
  const npm = async (command, args, { cwd }) => {
    assert.equal(command, "npm");
    if (args[0] === "pack") {
      writeFileSync(
        join(cwd, "relayfile-test-1.2.3.tgz"),
        "immutable package content",
      );
      return {
        code: 0,
        stdout: JSON.stringify([
          {
            filename: "relayfile-test-1.2.3.tgz",
            name: "@relayfile/test",
            version: "1.2.3",
            integrity: "sha512-local",
            shasum: "sha1-local",
          },
        ]),
        stderr: "",
      };
    }
    if (args[0] === "view") {
      state.views += 1;
      if (firstView) {
        firstView = false;
        return { code: 1, stdout: "", stderr: "npm error code E404" };
      }
      return {
        code: 0,
        stdout: JSON.stringify({
          integrity: "sha512-other",
          shasum: "sha1-other",
        }),
        stderr: "",
      };
    }
    if (args[0] === "publish") {
      state.publishes += 1;
      return { code: 0, stdout: "", stderr: "" };
    }
    throw new Error(`unexpected npm command: ${args.join(" ")}`);
  };
  await assert.rejects(
    reconcilePackage({
      packageDir: dir,
      tag: "next",
      sourceSha: "a".repeat(40),
      runId: 1,
      runAttempt: 1,
      npm,
      attempts: 5,
      sleep: async () => {},
    }),
    /post-publish verification failed/,
  );
  assert.equal(state.publishes, 1);
  assert.equal(state.views, 2);
  rmSync(dir, { recursive: true, force: true });
});

test("read-only preflight blocks every publish under mixed absent/conflict state", async () => {
  const absentDir = sandbox();
  const conflictDir = sandbox();
  const absentState = { views: 0, publishes: 0 };
  const conflictState = { views: 0, publishes: 0 };
  const npmFor = (state, mode) =>
    fakeNpm({
      state,
      registry:
        mode === "conflict"
          ? { integrity: "sha512-other", shasum: "sha1-other" }
          : { integrity: "sha512-local", shasum: "sha1-local" },
      viewError: mode === "absent" ? "npm error code E404" : undefined,
    });

  const results = await Promise.allSettled([
    reconcilePackage({
      packageDir: absentDir,
      tag: "next",
      sourceSha: "a".repeat(40),
      preflight: true,
      npm: npmFor(absentState, "absent"),
    }),
    reconcilePackage({
      packageDir: conflictDir,
      tag: "next",
      sourceSha: "b".repeat(40),
      preflight: true,
      npm: npmFor(conflictState, "conflict"),
    }),
  ]);

  assert.equal(results[0].status, "fulfilled");
  assert.equal(results[0].value.package.status, "absent");
  assert.equal(results[1].status, "rejected");
  assert.match(
    results[1].reason.message,
    /conflicts with the local release tarball/,
  );
  assert.equal(absentState.publishes, 0);
  assert.equal(conflictState.publishes, 0);
  rmSync(absentDir, { recursive: true, force: true });
  rmSync(conflictDir, { recursive: true, force: true });
});

test("reconciliation skips an identical already-published tarball", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const npm = fakeNpm({
    state,
    registry: { integrity: "sha512-local", shasum: "sha1-local" },
  });
  const result = await reconcilePackage({
    packageDir: dir,
    tag: "next",
    sourceSha: "b".repeat(40),
    runId: 1,
    runAttempt: 1,
    npm,
  });
  assert.equal(result.package.status, "already-published");
  assert.equal(state.publishes, 0);
  rmSync(dir, { recursive: true, force: true });
});

test("conflicting content fails closed without publishing", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const npm = fakeNpm({
    state,
    registry: { integrity: "sha512-other", shasum: "sha1-local" },
  });
  await assert.rejects(
    reconcilePackage({
      packageDir: dir,
      tag: "next",
      sourceSha: "c".repeat(40),
      npm,
    }),
    /conflicts with the local release tarball/,
  );
  assert.equal(state.publishes, 0);
  rmSync(dir, { recursive: true, force: true });
});

test("an ambiguous registry response fails closed", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const npm = fakeNpm({
    state,
    viewError: "npm error code E503 service unavailable",
  });
  await assert.rejects(
    reconcilePackage({
      packageDir: dir,
      tag: "next",
      sourceSha: "d".repeat(40),
      npm,
    }),
    /ambiguous/,
  );
  assert.equal(state.publishes, 0);
  rmSync(dir, { recursive: true, force: true });
});

test("registry errors are absent only for a canonical npm E404 response", () => {
  assert.equal(
    registryErrorKind({
      stdout: "",
      stderr: "npm error code E404\nnpm error 404 Not Found",
    }),
    "absent",
  );
  assert.equal(
    registryErrorKind({
      stdout: "",
      stderr: "npm error code E503\nnpm error 404 Not Found",
    }),
    "ambiguous",
  );
  assert.equal(
    registryErrorKind({ stdout: "HTTP 404", stderr: "npm error code E503" }),
    "ambiguous",
  );
});

test("content comparison requires at least one comparable digest", () => {
  assert.deepEqual(
    comparePackageContent(
      { integrity: null, shasum: null },
      { integrity: null, shasum: null },
    ),
    {
      kind: "ambiguous",
      reason: "registry and local content do not share a comparable digest",
    },
  );
  assert.equal(
    comparePackageContent(
      { integrity: "sha512-local", shasum: null },
      { integrity: null, shasum: "sha1-local" },
    ).kind,
    "ambiguous",
  );
});

test("normalizes npm 11 pack JSON object output", () => {
  const dir = sandbox();
  writeFileSync(
    join(dir, "relayfile-test-1.2.3.tgz"),
    "immutable package content",
  );
  const record = normalizePackRecord(
    {
      "@relayfile/test": {
        name: "@relayfile/test",
        version: "1.2.3",
        filename: "relayfile-test-1.2.3.tgz",
        integrity: "sha512-local",
      },
    },
    dir,
  );
  assert.equal(record.name, "@relayfile/test");
  assert.equal(record.filename, join(dir, "relayfile-test-1.2.3.tgz"));
  rmSync(dir, { recursive: true, force: true });
});

test("normalizes npm view dist JSON array output", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const npm = fakeNpm({
    state,
    registry: [{ integrity: "sha512-local", shasum: "sha1-local" }],
  });
  const result = await reconcilePackage({
    packageDir: dir,
    tag: "next",
    sourceSha: "e".repeat(40),
    npm,
  });
  assert.equal(result.package.status, "already-published");
  rmSync(dir, { recursive: true, force: true });
});
