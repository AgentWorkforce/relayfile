/* Agent Relay Cloud SDK 11.8.1; bundled for Relayfile login. */
"use strict";
var __create = Object.create;
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __getProtoOf = Object.getPrototypeOf;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __export = (target, all) => {
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: true });
};
var __copyProps = (to, from, except, desc) => {
  if (from && typeof from === "object" || typeof from === "function") {
    for (let key of __getOwnPropNames(from))
      if (!__hasOwnProp.call(to, key) && key !== except)
        __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  }
  return to;
};
var __toESM = (mod, isNodeMode, target) => (target = mod != null ? __create(__getProtoOf(mod)) : {}, __copyProps(
  // If the importer is in node compatibility mode or this is not an ESM
  // file that has been converted to a CommonJS file using a Babel-
  // compatible transform (i.e. "__esModule" has not been set), then set
  // "default" to the CommonJS "module.exports" for node compatibility.
  isNodeMode || !mod || !mod.__esModule ? __defProp(target, "default", { value: mod, enumerable: true }) : target,
  mod
));
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: true }), mod);

// packages/cli/scripts/cloud-auth-entry.mjs
var cloud_auth_entry_exports = {};
__export(cloud_auth_entry_exports, {
  ensureCloudSession: () => ensureCloudSession
});
module.exports = __toCommonJS(cloud_auth_entry_exports);

// ../relay/packages/cloud/src/auth.ts
var import_node_crypto = require("node:crypto");
var import_promises3 = __toESM(require("node:fs/promises"), 1);
var import_node_http = __toESM(require("node:http"), 1);
var import_node_os4 = __toESM(require("node:os"), 1);
var import_node_path3 = __toESM(require("node:path"), 1);
var import_node_child_process = require("node:child_process");
var import_promises4 = require("node:timers/promises");

// ../relay/packages/cloud/src/types.ts
var import_node_os = __toESM(require("node:os"), 1);
var import_node_path = __toESM(require("node:path"), 1);
var CloudAuthError = class extends Error {
  constructor(code, message, options) {
    super(message, options);
    this.code = code;
    this.name = "CloudAuthError";
  }
  code;
};
var REFRESH_WINDOW_MS = 5 * 6e4;
var REFRESH_TOKEN_WINDOW_MS = 24 * 60 * 60 * 1e3;
var DEFAULT_REFRESH_TIMEOUT_MS = 1e4;
var AUTH_FILE_PATH = import_node_path.default.join(import_node_os.default.homedir(), ".agentworkforce/relay", "cloud-auth.json");
function defaultApiUrl() {
  return process.env.CLOUD_API_URL?.trim() || "https://agentrelay.com/cloud";
}

// ../relay/packages/cloud/src/telemetry-headers.ts
var AGENT_RELAY_DISTINCT_ID_HEADER = "X-Agent-Relay-Distinct-Id";
var AGENT_RELAY_MACHINE_ID_HEADER = "X-Agent-Relay-Machine-Id";
var AGENT_RELAY_USER_ID_HEADER = "X-Agent-Relay-User-Id";
var AGENT_RELAY_ORG_ID_HEADER = "X-Agent-Relay-Org-Id";
var AGENT_RELAY_ORG_SLUG_HEADER = "X-Agent-Relay-Org-Slug";
var RELAYCAST_HARNESS_HEADER = "X-Relaycast-Harness";
var RELAYCAST_ORIGIN_CLIENT_HEADER = "X-Relaycast-Origin-Client";
var RELAYCAST_ORIGIN_VERSION_HEADER = "X-Relaycast-Origin-Version";
var AGENT_RELAY_DISTINCT_ID_ENV = "AGENT_RELAY_DISTINCT_ID";
var AGENT_RELAY_MACHINE_ID_ENV = "AGENT_RELAY_MACHINE_ID";
var AGENT_RELAY_USER_ID_ENV = "AGENT_RELAY_USER_ID";
var AGENT_RELAY_ORG_ID_ENV = "AGENT_RELAY_ORG_ID";
var AGENT_RELAY_ORG_SLUG_ENV = "AGENT_RELAY_ORG_SLUG";
var ORCHESTRATOR_HARNESS_ENV = "AGENT_RELAY_ORCHESTRATOR_HARNESS";
var TELEMETRY_CLIENT_ENV = "AGENT_RELAY_TELEMETRY_CLIENT";
var DISTINCT_ID_ALLOWED = /^[a-z0-9._:-]+$/i;
var HEADER_VALUE_ALLOWED = /^[a-z0-9 ._\-/():=;,+@]+$/i;
function sanitizeHeaderValue(raw, options) {
  if (!raw) return void 0;
  const trimmed = raw.trim();
  if (!trimmed) return void 0;
  if (options.pattern && !options.pattern.test(trimmed)) return void 0;
  return trimmed.slice(0, options.maxLength);
}
function isTelemetryDisabledByEnv(env) {
  const disabled = env.AGENT_RELAY_TELEMETRY_DISABLED ?? env.DO_NOT_TRACK;
  return disabled === "1" || disabled?.toLowerCase() === "true";
}
function buildAgentRelayTelemetryHeaders(env = process.env) {
  if (isTelemetryDisabledByEnv(env)) return {};
  const userId = sanitizeHeaderValue(env[AGENT_RELAY_USER_ID_ENV], {
    maxLength: 128,
    pattern: DISTINCT_ID_ALLOWED
  });
  const machineId = sanitizeHeaderValue(env[AGENT_RELAY_MACHINE_ID_ENV], {
    maxLength: 128,
    pattern: DISTINCT_ID_ALLOWED
  });
  const distinctId = sanitizeHeaderValue(env[AGENT_RELAY_DISTINCT_ID_ENV], {
    maxLength: 128,
    pattern: DISTINCT_ID_ALLOWED
  }) ?? userId ?? machineId;
  if (!distinctId) return {};
  const headers = {
    [AGENT_RELAY_DISTINCT_ID_HEADER]: distinctId
  };
  if (machineId) headers[AGENT_RELAY_MACHINE_ID_HEADER] = machineId;
  if (userId) headers[AGENT_RELAY_USER_ID_HEADER] = userId;
  const orgId = sanitizeHeaderValue(env[AGENT_RELAY_ORG_ID_ENV], {
    maxLength: 128,
    pattern: DISTINCT_ID_ALLOWED
  });
  if (orgId) headers[AGENT_RELAY_ORG_ID_HEADER] = orgId;
  const orgSlug = sanitizeHeaderValue(env[AGENT_RELAY_ORG_SLUG_ENV], {
    maxLength: 120,
    pattern: DISTINCT_ID_ALLOWED
  });
  if (orgSlug) headers[AGENT_RELAY_ORG_SLUG_HEADER] = orgSlug;
  const harness = sanitizeHeaderValue(env[ORCHESTRATOR_HARNESS_ENV], {
    maxLength: 120,
    pattern: HEADER_VALUE_ALLOWED
  });
  const client = sanitizeHeaderValue(env[TELEMETRY_CLIENT_ENV], {
    maxLength: 80,
    pattern: HEADER_VALUE_ALLOWED
  });
  const version = sanitizeHeaderValue(env.AGENT_RELAY_CLI_VERSION ?? env.AGENT_RELAY_SDK_VERSION, {
    maxLength: 48,
    pattern: HEADER_VALUE_ALLOWED
  });
  if (harness) headers[RELAYCAST_HARNESS_HEADER] = harness;
  if (client) headers[RELAYCAST_ORIGIN_CLIENT_HEADER] = client;
  if (version) headers[RELAYCAST_ORIGIN_VERSION_HEADER] = version;
  return headers;
}
function appendAgentRelayTelemetryHeaders(headers, env = process.env) {
  for (const [name, value] of Object.entries(buildAgentRelayTelemetryHeaders(env))) {
    if (!headers.has(name)) {
      headers.set(name, value);
    }
  }
  return headers;
}

// ../relay/packages/cloud/src/api-client.ts
function trimLeadingSlash(p) {
  return p.replace(/^\/+/, "");
}
function withTrailingSlash(p) {
  return p.endsWith("/") ? p : `${p}/`;
}
function buildApiUrl(apiUrl, p) {
  return new URL(trimLeadingSlash(p), withTrailingSlash(apiUrl));
}
var CloudApiClient = class _CloudApiClient {
  constructor(options) {
    this.options = options;
    this.apiUrl = options.apiUrl;
    this.accessToken = options.accessToken;
    this.refreshToken = options.refreshToken;
    this.accessTokenExpiresAt = options.accessTokenExpiresAt;
    this.refreshTokenExpiresAt = options.refreshTokenExpiresAt;
  }
  options;
  apiUrl;
  accessToken;
  refreshToken;
  accessTokenExpiresAt;
  refreshTokenExpiresAt;
  refreshPromise = null;
  static fromEnv(env) {
    const apiUrl = env.CLOUD_API_URL?.trim();
    const accessToken = env.CLOUD_API_ACCESS_TOKEN?.trim();
    const refreshToken = env.CLOUD_API_REFRESH_TOKEN?.trim();
    const accessTokenExpiresAt = env.CLOUD_API_ACCESS_TOKEN_EXPIRES_AT?.trim();
    const refreshTokenExpiresAt = env.CLOUD_API_REFRESH_TOKEN_EXPIRES_AT?.trim();
    if (!apiUrl || !accessToken || !refreshToken || !accessTokenExpiresAt) {
      return null;
    }
    return new _CloudApiClient({
      apiUrl,
      accessToken,
      refreshToken,
      accessTokenExpiresAt,
      refreshTokenExpiresAt
    });
  }
  snapshot() {
    return {
      apiUrl: this.apiUrl,
      accessToken: this.accessToken,
      refreshToken: this.refreshToken,
      accessTokenExpiresAt: this.accessTokenExpiresAt,
      ...this.refreshTokenExpiresAt ? { refreshTokenExpiresAt: this.refreshTokenExpiresAt } : {}
    };
  }
  async fetch(p, init = {}) {
    await this.refresh(false, init.signal ?? void 0);
    const response = await fetch(buildApiUrl(this.apiUrl, p), {
      ...init,
      headers: this.buildHeaders(init.headers)
    });
    if (response.status !== 401) {
      return response;
    }
    await this.refresh(true, init.signal ?? void 0);
    return fetch(buildApiUrl(this.apiUrl, p), {
      ...init,
      headers: this.buildHeaders(init.headers)
    });
  }
  async revoke() {
    const response = await fetch(buildApiUrl(this.apiUrl, "/api/v1/auth/token/revoke"), {
      method: "POST",
      headers: {
        "Content-Type": "application/json"
      },
      body: JSON.stringify({ token: this.refreshToken })
    });
    if (!response.ok && response.status !== 404) {
      throw new Error(`Failed to revoke API token: ${response.status} ${response.statusText}`);
    }
  }
  async refresh(force = false, signal) {
    if (this.refreshPromise) {
      return this.refreshPromise;
    }
    if (!force && !this.shouldRefresh()) {
      return;
    }
    this.refreshPromise = this.doRefresh(force, signal).finally(() => {
      this.refreshPromise = null;
    });
    return this.refreshPromise;
  }
  async doRefresh(force, signal) {
    if (this.options.refreshAuth) {
      this.applySnapshot(await this.options.refreshAuth(this.snapshot(), { force, signal }));
      await this.options.onRefresh?.(this.snapshot());
      return;
    }
    this.applySnapshot(await this.requestRefresh(signal));
    await this.options.onRefresh?.(this.snapshot());
  }
  async requestRefresh(signal) {
    const refreshTimeoutMs = this.options.refreshTimeoutMs ?? DEFAULT_REFRESH_TIMEOUT_MS;
    const controller = new AbortController();
    let timedOut = false;
    let callerAborted = false;
    const abortFromCaller = () => {
      callerAborted = true;
      controller.abort();
    };
    if (signal) {
      if (signal.aborted) {
        callerAborted = true;
        controller.abort();
      } else {
        signal.addEventListener("abort", abortFromCaller, { once: true });
      }
    }
    const timer = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, refreshTimeoutMs);
    let response;
    try {
      response = await fetch(buildApiUrl(this.apiUrl, "/api/v1/auth/token/refresh"), {
        method: "POST",
        headers: {
          "Content-Type": "application/json"
        },
        body: JSON.stringify({ refreshToken: this.refreshToken }),
        signal: controller.signal
      });
    } catch (error) {
      if (timedOut || !callerAborted && error instanceof Error && error.name === "AbortError") {
        throw new CloudAuthError(
          "AUTH_REFRESH_TIMEOUT",
          `Cloud auth refresh timed out after ${refreshTimeoutMs}ms`,
          { cause: error }
        );
      }
      throw error;
    } finally {
      clearTimeout(timer);
      if (signal) {
        signal.removeEventListener("abort", abortFromCaller);
      }
    }
    if (!response.ok) {
      throw new CloudAuthError(
        "AUTH_REFRESH_EXPIRED",
        `Failed to refresh API token: ${response.status} ${response.statusText}`
      );
    }
    const payload = await response.json();
    if (!payload.accessToken || !payload.accessTokenExpiresAt || !payload.refreshToken) {
      throw new CloudAuthError("AUTH_REFRESH_EXPIRED", "Refresh response missing token fields");
    }
    const nextRefreshTokenExpiresAt = typeof payload.refreshTokenExpiresAt === "string" && payload.refreshTokenExpiresAt.trim() ? payload.refreshTokenExpiresAt.trim() : this.refreshTokenExpiresAt;
    return {
      apiUrl: typeof payload.apiUrl === "string" && payload.apiUrl.trim() ? payload.apiUrl.trim() : this.apiUrl,
      accessToken: payload.accessToken,
      accessTokenExpiresAt: payload.accessTokenExpiresAt,
      refreshToken: payload.refreshToken,
      ...nextRefreshTokenExpiresAt ? { refreshTokenExpiresAt: nextRefreshTokenExpiresAt } : {}
    };
  }
  applySnapshot(snapshot) {
    this.apiUrl = snapshot.apiUrl;
    this.accessToken = snapshot.accessToken;
    this.accessTokenExpiresAt = snapshot.accessTokenExpiresAt;
    this.refreshToken = snapshot.refreshToken;
    this.refreshTokenExpiresAt = snapshot.refreshTokenExpiresAt;
  }
  buildHeaders(headers) {
    const merged = new Headers(headers);
    merged.set("Authorization", `Bearer ${this.accessToken}`);
    return appendAgentRelayTelemetryHeaders(merged);
  }
  shouldRefresh() {
    const expiresAt = Date.parse(this.accessTokenExpiresAt);
    if (Number.isNaN(expiresAt)) {
      return true;
    }
    if (expiresAt - Date.now() <= REFRESH_WINDOW_MS) {
      return true;
    }
    if (!this.refreshTokenExpiresAt) {
      return false;
    }
    const refreshExpiresAt = Date.parse(this.refreshTokenExpiresAt);
    if (Number.isNaN(refreshExpiresAt)) {
      return true;
    }
    return refreshExpiresAt - Date.now() <= REFRESH_TOKEN_WINDOW_MS;
  }
};

// ../relay/packages/cloud/src/identity.ts
var import_promises = __toESM(require("node:fs/promises"), 1);
var import_node_os2 = __toESM(require("node:os"), 1);
var import_node_path2 = __toESM(require("node:path"), 1);
var DEFAULT_DATA_DIR = import_node_path2.default.join(import_node_os2.default.homedir(), ".agentworkforce/relay");
var IDENTITY_FILE_NAME = "cloud-identity.json";
var IDENTITY_FILE_PATH = import_node_path2.default.join(DEFAULT_DATA_DIR, IDENTITY_FILE_NAME);
function identityFilePath(env = process.env) {
  const dataDir = env.AGENT_RELAY_DATA_DIR?.trim() || DEFAULT_DATA_DIR;
  return import_node_path2.default.join(dataDir, IDENTITY_FILE_NAME);
}
var IDENTITY_VALUE_ALLOWED = /^[\x20-\x7E]+$/;
function sanitize(value, maxLength) {
  if (typeof value !== "string") return void 0;
  const trimmed = value.trim();
  if (!trimmed) return void 0;
  if (!IDENTITY_VALUE_ALLOWED.test(trimmed)) return void 0;
  return trimmed.slice(0, maxLength);
}
function normalizeCloudIdentity(value) {
  if (!value || typeof value !== "object") return null;
  const raw = value;
  const userId = sanitize(raw.userId, 128);
  if (!userId) return null;
  const apiUrl = sanitize(raw.apiUrl, 512);
  const updatedAt = sanitize(raw.updatedAt, 40);
  return {
    userId,
    ...sanitize(raw.email, 320) ? { email: sanitize(raw.email, 320) } : {},
    ...sanitize(raw.name, 120) ? { name: sanitize(raw.name, 120) } : {},
    ...sanitize(raw.organizationId, 128) ? { organizationId: sanitize(raw.organizationId, 128) } : {},
    ...sanitize(raw.organizationSlug, 120) ? { organizationSlug: sanitize(raw.organizationSlug, 120) } : {},
    ...sanitize(raw.organizationName, 200) ? { organizationName: sanitize(raw.organizationName, 200) } : {},
    ...sanitize(raw.organizationRole, 60) ? { organizationRole: sanitize(raw.organizationRole, 60) } : {},
    ...sanitize(raw.workspaceId, 128) ? { workspaceId: sanitize(raw.workspaceId, 128) } : {},
    apiUrl: apiUrl ?? "",
    updatedAt: updatedAt ?? (/* @__PURE__ */ new Date(0)).toISOString()
  };
}
async function writeStoredIdentity(identity, env = process.env) {
  const normalized = normalizeCloudIdentity(identity);
  if (!normalized) return;
  const target = identityFilePath(env);
  const tmp = `${target}.${process.pid}.tmp`;
  try {
    await import_promises.default.mkdir(import_node_path2.default.dirname(target), { recursive: true, mode: 448 });
    await import_promises.default.writeFile(tmp, `${JSON.stringify(normalized, null, 2)}
`, { mode: 384 });
    await import_promises.default.rename(tmp, target);
  } catch {
    await import_promises.default.rm(tmp, { force: true }).catch(() => void 0);
  }
}

// ../relay/packages/cloud/src/device-auth.ts
var import_node_os3 = __toESM(require("node:os"), 1);
var import_promises2 = require("node:timers/promises");
var DEVICE_GRANT_TYPE = "urn:ietf:params:oauth:grant-type:device_code";
var DEFAULT_INTERVAL_SECONDS = 5;
var SLOW_DOWN_INCREMENT_SECONDS = 5;
var MAX_INTERVAL_SECONDS = 60;
var DEFAULT_EXPIRES_IN_SECONDS = 600;
var DEFAULT_REQUEST_TIMEOUT_MS = 3e4;
var MAX_TIMER_DELAY_MS = 2147483647;
function deviceError(message) {
  return new CloudAuthError("AUTH_DEVICE_FLOW_FAILED", message);
}
function clampTimerDelay(ms) {
  if (!Number.isFinite(ms)) {
    return MAX_TIMER_DELAY_MS;
  }
  return Math.min(MAX_TIMER_DELAY_MS, Math.max(1, Math.floor(ms)));
}
function normalizeTimeout(ms) {
  if (typeof ms !== "number" || !Number.isFinite(ms) || ms <= 0) {
    return DEFAULT_REQUEST_TIMEOUT_MS;
  }
  return clampTimerDelay(ms);
}
function formatDuration(ms) {
  return ms >= 1e3 ? `${Math.round(ms / 1e3)}s` : `${Math.round(ms)}ms`;
}
function isRequestTimeout(error) {
  let current = error;
  for (let depth = 0; current instanceof Error && depth < 4; depth += 1) {
    if (current.name === "TimeoutError" || current.name === "AbortError") {
      return true;
    }
    current = current.cause;
  }
  return false;
}
function clampInterval(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) {
    return DEFAULT_INTERVAL_SECONDS;
  }
  return Math.min(MAX_INTERVAL_SECONDS, Math.ceil(seconds));
}
function isHeadlessEnvironment(env = process.env, platform = import_node_os3.default.platform()) {
  if (env.SSH_CONNECTION || env.SSH_TTY || env.SSH_CLIENT) {
    return true;
  }
  if (platform !== "darwin" && platform !== "win32") {
    return !env.DISPLAY && !env.WAYLAND_DISPLAY;
  }
  return false;
}
async function startDeviceAuthorization(apiUrl, options = {}) {
  const fetchImpl = options.fetchImpl ?? fetch;
  const clientName = options.clientName ?? import_node_os3.default.hostname();
  const timeoutMs = normalizeTimeout(options.requestTimeoutMs);
  let response;
  try {
    response = await fetchImpl(buildApiUrl(apiUrl, "/api/v1/auth/device/start"), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ client_name: clientName }),
      // Nothing has been printed yet at this point, so a hang here shows the
      // user a bare cursor forever. Fail loudly instead.
      signal: AbortSignal.timeout(timeoutMs)
    });
  } catch (error) {
    if (isRequestTimeout(error)) {
      throw new CloudAuthError(
        "AUTH_DEVICE_FLOW_FAILED",
        `Timed out after ${formatDuration(timeoutMs)} trying to reach ${apiUrl} to start device login`,
        { cause: error }
      );
    }
    throw new CloudAuthError("AUTH_DEVICE_FLOW_FAILED", `Could not reach ${apiUrl} to start device login`, {
      cause: error
    });
  }
  const payload = await response.json().catch(() => null);
  if (!response.ok) {
    if (response.status === 404) {
      throw deviceError(
        `${apiUrl} does not support device login. Update the cloud deployment, or run \`agent-relay cloud login\` on a machine with a browser.`
      );
    }
    const detail = payload?.error_description || payload?.error || `HTTP ${response.status}`;
    throw deviceError(`Could not start device login: ${detail}`);
  }
  if (!payload?.device_code || !payload.user_code || !payload.verification_uri) {
    throw deviceError("Device login response was missing required fields");
  }
  return {
    deviceCode: payload.device_code,
    userCode: payload.user_code,
    verificationUri: payload.verification_uri,
    ...payload.verification_uri_complete ? { verificationUriComplete: payload.verification_uri_complete } : {},
    expiresInSeconds: payload.expires_in ?? DEFAULT_EXPIRES_IN_SECONDS,
    intervalSeconds: clampInterval(payload.interval ?? DEFAULT_INTERVAL_SECONDS)
  };
}
async function pollForDeviceToken(apiUrl, authorization, hooks = {}) {
  const fetchImpl = hooks.fetchImpl ?? fetch;
  const sleep = hooks.sleep ?? ((ms) => (0, import_promises2.setTimeout)(ms));
  const now = hooks.now ?? (() => Date.now());
  const log = hooks.log ?? ((message) => console.log(message));
  const requestTimeoutMs = normalizeTimeout(hooks.requestTimeoutMs);
  let intervalSeconds = authorization.intervalSeconds;
  const deadline = now() + authorization.expiresInSeconds * 1e3;
  for (; ; ) {
    await sleep(Math.min(intervalSeconds * 1e3, Math.max(0, deadline - now())));
    if (now() >= deadline) {
      throw deviceError(
        "Device login expired before it was approved. Run the command again to get a new code."
      );
    }
    const pollTimeoutMs = clampTimerDelay(Math.min(requestTimeoutMs, deadline - now()));
    let response;
    try {
      response = await fetchImpl(buildApiUrl(apiUrl, "/api/v1/auth/device/token"), {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          grant_type: DEVICE_GRANT_TYPE,
          device_code: authorization.deviceCode
        }),
        signal: AbortSignal.timeout(pollTimeoutMs)
      });
    } catch (error) {
      if (isRequestTimeout(error)) {
        if (now() >= deadline) {
          throw deviceError(
            "Device login expired before it was approved. Run the command again to get a new code."
          );
        }
        log(`No response from ${apiUrl} within ${formatDuration(pollTimeoutMs)}; retrying...`);
        intervalSeconds = clampInterval(intervalSeconds + SLOW_DOWN_INCREMENT_SECONDS);
        continue;
      }
      throw new CloudAuthError(
        "AUTH_DEVICE_FLOW_FAILED",
        `Lost connection to ${apiUrl} while waiting for approval`,
        { cause: error }
      );
    }
    const payload = await response.json().catch(() => null);
    if (response.ok) {
      if (!payload?.access_token || !payload.refresh_token || !payload.access_token_expires_at) {
        throw deviceError("Device login response was missing required fields");
      }
      return {
        accessToken: payload.access_token,
        refreshToken: payload.refresh_token,
        accessTokenExpiresAt: payload.access_token_expires_at,
        ...payload.refresh_token_expires_at ? { refreshTokenExpiresAt: payload.refresh_token_expires_at } : {},
        apiUrl: payload.api_url?.trim() || apiUrl
      };
    }
    if (response.status === 429) {
      const retryAfter = Number(response.headers.get("retry-after"));
      intervalSeconds = clampInterval(
        Number.isFinite(retryAfter) && retryAfter > 0 ? retryAfter : intervalSeconds + SLOW_DOWN_INCREMENT_SECONDS
      );
      continue;
    }
    if (response.status >= 500) {
      intervalSeconds = clampInterval(intervalSeconds + SLOW_DOWN_INCREMENT_SECONDS);
      continue;
    }
    switch (payload?.error) {
      case "authorization_pending":
        if (payload.interval) {
          intervalSeconds = Math.max(intervalSeconds, clampInterval(payload.interval));
        }
        continue;
      case "slow_down":
        intervalSeconds = clampInterval(
          Math.max(intervalSeconds + SLOW_DOWN_INCREMENT_SECONDS, payload.interval ?? 0)
        );
        continue;
      case "access_denied":
        throw deviceError("Device login was denied. No credentials were issued.");
      case "expired_token":
        throw deviceError(
          "Device login expired before it was approved. Run the command again to get a new code."
        );
      case "invalid_grant":
        throw deviceError("This device code is no longer valid. Run the command again to get a new code.");
      default: {
        const detail = payload?.error_description || payload?.error || `HTTP ${response.status}`;
        throw deviceError(`Device login failed: ${detail}`);
      }
    }
  }
}
function formatDeviceInstructions(authorization) {
  return [
    "",
    "To authorize this machine, visit:",
    `  ${authorization.verificationUri}`,
    "",
    "and enter code:",
    `  ${authorization.userCode}`,
    "",
    ...authorization.verificationUriComplete ? [`Or open this link directly:`, `  ${authorization.verificationUriComplete}`, ""] : []
  ].join("\n");
}
async function runDeviceAuthorizationFlow(apiUrl, options = {}) {
  const log = options.log ?? ((message) => console.log(message));
  const authorization = await startDeviceAuthorization(apiUrl, {
    ...options.clientName ? { clientName: options.clientName } : {},
    ...options.fetchImpl ? { fetchImpl: options.fetchImpl } : {},
    ...options.requestTimeoutMs !== void 0 ? { requestTimeoutMs: options.requestTimeoutMs } : {}
  });
  log(formatDeviceInstructions(authorization));
  log("Waiting for authorization...");
  const auth = await pollForDeviceToken(apiUrl, authorization, options);
  return auth;
}

// ../relay/packages/cloud/src/auth.ts
var AUTH_DIR_PATH = import_node_path3.default.dirname(AUTH_FILE_PATH);
var AUTH_LOCK_PATH = `${AUTH_FILE_PATH}.lock`;
var AUTH_LOCK_RETRY_DELAY_MS = 50;
var AUTH_LOCK_STALE_MS = 3e4;
var AUTH_LOCK_TIMEOUT_MS = 3e4;
var envBackedAuth = /* @__PURE__ */ new WeakSet();
function markEnvBackedAuth(auth) {
  envBackedAuth.add(auth);
  return auth;
}
function isEnvBackedAuth(auth) {
  return envBackedAuth.has(auth);
}
function readEnvAuth(env = process.env) {
  const apiUrl = env.CLOUD_API_URL?.trim();
  const accessToken = env.CLOUD_API_ACCESS_TOKEN?.trim();
  const refreshToken = env.CLOUD_API_REFRESH_TOKEN?.trim();
  const accessTokenExpiresAt = env.CLOUD_API_ACCESS_TOKEN_EXPIRES_AT?.trim();
  const refreshTokenExpiresAt = env.CLOUD_API_REFRESH_TOKEN_EXPIRES_AT?.trim();
  if (!apiUrl || !accessToken || !refreshToken || !accessTokenExpiresAt) {
    return null;
  }
  try {
    new URL(apiUrl);
  } catch {
    return null;
  }
  if (Number.isNaN(Date.parse(accessTokenExpiresAt))) {
    return null;
  }
  return markEnvBackedAuth({
    apiUrl,
    accessToken,
    refreshToken,
    accessTokenExpiresAt,
    ...refreshTokenExpiresAt && !Number.isNaN(Date.parse(refreshTokenExpiresAt)) ? { refreshTokenExpiresAt } : {}
  });
}
function toEnvAuthRefreshError(error) {
  if (error instanceof CloudAuthError && error.code === "AUTH_REFRESH_TIMEOUT") {
    return error;
  }
  const message = error instanceof Error && error.message ? `${error.message}. ` : "";
  return new CloudAuthError(
    "AUTH_ENV_REPROVISION_REQUIRED",
    `${message}Env-backed cloud auth could not be refreshed interactively; re-provision CLOUD_API_URL, CLOUD_API_ACCESS_TOKEN, CLOUD_API_REFRESH_TOKEN, CLOUD_API_ACCESS_TOKEN_EXPIRES_AT, and optionally CLOUD_API_REFRESH_TOKEN_EXPIRES_AT.`,
    { cause: error }
  );
}
function isValidStoredAuth(value) {
  if (!value || typeof value !== "object") {
    return false;
  }
  const auth = value;
  return typeof auth.accessToken === "string" && typeof auth.refreshToken === "string" && typeof auth.accessTokenExpiresAt === "string" && typeof auth.apiUrl === "string" && (auth.refreshTokenExpiresAt === void 0 || typeof auth.refreshTokenExpiresAt === "string") && !Number.isNaN(Date.parse(auth.accessTokenExpiresAt)) && (auth.refreshTokenExpiresAt === void 0 || !Number.isNaN(Date.parse(auth.refreshTokenExpiresAt)));
}
function isNodeErrorWithCode(error, code) {
  return typeof error === "object" && error !== null && "code" in error && error.code === code;
}
async function readCanonicalStoredAuth() {
  try {
    const file = await import_promises3.default.readFile(AUTH_FILE_PATH, "utf8");
    const parsed = JSON.parse(file);
    return isValidStoredAuth(parsed) ? parsed : null;
  } catch {
    return null;
  }
}
async function readStoredAuth(env = process.env) {
  const envAuth = readEnvAuth(env);
  if (envAuth) {
    return envAuth;
  }
  return readCanonicalStoredAuth();
}
async function writeStoredAuth(auth) {
  await import_promises3.default.mkdir(AUTH_DIR_PATH, {
    recursive: true,
    mode: 448
  });
  const temporaryPath = import_node_path3.default.join(
    AUTH_DIR_PATH,
    `.${import_node_path3.default.basename(AUTH_FILE_PATH)}.${process.pid}.${Date.now()}.${(0, import_node_crypto.randomUUID)()}.tmp`
  );
  try {
    await import_promises3.default.writeFile(temporaryPath, `${JSON.stringify(auth, null, 2)}
`, {
      encoding: "utf8",
      mode: 384
    });
    await import_promises3.default.chmod(temporaryPath, 384);
    await import_promises3.default.rename(temporaryPath, AUTH_FILE_PATH);
  } finally {
    await import_promises3.default.rm(temporaryPath, { force: true });
  }
}
async function refreshStoredCloudIdentity(auth, options = {}) {
  const env = options.env ?? process.env;
  try {
    const { response } = await authorizedApiFetch(
      auth,
      "/api/v1/auth/whoami",
      { method: "GET" },
      { interactive: false }
    );
    if (!response.ok) return null;
    const payload = await response.json().catch(() => null);
    if (!payload?.authenticated || !payload.user?.id) return null;
    const identity = toCloudIdentity(payload, auth.apiUrl);
    if (!identity) return null;
    await writeStoredIdentity(identity, env);
    return identity;
  } catch {
    return null;
  }
}
function toCloudIdentity(payload, apiUrl) {
  if (!payload.user?.id) return null;
  return {
    userId: payload.user.id,
    ...payload.user.email ? { email: payload.user.email } : {},
    ...payload.user.name ? { name: payload.user.name } : {},
    ...payload.currentOrganization ? {
      organizationId: payload.currentOrganization.id,
      organizationSlug: payload.currentOrganization.slug,
      organizationName: payload.currentOrganization.name,
      organizationRole: payload.currentOrganization.role
    } : {},
    ...payload.currentWorkspace ? { workspaceId: payload.currentWorkspace.id } : {},
    apiUrl,
    updatedAt: (/* @__PURE__ */ new Date()).toISOString()
  };
}
async function removeStaleStoredAuthLock() {
  try {
    const lockStats = await import_promises3.default.stat(AUTH_LOCK_PATH);
    if (Date.now() - lockStats.mtimeMs < AUTH_LOCK_STALE_MS) {
      return false;
    }
  } catch (error) {
    if (isNodeErrorWithCode(error, "ENOENT")) {
      return true;
    }
    throw error;
  }
  await import_promises3.default.rm(AUTH_LOCK_PATH, { recursive: true, force: true });
  return true;
}
async function acquireStoredAuthLock(signal) {
  const startedAt = Date.now();
  while (true) {
    if (signal?.aborted) {
      throw signal.reason ?? new Error("Cloud auth lock acquisition aborted");
    }
    try {
      await import_promises3.default.mkdir(AUTH_LOCK_PATH, { mode: 448 });
      return;
    } catch (error) {
      if (!isNodeErrorWithCode(error, "EEXIST")) {
        throw error;
      }
    }
    if (await removeStaleStoredAuthLock()) {
      continue;
    }
    if (Date.now() - startedAt >= AUTH_LOCK_TIMEOUT_MS) {
      throw new Error(`Timed out waiting for cloud auth lock at ${AUTH_LOCK_PATH}`);
    }
    await (0, import_promises4.setTimeout)(AUTH_LOCK_RETRY_DELAY_MS, void 0, { signal });
  }
}
async function withStoredAuthLock(callback, options = {}) {
  await import_promises3.default.mkdir(AUTH_DIR_PATH, {
    recursive: true,
    mode: 448
  });
  await acquireStoredAuthLock(options.signal);
  try {
    return await callback();
  } finally {
    await import_promises3.default.rm(AUTH_LOCK_PATH, { recursive: true, force: true });
  }
}
function shouldRefresh(accessTokenExpiresAt) {
  const expiresAt = Date.parse(accessTokenExpiresAt);
  if (Number.isNaN(expiresAt)) {
    return true;
  }
  return expiresAt - Date.now() <= REFRESH_WINDOW_MS;
}
function shouldRefreshStoredAuth(auth) {
  if (shouldRefresh(auth.accessTokenExpiresAt)) {
    return true;
  }
  if (!auth.refreshTokenExpiresAt) {
    return false;
  }
  const refreshExpiresAt = Date.parse(auth.refreshTokenExpiresAt);
  if (Number.isNaN(refreshExpiresAt)) {
    return true;
  }
  return refreshExpiresAt - Date.now() <= REFRESH_TOKEN_WINDOW_MS;
}
function openBrowser(url) {
  const platform = import_node_os4.default.platform();
  if (platform === "darwin") {
    return (0, import_node_child_process.spawn)("open", [url], { stdio: "ignore", detached: true });
  }
  if (platform === "win32") {
    return (0, import_node_child_process.spawn)("cmd", ["/c", "start", "", url], { stdio: "ignore", detached: true });
  }
  return (0, import_node_child_process.spawn)("xdg-open", [url], { stdio: "ignore", detached: true });
}
function browserRequired(message) {
  return new CloudAuthError("AUTH_BROWSER_REQUIRED", message);
}
function refreshExpired(message = "Stored cloud login has expired") {
  return new CloudAuthError("AUTH_REFRESH_EXPIRED", message);
}
function isAbortLikeError(error) {
  return error instanceof Error && (error.name === "AbortError" || error.name === "TimeoutError" || /aborted/i.test(error.message));
}
function addAbortListener(signal, listener) {
  signal.addEventListener("abort", listener, { once: true });
  return () => signal.removeEventListener("abort", listener);
}
async function fetchWithRefreshTimeout(url, init, options = {}) {
  const refreshTimeoutMs = options.refreshTimeoutMs ?? DEFAULT_REFRESH_TIMEOUT_MS;
  const controller = new AbortController();
  const removers = [];
  let timedOut = false;
  let callerAborted = false;
  const abortFromCaller = () => {
    callerAborted = true;
    controller.abort();
  };
  for (const signal of [options.signal, init.signal]) {
    if (!signal) {
      continue;
    }
    if (signal.aborted) {
      callerAborted = true;
      controller.abort();
      break;
    }
    removers.push(addAbortListener(signal, abortFromCaller));
  }
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, refreshTimeoutMs);
  try {
    return await fetch(url, {
      ...init,
      signal: controller.signal
    });
  } catch (error) {
    if (timedOut || !callerAborted && isAbortLikeError(error)) {
      throw new CloudAuthError(
        "AUTH_REFRESH_TIMEOUT",
        `Cloud auth refresh timed out after ${refreshTimeoutMs}ms`,
        { cause: error }
      );
    }
    throw error;
  } finally {
    clearTimeout(timer);
    for (const remove of removers) {
      remove();
    }
  }
}
function redirectToHostedCliAuthPage(response, apiUrl, options) {
  const resultUrl = buildApiUrl(apiUrl, "/cli/auth-result");
  resultUrl.searchParams.set("status", options.status);
  if (options.detail) {
    resultUrl.searchParams.set("detail", options.detail);
  }
  response.statusCode = 302;
  response.setHeader("location", resultUrl.toString());
  response.end();
}
async function beginBrowserLogin(apiUrl) {
  const state = (0, import_node_crypto.randomUUID)();
  return new Promise((resolve, reject) => {
    let settled = false;
    const server = import_node_http.default.createServer((request, response) => {
      const requestUrl = new URL(request.url || "/", "http://127.0.0.1");
      if (requestUrl.pathname !== "/callback") {
        response.statusCode = 404;
        response.end("Not found");
        return;
      }
      const returnedState = requestUrl.searchParams.get("state");
      if (returnedState !== state) {
        response.statusCode = 400;
        response.setHeader("content-type", "text/plain; charset=utf-8");
        response.end("Ignored invalid CLI login callback. Return to your terminal to continue login.");
        return;
      }
      const error = requestUrl.searchParams.get("error");
      if (error) {
        redirectToHostedCliAuthPage(response, apiUrl, {
          status: "error",
          detail: error
        });
        if (!settled) {
          settled = true;
          server.close();
          reject(new Error(error));
        }
        return;
      }
      const accessToken = requestUrl.searchParams.get("access_token");
      const refreshToken = requestUrl.searchParams.get("refresh_token");
      const accessTokenExpiresAt = requestUrl.searchParams.get("access_token_expires_at");
      const refreshTokenExpiresAt = requestUrl.searchParams.get("refresh_token_expires_at");
      const returnedApiUrl = requestUrl.searchParams.get("api_url");
      if (!accessToken || !refreshToken || !accessTokenExpiresAt || !returnedApiUrl) {
        redirectToHostedCliAuthPage(response, apiUrl, {
          status: "error",
          detail: "Expected access token, refresh token, API URL, and expiration timestamp."
        });
        if (!settled) {
          settled = true;
          server.close();
          reject(new Error("CLI login callback was missing required fields"));
        }
        return;
      }
      redirectToHostedCliAuthPage(response, returnedApiUrl, {
        status: "success",
        detail: `API endpoint: ${returnedApiUrl}`
      });
      if (!settled) {
        settled = true;
        server.close();
        resolve({
          accessToken,
          refreshToken,
          accessTokenExpiresAt,
          ...refreshTokenExpiresAt ? { refreshTokenExpiresAt } : {},
          apiUrl: returnedApiUrl
        });
      }
    });
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") {
        if (!settled) {
          settled = true;
          server.close();
          reject(new Error("Failed to start local callback server"));
        }
        return;
      }
      const callbackUrl = new URL("/callback", `http://127.0.0.1:${address.port}`);
      const loginUrl = buildApiUrl(apiUrl, "/api/v1/cli/login");
      loginUrl.searchParams.set("redirect_uri", callbackUrl.toString());
      loginUrl.searchParams.set("state", state);
      console.log(`Opening browser for cloud login: ${loginUrl.toString()}`);
      console.log("If the browser does not open, paste this URL into your browser.");
      try {
        const child = openBrowser(loginUrl.toString());
        child.unref();
      } catch {
      }
    });
    server.on("error", (error) => {
      if (!settled) {
        settled = true;
        reject(error);
      }
    });
    setTimeout(() => {
      if (!settled) {
        settled = true;
        server.close();
        reject(new Error("Timed out waiting for browser login"));
      }
    }, 5 * 6e4).unref();
  });
}
async function refreshStoredAuth(auth, options = {}) {
  if (isEnvBackedAuth(auth)) {
    return markEnvBackedAuth(await requestStoredAuthRefresh(auth, options));
  }
  return withStoredAuthLock(async () => {
    const latestAuth = await readCanonicalStoredAuth();
    const refreshSource = latestAuth?.apiUrl === auth.apiUrl ? latestAuth : auth;
    if (!options.force && latestAuth?.apiUrl === auth.apiUrl && !shouldRefreshStoredAuth(latestAuth)) {
      return latestAuth;
    }
    const nextAuth = await requestStoredAuthRefresh(refreshSource, options);
    await writeStoredAuth(nextAuth);
    return nextAuth;
  }, options);
}
async function requestStoredAuthRefresh(auth, options = {}) {
  const response = await fetchWithRefreshTimeout(
    buildApiUrl(auth.apiUrl, "/api/v1/auth/token/refresh"),
    {
      method: "POST",
      headers: {
        "content-type": "application/json"
      },
      body: JSON.stringify({ refreshToken: auth.refreshToken })
    },
    options
  );
  const payload = await response.json().catch(() => null);
  if (!response.ok || !payload?.accessToken || !payload?.refreshToken || !payload?.accessTokenExpiresAt) {
    throw refreshExpired();
  }
  const nextRefreshTokenExpiresAt = typeof payload.refreshTokenExpiresAt === "string" && payload.refreshTokenExpiresAt.trim() ? payload.refreshTokenExpiresAt.trim() : auth.refreshTokenExpiresAt;
  const nextAuth = {
    apiUrl: typeof payload.apiUrl === "string" && payload.apiUrl.trim() ? payload.apiUrl.trim() : auth.apiUrl,
    accessToken: payload.accessToken,
    refreshToken: payload.refreshToken,
    accessTokenExpiresAt: payload.accessTokenExpiresAt,
    ...nextRefreshTokenExpiresAt ? { refreshTokenExpiresAt: nextRefreshTokenExpiresAt } : {}
  };
  return nextAuth;
}
async function completeLogin(auth) {
  await writeStoredAuth(auth);
  const identity = await refreshStoredCloudIdentity(auth);
  console.log(`Logged in to ${auth.apiUrl}`);
  if (identity?.email) {
    const org = identity.organizationName ?? identity.organizationSlug;
    console.log(`Signed in as ${identity.email}${org ? ` (${org})` : ""}`);
  }
  return auth;
}
async function loginWithBrowser(apiUrl) {
  return completeLogin(await beginBrowserLogin(apiUrl));
}
async function loginWithDevice(apiUrl, options = {}) {
  return completeLogin(await runDeviceAuthorizationFlow(apiUrl, options));
}
async function loginInteractive(apiUrl, options = {}) {
  const env = options.env ?? process.env;
  if (options.device === true || isHeadlessEnvironment(env)) {
    return loginWithDevice(apiUrl);
  }
  return loginWithBrowser(apiUrl);
}
async function ensureCloudSession(options = {}) {
  const env = options.env ?? process.env;
  const apiUrl = options.apiUrl || env.CLOUD_API_URL?.trim() || defaultApiUrl();
  const force = options.force === true;
  const interactive = options.interactive !== false;
  const refreshTimeoutMs = options.refreshTimeoutMs;
  const stored = !force ? await readStoredAuth(env) : null;
  if (!stored) {
    if (!interactive) {
      throw browserRequired(
        isHeadlessEnvironment(env) ? "Cloud login required. Run `agent-relay cloud login --device`." : "Cloud login required. Run `agent-relay login`."
      );
    }
    const auth = await loginInteractive(apiUrl, { device: options.device, env });
    return createCloudSession(auth, { refreshTimeoutMs });
  }
  if (!shouldRefreshStoredAuth(stored)) {
    return createCloudSession(stored, { refreshTimeoutMs });
  }
  try {
    const auth = await refreshStoredAuth(stored, { refreshTimeoutMs });
    return createCloudSession(auth, { refreshTimeoutMs });
  } catch (error) {
    if (isEnvBackedAuth(stored)) {
      throw toEnvAuthRefreshError(error);
    }
    if (!interactive) {
      throw error;
    }
    const auth = await loginInteractive(stored.apiUrl, { device: options.device, env });
    return createCloudSession(auth, { refreshTimeoutMs });
  }
}
function createCloudSession(auth, options = {}) {
  const clientOptions = {
    ...auth,
    refreshTimeoutMs: options.refreshTimeoutMs
  };
  if (!isEnvBackedAuth(auth)) {
    clientOptions.refreshAuth = async (snapshot, refreshOptions) => toCloudApiClientSnapshot(
      await refreshStoredAuth(toStoredAuth(snapshot), {
        force: refreshOptions.force,
        refreshTimeoutMs: options.refreshTimeoutMs,
        signal: refreshOptions.signal
      })
    );
  }
  const client = new CloudApiClient(clientOptions);
  return { auth, client };
}
function toStoredAuth(snapshot) {
  return {
    apiUrl: snapshot.apiUrl,
    accessToken: snapshot.accessToken,
    refreshToken: snapshot.refreshToken,
    accessTokenExpiresAt: snapshot.accessTokenExpiresAt,
    ...snapshot.refreshTokenExpiresAt ? { refreshTokenExpiresAt: snapshot.refreshTokenExpiresAt } : {}
  };
}
function toCloudApiClientSnapshot(auth) {
  return auth;
}
function apiFetch(apiUrl, accessToken, requestPath, init) {
  const headers = new Headers(init.headers);
  if (!headers.has("content-type")) {
    headers.set("content-type", "application/json");
  }
  headers.set("authorization", `Bearer ${accessToken}`);
  appendAgentRelayTelemetryHeaders(headers);
  return fetch(buildApiUrl(apiUrl, requestPath), {
    ...init,
    headers
  });
}
async function authorizedApiFetch(auth, requestPath, init, options = {}) {
  let activeAuth = auth;
  let response = await apiFetch(activeAuth.apiUrl, activeAuth.accessToken, requestPath, init);
  if (response.status !== 401) {
    return { response, auth: activeAuth };
  }
  try {
    activeAuth = await refreshStoredAuth(activeAuth, {
      force: true,
      refreshTimeoutMs: options.refreshTimeoutMs,
      signal: init.signal ?? void 0
    });
  } catch (error) {
    if (isEnvBackedAuth(activeAuth)) {
      throw toEnvAuthRefreshError(error);
    }
    if (options.interactive === false) {
      throw error;
    }
    if (init.signal?.aborted) {
      throw init.signal.reason ?? new Error("Cloud request aborted before re-authentication");
    }
    activeAuth = await loginInteractive(activeAuth.apiUrl, {
      device: options.device,
      env: options.env
    });
  }
  response = await apiFetch(activeAuth.apiUrl, activeAuth.accessToken, requestPath, init);
  return { response, auth: activeAuth };
}
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  ensureCloudSession
});
