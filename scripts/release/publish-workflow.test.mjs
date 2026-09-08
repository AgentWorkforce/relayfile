/**
 * Contract tests for the release-critical shell in .github/workflows/publish.yml.
 *
 * These extract the real shipped shell and assert that release invariants stay
 * wired into the workflow, while the registry reconciliation behavior has its
 * own executable tests in reconcile-package.test.mjs.
 */
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const REPO = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const WORKFLOW = readFileSync(
  join(REPO, ".github/workflows/publish.yml"),
  "utf8",
);

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
    5,
    "release workflow should have five checked-out jobs",
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
  assert.equal(packageInputs.length, 1, "package input must only enter via env");
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

test("release permissions are scoped by job", () => {
  assert.doesNotMatch(
    WORKFLOW,
    /^permissions:\n\s+contents: write\n\s+id-token: write/m,
  );
  assert.match(WORKFLOW, /build:\n[\s\S]*?permissions:\n\s+contents: read/);
  assert.match(
    WORKFLOW,
    /publish-packages:[\s\S]*?permissions:\n\s+contents: read\n\s+id-token: write/,
  );
  assert.match(
    WORKFLOW,
    /create-release:[\s\S]*?permissions:\n\s+contents: write/,
  );
  assert.match(WORKFLOW, /persist-credentials: false/);
});
