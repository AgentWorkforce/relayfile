#!/usr/bin/env npx tsx
/**
 * Relayfile E2E Smoke Test
 *
 * Spins up a relayfile server + two mount daemons, then exercises the full
 * read/write/sync cycle between two agents sharing the same workspace.
 *
 * Usage:
 *   npx tsx scripts/e2e.ts                          # default (interactive)
 *   npx tsx scripts/e2e.ts --ci                      # CI mode (shorter pauses)
 *   npx tsx scripts/e2e.ts --continue-on-failure     # keep running after failures
 */

import { execSync, spawn, ChildProcess } from 'node:child_process';
import { createHmac } from 'node:crypto';
import { existsSync, readFileSync, writeFileSync, unlinkSync, mkdirSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------
const flags = new Set(process.argv.slice(2).filter((a) => a.startsWith('--')));
const CI = flags.has('--ci') || !!process.env.CI;
const CONTINUE_ON_FAILURE = flags.has('--continue-on-failure');

const PORT = 9090;
const BASE_URL = `http://127.0.0.1:${PORT}`;
const WORKSPACE = 'e2e-test';
const JWT_SECRET = 'test-secret';

// ---------------------------------------------------------------------------
// Terminal colors
// ---------------------------------------------------------------------------
const R = '\x1b[0m';
const B = '\x1b[1m';
const DIM = '\x1b[2m';
const RED = '\x1b[31m';
const GREEN = '\x1b[32m';
const YELLOW = '\x1b[33m';
const CYAN = '\x1b[36m';

function ts() {
  return `${DIM}${new Date().toISOString().slice(11, 23)}${R}`;
}

function log(icon: string, msg: string) {
  console.log(`${ts()} ${icon} ${msg}`);
}

function step(label: string) {
  console.log(`\n${ts()} ${YELLOW}${B}▸ ${label}${R}`);
}

function ok(msg: string) {
  log('✅', `${GREEN}${msg}${R}`);
}

function fail(msg: string) {
  log('❌', `${RED}${msg}${R}`);
}

function sleep(ms: number) {
  return new Promise<void>((r) => setTimeout(r, ms));
}

// ---------------------------------------------------------------------------
// JWT generation (mirrors generate-dev-token.sh)
// ---------------------------------------------------------------------------
function base64url(buf: Buffer): string {
  return buf.toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function generateToken(workspaceId: string, agentName: string, scopes: string[], expSeconds: number): string {
  const header = { alg: 'HS256', typ: 'JWT' };
  const payload = {
    workspace_id: workspaceId,
    agent_name: agentName,
    scopes,
    exp: Math.floor(Date.now() / 1000) + expSeconds,
    aud: 'relayfile',
  };
  const h = base64url(Buffer.from(JSON.stringify(header)));
  const p = base64url(Buffer.from(JSON.stringify(payload)));
  const sig = createHmac('sha256', JWT_SECRET).update(`${h}.${p}`).digest();
  return `${h}.${p}.${base64url(sig)}`;
}

// ---------------------------------------------------------------------------
// Process management
// ---------------------------------------------------------------------------
const children: ChildProcess[] = [];

function killAll() {
  for (const child of children) {
    try {
      child.kill('SIGTERM');
    } catch {}
  }
}

function spawnTracked(cmd: string, args: string[], env?: Record<string, string>): ChildProcess {
  const child = spawn(cmd, args, {
    env: { ...process.env, ...env },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  children.push(child);
  return child;
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------
let correlationCounter = 0;
function nextCorrelationId(): string {
  return `e2e_${Date.now()}_${++correlationCounter}`;
}

const TOKEN = generateToken(WORKSPACE, 'e2e', ['fs:read', 'fs:write', 'sync:read', 'ops:read'], 3600);

async function api(method: string, path: string, body?: unknown, headers?: Record<string, string>): Promise<{ status: number; data: any }> {
  const url = `${BASE_URL}${path}`;
  const opts: RequestInit = {
    method,
    headers: {
      Authorization: `Bearer ${TOKEN}`,
      'X-Correlation-Id': nextCorrelationId(),
      'Content-Type': 'application/json',
      ...headers,
    },
  };
  if (body !== undefined) {
    opts.body = JSON.stringify(body);
  }
  const resp = await fetch(url, opts);
  const text = await resp.text();
  let data: any;
  try {
    data = JSON.parse(text);
  } catch {
    data = text;
  }
  return { status: resp.status, data };
}

// ---------------------------------------------------------------------------
// Wait helpers
// ---------------------------------------------------------------------------
async function waitForHealth(timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const resp = await fetch(`${BASE_URL}/health`);
      if (resp.ok) return;
    } catch {}
    await sleep(200);
  }
  throw new Error(`Health check timed out after ${timeoutMs}ms`);
}

async function waitForFile(filePath: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (existsSync(filePath)) return;
    await sleep(200);
  }
  throw new Error(`Timed out waiting for file: ${filePath}`);
}

async function waitForFileContent(filePath: string, expected: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (existsSync(filePath)) {
      const content = readFileSync(filePath, 'utf-8');
      if (content.includes(expected)) return;
    }
    await sleep(200);
  }
  const actual = existsSync(filePath) ? readFileSync(filePath, 'utf-8') : '<file does not exist>';
  throw new Error(`Timed out waiting for content "${expected}" in ${filePath}. Actual: "${actual}"`);
}

async function waitForFileAbsent(filePath: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (!existsSync(filePath)) return;
    await sleep(200);
  }
  throw new Error(`Timed out waiting for file to disappear: ${filePath}`);
}

function assert(condition: boolean, msg: string): void {
  if (!condition) throw new Error(`Assertion failed: ${msg}`);
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------
async function main() {
  console.log(`
${B}${CYAN}╔══════════════════════════════════════════════╗
║          Relayfile E2E Smoke Test            ║
╚══════════════════════════════════════════════╝${R}
`);
  log('🌐', `Server: ${B}${BASE_URL}${R}`);
  log('⚙️ ', `Mode:   ${B}${CI ? 'CI (auto)' : 'Interactive'}${R}`);
  log('🧭', `Flow:   ${B}${CONTINUE_ON_FAILURE ? 'Continue on failure' : 'Fail fast (default)'}${R}`);

  const passed: string[] = [];
  const failed: string[] = [];

  const SYNC_WAIT = CI ? 8_000 : 12_000;
  const MOUNT_WAIT = CI ? 10_000 : 15_000;

  // Temp dirs
  const tmpBase = join(tmpdir(), `relayfile-e2e-${Date.now()}`);
  const agentADir = join(tmpBase, 'agent-a');
  const agentBDir = join(tmpBase, 'agent-b');
  mkdirSync(agentADir, { recursive: true });
  mkdirSync(agentBDir, { recursive: true });

  async function run(name: string, fn: () => Promise<void>) {
    try {
      await fn();
      ok(name);
      passed.push(name);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      fail(`${name}: ${msg}`);
      failed.push(name);
      if (!CONTINUE_ON_FAILURE) {
        throw new Error(`${name} failed: ${msg}`);
      }
    }
  }

  try {
    // ------------------------------------------------------------------
    // Build
    // ------------------------------------------------------------------
    step('Building relayfile and relayfile-mount');
    execSync('go build -o relayfile ./cmd/relayfile && go build -o relayfile-mount ./cmd/relayfile-mount', {
      cwd: process.cwd(),
      stdio: 'inherit',
    });
    ok('Build succeeded');

    // ------------------------------------------------------------------
    // Start server
    // ------------------------------------------------------------------
    step('Starting relayfile server');
    const server = spawnTracked('./relayfile', [], {
      RELAYFILE_ADDR: `:${PORT}`,
      RELAYFILE_BACKEND_PROFILE: 'memory',
      RELAYFILE_JWT_SECRET: JWT_SECRET,
      RELAYFILE_EXTERNAL_WRITEBACK: 'false',
    });
    server.stderr?.on('data', (d: Buffer) => {
      const line = d.toString().trim();
      if (line) log('📡', `${DIM}[server] ${line}${R}`);
    });

    await waitForHealth(10_000);
    ok('Server healthy');

    // ------------------------------------------------------------------
    // Start mount daemons
    // ------------------------------------------------------------------
    step('Starting mount daemons');
    const mountA = spawnTracked('./relayfile-mount', [
      '--base-url', BASE_URL,
      '--workspace', WORKSPACE,
      '--local-dir', agentADir,
      '--token', TOKEN,
      '--interval', '1s',
    ]);
    mountA.stderr?.on('data', (d: Buffer) => {
      const line = d.toString().trim();
      if (line) log('🔄', `${DIM}[mount-a] ${line}${R}`);
    });

    const mountB = spawnTracked('./relayfile-mount', [
      '--base-url', BASE_URL,
      '--workspace', WORKSPACE,
      '--local-dir', agentBDir,
      '--token', TOKEN,
      '--interval', '1s',
    ]);
    mountB.stderr?.on('data', (d: Buffer) => {
      const line = d.toString().trim();
      if (line) log('🔄', `${DIM}[mount-b] ${line}${R}`);
    });

    // Give daemons a moment to start
    await sleep(1000);
    ok('Mount daemons started');

    // ------------------------------------------------------------------
    // Test: Seed files via API
    // ------------------------------------------------------------------
    await run('Seed files via API', async () => {
      step('Seeding files via API');

      const files = [
        { path: '/src/index.ts', content: 'export const version = "1.0";' },
        { path: '/src/utils.ts', content: 'export function add(a: number, b: number) { return a + b; }' },
        { path: '/package.json', content: '{"name":"test","version":"1.0.0"}' },
      ];

      for (const f of files) {
        const resp = await api('PUT', `/v1/workspaces/${WORKSPACE}/fs/file?path=${encodeURIComponent(f.path)}`, {
          contentType: 'text/plain',
          content: f.content,
        }, { 'If-Match': '0' });
        assert(resp.status === 200 || resp.status === 201 || resp.status === 202,
          `Seed ${f.path} failed with status ${resp.status}: ${JSON.stringify(resp.data)}`);
        log('📝', `Seeded ${f.path}`);
      }

      const tree = await api('GET', `/v1/workspaces/${WORKSPACE}/fs/tree?path=/&depth=10`);
      assert(tree.status === 200, `Tree listing failed: ${tree.status}`);
      const entries = tree.data.entries || [];
      assert(entries.length >= 3, `Expected >= 3 tree entries, got ${entries.length}`);
    });

    // ------------------------------------------------------------------
    // Test: Mount daemon pulls files to Agent A
    // ------------------------------------------------------------------
    await run('Mount pulls files to Agent A', async () => {
      step('Waiting for Agent A to sync');
      await waitForFile(join(agentADir, 'src', 'index.ts'), MOUNT_WAIT);
      await waitForFile(join(agentADir, 'src', 'utils.ts'), MOUNT_WAIT);
      await waitForFile(join(agentADir, 'package.json'), MOUNT_WAIT);

      const content = readFileSync(join(agentADir, 'src', 'index.ts'), 'utf-8');
      assert(content.includes('version = "1.0"'), `Agent A index.ts content mismatch: ${content}`);
    });

    // ------------------------------------------------------------------
    // Test: Mount daemon pulls files to Agent B
    // ------------------------------------------------------------------
    await run('Mount pulls files to Agent B', async () => {
      step('Waiting for Agent B to sync');
      await waitForFile(join(agentBDir, 'src', 'index.ts'), MOUNT_WAIT);
      await waitForFile(join(agentBDir, 'src', 'utils.ts'), MOUNT_WAIT);
      await waitForFile(join(agentBDir, 'package.json'), MOUNT_WAIT);

      const content = readFileSync(join(agentBDir, 'src', 'index.ts'), 'utf-8');
      assert(content.includes('version = "1.0"'), `Agent B index.ts content mismatch: ${content}`);
    });

    // ------------------------------------------------------------------
    // Test: Agent A writes a file, Agent B sees it
    // ------------------------------------------------------------------
    await run('Agent A write syncs to Agent B', async () => {
      step('Agent A writes new-feature.ts');
      const featureContent = 'export const feature = true;';
      mkdirSync(join(agentADir, 'src'), { recursive: true });
      writeFileSync(join(agentADir, 'src', 'new-feature.ts'), featureContent);

      // Wait for Agent A daemon to push
      log('⏳', 'Waiting for Agent A to push...');
      await sleep(SYNC_WAIT);

      // Verify via API
      const resp = await api('GET', `/v1/workspaces/${WORKSPACE}/fs/file?path=${encodeURIComponent('/src/new-feature.ts')}`);
      assert(resp.status === 200, `API read new-feature.ts failed: ${resp.status} ${JSON.stringify(resp.data)}`);
      assert(resp.data.content.includes('feature = true'), `API content mismatch: ${resp.data.content}`);

      // Wait for Agent B daemon to pull
      log('⏳', 'Waiting for Agent B to pull...');
      await waitForFileContent(join(agentBDir, 'src', 'new-feature.ts'), 'feature = true', MOUNT_WAIT);
    });

    // ------------------------------------------------------------------
    // Test: Agent B edits a file, Agent A sees it
    // ------------------------------------------------------------------
    await run('Agent B edit syncs to Agent A', async () => {
      step('Agent B edits index.ts');
      writeFileSync(join(agentBDir, 'src', 'index.ts'), 'export const version = "2.0";');

      log('⏳', 'Waiting for sync cycle...');
      await waitForFileContent(join(agentADir, 'src', 'index.ts'), 'version = "2.0"', SYNC_WAIT + MOUNT_WAIT);
    });

    // ------------------------------------------------------------------
    // Test: Events feed
    // ------------------------------------------------------------------
    await run('Events feed contains entries', async () => {
      step('Checking events feed');
      const resp = await api('GET', `/v1/workspaces/${WORKSPACE}/fs/events?limit=50`);
      assert(resp.status === 200, `Events feed failed: ${resp.status}`);
      const events = resp.data.events || [];
      assert(events.length > 0, 'Events feed is empty');

      const types = events.map((e: any) => e.type);
      assert(types.includes('file.created'), `No file.created events. Types: ${types.join(', ')}`);
      log('📊', `Found ${events.length} events: ${[...new Set(types)].join(', ')}`);
    });

    // ------------------------------------------------------------------
    // Test: Revision conflict detection
    // ------------------------------------------------------------------
    await run('Revision conflict detection', async () => {
      step('Testing revision conflict');
      const readResp = await api('GET', `/v1/workspaces/${WORKSPACE}/fs/file?path=${encodeURIComponent('/src/index.ts')}`);
      assert(readResp.status === 200, `Read index.ts failed: ${readResp.status}`);

      const wrongRevision = 'wrong-rev-123';
      const writeResp = await api('PUT', `/v1/workspaces/${WORKSPACE}/fs/file?path=${encodeURIComponent('/src/index.ts')}`, {
        contentType: 'text/plain',
        content: 'should fail',
      }, { 'If-Match': wrongRevision });
      assert(writeResp.status === 409, `Expected 409 Conflict, got ${writeResp.status}: ${JSON.stringify(writeResp.data)}`);
      log('🔒', `Conflict correctly returned: ${writeResp.data.code}`);
    });

    // ------------------------------------------------------------------
    // Test: File deletion syncs
    // ------------------------------------------------------------------
    await run('File deletion syncs', async () => {
      step('Agent A deletes utils.ts');
      const utilsPath = join(agentADir, 'src', 'utils.ts');
      assert(existsSync(utilsPath), 'utils.ts should exist before deletion');
      unlinkSync(utilsPath);

      log('⏳', 'Waiting for deletion to sync...');
      await waitForFileAbsent(join(agentBDir, 'src', 'utils.ts'), SYNC_WAIT + MOUNT_WAIT);

      // Also verify via API
      const resp = await api('GET', `/v1/workspaces/${WORKSPACE}/fs/file?path=${encodeURIComponent('/src/utils.ts')}`);
      assert(resp.status === 404, `Expected 404 for deleted file, got ${resp.status}`);
    });

    // ------------------------------------------------------------------
    // Test: Bulk file performance
    // ------------------------------------------------------------------
    await run('Bulk file sync performance', async () => {
      step('Writing 50 files to Agent A workspace');
      const BULK_COUNT = 50;
      const bulkDir = join(agentADir, 'bulk');
      mkdirSync(bulkDir, { recursive: true });

      const startWrite = Date.now();
      for (let i = 0; i < BULK_COUNT; i++) {
        writeFileSync(join(bulkDir, `file-${String(i).padStart(3, '0')}.ts`), `export const n = ${i};`);
      }
      log('📝', `Wrote ${BULK_COUNT} files in ${Date.now() - startWrite}ms`);

      // Wait for all files to appear in Agent B
      const startSync = Date.now();
      const bulkTimeout = CI ? 60_000 : 90_000;
      const deadline = Date.now() + bulkTimeout;

      while (Date.now() < deadline) {
        let count = 0;
        for (let i = 0; i < BULK_COUNT; i++) {
          if (existsSync(join(agentBDir, 'bulk', `file-${String(i).padStart(3, '0')}.ts`))) {
            count++;
          }
        }
        if (count >= BULK_COUNT) break;
        if (Date.now() >= deadline) {
          throw new Error(`Only ${count}/${BULK_COUNT} files synced within ${bulkTimeout}ms`);
        }
        await sleep(500);
      }

      const syncLatency = Date.now() - startSync;
      log('⏱️ ', `${BULK_COUNT} files synced to Agent B in ${B}${syncLatency}ms${R}`);
    });

    // ------------------------------------------------------------------
    // Bulk API endpoint
    // ------------------------------------------------------------------
    await run('Bulk write API creates multiple files', async () => {
      step('Testing bulk write API');
      const bulkFiles = Array.from({ length: 5 }, (_, i) => ({
        path: `/bulk-api/file-${i}.txt`,
        content: `bulk content ${i}`,
      }));
      const { status, data } = await api('POST', `/v1/workspaces/${WORKSPACE}/fs/bulk`, { files: bulkFiles });
      if (status !== 202) throw new Error(`Bulk write returned ${status}: ${JSON.stringify(data)}`);
      if (data.written !== 5) throw new Error(`Expected 5 written, got ${data.written}`);
      if (data.errors && data.errors.length > 0) throw new Error(`Bulk write had errors: ${JSON.stringify(data.errors)}`);
      log('📦', `Bulk write: ${data.written} files created`);
    });

    // ------------------------------------------------------------------
    // Export endpoints
    // ------------------------------------------------------------------
    await run('JSON export returns files', async () => {
      step('Testing JSON export');
      const { status, data } = await api('GET', `/v1/workspaces/${WORKSPACE}/fs/export?format=json`);
      if (status !== 200) throw new Error(`JSON export returned ${status}`);
      if (!Array.isArray(data) || data.length === 0) throw new Error(`JSON export returned empty array`);
      log('📤', `JSON export: ${data.length} files`);
    });

    await run('Tar export returns gzip', async () => {
      step('Testing tar export');
      const url = `${BASE_URL}/v1/workspaces/${WORKSPACE}/fs/export?format=tar`;
      const res = await fetch(url, {
        headers: {
          Authorization: `Bearer ${TOKEN}`,
          'X-Correlation-Id': nextCorrelationId(),
        },
      });
      if (!res.ok) throw new Error(`Tar export returned ${res.status}`);
      const contentType = res.headers.get('content-type') ?? '';
      if (!contentType.includes('gzip')) throw new Error(`Expected gzip content-type, got ${contentType}`);
      const buf = Buffer.from(await res.arrayBuffer());
      // gzip magic bytes: 1f 8b
      if (buf[0] !== 0x1f || buf[1] !== 0x8b) throw new Error('Tar export is not valid gzip');
      log('📤', `Tar export: ${buf.length} bytes (gzip)`);
    });

    // ------------------------------------------------------------------
    // WebSocket real-time events
    // ------------------------------------------------------------------
    await run('WebSocket receives file change events', async () => {
      step('Testing WebSocket push');
      const wsUrl = `${BASE_URL.replace('http', 'ws')}/v1/workspaces/${WORKSPACE}/ws?token=${TOKEN}`;

      // Node 22+ has native WebSocket
      if (typeof globalThis.WebSocket === 'undefined') {
        log('⏭️ ', 'Skipping WebSocket test (Node < 22, no native WebSocket)');
        return;
      }

      const ws = new WebSocket(wsUrl);
      const events: unknown[] = [];

      await new Promise<void>((resolve, reject) => {
        const timeout = setTimeout(() => {
          ws.close();
          reject(new Error('WebSocket timeout waiting for events'));
        }, 10_000);

        ws.addEventListener('open', async () => {
          // Write a file to trigger an event
          await api('POST', `/v1/workspaces/${WORKSPACE}/fs/bulk`, {
            files: [{ path: '/ws-test.txt', content: 'websocket test' }],
          });
        });

        ws.addEventListener('message', (event) => {
          const data = JSON.parse(typeof event.data === 'string' ? event.data : '{}');
          events.push(data);
          if (events.length >= 1) {
            clearTimeout(timeout);
            ws.close();
            resolve();
          }
        });

        ws.addEventListener('error', () => {
          clearTimeout(timeout);
          reject(new Error('WebSocket connection error'));
        });
      });

      log('🔌', `WebSocket received ${events.length} event(s)`);
    });

  } finally {
    // ------------------------------------------------------------------
    // Teardown
    // ------------------------------------------------------------------
    step('Teardown');
    killAll();

    // Wait briefly for processes to exit
    await sleep(500);

    try {
      rmSync(tmpBase, { recursive: true, force: true });
      log('🗑️ ', 'Cleaned up temp dirs');
    } catch {}

    // Summary
    console.log(`
${B}${CYAN}╔══════════════════════════════════════════════╗
║                  Summary                     ║
╚══════════════════════════════════════════════╝${R}
`);
    if (passed.length > 0) {
      log('✅', `${GREEN}${B}${passed.length} passed${R}`);
      for (const t of passed) {
        log('  ', `${GREEN}• ${t}${R}`);
      }
    }
    if (failed.length > 0) {
      console.log();
      log('❌', `${RED}${B}${failed.length} failed${R}`);
      for (const t of failed) {
        log('  ', `${RED}• ${t}${R}`);
      }
    }
    console.log();

    if (failed.length > 0) {
      process.exit(1);
    }
  }
}

main().catch((err) => {
  fail(err instanceof Error ? err.message : String(err));
  killAll();
  process.exit(1);
});
