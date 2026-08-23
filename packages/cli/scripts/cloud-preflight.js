"use strict";

const path = require("path");

const DEFAULT_CLOUD_API_URL = "https://agentrelay.com/cloud";

function hasFlag(args, name) {
  return args.some((arg) => arg === name || arg.startsWith(`${name}=`));
}

function optionValue(args, name) {
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (arg === name) {
      return args[index + 1];
    }
    if (arg.startsWith(`${name}=`)) {
      return arg.slice(name.length + 1);
    }
  }
  return undefined;
}

function shouldPrepareCloudSession(args, env) {
  const setupCommand = args.length === 0 || args[0] === "setup";
  if (!setupCommand) {
    return false;
  }
  if (
    hasFlag(args, "--help") ||
    hasFlag(args, "-h") ||
    hasFlag(args, "--version") ||
    args[0] === "version"
  ) {
    return false;
  }
  // Explicit credentials are caller-owned. Let the Go CLI validate and use
  // them without replacing them with an interactive session.
  if (
    hasFlag(args, "--cloud-token") ||
    String(env.RELAYFILE_CLOUD_TOKEN || "").trim() ||
    String(env.CLOUD_API_ACCESS_TOKEN || "").trim()
  ) {
    return false;
  }
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
  if (!shouldPrepareCloudSession(args, env)) {
    return false;
  }

  const ensureCloudSession =
    dependencies.ensureCloudSession || loadCloudSessionSDK();
  const apiUrl =
    String(optionValue(args, "--cloud-api-url") || "").trim() ||
    String(env.RELAYFILE_CLOUD_API_URL || "").trim() ||
    String(env.CLOUD_API_URL || "").trim() ||
    DEFAULT_CLOUD_API_URL;
  const session = await ensureCloudSession({
    apiUrl,
    interactive: true,
    device: hasFlag(args, "--no-open"),
  });

  // The child receives the session through its environment, never through
  // argv or stdout. The SDK has already persisted the same session in Agent
  // Relay's canonical auth store for future commands.
  env.CLOUD_API_URL = session.auth.apiUrl;
  env.CLOUD_API_ACCESS_TOKEN = session.auth.accessToken;
  env.CLOUD_API_REFRESH_TOKEN = session.auth.refreshToken;
  env.CLOUD_API_ACCESS_TOKEN_EXPIRES_AT =
    session.auth.accessTokenExpiresAt;
  if (session.auth.refreshTokenExpiresAt) {
    env.CLOUD_API_REFRESH_TOKEN_EXPIRES_AT =
      session.auth.refreshTokenExpiresAt;
  } else {
    delete env.CLOUD_API_REFRESH_TOKEN_EXPIRES_AT;
  }
  return true;
}

module.exports = {
  DEFAULT_CLOUD_API_URL,
  optionValue,
  prepareCloudSession,
  shouldPrepareCloudSession,
};
