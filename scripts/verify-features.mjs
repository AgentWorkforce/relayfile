#!/usr/bin/env node
import { mkdirSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { join } from 'node:path';

const args = process.argv.slice(2);
const valueAfter = (name) => {
  const index = args.indexOf(name);
  return index >= 0 ? args[index + 1] : undefined;
};
const requested = valueAfter('--tier') ?? '1';
const requireLive = args.includes('--require-live');
const jsonOnly = args.includes('--json');
const tierList = requested === 'deterministic' ? [1, 2, 4] : requested === 'all' ? [1, 2, 3, 4, 5, 6] : requested.split(',').map(Number);
if (tierList.some((tier) => !Number.isInteger(tier) || tier < 1 || tier > 6)) {
  console.error('usage: node scripts/verify-features.mjs --tier <1..6|comma-list|deterministic|all> [--require-live] [--json]');
  process.exit(2);
}

const results = [];
function record(status, tier, check, detail) {
  const item = { status, tier, check, detail };
  results.push(item);
  if (!jsonOnly) console.log(`${status} tier=${tier} check=${check} detail=${detail}`);
}

function command(tier, check, executable, commandArgs, env = {}) {
  const result = spawnSync(executable, commandArgs, {
    cwd: process.cwd(), env: { ...process.env, ...env }, encoding: 'utf8',
  });
  if (!jsonOnly && result.stdout) process.stdout.write(result.stdout);
  if (!jsonOnly && result.stderr) process.stderr.write(result.stderr);
  if (result.status === 0) record('PASS', tier, check, `${executable} ${commandArgs.join(' ')}`);
  else record('FAIL', tier, check, `exit=${result.status}: ${executable} ${commandArgs.join(' ')}`);
}

function freePort() {
  const result = spawnSync(process.execPath, ['-e', [
    'const net=require("node:net");',
    'const server=net.createServer();',
    'server.listen(0,"127.0.0.1",()=>{console.log(server.address().port);server.close();});',
  ].join('')], { encoding: 'utf8' });
  const port = Number(result.stdout.trim());
  if (result.status !== 0 || !Number.isInteger(port) || port < 1 || port > 65_535) {
    throw new Error(`failed to allocate loopback port: ${result.stderr || result.stdout}`);
  }
  return String(port);
}

for (const tier of tierList) {
  if (tier === 1) {
    command(1, 'catalog-validator', 'node', ['scripts/validate-feature-catalog.mjs']);
    command(1, 'catalog-and-guardian-contracts', 'npx', ['tsx', '--test',
      '.agentworkforce/agents/relayfile-feature-guardian/manifest-contract.test.ts',
      '.agentworkforce/agents/relayfile-feature-guardian/agent.test.ts']);
    command(1, 'sdk-parity', 'node', ['scripts/check-sdk-parity.mjs']);
    command(1, 'openapi-contract-surface', 'bash', ['scripts/check-contract-surface.sh']);
  } else if (tier === 2) {
    const cleanEnv = { RELAYFILE_DEV_MODE: '1', RELAYFILE_BASE_URL: '', RELAYFILE_PORT: freePort() };
    command(2, 'local-conformance', 'npx', ['tsx', 'scripts/conformance.ts', '--ci'], cleanEnv);
    command(2, 'two-mount-e2e', 'npx', ['tsx', 'scripts/e2e.ts', '--ci'], { RELAYFILE_PORT: freePort() });
    if (!process.env.RELAYFILE_TEST_POSTGRES_DSN) record('SKIP', 2, 'postgres-backend', 'RELAYFILE_TEST_POSTGRES_DSN is absent');
  } else if (tier === 3) {
    if (process.env.RELAYFILE_LIVE_FUSE !== '1') record('SKIP', 3, 'fuse-live', 'RELAYFILE_LIVE_FUSE=1 is required');
    else {
      command(3, 'fuse-unit-and-host-support', 'go', ['test', './internal/mountfuse', '-count=1']);
      record('MANUAL', 3, 'fuse-mount-cycle', 'run the named FUSE procedure with an authorized disposable mountpoint');
    }
  } else if (tier === 4) {
    command(4, 'packed-clean-consumer', 'node', ['scripts/packed-feature-e2e.mjs']);
  } else if (tier === 5) {
    if (process.env.RELAYFILE_LIVE !== '1') record('SKIP', 5, 'hosted-disposable-flow', 'RELAYFILE_LIVE=1 and disposable hosted credentials are required');
    else record('MANUAL', 5, 'hosted-disposable-flow', 'live opt-in is present, but the checked-in SDK E2E is mocked; run the named hosted procedures and confirm cleanup before recording PASS');
  } else if (tier === 6) {
    if (process.env.RELAYFILE_PROVIDER_LIVE !== '1') record('SKIP', 6, 'provider-and-release', 'RELAYFILE_PROVIDER_LIVE=1 is absent');
    else record('MANUAL', 6, 'provider-and-release', 'run the provider-specific named procedure; publish/tag/release remains manual');
  }
}

const totals = { PASS: 0, FAIL: 0, SKIP: 0, MANUAL: 0 };
for (const result of results) totals[result.status] += 1;
const report = { generatedAt: new Date().toISOString(), requested, requireLive, totals, results };
mkdirSync(join('.workflow-artifacts', 'verify-features'), { recursive: true });
writeFileSync(join('.workflow-artifacts', 'verify-features', 'latest.json'), `${JSON.stringify(report, null, 2)}\n`);
console.log(JSON.stringify(report));
if (totals.FAIL > 0 || (requireLive && (totals.SKIP > 0 || totals.MANUAL > 0))) process.exitCode = 1;
