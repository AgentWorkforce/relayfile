import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";

import {
  backoffDelay,
  comparePackageContent,
  normalizePackRecord,
  normalizeRegistryRecord,
  REGISTRY_FETCH_RETRIES,
  REGISTRY_FETCH_RETRY_MAX_TIMEOUT_MS,
  REGISTRY_FETCH_RETRY_MIN_TIMEOUT_MS,
  REGISTRY_QUERY_TIMEOUT_MS,
  registryErrorKind,
  reconcilePackage,
} from "./reconcile-package.mjs";

const VALID_INTEGRITY = `sha512-${"A".repeat(86)}==`;
const OTHER_INTEGRITY = `sha512-${"A".repeat(85)}Q==`;
const VALID_SHASUM = "a".repeat(40);
const OTHER_SHASUM = "b".repeat(40);

function sandbox() {
  const dir = mkdtempSync(join(tmpdir(), "relayfile-reconcile-"));
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify({ name: "@relayfile/test", version: "1.2.3" }),
  );
  return dir;
}

function fakeNpm({ state, registry, viewError, onView }) {
  return async (command, args, options) => {
    const { cwd } = options;
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
            integrity: VALID_INTEGRITY,
            shasum: VALID_SHASUM,
          },
        ]),
        stderr: "",
      };
    }
    if (args[0] === "view") {
      state.views += 1;
      onView?.(args, options);
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

test("consumer publication waits until its required internal dependency is visible", async () => {
  const dir = sandbox();
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify({
      name: "@relayfile/test",
      version: "1.2.3",
      dependencies: { "@relayfile/core": "1.2.3", external: "^1.0.0" },
      optionalDependencies: { "@relayfile/mount-linux-arm64": "1.2.3" },
      devDependencies: { "@relayfile/sdk": "1.2.3" },
    }),
  );
  const state = { views: 0, publishes: 0 };
  let dependencyViews = 0;
  const waits = [];
  const npm = async (command, args, options) => {
    if (args[0] === "view" && args[1] === "@relayfile/core@1.2.3") {
      dependencyViews += 1;
      return dependencyViews === 1
        ? { code: 1, stdout: "", stderr: "npm error code E404" }
        : {
            code: 0,
            stdout: JSON.stringify({ integrity: VALID_INTEGRITY }),
            stderr: "",
          };
    }
    if (args[0] === "view" && state.publishes === 0) {
      return { code: 1, stdout: "", stderr: "npm error code E404" };
    }
    if (args[0] === "publish") assert.equal(dependencyViews, 2);
    return fakeNpm({ state, registry: { integrity: VALID_INTEGRITY } })(
      command,
      args,
      options,
    );
  };
  try {
    await reconcilePackage({
      packageDir: dir,
      tag: "latest",
      sourceSha: "a".repeat(40),
      npm,
      sleep: async (ms) => waits.push(ms),
    });
    assert.equal(state.publishes, 1);
    assert.equal(dependencyViews, 2);
    assert.deepEqual(waits, [5000]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("missing or ambiguous internal dependencies cannot publish a consumer", async () => {
  for (const dependencyError of [
    "npm error code E404",
    "npm error code E503",
  ]) {
    const dir = sandbox();
    writeFileSync(
      join(dir, "package.json"),
      JSON.stringify({
        name: "@relayfile/test",
        version: "1.2.3",
        dependencies: { "@relayfile/core": "1.2.3" },
      }),
    );
    const state = { views: 0, publishes: 0 };
    const npm = async (command, args, options) => {
      if (args[0] === "view")
        return {
          code: 1,
          stdout: "",
          stderr:
            args[1] === "@relayfile/core@1.2.3"
              ? dependencyError
              : "npm error code E404",
        };
      return fakeNpm({ state })(command, args, options);
    };
    try {
      await assert.rejects(
        reconcilePackage({
          packageDir: dir,
          tag: "latest",
          npm,
          dependencyAttempts: 2,
          sleep: async () => {},
        }),
        /registry query|dependency.*not visible/,
      );
      assert.equal(state.publishes, 0);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  }
});

test("read-only preflight and dry run do not wait on unpublished release dependencies", async () => {
  for (const flags of [{ preflight: true }, { dryRun: true }]) {
    const dir = sandbox();
    writeFileSync(
      join(dir, "package.json"),
      JSON.stringify({
        name: "@relayfile/test",
        version: "1.2.3",
        dependencies: { "@relayfile/core": "1.2.3" },
      }),
    );
    const state = { views: 0, publishes: 0 };
    const npm = fakeNpm({
      state,
      viewError: "npm error code E404",
      onView: (args) => assert.equal(args[1], "@relayfile/test@1.2.3"),
    });
    try {
      await reconcilePackage({ packageDir: dir, tag: "latest", npm, ...flags });
      assert.equal(state.publishes, 0);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  }
});

test("dependency queries and retry delays share one bounded wait budget", async () => {
  const dir = sandbox();
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify({
      name: "@relayfile/test",
      version: "1.2.3",
      dependencies: { "@relayfile/core": "1.2.3" },
    }),
  );
  const state = { views: 0, publishes: 0 };
  let elapsedMs = 0;
  const queryCaps = [];
  const waits = [];
  const npm = async (command, args, options) => {
    if (args[0] === "view") {
      if (args[1] === "@relayfile/core@1.2.3") {
        queryCaps.push(options.timeout);
        elapsedMs += 3000;
      }
      return { code: 1, stdout: "", stderr: "npm error code E404" };
    }
    return fakeNpm({ state })(command, args, options);
  };
  try {
    await assert.rejects(
      reconcilePackage({
        packageDir: dir,
        tag: "latest",
        npm,
        dependencyWaitBudgetMs: 12000,
        now: () => elapsedMs,
        sleep: async (ms) => {
          waits.push(ms);
          elapsedMs += ms;
        },
      }),
      /dependency.*not visible/,
    );
    assert.deepEqual(queryCaps, [12000, 4000]);
    assert.deepEqual(waits, [5000, 1000]);
    assert.equal(elapsedMs, 12000);
    assert.equal(state.publishes, 0);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("required SDK peers wait on their version floor, optional peers do not", async () => {
  const dir = sandbox();
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify({
      name: "@relayfile/test",
      version: "1.2.3",
      peerDependencies: {
        "@relayfile/sdk": "^1.2.3",
        "@relayfile/optional": "^1.2.3",
      },
      peerDependenciesMeta: { "@relayfile/optional": { optional: true } },
    }),
  );
  const state = { views: 0, publishes: 0 };
  let sdkViews = 0;
  const npm = async (command, args, options) => {
    if (args[0] === "view") {
      assert.notEqual(args[1], "@relayfile/optional@1.2.3");
      if (args[1] === "@relayfile/sdk@1.2.3") {
        sdkViews += 1;
        if (sdkViews === 1)
          return { code: 1, stdout: "", stderr: "npm error code E404" };
        return {
          code: 0,
          stdout: JSON.stringify({ integrity: VALID_INTEGRITY }),
          stderr: "",
        };
      }
      if (state.publishes === 0)
        return { code: 1, stdout: "", stderr: "npm error code E404" };
    }
    if (args[0] === "publish") assert.equal(sdkViews, 2);
    return fakeNpm({ state, registry: { integrity: VALID_INTEGRITY } })(
      command,
      args,
      options,
    );
  };
  try {
    await reconcilePackage({
      packageDir: dir,
      tag: "latest",
      npm,
      sleep: async () => {},
    });
    assert.equal(state.publishes, 1);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("dependency pins preserve valid prerelease and build metadata", async () => {
  const dir = sandbox();
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify({
      name: "@relayfile/test",
      version: "1.2.3",
      dependencies: { "@relayfile/core": "1.2.3-rc.1+build.1" },
    }),
  );
  const state = { views: 0, publishes: 0 };
  let dependencySeen = false;
  const npm = async (command, args, options) => {
    if (args[0] === "view") {
      if (args[1] === "@relayfile/core@1.2.3-rc.1+build.1") {
        dependencySeen = true;
        return {
          code: 0,
          stdout: JSON.stringify({ integrity: VALID_INTEGRITY }),
          stderr: "",
        };
      }
      if (state.publishes === 0)
        return { code: 1, stdout: "", stderr: "npm error code E404" };
    }
    if (args[0] === "publish") assert.equal(dependencySeen, true);
    return fakeNpm({ state, registry: { integrity: VALID_INTEGRITY } })(
      command,
      args,
      options,
    );
  };
  try {
    await reconcilePackage({ packageDir: dir, tag: "latest", npm });
    assert.equal(state.publishes, 1);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

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
      registry: { integrity: VALID_INTEGRITY, shasum: VALID_SHASUM },
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

test("reconciliation rejects SHA-1-only local pack identity before publishing", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const npm = async (command, args, { cwd }) => {
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
            shasum: VALID_SHASUM,
          },
        ]),
        stderr: "",
      };
    }
    if (args[0] === "view") state.views += 1;
    if (args[0] === "publish") state.publishes += 1;
    throw new Error(`unexpected npm command: ${args.join(" ")}`);
  };

  await assert.rejects(
    reconcilePackage({
      packageDir: dir,
      tag: "next",
      sourceSha: "a".repeat(40),
      npm,
    }),
    /no SHA-512 integrity/,
  );
  assert.deepEqual(state, { views: 0, publishes: 0 });
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
            integrity: VALID_INTEGRITY,
            shasum: VALID_SHASUM,
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
          integrity: VALID_INTEGRITY,
          shasum: VALID_SHASUM,
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

test("post-publish verification retries immediately when delay is zero", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const sleeps = [];
  const npm = async (command, args, options) => {
    if (args[0] === "view" && state.views < 2) {
      state.views += 1;
      return { code: 1, stdout: "", stderr: "npm error code E404" };
    }
    return fakeNpm({
      state,
      registry: { integrity: VALID_INTEGRITY, shasum: VALID_SHASUM },
    })(command, args, options);
  };
  const result = await reconcilePackage({
    packageDir: dir,
    tag: "next",
    sourceSha: "a".repeat(40),
    npm,
    attempts: 2,
    delayMs: 0,
    sleep: async (delay) => sleeps.push(delay),
  });
  assert.equal(result.package.status, "published");
  assert.deepEqual(state, { views: 3, publishes: 1 });
  assert.deepEqual(sleeps, []);
  rmSync(dir, { recursive: true, force: true });
});

test("invalid registry query timeouts fail before npm is invoked", async () => {
  for (const registryQueryTimeoutMs of [
    0,
    Number.NaN,
    Number.POSITIVE_INFINITY,
  ]) {
    const dir = sandbox();
    let npmCalls = 0;
    await assert.rejects(
      reconcilePackage({
        packageDir: dir,
        tag: "next",
        sourceSha: "a".repeat(40),
        registryQueryTimeoutMs,
        npm: async () => {
          npmCalls += 1;
          throw new Error("npm must not be invoked");
        },
      }),
      /registry query timeout must be a finite positive number/,
    );
    assert.equal(npmCalls, 0);
    rmSync(dir, { recursive: true, force: true });
  }
});

test("post-publish verification rejects an empty registry shasum", async () => {
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
            integrity: VALID_INTEGRITY,
            shasum: VALID_SHASUM,
          },
        ]),
        stderr: "",
      };
    }
    if (args[0] === "view") {
      state.views += 1;
      if (state.views === 1)
        return { code: 1, stdout: "", stderr: "npm error code E404" };
      return {
        code: 0,
        stdout: JSON.stringify({ integrity: VALID_INTEGRITY, shasum: "" }),
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
      attempts: 1,
      sleep: async () => {},
    }),
    /post-publish verification failed.*no usable digest/,
  );
  assert.equal(state.publishes, 1);
  assert.equal(state.views, 2);
  rmSync(dir, { recursive: true, force: true });
});

test("post-publish propagation retry delays are capped per wait and in total", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const delays = [];
  await assert.rejects(
    reconcilePackage({
      packageDir: dir,
      tag: "next",
      sourceSha: "a".repeat(40),
      runId: 1,
      runAttempt: 1,
      npm: fakeNpm({
        state,
        registry: { integrity: VALID_INTEGRITY, shasum: VALID_SHASUM },
        viewError: "npm error code E404",
      }),
      attempts: 99,
      delayMs: 5,
      maxDelayMs: 10,
      maxTotalRetryDelayMs: 23,
      now: () => 0,
      sleep: async (delay) => delays.push(delay),
    }),
    /post-publish verification failed/,
  );
  assert.deepEqual(delays, [5, 10, 8]);
  assert.equal(
    delays.reduce((total, delay) => total + delay, 0),
    23,
  );
  assert.equal(backoffDelay({ attempt: 9 }), 30000);
  rmSync(dir, { recursive: true, force: true });
});

test("registry commands and their retry time share the total retry budget", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const timeouts = [];
  const argsSeen = [];
  const delays = [];
  let clock = 0;
  const npm = fakeNpm({
    state,
    registry: { integrity: VALID_INTEGRITY, shasum: VALID_SHASUM },
    viewError: "npm error code E404",
    onView: (args, options) => {
      argsSeen.push(args);
      timeouts.push(options.timeout);
      if (state.views > 1) clock += Math.min(9, options.timeout);
    },
  });

  await assert.rejects(
    reconcilePackage({
      packageDir: dir,
      tag: "next",
      sourceSha: "a".repeat(40),
      runId: 1,
      runAttempt: 1,
      npm,
      attempts: 99,
      delayMs: 5,
      maxDelayMs: 10,
      maxTotalRetryDelayMs: 23,
      now: () => clock,
      sleep: async (delay) => {
        delays.push(delay);
        clock += delay;
      },
    }),
    /post-publish verification failed/,
  );

  assert.deepEqual(timeouts, [REGISTRY_QUERY_TIMEOUT_MS, 23, 9]);
  assert.deepEqual(delays, [5]);
  for (const args of argsSeen) {
    assert.ok(args.includes(`--fetch-retries=${REGISTRY_FETCH_RETRIES}`));
    assert.ok(
      args.includes(
        `--fetch-retry-mintimeout=${REGISTRY_FETCH_RETRY_MIN_TIMEOUT_MS}`,
      ),
    );
    assert.ok(
      args.includes(
        `--fetch-retry-maxtimeout=${REGISTRY_FETCH_RETRY_MAX_TIMEOUT_MS}`,
      ),
    );
  }
  rmSync(dir, { recursive: true, force: true });
});

test("overscheduled sleeps charge their actual elapsed retry time", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const timeouts = [];
  const delays = [];
  let clock = 0;
  const npm = fakeNpm({
    state,
    registry: { integrity: VALID_INTEGRITY, shasum: VALID_SHASUM },
    viewError: "npm error code E404",
    onView: (_args, options) => timeouts.push(options.timeout),
  });

  await assert.rejects(
    reconcilePackage({
      packageDir: dir,
      tag: "next",
      sourceSha: "a".repeat(40),
      runId: 1,
      runAttempt: 1,
      npm,
      attempts: 99,
      delayMs: 5,
      maxDelayMs: 10,
      maxTotalRetryDelayMs: 20,
      now: () => clock,
      sleep: async (delay) => {
        delays.push(delay);
        clock += delay + 10;
      },
    }),
    /post-publish verification failed/,
  );

  assert.deepEqual(timeouts, [REGISTRY_QUERY_TIMEOUT_MS, 20, 5]);
  assert.deepEqual(delays, [5, 5]);
  assert.equal(clock, 30);
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
            integrity: VALID_INTEGRITY,
            shasum: VALID_SHASUM,
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
          integrity: OTHER_INTEGRITY,
          shasum: OTHER_SHASUM,
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
          ? { integrity: OTHER_INTEGRITY, shasum: OTHER_SHASUM }
          : { integrity: VALID_INTEGRITY, shasum: VALID_SHASUM },
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
    registry: { integrity: VALID_INTEGRITY, shasum: null },
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

test("reconciliation rejects SHA-1-only registry identity", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  await assert.rejects(
    reconcilePackage({
      packageDir: dir,
      tag: "next",
      sourceSha: "b".repeat(40),
      npm: fakeNpm({
        state,
        registry: { integrity: null, shasum: VALID_SHASUM },
      }),
    }),
    /no usable digest|comparable SHA-512 integrity/,
  );
  assert.equal(state.publishes, 0);
  rmSync(dir, { recursive: true, force: true });
});

test("conflicting content fails closed without publishing", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const npm = fakeNpm({
    state,
    registry: { integrity: OTHER_INTEGRITY, shasum: VALID_SHASUM },
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
      reason:
        "registry and local content do not share a comparable SHA-512 integrity",
    },
  );
  assert.equal(
    comparePackageContent(
      { integrity: VALID_INTEGRITY, shasum: null },
      { integrity: null, shasum: VALID_SHASUM },
    ).kind,
    "ambiguous",
  );
});

test("content comparison rejects SHA-1-only identity and accepts integrity-only identity", () => {
  assert.equal(
    comparePackageContent(
      { integrity: null, shasum: VALID_SHASUM },
      { integrity: null, shasum: VALID_SHASUM },
    ).kind,
    "ambiguous",
  );
  assert.equal(
    comparePackageContent(
      { integrity: VALID_INTEGRITY, shasum: null },
      { integrity: VALID_INTEGRITY, shasum: null },
    ).kind,
    "identical",
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
        integrity: VALID_INTEGRITY,
      },
    },
    dir,
  );
  assert.equal(record.name, "@relayfile/test");
  assert.equal(record.filename, join(dir, "relayfile-test-1.2.3.tgz"));
  rmSync(dir, { recursive: true, force: true });
});

test("normalizePackRecord rejects an empty shasum beside valid integrity", () => {
  const dir = sandbox();
  writeFileSync(
    join(dir, "relayfile-test-1.2.3.tgz"),
    "immutable package content",
  );
  assert.throws(
    () =>
      normalizePackRecord(
        {
          filename: "relayfile-test-1.2.3.tgz",
          name: "@relayfile/test",
          version: "1.2.3",
          integrity: VALID_INTEGRITY,
          shasum: "",
        },
        dir,
      ),
    /malformed SHA-1 shasum/,
  );
  rmSync(dir, { recursive: true, force: true });
});

test("normalizePackRecord rejects a malformed shasum beside valid integrity", () => {
  const dir = sandbox();
  writeFileSync(
    join(dir, "relayfile-test-1.2.3.tgz"),
    "immutable package content",
  );
  assert.throws(
    () =>
      normalizePackRecord(
        {
          filename: "relayfile-test-1.2.3.tgz",
          name: "@relayfile/test",
          version: "1.2.3",
          integrity: VALID_INTEGRITY,
          shasum: "not-a-sha1",
        },
        dir,
      ),
    /malformed SHA-1 shasum/,
  );
  rmSync(dir, { recursive: true, force: true });
});

test("normalizes npm view dist JSON array output", async () => {
  const dir = sandbox();
  const state = { views: 0, publishes: 0 };
  const npm = fakeNpm({
    state,
    registry: [{ integrity: VALID_INTEGRITY, shasum: VALID_SHASUM }],
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

test("rejects ambiguous or wrong-version registry records", () => {
  assert.equal(
    normalizeRegistryRecord(
      [{ integrity: VALID_INTEGRITY }, { integrity: OTHER_INTEGRITY }],
      { name: "@relayfile/test", version: "1.2.3" },
    ),
    null,
  );
  assert.equal(
    normalizeRegistryRecord(
      {
        name: "@relayfile/test",
        version: "1.2.4",
        integrity: VALID_INTEGRITY,
      },
      { name: "@relayfile/test", version: "1.2.3" },
    ),
    null,
  );
});

test("normalizeRegistryRecord rejects an empty shasum beside valid integrity", () => {
  assert.equal(
    normalizeRegistryRecord(
      {
        name: "@relayfile/test",
        version: "1.2.3",
        integrity: VALID_INTEGRITY,
        shasum: "",
      },
      { name: "@relayfile/test", version: "1.2.3" },
    ),
    null,
  );
});

test("normalizeRegistryRecord rejects a malformed shasum beside valid integrity", () => {
  assert.equal(
    normalizeRegistryRecord(
      {
        name: "@relayfile/test",
        version: "1.2.3",
        integrity: VALID_INTEGRITY,
        shasum: "not-a-sha1",
      },
      { name: "@relayfile/test", version: "1.2.3" },
    ),
    null,
  );
});
