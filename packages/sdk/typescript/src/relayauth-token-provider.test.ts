import { afterEach, describe, expect, it, vi } from "vitest"
import {
  createRelayauthPathTokenAccessTokenProvider,
  isRelayauthRefreshToken
} from "./relayauth-token-provider.js"
import { CloudApiError } from "./setup-errors.js"

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
})
