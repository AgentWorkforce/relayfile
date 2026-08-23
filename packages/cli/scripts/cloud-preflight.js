"use strict";

const path = require("path");

const DEFAULT_CLOUD_API_URL = "https://agentrelay.com/cloud";
const DEFAULT_LOGIN_TIMEOUT_MS = 5 * 60 * 1000;
const DEFAULT_REFRESH_TIMEOUT_MS = 10 * 1000;
const MAX_NODE_TIMER_DELAY_MS = 2_147_483_647;
const MAX_GO_DURATION_NANOSECONDS = 9_223_372_036_854_775_807;
const SETUP_FLAGS = new Map([
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
  ["h", false],
]);
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
  "False",
]);
const GO_TRUE_VALUES = new Set(["1", "t", "T", "true", "TRUE", "True"]);
const GO_DURATION_UNITS_IN_NANOSECONDS = new Map([
  ["ns", 1],
  ["us", 1_000],
  ["µs", 1_000],
  ["μs", 1_000],
  ["ms", 1_000_000],
  ["s", 1_000_000_000],
  ["m", 60 * 1_000_000_000],
  ["h", 60 * 60 * 1_000_000_000],
]);
const VALID_INTEGRATION_BACKENDS = new Set([
  "",
  "default",
  "nango",
  "composio",
]);

function hasFlag(args, name) {
  return args.some((arg) => arg === name || arg.startsWith(`${name}=`));
}

function parseGoDurationMilliseconds(value) {
  let remaining = String(value);
  let sign = 1;
  if (remaining.startsWith("+") || remaining.startsWith("-")) {
    sign = remaining[0] === "-" ? -1 : 1;
    remaining = remaining.slice(1);
  }
  if (remaining === "0") {
    return 0;
  }
  if (!remaining) {
    return null;
  }

  let nanoseconds = 0;
  let parts = 0;
  while (remaining) {
    const match = /^(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/.exec(
      remaining,
    );
    if (!match) {
      return null;
    }
    const amount = Number(match[1]);
    const unitNanoseconds = GO_DURATION_UNITS_IN_NANOSECONDS.get(match[2]);
    nanoseconds += amount * unitNanoseconds;
    if (
      !Number.isFinite(nanoseconds) ||
      nanoseconds > MAX_GO_DURATION_NANOSECONDS
    ) {
      return null;
    }
    remaining = remaining.slice(match[0].length);
    parts += 1;
  }
  return parts > 0 ? (sign * nanoseconds) / 1_000_000 : null;
}

function parseSetupArguments(args) {
  const setupArgs = args[0] === "setup" ? args.slice(1) : args;
  const values = new Map();
  const durations = new Map();
  for (let index = 0; index < setupArgs.length; index += 1) {
    const arg = setupArgs[index];
    if (arg === "--") {
      if (index !== setupArgs.length - 1) {
        return { valid: false, error: "setup does not accept positional arguments" };
      }
      break;
    }

    const match = /^--?([^=]+)(?:=(.*))?$/.exec(arg);
    if (!match) {
      return {
        valid: false,
        error: `unexpected setup argument ${JSON.stringify(arg)}`,
      };
    }
    const [, name, inlineValue] = match;
    const takesValue = SETUP_FLAGS.get(name);
    if (takesValue === undefined) {
      return { valid: false, error: `unknown setup flag --${name}` };
    }
    if (takesValue) {
      let value = inlineValue;
      if (inlineValue === undefined) {
        const next = setupArgs[index + 1];
        if (next === undefined || next.startsWith("-")) {
          return { valid: false, error: `--${name} requires a value` };
        }
        value = next;
        index += 1;
      }
      values.set(name, value);
      if (name === "login-timeout" || name === "connect-timeout") {
        const duration = parseGoDurationMilliseconds(value);
        if (duration === null) {
          return {
            valid: false,
            error: `--${name} has invalid duration ${JSON.stringify(value)}`,
          };
        }
        if (name === "login-timeout" && duration <= 0) {
          return {
            valid: false,
            error: "--login-timeout must be greater than zero",
          };
        }
        durations.set(name, duration);
      }
      continue;
    }
    if (inlineValue !== undefined && !GO_BOOLEAN_VALUES.has(inlineValue)) {
      return {
        valid: false,
        error: `--${name} has invalid boolean value ${JSON.stringify(inlineValue)}`,
      };
    }
    values.set(name, inlineValue === undefined || GO_TRUE_VALUES.has(inlineValue));
  }

  const backend = String(values.get("backend") || "").trim().toLowerCase();
  if (!VALID_INTEGRATION_BACKENDS.has(backend)) {
    return {
      valid: false,
      error: `unsupported integration backend ${JSON.stringify(backend)} (expected nango or composio)`,
    };
  }

  return { valid: true, values, durations };
}

function hasValidSetupArguments(args) {
  return parseSetupArguments(args).valid;
}

function shouldPrepareCloudSession(args, env) {
  const setupCommand = args.length === 0 || args[0] === "setup";
  if (!setupCommand) {
    return false;
  }
  if (
    hasFlag(args, "--version") ||
    args[0] === "version"
  ) {
    return false;
  }
  const parsed = parseSetupArguments(args);
  if (!parsed.valid) {
    return false;
  }
  if (parsed.values.has("help") || parsed.values.has("h")) {
    return false;
  }
  // Explicit credentials are caller-owned. Let the Go CLI validate and use
  // them without replacing them with an interactive session.
  if (
    parsed.values.has("cloud-token") ||
    String(env.RELAYFILE_CLOUD_TOKEN || "").trim() ||
    String(env.CLOUD_API_ACCESS_TOKEN || "").trim()
  ) {
    return false;
  }
  // Let the native CLI report malformed flags without first opening a login
  // flow or mutating the caller's canonical Cloud session.
  return true;
}

function loadCloudSessionSDK() {
  const bundlePath = path.join(__dirname, "cloud-auth.cjs");
  try {
    return require(bundlePath).ensureCloudSession;
  } catch (error) {
    throw new Error(
      "Relayfile's Agent Relay Cloud SDK bundle is missing. Reinstall relayfile or run its package build.",
      { cause: error },
    );
  }
}

async function prepareCloudSession(args, env = process.env, dependencies = {}) {
  const setupCommand = args.length === 0 || args[0] === "setup";
  const skipsValidation =
    hasFlag(args, "--help") ||
    hasFlag(args, "-h") ||
    hasFlag(args, "--version") ||
    args[0] === "version";
  const parsed =
    setupCommand && !skipsValidation ? parseSetupArguments(args) : null;
  if (parsed && !parsed.valid) {
    throw new Error(parsed.error);
  }
  if (!shouldPrepareCloudSession(args, env)) {
    return false;
  }

  const ensureCloudSession =
    dependencies.ensureCloudSession || loadCloudSessionSDK();
  const apiUrl =
    String(parsed.values.get("cloud-api-url") || "").trim() ||
    String(env.RELAYFILE_CLOUD_API_URL || "").trim() ||
    String(env.CLOUD_API_URL || "").trim() ||
    DEFAULT_CLOUD_API_URL;
  const loginTimeoutMs =
    parsed.durations.get("login-timeout") || DEFAULT_LOGIN_TIMEOUT_MS;
  let timer;
  try {
    await Promise.race([
      ensureCloudSession({
        apiUrl,
        interactive: true,
        device: parsed.values.get("no-open") === true,
        refreshTimeoutMs: Math.max(
          1,
          Math.min(loginTimeoutMs, DEFAULT_REFRESH_TIMEOUT_MS),
        ),
      }),
      new Promise((_, reject) => {
        timer = setTimeout(
          () =>
            reject(
              new Error(
                `Cloud sign-in timed out after ${parsed.values.get("login-timeout") || "5m"}`,
              ),
            ),
          Math.min(loginTimeoutMs, MAX_NODE_TIMER_DELAY_MS),
        );
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }

  // The SDK owns and refreshes its canonical on-disk session. Do not promote
  // that session into CLOUD_API_* for the child: Relayfile would correctly
  // treat those variables as caller-owned and would not persist rotated
  // refresh tokens back to the shared file. The native runtime reads the same
  // canonical file directly. Genuine caller-provided environment credentials
  // bypass this preflight above and remain untouched.
  return true;
}

module.exports = {
  DEFAULT_CLOUD_API_URL,
  hasValidSetupArguments,
  parseGoDurationMilliseconds,
  parseSetupArguments,
  prepareCloudSession,
  shouldPrepareCloudSession,
};
