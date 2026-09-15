import { afterEach, describe, expect, it, vi } from "vitest"
import {
  createRelayauthPathTokenAccessTokenProvider,
  isRelayauthRefreshToken
} from "./relayauth-token-provider.js"
import {
  CloudApiError,
  CloudTimeoutError,
  RelayfileSetupError
} from "./setup-errors.js"
import { RelayFileClient } from "./client.js"
import { resolveCloudTokensAccessToken } from "./setup.js"

// Build a compact JWT (header.payload.signature) with the given claims and an
// optional relay_* prefix, matching how relay_pa tokens are wrapped.
function jwt(claims: Record<string, unknown>, prefix = ""): string {
  const b64 = (o: unknown) =>
    Buffer.from(JSON.stringify(o)).toString("base64url")
  return `${prefix}${b64({ alg: "none" })}.${b64(claims)}.sig`
}

const RELAYAUTH_ISS = "https://relayauth.dev"

function relayPaRefresh(expSecondsFromNow = 90 * 24 * 3600): string {
  return jwt(
    {
      iss: RELAYAUTH_ISS,
      aud: ["relayauth"],
      scopes: ["relayauth:token:refresh"],
      token_type: "refresh",
      exp: Math.floor(Date.now() / 1000) + expSecondsFromNow
    },
    "relay_pa_"
  )
}

function accessToken(expSecondsFromNow: number): string {
  return jwt(
    {
      iss: RELAYAUTH_ISS,
      aud: ["relayfile"],
      scopes: ["relayfile:fs:read:/eng548/*", "relayfile:fs:write:/eng548/*"],
      exp: Math.floor(Date.now() / 1000) + expSecondsFromNow
    },
    "relay_pa_"
  )
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe("isRelayauthRefreshToken", () => {
  it("recognizes a relay_pa refresh token by aud and by scope", () => {
    expect(isRelayauthRefreshToken(relayPaRefresh())).toBe(true)
  })

  it("rejects a cloud device-auth refresh token", () => {
    const cloud = jwt({ iss: "https://agentrelay.com", aud: ["cloud"] }, "cld_rt_")
    expect(isRelayauthRefreshToken(cloud)).toBe(false)
  })
})

describe("createRelayauthPathTokenAccessTokenProvider", () => {
  it("refreshes at the RELAYAUTH api host derived from iss, not the cloud endpoint", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(
          JSON.stringify({
            accessToken: accessToken(3600),
            refreshToken: relayPaRefresh(),
            accessTokenExpiresAt: new Date(Date.now() + 3600_000).toISOString(),
            refreshTokenExpiresAt: new Date(Date.now() + 90 * 86400_000).toISOString()
          }),
          { status: 200, headers: { "content-type": "application/json" } }
        )
      )

    const provider = createRelayauthPathTokenAccessTokenProvider({
      // access token already expired -> forces a refresh on first call
      accessToken: accessToken(-10),
      refreshToken: relayPaRefresh()
    })

    const token = await provider()
    expect(typeof token).toBe("string")

    const calledUrl = String(fetchMock.mock.calls[0]?.[0])
    // The whole point of the fix: RELAYAUTH endpoint on the API host, NOT cloud.
    expect(calledUrl).toBe("https://api.relayauth.dev/v1/tokens/refresh")
    expect(calledUrl).not.toContain("api/v1/auth/token/refresh")
  })

  it("does not refresh while the access token is still valid", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch")
    const provider = createRelayauthPathTokenAccessTokenProvider({
      accessToken: accessToken(3600),
      refreshToken: relayPaRefresh()
    })
    await provider()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("rotates the access token and surfaces the rotated one", async () => {
    const rotated = accessToken(3600)
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          accessToken: rotated,
          refreshToken: relayPaRefresh(),
          accessTokenExpiresAt: new Date(Date.now() + 3600_000).toISOString()
        }),
        { status: 200, headers: { "content-type": "application/json" } }
      )
    )
    const provider = createRelayauthPathTokenAccessTokenProvider({
      accessToken: accessToken(-10),
      refreshToken: relayPaRefresh()
    })
    expect(await provider()).toBe(rotated)
  })

  it("propagates a non-2xx refresh as CloudApiError (e.g. invalid_grant)", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: "invalid_grant" }), {
        status: 403,
        headers: { "content-type": "application/json" }
      })
    )
    const provider = createRelayauthPathTokenAccessTokenProvider({
      accessToken: accessToken(-10),
      refreshToken: relayPaRefresh()
    })
    await expect(provider()).rejects.toBeInstanceOf(CloudApiError)
  })

  it("preserves a non-default port from the issuer", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(
          JSON.stringify({
            accessToken: accessToken(3600),
            refreshToken: relayPaRefresh()
          }),
          { status: 200, headers: { "content-type": "application/json" } }
        )
      )
    const refresh = jwt(
      {
        iss: "https://relayauth.dev:8443",
        aud: ["relayauth"],
        exp: Math.floor(Date.now() / 1000) + 90 * 24 * 3600
      },
      "relay_pa_"
    )
    const provider = createRelayauthPathTokenAccessTokenProvider({
      accessToken: accessToken(-10),
      refreshToken: refresh
    })
    await provider()
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "https://api.relayauth.dev:8443/v1/tokens/refresh"
    )
  })

  it("rejects a non-HTTPS RelayAuth URL, but allows loopback for local dev", () => {
    const refresh = relayPaRefresh()
    expect(() =>
      createRelayauthPathTokenAccessTokenProvider({
        accessToken: accessToken(3600),
        refreshToken: refresh,
        relayauthUrl: "http://api.evil.example"
      })
    ).toThrowError(RelayfileSetupError)
    // loopback over http is permitted (local self-host)
    expect(() =>
      createRelayauthPathTokenAccessTokenProvider({
        accessToken: accessToken(3600),
        refreshToken: refresh,
        relayauthUrl: "http://localhost:8787"
      })
    ).not.toThrow()
  })

  it("does not leak the refresh token when the endpoint cannot be resolved", () => {
    const secret = jwt({ aud: ["relayauth"] }, "relay_pa_") // no iss claim
    try {
      createRelayauthPathTokenAccessTokenProvider({
        accessToken: accessToken(3600),
        refreshToken: secret
      })
      throw new Error("expected throw")
    } catch (err) {
      expect(err).toBeInstanceOf(RelayfileSetupError)
      expect((err as Error).message).not.toContain(secret)
      expect((err as Error).message).not.toContain(secret.split(".")[1])
    }
  })

  it("times out if the server stalls the response body after headers", async () => {
    vi.useFakeTimers()
    try {
      vi.spyOn(globalThis, "fetch").mockImplementation(
        (_url, init?: RequestInit) => {
          const signal = init?.signal
          const response = {
            ok: true,
            status: 200,
            headers: { get: () => "application/json" },
            // Body never arrives until the request is aborted by the timeout.
            text: () =>
              new Promise<string>((_resolve, reject) => {
                signal?.addEventListener("abort", () =>
                  reject(new DOMException("aborted", "AbortError"))
                )
              })
          } as unknown as Response
          return Promise.resolve(response)
        }
      )
      const provider = createRelayauthPathTokenAccessTokenProvider(
        { accessToken: accessToken(-10), refreshToken: relayPaRefresh() },
        { requestTimeoutMs: 1000 }
      )
      const pending = provider()
      const assertion = expect(pending).rejects.toBeInstanceOf(CloudTimeoutError)
      await vi.advanceTimersByTimeAsync(1001)
      await assertion
    } finally {
      vi.useRealTimers()
    }
  })

  it("retries onTokens after a persistence failure without blocking token use", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          accessToken: accessToken(3600),
          refreshToken: relayPaRefresh()
        }),
        { status: 200, headers: { "content-type": "application/json" } }
      )
    )
    let calls = 0
    const onTokens = vi.fn(async () => {
      calls += 1
      if (calls === 1) throw new Error("disk full")
    })
    const provider = createRelayauthPathTokenAccessTokenProvider(
      { accessToken: accessToken(-10), refreshToken: relayPaRefresh() },
      { onTokens }
    )
    // First call: refresh happens, onTokens throws — but a valid token is still returned.
    const token1 = await provider()
    expect(typeof token1).toBe("string")
    expect(onTokens).toHaveBeenCalledTimes(1)
    // Second call: access token still valid, but persistence is pending -> retried and succeeds.
    await provider()
    expect(onTokens).toHaveBeenCalledTimes(2)
    // Third call: nothing pending -> no further callback.
    await provider()
    expect(onTokens).toHaveBeenCalledTimes(2)
  })
})

describe("RelayFileClient token-pair auto-wrap", () => {
  it("auto-refreshes a relay_pa token pair at RelayAuth with no extra wiring", async () => {
    const rotated = accessToken(3600)
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({ accessToken: rotated, refreshToken: relayPaRefresh() }),
        { status: 200, headers: { "content-type": "application/json" } }
      )
    )
    const client = new RelayFileClient({
      // Just the pair — the client wraps it in the RelayAuth rotating provider.
      token: {
        accessToken: accessToken(-10), // expired -> forces a refresh
        refreshToken: relayPaRefresh()
      },
      workspaceId: "rw_test"
    })
    const token = await client.getToken()
    expect(token).toBe(rotated)
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "https://api.relayauth.dev/v1/tokens/refresh"
    )
  })

  it("leaves a raw string token untouched (no refresh machinery)", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch")
    const client = new RelayFileClient({
      token: "relay_pa_static.token.here",
      workspaceId: "rw_test"
    })
    expect(await client.getToken()).toBe("relay_pa_static.token.here")
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("hands the rotated pair to onTokens so it survives a restart", async () => {
    // Reproduces the Render redeploy failure: the refresh token rotates and the
    // old one is revoked, so without persistence the next process reloads the
    // spent pair. With onTokens the caller can write the live pair back.
    const rotatedAccess = accessToken(3600)
    const rotatedRefresh = relayPaRefresh()
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          accessToken: rotatedAccess,
          refreshToken: rotatedRefresh
        }),
        { status: 200, headers: { "content-type": "application/json" } }
      )
    )
    const persisted: Array<{ accessToken: string; refreshToken: string }> = []
    const client = new RelayFileClient({
      token: {
        accessToken: accessToken(-10), // expired -> forces a refresh
        refreshToken: relayPaRefresh()
      },
      onTokens: (pair) => {
        persisted.push({
          accessToken: pair.accessToken,
          refreshToken: pair.refreshToken
        })
      },
      workspaceId: "rw_test"
    })
    expect(await client.getToken()).toBe(rotatedAccess)
    expect(persisted).toEqual([
      { accessToken: rotatedAccess, refreshToken: rotatedRefresh }
    ])
  })
})

describe("resolveCloudTokensAccessToken routing (base + CLI fromCloudTokens)", () => {
  const expired = new Date(Date.now() - 1000).toISOString()

  it("routes a relay_pa pair to the RelayAuth endpoint", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({ accessToken: accessToken(3600), refreshToken: relayPaRefresh() }),
        { status: 200, headers: { "content-type": "application/json" } }
      )
    )
    const provider = resolveCloudTokensAccessToken(
      {
        accessToken: accessToken(-10),
        refreshToken: relayPaRefresh(),
        accessTokenExpiresAt: expired
      },
      {},
      "https://agentrelay.com/cloud"
    )
    await (provider as () => Promise<string>)()
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "https://api.relayauth.dev/v1/tokens/refresh"
    )
  })

  it("routes a Cloud device-auth pair to the Cloud endpoint", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          accessToken: "new-access",
          refreshToken: "new-refresh",
          accessTokenExpiresAt: new Date(Date.now() + 3600_000).toISOString()
        }),
        { status: 200, headers: { "content-type": "application/json" } }
      )
    )
    const cloudRefresh = jwt({ iss: "https://agentrelay.com", aud: ["cloud"] }, "cld_rt_")
    const provider = resolveCloudTokensAccessToken(
      {
        accessToken: "expired-access",
        refreshToken: cloudRefresh,
        accessTokenExpiresAt: expired
      },
      {},
      "https://agentrelay.com/cloud"
    )
    await (provider as () => Promise<string>)()
    const url = String(fetchMock.mock.calls[0]?.[0])
    expect(url).toContain("/api/v1/auth/token/refresh")
    expect(url).not.toContain("api.relayauth.dev")
  })
})
