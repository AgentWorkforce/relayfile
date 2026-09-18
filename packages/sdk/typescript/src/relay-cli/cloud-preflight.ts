/**
 * The one implementation of relayfile's Cloud sign-in preflight.
 *
 * Agent Relay's Cloud SDK owns interactive login, token refresh, locking, and
 * the canonical session store. The native runtime reads that same store
 * directly instead of receiving copied tokens, so this preflight only has to
 * ensure a session exists before `relayfile setup` starts.
 *
 * Both entry points into the Go binary run it: the `relayfile` bin shim
 * (`packages/cli/scripts/run.js`) and the `agent-relay file` CLI surface, so
 * `agent-relay file setup` behaves identically to `relayfile setup`.
 */

import { createRequire } from "node:module"
import { existsSync } from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

export const DEFAULT_CLOUD_API_URL = "https://agentrelay.com/cloud"
export const SETUP_INTENT =
  "Relayfile setup. This signs you in, connects an integration, and prepares a local VFS mount."
export const SETUP_INTENT_PRINTED_ENV = "RELAYFILE_NPM_SETUP_INTENT_PRINTED"

const DEFAULT_LOGIN_TIMEOUT_MS = 5 * 60 * 1000
const DEFAULT_REFRESH_TIMEOUT_MS = 10 * 1000
const MAX_NODE_TIMER_DELAY_MS = 2_147_483_647
const MAX_GO_DURATION_NANOSECONDS = 9_223_372_036_854_775_807

/** Flags `relayfile setup` accepts, mapped to whether they take a value. */
const SETUP_FLAGS = new Map<string, boolean>([
  ["cloud-api-url", true],
  ["cloud-token", true],
  ["workspace", true],
  ["provider", true],
  ["backend", true],
  ["local-dir", true],
  ["no-open", false],
  ["skip-mount", false],
  ["once", false],
  ["login-timeout", true],
  ["connect-timeout", true],
  ["help", false],
  ["h", false]
])

const GO_BOOLEAN_VALUES = new Set([
  "1",
  "t",
  "T",
  "true",
  "TRUE",
  "True",
  "0",
  "f",
  "F",
  "false",
  "FALSE",
  "False"
])
const GO_TRUE_VALUES = new Set(["1", "t", "T", "true", "TRUE", "True"])
const GO_DURATION_UNITS_IN_NANOSECONDS = new Map<string, number>([
  ["ns", 1],
  ["us", 1_000],
  ["µs", 1_000],
  ["μs", 1_000],
  ["ms", 1_000_000],
  ["s", 1_000_000_000],
  ["m", 60 * 1_000_000_000],
  ["h", 60 * 60 * 1_000_000_000]
])
const VALID_INTEGRATION_BACKENDS = new Set(["", "default", "nango", "composio"])

export type SetupFlagValue = string | boolean

export type ParsedSetupArguments =
  | { valid: true; values: Map<string, SetupFlagValue>; durations: Map<string, number> }
  | { valid: false; error: string }

export interface EnsureCloudSessionOptions {
  apiUrl: string
  client: string
  interactive: boolean
  device: boolean
  loginTimeoutMs: number
  refreshTimeoutMs: number
  signal: AbortSignal
}

export type EnsureCloudSession = (options: EnsureCloudSessionOptions) => Promise<unknown>

export interface CloudPreflightOptions {
  /** Defaults to `process.env`. Mutated to record the printed intent. */
  env?: NodeJS.ProcessEnv
  /** Where the setup intent line is written. Defaults to `console.log`. */
  writeLine?: (line: string) => void
  /** Injected Cloud SDK entry point; loaded from the vendored bundle by default. */
  ensureCloudSession?: EnsureCloudSession
  /** Explicit path to the vendored `cloud-auth.cjs` bundle. */
  cloudAuthBundlePath?: string
}

/**
 * Parse a Go `time.Duration` string the way the Go CLI does.
 *
 * @param value - A duration such as `5m` or `1h30m`.
 * @returns Milliseconds, or null when the string is not a valid duration.
 */
export function parseGoDurationMilliseconds(value: string): number | null {
  let remaining = String(value)
  let sign = 1
  if (remaining.startsWith("+") || remaining.startsWith("-")) {
    sign = remaining[0] === "-" ? -1 : 1
    remaining = remaining.slice(1)
  }
  if (remaining === "0") {
    return 0
  }
  if (!remaining) {
    return null
  }

  let nanoseconds = 0
  let parts = 0
  while (remaining) {
    const match = /^(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/.exec(remaining)
    if (!match) {
      return null
    }
    const amount = Number(match[1])
    const unitNanoseconds = GO_DURATION_UNITS_IN_NANOSECONDS.get(match[2]!)!
    nanoseconds += amount * unitNanoseconds
    if (!Number.isFinite(nanoseconds) || nanoseconds > MAX_GO_DURATION_NANOSECONDS) {
      return null
    }
    remaining = remaining.slice(match[0].length)
    parts += 1
  }
  return parts > 0 ? (sign * nanoseconds) / 1_000_000 : null
}

function wantsNativeVersion(args: readonly string[]): boolean {
  return args.length === 1 && (args[0] === "--version" || args[0] === "version")
}

function wantsNativeHelp(args: readonly string[]): boolean {
  return args.some((arg) => arg === "--help" || arg === "-h")
}

/**
 * Parse `relayfile setup`'s arguments without running it.
 *
 * @param args - Argv, with or without a leading `setup`.
 * @returns The parsed flags, or the first validation error.
 */
export function parseSetupArguments(args: readonly string[]): ParsedSetupArguments {
  const setupArgs = args[0] === "setup" ? args.slice(1) : args
  const values = new Map<string, SetupFlagValue>()
  const durations = new Map<string, number>()
  for (let index = 0; index < setupArgs.length; index += 1) {
    const arg = setupArgs[index]!
    if (arg === "--") {
      if (index !== setupArgs.length - 1) {
        return { valid: false, error: "setup does not accept positional arguments" }
      }
      break
    }

    const match = /^--?([^=]+)(?:=(.*))?$/.exec(arg)
    if (!match) {
      return { valid: false, error: `unexpected setup argument ${JSON.stringify(arg)}` }
    }
    const name = match[1]!
    const inlineValue = match[2]
    const takesValue = SETUP_FLAGS.get(name)
    if (takesValue === undefined) {
      return { valid: false, error: `unknown setup flag --${name}` }
    }
    if (takesValue) {
      let value = inlineValue
      if (inlineValue === undefined) {
        const next = setupArgs[index + 1]
        if (next === undefined) {
          return { valid: false, error: `--${name} requires a value` }
        }
        value = next
        index += 1
      }
      values.set(name, value!)
      if (name === "login-timeout" || name === "connect-timeout") {
        const duration = parseGoDurationMilliseconds(value!)
        if (duration === null) {
          return {
            valid: false,
            error: `--${name} has invalid duration ${JSON.stringify(value)}`
          }
        }
        if (name === "login-timeout" && duration <= 0) {
          return { valid: false, error: "--login-timeout must be greater than zero" }
        }
        durations.set(name, duration)
      }
      continue
    }
    if (inlineValue !== undefined && !GO_BOOLEAN_VALUES.has(inlineValue)) {
      return {
        valid: false,
        error: `--${name} has invalid boolean value ${JSON.stringify(inlineValue)}`
      }
    }
    values.set(name, inlineValue === undefined || GO_TRUE_VALUES.has(inlineValue))
  }

  const backend = String(values.get("backend") || "")
    .trim()
    .toLowerCase()
  if (!VALID_INTEGRATION_BACKENDS.has(backend)) {
    return {
      valid: false,
      error: `unsupported integration backend ${JSON.stringify(
        backend
      )} (expected nango or composio)`
    }
  }

  return { valid: true, values, durations }
}

/**
 * Report whether the given argv would parse as a valid `setup` invocation.
 *
 * @param args - Argv, with or without a leading `setup`.
 * @returns True when the arguments are valid.
 */
export function hasValidSetupArguments(args: readonly string[]): boolean {
  return parseSetupArguments(args).valid
}

/**
 * Decide whether a Cloud session must be prepared before the binary runs.
 *
 * @param args - Argv passed to relayfile.
 * @param env - Environment to read caller-owned credentials from.
 * @returns True only for a real interactive `setup` with no explicit credentials.
 */
export function shouldPrepareCloudSession(
  args: readonly string[],
  env: NodeJS.ProcessEnv
): boolean {
  const setupCommand = args.length === 0 || args[0] === "setup"
  if (!setupCommand) {
    return false
  }
  if (wantsNativeVersion(args) || wantsNativeHelp(args)) {
    return false
  }
  const parsed = parseSetupArguments(args)
  if (!parsed.valid) {
    return false
  }
  if (parsed.values.has("help") || parsed.values.has("h")) {
    return false
  }
  // Explicit credentials are caller-owned. Let the Go CLI validate and use
  // them without replacing them with an interactive session.
  const relayfileCloudToken = parsed.values.has("cloud-token")
    ? String(parsed.values.get("cloud-token") || "").trim()
    : String(env.RELAYFILE_CLOUD_TOKEN || "").trim()
  if (relayfileCloudToken || String(env.CLOUD_API_ACCESS_TOKEN || "").trim()) {
    return false
  }
  // Let the native CLI report malformed flags without first opening a login
  // flow or mutating the caller's canonical Cloud session.
  return true
}

/**
 * Print the one-time setup intent line, if this invocation warrants it.
 *
 * @param args - Argv passed to relayfile.
 * @param env - Environment; the printed marker is recorded here.
 * @param writeLine - Sink for the line. Defaults to `console.log`.
 * @returns True when this invocation prepares a Cloud session.
 */
export function announceSetupIntent(
  args: readonly string[],
  env: NodeJS.ProcessEnv,
  writeLine: (line: string) => void = console.log
): boolean {
  if (!shouldPrepareCloudSession(args, env)) {
    return false
  }
  if (String(env[SETUP_INTENT_PRINTED_ENV] || "").trim() !== "1") {
    writeLine(SETUP_INTENT)
    env[SETUP_INTENT_PRINTED_ENV] = "1"
  }
  return true
}

function bundleCandidates(explicitPath?: string): string[] {
  const candidates: string[] = []
  if (explicitPath) {
    candidates.push(explicitPath)
  }
  try {
    const require = createRequire(import.meta.url)
    candidates.push(require.resolve("relayfile/scripts/cloud-auth.cjs"))
  } catch {
    // The CLI package is not installed alongside the SDK; fall through to the
    // workspace layout below.
  }
  let current = path.dirname(fileURLToPath(import.meta.url))
  for (;;) {
    candidates.push(
      path.join(current, "packages", "cli", "scripts", "cloud-auth.cjs")
    )
    const parent = path.dirname(current)
    if (parent === current) {
      break
    }
    current = parent
  }
  return candidates
}

/**
 * Load `ensureCloudSession` from the vendored Agent Relay Cloud SDK bundle.
 *
 * @param explicitPath - A known bundle path, preferred when supplied.
 * @returns The Cloud SDK's session entry point.
 * @throws When the bundle cannot be found or loaded.
 */
export function loadCloudSessionSDK(explicitPath?: string): EnsureCloudSession {
  const require = createRequire(import.meta.url)
  const candidates = bundleCandidates(explicitPath)
  for (const candidate of candidates) {
    if (!existsSync(candidate)) {
      continue
    }
    try {
      return require(candidate).ensureCloudSession as EnsureCloudSession
    } catch (error) {
      throw new Error(
        "Relayfile's Agent Relay Cloud SDK bundle failed to load. Reinstall relayfile or run its package build.",
        { cause: error }
      )
    }
  }
  throw new Error(
    "Relayfile's Agent Relay Cloud SDK bundle is missing. Reinstall relayfile or run its package build."
  )
}

/**
 * Ensure a Cloud session exists before `relayfile setup` starts.
 *
 * @param args - Argv passed to relayfile.
 * @param options - Environment, output sink, and injectable Cloud SDK.
 * @returns True when a session was prepared, false when the invocation needs none.
 * @throws When setup's arguments are invalid, or sign-in fails or times out.
 */
export async function prepareCloudSession(
  args: readonly string[],
  options: CloudPreflightOptions = {}
): Promise<boolean> {
  const env = options.env ?? process.env
  const setupCommand = args.length === 0 || args[0] === "setup"
  if (wantsNativeVersion(args) || wantsNativeHelp(args)) {
    return false
  }
  const parsed = setupCommand ? parseSetupArguments(args) : null
  if (parsed !== null && parsed.valid === false) {
    throw new Error(parsed.error)
  }
  if (!shouldPrepareCloudSession(args, env)) {
    return false
  }
  // shouldPrepareCloudSession only returns true for a setup invocation, so a
  // valid parse is guaranteed here.
  const setup = parsed as Extract<ParsedSetupArguments, { valid: true }>

  const ensureCloudSession =
    options.ensureCloudSession ?? loadCloudSessionSDK(options.cloudAuthBundlePath)
  const apiUrl =
    String(setup.values.get("cloud-api-url") || "").trim() ||
    String(env.RELAYFILE_CLOUD_API_URL || "").trim() ||
    String(env.CLOUD_API_URL || "").trim() ||
    DEFAULT_CLOUD_API_URL
  const loginTimeoutMs = setup.durations.get("login-timeout") || DEFAULT_LOGIN_TIMEOUT_MS
  const loginAbort = new AbortController()
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    await Promise.race([
      ensureCloudSession({
        apiUrl,
        client: "relayfile",
        interactive: true,
        device: setup.values.get("no-open") === true,
        loginTimeoutMs,
        refreshTimeoutMs: Math.max(
          1,
          Math.min(loginTimeoutMs, DEFAULT_REFRESH_TIMEOUT_MS)
        ),
        signal: loginAbort.signal
      }),
      new Promise((_, reject) => {
        timer = setTimeout(
          () => {
            const error = new Error(
              `Cloud sign-in timed out after ${setup.values.get("login-timeout") || "5m"}`
            )
            loginAbort.abort(error)
            reject(error)
          },
          Math.min(loginTimeoutMs, MAX_NODE_TIMER_DELAY_MS)
        )
      })
    ])
  } finally {
    clearTimeout(timer)
  }

  // The SDK owns and refreshes its canonical on-disk session. Do not promote
  // that session into CLOUD_API_* for the child: Relayfile would correctly
  // treat those variables as caller-owned and would not persist rotated
  // refresh tokens back to the shared file. The native runtime reads the same
  // canonical file directly. Genuine caller-provided environment credentials
  // bypass this preflight above and remain untouched.
  return true
}
