#!/usr/bin/env node

import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(SCRIPT_DIR, "../..");
const SKIP_EXIT_CODE = 77;
const TOKEN_PREFIXES = ["relay_pa_", "relay_ws_", "relay_id_"];

const REQUIRED_ENV = [
  "RELAYFILE_TIER2_RELAYFILE_URL",
  "RELAYFILE_TIER2_RELAYAUTH_URL",
  "RELAYFILE_TIER2_WORKSPACE_ID",
  "RELAYFILE_TIER2_WORKSPACE_TOKEN",
];

function nowIso() {
  return new Date().toISOString();
}

function usage() {
  return `Tier-2 CF-DO scoped writeback harness.

This is intentionally credential-gated. Without RELAYFILE_TIER2_RUN=1 and the
required CF-DO workspace credentials, it writes a loud skipped evidence record
and exits ${SKIP_EXIT_CODE}; it must never false-green without a real run.

Required for a real run:
  RELAYFILE_TIER2_RUN=1
  RELAYFILE_TIER2_RELAYFILE_URL=https://dev-<stage>-api.relayfile.dev
  RELAYFILE_TIER2_RELAYAUTH_URL=https://dev-<stage>-api.relayauth.dev
  RELAYFILE_TIER2_WORKSPACE_ID=<disposable-workspace-id>
  RELAYFILE_TIER2_WORKSPACE_TOKEN=<workspace-token>
  RELAYFILE_TIER2_RELAYAUTH_API_KEY=<relayauth-api-key>  # optional for minting

Optional:
  RELAYFILE_TIER2_MEMBER_COUNT=6
  RELAYFILE_TIER2_WRITES_PER_MEMBER=2
  RELAYFILE_TIER2_RUN_ID=<stable-run-id>
  RELAYFILE_TIER2_EVIDENCE=/path/to/evidence.json
  RELAYFILE_TIER2_KEEP_TMP=1
  RELAYFILE_TIER2_API_TIMEOUT_MS=45000
  RELAYFILE_TIER2_MOUNT_TIMEOUT=45s

Modes:
  --self-test   validate the no-credential guard and token-scope validator
`;
}

function parseArgs(argv) {
  const args = new Set(argv);
  return {
    help: args.has("--help") || args.has("-h"),
    selfTest: args.has("--self-test"),
  };
}

function intEnv(name, fallback, { min = 1, max = Number.MAX_SAFE_INTEGER } = {}) {
  const raw = process.env[name]?.trim();
  if (!raw) return fallback;
  if (!/^\d+$/.test(raw)) {
    throw new Error(`${name} must be an integer between ${min} and ${max}`);
  }
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed) || parsed < min || parsed > max) {
    throw new Error(`${name} must be an integer between ${min} and ${max}`);
  }
  return parsed;
}

function trimEnv(name, env = process.env) {
  return env[name]?.trim() ?? "";
}

function normalizeBaseUrl(raw, name) {
  const value = raw.trim().replace(/\/+$/, "");
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function normalizeRemotePath(raw) {
  let value = String(raw ?? "").trim().replaceAll("\\", "/");
  if (!value) return "/";
  if (!value.startsWith("/")) value = `/${value}`;
  const segments = [];
  for (const part of value.split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") {
      segments.pop();
      continue;
    }
    segments.push(part);
  }
  return `/${segments.join("/")}`;
}

function pathScope(root) {
  return `relayfile:fs:write:${normalizeRemotePath(root)}/*`;
}

function isBroadOrAdminScope(scope) {
  const value = String(scope ?? "").trim();
  if (!value) return false;
  return (
    value === "fs:write" ||
    value === "fs:manage" ||
    value === "relayfile:fs:write:/*" ||
    value === "relayfile:fs:manage:/*" ||
    value.startsWith("admin:") ||
    value.startsWith("relayfile:fs:manage:")
  );
}

function validateMemberWriteScopes(scopes, assignedRoot) {
  if (!Array.isArray(scopes) || scopes.length === 0) {
    throw new Error(`member token for ${assignedRoot} has no scopes`);
  }
  const requiredWriteScope = pathScope(assignedRoot);
  let writeScopes = 0;
  for (const scope of scopes) {
    if (isBroadOrAdminScope(scope)) {
      throw new Error(`member token for ${assignedRoot} has broad/admin scope ${scope}`);
    }
    if (String(scope ?? "").trim().startsWith("relayfile:fs:write:")) {
      writeScopes += 1;
      if (String(scope).trim() !== requiredWriteScope) {
        throw new Error(`member token for ${assignedRoot} has wrong-root write scope ${scope}`);
      }
    }
  }
  if (writeScopes === 0) {
    throw new Error(
      `member token for ${assignedRoot} is missing exact write scope ${requiredWriteScope}`,
    );
  }
}

function stripRelayAuthPrefix(token) {
  for (const prefix of TOKEN_PREFIXES) {
    if (token.startsWith(prefix)) return token.slice(prefix.length);
  }
  return token;
}

function decodeBase64Url(segment) {
  const padded = segment.padEnd(segment.length + ((4 - (segment.length % 4)) % 4), "=");
  return Buffer.from(padded, "base64url").toString("utf8");
}

function decodeJwtClaims(token) {
  const raw = stripRelayAuthPrefix(String(token ?? "").trim());
  const parts = raw.split(".");
  if (parts.length !== 3) {
    throw new Error("path token is not a JWT-shaped RelayAuth token");
  }
  return JSON.parse(decodeBase64Url(parts[1]));
}

function scopesFromToken(token) {
  const claims = decodeJwtClaims(token);
  const raw = claims.scopes ?? claims.scope;
  if (Array.isArray(raw)) return raw.map(String).filter(Boolean);
  if (typeof raw === "string") return raw.split(/[\s,\t\r\n]+/).filter(Boolean);
  return [];
}

function missingCredentialReasons(env = process.env) {
  const missing = [];
  for (const name of REQUIRED_ENV) {
    if (!trimEnv(name, env)) missing.push(name);
  }
  if (trimEnv("RELAYFILE_TIER2_RUN", env) !== "1") {
    missing.push("RELAYFILE_TIER2_RUN=1");
  }
  return missing;
}

function buildConfig() {
  const runId =
    trimEnv("RELAYFILE_TIER2_RUN_ID") ||
    `tier2-${new Date().toISOString().replace(/[:.]/g, "-")}`;
  return {
    startedAt: nowIso(),
    runId,
    relayfileUrl: normalizeBaseUrl(trimEnv("RELAYFILE_TIER2_RELAYFILE_URL"), "RELAYFILE_TIER2_RELAYFILE_URL"),
    relayauthUrl: normalizeBaseUrl(trimEnv("RELAYFILE_TIER2_RELAYAUTH_URL"), "RELAYFILE_TIER2_RELAYAUTH_URL"),
    workspaceId: trimEnv("RELAYFILE_TIER2_WORKSPACE_ID"),
    workspaceToken: trimEnv("RELAYFILE_TIER2_WORKSPACE_TOKEN"),
    relayAuthApiKey: trimEnv("RELAYFILE_TIER2_RELAYAUTH_API_KEY"),
    memberCount: intEnv("RELAYFILE_TIER2_MEMBER_COUNT", 6, { min: 1, max: 24 }),
    writesPerMember: intEnv("RELAYFILE_TIER2_WRITES_PER_MEMBER", 2, { min: 1, max: 100 }),
    tokenTtlSeconds: intEnv("RELAYFILE_TIER2_TOKEN_TTL_SECONDS", 3600, { min: 300 }),
    apiTimeoutMs: intEnv("RELAYFILE_TIER2_API_TIMEOUT_MS", 45_000, { min: 1_000 }),
    mountTimeout: trimEnv("RELAYFILE_TIER2_MOUNT_TIMEOUT") || "45s",
    keepTmp: trimEnv("RELAYFILE_TIER2_KEEP_TMP") === "1",
    evidencePath:
      trimEnv("RELAYFILE_TIER2_EVIDENCE") ||
      path.join(os.tmpdir(), `relayfile-tier2-cfdo-${runId}.json`),
  };
}

function baseEvidence(status, details = {}) {
  return {
    schema: "relayfile.tier2.cfdo-scoped-writeback.v1",
    status,
    generatedAt: nowIso(),
    criticalLabel:
      "Tier-2 evidence only. This harness is credential-gated; absent credentials are skipped, not passed. Section 7 remains open until this runs against real CF-DO.",
    acceptance:
      "Pass criteria are absence of the #1602 pathology: no 500/object-reset/context-deadline; graceful 429 workspace_busy with Retry-After is accepted at the effective admission cap.",
    admissionLayers: {
      edgeWriteAdmission:
        "caller/stage configured; record observed statuses, Retry-After, and response bodies at run time",
      workspaceDOInternal:
        "source-confirmed default max inflight is 12 in request-admission; record observed evidence separately",
    },
    ...details,
  };
}

async function writeEvidence(evidencePath, evidence) {
  await mkdir(path.dirname(evidencePath), { recursive: true });
  await writeFile(evidencePath, `${JSON.stringify(evidence, null, 2)}\n`, "utf8");
}

async function requestJSON(config, { method, api = "relayfile", path: requestPath, token, headers = {}, body }) {
  const baseUrl = api === "relayauth" ? config.relayauthUrl : config.relayfileUrl;
  const requestUrl = `${baseUrl}${requestPath}`;
  const started = Date.now();
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), config.apiTimeoutMs).unref();
  let response;
  try {
    response = await fetch(requestUrl, {
      method,
      headers: {
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...(body ? { "Content-Type": "application/json" } : {}),
        ...headers,
      },
      body: body ? JSON.stringify(body) : undefined,
      signal: controller.signal,
    });
  } catch (error) {
    return {
      ok: false,
      status: 0,
      durationMs: Date.now() - started,
      retryAfter: null,
      body: null,
      text: error instanceof Error ? error.message : String(error),
      url: requestUrl.replace(/[?].*/, "?[query]"),
    };
  } finally {
    clearTimeout(timer);
  }
  const text = await response.text();
  let parsed = null;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = null;
    }
  }
  return {
    ok: response.ok,
    status: response.status,
    durationMs: Date.now() - started,
    retryAfter: response.headers.get("Retry-After"),
    body: parsed,
    text: parsed ? undefined : text.slice(0, 1000),
    url: requestUrl.replace(/[?].*/, "?[query]"),
  };
}

async function mintPathToken(config, member) {
  const headers = {};
  if (config.relayAuthApiKey) {
    headers["x-api-key"] = config.relayAuthApiKey;
  }
  const response = await requestJSON(config, {
    api: "relayauth",
    method: "POST",
    path: "/v1/tokens/path",
    token: config.relayAuthApiKey ? "" : config.workspaceToken,
    headers,
    body: {
      workspaceId: config.workspaceId,
      paths: [member.remoteRoot],
      ttlSeconds: config.tokenTtlSeconds,
      agentName: `tier2-member-${member.index}`,
    },
  });
  if (!response.ok) {
    throw new Error(`path token mint failed for ${member.name}: ${response.status} ${JSON.stringify(response.body ?? response.text)}`);
  }
  const accessToken = response.body?.accessToken;
  if (typeof accessToken !== "string" || accessToken.trim() === "") {
    throw new Error(`path token mint for ${member.name} did not return accessToken`);
  }
  const scopes = scopesFromToken(accessToken);
  validateMemberWriteScopes(scopes, member.remoteRoot);
  return { token: accessToken, scopes, mintResponse: response };
}

async function seedRemote(config, members) {
  const files = members.map((member) => ({
    path: `${member.remoteRoot}/README.md`,
    contentType: "text/markdown",
    content: `# ${member.name}\n\nseed ${config.runId}\n`,
  }));
  const response = await requestJSON(config, {
    method: "POST",
    path: `/v1/workspaces/${encodeURIComponent(config.workspaceId)}/fs/bulk`,
    token: config.workspaceToken,
    body: { files },
  });
  if (!response.ok) {
    throw new Error(`remote seed failed: ${response.status} ${JSON.stringify(response.body ?? response.text)}`);
  }
  return response;
}

async function readRemoteFile(config, remotePath) {
  const query = new URLSearchParams({ path: remotePath });
  return requestJSON(config, {
    method: "GET",
    path: `/v1/workspaces/${encodeURIComponent(config.workspaceId)}/fs/file?${query.toString()}`,
    token: config.workspaceToken,
  });
}

async function buildMountBinary(workRoot) {
  const out = path.join(workRoot, "bin", "relayfile-mount");
  await mkdir(path.dirname(out), { recursive: true });
  const result = await runProcess("go", ["build", "-o", out, "./cmd/relayfile-mount"], {
    cwd: REPO_ROOT,
    timeoutMs: 120_000,
  });
  if (result.exitCode !== 0) {
    throw new Error(`go build relayfile-mount failed: ${result.error || result.stderr || result.stdout}`);
  }
  return out;
}

function runProcess(command, args, { cwd = REPO_ROOT, env = {}, timeoutMs = 60_000 } = {}) {
  return new Promise((resolve) => {
    let settled = false;
    const child = spawn(command, args, {
      cwd,
      env: { ...process.env, ...env },
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGTERM");
      setTimeout(() => child.kill("SIGKILL"), 2000).unref();
    }, timeoutMs);
    child.stdout.on("data", (chunk) => {
      stdout += chunk.toString("utf8");
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString("utf8");
    });
    const finish = (result) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({
        command,
        args,
        timedOut,
        stdout,
        stderr,
        ...result,
      });
    };
    child.on("error", (error) => {
      finish({
        exitCode: null,
        signal: null,
        error: error instanceof Error ? error.message : String(error),
      });
    });
    child.on("close", (exitCode, signal) => {
      finish({
        exitCode,
        signal,
      });
    });
  });
}

async function runMountOnce(config, binaryPath, member, phase, extraArgs = []) {
  const args = [
    "--base-url",
    config.relayfileUrl,
    "--workspace",
    config.workspaceId,
    "--token",
    member.token,
    "--remote-path",
    member.remoteRoot,
    "--local-dir",
    member.liveRoot,
    "--state-file",
    path.join(member.liveRoot, ".relayfile-mount-state.json"),
    "--mode",
    "poll",
    "--websocket=false",
    "--once",
    "--timeout",
    config.mountTimeout,
    "--bootstrap-timeout",
    "0",
    ...extraArgs,
  ];
  const started = Date.now();
  const result = await runProcess(binaryPath, args, {
    timeoutMs: 180_000,
  });
  return {
    phase,
    member: member.name,
    remoteRoot: member.remoteRoot,
    exitCode: result.exitCode,
    signal: result.signal,
    error: result.error,
    timedOut: result.timedOut,
    durationMs: Date.now() - started,
    stdout: result.stdout,
    stderr: result.stderr,
    observations: analyzeMountOutput(`${result.stdout}\n${result.stderr}`),
  };
}

function analyzeMountOutput(text) {
  const lower = text.toLowerCase();
  const statusCodes = [...text.matchAll(/\bhttp\s+(\d{3})\b/gi)].map((match) => Number(match[1]));
  return {
    statusCodes,
    saw429: statusCodes.includes(429) || lower.includes("workspace_busy"),
    sawRetryAfter: lower.includes("retry-after"),
    sawContextDeadline: lower.includes("context deadline"),
    sawObjectReset: lower.includes("object reset") || lower.includes("exceeded timeout"),
    sawHttp500: statusCodes.some((code) => code >= 500),
  };
}

function assertNoPathology(mounts) {
  const bad = mounts.filter((mount) => {
    const obs = mount.observations;
    return obs.sawContextDeadline || obs.sawObjectReset || obs.sawHttp500 || mount.timedOut;
  });
  if (bad.length > 0) {
    throw new Error(`observed #1602-class pathology: ${bad.map((m) => `${m.member}:${JSON.stringify(m.observations)}`).join(", ")}`);
  }
}

async function prepareMemberRoots(config, workRoot, members) {
  for (const member of members) {
    member.seedRoot = path.join(workRoot, "members", member.name, "seed", "project", "cloud");
    member.liveRoot = path.join(member.seedRoot, "packages", member.name);
    member.outOfScopeLocalPath = path.join(member.seedRoot, "outside", `${member.name}-SHOULD_NOT_WRITE.md`);
    await mkdir(member.liveRoot, { recursive: true });
    await mkdir(path.dirname(member.outOfScopeLocalPath), { recursive: true });
    await writeFile(
      path.join(member.liveRoot, "README.md"),
      `# ${member.name}\n\nseed ${config.runId}\n`,
      "utf8",
    );
    await writeFile(
      member.outOfScopeLocalPath,
      `out-of-scope sentinel for ${member.name} ${config.runId}\n`,
      "utf8",
    );
  }
}

async function writeMemberChanges(config, members) {
  for (const member of members) {
    member.expectedRemoteFiles = [];
    for (let index = 0; index < config.writesPerMember; index += 1) {
      const rel = `result-${index}.md`;
      const content = `member=${member.name}\nrun=${config.runId}\nwrite=${index}\n`;
      await writeFile(path.join(member.liveRoot, rel), content, "utf8");
      member.expectedRemoteFiles.push({
        path: `${member.remoteRoot}/${rel}`,
        content,
      });
    }
  }
}

async function verifyRemoteVisibility(config, members) {
  const checks = [];
  for (const member of members) {
    for (const expected of member.expectedRemoteFiles) {
      const response = await readRemoteFile(config, expected.path);
      checks.push({
        member: member.name,
        path: expected.path,
        status: response.status,
        ok: response.ok,
        contentMatches: response.body?.content === expected.content,
      });
      if (!response.ok || response.body?.content !== expected.content) {
        throw new Error(`remote visibility failed for ${expected.path}: status ${response.status}`);
      }
    }
    const outOfScopeRemote = `/team-tier2/${config.runId}/outside/${member.name}-SHOULD_NOT_WRITE.md`;
    const outResponse = await readRemoteFile(config, outOfScopeRemote);
    checks.push({
      member: member.name,
      path: outOfScopeRemote,
      status: outResponse.status,
      ok: outResponse.ok,
      expectedMissing: true,
    });
    if (outResponse.ok) {
      throw new Error(`out-of-scope sentinel unexpectedly reached remote: ${outOfScopeRemote}`);
    }
  }
  return checks;
}

async function directAdmissionProbe(config, members) {
  const files = members.map((member, index) => ({
    path: `${member.remoteRoot}/admission-probe-${Date.now()}-${index}.md`,
    contentType: "text/markdown",
    content: `admission probe ${member.name} ${config.runId}\n`,
  }));
  const started = Date.now();
  const responses = await Promise.all(
    files.map((file) =>
      requestJSON(config, {
        method: "POST",
        path: `/v1/workspaces/${encodeURIComponent(config.workspaceId)}/fs/bulk`,
        token: config.workspaceToken,
        headers: { "X-Relayfile-Write-Class": "foreground_content" },
        body: { files: [file] },
      }),
    ),
  );
  return {
    durationMs: Date.now() - started,
    responses: responses.map((response) => ({
      status: response.status,
      retryAfter: response.retryAfter,
      code: response.body?.code,
      reason: response.body?.reason,
      durationMs: response.durationMs,
    })),
  };
}

async function runHarness() {
  const config = buildConfig();
  const workRoot = await mkdtemp(path.join(os.tmpdir(), `relayfile-tier2-${config.runId}-`));
  const members = Array.from({ length: config.memberCount }, (_, index) => ({
    index,
    name: `member-${index + 1}`,
    remoteRoot: `/team-tier2/${config.runId}/member-${index + 1}`,
  }));
  const evidence = baseEvidence("running", {
    runId: config.runId,
    startedAt: config.startedAt,
    config: {
      relayfileUrl: config.relayfileUrl,
      relayauthUrl: config.relayauthUrl,
      workspaceId: config.workspaceId,
      memberCount: config.memberCount,
      writesPerMember: config.writesPerMember,
      apiTimeoutMs: config.apiTimeoutMs,
      mountTimeout: config.mountTimeout,
      tokenSource: config.relayAuthApiKey ? "relayauth-api-key" : "workspace-token",
      evidencePath: config.evidencePath,
    },
    workRoot,
    members: members.map((member) => ({
      name: member.name,
      remoteRoot: member.remoteRoot,
      requiredWriteScope: pathScope(member.remoteRoot),
    })),
  });
  try {
    const binaryPath = await buildMountBinary(workRoot);
    evidence.binary = { path: binaryPath };

    await prepareMemberRoots(config, workRoot, members);
    evidence.seed = { remote: await seedRemote(config, members) };

    for (const member of members) {
      const tokenResult = await mintPathToken(config, member);
      member.token = tokenResult.token;
      member.scopes = tokenResult.scopes;
    }
    evidence.memberScopes = members.map((member) => ({
      name: member.name,
      remoteRoot: member.remoteRoot,
      scopes: member.scopes,
    }));

    const initialMounts = await Promise.all(
      members.map((member) => runMountOnce(config, binaryPath, member, "bootstrap")),
    );
    assertNoPathology(initialMounts);
    const failedInitial = initialMounts.filter((mount) => mount.exitCode !== 0);
    if (failedInitial.length > 0) {
      throw new Error(`bootstrap mount failed: ${failedInitial.map((m) => `${m.member}:${m.exitCode}`).join(", ")}`);
    }

    await writeMemberChanges(config, members);
    const writebackMounts = await Promise.all(
      members.map((member) => runMountOnce(config, binaryPath, member, "writeback", ["--full-reconcile"])),
    );
    assertNoPathology(writebackMounts);

    const visibilityChecks = await verifyRemoteVisibility(config, members);
    const admissionProbe = await directAdmissionProbe(config, members);

    const failedWriteback = writebackMounts.filter((mount) => mount.exitCode !== 0);
    const observed429 = [...initialMounts, ...writebackMounts].some((mount) => mount.observations.saw429) ||
      admissionProbe.responses.some((response) => response.status === 429);
    if (failedWriteback.length > 0 && !observed429) {
      throw new Error(`writeback mount failed without graceful 429 evidence: ${failedWriteback.map((m) => `${m.member}:${m.exitCode}`).join(", ")}`);
    }

    evidence.status = "passed";
    evidence.completedAt = nowIso();
    evidence.mounts = [...initialMounts, ...writebackMounts].map(redactMountResult);
    evidence.remoteVisibility = visibilityChecks;
    evidence.admissionProbe = admissionProbe;
    evidence.summary = {
      observed429,
      observedRetryAfter: admissionProbe.responses.some((response) => response.retryAfter) ||
        evidence.mounts.some((mount) => mount.observations.sawRetryAfter),
      observed500: evidence.mounts.some((mount) => mount.observations.sawHttp500) ||
        admissionProbe.responses.some((response) => response.status >= 500),
      observedContextDeadline: evidence.mounts.some((mount) => mount.observations.sawContextDeadline),
      observedObjectReset: evidence.mounts.some((mount) => mount.observations.sawObjectReset),
    };
    if (evidence.summary.observed500 || evidence.summary.observedContextDeadline || evidence.summary.observedObjectReset) {
      evidence.status = "failed";
      throw new Error(`observed #1602-class pathology: ${JSON.stringify(evidence.summary)}`);
    }
    return { evidence, exitCode: 0 };
  } catch (error) {
    evidence.status = "failed";
    evidence.completedAt = nowIso();
    evidence.error = error instanceof Error ? { message: error.message, stack: error.stack } : { message: String(error) };
    return { evidence, exitCode: 1 };
  } finally {
    if (!config.keepTmp) {
      await rm(workRoot, { recursive: true, force: true });
    }
  }
}

function redactMountResult(result) {
  return {
    phase: result.phase,
    member: result.member,
    remoteRoot: result.remoteRoot,
    exitCode: result.exitCode,
    signal: result.signal,
    error: result.error,
    timedOut: result.timedOut,
    durationMs: result.durationMs,
    observations: result.observations,
    stdoutTail: tail(result.stdout),
    stderrTail: tail(result.stderr),
  };
}

function tail(text, limit = 4000) {
  if (!text) return "";
  const value = text.length > limit ? text.slice(-limit) : text;
  return value
    .replace(/Bearer\s+[^\s"'`]+/g, "Bearer [redacted]")
    .replace(/relay_(pa|ws|id)_[A-Za-z0-9._-]+/g, "relay_$1_[redacted]");
}

async function selfTest() {
  const good = [pathScope("/team-tier2/run/member-1"), "relayfile:fs:read:/team-tier2/run/member-1/*"];
  validateMemberWriteScopes(good, "/team-tier2/run/member-1");
  assert.throws(() => validateMemberWriteScopes([], "/team-tier2/run/member-1"), /no scopes/);
  assert.throws(() => validateMemberWriteScopes(["fs:write"], "/team-tier2/run/member-1"), /broad/);
  assert.throws(() => validateMemberWriteScopes(["relayfile:fs:write:/*"], "/team-tier2/run/member-1"), /broad/);
  assert.throws(
    () => validateMemberWriteScopes(["relayfile:fs:manage:/team-tier2/run/member-1/*"], "/team-tier2/run/member-1"),
    /broad/,
  );
  assert.throws(
    () => validateMemberWriteScopes([pathScope("/team-tier2/run/member-2")], "/team-tier2/run/member-1"),
    /wrong-root write scope/,
  );
  assert.throws(
    () =>
      validateMemberWriteScopes(
        [pathScope("/team-tier2/run/member-1"), pathScope("/team-tier2/run/member-2")],
        "/team-tier2/run/member-1",
      ),
    /wrong-root write scope/,
  );
  const scrubbed = tail(
    "Authorization: Bearer relay_ws_header.payload.signature token=relay_pa_header.payload.signature",
  );
  assert(!scrubbed.includes("relay_ws_header.payload.signature"));
  assert(!scrubbed.includes("relay_pa_header.payload.signature"));
  assert(scrubbed.includes("Bearer [redacted]"));
  const oldMemberCount = process.env.RELAYFILE_TIER2_MEMBER_COUNT;
  try {
    process.env.RELAYFILE_TIER2_MEMBER_COUNT = "1abc";
    assert.throws(() => intEnv("RELAYFILE_TIER2_MEMBER_COUNT", 1), /integer/);
  } finally {
    if (oldMemberCount === undefined) {
      delete process.env.RELAYFILE_TIER2_MEMBER_COUNT;
    } else {
      process.env.RELAYFILE_TIER2_MEMBER_COUNT = oldMemberCount;
    }
  }
  const missing = missingCredentialReasons({});
  assert(missing.includes("RELAYFILE_TIER2_RUN=1"));
  assert(missing.includes("RELAYFILE_TIER2_WORKSPACE_TOKEN"));
  console.log("tier2 harness self-test passed");
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    console.log(usage());
    return;
  }
  if (args.selfTest) {
    await selfTest();
    return;
  }

  const missing = missingCredentialReasons();
  if (missing.length > 0) {
    const runId = trimEnv("RELAYFILE_TIER2_RUN_ID") || "skipped";
    const evidencePath =
      trimEnv("RELAYFILE_TIER2_EVIDENCE") ||
      path.join(os.tmpdir(), `relayfile-tier2-cfdo-${runId}.json`);
    const evidence = baseEvidence("skipped", {
      reason: "missing explicit CF-DO harness credentials or RELAYFILE_TIER2_RUN=1",
      missing,
      evidencePath,
      note:
        "This skip is intentional and must not be interpreted as Tier-2 passing. Run with provisioned CF-DO workspace credentials to produce load evidence.",
    });
    await writeEvidence(evidencePath, evidence);
    console.error(`TIER2_HARNESS_SKIPPED ${JSON.stringify({ missing, evidencePath })}`);
    process.exitCode = SKIP_EXIT_CODE;
    return;
  }

  const { evidence, exitCode } = await runHarness();
  await writeEvidence(evidence.config.evidencePath, evidence);
  console.log(`TIER2_HARNESS_EVIDENCE ${evidence.config.evidencePath}`);
  process.exitCode = exitCode;
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
