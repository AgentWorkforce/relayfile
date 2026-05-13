import { mkdtempSync, rmSync } from "node:fs";
import { mkdir, readdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";

export function createRelayfileExecutor(opts = {}) {
  return async function relayfileExecute(testCase) {
    const input = testCase.input ?? {};
    const mock = testCase.mock ?? {};
    const useConfiguredMount = input.useConfiguredMount === true;
    const configuredMount = opts.mountPath ?? process.env.RELAYFILE_MOUNT;
    const mountRoot = useConfiguredMount && configuredMount
      ? path.resolve(configuredMount)
      : mkdtempSync(path.join(tmpdir(), "relayfile-eval-"));
    const cleanup = !(useConfiguredMount && configuredMount);

    const state = {
      mountRoot,
      acl: normalizeAcl(mock.acl),
      metadata: normalizeMetadata(mock.metadata),
      readOnlyFields: normalizeReadOnlyFields(mock.readOnlyFields),
      sideEffects: [],
      toolCalls: [],
      filesModified: new Set(),
      writebackQueue: [],
      contentLines: [],
      metrics: {
        startTime: Date.now(),
        operationCount: 0,
      },
    };

    try {
      await seedMount(state, mock);
      const beforeSnapshot = await snapshotMount(state);

      for (const operation of normalizeOperations(input.operation ?? input.operations)) {
        state.metrics.operationCount += 1;
        await executeOperation(state, operation, mock);
      }

      const afterSnapshot = await snapshotMount(state);
      const modifiedByDiff = diffSnapshots(beforeSnapshot, afterSnapshot);
      for (const filePath of modifiedByDiff) state.filesModified.add(filePath);

      state.metrics.durationMs = Date.now() - state.metrics.startTime;
      state.metrics.toolCalls = state.toolCalls.length;

      return {
        ok: !state.sideEffects.some((effect) => effect.kind === "error"),
        status: "completed",
        content: state.contentLines.join("\n"),
        toolCalls: state.toolCalls,
        sideEffects: state.sideEffects,
        filesModified: [...state.filesModified].sort(compareStrings),
        writebackQueue: state.writebackQueue,
        metadata: state.metadata,
        mountSnapshot: afterSnapshot,
        metrics: state.metrics,
        notes: cleanup
          ? "Relayfile eval ran against an isolated in-memory filesystem fixture."
          : `Relayfile eval ran against configured mount ${mountRoot}.`,
      };
    } finally {
      if (cleanup) rmSync(mountRoot, { recursive: true, force: true });
    }
  };
}

async function seedMount(state, mock) {
  const files = mock.files && typeof mock.files === "object" ? mock.files : {};
  for (const [virtualPath, value] of Object.entries(files)) {
    const content = typeof value === "string" ? value : `${JSON.stringify(value, null, 2)}\n`;
    await writeVirtualFile(state, virtualPath, content, { seed: true });
  }
}

async function executeOperation(state, operation, mock) {
  const op = operation.op ?? operation.type;
  if (!op) throw new Error(`operation is missing op/type: ${JSON.stringify(operation)}`);

  if (op === "sleep") {
    const ms = Number(operation.ms ?? 0);
    await new Promise((resolve) => setTimeout(resolve, Math.max(0, Math.min(ms, 5_000))));
    state.toolCalls.push({ name: "sleep", ms });
    state.contentLines.push(`slept ${ms}ms`);
    return;
  }

  if (op === "synthesizeYesterday") {
    await synthesizeYesterday(state, operation, mock);
    return;
  }

  const virtualPath = normalizeMountPath(operation.path);
  if (op === "read") {
    if (!allowRead(state, virtualPath, "read")) return;
    const resolved = resolveVirtualPath(state, virtualPath);
    const content = await readFile(resolved, "utf8").catch((error) => {
      recordFileError(state, "read", virtualPath, error, operation);
      return undefined;
    });
    if (content === undefined) return;
    state.toolCalls.push({ name: "read", path: virtualPath, bytes: Buffer.byteLength(content) });
    state.contentLines.push(`${label(operation)}read ${virtualPath}: ${preview(content)}`);
    return;
  }

  if (op === "list") {
    if (!allowRead(state, virtualPath, "list")) return;
    const entries = await listVirtualDir(state, virtualPath);
    state.toolCalls.push({ name: "list", path: virtualPath, entries });
    state.contentLines.push(`${label(operation)}list ${virtualPath}: ${entries.join(", ")}`);
    return;
  }

  if (op === "grep") {
    if (!allowRead(state, virtualPath, "grep")) return;
    const matches = await grepVirtualTree(state, virtualPath, String(operation.pattern ?? ""));
    state.toolCalls.push({ name: "grep", path: virtualPath, pattern: operation.pattern, matches: matches.length });
    for (const match of matches) state.contentLines.push(`${label(operation)}${match.path}: ${match.line}`);
    if (matches.length === 0) state.contentLines.push(`${label(operation)}no matches under ${virtualPath}`);
    return;
  }

  if (op === "stat") {
    if (!allowRead(state, virtualPath, "stat")) return;
    const stats = await stat(resolveVirtualPath(state, virtualPath)).catch((error) => {
      recordFileError(state, "stat", virtualPath, error, operation);
      return undefined;
    });
    if (stats === undefined) return;
    state.toolCalls.push({ name: "stat", path: virtualPath, isFile: stats.isFile(), isDirectory: stats.isDirectory() });
    state.contentLines.push(`${label(operation)}stat ${virtualPath}: ${stats.isDirectory() ? "directory" : "file"}`);
    return;
  }

  if (op === "delete") {
    if (!allowWrite(state, virtualPath, "delete")) return;
    await rm(resolveVirtualPath(state, virtualPath), { recursive: true, force: true });
    state.filesModified.add(virtualPath);
    state.sideEffects.push({ kind: "deleted", path: virtualPath });
    state.toolCalls.push({ name: "delete", path: virtualPath });
    state.contentLines.push(`${label(operation)}deleted ${virtualPath}`);
    return;
  }

  if (op === "materializeWebhook") {
    const rawPath = typeof operation.path === "string" ? operation.path.trim() : "";
    const webhookPath = normalizeMountPath(rawPath);
    if (!rawPath || webhookPath === "/") {
      throw new Error(`materializeWebhook operation is missing or invalid path: ${JSON.stringify(operation)}`);
    }
    const provider = String(operation.provider ?? operation.adapter ?? "").trim();
    if (!provider) throw new Error(`materializeWebhook operation is missing provider: ${JSON.stringify(operation)}`);
    const rawContent = operation.content === undefined ? {} : operation.content;
    const content = typeof rawContent === "string" ? rawContent : `${JSON.stringify(rawContent, null, 2)}\n`;
    await writeVirtualFile(state, webhookPath, content);
    const effect = {
      kind: "webhookMaterialized",
      provider,
      path: webhookPath,
      receivedAt: operation.receivedAt,
      source: operation.source,
    };
    state.sideEffects.push(effect);
    state.toolCalls.push({ name: "materializeWebhook", provider, path: webhookPath });
    state.contentLines.push(`${label(operation)}webhook ${provider} materialized ${webhookPath}: ${preview(content)}`);
    return;
  }

  if (op === "write" || op === "append") {
    if (!allowWrite(state, virtualPath, op)) return;
    const rawContent = operation.content === undefined ? "" : operation.content;
    const content = typeof rawContent === "string" ? rawContent : `${JSON.stringify(rawContent, null, 2)}\n`;
    const rejection = validateReadOnlyFields(state, virtualPath, content);
    if (rejection) {
      await recordWritebackStatus(state, virtualPath, rejection);
      state.sideEffects.push({ kind: "validationRejected", path: virtualPath, reason: rejection });
      state.toolCalls.push({ name: op, path: virtualPath, rejected: true });
      state.contentLines.push(`${label(operation)}rejected ${virtualPath}: ${rejection}`);
      return;
    }

    if (operation.createReceipt) {
      const receipt = {
        created: true,
        path: normalizeMountPath(operation.createReceipt.path),
        url: operation.createReceipt.url,
      };
      await writeVirtualFile(state, virtualPath, `${JSON.stringify(receipt, null, 2)}\n`);
      state.sideEffects.push({ kind: "wrote", path: virtualPath });
      queueWriteback(state, operation, virtualPath);
      state.toolCalls.push({ name: op, path: virtualPath, bytes: Buffer.byteLength(JSON.stringify(receipt, null, 2)) });
      state.contentLines.push(`${label(operation)}created receipt ${virtualPath} -> ${receipt.path}`);
      return;
    }

    const resolvedPath = resolveVirtualPath(state, virtualPath);
    const nextContent = op === "append" && await pathExists(resolvedPath)
      ? `${await readFile(resolveVirtualPath(state, virtualPath), "utf8")}${content}`
      : content;
    await writeVirtualFile(state, virtualPath, nextContent);
    state.sideEffects.push({ kind: "wrote", path: virtualPath });
    queueWriteback(state, operation, virtualPath);
    state.toolCalls.push({ name: op, path: virtualPath, bytes: Buffer.byteLength(content) });
    state.contentLines.push(`${label(operation)}${op} ${virtualPath}: ${preview(content)}`);
    return;
  }

  throw new Error(`unknown relayfile eval operation "${op}"`);
}

function queueWriteback(state, operation, virtualPath) {
  const writeback = operation.writeback ?? (operation.adapter ? { adapter: operation.adapter } : undefined);
  if (!writeback) return;
  const queued = {
    kind: "writebackQueued",
    adapter: writeback.adapter,
    path: normalizeMountPath(writeback.path ?? virtualPath),
    mode: writeback.mode ?? "patch",
  };
  state.writebackQueue.push(queued);
  state.sideEffects.push(queued);
}

async function synthesizeYesterday(state, operation, mock) {
  const date = String(operation.date ?? mock.yesterday ?? "2026-05-08");
  const roots = Array.isArray(operation.paths) ? operation.paths.map(normalizeMountPath) : ["/"];
  const matches = [];
  for (const root of roots) {
    if (!allowRead(state, root, "synthesizeYesterday")) continue;
    for (const filePath of await listFilesRecursive(state, root)) {
      const content = await readFile(resolveVirtualPath(state, filePath), "utf8");
      const parsed = tryParseJson(content);
      const haystack = JSON.stringify(parsed ?? content);
      if (haystack.includes(date)) {
        matches.push({ path: filePath, title: parsed?.title ?? parsed?.name ?? path.basename(filePath), content });
      }
    }
  }
  state.toolCalls.push({ name: "synthesizeYesterday", date, matches: matches.length });
  state.metrics.relayfileMatches = matches.length;
  if (mock.comparison && typeof mock.comparison === "object") {
    state.metrics.mcpMatches = Number(mock.comparison.mcpMatches ?? 0);
    state.metrics.mcpToolCalls = Number(mock.comparison.mcpToolCalls ?? 0);
    state.metrics.mcpTokens = Number(mock.comparison.mcpTokens ?? 0);
    state.metrics.relayfileTokens = Number(mock.comparison.relayfileTokens ?? 0);
    state.metrics.mcpCostUsd = Number(mock.comparison.mcpCostUsd ?? 0);
    state.metrics.relayfileCostUsd = Number(mock.comparison.relayfileCostUsd ?? 0);
  }
  state.contentLines.push(`yesterday ${date}: ${matches.map((match) => `${match.title} (${match.path})`).join("; ")}`);
  if (mock.comparison?.mcpMissed) {
    state.contentLines.push(`mcp missed: ${mock.comparison.mcpMissed}`);
  }
}

async function writeVirtualFile(state, virtualPath, content, options = {}) {
  const resolved = resolveVirtualPath(state, virtualPath);
  await mkdir(path.dirname(resolved), { recursive: true });
  await writeFile(resolved, content);
  if (!options.seed) state.filesModified.add(normalizeMountPath(virtualPath));
}

async function listVirtualDir(state, virtualPath) {
  const resolved = resolveVirtualPath(state, virtualPath);
  const entries = await readdir(resolved);
  return entries
    .filter((entry) => {
      const childPath = normalizeMountPath(path.posix.join(virtualPath, entry));
      return accessForPath(state, childPath) !== "none";
    })
    .sort(compareStrings);
}

async function grepVirtualTree(state, rootPath, pattern) {
  const matches = [];
  for (const filePath of await listFilesRecursive(state, rootPath)) {
    const content = await readFile(resolveVirtualPath(state, filePath), "utf8");
    content.split("\n").forEach((line, index) => {
      if (line.includes(pattern)) matches.push({ path: filePath, line: `${index + 1}:${line.trim()}` });
    });
  }
  return matches;
}

async function listFilesRecursive(state, rootPath) {
  const normalizedRoot = normalizeMountPath(rootPath);
  const resolvedRoot = resolveVirtualPath(state, normalizedRoot);
  const stats = await stat(resolvedRoot).catch((error) => {
    if (error?.code === "ENOENT") return undefined;
    throw error;
  });
  if (!stats) return [];
  if (stats.isFile()) return [normalizedRoot];

  const result = [];
  const entries = await readdir(resolvedRoot);
  for (const entry of entries.sort(compareStrings)) {
    const childPath = normalizeMountPath(path.posix.join(normalizedRoot, entry));
    if (accessForPath(state, childPath) === "none") continue;
    const childStats = await stat(resolveVirtualPath(state, childPath));
    if (childStats.isDirectory()) result.push(...await listFilesRecursive(state, childPath));
    else result.push(childPath);
  }
  return result;
}

async function snapshotMount(state) {
  const files = {};
  for (const filePath of await listFilesRecursive(state, "/")) {
    files[filePath] = {
      content: await readFile(resolveVirtualPath(state, filePath), "utf8"),
      metadata: state.metadata[filePath] ?? {},
    };
  }
  return files;
}

function diffSnapshots(before, after) {
  const modified = new Set();
  for (const filePath of Object.keys(after)) {
    if (!before[filePath] || before[filePath].content !== after[filePath].content) modified.add(filePath);
  }
  for (const filePath of Object.keys(before)) {
    if (!after[filePath]) modified.add(filePath);
  }
  return [...modified].sort(compareStrings);
}

async function recordWritebackStatus(state, virtualPath, reason) {
  const statusPath = "/.relay/writeback-status.json";
  const status = {
    ok: false,
    path: normalizeMountPath(virtualPath),
    reason,
  };
  await writeVirtualFile(state, statusPath, `${JSON.stringify(status, null, 2)}\n`);
}

function validateReadOnlyFields(state, virtualPath, content) {
  const readOnlyFields = fieldsForPath(state, virtualPath);
  if (readOnlyFields.length === 0) return undefined;
  const parsed = tryParseJson(content);
  if (!parsed || typeof parsed !== "object") return undefined;
  const rejected = readOnlyFields.find((field) => hasPropertyPath(parsed, field));
  return rejected ? `ReadOnlyFieldError(${JSON.stringify(rejected)})` : undefined;
}

function fieldsForPath(state, virtualPath) {
  const normalized = normalizeMountPath(virtualPath);
  let fields = [];
  let bestPrefix = "";
  for (const [prefix, values] of Object.entries(state.readOnlyFields)) {
    if (pathIsWithin(normalized, prefix) && prefix.length >= bestPrefix.length) {
      bestPrefix = prefix;
      fields = values;
    }
  }
  return fields;
}

function allowRead(state, virtualPath, op) {
  if (accessForPath(state, virtualPath) !== "none") return true;
  recordDenied(state, virtualPath, op);
  return false;
}

function allowWrite(state, virtualPath, op) {
  if (accessForPath(state, virtualPath) === "readwrite") return true;
  recordDenied(state, virtualPath, op);
  return false;
}

function recordDenied(state, virtualPath, op) {
  const normalized = normalizeMountPath(virtualPath);
  state.sideEffects.push({ kind: "denied", op, path: normalized });
  state.toolCalls.push({ name: op, path: normalized, denied: true });
  state.contentLines.push(`${op} denied by ACL: ${normalized}`);
}

function accessForPath(state, virtualPath) {
  const normalized = normalizeMountPath(virtualPath);
  let access = "readwrite";
  let bestPrefix = "";
  for (const rule of state.acl) {
    if (pathIsWithin(normalized, rule.pathPrefix) && rule.pathPrefix.length >= bestPrefix.length) {
      access = rule.access;
      bestPrefix = rule.pathPrefix;
    }
  }
  return access;
}

function resolveVirtualPath(state, virtualPath) {
  const normalized = normalizeMountPath(virtualPath);
  const resolved = path.resolve(state.mountRoot, `.${normalized}`);
  const relative = path.relative(state.mountRoot, resolved);
  if (relative.startsWith("..") || path.isAbsolute(relative)) {
    throw new Error(`mount path escapes root: ${virtualPath}`);
  }
  return resolved;
}

function normalizeOperations(raw) {
  if (Array.isArray(raw)) return raw;
  if (raw && typeof raw === "object") return [raw];
  throw new Error("relayfile executor requires input.operation or input.operations");
}

function normalizeAcl(raw) {
  if (!Array.isArray(raw)) return [];
  return raw.map((rule) => ({
    pathPrefix: normalizeMountPath(rule.pathPrefix ?? rule.path ?? "/"),
    access: rule.access === "none" || rule.access === "read" ? rule.access : "readwrite",
  }));
}

function normalizeMetadata(raw) {
  const metadata = {};
  if (!raw || typeof raw !== "object") return metadata;
  for (const [filePath, value] of Object.entries(raw)) {
    metadata[normalizeMountPath(filePath)] = value && typeof value === "object" ? value : {};
  }
  return metadata;
}

function normalizeReadOnlyFields(raw) {
  const fields = {};
  if (!raw || typeof raw !== "object") return fields;
  for (const [prefix, value] of Object.entries(raw)) {
    fields[normalizeMountPath(prefix)] = Array.isArray(value) ? value.map(String) : [];
  }
  return fields;
}

function normalizeMountPath(filePath) {
  const raw = String(filePath ?? "/").trim();
  const withSlash = raw.startsWith("/") ? raw : `/${raw}`;
  return withSlash.replace(/\/+/g, "/").replace(/\/$/, "") || "/";
}

function pathIsWithin(filePath, prefix) {
  const normalizedFile = normalizeMountPath(filePath);
  const normalizedPrefix = normalizeMountPath(prefix);
  if (normalizedPrefix === "/") return true;
  return normalizedFile === normalizedPrefix || normalizedFile.startsWith(`${normalizedPrefix}/`);
}

async function pathExists(filePath) {
  try {
    await stat(filePath);
    return true;
  } catch (error) {
    if (error?.code === "ENOENT") return false;
    throw error;
  }
}

function recordFileError(state, op, virtualPath, error, operation) {
  const reason = error?.code === "ENOENT" ? "ENOENT" : String(error?.message ?? error);
  state.sideEffects.push({ kind: "error", op, path: virtualPath, reason });
  state.toolCalls.push({ name: op, path: virtualPath, error: reason });
  state.contentLines.push(`${label(operation)}${op} ${virtualPath}: ${reason === "ENOENT" ? "file not found" : reason}`);
}

function hasPropertyPath(value, dottedPath) {
  const segments = String(dottedPath).split(".").filter(Boolean);
  if (segments.length === 0) return false;
  let current = value;
  for (const segment of segments) {
    if (current === null || typeof current !== "object") return false;
    if (!Object.prototype.hasOwnProperty.call(current, segment)) return false;
    current = current[segment];
  }
  return true;
}

function tryParseJson(content) {
  try {
    return JSON.parse(content);
  } catch {
    return undefined;
  }
}

function label(operation) {
  return operation.id ? `[${operation.id}] ` : "";
}

function preview(content) {
  return String(content).replace(/\s+/g, " ").trim().slice(0, 180);
}

function compareStrings(left, right) {
  return left.localeCompare(right, "en");
}
