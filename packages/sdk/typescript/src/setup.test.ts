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

function makeJoinResponse(token = "rf_jwt_1"): Response {
  return jsonResponse({
    workspaceId: "ws_123",
    token,
    relayfileUrl: "https://relayfile.test",
    wsUrl: "wss://relayfile.test/ws",
    relaycastApiKey: "rc_test"
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
    expect(readRequestHeaders(fetchMock, 0)["X-Relayfile-SDK-Version"]).toBe("0.5.3")
    expect(readRequestHeaders(fetchMock, 1)["X-Relayfile-SDK-Version"]).toBe("0.5.3")
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

  it("returns mount env and agent invites for relayfile plus relaycast", async () => {
    queueFetch(makeJoinResponse("rf_jwt_mount"))

    const setup = new RelayfileSetup({
      cloudApiUrl: "https://staging.agentrelay.com/cloud"
    })
    const handle = await setup.joinWorkspace("ws_123", {
      agentName: "lead-agent",
      scopes: ["fs:read", "fs:write", "relaycast:write"]
    })

    expect(handle.mountEnv({ localDir: "/workspace", remotePath: "/notion" })).toEqual({
      RELAYFILE_BASE_URL: "https://relayfile.test",
      RELAYFILE_TOKEN: "rf_jwt_mount",
      RELAYFILE_WORKSPACE: "ws_123",
      RELAYFILE_REMOTE_PATH: "/notion",
      RELAYFILE_LOCAL_DIR: "/workspace",
      RELAYCAST_API_KEY: "rc_test",
      RELAY_API_KEY: "rc_test",
      RELAYCAST_BASE_URL: "https://api.relaycast.dev",
      RELAY_BASE_URL: "https://api.relaycast.dev"
    })
    expect(handle.agentInvite({ agentName: "review-agent" })).toEqual({
      workspaceId: "ws_123",
      cloudApiUrl: "https://staging.agentrelay.com/cloud",
      relayfileUrl: "https://relayfile.test",
      relaycastApiKey: "rc_test",
      relaycastBaseUrl: "https://api.relaycast.dev",
      agentName: "review-agent",
      scopes: ["fs:read", "fs:write", "relaycast:write"],
      relayfileToken: "rf_jwt_mount"
    })
  })
})
