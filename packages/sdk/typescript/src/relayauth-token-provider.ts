import type { AccessTokenProvider } from "./client.js"
import {
  CloudApiError,
  CloudTimeoutError,
  MalformedCloudResponseError,
  RelayfileSetupError
} from "./setup-errors.js"
import { RELAYFILE_SDK_VERSION } from "./version.js"

// A `relay_pa` (RelayAuth path-token) access+refresh pair is issued and rotated
// by RelayAuth, NOT by the Cloud device-auth flow. Its refresh token carries
// `aud: ["relayauth"]` and scope `relayauth:token:refresh`, and it is exchanged
// at `<relayauth>/v1/tokens/refresh` — a DIFFERENT endpoint from Cloud's
// `api/v1/auth/token/refresh`. Sending such a refresh token to the Cloud endpoint
// returns `invalid_grant`, which is the failure mode this provider fixes:
// `createRelayfileCloudAccessTokenProvider` unconditionally targets Cloud, so a
// relay_pa credential (e.g. `RELAYFILE_ACCESS_TOKEN` + `RELAYFILE_REFRESH_TOKEN`
// in an agent .env) never rotates through it. Conveniently, RelayAuth's refresh
// response has the SAME shape Cloud returns
// (`accessToken`/`refreshToken`/`accessTokenExpiresAt`/`refreshTokenExpiresAt`),
// so only the endpoint differs.

const DEFAULT_REQUEST_TIMEOUT_MS = 30_000
const DEFAULT_REFRESH_WINDOW_MS = 60_000

export interface RelayauthPathTokenSet {
  accessToken: string
  refreshToken: string
  /** Optional; when omitted it is derived from the access token's `exp` claim. */
  accessTokenExpiresAt?: string
  refreshTokenExpiresAt?: string
  /**
   * RelayAuth base URL (e.g. `https://api.relayauth.dev`). When omitted it is
   * derived from the refresh token's `iss` claim, mapping the issuer host to its
   * API host (`relayauth.dev` -> `api.relayauth.dev`).
   */
  relayauthUrl?: string
}

export interface RelayauthPathTokenSetupOptions {
  requestTimeoutMs?: number
  refreshWindowMs?: number
  onTokens?: (tokens: RelayauthPathTokenSet) => void | Promise<void>
}

/** True when `refreshToken` is a RelayAuth-issued refresh token (relay_pa pair). */
export function isRelayauthRefreshToken(refreshToken: string): boolean {
  const claims = decodeJwtClaims(refreshToken)
  if (!claims) return false
  const aud = claims.aud
  const audiences = Array.isArray(aud) ? aud : typeof aud === "string" ? [aud] : []
  if (audiences.includes("relayauth")) return true
  const scopes = Array.isArray(claims.scopes) ? claims.scopes : []
  return scopes.includes("relayauth:token:refresh")
}

export function createRelayauthPathTokenAccessTokenProvider(
  initialTokens: RelayauthPathTokenSet,
  options: RelayauthPathTokenSetupOptions = {}
): AccessTokenProvider {
  const requestTimeoutMs = Math.max(
    1,
    Math.floor(options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS)
  )
  const refreshWindowMs = Math.max(
    0,
    Math.floor(options.refreshWindowMs ?? DEFAULT_REFRESH_WINDOW_MS)
  )
  const refreshUrl = buildRefreshUrl(initialTokens)

  let tokens = withDerivedAccessExpiry(initialTokens)
  let refreshPromise: Promise<void> | undefined
  // A rotated token set whose `onTokens` persistence callback has not yet
  // succeeded. Until it does, a consumed refresh token may still live in the
  // caller's persisted copy (→ `invalid_grant` after restart), so we retry the
  // callback on every subsequent use rather than treating rotation as complete.
  let pendingPersist: RelayauthPathTokenSet | undefined

  return async () => {
    if (shouldRefresh(tokens, refreshWindowMs)) {
      if (!refreshPromise) {
        refreshPromise = refresh()
      }
      try {
        await refreshPromise
      } finally {
        refreshPromise = undefined
      }
    } else if (pendingPersist) {
      // No refresh needed, but a prior rotation still owes a successful persist.
      await persistPendingTokens()
    }
    return tokens.accessToken
  }

  async function refresh(): Promise<void> {
    const { response, payload } = await fetchJsonWithTimeout(
      refreshUrl,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Relayfile-SDK-Version": RELAYFILE_SDK_VERSION
        },
        body: JSON.stringify({ refreshToken: tokens.refreshToken })
      },
      requestTimeoutMs
    )
    if (!response.ok) {
      throw new CloudApiError(response.status, payload)
    }
    tokens = withDerivedAccessExpiry({
      relayauthUrl: tokens.relayauthUrl,
      accessToken: requireStringField(payload, "accessToken"),
      refreshToken: requireStringField(payload, "refreshToken"),
      accessTokenExpiresAt: readOptionalStringField(payload, "accessTokenExpiresAt"),
      refreshTokenExpiresAt: readOptionalStringField(payload, "refreshTokenExpiresAt")
    })
    pendingPersist = { ...tokens }
    await persistPendingTokens()
  }

  // Best-effort, retried persistence. A callback failure never rejects the
  // provider — the in-memory access token is valid and usable — but the set
  // stays pending so the next provider() call re-attempts, so a durable copy
  // eventually catches up with the rotated refresh token.
  async function persistPendingTokens(): Promise<void> {
    if (!pendingPersist) {
      return
    }
    if (!options.onTokens) {
      pendingPersist = undefined
      return
    }
    const toPersist = pendingPersist
    try {
      await options.onTokens({ ...toPersist })
      // Only clear if nothing rotated again while the callback was in flight.
      if (pendingPersist === toPersist) {
        pendingPersist = undefined
      }
    } catch {
      // Keep pending; retried on the next use.
    }
  }
}

function buildRefreshUrl(tokens: RelayauthPathTokenSet): string {
  const explicit = normalizeNonEmptyString(tokens.relayauthUrl)
  const base = explicit ?? deriveRelayauthApiBase(tokens.refreshToken)
  if (!base) {
    // NEVER put the refresh token in the error — it is a live bearer credential.
    throw new RelayfileSetupError(
      "Cannot determine the RelayAuth refresh endpoint: pass `relayauthUrl`, or use a refresh token that carries an `iss` claim.",
      "relayauth_url_unresolved"
    )
  }
  let url: URL
  try {
    url = new URL(base)
  } catch {
    throw new RelayfileSetupError(
      `Invalid RelayAuth URL: ${base}`,
      "relayauth_url_invalid"
    )
  }
  // The refresh token is transmitted in the request body, so the endpoint must
  // be HTTPS. A loopback host is allowed for local self-host development.
  if (url.protocol !== "https:" && !isLoopbackHost(url.hostname)) {
    throw new RelayfileSetupError(
      `RelayAuth refresh endpoint must use HTTPS (got ${url.protocol}//${url.host}).`,
      "relayauth_url_insecure"
    )
  }
  if (!url.pathname.endsWith("/")) {
    url.pathname = `${url.pathname}/`
  }
  return new URL("v1/tokens/refresh", url).toString()
}

function isLoopbackHost(hostname: string): boolean {
  return (
    hostname === "localhost" ||
    hostname === "127.0.0.1" ||
    hostname === "::1" ||
    hostname === "[::1]"
  )
}

// Derive the RelayAuth API base from the refresh token's `iss`. The issuer is the
// public host (`https://relayauth.dev`); token exchange lives on the API host
// (`https://api.relayauth.dev`). A host that already begins with `api.` is left
// as-is so self-hosted issuers work unchanged.
function deriveRelayauthApiBase(refreshToken: string): string | undefined {
  const claims = decodeJwtClaims(refreshToken)
  const iss = typeof claims?.iss === "string" ? claims.iss : undefined
  if (!iss) return undefined
  try {
    const url = new URL(iss)
    if (!url.hostname.startsWith("api.")) {
      url.hostname = `api.${url.hostname}`
    }
    // `url.host` preserves a non-default port; `url.hostname` would drop it.
    return `${url.protocol}//${url.host}`
  } catch {
    return undefined
  }
}

function withDerivedAccessExpiry(
  tokens: RelayauthPathTokenSet
): RelayauthPathTokenSet & { accessTokenExpiresAt: string } {
  const explicit = normalizeNonEmptyString(tokens.accessTokenExpiresAt)
  if (explicit && !Number.isNaN(Date.parse(explicit))) {
    return { ...tokens, accessTokenExpiresAt: explicit }
  }
  const exp = decodeJwtClaims(tokens.accessToken)?.exp
  const iso =
    typeof exp === "number" && Number.isFinite(exp)
      ? new Date(exp * 1000).toISOString()
      : new Date(0).toISOString() // unknown -> force refresh on first use
  return { ...tokens, accessTokenExpiresAt: iso }
}

function shouldRefresh(
  tokens: RelayauthPathTokenSet & { accessTokenExpiresAt: string },
  refreshWindowMs: number
): boolean {
  const expiresAt = Date.parse(tokens.accessTokenExpiresAt)
  if (Number.isNaN(expiresAt)) {
    return true
  }
  return expiresAt - Date.now() <= refreshWindowMs
}

interface JwtClaims {
  iss?: unknown
  aud?: unknown
  exp?: unknown
  scopes?: unknown
}

function decodeJwtClaims(token: string): JwtClaims | undefined {
  try {
    const compact = token.replace(/^relay_[a-z]+_/, "")
    const payload = compact.split(".")[1]
    if (!payload) return undefined
    const json = Buffer.from(payload, "base64url").toString("utf8")
    return JSON.parse(json) as JwtClaims
  } catch {
    return undefined
  }
}

// Runs the fetch AND the response-body read under one timeout, so a server that
// sends headers then stalls the body still trips `requestTimeoutMs` instead of
// hanging the shared refresh (and every caller awaiting it) indefinitely.
async function fetchJsonWithTimeout(
  url: string,
  init: RequestInit,
  timeoutMs: number
): Promise<{ response: Response; payload: unknown }> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  try {
    const response = await fetch(url, { ...init, signal: controller.signal })
    const payload = await readResponseBody(response)
    return { response, payload }
  } catch (error) {
    if (controller.signal.aborted) {
      throw new CloudTimeoutError("refreshRelayauthAccessToken", timeoutMs)
    }
    throw error
  } finally {
    clearTimeout(timer)
  }
}

async function readResponseBody(response: Response): Promise<unknown> {
  const text = await response.text()
  if (text === "") {
    return null
  }
  const contentType = response.headers.get("content-type") ?? ""
  if (contentType.includes("application/json")) {
    try {
      return JSON.parse(text)
    } catch {
      return text
    }
  }
  return text
}

function requireStringField(payload: unknown, field: string): string {
  const value = readField(payload, field)
  if (typeof value !== "string" || value.trim() === "") {
    throw new MalformedCloudResponseError(field, payload)
  }
  return value
}

function readOptionalStringField(payload: unknown, field: string): string | undefined {
  const value = readField(payload, field)
  if (value === undefined) {
    return undefined
  }
  if (typeof value !== "string" || value.trim() === "") {
    throw new MalformedCloudResponseError(field, payload)
  }
  return value
}

function readField(payload: unknown, field: string): unknown {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
    return undefined
  }
  return (payload as Record<string, unknown>)[field]
}

function normalizeNonEmptyString(value?: string): string | undefined {
  const normalized = value?.trim()
  return normalized ? normalized : undefined
}
