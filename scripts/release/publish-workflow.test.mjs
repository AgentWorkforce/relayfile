/**
 * Contract tests for the release-critical shell in .github/workflows/publish.yml.
 *
 * These extract the real shipped shell and execute it against a stubbed `npm`,
 * so they fail if the collision guard is narrowed back to a single package or
 * the propagation-tolerant lockfile step is reverted to a bare `npm install`.
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, chmodSync, rmSync } from 'node:fs';
import { readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const REPO = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const WORKFLOW = readFileSync(join(REPO, '.github/workflows/publish.yml'), 'utf8');

/** The package.json paths the release versions and publishes. */
const EXPECTED_PACKAGE_PATHS = [
  'packages/core/package.json',
  'packages/sdk/typescript/package.json',
  'packages/client/package.json',
  'packages/agents/package.json',
  'packages/cli/package.json',
  'packages/file-observer/package.json',
  'packages/local-mount/package.json',
  'packages/mount-darwin-arm64/package.json',
  'packages/mount-darwin-x64/package.json',
  'packages/mount-linux-arm64/package.json',
  'packages/mount-linux-x64/package.json',
];

function dedent(block) {
  return block.split('\n').map((line) => line.replace(/^ {10}/, '')).join('\n');
}

/** The shared PACKAGE_PATHS_JSON assignment, verbatim. */
function extractPackagePaths() {
  const start = WORKFLOW.indexOf("          PACKAGE_PATHS_JSON='[");
  assert.notEqual(start, -1, 'PACKAGE_PATHS_JSON assignment not found');
  const end = WORKFLOW.indexOf("\n          ]'", start);
  assert.notEqual(end, -1, 'PACKAGE_PATHS_JSON assignment is unterminated');
  return dedent(WORKFLOW.slice(start, end + "\n          ]'".length));
}

/** The collision-guard `if` block, verbatim. */
function extractCollisionGuard() {
  const start = WORKFLOW.indexOf('          if [ -z "$CUSTOM_VERSION" ]; then');
  assert.notEqual(start, -1, 'collision guard not found');
  const end = WORKFLOW.indexOf('\n          fi\n', start);
  assert.notEqual(end, -1, 'collision guard is unterminated');
  return dedent(WORKFLOW.slice(start, end + '\n          fi'.length));
}

/**
 * A throwaway repo with the real package names and an `npm` stub that reports
 * exactly `published` as taken.
 */
function makeSandbox(published) {
  const dir = mkdtempSync(join(tmpdir(), 'relayfile-guard-'));
  for (const rel of EXPECTED_PACKAGE_PATHS) {
    const name = readFileSync(join(REPO, rel), 'utf8');
    mkdirSync(join(dir, dirname(rel)), { recursive: true });
    writeFileSync(join(dir, rel), name);
  }
  const bin = join(dir, 'bin');
  mkdirSync(bin);
  // `npm view <spec> version` exits 0 only for the specs we declare published.
  writeFileSync(
    join(bin, 'npm'),
    `#!/bin/sh\n[ "$1" = view ] || exit 0\nfor s in ${published.map((s) => `'${s}'`).join(' ')}; do\n  [ "$2" = "$s" ] && exit 0\ndone\nexit 1\n`,
  );
  chmodSync(join(bin, 'npm'), 0o755);
  return { dir, bin };
}

function runGuard({ published, newVersion, customVersion = '', currentVersion = '0.10.52' }) {
  const { dir, bin } = makeSandbox(published);
  const script = `set -u\n${extractPackagePaths()}\n${extractCollisionGuard()}\n`;
  try {
    const stdout = execFileSync('bash', ['-c', script], {
      cwd: dir,
      encoding: 'utf8',
      env: {
        ...process.env,
        PATH: `${bin}:${process.env.PATH}`,
        CUSTOM_VERSION: customVersion,
        NEW_VERSION: newVersion,
        CURRENT_VERSION: currentVersion,
      },
    });
    return { code: 0, stdout };
  } catch (error) {
    return { code: error.status, stdout: `${error.stdout ?? ''}${error.stderr ?? ''}` };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

test('the shared package list still covers every published package', () => {
  const paths = JSON.parse(extractPackagePaths().replace(/^PACKAGE_PATHS_JSON='/, '').replace(/'$/, ''));
  assert.deepEqual(paths, EXPECTED_PACKAGE_PATHS);
});

test('the version-sync script consumes the shared list rather than its own copy', () => {
  assert.match(WORKFLOW, /const packagePaths = \$\{PACKAGE_PATHS_JSON\};/);
  const inlineArrays = WORKFLOW.match(/'packages\/mount-darwin-arm64\/package\.json'/g) ?? [];
  assert.equal(inlineArrays.length, 0, 'a duplicate hardcoded package list has reappeared');
});

test('the guard passes when the computed version is free everywhere', () => {
  const result = runGuard({ published: [], newVersion: '0.10.55' });
  assert.equal(result.code, 0);
});

test('the guard blocks the 0.10.54 drift that half-released today', () => {
  const published = EXPECTED_PACKAGE_PATHS.map(
    (p) => `${JSON.parse(readFileSync(join(REPO, p), 'utf8')).name}@0.10.54`,
  );
  const result = runGuard({ published, newVersion: '0.10.54' });
  assert.equal(result.code, 1);
  assert.match(result.stdout, /already published/);
});

test('REGRESSION: a scoped package taken by a half-finished run is caught, not just relayfile', () => {
  // The exact hole a `relayfile`-only check leaves: an interrupted matrix
  // published @relayfile/core but never reached the `relayfile` CLI package.
  const result = runGuard({ published: ['@relayfile/core@0.10.55'], newVersion: '0.10.55' });
  assert.equal(result.code, 1, 'guard must not pass when any published name is taken');
  assert.match(result.stdout, /@relayfile\/core/);
});

test('the guard defers to an operator-supplied custom version', () => {
  const result = runGuard({
    published: ['@relayfile/core@0.10.54', 'relayfile@0.10.54'],
    newVersion: '0.10.54',
    customVersion: '0.10.54',
  });
  assert.equal(result.code, 0);
});

test('Create Release still runs the propagation-tolerant lockfile step', () => {
  assert.match(WORKFLOW, /node scripts\/release\/regenerate-release-lockfiles\.mjs "\$RELEASE_VERSION"/);
  assert.doesNotMatch(WORKFLOW, /npm install --prefix packages\/sdk\/typescript --package-lock-only/);
});
