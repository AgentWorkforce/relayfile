import { describe, expect, it, vi } from "vitest";
import type {
  ConnectCapableProvider,
  ConnectConnectionStatus,
  ConnectionProvider
} from "./connection.js";
import { supportsConnect } from "./connection.js";
import { SelfHostConnect } from "./self-host-connect.js";

describe("supportsConnect", () => {
  it("guards connection providers with self-host Connect methods", () => {
    expect(supportsConnect(baseProvider())).toBe(false);
    expect(supportsConnect(connectProvider())).toBe(true);
  });
});

describe("SelfHostConnect", () => {
  it("starts provider-backed sessions with explicit providerConfigKey mapping", async () => {
    const provider = connectProvider();
    const connect = new SelfHostConnect({
      provider,
      providerConfigKeys: {
        github: "github-prod"
      }
    });

    const result = await connect.startConnect("github", {
      endUserId: "user_123",
      connectionId: "conn_hint",
      metadata: { workspaceId: "rw_123" }
    });

    expect(provider.createConnectSession).toHaveBeenCalledWith({
      relayfileProvider: "github",
      providerConfigKey: "github-prod",
      endUserId: "user_123",
      connectionId: "conn_hint",
      metadata: { workspaceId: "rw_123" }
    });
    expect(result).toEqual({
      relayfileProvider: "github",
      providerConfigKey: "github-prod",
      connectLink: "https://connect.test/github",
      sessionToken: "session_123",
      expiresAt: "2026-06-18T12:00:00Z",
      connectionId: "conn_123"
    });
  });

  it("fails before calling the provider when mapping is missing", async () => {
    const provider = connectProvider();
    const connect = new SelfHostConnect({
      provider,
      providerConfigKeys: {}
    });

    await expect(
      connect.startConnect("github", { endUserId: "user_123" })
    ).rejects.toThrow('No providerConfigKey mapping configured for relayfile provider "github".');
    expect(provider.createConnectSession).not.toHaveBeenCalled();
  });

  it("fails before calling connect methods when provider lacks Connect support", async () => {
    const provider = baseProvider();
    const connect = new SelfHostConnect({
      provider,
      providerConfigKeys: {
        github: "github-prod"
      }
    });

    await expect(
      connect.startConnect("github", { endUserId: "user_123" })
    ).rejects.toThrow('Provider "base" does not support self-host Connect.');
  });

  it("waits until the provider reports auth-ready oauth_connected", async () => {
    const statuses: ConnectConnectionStatus[] = [
      { connectionId: "conn_123", state: "pending" },
      { connectionId: "conn_123", state: "oauth_connected" }
    ];
    const provider = connectProvider({
      getConnectionStatus: vi.fn(async () => statuses.shift()!)
    });
    const connect = new SelfHostConnect({
      provider,
      providerConfigKeys: {
        github: "github-prod"
      }
    });

    const result = await connect.waitForConnection("github", {
      connectionId: "conn_123",
      pollIntervalMs: 0,
      timeoutMs: 1_000
    });

    expect(result.state).toBe("oauth_connected");
    expect(provider.getConnectionStatus).toHaveBeenCalledTimes(2);
    expect(provider.getConnectionStatus).toHaveBeenLastCalledWith({
      relayfileProvider: "github",
      providerConfigKey: "github-prod",
      connectionId: "conn_123"
    });
  });

  it("rejects immediately with AbortError when waitForConnection starts aborted", async () => {
    const provider = connectProvider();
    const connect = new SelfHostConnect({
      provider,
      providerConfigKeys: {
        github: "github-prod"
      }
    });
    const controller = new AbortController();
    controller.abort();

    await expect(
      connect.waitForConnection("github", {
        connectionId: "conn_123",
        signal: controller.signal
      })
    ).rejects.toMatchObject({ name: "AbortError" });
    expect(provider.getConnectionStatus).not.toHaveBeenCalled();
  });

  it("removes the abort listener when waitForConnection sleep is aborted", async () => {
    const provider = connectProvider({
      getConnectionStatus: vi.fn(async () => ({
        connectionId: "conn_123",
        state: "pending"
      }))
    });
    const connect = new SelfHostConnect({
      provider,
      providerConfigKeys: {
        github: "github-prod"
      }
    });
    const controller = new AbortController();
    const removeListener = vi.spyOn(controller.signal, "removeEventListener");

    const promise = connect.waitForConnection("github", {
      connectionId: "conn_123",
      pollIntervalMs: 10_000,
      timeoutMs: 60_000,
      signal: controller.signal
    });
    await Promise.resolve();
    await Promise.resolve();

    controller.abort();

    await expect(promise).rejects.toMatchObject({ name: "AbortError" });
    expect(removeListener).toHaveBeenCalledWith("abort", expect.any(Function));
  });
});

function baseProvider(): ConnectionProvider {
  return {
    name: "base",
    proxy: vi.fn(),
    healthCheck: vi.fn()
  };
}

function connectProvider(
  overrides: Partial<ConnectCapableProvider> = {}
): ConnectCapableProvider {
  return {
    name: "connect",
    proxy: vi.fn(),
    healthCheck: vi.fn(),
    createConnectSession: vi.fn(async () => ({
      connectLink: "https://connect.test/github",
      sessionToken: "session_123",
      expiresAt: "2026-06-18T12:00:00Z",
      connectionId: "conn_123"
    })),
    getConnectionStatus: vi.fn(async () => ({
      connectionId: "conn_123",
      state: "oauth_connected"
    })),
    ...overrides
  };
}
