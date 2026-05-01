import type { AccessTokenProvider } from "./client.js"
import {
  CloudAbortError,
  CloudApiError,
  CloudTimeoutError,
  MalformedCloudResponseError
} from "./setup-errors.js"
import type { RelayfileSetupOptions } from "./setup-types.js"

const DEFAULT_CLOUD_API_URL = "https://agentrelay.com/cloud"
const DEFAULT_LOGIN_TIMEOUT_MS = 5 * 60 * 1000
const DEFAULT_REDIRECT_HOST = "127.0.0.1"
const DEFAULT_REDIRECT_PATH = "/callback"
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

export interface RelayfileCloudLoginOptions
  extends RelayfileCloudTokenSetupOptions {
  redirectHost?: string
  redirectPort?: number
  redirectPath?: string
  timeoutMs?: number
  state?: string
  signal?: AbortSignal
  openBrowser?: boolean
  onLoginUrl?: (url: string) => void | Promise<void>
  successMessage?: string
}

export async function runRelayfileCloudLogin(
  options: RelayfileCloudLoginOptions = {}
): Promise<RelayfileCloudTokenSet> {
  const cloudApiUrl = normalizeNonEmptyString(options.cloudApiUrl) ?? DEFAULT_CLOUD_API_URL
  const redirectHost = normalizeNonEmptyString(options.redirectHost) ?? DEFAULT_REDIRECT_HOST
  const redirectPort = Math.max(0, Math.floor(options.redirectPort ?? 0))
  const redirectPath = normalizeRedirectPath(options.redirectPath)
  const timeoutMs = Math.max(1, Math.floor(options.timeoutMs ?? DEFAULT_LOGIN_TIMEOUT_MS))
  const state = normalizeNonEmptyString(options.state) ?? await createRandomState()
  const http = await import("node:http")

  return new Promise<RelayfileCloudTokenSet>((resolve, reject) => {
    let settled = false
    let loginUrlSent = false
    let timer: ReturnType<typeof setTimeout> | undefined

    const server = http.createServer((request, response) => {
      if (!request.url) {
        response.writeHead(400, { "content-type": "text/plain" })
        response.end("Missing callback URL")
        return
      }

      const callbackUrl = new URL(
        request.url,
        `http://${redirectHost}:${readServerPort(server, redirectPort)}`
      )
      if (callbackUrl.pathname !== redirectPath) {
        response.writeHead(404, { "content-type": "text/plain" })
        response.end("Not found")
        return
      }

      const error = callbackUrl.searchParams.get("error")
      if (error) {
        response.writeHead(400, { "content-type": "text/plain" })
        response.end("Relayfile Cloud login failed")
        finish(reject, new Error(`Relayfile Cloud login failed: ${error}`))
        return
      }

      const returnedState = callbackUrl.searchParams.get("state")
      if (returnedState !== state) {
        response.writeHead(400, { "content-type": "text/plain" })
        response.end("Relayfile Cloud login state mismatch")
        finish(reject, new Error("Relayfile Cloud login state mismatch"))
        return
      }

      let tokens: RelayfileCloudTokenSet
      try {
        tokens = readTokenSetFromSearchParams(callbackUrl.searchParams, cloudApiUrl)
      } catch (error) {
        response.writeHead(400, { "content-type": "text/plain" })
        response.end("Relayfile Cloud login callback was missing token fields")
        finish(reject, error)
        return
      }

      response.writeHead(200, { "content-type": "text/plain" })
      response.end(options.successMessage ?? "Relayfile Cloud login complete. You can close this tab.")
      finish(resolve, tokens)
    })

    const abort = () => {
      finish(reject, new CloudAbortError("cloudLogin"))
    }

    const finish = <T>(
      settle: (value: T | PromiseLike<T>) => void,
      value: T
    ): void => {
      if (settled) {
        return
      }
      settled = true
      if (timer) {
        clearTimeout(timer)
      }
      options.signal?.removeEventListener("abort", abort)
      server.close()
      settle(value)
    }

    if (options.signal?.aborted) {
      finish(reject, new CloudAbortError("cloudLogin"))
      return
    }

    timer = setTimeout(() => {
      finish(reject, new CloudTimeoutError("cloudLogin", timeoutMs))
    }, timeoutMs)
    options.signal?.addEventListener("abort", abort, { once: true })

    server.once("error", (error) => {
      finish(reject, error)
    })

    server.listen(redirectPort, redirectHost, () => {
      const port = readServerPort(server, redirectPort)
      const redirectUri = `http://${redirectHost}:${port}${redirectPath}`
      const loginUrl = buildCloudUrl(cloudApiUrl, "api/v1/cli/login")
      loginUrl.searchParams.set("redirect_uri", redirectUri)
      loginUrl.searchParams.set("state", state)

      Promise.resolve(deliverLoginUrl(loginUrl.toString(), options.onLoginUrl))
        .then(async () => {
          loginUrlSent = true
          if (options.openBrowser) {
            await openBrowser(loginUrl.toString())
          }
        })
        .catch((error) => {
          finish(reject, error)
        })
    })

    server.once("close", () => {
      if (!settled && !loginUrlSent) {
        finish(reject, new Error("Relayfile Cloud login server closed before login URL was delivered"))
      }
    })
  })
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
        headers: { "Content-Type": "application/json" },
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

function readTokenSetFromSearchParams(
  params: URLSearchParams,
  fallbackApiUrl: string
): RelayfileCloudTokenSet {
  return {
    apiUrl: normalizeNonEmptyString(params.get("api_url") ?? undefined) ?? fallbackApiUrl,
    accessToken: requireSearchParam(params, "access_token"),
    refreshToken: requireSearchParam(params, "refresh_token"),
    accessTokenExpiresAt: requireSearchParam(params, "access_token_expires_at"),
    refreshTokenExpiresAt: normalizeNonEmptyString(
      params.get("refresh_token_expires_at") ?? undefined
    )
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

function requireSearchParam(params: URLSearchParams, field: string): string {
  const value = normalizeNonEmptyString(params.get(field) ?? undefined)
  if (!value) {
    throw new MalformedCloudResponseError(field, Object.fromEntries(params))
  }
  return value
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

function normalizeRedirectPath(path?: string): string {
  const normalized = normalizeNonEmptyString(path) ?? DEFAULT_REDIRECT_PATH
  return normalized.startsWith("/") ? normalized : `/${normalized}`
}

function normalizeNonEmptyString(value?: string): string | undefined {
  const normalized = value?.trim()
  return normalized ? normalized : undefined
}

async function createRandomState(): Promise<string> {
  const crypto = await import("node:crypto")
  return crypto.randomBytes(24).toString("base64url")
}

function readServerPort(
  server: { address: () => string | { port: number } | null },
  fallback: number
): number {
  const address = server.address()
  return typeof address === "object" && address !== null ? address.port : fallback
}

async function openBrowser(url: string): Promise<void> {
  const childProcess = await import("node:child_process")
  const command =
    process.platform === "darwin"
      ? "open"
      : process.platform === "win32"
        ? "cmd"
        : "xdg-open"
  const args =
    process.platform === "win32"
      ? ["/c", "start", "", url]
      : [url]

  await new Promise<void>((resolve) => {
    const child = childProcess.spawn(command, args, {
      detached: true,
      stdio: "ignore"
    })
    child.once("error", () => resolve())
    child.once("spawn", () => {
      child.unref()
      resolve()
    })
  })
}

function deliverLoginUrl(
  url: string,
  onLoginUrl?: (url: string) => void | Promise<void>
): void | Promise<void> {
  if (onLoginUrl) {
    return onLoginUrl(url)
  }
  console.log(`Sign in to Relayfile Cloud: ${url}`)
}
