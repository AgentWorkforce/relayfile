import type {
  ConnectCapableProvider,
  ConnectConnectionStatus,
  ConnectionProvider,
  CreateConnectSessionInput,
  ProviderConfigKeyMap
} from "./connection.js";
import { supportsConnect } from "./connection.js";

const DEFAULT_WAIT_INTERVAL_MS = 2_000;
const DEFAULT_WAIT_TIMEOUT_MS = 5 * 60_000;
const AUTH_READY_STATE = "oauth_connected";

export interface SelfHostConnectOptions {
  provider: ConnectionProvider;
  providerConfigKeys: ProviderConfigKeyMap;
  defaultPollIntervalMs?: number;
  defaultTimeoutMs?: number;
}

export interface StartSelfHostConnectOptions {
  endUserId: string;
  connectionId?: string;
  metadata?: Record<string, unknown>;
}

export interface SelfHostConnectResult {
  relayfileProvider: string;
  providerConfigKey: string;
  connectLink: string | null;
  sessionToken: string | null;
  expiresAt: string | null;
  connectionId: string;
}

export interface WaitForSelfHostConnectionOptions {
  connectionId: string;
  pollIntervalMs?: number;
  timeoutMs?: number;
  signal?: AbortSignal;
  onPoll?: (elapsedMs: number, status?: ConnectConnectionStatus) => void;
}

export class SelfHostConnect {
  private readonly provider: ConnectionProvider;
  private readonly providerConfigKeys: ProviderConfigKeyMap;
  private readonly defaultPollIntervalMs: number;
  private readonly defaultTimeoutMs: number;

  constructor(options: SelfHostConnectOptions) {
    this.provider = options.provider;
    this.providerConfigKeys = { ...options.providerConfigKeys };
    this.defaultPollIntervalMs = normalizePositiveInteger(
      options.defaultPollIntervalMs,
      DEFAULT_WAIT_INTERVAL_MS
    );
    this.defaultTimeoutMs = normalizePositiveInteger(
      options.defaultTimeoutMs,
      DEFAULT_WAIT_TIMEOUT_MS
    );
  }

  async startConnect(
    relayfileProvider: string,
    options: StartSelfHostConnectOptions
  ): Promise<SelfHostConnectResult> {
    const normalizedProvider = normalizeRelayfileProvider(relayfileProvider);
    const providerConfigKey = this.resolveProviderConfigKey(normalizedProvider);
    const connectProvider = this.requireConnectProvider();
    const endUserId = options.endUserId?.trim();
    if (!endUserId) {
      throw new Error("endUserId is required to start a self-host connect session");
    }

    const input: CreateConnectSessionInput = {
      relayfileProvider: normalizedProvider,
      providerConfigKey,
      endUserId
    };
    const connectionId = options.connectionId?.trim();
    if (connectionId) {
      input.connectionId = connectionId;
    }
    if (options.metadata) {
      input.metadata = { ...options.metadata };
    }

    const session = await connectProvider.createConnectSession(input);
    return {
      relayfileProvider: normalizedProvider,
      providerConfigKey,
      connectLink: session.connectLink,
      sessionToken: session.sessionToken,
      expiresAt: session.expiresAt,
      connectionId: session.connectionId
    };
  }

  async waitForConnection(
    relayfileProvider: string,
    options: WaitForSelfHostConnectionOptions
  ): Promise<ConnectConnectionStatus> {
    const normalizedProvider = normalizeRelayfileProvider(relayfileProvider);
    const providerConfigKey = this.resolveProviderConfigKey(normalizedProvider);
    const connectProvider = this.requireConnectProvider();
    const connectionId = options.connectionId?.trim();
    if (!connectionId) {
      throw new Error("connectionId is required to wait for a self-host connection");
    }

    const pollIntervalMs = normalizeNonNegativeInteger(
      options.pollIntervalMs,
      this.defaultPollIntervalMs
    );
    const timeoutMs = normalizePositiveInteger(options.timeoutMs, this.defaultTimeoutMs);
    const startedAt = Date.now();

    for (;;) {
      throwIfAborted(options.signal);
      const elapsedMs = Date.now() - startedAt;
      if (elapsedMs >= timeoutMs) {
        throw new Error(
          `Timed out waiting for ${normalizedProvider} connection "${connectionId}" after ${elapsedMs}ms.`
        );
      }

      const status = await connectProvider.getConnectionStatus({
        relayfileProvider: normalizedProvider,
        providerConfigKey,
        connectionId
      });
      options.onPoll?.(Date.now() - startedAt, status);
      if (isAuthReady(status)) {
        return status;
      }

      const sleepMs = Math.min(
        pollIntervalMs,
        Math.max(0, timeoutMs - (Date.now() - startedAt))
      );
      await sleep(sleepMs, options.signal);
    }
  }

  private resolveProviderConfigKey(relayfileProvider: string): string {
    const providerConfigKey = this.providerConfigKeys[relayfileProvider]?.trim();
    if (!providerConfigKey) {
      throw new Error(
        `No providerConfigKey mapping configured for relayfile provider "${relayfileProvider}".`
      );
    }
    return providerConfigKey;
  }

  private requireConnectProvider(): ConnectCapableProvider {
    if (!supportsConnect(this.provider)) {
      throw new Error(`Provider "${this.provider.name}" does not support self-host Connect.`);
    }
    return this.provider;
  }
}

function isAuthReady(status: ConnectConnectionStatus): boolean {
  return status.state === AUTH_READY_STATE || status.ready === true;
}

function normalizeRelayfileProvider(provider: string): string {
  const normalized = provider?.trim();
  if (!normalized) {
    throw new Error("relayfileProvider is required");
  }
  return normalized;
}

function normalizePositiveInteger(value: number | undefined, fallback: number): number {
  if (value === undefined) {
    return fallback;
  }
  return Math.max(1, Math.floor(value));
}

function normalizeNonNegativeInteger(value: number | undefined, fallback: number): number {
  if (value === undefined) {
    return fallback;
  }
  return Math.max(0, Math.floor(value));
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) {
    throw new Error("Aborted while waiting for self-host connection.");
  }
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  if (ms <= 0) {
    return Promise.resolve();
  }
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(resolve, ms);
    signal?.addEventListener(
      "abort",
      () => {
        clearTimeout(timeout);
        reject(new Error("Aborted while waiting for self-host connection."));
      },
      { once: true }
    );
  });
}
