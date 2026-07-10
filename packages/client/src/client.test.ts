import { type ChildProcess, spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  MIN_RELAYFILE_VERSION,
  RELAYFILE_API_VERSION,
  RelayfileControlPlaneClient,
  RelayfileControlPlaneError,
  assertRelayfileVersion,
  compareSemver,
  defaultRelayfileSocketPath,
} from './client.js';

describe('assertRelayfileVersion', () => {
  it('accepts the minimum and newer, tolerates v-prefix/words', () => {
    expect(() => assertRelayfileVersion(MIN_RELAYFILE_VERSION)).not.toThrow();
    expect(() => assertRelayfileVersion('v0.99.0')).not.toThrow();
    expect(() => assertRelayfileVersion('relayfile 1.2.3 (abc)')).not.toThrow();
  });
  it('rejects older + unparseable', () => {
    expect(() => assertRelayfileVersion('0.10.16')).toThrow(/0\.10\.17 is required/);
    expect(() => assertRelayfileVersion('nope')).toThrow(/unparseable version/);
  });
  it('treats missing semver components as zero', () => {
    expect(compareSemver('1', '1.0.0')).toBe(0);
    expect(compareSemver('1.2', '1.1.9')).toBeGreaterThan(0);
  });
});

describe('defaultRelayfileSocketPath', () => {
  const saved = { sock: process.env.RELAYFILE_SOCK, xdg: process.env.XDG_RUNTIME_DIR };
  afterEach(() => {
    if (saved.sock === undefined) delete process.env.RELAYFILE_SOCK;
    else process.env.RELAYFILE_SOCK = saved.sock;
    if (saved.xdg === undefined) delete process.env.XDG_RUNTIME_DIR;
    else process.env.XDG_RUNTIME_DIR = saved.xdg;
  });
  it('prefers RELAYFILE_SOCK, then XDG, then tmpdir', () => {
    process.env.RELAYFILE_SOCK = '/run/custom.sock';
    expect(defaultRelayfileSocketPath()).toBe('/run/custom.sock');
    delete process.env.RELAYFILE_SOCK;
    process.env.XDG_RUNTIME_DIR = '/run/user/1000';
    expect(defaultRelayfileSocketPath()).toBe('/run/user/1000/relayfile.sock');
    delete process.env.XDG_RUNTIME_DIR;
    expect(defaultRelayfileSocketPath()).toBe(join(tmpdir(), 'relayfile.sock'));
  });
});

describe('RelayfileControlPlaneClient lifecycle', () => {
  it('require-daemon mode (autoStart:false) fails fast with an actionable error', async () => {
    const client = new RelayfileControlPlaneClient({
      socketPath: join(tmpdir(), `rf-absent-${process.pid}.sock`),
      autoStart: false,
    });
    await expect(client.ensureReady()).rejects.toMatchObject({ code: 'DAEMON_UNAVAILABLE' });
    await expect(client.ensureReady()).rejects.toThrow(/control-plane serve|not running/);
  });

  it('rejects a daemon whose supportedApiVersions excludes this client', async () => {
    const client = new RelayfileControlPlaneClient({ socketPath: '/nope.sock', autoStart: false });
    vi.spyOn(client, 'hello').mockResolvedValue({
      daemonVersion: '0.10.17',
      apiVersion: 1,
      supportedApiVersions: [1],
    });
    await expect(client.ensureReady()).rejects.toMatchObject({ code: 'VERSION_INCOMPATIBLE' });
  });

  it('rejects a daemon older than the minimum version', async () => {
    const client = new RelayfileControlPlaneClient({ socketPath: '/nope.sock', autoStart: false });
    vi.spyOn(client, 'hello').mockResolvedValue({
      daemonVersion: '0.10.16',
      apiVersion: RELAYFILE_API_VERSION,
      supportedApiVersions: [RELAYFILE_API_VERSION],
    });
    await expect(client.ensureReady()).rejects.toThrow(/0\.10\.17 is required/);
  });

  it('auto-start with a missing binary fails fast with DAEMON_UNAVAILABLE (no crash)', async () => {
    const client = new RelayfileControlPlaneClient({
      socketPath: join(tmpdir(), `rf-missing-bin-${process.pid}.sock`),
      binary: join(tmpdir(), 'definitely-not-a-relayfile-binary-xyz'),
      autoStart: true,
      startTimeoutMs: 3000,
    });
    await expect(client.ensureReady()).rejects.toMatchObject({ code: 'DAEMON_UNAVAILABLE' });
  }, 10000);

  it('does not cache a failed readiness probe (retries next call)', async () => {
    const client = new RelayfileControlPlaneClient({ socketPath: '/nope.sock', autoStart: false });
    const hello = vi
      .spyOn(client, 'hello')
      .mockRejectedValueOnce(new RelayfileControlPlaneError('DAEMON_UNAVAILABLE', 'down'))
      .mockResolvedValue({
        daemonVersion: '0.10.17',
        apiVersion: RELAYFILE_API_VERSION,
        supportedApiVersions: [RELAYFILE_API_VERSION],
      });
    await expect(client.ensureReady()).rejects.toMatchObject({ code: 'DAEMON_UNAVAILABLE' });
    await expect(client.ensureReady()).resolves.toBeUndefined();
    expect(hello).toHaveBeenCalledTimes(2);
  });
});

describe('RelayfileControlPlaneClient integration webhook subscriptions', () => {
  it('posts and deletes webhook subscriptions through the control-plane socket', async () => {
    const client = new RelayfileControlPlaneClient({ socketPath: '/nope.sock', autoStart: false });
    vi.spyOn(client, 'ensureReady').mockResolvedValue(undefined);
    const rawRequest = vi.spyOn(client as unknown as { rawRequest: (opts: unknown) => Promise<unknown> }, 'rawRequest');
    rawRequest.mockResolvedValueOnce({ subscriptionId: 'whsub_123' }).mockResolvedValueOnce({ ok: true });

    await expect(
      client.createWebhookSubscription({
        workspace: 'demo',
        url: 'https://cast.test/v1/integrations/relayfile/inbound/ws/ch',
        pathGlobs: ['/github/repos/acme/widgets/issues/**'],
        secret: 'inbound-secret',
      })
    ).resolves.toEqual({ subscriptionId: 'whsub_123' });

    await expect(client.deleteWebhookSubscription('whsub_123', 'demo')).resolves.toEqual({ ok: true });

    expect(rawRequest).toHaveBeenNthCalledWith(1, {
      method: 'POST',
      path: '/v1/integrations/webhook-subscriptions',
      body: {
        workspace: 'demo',
        url: 'https://cast.test/v1/integrations/relayfile/inbound/ws/ch',
        pathGlobs: ['/github/repos/acme/widgets/issues/**'],
        secret: 'inbound-secret',
      },
    });
    expect(rawRequest).toHaveBeenNthCalledWith(2, {
      method: 'DELETE',
      path: '/v1/integrations/webhook-subscriptions',
      body: { subscriptionId: 'whsub_123', workspace: 'demo' },
    });
  });

  it('lists webhook subscriptions through the control-plane socket', async () => {
    const client = new RelayfileControlPlaneClient({ socketPath: '/nope.sock', autoStart: false });
    vi.spyOn(client, 'ensureReady').mockResolvedValue(undefined);
    const rawRequest = vi.spyOn(client as unknown as { rawRequest: (opts: unknown) => Promise<unknown> }, 'rawRequest');
    rawRequest.mockResolvedValueOnce({
      subscriptions: [
        {
          subscriptionId: 'whsub_123',
          url: 'https://cast.test/v1/integrations/relayfile/inbound/ws/ch',
          pathGlobs: ['/github/repos/acme/widgets/issues/**'],
        },
      ],
    });

    await expect(client.listWebhookSubscriptions('demo')).resolves.toEqual({
      subscriptions: [
        {
          subscriptionId: 'whsub_123',
          url: 'https://cast.test/v1/integrations/relayfile/inbound/ws/ch',
          pathGlobs: ['/github/repos/acme/widgets/issues/**'],
        },
      ],
    });

    expect(rawRequest).toHaveBeenCalledWith({
      method: 'GET',
      path: '/v1/integrations/webhook-subscriptions',
      query: { workspace: 'demo' },
    });
  });
});

// Real-daemon contract tests — opt-in via RELAYFILE_BIN (CI builds the binary:
// `go build -o relayfile ./cmd/relayfile-cli`). Boots the daemon and drives the
// client over the socket.
const RELAYFILE_BIN = process.env.RELAYFILE_BIN?.trim();
const describeContract = RELAYFILE_BIN ? describe : describe.skip;

describeContract('control-plane client (real daemon)', () => {
  const sock = join(tmpdir(), `rf-client-${process.pid}.sock`);
  let home: string;
  let daemon: ChildProcess;
  const client = new RelayfileControlPlaneClient({ socketPath: sock, autoStart: false });

  beforeAll(async () => {
    home = mkdtempSync(join(tmpdir(), 'relayfile-client-'));
    daemon = spawn(RELAYFILE_BIN!, ['control-plane', 'serve', '--sock', sock], {
      env: { ...process.env, HOME: home, RELAYFILE_SOCK: sock },
      stdio: 'ignore',
    });
    const deadline = Date.now() + 5000;
    for (;;) {
      try {
        await client.hello();
        return;
      } catch (err) {
        if (Date.now() > deadline) throw err;
        await new Promise((r) => setTimeout(r, 100));
      }
    }
  });

  afterAll(() => {
    daemon?.kill('SIGTERM');
    rmSync(sock, { force: true });
    rmSync(home, { recursive: true, force: true });
  });

  beforeEach(() => undefined);

  it('hello() negotiates daemon version + api version', async () => {
    const hello = await client.hello();
    expect(hello.supportedApiVersions).toContain(RELAYFILE_API_VERSION);
    expect(() => assertRelayfileVersion(hello.daemonVersion)).not.toThrow();
  });

  it('resolvePath() maps native -> glob and is idempotent', async () => {
    expect((await client.resolvePath('github', 'owner/repo')).pathGlob).toBe('/github/repos/owner/repo/**');
    expect((await client.resolvePath('github', '/github/repos/owner/repo/**')).pathGlob).toBe(
      '/github/repos/owner/repo/**'
    );
  });

  it('bind -> listBindings -> unbind round-trips on the resolved glob', async () => {
    const { pathGlob } = await client.resolvePath('github', 'acme/widgets');
    await client.bind({
      provider: 'github',
      resource: pathGlob,
      channel: 'general',
      webhookId: 'wh',
      webhookToken: 'tok',
      subscriptionId: 'sub',
      webhookSubscriptionId: 'whsub',
    });
    const after = await client.listBindings();
    const binding = after.find((b) => b.pathGlob === pathGlob);
    expect(binding).toBeDefined();
    expect(binding!.channel).toBe('general');
    expect(binding!.webhookSubscriptionId).toBe('whsub');
    await client.unbind('github', pathGlob);
    expect((await client.listBindings()).find((b) => b.pathGlob === pathGlob)).toBeUndefined();
  });
});
