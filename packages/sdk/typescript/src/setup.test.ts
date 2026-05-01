import { beforeEach, describe, expect, it, vi } from "vitest"
import { RelayFileClient } from "./client.js"
import {
  CloudAbortError,
  CloudApiError,
  CloudTimeoutError,
  IntegrationConnectionTimeoutError,
  MalformedCloudResponseError
} from "./setup-errors.js"
import { RelayfileSetup } from "./setup.js"
import { type WaitForConnectionOptions } from "./setup-types.js"

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
    const loginPromise = RelayfileSetup.login({
      cloudApiUrl: "https://cloud.test/base",
      state: "state_test",
      timeoutMs: 5_000,
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
  })

  it("prints the cloud login URL by default", async () => {
    const logSpy = vi.spyOn(console, "log").mockImplementation(() => {})
    const loginPromise = RelayfileSetup.login({
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
    await expect(loginPromise).resolves.toBeInstanceOf(RelayfileSetup)
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
})
