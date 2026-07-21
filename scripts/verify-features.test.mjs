import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

function runTier5({ live, requireLive = false }) {
  const env = { ...process.env };
  delete env.RELAYFILE_LIVE;
  if (live) env.RELAYFILE_LIVE = '1';
  const args = ['scripts/verify-features.mjs', '--tier', '5', '--json'];
  if (requireLive) args.push('--require-live');
  const result = spawnSync(process.execPath, args, { cwd: process.cwd(), env, encoding: 'utf8' });
  const lines = result.stdout.trim().split('\n');
  const report = JSON.parse(lines.at(-1));
  return { result, report };
}

test('tier 5 without live opt-in is SKIP and fails a required-live gate', () => {
  const optional = runTier5({ live: false });
  assert.equal(optional.result.status, 0);
  assert.deepEqual(optional.report.totals, { PASS: 0, FAIL: 0, SKIP: 1, MANUAL: 0 });

  const required = runTier5({ live: false, requireLive: true });
  assert.equal(required.result.status, 1);
  assert.deepEqual(required.report.totals, { PASS: 0, FAIL: 0, SKIP: 1, MANUAL: 0 });
});

test('tier 5 live opt-in stays MANUAL instead of passing the mocked SDK E2E', () => {
  const optional = runTier5({ live: true });
  assert.equal(optional.result.status, 0);
  assert.deepEqual(optional.report.totals, { PASS: 0, FAIL: 0, SKIP: 0, MANUAL: 1 });
  assert.match(optional.report.results[0].detail, /checked-in SDK E2E is mocked/);

  const required = runTier5({ live: true, requireLive: true });
  assert.equal(required.result.status, 1);
  assert.deepEqual(required.report.totals, { PASS: 0, FAIL: 0, SKIP: 0, MANUAL: 1 });
});
