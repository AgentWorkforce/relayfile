import { mkdtemp, mkdir, readFile, rm, stat, writeFile } from "node:fs/promises"
import os from "node:os"
import path from "node:path"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { RelayFileClient } from "./client.js"
import {
  CloudAbortError,
  CloudApiError,
  CloudTimeoutError,
  InvalidLocalDirError,
  InvalidMountModeError,
  InvalidRemotePathError,
  IntegrationConnectionTimeoutError,
  MountSessionInputError,
  MalformedCloudResponseError
} from "./setup-errors.js"
import { RelayfileSetup } from "./setup.js"
import { RelayfileSetup as RelayfileCliSetup } from "./cli/setup.js"
import {
  type MountLauncher,
  type MountWorkspaceInput,
  type MountedWorkspaceStatus,
  type WaitForConnectionOptions
} from "./setup-types.js"

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: init.status ?? 200,
    headers: {
      "content-type": "application/json",
      ...(init.headers ?? {})
    }
  })
}

function makeJoinResponse(
  token = "rf_jwt_1",
  overrides: Record<string, unknown> = {}
): Response {
  return jsonResponse({
    workspaceId: "ws_123",
    token,
    relayfileUrl: "https://relayfile.test",
    wsUrl: "wss://relayfile.test/ws",
    relaycastApiKey: "rc_test",
    ...overrides
  })
}

function queueFetch(...responses: Array<Response | Promise<Response>>) {
  const fetchMock = vi.fn()
  for (const response of responses) {
    fetchMock.mockResolvedValueOnce(response)
  }
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

function readRequestUrl(fetchMock: ReturnType<typeof vi.fn>, index: number): string {
  return String(fetchMock.mock.calls[index]?.[0])
}

function readRequestBody(fetchMock: ReturnType<typeof vi.fn>, index: number): unknown {
  const init = fetchMock.mock.calls[index]?.[1] as RequestInit | undefined
  return init?.body ? JSON.parse(String(init.body)) : undefined
}

function readRequestHeaders(
  fetchMock: ReturnType<typeof vi.fn>,
  index: number
): Record<string, string> {
  const init = fetchMock.mock.calls[index]?.[1] as RequestInit | undefined
  return init?.headers as Record<string, string>
}

function makeMountSessionResponse(
  overrides: Record<string, unknown> = {}
): Response {
  return jsonResponse({
    workspaceId: "ws_123",
    relayfileBaseUrl: "https://relayfile.mount.test/",
    relayfileToken: "rf_mount_token",
    wsUrl: "wss://relayfile.mount.test/ws",
    remotePath: "/notion",
    localDir: "/ignored/by/sdk",
    mode: "poll",
    scopes: ["fs:read", "fs:write"],
    tokenIssuedAt: "2026-05-09T10:00:00.000Z",
    expiresAt: "2026-05-09T11:00:00.000Z",
    suggestedRefreshAt: "2026-05-09T10:55:00.000Z",
    relaycastApiKey: "rc_mount",
    relaycastBaseUrl: "https://relaycast.mount.test",
    ...overrides
  })
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((innerResolve, innerReject) => {
    resolve = innerResolve
    reject = innerReject
  })
  return { promise, resolve, reject }
}

function createLauncherStub(
  status: Partial<MountedWorkspaceStatus> = {}
): {
  launcher: MountLauncher
  instance: {
    pid: number
    ready: Promise<void>
    status: ReturnType<typeof vi.fn>
    stop: ReturnType<typeof vi.fn>
  }
  readyControl: ReturnType<typeof deferred<void>>
} {
  const readyControl = deferred<void>()
  const instance = {
    pid: 4321,
    ready: readyControl.promise,
    status: vi.fn().mockResolvedValue({
      ready: true,
      mode: "poll",
      expiresAt: null,
      suggestedRefreshAt: null,
      ...status
    } satisfies MountedWorkspaceStatus),
    stop: vi.fn().mockResolvedValue(undefined)
  }
  const launcher: MountLauncher = {
    start: vi.fn().mockResolvedValue(instance)
  }
  return { launcher, instance, readyControl }
}

async function flushPromises(times = 3): Promise<void> {
  for (let index = 0; index < times; index += 1) {
    await Promise.resolve()
  }
}

async function advanceTimers(options?: WaitForConnectionOptions): Promise<void> {
  const pollIntervalMs = options?.pollIntervalMs ?? options?.intervalMs ?? 0
  const timeoutMs = options?.timeoutMs ?? pollIntervalMs
  await vi.advanceTimersByTimeAsync(pollIntervalMs + timeoutMs + 5)
}

describe("RelayfileSetup", () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it("uses the exact default cloud URL and create/join preserve /cloud", async () => {
    const fetchMock = queueFetch(
      jsonResponse({
        workspaceId: "ws_123",
        relayfileUrl: "https://relayfile.test",
        relaycastApiKey: "rc_test",
        createdAt: "2026-04-30T00:00:00.000Z",
        name: "sdk-workspace"
      }),
      makeJoinResponse()
    )

    const setup = new RelayfileSetup({ retry: { maxRetries: 0, baseDelayMs: 1 } })
    const handle = await setup.createWorkspace({ name: "sdk-workspace" })

    expect(handle.workspaceId).toBe("ws_123")
    expect(readRequestUrl(fetchMock, 0)).toBe("https://agentrelay.com/cloud/api/v1/workspaces")
    expect(readRequestUrl(fetchMock, 1)).toBe("https://agentrelay.com/cloud/api/v1/workspaces/ws_123/join")
    expect(readRequestHeaders(fetchMock, 0)["X-Relayfile-SDK-Version"]).toBe("0.6.0")
    expect(readRequestHeaders(fetchMock, 1)["X-Relayfile-SDK-Version"]).toBe("0.6.0")
  })

  it("logs in through the cloud callback URL and returns an authenticated setup", async () => {
    const loginUrls: string[] = []
    const onTokens = vi.fn()
    const loginPromise = RelayfileCliSetup.login({
      cloudApiUrl: "https://cloud.test/base",
      state: "state_test",
      timeoutMs: 5_000,
      onTokens,
      onLoginUrl: (url) => {
        loginUrls.push(url)
      }
    })

    for (let index = 0; index < 50 && loginUrls.length === 0; index += 1) {
      await new Promise((resolve) => setTimeout(resolve, 10))
    }

    expect(loginUrls).toHaveLength(1)
    const loginUrl = new URL(loginUrls[0])
    expect(loginUrl.toString()).toContain("https://cloud.test/base/api/v1/cli/login")
    expect(loginUrl.searchParams.get("state")).toBe("state_test")

    const redirectUri = loginUrl.searchParams.get("redirect_uri")
    expect(redirectUri).toBeTruthy()
    const callbackUrl = new URL(redirectUri!)
    callbackUrl.searchParams.set("state", "state_test")
    callbackUrl.searchParams.set("access_token", "cld_at_login")
    callbackUrl.searchParams.set("refresh_token", "cld_rt_login")
    callbackUrl.searchParams.set(
      "access_token_expires_at",
      new Date(Date.now() + 60 * 60 * 1000).toISOString()
    )
    callbackUrl.searchParams.set("api_url", "https://cloud.test/base")

    const response = await fetch(callbackUrl)
    expect(response.status).toBe(200)

    const setup = await loginPromise
    expect(setup.getCloudApiUrl()).toBe("https://cloud.test/base")
    expect(onTokens).toHaveBeenCalledOnce()
    expect(onTokens).toHaveBeenCalledWith({
      apiUrl: "https://cloud.test/base",
      accessToken: "cld_at_login",
      refreshToken: "cld_rt_login",
      accessTokenExpiresAt: callbackUrl.searchParams.get("access_token_expires_at"),
      refreshTokenExpiresAt: undefined
    })
  })

  it("keeps interactive cloud login out of the default Worker-safe entry", async () => {
    await expect(RelayfileSetup.login()).rejects.toMatchObject({
      code: "node_only_sdk_feature"
    })
  })

  it("prints the cloud login URL by default", async () => {
    const logSpy = vi.spyOn(console, "log").mockImplementation(() => {})
    const loginPromise = RelayfileCliSetup.login({
      cloudApiUrl: "https://cloud.test/base",
      state: "state_console",
      timeoutMs: 5_000
    })

    for (let index = 0; index < 50 && logSpy.mock.calls.length === 0; index += 1) {
      await new Promise((resolve) => setTimeout(resolve, 10))
    }

    expect(logSpy).toHaveBeenCalledOnce()
    const logged = String(logSpy.mock.calls[0][0])
    const loginUrl = new URL(logged.replace("Sign in to Relayfile Cloud: ", ""))
    const callbackUrl = new URL(loginUrl.searchParams.get("redirect_uri")!)
    callbackUrl.searchParams.set("state", "state_console")
    callbackUrl.searchParams.set("access_token", "cld_at_console")
    callbackUrl.searchParams.set("refresh_token", "cld_rt_console")
    callbackUrl.searchParams.set(
      "access_token_expires_at",
      new Date(Date.now() + 60 * 60 * 1000).toISOString()
    )

    await fetch(callbackUrl)
    await expect(loginPromise).resolves.toBeInstanceOf(RelayfileCliSetup)
  })

  it("creates a setup from cloud tokens and refreshes before requests", async () => {
    const onTokens = vi.fn()
    const fetchMock = queueFetch(
      jsonResponse({
        accessToken: "cld_at_refreshed",
        refreshToken: "cld_rt_refreshed",
        accessTokenExpiresAt: new Date(Date.now() + 60 * 60 * 1000).toISOString(),
        refreshTokenExpiresAt: new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString()
      }),
      jsonResponse({
        workspaceId: "ws_123",
        relayfileUrl: "https://relayfile.test",
        relaycastApiKey: "rc_test",
        createdAt: "2026-04-30T00:00:00.000Z"
      }),
      makeJoinResponse()
    )

    const setup = RelayfileSetup.fromCloudTokens(
      {
        apiUrl: "https://cloud.test/base",
        accessToken: "cld_at_expiring",
        refreshToken: "cld_rt_original",
        accessTokenExpiresAt: new Date(Date.now() + 500).toISOString()
      },
      {
        refreshWindowMs: 1_000,
        onTokens
      }
    )

    await setup.createWorkspace()

    expect(readRequestUrl(fetchMock, 0)).toBe("https://cloud.test/base/api/v1/auth/token/refresh")
    expect(readRequestHeaders(fetchMock, 0)["X-Relayfile-SDK-Version"]).toBe("0.6.0")
    expect(readRequestBody(fetchMock, 0)).toEqual({
      refreshToken: "cld_rt_original"
    })
    expect(readRequestHeaders(fetchMock, 1).Authorization).toBe("Bearer cld_at_refreshed")
    expect(readRequestHeaders(fetchMock, 2).Authorization).toBe("Bearer cld_at_refreshed")
    expect(onTokens).toHaveBeenCalledWith(
      expect.objectContaining({
        accessToken: "cld_at_refreshed",
        refreshToken: "cld_rt_refreshed",
        apiUrl: "https://cloud.test/base"
      })
    )
  })

  it("preserves a path-bearing override URL for join and integration calls", async () => {
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({
        token: "session_token",
        expiresAt: "2026-04-30T01:00:00.000Z",
        connectLink: "https://nango.test/connect",
        connectionId: "conn_123"
      }),
      jsonResponse({ ready: true }),
      jsonResponse({})
    )

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://staging.agentrelay.com/base/cloud"
    })
    const handle = await setup.joinWorkspace("ws_123")
    await handle.connectIntegration("github")
    await handle.isConnected("github", "conn_123")
    await handle.disconnectIntegration("github", "conn_123")

    expect(readRequestUrl(fetchMock, 0)).toBe("https://staging.agentrelay.com/base/cloud/api/v1/workspaces/ws_123/join")
    expect(readRequestUrl(fetchMock, 1)).toBe("https://staging.agentrelay.com/base/cloud/api/v1/workspaces/ws_123/integrations/connect-session")
    expect(readRequestUrl(fetchMock, 2)).toBe("https://staging.agentrelay.com/base/cloud/api/v1/workspaces/ws_123/integrations/github/status?connectionId=conn_123")
    expect(readRequestUrl(fetchMock, 3)).toBe("https://staging.agentrelay.com/base/cloud/api/v1/workspaces/ws_123/integrations/github/status")
  })

  it("adopts an existing Nango connection via POST /integrations/{provider}/adopt", async () => {
    // Operator path: a Nango connection has already been minted out-of-band
    // (UI or third-party flow). The SDK's adopt method posts the
    // connectionId to Cloud's adopt route, threads the auth token, and
    // surfaces the replacedConnectionId so callers can tell whether they
    // migrated a stale row. The CLI surface (`relayfile integration adopt`)
    // sits on top of this method, so any regression in the request shape
    // or the response parsing would break the operator workflow.
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({
        ok: true,
        connectionId: "conn_adopt_new",
        replacedConnectionId: "conn_adopt_old"
      })
    )

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://staging.agentrelay.com/base/cloud"
    })
    const handle = await setup.joinWorkspace("ws_123")
    const result = await handle.adoptIntegration("github", "conn_adopt_new", {
      providerConfigKey: "github-relay"
    })

    expect(result).toEqual({
      connectionId: "conn_adopt_new",
      replacedConnectionId: "conn_adopt_old"
    })
    expect(readRequestUrl(fetchMock, 1)).toBe(
      "https://staging.agentrelay.com/base/cloud/api/v1/workspaces/ws_123/integrations/github/adopt"
    )
    expect(readRequestBody(fetchMock, 1)).toEqual({
      connectionId: "conn_adopt_new",
      providerConfigKey: "github-relay"
    })
    expect(readRequestHeaders(fetchMock, 1).Authorization).toBe("Bearer rf_jwt_1")
  })

  it("omits replacedConnectionId from adoptIntegration result when the row was a fresh insert", async () => {
    // Cloud only returns replacedConnectionId on the stale-row migration
    // path; for a fresh insert the field is absent. The SDK must not
    // fabricate it (e.g. by echoing the request connectionId), because
    // callers use the presence/absence of replacedConnectionId to decide
    // whether to warn the operator that an old binding was overwritten.
    queueFetch(
      makeJoinResponse(),
      jsonResponse({ ok: true, connectionId: "conn_fresh" })
    )

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://staging.agentrelay.com/base/cloud"
    })
    const handle = await setup.joinWorkspace("ws_123")
    const result = await handle.adoptIntegration("notion", "conn_fresh")

    expect(result).toEqual({ connectionId: "conn_fresh" })
    expect("replacedConnectionId" in result).toBe(false)
  })

  it("propagates Cloud 409 conflicts from adoptIntegration as CloudApiError", async () => {
    // Conflict semantics must surface to the CLI so it can render the
    // operator-facing message and exit non-zero. The body's `code` field
    // tells the CLI which mitigation to suggest (disconnect first vs.
    // re-target a different workspace).
    queueFetch(
      makeJoinResponse(),
      jsonResponse(
        {
          ok: false,
          code: "existing_connection_live_or_unknown",
          error: "Workspace ws_123 already has a live github connection",
          existingConnectionId: "conn_live"
        },
        { status: 409 }
      )
    )

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://staging.agentrelay.com/base/cloud",
      retry: { maxRetries: 0, baseDelayMs: 1 }
    })
    const handle = await setup.joinWorkspace("ws_123")
    await expect(
      handle.adoptIntegration("github", "conn_intruder")
    ).rejects.toMatchObject({
      httpStatus: 409,
      httpBody: expect.objectContaining({
        code: "existing_connection_live_or_unknown"
      })
    } satisfies Partial<CloudApiError>)
  })

  it("lists accessible resources via GET /integrations/{provider}/accessible-resources", async () => {
    // The post-OAuth picker flow for Jira/Confluence: the CLI calls this to
    // figure out which Atlassian sites the operator's OAuth grant covers
    // so it can render a numbered picker. The SDK has to round-trip the
    // shape exactly because the CLI's prompt parses each entry's id+url
    // and feeds them into setIntegrationMetadata.
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({
        ok: true,
        resources: [
          {
            id: "cloud-1",
            url: "https://foo.atlassian.net",
            name: "Foo",
            scopes: ["read:jira-work"]
          },
          { id: "cloud-2", url: "https://bar.atlassian.net" }
        ]
      })
    )

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://staging.agentrelay.com/base/cloud"
    })
    const handle = await setup.joinWorkspace("ws_123")
    const resources = await handle.listAccessibleResources("jira")

    expect(resources).toEqual([
      {
        id: "cloud-1",
        url: "https://foo.atlassian.net",
        name: "Foo",
        scopes: ["read:jira-work"]
      },
      { id: "cloud-2", url: "https://bar.atlassian.net" }
    ])
    expect(readRequestUrl(fetchMock, 1)).toBe(
      "https://staging.agentrelay.com/base/cloud/api/v1/workspaces/ws_123/integrations/jira/accessible-resources"
    )
    expect(readRequestHeaders(fetchMock, 1).Authorization).toBe("Bearer rf_jwt_1")
  })

  it("normalizes listAccessibleResources provider casing and rejects blank providers", async () => {
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({
        ok: true,
        resources: [{ id: "cloud-1", url: "https://foo.atlassian.net" }]
      })
    )

    const setup = new RelayfileSetup({ cloudApiUrl: "https://cloud.test" })
    const handle = await setup.joinWorkspace("ws_123")
    await expect(handle.listAccessibleResources("  JiRa  ")).resolves.toEqual([
      { id: "cloud-1", url: "https://foo.atlassian.net" }
    ])
    expect(readRequestUrl(fetchMock, 1)).toBe(
      "https://cloud.test/api/v1/workspaces/ws_123/integrations/jira/accessible-resources"
    )
    expect(readRequestHeaders(fetchMock, 1).Authorization).toBe("Bearer rf_jwt_1")

    await expect(handle.listAccessibleResources("")).rejects.toThrow(
      /provider is required/
    )
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("listAccessibleResources filters out entries missing id or url", async () => {
    // Defense in depth: if Cloud ever loosens its normalization, the SDK
    // still refuses to return junk entries that the CLI would crash on.
    queueFetch(
      makeJoinResponse(),
      jsonResponse({
        ok: true,
        resources: [
          { id: "cloud-1", url: "https://foo.atlassian.net" },
          { id: "cloud-2" },
          { url: "https://baz.atlassian.net" },
          null,
          "not-an-object"
        ]
      })
    )

    const setup = new RelayfileSetup({ cloudApiUrl: "https://cloud.test" })
    const handle = await setup.joinWorkspace("ws_123")
    const resources = await handle.listAccessibleResources("jira")

    expect(resources).toEqual([
      { id: "cloud-1", url: "https://foo.atlassian.net" }
    ])
  })

  it("listAccessibleResources surfaces Cloud's provider_has_no_accessible_resources as CloudApiError", async () => {
    // Cloud returns 400 with code=provider_has_no_accessible_resources for
    // non-Atlassian providers. The SDK must surface that as CloudApiError
    // so callers can branch on the code instead of silently treating it
    // like an empty list.
    queueFetch(
      makeJoinResponse(),
      jsonResponse(
        {
          ok: false,
          code: "provider_has_no_accessible_resources",
          error: "Provider \"github\" does not expose accessible resources"
        },
        { status: 400 }
      )
    )

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://cloud.test",
      retry: { maxRetries: 0, baseDelayMs: 1 }
    })
    const handle = await setup.joinWorkspace("ws_123")
    await expect(
      handle.listAccessibleResources("github")
    ).rejects.toMatchObject({
      httpStatus: 400,
      httpBody: expect.objectContaining({
        code: "provider_has_no_accessible_resources"
      })
    } satisfies Partial<CloudApiError>)
  })

  it("sets integration metadata via PUT /integrations/{provider}/metadata", async () => {
    // The second half of the post-OAuth picker: after listAccessibleResources
    // returns multiple sites and the operator picks one, the CLI calls
    // this with { cloudId, baseUrl }. Cloud forwards to nango.setMetadata
    // and echoes the value back; the SDK returns whatever Cloud echoed so
    // CLI confirmation messages reflect the canonical stored shape.
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({
        ok: true,
        metadata: {
          cloudId: "cloud-1",
          baseUrl: "https://foo.atlassian.net"
        }
      })
    )

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://staging.agentrelay.com/base/cloud"
    })
    const handle = await setup.joinWorkspace("ws_123")
    const updated = await handle.setIntegrationMetadata("jira", {
      cloudId: "cloud-1",
      baseUrl: "https://foo.atlassian.net"
    })

    expect(updated).toEqual({
      cloudId: "cloud-1",
      baseUrl: "https://foo.atlassian.net"
    })
    expect(readRequestUrl(fetchMock, 1)).toBe(
      "https://staging.agentrelay.com/base/cloud/api/v1/workspaces/ws_123/integrations/jira/metadata"
    )
    expect(readRequestBody(fetchMock, 1)).toEqual({
      metadata: {
        cloudId: "cloud-1",
        baseUrl: "https://foo.atlassian.net"
      }
    })
    const init = fetchMock.mock.calls[1]?.[1] as RequestInit
    expect(init.method).toBe("PUT")
  })

  it("setIntegrationMetadata rejects non-object metadata locally before hitting Cloud", async () => {
    // We catch this in the SDK so the operator-facing error message is
    // immediate ("metadata must be a plain object") rather than an
    // unhelpful Cloud 400 round-trip.
    const fetchMock = queueFetch(makeJoinResponse())
    const setup = new RelayfileSetup({ cloudApiUrl: "https://cloud.test" })
    const handle = await setup.joinWorkspace("ws_123")
    // @ts-expect-error - exercising runtime guard
    await expect(handle.setIntegrationMetadata("jira", "nope")).rejects.toThrow(
      /metadata must be a plain object/
    )
    expect(fetchMock).toHaveBeenCalledTimes(1)
    // @ts-expect-error - exercising runtime guard
    await expect(handle.setIntegrationMetadata("jira", null)).rejects.toThrow(
      /metadata must be a plain object/
    )
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await expect(
      // @ts-expect-error - exercising runtime guard
      handle.setIntegrationMetadata("jira", ["array"])
    ).rejects.toThrow(/metadata must be a plain object/)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await expect(
      // @ts-expect-error - exercising runtime guard
      handle.setIntegrationMetadata("jira", new Date())
    ).rejects.toThrow(/metadata must be a plain object/)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await expect(
      // @ts-expect-error - exercising runtime guard
      handle.setIntegrationMetadata("jira", new Map())
    ).rejects.toThrow(/metadata must be a plain object/)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("setIntegrationMetadata surfaces Cloud 400 invalid_metadata as CloudApiError", async () => {
    queueFetch(
      makeJoinResponse(),
      jsonResponse(
        {
          ok: false,
          code: "invalid_metadata",
          error: "metadata key \"_internal\" is reserved by the Nango backend"
        },
        { status: 400 }
      )
    )

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://cloud.test",
      retry: { maxRetries: 0, baseDelayMs: 1 }
    })
    const handle = await setup.joinWorkspace("ws_123")
    await expect(
      handle.setIntegrationMetadata("jira", { _internal: "nope" })
    ).rejects.toMatchObject({
      httpStatus: 400,
      httpBody: expect.objectContaining({ code: "invalid_metadata" })
    } satisfies Partial<CloudApiError>)
  })

  it("setIntegrationMetadata rejects malformed Cloud metadata responses", async () => {
    queueFetch(
      makeJoinResponse(),
      jsonResponse({
        ok: true,
        metadata: []
      })
    )

    const setup = new RelayfileSetup({ cloudApiUrl: "https://cloud.test" })
    const handle = await setup.joinWorkspace("ws_123")
    await expect(
      handle.setIntegrationMetadata("jira", { cloudId: "cloud-1" })
    ).rejects.toThrow(/expected metadata to be a plain object/)
  })

  it("throws CloudApiError with parsed body on createWorkspace failure", async () => {
    queueFetch(
      jsonResponse(
        { error: "boom", message: "cloud exploded" },
        { status: 500 }
      )
    )

    const setup = new RelayfileSetup({ retry: { maxRetries: 0, baseDelayMs: 1 } })
    await expect(setup.createWorkspace()).rejects.toMatchObject({
      httpStatus: 500,
      httpBody: { error: "boom", message: "cloud exploded" }
    } satisfies Partial<CloudApiError>)
  })

  it("throws MalformedCloudResponseError with the missing field name", async () => {
    queueFetch(
      jsonResponse({
        relayfileUrl: "https://relayfile.test",
        relaycastApiKey: "rc_test",
        createdAt: "2026-04-30T00:00:00.000Z"
      })
    )

    const setup = new RelayfileSetup()
    await expect(setup.createWorkspace()).rejects.toMatchObject({
      field: "workspaceId"
    } satisfies Partial<MalformedCloudResponseError>)
  })

  it("returns alreadyConnected without creating a connect session", async () => {
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({ ready: true })
    )

    const setup = new RelayfileSetup()
    const handle = await setup.joinWorkspace("ws_123")
    const result = await handle.connectIntegration("github", {
      connectionId: "conn_existing"
    })

    expect(result).toEqual({
      alreadyConnected: true,
      connectLink: null,
      sessionToken: null,
      expiresAt: null,
      connectionId: "conn_existing"
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("stores the workspaceId when connect-session omits connectionId", async () => {
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({
        token: "session_token",
        expiresAt: "2026-04-30T01:00:00.000Z",
        connectLink: "https://nango.test/connect"
      })
    )

    const setup = new RelayfileSetup()
    const handle = await setup.joinWorkspace("ws_123")
    const result = await handle.connectIntegration("github")

    expect(result.connectionId).toBe("ws_123")
    expect(readRequestBody(fetchMock, 1)).toEqual({
      allowedIntegrations: ["github"]
    })
  })

  it("waitForConnection supports polling callbacks and delayed success", async () => {
    vi.useFakeTimers()
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({ ready: false }),
      jsonResponse({ ready: false }),
      jsonResponse({ ready: true })
    )

    const setup = new RelayfileSetup()
    const handle = await setup.joinWorkspace("ws_123")
    const onPoll = vi.fn()

    const waitPromise = handle.waitForConnection("github", {
      connectionId: "conn_123",
      pollIntervalMs: 1000,
      onPoll
    })

    await vi.runAllTimersAsync()
    await waitPromise

    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(onPoll.mock.calls).toEqual([[0], [1000], [2000]])
  })

  it("waitForConnection honors Retry-After on 429 responses", async () => {
    vi.useFakeTimers()
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({ message: "slow down" }, {
        status: 429,
        headers: { "retry-after": "2" }
      }),
      jsonResponse({ ready: true })
    )

    const setup = new RelayfileSetup()
    const handle = await setup.joinWorkspace("ws_123")
    const waitPromise = handle.waitForConnection("github", {
      connectionId: "conn_123"
    })

    await vi.advanceTimersByTimeAsync(1_999)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(2)
    await waitPromise

    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it("waitForConnection times out and aborts correctly", async () => {
    vi.useFakeTimers()
    queueFetch(makeJoinResponse(), jsonResponse({ ready: false }))

    const setup = new RelayfileSetup({ retry: { maxRetries: 0, baseDelayMs: 1 } })
    const handle = await setup.joinWorkspace("ws_123")
    const timeoutPromise = handle.waitForConnection("github", {
      connectionId: "conn_123",
      pollIntervalMs: 1000,
      timeoutMs: 1000
    })
    const timeoutExpectation = expect(timeoutPromise).rejects.toBeInstanceOf(
      IntegrationConnectionTimeoutError
    )

    await advanceTimers({ pollIntervalMs: 1000, timeoutMs: 1000 })
    await timeoutExpectation

    const controller = new AbortController()
    queueFetch(makeJoinResponse(), jsonResponse({ ready: false }))
    const abortedHandle = await setup.joinWorkspace("ws_123")
    const abortPromise = abortedHandle.waitForConnection("github", {
      connectionId: "conn_123",
      pollIntervalMs: 1000,
      signal: controller.signal
    })
    controller.abort()

    await expect(abortPromise).rejects.toBeInstanceOf(CloudAbortError)
  })

  it("waitForConnection fails immediately on 401", async () => {
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({ error: "Unauthorized" }, { status: 401 })
    )

    const setup = new RelayfileSetup({ retry: { maxRetries: 3, baseDelayMs: 1 } })
    const handle = await setup.joinWorkspace("ws_123")

    await expect(
      handle.waitForConnection("github", { connectionId: "conn_123" })
    ).rejects.toMatchObject({
      httpStatus: 401
    } satisfies Partial<CloudApiError>)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("returns a singleton RelayFileClient and refreshes with the original join options", async () => {
    const fetchMock = queueFetch(
      makeJoinResponse("rf_jwt_original"),
      makeJoinResponse("rf_jwt_refreshed"),
      jsonResponse({ path: "/", entries: [], nextCursor: null })
    )

    const setup = new RelayfileSetup()
    const handle = await setup.joinWorkspace("ws_123", {
      agentName: "sdk-bot",
      scopes: ["fs:read"],
      permissions: { readonly: ["/github/**"] }
    })

    const client = handle.client()
    expect(client).toBeInstanceOf(RelayFileClient)
    expect(handle.client()).toBe(client)

    ;(handle as unknown as { _tokenIssuedAt: number })._tokenIssuedAt =
      Date.now() - 56 * 60 * 1000

    await client.listTree("ws_123")

    expect(readRequestBody(fetchMock, 0)).toEqual({
      agentName: "sdk-bot",
      scopes: ["fs:read"],
      permissions: { readonly: ["/github/**"] }
    })
    expect(readRequestBody(fetchMock, 1)).toEqual(readRequestBody(fetchMock, 0))

    const relayRequestInit = fetchMock.mock.calls[2]?.[1] as RequestInit
    expect((relayRequestInit.headers as Record<string, string>).Authorization).toBe(
      "Bearer rf_jwt_refreshed"
    )
  })

  it("offers a one-call Notion connect helper", async () => {
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({
        token: "session_token",
        expiresAt: "2026-04-30T01:00:00.000Z",
        connectLink: "https://nango.test/notion",
        connectionId: "conn_notion"
      })
    )

    const setup = new RelayfileSetup()
    const handle = await setup.joinWorkspace("ws_123")
    const result = await handle.connectNotion()

    expect(result.connectLink).toBe("https://nango.test/notion")
    expect(readRequestBody(fetchMock, 1)).toEqual({
      allowedIntegrations: ["notion"]
    })
  })

  it("forces connectNotion to request only the notion integration", async () => {
    const fetchMock = queueFetch(
      makeJoinResponse(),
      jsonResponse({
        token: "session_token",
        expiresAt: "2026-04-30T01:00:00.000Z",
        connectLink: "https://nango.test/notion",
        connectionId: "conn_notion"
      })
    )

    const setup = new RelayfileSetup()
    const handle = await setup.joinWorkspace("ws_123")

    await handle.connectNotion({
      allowedIntegrations: ["github", "linear"]
    } as never)

    expect(readRequestBody(fetchMock, 1)).toEqual({
      allowedIntegrations: ["notion"]
    })
  })

  it("delegates waitForNotion to waitForConnection('notion')", async () => {
    const setup = new RelayfileSetup()
    const fetchMock = queueFetch(makeJoinResponse())
    const handle = await setup.joinWorkspace("ws_123")
    const waitForConnectionSpy = vi
      .spyOn(handle, "waitForConnection")
      .mockResolvedValue(undefined)

    await handle.waitForNotion({ timeoutMs: 1234 })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(waitForConnectionSpy).toHaveBeenCalledWith("notion", {
      timeoutMs: 1234
    })
  })

  it("returns mount env using the cloud relaycast base URL and mount options", async () => {
    queueFetch(
      makeJoinResponse("rf_jwt_mount", {
        relaycastBaseUrl: "https://relaycast.staging.test"
      })
    )

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://staging.agentrelay.com/cloud"
    })
    const handle = await setup.joinWorkspace("ws_123", {
      agentName: "lead-agent",
      scopes: ["fs:read", "fs:write", "relaycast:write"]
    })

    expect(
      handle.mountEnv({
        localDir: "/workspace",
        remotePath: "/notion",
        mode: "poll"
      })
    ).toEqual({
      RELAYFILE_BASE_URL: "https://relayfile.test",
      RELAYFILE_TOKEN: "rf_jwt_mount",
      RELAYFILE_WORKSPACE: "ws_123",
      RELAYFILE_REMOTE_PATH: "/notion",
      RELAYFILE_LOCAL_DIR: "/workspace",
      RELAYFILE_MOUNT_MODE: "poll",
      RELAYCAST_API_KEY: "rc_test",
      RELAY_API_KEY: "rc_test",
      RELAYCAST_BASE_URL: "https://relaycast.staging.test",
      RELAY_BASE_URL: "https://relaycast.staging.test"
    })
  })

  it("lets mountEnv override the relaycast base URL explicitly", async () => {
    queueFetch(
      makeJoinResponse("rf_jwt_mount", {
        relaycastBaseUrl: "https://relaycast.staging.test"
      })
    )

    const setup = new RelayfileSetup()
    const handle = await setup.joinWorkspace("ws_123")

    expect(
      handle.mountEnv({
        relaycastBaseUrl: "https://relaycast.override.test"
      })
    ).toEqual({
      RELAYFILE_BASE_URL: "https://relayfile.test",
      RELAYFILE_TOKEN: "rf_jwt_mount",
      RELAYFILE_WORKSPACE: "ws_123",
      RELAYFILE_REMOTE_PATH: "/",
      RELAYCAST_API_KEY: "rc_test",
      RELAY_API_KEY: "rc_test",
      RELAYCAST_BASE_URL: "https://relaycast.override.test",
      RELAY_BASE_URL: "https://relaycast.override.test"
    })
  })

  it("returns agent invites for relayfile plus relaycast and omits relayfileToken on request", async () => {
    queueFetch(
      makeJoinResponse("rf_jwt_mount", {
        relaycastBaseUrl: "https://relaycast.staging.test"
      })
    )

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://staging.agentrelay.com/cloud"
    })
    const leadScopes = ["fs:read", "fs:write", "relaycast:write"]
    const handle = await setup.joinWorkspace("ws_123", {
      agentName: "lead-agent",
      scopes: leadScopes
    })

    expect(handle.agentInvite({ agentName: "review-agent" })).toEqual({
      workspaceId: "ws_123",
      cloudApiUrl: "https://staging.agentrelay.com/cloud",
      relayfileUrl: "https://relayfile.test",
      relaycastApiKey: "rc_test",
      relaycastBaseUrl: "https://relaycast.staging.test",
      agentName: "review-agent",
      scopes: ["fs:read", "fs:write", "relaycast:write"],
      relayfileToken: "rf_jwt_mount"
    })

    const tokenlessInvite = handle.agentInvite({
      includeRelayfileToken: false
    })
    expect(tokenlessInvite).toEqual({
      workspaceId: "ws_123",
      cloudApiUrl: "https://staging.agentrelay.com/cloud",
      relayfileUrl: "https://relayfile.test",
      relaycastApiKey: "rc_test",
      relaycastBaseUrl: "https://relaycast.staging.test",
      agentName: "lead-agent",
      scopes: ["fs:read", "fs:write", "relaycast:write"]
    })
    expect("relayfileToken" in tokenlessInvite).toBe(false)

    const inviteScopes = handle.agentInvite().scopes
    inviteScopes.push("fs:admin")
    expect(leadScopes).toEqual(["fs:read", "fs:write", "relaycast:write"])
    expect(handle.agentInvite().scopes).toEqual([
      "fs:read",
      "fs:write",
      "relaycast:write"
    ])
  })

  it("agentInviteScoped mints a fresh JWT scoped to the requested subset", async () => {
    const fetchMock = queueFetch(
      makeJoinResponse("rf_jwt_lead", {
        relaycastBaseUrl: "https://relaycast.staging.test"
      }),
      makeJoinResponse("rf_jwt_scoped", {
        relaycastBaseUrl: "https://relaycast.staging.test"
      })
    )

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://staging.agentrelay.com/cloud"
    })
    const handle = await setup.joinWorkspace("ws_123", {
      agentName: "lead-agent",
      scopes: ["fs:read", "fs:write", "relaycast:write"]
    })

    const invite = await handle.agentInviteScoped({
      agentName: "review-agent",
      scopes: ["relayfile:fs:read:/notion/pages/*"]
    })

    // Second fetch is the scoped join request.
    expect(readRequestUrl(fetchMock, 1)).toBe(
      "https://staging.agentrelay.com/cloud/api/v1/workspaces/ws_123/join"
    )
    expect(readRequestBody(fetchMock, 1)).toEqual({
      agentName: "review-agent",
      scopes: ["relayfile:fs:read:/notion/pages/*"]
    })
    // Authorization header carries the calling workspace JWT so the cloud
    // can verify requestedScopes ⊆ caller's grant. Without this the cloud
    // can't enforce subset semantics and a permissive endpoint would mint
    // a silently-wide token.
    expect(readRequestHeaders(fetchMock, 1).Authorization).toBe(
      "Bearer rf_jwt_lead"
    )
    expect(invite).toEqual({
      workspaceId: "ws_123",
      cloudApiUrl: "https://staging.agentrelay.com/cloud",
      relayfileUrl: "https://relayfile.test",
      relaycastApiKey: "rc_test",
      relaycastBaseUrl: "https://relaycast.staging.test",
      agentName: "review-agent",
      scopes: ["relayfile:fs:read:/notion/pages/*"],
      relayfileToken: "rf_jwt_scoped"
    })
  })

  it("agentInviteScoped picks up a fresher relaycastBaseUrl from the join response", async () => {
    queueFetch(
      makeJoinResponse("rf_jwt_lead", {
        relaycastBaseUrl: "https://relaycast.old.test"
      }),
      makeJoinResponse("rf_jwt_scoped", {
        relaycastBaseUrl: "https://relaycast.new.test"
      })
    )

    const setup = new RelayfileSetup()
    const handle = await setup.joinWorkspace("ws_123", {
      agentName: "lead-agent",
      scopes: ["fs:read", "fs:write"]
    })

    const invite = await handle.agentInviteScoped({
      agentName: "review-agent",
      scopes: ["fs:read"]
    })

    expect(invite.relaycastBaseUrl).toBe("https://relaycast.new.test")
  })

  it("agentInviteScoped surfaces cloud-side scope rejection", async () => {
    queueFetch(
      makeJoinResponse("rf_jwt_lead"),
      jsonResponse(
        { error: "insufficient_scope", code: "insufficient_scope" },
        { status: 403 }
      )
    )

    const setup = new RelayfileSetup()
    const handle = await setup.joinWorkspace("ws_123", {
      agentName: "lead-agent",
      scopes: ["fs:read"]
    })

    await expect(
      handle.agentInviteScoped({
        agentName: "review-agent",
        scopes: ["fs:admin"]
      })
    ).rejects.toThrow()
  })

  it("agentInviteScoped omits relayfileToken when includeRelayfileToken is false", async () => {
    queueFetch(
      makeJoinResponse("rf_jwt_lead"),
      makeJoinResponse("rf_jwt_scoped")
    )

    const setup = new RelayfileSetup()
    const handle = await setup.joinWorkspace("ws_123", {
      agentName: "lead-agent",
      scopes: ["fs:read", "fs:write"]
    })

    const invite = await handle.agentInviteScoped({
      agentName: "review-agent",
      scopes: ["fs:read"],
      includeRelayfileToken: false
    })

    expect("relayfileToken" in invite).toBe(false)
    expect(invite.scopes).toEqual(["fs:read"])
  })

  it("mountWorkspace posts the mount-session request, maps env, and returns a mounted handle", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-mount-workspace-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const fetchMock = queueFetch(
      makeJoinResponse("rf_jwt_joined"),
      makeMountSessionResponse()
    )
    const { launcher, instance, readyControl } = createLauncherStub()

    try {
      const setup = new RelayfileSetup()
      const workspace = await setup.joinWorkspace("ws_123")
      readyControl.resolve()
      const handle = await setup.mountWorkspace({
        workspace,
        localDir,
        remotePath: "/notion",
        mode: "poll",
        launcher
      })

      expect((launcher.start as ReturnType<typeof vi.fn>).mock.calls).toHaveLength(1)
      const launcherStart = (launcher.start as ReturnType<typeof vi.fn>).mock.calls[0][0]
      expect(launcherStart.env).toMatchObject({
        RELAYFILE_BASE_URL: "https://relayfile.mount.test",
        RELAYFILE_TOKEN: "rf_mount_token",
        RELAYFILE_WORKSPACE: "ws_123",
        RELAYFILE_REMOTE_PATH: "/notion",
        RELAYFILE_LOCAL_DIR: localDir,
        RELAYFILE_MOUNT_MODE: "poll",
        RELAYFILE_MOUNT_LOCAL_LAYOUT: "exact",
        RELAYFILE_MOUNT_SYNC_MODE: "mirror",
        RELAYCAST_API_KEY: "rc_mount",
        RELAY_API_KEY: "rc_mount",
        RELAYCAST_BASE_URL: "https://relaycast.mount.test",
        RELAY_BASE_URL: "https://relaycast.mount.test"
      })

      expect(readRequestUrl(fetchMock, 1)).toBe(
        "https://agentrelay.com/cloud/api/v1/workspaces/ws_123/relayfile/mount-session"
      )
      expect(readRequestBody(fetchMock, 1)).toEqual({
        localDir,
        remotePath: "/notion",
        mode: "poll",
        agentName: "relayfile-mount"
      })
      expect(readRequestHeaders(fetchMock, 1).Authorization).toBe(
        "Bearer rf_jwt_joined"
      )
      expect(handle.workspaceId).toBe("ws_123")
      expect(handle.localDir).toBe(localDir)
      expect(handle.remotePath).toBe("/notion")
      expect(handle.mode).toBe("poll")
      expect(handle.ready).toBe(true)
      expect(handle.expiresAt).toBe("2026-05-09T11:00:00.000Z")
      expect(handle.suggestedRefreshAt).toBe("2026-05-09T10:55:00.000Z")
      expect(handle.env()).toMatchObject({
        RELAYFILE_BASE_URL: "https://relayfile.mount.test",
        RELAYFILE_TOKEN: "rf_mount_token",
        RELAYFILE_WORKSPACE: "ws_123",
        RELAYFILE_REMOTE_PATH: "/notion",
        RELAYFILE_LOCAL_DIR: localDir,
        RELAYFILE_MOUNT_MODE: "poll",
        RELAYFILE_MOUNT_LOCAL_LAYOUT: "exact",
        RELAYFILE_MOUNT_SYNC_MODE: "mirror"
      })

      await handle.stop()
      expect(instance.stop).toHaveBeenCalledTimes(1)
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("passes explicit local layout and sync mode only to the local mount launcher", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-mount-workspace-contract-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const fetchMock = queueFetch(
      makeJoinResponse("rf_jwt_joined"),
      makeMountSessionResponse({
        remotePath: "/slack/channels/C123/messages"
      })
    )
    const { launcher, readyControl } = createLauncherStub()

    try {
      const setup = new RelayfileSetup()
      const workspace = await setup.joinWorkspace("ws_123")
      readyControl.resolve()
      await setup.mountWorkspace({
        workspace,
        localDir,
        remotePath: "/slack/channels/C123/messages",
        mode: "poll",
        localLayout: "scoped",
        syncMode: "write-only",
        launcher
      })

      const launcherStart = (launcher.start as ReturnType<typeof vi.fn>).mock.calls[0][0]
      expect(launcherStart.env).toMatchObject({
        RELAYFILE_REMOTE_PATH: "/slack/channels/C123/messages",
        RELAYFILE_LOCAL_DIR: localDir,
        RELAYFILE_MOUNT_LOCAL_LAYOUT: "scoped",
        RELAYFILE_MOUNT_SYNC_MODE: "write-only"
      })
      expect(readRequestBody(fetchMock, 1)).toEqual({
        localDir,
        remotePath: "/slack/channels/C123/messages",
        mode: "poll",
        agentName: "relayfile-mount"
      })
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("mountWorkspace joins by workspaceId before mounting", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-mount-by-id-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const fetchMock = queueFetch(
      makeJoinResponse("rf_jwt_from_join"),
      makeMountSessionResponse()
    )
    const { launcher, readyControl } = createLauncherStub()

    try {
      const setup = new RelayfileSetup()
      const handlePromise = setup.mountWorkspace({
        workspaceId: "ws_123",
        localDir,
        remotePath: "/notion",
        launcher
      })

      readyControl.resolve()
      await handlePromise

      expect(readRequestUrl(fetchMock, 0)).toBe(
        "https://agentrelay.com/cloud/api/v1/workspaces/ws_123/join"
      )
      expect(readRequestUrl(fetchMock, 1)).toBe(
        "https://agentrelay.com/cloud/api/v1/workspaces/ws_123/relayfile/mount-session"
      )
      expect(readRequestHeaders(fetchMock, 1).Authorization).toBe(
        "Bearer rf_jwt_from_join"
      )
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("mountWorkspace rejects MountSessionInputError when both workspace and workspaceId are given", async () => {
    const setup = new RelayfileSetup()

    await expect(
      setup.mountWorkspace({
        workspace: {} as never,
        workspaceId: "ws_123",
        localDir: "/tmp/relayfile-mount"
      } as MountWorkspaceInput)
    ).rejects.toBeInstanceOf(MountSessionInputError)
  })

  it("mountWorkspace rejects MountSessionInputError when neither workspace nor workspaceId is given", async () => {
    const setup = new RelayfileSetup()

    await expect(
      setup.mountWorkspace({
        localDir: "/tmp/relayfile-mount"
      } as MountWorkspaceInput)
    ).rejects.toBeInstanceOf(MountSessionInputError)
  })

  it("mountWorkspace rejects unsupported stream mode before any HTTP request", async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)
    const setup = new RelayfileSetup()

    await expect(
      setup.mountWorkspace({
        workspaceId: "ws_123",
        localDir: "/tmp/relayfile-mount",
        mode: "stream" as unknown as "poll"
      })
    ).rejects.toBeInstanceOf(InvalidMountModeError)

    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("mountWorkspace maps Cloud mount-session validation errors from httpBody.error", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-mount-validation-")
    )
    const localDir = path.join(tempRoot, "mirror")

    try {
      const cases = [
        {
          body: { error: "invalid_mode" },
          expected: InvalidMountModeError,
          match: { code: "invalid_mount_mode", mode: "poll" }
        },
        {
          body: { error: "invalid_local_dir" },
          expected: InvalidLocalDirError,
          match: { code: "invalid_local_dir", localDir }
        },
        {
          body: { error: "invalid_remote_path" },
          expected: InvalidRemotePathError,
          match: { code: "invalid_remote_path", remotePath: "/notion" }
        }
      ] as const

      for (const testCase of cases) {
        queueFetch(
          makeJoinResponse("rf_jwt_joined"),
          jsonResponse(testCase.body, { status: 400 })
        )
        const setup = new RelayfileSetup()
        const mountPromise = setup.mountWorkspace({
          workspaceId: "ws_123",
          localDir,
          remotePath: "/notion"
        })

        await expect(mountPromise).rejects.toBeInstanceOf(testCase.expected)

        await expect(mountPromise).rejects.toMatchObject(testCase.match)
      }
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("mountWorkspace stops the launcher when aborted after the mount session response", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-mount-abort-")
    )
    const localDir = path.join(tempRoot, "mirror")
    queueFetch(makeJoinResponse("rf_jwt_joined"), makeMountSessionResponse())
    const { instance } = createLauncherStub()
    const controller = new AbortController()
    const launcher: MountLauncher = {
      start: vi.fn().mockImplementation(async () => {
        controller.abort()
        return instance
      })
    }

    try {
      const setup = new RelayfileSetup()
      const mountPromise = setup.mountWorkspace({
        workspaceId: "ws_123",
        localDir,
        launcher,
        signal: controller.signal
      })

      await expect(mountPromise).rejects.toBeInstanceOf(CloudAbortError)
      expect((launcher.start as ReturnType<typeof vi.fn>).mock.calls).toHaveLength(1)
      expect(instance.stop).toHaveBeenCalledTimes(1)
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("ensureMountedWorkspace throws MountSessionInputError when verifyProvider=true and no provider is given", async () => {
    const setup = new RelayfileSetup()

    await expect(
      setup.ensureMountedWorkspace({
        workspace: {} as never,
        localDir: "/tmp/relayfile-mount",
        launcher: createLauncherStub().launcher
      })
    ).rejects.toBeInstanceOf(MountSessionInputError)
  })

  it("ensureMountedWorkspace throws ProviderNotConnectedError before posting mount-session", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-provider-disconnected-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const fetchMock = queueFetch(
      makeJoinResponse("rf_jwt_joined"),
      jsonResponse({ ready: false, state: "not_connected" })
    )
    const { launcher } = createLauncherStub()

    try {
      const setup = new RelayfileSetup()
      await expect(
        setup.ensureMountedWorkspace({
          workspaceId: "ws_123",
          provider: "notion",
          localDir,
          launcher
        })
      ).rejects.toMatchObject({
        code: "provider_not_connected",
        provider: "notion"
      })

      expect(fetchMock).toHaveBeenCalledTimes(2)
      expect(readRequestUrl(fetchMock, 1)).toContain(
        "/api/v1/workspaces/ws_123/integrations/notion/status?connectionId=ws_123"
      )
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("ensureMountedWorkspace throws ProviderNotReadyError when verification is required", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-provider-not-ready-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const fetchMock = queueFetch(
      makeJoinResponse("rf_jwt_joined"),
      jsonResponse({
        ready: false,
        state: "syncing",
        initialSync: { state: "queued" }
      })
    )
    const { launcher } = createLauncherStub()

    try {
      const setup = new RelayfileSetup()
      await expect(
        setup.ensureMountedWorkspace({
          workspaceId: "ws_123",
          provider: "notion",
          localDir,
          launcher,
          providerReadyTimeoutMs: 0
        })
      ).rejects.toMatchObject({
        code: "provider_not_ready",
        provider: "notion",
        state: "syncing",
        initialSyncState: "queued"
      })

      expect(fetchMock).toHaveBeenCalledTimes(2)
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("ensureMountedWorkspace skips provider verification when verifyProvider is false", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-provider-skip-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const fetchMock = queueFetch(
      makeJoinResponse("rf_jwt_joined"),
      makeMountSessionResponse()
    )
    const { launcher, readyControl } = createLauncherStub()

    try {
      const setup = new RelayfileSetup()
      const handlePromise = setup.ensureMountedWorkspace({
        workspaceId: "ws_123",
        localDir,
        verifyProvider: false,
        launcher
      })

      readyControl.resolve()
      await handlePromise

      expect(fetchMock).toHaveBeenCalledTimes(2)
      expect(readRequestUrl(fetchMock, 1)).toContain("/relayfile/mount-session")
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("ensureMountedWorkspace renews a background mount at the suggested refresh time", async () => {
    vi.useFakeTimers()
    vi.setSystemTime("2026-05-09T10:00:00.000Z")
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-supervised-refresh-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const fetchMock = queueFetch(
      makeJoinResponse("rf_jwt_joined"),
      makeMountSessionResponse({
        relayfileToken: "rf_mount_token_1",
        expiresAt: "2026-05-09T10:01:05.000Z",
        suggestedRefreshAt: "2026-05-09T10:00:01.000Z"
      }),
      makeMountSessionResponse({
        relayfileToken: "rf_mount_token_2",
        expiresAt: "2026-05-09T11:00:00.000Z",
        suggestedRefreshAt: "2026-05-09T10:55:00.000Z"
      })
    )
    const firstStop = vi.fn().mockResolvedValue(undefined)
    const secondStop = vi.fn().mockResolvedValue(undefined)
    const instances = [firstStop, secondStop].map((stop) => ({
      pid: 4321,
      ready: Promise.resolve(),
      status: vi.fn().mockResolvedValue({
        ready: true,
        mode: "poll",
        expiresAt: null,
        suggestedRefreshAt: null
      } satisfies MountedWorkspaceStatus),
      stop
    }))
    const launcher: MountLauncher = {
      start: vi.fn()
        .mockResolvedValueOnce(instances[0])
        .mockResolvedValueOnce(instances[1])
    }
    const events: Array<{ type: string; attempt: number }> = []

    try {
      const setup = new RelayfileSetup({ retry: { maxRetries: 0 } })
      const handle = await setup.ensureMountedWorkspace({
        workspaceId: "ws_123",
        localDir,
        verifyProvider: false,
        launcher,
        healthCheckIntervalMs: 30_000,
        onSupervisorEvent: (event) => { events.push(event) }
      })

      expect(handle.env().RELAYFILE_TOKEN).toBe("rf_mount_token_1")
      await vi.advanceTimersByTimeAsync(1_000)

      expect(fetchMock).toHaveBeenCalledTimes(3)
      expect(firstStop).toHaveBeenCalledTimes(1)
      expect(handle.env().RELAYFILE_TOKEN).toBe("rf_mount_token_2")
      expect(handle.expiresAt).toBe("2026-05-09T11:00:00.000Z")
      expect(events).toEqual([expect.objectContaining({
        type: "mount.refreshed",
        workspaceId: "ws_123",
        localDir,
        attempt: 0
      })])

      await handle.stop()
      expect(secondStop).toHaveBeenCalledTimes(1)
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("ensureMountedWorkspace restarts a background mount that becomes unhealthy", async () => {
    vi.useFakeTimers()
    vi.setSystemTime("2026-05-09T10:00:00.000Z")
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-supervised-health-")
    )
    const localDir = path.join(tempRoot, "mirror")
    queueFetch(
      makeJoinResponse("rf_jwt_joined"),
      makeMountSessionResponse({
        suggestedRefreshAt: "2026-05-09T11:00:00.000Z"
      }),
      makeMountSessionResponse({
        relayfileToken: "rf_mount_restarted",
        suggestedRefreshAt: "2026-05-09T11:00:00.000Z"
      })
    )
    const firstStop = vi.fn().mockResolvedValue(undefined)
    const secondStop = vi.fn().mockResolvedValue(undefined)
    const launcher: MountLauncher = {
      start: vi.fn()
        .mockResolvedValueOnce({
          pid: 4321,
          ready: Promise.resolve(),
          status: vi.fn().mockResolvedValue({
            ready: false,
            mode: "poll",
            expiresAt: null,
            suggestedRefreshAt: null
          } satisfies MountedWorkspaceStatus),
          stop: firstStop
        })
        .mockResolvedValueOnce({
          pid: 4322,
          ready: Promise.resolve(),
          status: vi.fn().mockResolvedValue({
            ready: true,
            mode: "poll",
            expiresAt: null,
            suggestedRefreshAt: null
          } satisfies MountedWorkspaceStatus),
          stop: secondStop
        })
    }

    try {
      const setup = new RelayfileSetup({ retry: { maxRetries: 0 } })
      const handle = await setup.ensureMountedWorkspace({
        workspaceId: "ws_123",
        localDir,
        verifyProvider: false,
        launcher,
        healthCheckIntervalMs: 1_000
      })

      await vi.advanceTimersByTimeAsync(1_000)

      expect(launcher.start).toHaveBeenCalledTimes(2)
      expect(firstStop).toHaveBeenCalledTimes(1)
      expect(handle.ready).toBe(true)
      await handle.stop()
      expect(secondStop).toHaveBeenCalledTimes(1)
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("ensureMountedWorkspace does not start a mount for an already aborted signal", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-supervised-preaborted-")
    )
    const localDir = path.join(tempRoot, "mirror")
    queueFetch(
      makeJoinResponse("rf_jwt_joined"),
      makeMountSessionResponse({ suggestedRefreshAt: "2026-05-09T11:00:00.000Z" })
    )
    const stop = vi.fn().mockResolvedValue(undefined)
    const launcher: MountLauncher = {
      start: vi.fn().mockResolvedValue({
        pid: 4321,
        ready: Promise.resolve(),
        status: vi.fn().mockResolvedValue({
          ready: true,
          mode: "poll",
          expiresAt: null,
          suggestedRefreshAt: null
        } satisfies MountedWorkspaceStatus),
        stop
      })
    }
    const controller = new AbortController()
    controller.abort()

    try {
      const setup = new RelayfileSetup({ retry: { maxRetries: 0 } })
      await expect(setup.ensureMountedWorkspace({
          workspaceId: "ws_123",
          localDir,
          verifyProvider: false,
          launcher,
          signal: controller.signal
        }))
        .rejects.toThrow("Aborted while waiting for mountWorkspace")
      expect(launcher.start).not.toHaveBeenCalled()
      expect(stop).not.toHaveBeenCalled()
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("does not launch a replacement after stop begins during refresh", async () => {
    vi.useFakeTimers()
    vi.setSystemTime("2026-05-09T10:00:00.000Z")
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-supervised-stop-refresh-")
    )
    const localDir = path.join(tempRoot, "mirror")
    queueFetch(
      makeJoinResponse("rf_jwt_joined"),
      makeMountSessionResponse({
        suggestedRefreshAt: "2026-05-09T10:00:01.000Z"
      })
    )
    let releaseStop!: () => void
    const stopBlocked = new Promise<void>((resolve) => { releaseStop = resolve })
    const firstStop = vi.fn().mockReturnValue(stopBlocked)
    const launcher: MountLauncher = {
      start: vi.fn().mockResolvedValue({
        pid: 4321,
        ready: Promise.resolve(),
        status: vi.fn().mockResolvedValue({
          ready: true,
          mode: "poll",
          expiresAt: null,
          suggestedRefreshAt: null
        } satisfies MountedWorkspaceStatus),
        stop: firstStop
      })
    }

    try {
      const setup = new RelayfileSetup({ retry: { maxRetries: 0 } })
      const handle = await setup.ensureMountedWorkspace({
        workspaceId: "ws_123",
        localDir,
        verifyProvider: false,
        launcher,
        healthCheckIntervalMs: 30_000
      })

      await vi.advanceTimersByTimeAsync(1_000)
      expect(firstStop).toHaveBeenCalledTimes(1)
      const stopping = handle.stop()
      releaseStop()
      await stopping

      expect(launcher.start).toHaveBeenCalledTimes(1)
      expect(handle.ready).toBe(false)
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("mounted handle status reads .relay/state.json and keeps expiresAt fields stable", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-status-state-file-")
    )
    const localDir = path.join(tempRoot, "mirror")
    queueFetch(makeJoinResponse("rf_jwt_joined"), makeMountSessionResponse())
    const { launcher, readyControl } = createLauncherStub()

    try {
      const now = Date.now()
      const lastReconcileAt = new Date(now - 1_000).toISOString()
      const lastEventAt = new Date(now - 500).toISOString()
      await mkdir(path.join(localDir, ".relay"), { recursive: true })
      await writeFile(
        path.join(localDir, ".relay", "state.json"),
        JSON.stringify({
          mode: "poll",
          intervalMs: 30_000,
          lastReconcileAt,
          lastEventAt,
          pendingWriteback: 2,
          pendingConflicts: 1,
          daemon: { pid: 9898 },
          providers: [{ status: "ready" }]
        }),
        "utf8"
      )

      const setup = new RelayfileCliSetup()
      const handlePromise = setup.mountWorkspace({
        workspaceId: "ws_123",
        localDir,
        launcher,
        background: false
      })

      readyControl.resolve()
      const handle = await handlePromise
      const status = await handle.status()

      expect(status).toMatchObject({
        ready: true,
        mode: "poll",
        pid: 9898,
        lastReconcileAt,
        lastEventAt,
        pendingWriteback: 2,
        pendingConflicts: 1,
        expiresAt: "2026-05-09T11:00:00.000Z",
        suggestedRefreshAt: "2026-05-09T10:55:00.000Z"
      })
      expect(handle.ready).toBe(true)
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("mounted handle status falls back to an HTTP probe when state.json is absent", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-status-probe-")
    )
    const localDir = path.join(tempRoot, "mirror")
    const fetchMock = queueFetch(
      makeJoinResponse("rf_jwt_joined"),
      makeMountSessionResponse(),
      jsonResponse({ entries: [] })
    )
    const { launcher, readyControl } = createLauncherStub()

    try {
      const setup = new RelayfileCliSetup()
      const handlePromise = setup.mountWorkspace({
        workspaceId: "ws_123",
        localDir,
        launcher,
        background: false
      })

      readyControl.resolve()
      const handle = await handlePromise
      const status = await handle.status()

      expect(status.ready).toBe(true)
      expect(readRequestUrl(fetchMock, 2)).toBe(
        "https://relayfile.mount.test/v1/workspaces/ws_123/fs/tree?path=%2Fnotion&depth=1"
      )
      expect(readRequestHeaders(fetchMock, 2).Authorization).toBe(
        "Bearer rf_mount_token"
      )
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })

  it("mounted handle stop is idempotent and never removes the local mirror", async () => {
    const tempRoot = await mkdtemp(
      path.join(os.tmpdir(), "relayfile-sdk-stop-idempotent-")
    )
    const localDir = path.join(tempRoot, "mirror")
    queueFetch(makeJoinResponse("rf_jwt_joined"), makeMountSessionResponse())
    const { launcher, instance, readyControl } = createLauncherStub()

    try {
      const setup = new RelayfileSetup()
      const handlePromise = setup.mountWorkspace({
        workspaceId: "ws_123",
        localDir,
        launcher
      })

      readyControl.resolve()
      const handle = await handlePromise
      await mkdir(localDir, { recursive: true })

      await handle.stop()
      await handle.stop()

      expect(instance.stop).toHaveBeenCalledTimes(1)
      expect((await stat(localDir)).isDirectory()).toBe(true)
    } finally {
      await rm(tempRoot, { recursive: true, force: true })
    }
  })
})
