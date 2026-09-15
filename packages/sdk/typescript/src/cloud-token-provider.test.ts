import { afterEach, describe, expect, it, vi } from "vitest"
import { createRelayfileCloudAccessTokenProvider } from "./cloud-token-provider.js"

afterEach(() => {
  vi.restoreAllMocks()
})

const iso = (secondsFromNow: number): string =>
  new Date(Date.now() + secondsFromNow * 1000).toISOString()

describe("createRelayfileCloudAccessTokenProvider persistence", () => {
  it("retries onTokens after a persistence failure without blocking token use", async () => {
    // Mirrors the RelayAuth provider: a rotated pair whose onTokens callback
    // throws must not fail token use and must be retried, so a restart never
    // reloads a revoked refresh token.
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          accessToken: "cloud.rotated.access",
          refreshToken: "cloud.rotated.refresh",
          accessTokenExpiresAt: iso(3600)
        }),
        { status: 200, headers: { "content-type": "application/json" } }
      )
    )
    let calls = 0
    const onTokens = vi.fn(async () => {
      calls += 1
      if (calls === 1) throw new Error("disk full")
    })
    const provider = createRelayfileCloudAccessTokenProvider(
      {
        accessToken: "cloud.initial.access",
        refreshToken: "cloud.initial.refresh",
        accessTokenExpiresAt: iso(-10) // expired -> forces a refresh
      },
      { onTokens }
    )
    // First call: refresh happens, onTokens throws — but a valid token is still returned.
    const token1 = await provider()
    expect(token1).toBe("cloud.rotated.access")
    expect(onTokens).toHaveBeenCalledTimes(1)
    // Second call: access token still valid, but persistence is pending -> retried and succeeds.
    await provider()
    expect(onTokens).toHaveBeenCalledTimes(2)
    // Third call: nothing pending -> no further callback.
    await provider()
    expect(onTokens).toHaveBeenCalledTimes(2)
  })
})
