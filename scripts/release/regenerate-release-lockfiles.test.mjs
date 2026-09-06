import test from 'node:test';
import assert from 'node:assert/strict';

import {
  backoffDelay,
  classifyInstallFailure,
  collectRequiredPackages,
  parseUnresolvedSpecs,
  regenerateReleaseLockfiles,
  waitForRegistryPropagation,
} from './regenerate-release-lockfiles.mjs';

const VERSION = '0.10.54';
const REQUIRED = ['@relayfile/core', '@relayfile/mount-darwin-arm64'];

/** Verbatim from the failed Create Release job of run 34034791408. */
const REAL_ETARGET_OUTPUT = `
npm error code ETARGET
npm error notarget No matching version found for @relayfile/core@0.10.54.
npm error notarget In most cases you or one of your dependencies are requesting
npm error notarget a package version that doesn't exist.
`;

const noSleep = async () => {};

function scriptedRun(outcomes) {
  const calls = [];
  return {
    calls,
    run: async (command, args) => {
      calls.push(`${command} ${args.join(' ')}`);
      const next = outcomes.shift();
      return next ?? { code: 0, stdout: '', stderr: '' };
    },
  };
}

/* ------------------------------ parsing ---------------------------------- */

test('parses the scoped package and version out of a real npm ETARGET message', () => {
  assert.deepEqual(parseUnresolvedSpecs(REAL_ETARGET_OUTPUT), [
    { name: '@relayfile/core', version: '0.10.54' },
  ]);
});

/* --------------------------- classification ------------------------------ */

test('an internal package at the release version is propagation lag', () => {
  const verdict = classifyInstallFailure({
    output: REAL_ETARGET_OUTPUT, version: VERSION, requiredPackages: REQUIRED,
  });
  assert.equal(verdict.kind, 'propagation');
});

test('an internal package at some OTHER version is fatal, not propagation', () => {
  const verdict = classifyInstallFailure({
    output: 'npm error code ETARGET\nnpm error notarget No matching version found for @relayfile/core@0.10.99.',
    version: VERSION,
    requiredPackages: REQUIRED,
  });
  assert.equal(verdict.kind, 'fatal');
  assert.match(verdict.reason, /never published/);
});

test('a third-party package is fatal even at the release version', () => {
  const verdict = classifyInstallFailure({
    output: 'npm error code ETARGET\nnpm error notarget No matching version found for left-pad@0.10.54.',
    version: VERSION,
    requiredPackages: REQUIRED,
  });
  assert.equal(verdict.kind, 'fatal');
});

test('a non-ETARGET failure is fatal and is never retried', () => {
  const verdict = classifyInstallFailure({
    output: 'npm error code EACCES\nnpm error syscall open', version: VERSION, requiredPackages: REQUIRED,
  });
  assert.equal(verdict.kind, 'fatal');
  assert.match(verdict.reason, /not an ETARGET failure/);
});

/* ------------------------- required-package set --------------------------- */

test('collects internal deps pinned exactly or by caret at the release version', () => {
  const required = collectRequiredPackages({
    version: VERSION,
    manifests: [
      { dependencies: { '@relayfile/core': '0.10.54', chalk: '^5.0.0' },
        optionalDependencies: { '@relayfile/mount-linux-x64': '0.10.54' } },
      { peerDependencies: { '@relayfile/sdk': '^0.10.54' } },
      // A stale pin at an older version is not part of this release.
      { dependencies: { '@relayfile/client': '0.10.52' } },
    ],
  });
  assert.deepEqual(required, ['@relayfile/core', '@relayfile/mount-linux-x64', '@relayfile/sdk']);
});

/* ----------------------------- propagation ------------------------------- */

test('waits until a lagging version appears, then proceeds', async () => {
  let looks = 0;
  const result = await waitForRegistryPropagation({
    version: VERSION,
    packages: ['@relayfile/core'],
    probe: async () => { looks += 1; return looks >= 3; },
    sleep: noSleep, log: () => {},
  });
  assert.equal(result.attempts, 3);
});

test('a version that never appears throws — it does NOT silently pass', async () => {
  await assert.rejects(
    waitForRegistryPropagation({
      version: VERSION,
      packages: ['@relayfile/core'],
      probe: async () => false,
      sleep: noSleep, attempts: 4, log: () => {},
    }),
    /never became installable|never published|never served/,
  );
});

test('backoff grows exponentially and is capped', () => {
  assert.equal(backoffDelay({ attempt: 1, baseDelayMs: 5000, maxDelayMs: 30000 }), 5000);
  assert.equal(backoffDelay({ attempt: 2, baseDelayMs: 5000, maxDelayMs: 30000 }), 10000);
  assert.equal(backoffDelay({ attempt: 9, baseDelayMs: 5000, maxDelayMs: 30000 }), 30000);
});

/* ------------------------- the regression itself -------------------------- */

test('REGRESSION: the v0.10.54 timeline now survives; the old un-retried step does not', async () => {
  // The real sequence: the root lockfile regenerates fine, then the sdk lockfile
  // hits ETARGET on @relayfile/core because the packument CDN has not caught up.
  const timeline = () => [
    { code: 0, stdout: 'up to date, audited 574 packages', stderr: '' },
    { code: 1, stdout: '', stderr: REAL_ETARGET_OUTPUT },
    { code: 1, stdout: '', stderr: REAL_ETARGET_OUTPUT },
  ];

  // Old behaviour: one shot per command, no retry. This is what the inline
  // shell step did, and it is what broke 0.10.51, 0.10.53 and 0.10.54.
  const before = scriptedRun(timeline());
  await assert.rejects(
    regenerateReleaseLockfiles({
      version: VERSION, requiredPackages: REQUIRED,
      commands: [['npm', ['install']], ['npm', ['install', '--prefix', 'packages/sdk/typescript']]],
      run: before.run, probe: async () => true,
      sleep: noSleep, attempts: 1, log: () => {},
    }),
    /ETARGET/,
  );

  // Fixed behaviour: the same registry, the same lag, a green release.
  const after = scriptedRun(timeline());
  await regenerateReleaseLockfiles({
    version: VERSION, requiredPackages: REQUIRED,
    commands: [['npm', ['install']], ['npm', ['install', '--prefix', 'packages/sdk/typescript']]],
    run: after.run, probe: async () => true,
    sleep: noSleep, attempts: 5, log: () => {},
  });
  assert.deepEqual(after.calls, [
    'npm install',
    'npm install --prefix packages/sdk/typescript',
    'npm install --prefix packages/sdk/typescript',
    'npm install --prefix packages/sdk/typescript',
  ]);
});

test('a fatal ETARGET aborts immediately instead of burning the retry budget', async () => {
  const { calls, run } = scriptedRun([
    { code: 1, stdout: '', stderr: 'npm error code ETARGET\nnpm error notarget No matching version found for @relayfile/core@9.9.9.' },
  ]);
  await assert.rejects(
    regenerateReleaseLockfiles({
      version: VERSION, requiredPackages: REQUIRED,
      commands: [['npm', ['install']]],
      run, probe: async () => true, sleep: noSleep, attempts: 5, log: () => {},
    }),
    /never published/,
  );
  assert.equal(calls.length, 1, 'must not retry a genuinely missing version');
});

test('persistent propagation failure ends the release non-zero', async () => {
  const { run } = scriptedRun([]);
  const alwaysEtarget = async () => ({ code: 1, stdout: '', stderr: REAL_ETARGET_OUTPUT });
  await assert.rejects(
    regenerateReleaseLockfiles({
      version: VERSION, requiredPackages: REQUIRED,
      commands: [['npm', ['install']]],
      run: alwaysEtarget, probe: async () => true, sleep: noSleep, attempts: 3, log: () => {},
    }),
    /never became installable/,
  );
  void run;
});
