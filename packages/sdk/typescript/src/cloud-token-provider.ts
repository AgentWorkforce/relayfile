import type { AccessTokenProvider } from "./client.js"
import {
  CloudApiError,
  CloudTimeoutError,
  MalformedCloudResponseError
} from "./setup-errors.js"
import type { RelayfileSetupOptions } from "./setup-types.js"
import { RELAYFILE_SDK_VERSION } from "./version.js"

const DEFAULT_CLOUD_API_URL = "https://agentrelay.com/cloud"
const DEFAULT_REQUEST_TIMEOUT_MS = 30_000
const DEFAULT_REFRESH_WINDOW_MS = 60_000

export interface RelayfileCloudTokenSet {
  apiUrl?: string
  accessToken: string
  refreshToken: string
  accessTokenExpiresAt: string
  refreshTokenExpiresAt?: string
}

export interface RelayfileCloudTokenSetupOptions
  extends Omit<RelayfileSetupOptions, "accessToken" | "cloudApiUrl"> {
  cloudApiUrl?: string
  refreshWindowMs?: number
  onTokens?: (tokens: RelayfileCloudTokenSet) => void | Promise<void>
}

export function createRelayfileCloudAccessTokenProvider(
  initialTokens: RelayfileCloudTokenSet,
  options: RelayfileCloudTokenSetupOptions = {}
): AccessTokenProvider {
  const cloudApiUrl =
    normalizeNonEmptyString(options.cloudApiUrl) ??
    normalizeNonEmptyString(initialTokens.apiUrl) ??
    DEFAULT_CLOUD_API_URL
  const requestTimeoutMs = Math.max(
    1,
    Math.floor(options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS)
  )
  const refreshWindowMs = Math.max(
    0,
    Math.floor(options.refreshWindowMs ?? DEFAULT_REFRESH_WINDOW_MS)
  )
  let tokens: RelayfileCloudTokenSet = {
    ...initialTokens,
    apiUrl: normalizeNonEmptyString(initialTokens.apiUrl) ?? cloudApiUrl
  }
  let refreshPromise: Promise<void> | undefined

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
    }
    return tokens.accessToken
  }

  async function refresh(): Promise<void> {
    const response = await fetchWithTimeout(
      buildCloudUrl(cloudApiUrl, "api/v1/auth/token/refresh").toString(),
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
    const payload = await readResponseBody(response)
    if (!response.ok) {
      throw new CloudApiError(response.status, payload)
    }
    tokens = readTokenSetFromPayload(payload, cloudApiUrl)
    await options.onTokens?.({ ...tokens })
  }
}

function shouldRefresh(
  tokens: RelayfileCloudTokenSet,
  refreshWindowMs: number
): boolean {
  const expiresAt = Date.parse(tokens.accessTokenExpiresAt)
  if (Number.isNaN(expiresAt)) {
    return true
  }
  return expiresAt - Date.now() <= refreshWindowMs
}

async function fetchWithTimeout(
  url: string,
  init: RequestInit,
  timeoutMs: number
): Promise<Response> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  try {
    return await fetch(url, { ...init, signal: controller.signal })
  } catch (error) {
    if (controller.signal.aborted) {
      throw new CloudTimeoutError("refreshCloudAccessToken", timeoutMs)
    }
    throw error
  } finally {
    clearTimeout(timer)
  }
}

function readTokenSetFromPayload(
  payload: unknown,
  fallbackApiUrl: string
): RelayfileCloudTokenSet {
  return {
    apiUrl: readOptionalStringField(payload, "apiUrl") ?? fallbackApiUrl,
    accessToken: requireStringField(payload, "accessToken"),
    refreshToken: requireStringField(payload, "refreshToken"),
    accessTokenExpiresAt: requireStringField(payload, "accessTokenExpiresAt"),
    refreshTokenExpiresAt: readOptionalStringField(payload, "refreshTokenExpiresAt")
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

function buildCloudUrl(baseUrl: string, path: string): URL {
  const base = new URL(baseUrl)
  if (!base.pathname.endsWith("/")) {
    base.pathname = `${base.pathname}/`
  }
  return new URL(path.replace(/^\/+/, ""), base)
}

function normalizeNonEmptyString(value?: string): string | undefined {
  const normalized = value?.trim()
  return normalized ? normalized : undefined
}
