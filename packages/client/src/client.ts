import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { request } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import type { components } from './generated/control-plane.js';

/**
 * Typed client for the relayfile control-plane: HTTP/JSON over a local unix
 * domain socket (`relayfile control-plane serve`). It replaces shelling out to
 * the `relayfile` CLI and parsing stdout — the contract is now the socket's
 * request/response shapes, version-negotiated via `/v1/hello`.
 *
 * Transport is Node's built-in `http.request({ socketPath })` — no dependency,
 * unix-socket support is native. Request/response types are generated from the
 * authoritative `openapi/relayfile-control-plane-v1.openapi.yaml`, so a field
 * rename in the contract is a compile error here.
 */

/** Control-plane API version this client speaks. Bump when adding required endpoint support. */
export const RELAYFILE_API_VERSION = 3;

/**
 * Minimum `relayfile` daemon version this client requires. API compatibility is
 * enforced independently through supportedApiVersions.
 */
export const MIN_RELAYFILE_VERSION = '0.10.17';

/** First published relayfile binary that can replace a stale daemon for API v3. */
const MIN_RELAYFILE_VERSION_FOR_CURRENT_API = '0.10.21';

const SEMVER_RE = /^\d+\.\d+\.\d+(?:[-+].*)?$/;

/** First semver-looking token in a string (tolerates a leading `v` and surrounding words). */
export function firstSemver(value: string): string {
  for (const raw of value.split(/\s+/)) {
    const token = raw.replace(/^v/, '');
    if (SEMVER_RE.test(token)) return token;
  }
  return '';
}

/** Compare two semvers by numeric core; -1 / 0 / 1. Pre-release/build metadata is ignored. */
export function compareSemver(a: string, b: string): number {
  const core = (v: string) => v.split(/[-+]/)[0]!.split('.').map((n) => Number.parseInt(n, 10) || 0);
  const [a0 = 0, a1 = 0, a2 = 0] = core(a);
  const [b0 = 0, b1 = 0, b2 = 0] = core(b);
  return a0 - b0 || a1 - b1 || a2 - b2;
}

/**
 * Pure version gate: throws an actionable error unless `version` (a daemon
 * version string) is a semver >= MIN_RELAYFILE_VERSION. Exported so the contract
 * it guards is unit-tested without a running daemon.
 */
export function assertRelayfileVersion(version: string): void {
  const parsed = firstSemver(version);
  if (!parsed || compareSemver(parsed, MIN_RELAYFILE_VERSION) < 0) {
    // Typed (not a bare Error) so callers that re-throw daemon/version failures
    // by code don't silently swallow it.
    throw new RelayfileControlPlaneError(
      'VERSION_INCOMPATIBLE',
      `relayfile >= ${MIN_RELAYFILE_VERSION} is required; found ${
        parsed ? `"${parsed}"` : `unparseable version "${version.trim()}"`
      }. Update relayfile (npm install -g relayfile@latest) or set RELAYFILE_BIN.`
    );
  }
}

/** Resolve the control-plane socket path, mirroring relayfile's Go default. */
export function defaultRelayfileSocketPath(): string {
  const explicit = process.env.RELAYFILE_SOCK?.trim();
  if (explicit) return explicit;
  const xdg = process.env.XDG_RUNTIME_DIR?.trim();
  if (xdg) return join(xdg, 'relayfile.sock');
  return join(tmpdir(), 'relayfile.sock');
}

function relayfileBinary(): string {
  return process.env.RELAYFILE_BIN?.trim() || 'relayfile';
}

/** Structured control-plane error carrying the daemon's typed code + HTTP status. */
export class RelayfileControlPlaneError extends Error {
  constructor(
    public readonly code: string,
    message: string,
    public readonly status?: number,
    /** True only for connection failures that may clear while a daemon starts. */
    public readonly transient = false
  ) {
    super(message);
    this.name = 'RelayfileControlPlaneError';
  }
}

// Request/response types are DERIVED from the control-plane OpenAPI contract
// (generated/control-plane.ts, from openapi/relayfile-control-plane-v1.openapi.yaml
// via `npm run codegen`). A field rename in the contract becomes a compile error
// here — the compile-time field-drift guarantee.
type Schemas = components['schemas'];

export type HelloResponse = Schemas['HelloResponse'];
export type RelayfileBindingRecord = Schemas['Binding'];
export type ResolvePathResult = Schemas['ResolveResourcePathResponse'];
export type BindResult = Schemas['BindResponse'];
export type BindRequestBody = Schemas['BindRequest'];
export type ConnectRequestBody = Schemas['ConnectProviderRequest'];
export type ConnectResult = Schemas['ConnectProviderResponse'];
export type ProviderStatusResult = Schemas['ProviderStatus'];
export type WritebackSecretResult = Schemas['WritebackSecret'];
export type WebhookSubscriptionRequestBody = Schemas['WebhookSubscriptionRequest'];
export type WebhookSubscriptionResult = Schemas['WebhookSubscriptionResponse'];
export type DeleteWebhookSubscriptionRequestBody = Schemas['DeleteWebhookSubscriptionRequest'];
export type DeleteWebhookSubscriptionResult = Schemas['DeleteWebhookSubscriptionResponse'];
export type ListWebhookSubscriptionsResult = Schemas['ListWebhookSubscriptionsResponse'];
export type WebhookSubscriptionSummary = Schemas['WebhookSubscriptionSummary'];

export interface RelayfileClientOptions {
  /** Socket to connect to. Defaults to defaultRelayfileSocketPath(). */
  socketPath?: string;
  /** Binary used to auto-start the daemon. Defaults to RELAYFILE_BIN or `relayfile`. */
  binary?: string;
  /**
   * Auto-start `relayfile control-plane serve` when the socket is absent.
   * Defaults to true unless RELAYFILE_REQUIRE_DAEMON=1 (strict, never-spawn mode).
   */
  autoStart?: boolean;
  /** How long to wait for an auto-started daemon to answer /v1/hello. */
  startTimeoutMs?: number;
  /** Per-request timeout. A hung socket rejects instead of blocking forever. */
  requestTimeoutMs?: number;
}

interface RequestOptions {
  method: 'DELETE' | 'GET' | 'POST';
  path: string;
  query?: Record<string, string | undefined>;
  body?: unknown;
  /** Discovery hello intentionally omits the API header until negotiation completes. */
  includeVersionHeader?: boolean;
  /** Do not reuse a socket that may still point at an unlinked stale daemon. */
  freshConnection?: boolean;
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

const TRANSIENT_CONNECTION_CODES = new Set(['ENOENT', 'ECONNREFUSED', 'ETIMEDOUT']);

function isTransientConnectionCode(code: string | undefined): boolean {
  return code != null && TRANSIENT_CONNECTION_CODES.has(code);
}

function isTransientControlPlaneError(err: unknown): err is RelayfileControlPlaneError {
  return (
    err instanceof RelayfileControlPlaneError && err.code === 'DAEMON_UNAVAILABLE' && err.transient
  );
}

function shellQuote(value: string): string {
  return /^[A-Za-z0-9_./:@%+=,-]+$/.test(value) ? value : `'${value.replaceAll("'", "'\\''")}'`;
}

interface SpawnedDaemon {
  getError(): Error | undefined;
}

interface CommandResult {
  stdout: string;
  stderr: string;
  code: number | null;
}

export class RelayfileControlPlaneClient {
  private readonly socketPath: string;
  private readonly binary: string;
  private readonly autoStart: boolean;
  private readonly startTimeoutMs: number;
  private readonly requestTimeoutMs: number;
  private ready: Promise<void> | undefined;

  constructor(options: RelayfileClientOptions = {}) {
    this.socketPath = options.socketPath ?? defaultRelayfileSocketPath();
    this.binary = options.binary ?? relayfileBinary();
    this.autoStart = options.autoStart ?? process.env.RELAYFILE_REQUIRE_DAEMON !== '1';
    this.startTimeoutMs = options.startTimeoutMs ?? 5000;
    this.requestTimeoutMs = options.requestTimeoutMs ?? 10000;
  }

  /** Low-level request over the unix socket. Throws RelayfileControlPlaneError. */
  private rawRequest<T>(opts: RequestOptions): Promise<T> {
    const query = opts.query
      ? Object.entries(opts.query)
          .filter(([, v]) => v != null && v !== '')
          .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
          .join('&')
      : '';
    const path = query ? `${opts.path}?${query}` : opts.path;
    const payload = opts.body == null ? undefined : JSON.stringify(opts.body);

    return new Promise<T>((resolve, reject) => {
      let settled = false;
      const fail = (err: RelayfileControlPlaneError) => {
        if (settled) return;
        settled = true;
        reject(err);
      };
      const ok = (value: T) => {
        if (settled) return;
        settled = true;
        resolve(value);
      };

      const req = request(
        {
          socketPath: this.socketPath,
          method: opts.method,
          path,
          timeout: this.requestTimeoutMs,
          // Discovery must open a fresh socket. A pooled keep-alive connection
          // would keep reaching the unlinked stale daemon after replacement.
          agent: opts.freshConnection ? false : undefined,
          headers: {
            ...(opts.includeVersionHeader === false
              ? {}
              : { 'X-Relayfile-API-Version': String(RELAYFILE_API_VERSION) }),
            Accept: 'application/json',
            ...(payload
              ? { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(payload) }
              : {}),
          },
        },
        (res) => {
          // The response stream can emit 'error' (e.g. socket reset mid-body);
          // without a listener that's an unhandled error -> crash.
          res.on('error', (err: NodeJS.ErrnoException) =>
            fail(
              new RelayfileControlPlaneError(
                'DAEMON_UNAVAILABLE',
                err.message,
                undefined,
                isTransientConnectionCode(err.code)
              )
            )
          );
          const chunks: Buffer[] = [];
          res.on('data', (c) => chunks.push(c as Buffer));
          res.on('end', () => {
            const text = Buffer.concat(chunks).toString('utf8');
            const status = res.statusCode ?? 0;
            let parsed: unknown;
            try {
              parsed = text ? JSON.parse(text) : undefined;
            } catch {
              fail(
                new RelayfileControlPlaneError(
                  'DAEMON_UNAVAILABLE',
                  `relayfile control-plane returned non-JSON (status ${status}): ${text.slice(0, 200)}`,
                  status
                )
              );
              return;
            }
            if (status >= 200 && status < 300) {
              ok(parsed as T);
              return;
            }
            const err = (parsed as { error?: { code?: string; message?: string } })?.error;
            fail(
              new RelayfileControlPlaneError(
                err?.code ?? 'DAEMON_UNAVAILABLE',
                err?.message ?? `relayfile control-plane error (status ${status})`,
                status
              )
            );
          });
        }
      );
      req.on('timeout', () => {
        req.destroy(
          new RelayfileControlPlaneError(
            'DAEMON_UNAVAILABLE',
            `relayfile control-plane request timed out after ${this.requestTimeoutMs}ms (${opts.method} ${opts.path})`,
            undefined,
            true
          )
        );
      });
      req.on('error', (err: NodeJS.ErrnoException) => {
        if (err instanceof RelayfileControlPlaneError) {
          fail(err);
          return;
        }
        // ENOENT (no socket file) / ECONNREFUSED (nothing listening) => daemon down.
        fail(
          new RelayfileControlPlaneError(
            'DAEMON_UNAVAILABLE',
            err.code === 'ENOENT' || err.code === 'ECONNREFUSED'
              ? `relayfile control-plane is not running at ${this.socketPath} (${err.code})`
              : err.message,
            undefined,
            isTransientConnectionCode(err.code)
          )
        );
      });
      if (payload) req.write(payload);
      req.end();
    });
  }

  /** Connect (auto-starting the daemon if configured), version-negotiating once. */
  async ensureReady(): Promise<void> {
    if (!this.ready) {
      this.ready = this.connectAndNegotiate().catch((err) => {
        // Don't cache a transient failure — let the next call retry.
        this.ready = undefined;
        throw err;
      });
    }
    return this.ready;
  }

  private async connectAndNegotiate(): Promise<void> {
    let hello: HelloResponse;
    try {
      hello = await this.hello();
    } catch (err) {
      if (err instanceof RelayfileControlPlaneError && err.code === 'VERSION_INCOMPATIBLE') {
        throw await this.versionRejectionError(err);
      }
      if (!isTransientControlPlaneError(err)) throw err;
      if (!this.autoStart) {
        throw new RelayfileControlPlaneError(
          'DAEMON_UNAVAILABLE',
          `relayfile control-plane is not running at ${this.socketPath}. ` +
            `Start it with \`relayfile control-plane serve\` (or unset RELAYFILE_REQUIRE_DAEMON to auto-start).`
        );
      }
      hello = await this.startDaemonAndConnect();
    }
    if (!hello.supportedApiVersions?.includes(RELAYFILE_API_VERSION)) {
      const installedVersion = await this.installedBinaryVersion();
      const daemonVersion = firstSemver(hello.daemonVersion);
      const canReplaceStaleDaemon =
        this.autoStart &&
        installedVersion != null &&
        daemonVersion !== '' &&
        compareSemver(installedVersion, MIN_RELAYFILE_VERSION_FOR_CURRENT_API) >= 0 &&
        compareSemver(installedVersion, daemonVersion) > 0;

      if (!canReplaceStaleDaemon) {
        throw this.versionMismatchError(hello, installedVersion);
      }

      try {
        await this.stopStaleDaemon();
        hello = await this.startDaemonAndConnect(installedVersion);
      } catch (err) {
        if (err instanceof RelayfileControlPlaneError && err.code === 'VERSION_INCOMPATIBLE') {
          throw err;
        }
        throw this.versionMismatchError(
          hello,
          installedVersion,
          `Automatic stale-daemon replacement failed: ${
            err instanceof Error ? err.message : String(err)
          }`
        );
      }
    }
    assertRelayfileVersion(hello.daemonVersion);
  }

  private spawnDaemon(): SpawnedDaemon {
    // A missing binary / bad RELAYFILE_BIN surfaces as an async 'error' event on
    // the child (ENOENT), not a throw from spawn().
    let spawnError: Error | undefined;
    let child;
    try {
      child = spawn(this.binary, ['control-plane', 'serve', '--sock', this.socketPath], {
        detached: true,
        stdio: 'ignore',
      });
      child.on('error', (err) => {
        spawnError = err;
      });
      child.once('exit', (code, signal) => {
        spawnError = new Error(
          `relayfile control-plane exited before readiness (${signal ? `signal ${signal}` : `code ${code}`})`
        );
      });
    } catch (err) {
      throw new RelayfileControlPlaneError(
        'DAEMON_UNAVAILABLE',
        `failed to start relayfile control-plane (${err instanceof Error ? err.message : String(err)}). ` +
        `Install relayfile or set RELAYFILE_BIN.`
      );
    }
    child.unref?.();
    return { getError: () => spawnError };
  }

  private async startDaemonAndConnect(installedVersion?: string): Promise<HelloResponse> {
    const spawned = this.spawnDaemon();
    const deadline = Date.now() + this.startTimeoutMs;
    let lastErr: unknown;
    while (Date.now() < deadline) {
      const spawnError = spawned.getError();
      if (spawnError) {
        throw new RelayfileControlPlaneError(
          'DAEMON_UNAVAILABLE',
          `failed to start relayfile control-plane (${spawnError.message}). ` +
            `Install relayfile or set RELAYFILE_BIN.`
        );
      }
      await sleep(100);
      let hello: HelloResponse;
      try {
        hello = await this.hello();
      } catch (err) {
        if (err instanceof RelayfileControlPlaneError && err.code === 'VERSION_INCOMPATIBLE') {
          throw await this.versionRejectionError(err, installedVersion);
        }
        if (!isTransientControlPlaneError(err)) throw err;
        lastErr = err;
        continue;
      }

      if (hello.supportedApiVersions?.includes(RELAYFILE_API_VERSION)) {
        return hello;
      }

      const mismatch = this.versionMismatchError(
        hello,
        installedVersion ?? (await this.installedBinaryVersion())
      );
      throw mismatch;
    }
    throw new RelayfileControlPlaneError(
      'DAEMON_UNAVAILABLE',
      `relayfile control-plane did not become ready within ${this.startTimeoutMs}ms ` +
        `(${lastErr instanceof Error ? lastErr.message : String(lastErr)}).`
    );
  }

  private async stopStaleDaemon(): Promise<void> {
    const result = await this.runCommand('lsof', ['-t', '--', this.socketPath], 1000);
    const pids = [
      ...new Set(
        (result?.code === 0 ? result.stdout : '')
          .split(/\s+/)
          .map((value) => Number.parseInt(value, 10))
          .filter((pid) => Number.isSafeInteger(pid) && pid > 1 && pid !== process.pid)
      ),
    ];
    if (pids.length !== 1) {
      throw new Error(
        pids.length === 0
          ? `could not identify the process serving ${this.socketPath}`
          : `refusing to stop multiple processes serving ${this.socketPath}: ${pids.join(', ')}`
      );
    }

    try {
      process.kill(pids[0]!, 'SIGTERM');
    } catch (err) {
      throw new Error(
        `could not stop stale relayfile control-plane pid ${pids[0]}: ${
          err instanceof Error ? err.message : String(err)
        }`
      );
    }

    const deadline = Date.now() + this.startTimeoutMs;
    while (existsSync(this.socketPath) && Date.now() < deadline) {
      await sleep(50);
    }
    if (existsSync(this.socketPath)) {
      throw new Error(
        `stale relayfile control-plane pid ${pids[0]} did not release ${this.socketPath} within ${this.startTimeoutMs}ms`
      );
    }
  }

  private versionMismatchError(
    hello: HelloResponse,
    installedVersion: string | undefined,
    detail?: string
  ): RelayfileControlPlaneError {
    const supported = hello.supportedApiVersions?.length
      ? hello.supportedApiVersions.map((version) => `v${version}`).join(', ')
      : 'none reported';
    return new RelayfileControlPlaneError(
      'VERSION_INCOMPATIBLE',
      `relayfile daemon ${hello.daemonVersion} speaks API v${hello.apiVersion} ` +
        `(supports ${supported}); this client requires API v${RELAYFILE_API_VERSION}. ` +
        `Installed relayfile binary: ${installedVersion ?? 'version unknown'}. ` +
        `${detail ? `${detail} ` : ''}` +
        `Upgrade with \`npm install -g relayfile@latest\`, then restart the running control-plane with ` +
        `\`${this.restartCommand()}\`.`
    );
  }

  private async versionRejectionError(
    err: RelayfileControlPlaneError,
    installedVersion?: string
  ): Promise<RelayfileControlPlaneError> {
    installedVersion ??= await this.installedBinaryVersion();
    const spoken = err.message.match(/speaks API v(\d+)/i)?.[1];
    return new RelayfileControlPlaneError(
      'VERSION_INCOMPATIBLE',
      `relayfile daemon version unknown (discovery hello was rejected) speaks API ` +
        `${spoken ? `v${spoken}` : 'version unknown'} (supported versions were not reported); ` +
        `this client requires API v${RELAYFILE_API_VERSION}. Installed relayfile binary: ` +
        `${installedVersion ?? 'version unknown'}. Upgrade with \`npm install -g relayfile@latest\`, ` +
        `then restart the running control-plane with \`${this.restartCommand()}\`. ` +
        `Daemon response: ${err.message}`,
      err.status
    );
  }

  private restartCommand(): string {
    const socket = shellQuote(this.socketPath);
    return (
      `kill $(lsof -t -- ${socket}) && ` +
      `${shellQuote(this.binary)} control-plane serve --sock ${socket}`
    );
  }

  private async installedBinaryVersion(): Promise<string | undefined> {
    const result = await this.runCommand(
      this.binary,
      ['--version'],
      Math.min(this.requestTimeoutMs, 2000)
    );
    return firstSemver(`${result?.stdout ?? ''} ${result?.stderr ?? ''}`) || undefined;
  }

  private runCommand(command: string, args: string[], timeoutMs: number): Promise<CommandResult | undefined> {
    return new Promise((resolve) => {
      let child;
      try {
        child = spawn(command, args, { stdio: ['ignore', 'pipe', 'pipe'] });
      } catch {
        resolve(undefined);
        return;
      }

      let stdout = '';
      let stderr = '';
      let settled = false;
      let timeout: NodeJS.Timeout | undefined;
      const finish = (result?: CommandResult) => {
        if (settled) return;
        settled = true;
        if (timeout) clearTimeout(timeout);
        resolve(result);
      };
      child.stdout?.on('data', (chunk) => {
        stdout += String(chunk);
      });
      child.stderr?.on('data', (chunk) => {
        stderr += String(chunk);
      });
      child.once('error', () => finish());
      child.once('close', (code) => finish({ stdout, stderr, code }));
      timeout = setTimeout(() => {
        child.kill();
        finish();
      }, timeoutMs);
      timeout.unref?.();
    });
  }

  // ── endpoints ──────────────────────────────────────────────────────────────

  hello(): Promise<HelloResponse> {
    return this.rawRequest<HelloResponse>({
      method: 'GET',
      path: '/v1/hello',
      includeVersionHeader: false,
      freshConnection: true,
    });
  }

  resolvePath(provider: string, resource: string): Promise<ResolvePathResult> {
    return this.request<ResolvePathResult>({
      method: 'POST',
      path: '/v1/integrations/resolve-path',
      body: { provider, resource },
    });
  }

  bind(input: BindRequestBody): Promise<BindResult> {
    return this.request<BindResult>({ method: 'POST', path: '/v1/integrations/bind', body: input });
  }

  async listBindings(): Promise<RelayfileBindingRecord[]> {
    const res = await this.request<{ bindings?: RelayfileBindingRecord[] }>({
      method: 'GET',
      path: '/v1/integrations/bindings',
    });
    return res.bindings ?? [];
  }

  unbind(provider: string, resource: string): Promise<unknown> {
    return this.request({ method: 'POST', path: '/v1/integrations/unbind', body: { provider, resource } });
  }

  /** Returns the connection status, or null when the provider is not connected. */
  async providerStatus(provider: string): Promise<ProviderStatusResult | null> {
    try {
      return await this.request<ProviderStatusResult>({
        method: 'GET',
        path: '/v1/integrations/provider-status',
        query: { provider },
      });
    } catch (err) {
      if (err instanceof RelayfileControlPlaneError && err.status === 404) return null;
      throw err;
    }
  }

  connect(input: ConnectRequestBody): Promise<ConnectResult> {
    return this.request<ConnectResult>({ method: 'POST', path: '/v1/integrations/connect', body: input });
  }

  writebackSecret(channel: string, workspace?: string): Promise<WritebackSecretResult> {
    return this.request<WritebackSecretResult>({
      method: 'POST',
      path: '/v1/integrations/writeback-secret',
      body: { channel, ...(workspace ? { workspace } : {}) },
    });
  }

  /**
   * Lists the workspace's inbound webhook subscriptions so callers can
   * reconcile subscriptions created by runs that crashed before persisting
   * the server-assigned id.
   */
  listWebhookSubscriptions(workspace?: string): Promise<ListWebhookSubscriptionsResult> {
    return this.request<ListWebhookSubscriptionsResult>({
      method: 'GET',
      path: '/v1/integrations/webhook-subscriptions',
      ...(workspace ? { query: { workspace } } : {}),
    });
  }

  createWebhookSubscription(input: WebhookSubscriptionRequestBody): Promise<WebhookSubscriptionResult> {
    return this.request<WebhookSubscriptionResult>({
      method: 'POST',
      path: '/v1/integrations/webhook-subscriptions',
      body: input,
    });
  }

  deleteWebhookSubscription(
    subscriptionId: DeleteWebhookSubscriptionRequestBody['subscriptionId'],
    workspace?: DeleteWebhookSubscriptionRequestBody['workspace']
  ): Promise<DeleteWebhookSubscriptionResult> {
    return this.request<DeleteWebhookSubscriptionResult>({
      method: 'DELETE',
      path: '/v1/integrations/webhook-subscriptions',
      body: { subscriptionId, ...(workspace ? { workspace } : {}) } satisfies DeleteWebhookSubscriptionRequestBody,
    });
  }

  /** Endpoint request that ensures the daemon is up + version-compatible first. */
  private async request<T>(opts: RequestOptions): Promise<T> {
    await this.ensureReady();
    return this.rawRequest<T>(opts);
  }
}
