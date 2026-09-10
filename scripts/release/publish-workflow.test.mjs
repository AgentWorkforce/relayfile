/**
 * Contract tests for the release-critical shell in .github/workflows/publish.yml.
 *
 * These extract the real shipped shell and assert that release invariants stay
 * wired into the workflow, while the registry reconciliation behavior has its
 * own executable tests in reconcile-package.test.mjs.
 */
import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { join, dirname } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath, pathToFileURL } from "node:url";
import {
  RELEASE_BINARY_NAMES,
  RELEASE_PACKAGE_NAMES,
} from "./create-release-attestation.mjs";

const VALID_INTEGRITY = `sha512-${"A".repeat(86)}==`;
const VALID_SHASUM = "a".repeat(40);
const OTHER_INTEGRITY = `sha512-${"A".repeat(85)}Q==`;
const OTHER_SHASUM = "b".repeat(40);

const REPO = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const WORKFLOW = readFileSync(
  join(REPO, ".github/workflows/publish.yml"),
  "utf8",
);
const PYTHON_WORKFLOW = readFileSync(
  join(REPO, ".github/workflows/publish-python.yml"),
  "utf8",
);
const PYTHON_BASELINE = readFileSync(
  join(REPO, "scripts/release/resolve-python-release-baseline.mjs"),
  "utf8",
);

test("release modules are import-safe when the argv entrypoint is stdin", () => {
  for (const script of [
    "create-release-attestation.mjs",
    "reconcile-package.mjs",
    "resolve-release-baseline.mjs",
    "resolve-python-release-baseline.mjs",
  ]) {
    const moduleUrl = pathToFileURL(join(REPO, "scripts", "release", script));
    const result = spawnSync(process.execPath, ["--input-type=module", "-"], {
      input: `await import(${JSON.stringify(moduleUrl.href)});\n`,
      encoding: "utf8",
    });
    assert.equal(result.status, 0, `${script}: ${result.stderr}`);
  }
});

/** The package.json paths the release versions and publishes. */
const EXPECTED_PACKAGE_PATHS = [
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

function dedent(block) {
  return block
    .split("\n")
    .map((line) => line.replace(/^ {10}/, ""))
    .join("\n");
}

function workflowJob(name) {
  const marker = `  ${name}:\n`;
  const start = WORKFLOW.indexOf(marker);
  assert.notEqual(start, -1, `missing ${name} workflow job`);
  const bodyStart = start + marker.length;
  const nextJob = WORKFLOW.slice(bodyStart).match(/^  [A-Za-z0-9_-]+:\n/m);
  const end = nextJob ? bodyStart + nextJob.index : WORKFLOW.length;
  return WORKFLOW.slice(start, end);
}

function extractStepRun(name) {
  return extractStepRunFrom(WORKFLOW, name);
}

function extractStepRunFrom(workflow, name) {
  const marker = `      - name: ${name}`;
  const step = workflow.indexOf(marker);
  assert.notEqual(step, -1, `${name} step not found`);
  const run = workflow.indexOf("\n        run: |", step);
  assert.notEqual(run, -1, `${name} run block not found`);
  const bodyStart = run + "\n        run: |".length + 1;
  const bodyEnd = workflow.indexOf("\n      - name:", bodyStart);
  assert.notEqual(bodyEnd, -1, `${name} run block is unterminated`);
  return dedent(workflow.slice(bodyStart, bodyEnd));
}

function extractPythonStepRun(name) {
  return extractStepRunFrom(PYTHON_WORKFLOW, name);
}

function runBash(script, { cwd, env = {} }) {
  try {
    return {
      status: 0,
      stdout: execFileSync("bash", ["-c", `set -euo pipefail\n${script}`], {
        cwd,
        env: { ...process.env, ...env },
        encoding: "utf8",
      }),
    };
  } catch (error) {
    return {
      status: error.status ?? 1,
      stdout: `${error.stdout ?? ""}${error.stderr ?? ""}`,
    };
  }
}

function runPythonPypiState(statuses, { recovery = "false" } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "relayfile-python-pypi-state-"));
  const bin = join(dir, "bin");
  const output = join(dir, "output");
  mkdirSync(bin);
  writeFileSync(join(dir, "statuses"), `${statuses.join("\n")}\n`);
  writeFileSync(join(dir, "cursor"), "1\n");
  writeFileSync(
    join(bin, "curl"),
    '#!/bin/sh\n' +
      'n=$(cat "$PYPI_CURSOR")\n' +
      'status=$(sed -n "${n}p" "$PYPI_STATUSES")\n' +
      'printf "%s" "$((n + 1))" > "$PYPI_CURSOR"\n' +
      'printf "%s" "${status:-000}"\n',
  );
  writeFileSync(join(bin, "sleep"), "#!/bin/sh\nexit 0\n");
  chmodSync(join(bin, "curl"), 0o755);
  chmodSync(join(bin, "sleep"), 0o755);
  try {
    const result = runBash(extractPythonStepRun("Check PyPI version state"), {
      cwd: REPO,
      env: {
        NEW_VERSION: "1.2.4",
        RELEASE_RECOVERY: recovery,
        GITHUB_OUTPUT: output,
        PYPI_STATUSES: join(dir, "statuses"),
        PYPI_CURSOR: join(dir, "cursor"),
        PATH: `${bin}:${process.env.PATH}`,
      },
    });
    return {
      ...result,
      outputFile: existsSync(output) ? readFileSync(output, "utf8") : "",
    };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function runDispatchValidation({ packageInput, dryRunInput }) {
  const dir = mkdtempSync(join(tmpdir(), "relayfile-dispatch-"));
  const output = join(dir, "output");
  const envOutput = join(dir, "env");
  try {
    const result = runBash(extractStepRun("Validate and map dispatch inputs"), {
      cwd: REPO,
      env: {
        PACKAGE_INPUT: packageInput,
        DRY_RUN_INPUT: dryRunInput,
        GITHUB_OUTPUT: output,
        GITHUB_ENV: envOutput,
      },
    });
    return {
      ...result,
      outputFile: existsSync(output) ? readFileSync(output, "utf8") : "",
      envFile: existsSync(envOutput) ? readFileSync(envOutput, "utf8") : "",
    };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function runVersionStep({
  customVersion = "",
  versionType = "patch",
  npmStub = false,
}) {
  const dir = mkdtempSync(join(tmpdir(), "relayfile-version-"));
  const output = join(dir, "output");
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify({ name: "relayfile-test-release", version: "1.2.3" }) + "\n",
  );
  if (npmStub) {
    const bin = join(dir, "bin");
    mkdirSync(bin);
    writeFileSync(
      join(bin, "npm"),
      '#!/bin/sh\nif [ "${1:-}" = publish ]; then touch "$PWD/npm-published"; fi\nexit 0\n',
    );
    chmodSync(join(bin, "npm"), 0o755);
  }
  try {
    const result = runBash(extractStepRun("Version all packages"), {
      cwd: dir,
      env: {
        CUSTOM_VERSION: customVersion,
        VERSION_TYPE: versionType,
        PREID: "beta",
        NPM_TAG: "next",
        GITHUB_OUTPUT: output,
        GITHUB_WORKSPACE: REPO,
        ...(npmStub ? { PATH: `${join(dir, "bin")}:${process.env.PATH}` } : {}),
      },
    });
    const published = existsSync(join(dir, "npm-published"));
    return {
      ...result,
      packageJson: readFileSync(join(dir, "package.json"), "utf8"),
      outputFile: existsSync(output) ? readFileSync(output, "utf8") : "",
      published,
    };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function runVersionStepWithConflictingAutomaticTag() {
  const dir = mkdtempSync(join(tmpdir(), "relayfile-version-conflict-"));
  const output = join(dir, "output");
  git(dir, "init", "-q");
  git(dir, "config", "user.name", "Release Test");
  git(dir, "config", "user.email", "release-test@example.invalid");
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify({ name: "relayfile-test-release", version: "1.2.3" }) + "\n",
  );
  git(dir, "add", "package.json");
  git(dir, "commit", "-qm", "source");
  const sourceSha = git(dir, "rev-parse", "HEAD");
  // A lightweight/untrusted tag must block the automatic 1.2.4 bump before
  // any package reconciliation can start.
  git(dir, "tag", "v1.2.4", sourceSha);
  try {
    return runBash(extractStepRun("Version all packages"), {
      cwd: dir,
      env: {
        CUSTOM_VERSION: "",
        VERSION_TYPE: "patch",
        PREID: "beta",
        NPM_TAG: "next",
        GITHUB_OUTPUT: output,
        GITHUB_WORKSPACE: REPO,
        SOURCE_SHA: sourceSha,
        GITHUB_RUN_ID: "12345",
        GITHUB_RUN_ATTEMPT: "1",
        RELEASE_RUN_ID: "12345",
        RELEASE_RUN_ATTEMPT: "1",
        RELEASE_REPOSITORY: "AgentWorkforce/relayfile",
      },
    });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function runVersionStepAfterTaggedRelease({
  customVersion = "",
  runAttempt = "1",
  tagMetadata = true,
} = {}) {
  const dir = mkdtempSync(join(tmpdir(), "relayfile-version-tag-"));
  const output = join(dir, "output");
  git(dir, "init", "-q");
  git(dir, "config", "user.name", "Release Test");
  git(dir, "config", "user.email", "release-test@example.invalid");
  const packagePaths = ["package.json", ...EXPECTED_PACKAGE_PATHS];
  for (const path of new Set(packagePaths)) {
    const file = join(dir, path);
    mkdirSync(dirname(file), { recursive: true });
    writeFileSync(
      file,
      JSON.stringify({ name: path, version: "1.2.3" }) + "\n",
    );
  }
  git(dir, "add", ".");
  git(dir, "commit", "-qm", "source");
  const sourceSha = git(dir, "rev-parse", "HEAD");
  for (const path of new Set(packagePaths)) {
    writeFileSync(
      join(dir, path),
      JSON.stringify({ name: path, version: "1.2.4" }) + "\n",
    );
  }
  git(dir, "commit", "-qam", "chore(release): v1.2.4");
  const releaseCommit = git(dir, "rev-parse", "HEAD");
  const releaseTree = git(dir, "rev-parse", "HEAD^{tree}");
  const tagArgs = [
    "tag",
    "-a",
    "v1.2.4",
    releaseCommit,
    "-m",
    "Release v1.2.4",
  ];
  if (tagMetadata) {
    tagArgs.push(
      "-m",
      `source-sha=${sourceSha}`,
      "-m",
      `tag-tree=${releaseTree}`,
      "-m",
      "workflow-run-id=12345",
      "-m",
      "workflow-run-attempt=1",
    );
  }
  git(dir, ...tagArgs);
  const releaseAttestation = join(dir, "release-attestation.json");
  writeFileSync(
    releaseAttestation,
    JSON.stringify({
      kind: "relayfileRelease",
      schemaVersion: 1,
      sourceSha,
      version: "1.2.4",
      producer: {
        repository: "AgentWorkforce/relayfile",
        workflow: "Publish Package",
        workflowPath: ".github/workflows/publish.yml",
        workflowRunId: "12345",
        workflowRunAttempt: "1",
      },
      tag: { name: "v1.2.4", commit: releaseCommit, tree: releaseTree },
      versions: Object.fromEntries(
        RELEASE_PACKAGE_NAMES.map((name) => [name, "1.2.4"]),
      ),
      packages: RELEASE_PACKAGE_NAMES.map((name) => ({
        schemaVersion: 1,
        kind: "relayfileReleasePackage",
        sourceSha,
        workflowRunId: "12345",
        workflowRunAttempt: "1",
        package: {
          name,
          version: "1.2.4",
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
            version: "1.2.4",
            integrity: VALID_INTEGRITY,
            shasum: VALID_SHASUM,
          },
        },
      })),
      binaries: RELEASE_BINARY_NAMES.map((file) => ({
        file,
        sha256: "b".repeat(64),
      })),
    }) + "\n",
  );
  const fakeGhDir = join(dir, "fake-gh");
  mkdirSync(fakeGhDir);
  const fakeGh = join(fakeGhDir, "gh");
  writeFileSync(
    fakeGh,
    `#!/bin/sh
set -eu
if [ "\${1:-}" = release ] && [ "\${2:-}" = download ]; then
  target=.
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --dir ]; then target=$2; shift 2; continue; fi
    shift
  done
  mkdir -p "$target"
  cp "$FAKE_RELEASE_ATTESTATION" "$target/release-attestation.json"
  exit 0
fi
if [ "\${1:-}" = attestation ] && [ "\${2:-}" = verify ]; then
  echo '[{"verificationResult":{"signature":{"certificate":{}}}}]'
  exit 0
fi
exit 1
`,
  );
  chmodSync(fakeGh, 0o755);
  git(dir, "checkout", "-q", sourceSha);
  const result = runBash(extractStepRun("Version all packages"), {
    cwd: dir,
    env: {
      CUSTOM_VERSION: customVersion,
      VERSION_TYPE: "patch",
      PREID: "beta",
      NPM_TAG: "next",
      GITHUB_OUTPUT: output,
      GITHUB_WORKSPACE: REPO,
      SOURCE_SHA: sourceSha,
      GITHUB_RUN_ATTEMPT: runAttempt,
      GITHUB_RUN_ID: "12345",
      RELEASE_RUN_ATTEMPT: runAttempt,
      RELEASE_RUN_ID: "12345",
      RELEASE_REPOSITORY: "AgentWorkforce/relayfile",
      FAKE_RELEASE_ATTESTATION: releaseAttestation,
      PATH: `${fakeGhDir}:${process.env.PATH}`,
    },
  });
  const packageJson = readFileSync(join(dir, "package.json"), "utf8");
  const outputFile = existsSync(output) ? readFileSync(output, "utf8") : "";
  rmSync(dir, { recursive: true, force: true });
  return { ...result, packageJson, outputFile };
}

function writeNpmStub(dir, mode) {
  const bin = join(dir, "bin");
  mkdirSync(bin);
  const script = `#!/bin/sh
set -eu
state="$PWD/npm-stub-state"
case "$1" in
  pack)
    printf '%s' 'immutable package content' > relayfile-test-1.2.3.tgz
    printf '%s\\n' '[{"filename":"relayfile-test-1.2.3.tgz","name":"@relayfile/test","version":"1.2.3","integrity":"${VALID_INTEGRITY}","shasum":"${VALID_SHASUM}"}]'
    ;;
  view)
    count=0
    if [ -f "$state" ]; then count=$(cat "$state"); fi
    count=$((count + 1))
    printf '%s\\n' "$count" > "$state"
    if [ "${mode}" = outage ]; then
      printf '%s\\n' 'npm error code E503 service unavailable' >&2
      exit 1
    fi
    if [ "${mode}" = conflict ]; then
      printf '%s\\n' '{"integrity":"${OTHER_INTEGRITY}","shasum":"${OTHER_SHASUM}"}'
    elif [ "$count" -eq 1 ] && [ "${mode}" = absent ]; then
      printf '%s\\n' 'npm error code E404' >&2
      exit 1
    else
      printf '%s\\n' '{"integrity":"${VALID_INTEGRITY}","shasum":"${VALID_SHASUM}"}'
    fi
    ;;
  publish)
    printf '%s\\n' published > "$PWD/npm-published"
    ;;
  *)
    printf '%s\\n' "unexpected npm command: $*" >&2
    exit 1
    ;;
esac
`;
  writeFileSync(join(bin, "npm"), script);
  chmodSync(join(bin, "npm"), 0o755);
  return bin;
}

function runReconcileCli(mode, { relativeScript = false } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "relayfile-reconcile-cli-"));
  const packageDir = join(dir, "package");
  mkdirSync(packageDir);
  writeFileSync(
    join(packageDir, "package.json"),
    JSON.stringify({ name: "@relayfile/test", version: "1.2.3" }) + "\n",
  );
  const output = join(dir, "attestation.json");
  const bin = writeNpmStub(packageDir, mode);
  const script = join(REPO, "scripts/release/reconcile-package.mjs");
  try {
    const result = execFileSync(
      process.execPath,
      [
        relativeScript ? "scripts/release/reconcile-package.mjs" : script,
        "--package-dir",
        packageDir,
        "--tag",
        "next",
        "--source-sha",
        "a".repeat(40),
        "--run-id",
        "1",
        "--run-attempt",
        "1",
        "--output",
        output,
      ],
      {
        cwd: relativeScript ? REPO : packageDir,
        env: { ...process.env, PATH: `${bin}:${process.env.PATH}` },
        encoding: "utf8",
      },
    );
    return {
      status: 0,
      output: result,
      published: existsSync(join(packageDir, "npm-published")),
    };
  } catch (error) {
    return {
      status: error.status ?? 1,
      output: `${error.stdout ?? ""}${error.stderr ?? ""}`,
      published: existsSync(join(packageDir, "npm-published")),
    };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function extractTagPreparation() {
  const start = WORKFLOW.indexOf(
    "          INTENDED_TREE=$(git rev-parse 'HEAD^{tree}')",
  );
  assert.notEqual(start, -1, "tag preparation body not found");
  const end = WORKFLOW.indexOf('\n          } >> "$GITHUB_ENV"', start);
  assert.notEqual(end, -1, "tag preparation body is unterminated");
  return dedent(
    WORKFLOW.slice(start, end + '\n          } >> "$GITHUB_ENV"'.length),
  );
}

function git(cwd, ...args) {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

function makeTagSandbox() {
  const dir = mkdtempSync(join(tmpdir(), "relayfile-tag-"));
  git(dir, "init", "-q");
  git(dir, "config", "user.name", "Release Test");
  git(dir, "config", "user.email", "release-test@example.invalid");
  writeFileSync(join(dir, "version.txt"), "source\n");
  git(dir, "add", "version.txt");
  git(dir, "commit", "-qm", "source");
  const sourceSha = git(dir, "rev-parse", "HEAD");
  writeFileSync(join(dir, "version.txt"), "release\n");
  git(dir, "commit", "-qam", "release");
  return { dir, sourceSha };
}

function runTagPreparation({
  existingTag = false,
  mismatch = false,
  lightweight = false,
} = {}) {
  const { dir, sourceSha } = makeTagSandbox();
  const envFile = join(dir, "github-env");
  if (existingTag) {
    const releaseTree = git(dir, "rev-parse", "HEAD^{tree}");
    const tagCommit = git(dir, "commit-tree", releaseTree, "-p", sourceSha);
    if (lightweight) {
      git(dir, "tag", "v1.2.4", tagCommit);
    } else {
      git(dir, "tag", "-a", "v1.2.4", tagCommit, "-m", "Release v1.2.4");
    }
    if (mismatch) {
      writeFileSync(join(dir, "version.txt"), "mismatch\n");
      git(dir, "commit", "-qam", "mismatch");
    }
  }
  const result = runBash(extractTagPreparation(), {
    cwd: dir,
    env: {
      NEW_VERSION: "1.2.4",
      SOURCE_SHA: sourceSha,
      GITHUB_ENV: envFile,
    },
  });
  const env = existsSync(envFile) ? readFileSync(envFile, "utf8") : "";
  rmSync(dir, { recursive: true, force: true });
  return { ...result, env };
}

test("dispatch validation executes and rejects option-shaped values", () => {
  const valid = runDispatchValidation({
    packageInput: "all",
    dryRunInput: "false",
  });
  assert.equal(valid.status, 0);
  assert.match(valid.outputFile, /^package=all$/m);
  assert.match(valid.outputFile, /^dry_run=false$/m);
  assert.match(valid.envFile, /^RELEASE_PACKAGE=all$/m);
  assert.match(valid.envFile, /^RELEASE_DRY_RUN=false$/m);
  for (const values of [
    { packageInput: "--help", dryRunInput: "false" },
    { packageInput: "all", dryRunInput: "--help" },
    { packageInput: "not-a-package", dryRunInput: "false" },
    { packageInput: "all", dryRunInput: "maybe" },
  ]) {
    assert.notEqual(
      runDispatchValidation(values).status,
      0,
      `accepted invalid dispatch values: ${JSON.stringify(values)}`,
    );
  }
});

test("version step executes strict custom and bump validation", () => {
  const custom = runVersionStep({ customVersion: "1.2.4-beta.1" });
  assert.equal(custom.status, 0, custom.stdout);
  assert.match(custom.packageJson, /"version"\s*:\s*"1\.2\.4-beta\.1"/);
  assert.match(custom.outputFile, /new_version=1\.2\.4-beta\.1/);

  for (const customVersion of [
    "--allow-same-version",
    "1.2.3; echo PWNED",
    "1.2",
    "01.2.3",
    "1.2.3-01",
    "1.2.3\n",
  ]) {
    const result = runVersionStep({ customVersion });
    assert.notEqual(
      result.status,
      0,
      `accepted invalid custom_version: ${customVersion}`,
    );
    assert.match(result.packageJson, /"version"\s*:\s*"1\.2\.3"/);
  }

  const invalidBump = runVersionStep({ versionType: "--allow-same-version" });
  assert.notEqual(invalidBump.status, 0);
  assert.match(invalidBump.packageJson, /"version"\s*:\s*"1\.2\.3"/);
});

test("same-source custom versions fail before any package publish", () => {
  const result = runVersionStep({
    customVersion: "1.2.3",
    npmStub: true,
  });
  assert.notEqual(result.status, 0);
  assert.match(
    result.stdout,
    /custom_version must differ from the source version/,
  );
  assert.equal(result.published, false);
  const guard = WORKFLOW.indexOf(
    "custom_version must differ from the source version",
  );
  const build = WORKFLOW.indexOf("- name: Build packages");
  const reconcile = WORKFLOW.indexOf("scripts/release/reconcile-package.mjs");
  assert.ok(guard >= 0 && guard < build && guard < reconcile);
});

test("next dispatch bumps beyond a prior trusted release tag", () => {
  const result = runVersionStepAfterTaggedRelease();
  assert.equal(result.status, 0, result.stdout);
  assert.match(result.packageJson, /"version"\s*:\s*"1\.2\.5"/);
});

test("custom versions colliding with an existing tag fail before publication", () => {
  const result = runVersionStepAfterTaggedRelease({ customVersion: "1.2.4" });
  assert.notEqual(result.status, 0);
  assert.match(result.stdout, /already has a conflicting release tag/);
});

test("automatic versions colliding with an untrusted tag fail before publication", () => {
  const result = runVersionStepWithConflictingAutomaticTag();
  assert.notEqual(result.status, 0);
  assert.match(
    result.stdout,
    /already exists without an exact verified recovery/,
  );
  const guard = WORKFLOW.indexOf(
    "already exists without an exact verified recovery",
  );
  const build = WORKFLOW.indexOf("- name: Build packages");
  const reconcile = WORKFLOW.indexOf("scripts/release/reconcile-package.mjs");
  assert.ok(guard >= 0 && guard < build && guard < reconcile);
});

test("only an exact verified same-workflow rerun may reuse a tagged custom version", () => {
  const result = runVersionStepAfterTaggedRelease({
    customVersion: "1.2.4",
    runAttempt: "2",
    tagMetadata: true,
  });
  assert.equal(result.status, 0, result.stdout);
  assert.match(result.packageJson, /"version"\s*:\s*"1\.2\.4"/);
  assert.match(result.outputFile, /release_run_id=12345/);
  assert.match(result.outputFile, /release_run_attempt=1/);
});

test("reconciliation CLI shell harness covers canonical E404, collision, and outage", () => {
  const absent = runReconcileCli("absent");
  assert.equal(absent.status, 0, absent.output);
  assert.equal(absent.published, true);

  const relative = runReconcileCli("absent", { relativeScript: true });
  assert.equal(relative.status, 0, relative.output);
  assert.equal(relative.published, true);

  const conflict = runReconcileCli("conflict");
  assert.notEqual(conflict.status, 0);
  assert.equal(conflict.published, false);
  assert.match(conflict.output, /conflicts with the local release tarball/);

  const outage = runReconcileCli("outage");
  assert.notEqual(outage.status, 0);
  assert.equal(outage.published, false);
  assert.match(outage.output, /ambiguous/);
});

test("tag preparation shell harness proves new and existing tag invariants", () => {
  assert.match(
    extractTagPreparation(),
    /git cat-file -t "refs\/tags\/v\$\{NEW_VERSION\}".*= "tag"/,
  );
  const fresh = runTagPreparation();
  assert.equal(fresh.status, 0, fresh.stdout);
  assert.match(fresh.env, /TAG_EXISTS=false/);
  assert.match(fresh.env, /TAG_COMMIT=[0-9a-f]{40}/);

  const existing = runTagPreparation({ existingTag: true });
  assert.equal(existing.status, 0, existing.stdout);
  assert.match(existing.env, /TAG_EXISTS=true/);

  const mismatch = runTagPreparation({ existingTag: true, mismatch: true });
  assert.notEqual(mismatch.status, 0);

  const lightweight = runTagPreparation({
    existingTag: true,
    lightweight: true,
  });
  assert.notEqual(lightweight.status, 0);
});

/** The shared PACKAGE_PATHS_JSON assignment, verbatim. */
function extractPackagePaths() {
  const start = WORKFLOW.indexOf("          PACKAGE_PATHS_JSON='[");
  assert.notEqual(start, -1, "PACKAGE_PATHS_JSON assignment not found");
  const end = WORKFLOW.indexOf("\n          ]'", start);
  assert.notEqual(end, -1, "PACKAGE_PATHS_JSON assignment is unterminated");
  return dedent(WORKFLOW.slice(start, end + "\n          ]'".length));
}

test("the shared package list still covers every published package", () => {
  const paths = JSON.parse(
    extractPackagePaths()
      .replace(/^PACKAGE_PATHS_JSON='/, "")
      .replace(/'$/, ""),
  );
  assert.deepEqual(paths, EXPECTED_PACKAGE_PATHS);
});

test("the version-sync script consumes the shared list rather than its own copy", () => {
  assert.match(WORKFLOW, /const packagePaths = \$\{PACKAGE_PATHS_JSON\};/);
  const inlineArrays =
    WORKFLOW.match(/'packages\/mount-darwin-arm64\/package\.json'/g) ?? [];
  assert.equal(
    inlineArrays.length,
    0,
    "a duplicate hardcoded package list has reappeared",
  );
});

test("Create Release still runs the propagation-tolerant lockfile step", () => {
  assert.match(
    WORKFLOW,
    /node scripts\/release\/regenerate-release-lockfiles\.mjs "\$RELEASE_VERSION"/,
  );
  assert.doesNotMatch(
    WORKFLOW,
    /npm install --prefix packages\/sdk\/typescript --package-lock-only/,
  );
});

test("every checkout is pinned to the immutable dispatch/build source SHA", () => {
  const checkouts = [
    ...WORKFLOW.matchAll(
      /- name: Checkout code\n\s+uses: actions\/checkout@[0-9a-f]{40}(?:\s+# v4)?([\s\S]*?)(?=\n\s+- name:|\n\s+\w[\w-]*:\s*$)/g,
    ),
  ];
  assert.equal(
    checkouts.length,
    6,
    "release workflow should have six checked-out jobs",
  );
  for (const [, block] of checkouts) {
    assert.match(
      block,
      /ref:\s+\$\{\{ (?:github\.sha|needs\.build\.outputs\.source_sha) \}\}/,
    );
    assert.match(block, /fetch-depth:\s+0/);
  }
  assert.match(WORKFLOW, /test "\$SOURCE_SHA" = "\$GITHUB_SHA"/);
  assert.match(
    WORKFLOW,
    /test "\$\(git rev-parse HEAD\)" = "\$\{\{ needs\.build\.outputs\.source_sha \}\}"/,
  );
});

test("prereleases cannot use latest and GitHub marks them prerelease", () => {
  assert.match(WORKFLOW, /prereleases may not use the npm latest dist-tag/);
  assert.match(WORKFLOW, /NPM_TAG: \$\{\{ github\.event\.inputs\.tag \}\}/);
  assert.match(WORKFLOW, /\[ "\$NPM_TAG" = "latest" \]/);
  assert.match(
    WORKFLOW,
    /prerelease:\s+\$\{\{ needs\.build\.outputs\.is_prerelease \}\}/,
  );
});

test("release input values are passed through env, not interpolated into shell source", () => {
  assert.match(
    WORKFLOW,
    /CUSTOM_VERSION: \$\{\{ github\.event\.inputs\.custom_version \}\}/,
  );
  assert.doesNotMatch(
    WORKFLOW,
    /CUSTOM_VERSION="\$\{\{ github\.event\.inputs\.custom_version \}\}"/,
  );
  assert.match(
    WORKFLOW,
    /PACKAGE_INPUT: \$\{\{ github\.event\.inputs\.package \}\}/,
  );
  assert.match(
    WORKFLOW,
    /DRY_RUN_INPUT: \$\{\{ github\.event\.inputs\.dry_run \}\}/,
  );
  const packageInputs =
    WORKFLOW.match(/\$\{\{ github\.event\.inputs\.package \}\}/g) ?? [];
  const dryRunInputs =
    WORKFLOW.match(/\$\{\{ github\.event\.inputs\.dry_run \}\}/g) ?? [];
  assert.equal(
    packageInputs.length,
    1,
    "package input must only enter via env",
  );
  assert.equal(dryRunInputs.length, 1, "dry_run input must only enter via env");
  assert.match(WORKFLOW, /case "\$PACKAGE_INPUT" in[\s\S]*RELEASE_PACKAGE=/);
  assert.match(WORKFLOW, /case "\$DRY_RUN_INPUT" in[\s\S]*RELEASE_DRY_RUN=/);
});

test("all credential-bearing workflow actions are pinned to full commit SHAs", () => {
  const refs = [...WORKFLOW.matchAll(/^\s+uses:\s+([^\s#]+)/gm)].map(
    ([, ref]) => ref,
  );
  assert.ok(refs.length > 0, "workflow should use actions");
  for (const ref of refs) {
    assert.match(ref, /@[0-9a-f]{40}$/, `mutable action ref: ${ref}`);
  }
  assert.match(WORKFLOW, /# v4/);
  assert.match(WORKFLOW, /# v5/);
  assert.match(WORKFLOW, /# v2/);
});

test("package publication goes through reconciliation and post-publish attestation", () => {
  assert.match(WORKFLOW, /scripts\/release\/reconcile-package\.mjs/);
  assert.doesNotMatch(WORKFLOW, /run:\s+npm publish --access public/);
  assert.match(WORKFLOW, /Upload package attestation/);
  assert.match(WORKFLOW, /scripts\/release\/create-release-attestation\.mjs/);
  assert.match(WORKFLOW, /release-attestation\.json/);
  assert.doesNotMatch(
    WORKFLOW,
    /if \[ -z "\$CUSTOM_VERSION" \]; then[\s\S]*?npm view/,
    "a preflight collision guard would prevent resumable reconciliation",
  );
});

test("all package publication is behind a successful read-only reconciliation barrier", () => {
  assert.match(WORKFLOW, /preflight-packages:/);
  assert.match(WORKFLOW, /--preflight true/);
  assert.match(
    WORKFLOW,
    /publish-packages:[\s\S]*?needs: \[build, build-mount-binaries, preflight-packages\][\s\S]*?needs\.preflight-packages\.result == 'success'/,
  );
  assert.match(
    WORKFLOW,
    /publish-single:[\s\S]*?needs: \[build, build-mount-binaries, preflight-packages\][\s\S]*?needs\.preflight-packages\.result == 'success'/,
  );
});

test("versioning resolves a trusted tag baseline and preserves rerun targets", () => {
  assert.match(WORKFLOW, /resolve-release-baseline\.mjs/);
  assert.match(WORKFLOW, /BASELINE_VERSION=/);
  assert.match(WORKFLOW, /RUN_ATTEMPT=.*GITHUB_RUN_ATTEMPT/);
  assert.match(WORKFLOW, /npm version "\$RESUMABLE_VERSION"/);
  assert.match(WORKFLOW, /RESUMABLE_RUN_ID=.*resumable_run_id/);
  assert.match(WORKFLOW, /RESUMABLE_RUN_ATTEMPT=.*resumable_run_attempt/);
  assert.match(
    WORKFLOW,
    /release_run_id: \$\{\{ steps\.bump\.outputs\.release_run_id \}\}/,
  );
  assert.match(
    WORKFLOW,
    /release_run_attempt: \$\{\{ steps\.bump\.outputs\.release_run_attempt \}\}/,
  );
  assert.match(WORKFLOW, /ATTESTATION_RUN_ID="\$RESUMABLE_RUN_ID"/);
});

test("tagging verifies the generated tag commit and never pushes a moving branch", () => {
  assert.match(WORKFLOW, /test "\$TAG_PARENT" = "\$SOURCE_SHA"/);
  assert.match(WORKFLOW, /test "\$TAG_TREE" = "\$INTENDED_TREE"/);
  assert.match(
    WORKFLOW,
    /refs\/tags\/v\$\{NEW_VERSION\}:refs\/tags\/v\$\{NEW_VERSION\}/,
  );
  const attest = WORKFLOW.indexOf("- name: Generate release attestation");
  const push = WORKFLOW.indexOf("- name: Create and push release tag");
  assert.ok(
    attest >= 0 && push > attest,
    "attestation must precede remote tag push",
  );
  assert.doesNotMatch(WORKFLOW, /git push\s*\n/);
});

test("release baseline uses a cryptographically verified external attestation", () => {
  assert.match(WORKFLOW, /--repository "\$RELEASE_REPOSITORY"/);
  assert.match(WORKFLOW, /--run-id "\$RELEASE_RUN_ID"/);
  assert.match(WORKFLOW, /--run-attempt "\$RELEASE_RUN_ATTEMPT"/);
  assert.match(WORKFLOW, /--workflow-path "\.github\/workflows\/publish\.yml"/);
  assert.match(
    WORKFLOW,
    /actions\/attest@508db95dd578ae2727ebd6217d5ba78e4fbda05d/,
  );
  assert.match(WORKFLOW, /subject-path: release-attestation\.json/);
  assert.match(
    WORKFLOW,
    /name: release-attestation-\$\{\{ github\.run_attempt \}\}/,
  );
  assert.match(WORKFLOW, /tag-tree=\$\{TAG_TREE\}/);
});

test("release permissions are scoped by job", () => {
  const buildJob = workflowJob("build");
  assert.doesNotMatch(
    WORKFLOW,
    /^permissions:\n\s+contents: write\n\s+id-token: write/m,
  );
  assert.match(
    buildJob,
    /^    permissions:\n      contents: read\n      actions: read\n      attestations: read$/m,
  );
  assert.match(
    WORKFLOW,
    /publish-packages:[\s\S]*?permissions:\n\s+contents: read\n\s+id-token: write/,
  );
  assert.match(
    WORKFLOW,
    /create-release:[\s\S]*?permissions:\n\s+contents: write/,
  );
  assert.match(
    WORKFLOW,
    /create-release:[\s\S]*?permissions:[\s\S]*?attestations: write[\s\S]*?artifact-metadata: write/,
  );
  assert.match(WORKFLOW, /persist-credentials: false/);
});

test("Python release is ephemeral and publishes only an annotated source tag", () => {
  assert.doesNotMatch(PYTHON_WORKFLOW, /git push origin HEAD:main/);
  assert.doesNotMatch(PYTHON_WORKFLOW, /git commit/);
  assert.doesNotMatch(PYTHON_WORKFLOW, /git add packages\/sdk\/python/);
  assert.match(PYTHON_WORKFLOW, /resolve-python-release-baseline\.mjs/);
  assert.match(PYTHON_BASELINE, /parseStrictPep440/);
  assert.match(PYTHON_BASELINE, /draft !== true/);
  assert.match(PYTHON_BASELINE, /published_at/);
  assert.match(PYTHON_BASELINE, /source-sha/);
  assert.match(PYTHON_WORKFLOW, /WORKFLOW_RUN_ID: \$\{\{ github\.run_id \}\}/);
  assert.match(PYTHON_WORKFLOW, /--workflow-run-id "\$WORKFLOW_RUN_ID"/);
  assert.match(PYTHON_WORKFLOW, /RESUMABLE_VERSION=/);
  assert.match(PYTHON_WORKFLOW, /Reusing completed Python SDK release/);
  assert.match(PYTHON_WORKFLOW, /canonical PEP 440/);
  assert.equal(
    (PYTHON_WORKFLOW.match(/--verify-tag "\$(?:RELEASE_TAG|TAG)"/g) ?? []).length,
    2,
    "both existing-tag paths must use canonical provenance verification",
  );
  assert.match(PYTHON_WORKFLOW, /JOB_STATUS: \$\{\{ job\.status \}\}/);
  assert.match(
    PYTHON_WORKFLOW,
    /if \[ "\$JOB_STATUS" != "success" \]; then[\s\S]*?Release failed before completion[\s\S]*?elif \[ "\$RELEASE_DRY_RUN" = "true" \]/,
  );
  assert.match(
    PYTHON_WORKFLOW,
    /git tag -a "\$TAG"[\s\S]*source-sha=\$\{SOURCE_SHA\}[\s\S]*tag-tree=\$\{TAG_TREE\}/,
  );
  assert.match(
    PYTHON_WORKFLOW,
    /git push origin "refs\/tags\/sdk-python-v\$\{NEW_VERSION\}:refs\/tags\/sdk-python-v\$\{NEW_VERSION\}"/,
  );
  assert.match(PYTHON_WORKFLOW, /relayfile-sdk \$\{NEW_VERSION\} already exists/);
  assert.match(PYTHON_WORKFLOW, /--connect-timeout 5 --max-time 20/);
  assert.match(
    PYTHON_WORKFLOW,
    /name: Check PyPI version state[\s\S]*if: github\.ref == 'refs\/heads\/main' && github\.event\.inputs\.dry_run != 'true'/,
  );
  assert.match(PYTHON_WORKFLOW, /No attested Python release tag found/);
});

test("Python PyPI state gate handles absence, recovery, outage, and existing versions", () => {
  const absent = runPythonPypiState(["404"]);
  assert.equal(absent.status, 0, absent.stdout);
  assert.match(absent.outputFile, /published=false/);

  const existing = runPythonPypiState(["200"]);
  assert.notEqual(existing.status, 0);
  assert.match(existing.stdout, /already exists without a same-source tag reservation/);

  const recovery = runPythonPypiState(["200"], { recovery: "true" });
  assert.equal(recovery.status, 0, recovery.stdout);
  assert.match(recovery.outputFile, /published=true/);

  const outage = runPythonPypiState(["503", "503", "503", "503"]);
  assert.notEqual(outage.status, 0);
  assert.match(outage.stdout, /remained transiently unavailable/);

  assert.match(
    PYTHON_WORKFLOW,
    /name: Check PyPI version state[\s\S]*if: github\.ref == 'refs\/heads\/main' && github\.event\.inputs\.dry_run != 'true'/,
  );
});

test("Python tag reservation refuses a checkout that is not the declared source", () => {
  const cwd = mkdtempSync(join(tmpdir(), "relayfile-python-source-"));
  try {
    git(cwd, "init", "-q");
    git(cwd, "config", "user.name", "Release Test");
    git(cwd, "config", "user.email", "release-test@example.invalid");
    writeFileSync(join(cwd, "source.txt"), "source\n");
    git(cwd, "add", ".");
    git(cwd, "commit", "-qm", "source");
    const result = runBash(
      extractPythonStepRun("Reserve Python SDK release tag"),
      {
        cwd,
        env: {
          NEW_VERSION: "1.2.4",
          SOURCE_SHA: "a".repeat(40),
          RELEASE_RUN_ID: "123",
          RELEASE_RUN_ATTEMPT: "1",
        },
      },
    );
    assert.notEqual(result.status, 0);
    assert.doesNotMatch(result.stdout, /git push/);
  } finally {
    rmSync(cwd, { recursive: true, force: true });
  }
});
