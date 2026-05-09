export function assertRelayfileExpected(testCase, actualInput) {
  const expected = testCase.expected ?? {};
  const actual = normalizeRelayfileActual(actualInput);
  const checks = [];

  for (const filePath of asArray(expected.fileExists)) {
    addCheck(
      checks,
      `fileExists:${filePath}`,
      hasFile(actual, filePath),
      `expected ${filePath} to exist`,
    );
  }

  for (const filePath of asArray(expected.fileNotExists)) {
    addCheck(
      checks,
      `fileNotExists:${filePath}`,
      !hasFile(actual, filePath),
      `expected ${filePath} not to exist`,
    );
  }

  for (const item of asArray(expected.fileContentIncludes)) {
    const { path, value } = objectCheck(item, "fileContentIncludes");
    const content = readSnapshotContent(actual, path);
    addCheck(
      checks,
      `fileContentIncludes:${path}`,
      content.includes(String(value)),
      `expected ${path} content to include ${JSON.stringify(value)}`,
    );
  }

  for (const item of asArray(expected.fileContentEquals)) {
    const { path, value } = objectCheck(item, "fileContentEquals");
    const content = readSnapshotContent(actual, path);
    addCheck(
      checks,
      `fileContentEquals:${path}`,
      content === String(value),
      `expected ${path} content to equal ${JSON.stringify(value)}`,
    );
  }

  for (const item of asArray(expected.fileMetadata)) {
    const { path, key, value } = objectCheck(item, "fileMetadata");
    const metadata = actual.metadata?.[normalizeMountPath(path)] ?? {};
    addCheck(
      checks,
      `fileMetadata:${path}:${key}`,
      deepEqual(metadata[key], value),
      `expected ${path} metadata ${key} to equal ${JSON.stringify(value)}`,
    );
  }

  for (const filePath of asArray(expected.aclDenied)) {
    const normalized = normalizeMountPath(filePath);
    addCheck(
      checks,
      `aclDenied:${normalized}`,
      actual.sideEffects.some((effect) => effect.kind === "denied" && normalizeMountPath(effect.path) === normalized),
      `expected ACL denial for ${normalized}`,
    );
  }

  for (const item of asArray(expected.writebackQueued)) {
    const { adapter, path } = objectCheck(item, "writebackQueued");
    const normalized = normalizeMountPath(path);
    addCheck(
      checks,
      `writebackQueued:${adapter}:${normalized}`,
      actual.sideEffects.some((effect) => (
        effect.kind === "writebackQueued"
        && effect.adapter === adapter
        && normalizeMountPath(effect.path) === normalized
      )),
      `expected writeback queued for ${adapter}:${normalized}`,
    );
  }

  for (const prefix of asArray(expected.noFilesModified)) {
    const normalized = normalizeMountPath(prefix);
    const modified = actual.filesModified.filter((filePath) => pathIsWithin(filePath, normalized));
    addCheck(
      checks,
      `noFilesModified:${normalized}`,
      modified.length === 0,
      `expected no files modified under ${normalized}, got ${modified.join(", ") || "none"}`,
    );
  }

  for (const item of asArray(expected.metricLessThan)) {
    const { left, right } = objectCheck(item, "metricLessThan");
    const leftValue = readMetric(actual, left);
    const rightValue = typeof right === "number" ? right : readMetric(actual, right);
    addCheck(
      checks,
      `metricLessThan:${left}<${right}`,
      leftValue < rightValue,
      `expected metric ${left} (${leftValue}) to be less than ${right} (${rightValue})`,
    );
  }

  for (const item of asArray(expected.metricGreaterThan)) {
    const { left, right } = objectCheck(item, "metricGreaterThan");
    const leftValue = readMetric(actual, left);
    const rightValue = typeof right === "number" ? right : readMetric(actual, right);
    addCheck(
      checks,
      `metricGreaterThan:${left}>${right}`,
      leftValue > rightValue,
      `expected metric ${left} (${leftValue}) to be greater than ${right} (${rightValue})`,
    );
  }

  return checks;
}

function normalizeRelayfileActual(actualInput) {
  const actual = actualInput && typeof actualInput === "object" ? actualInput : {};
  return {
    ...actual,
    mountSnapshot: actual.mountSnapshot && typeof actual.mountSnapshot === "object" ? actual.mountSnapshot : {},
    metadata: actual.metadata && typeof actual.metadata === "object" ? actual.metadata : {},
    sideEffects: Array.isArray(actual.sideEffects) ? actual.sideEffects : [],
    filesModified: Array.isArray(actual.filesModified)
      ? actual.filesModified.map((filePath) => normalizeMountPath(filePath))
      : [],
  };
}

function addCheck(checks, name, passed, message) {
  checks.push({ name, passed: Boolean(passed), message });
}

function asArray(value) {
  if (value === undefined || value === null || value === false) return [];
  return Array.isArray(value) ? value : [value];
}

function objectCheck(value, name) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${name} check must be an object`);
  }
  return value;
}

function hasFile(actual, filePath) {
  return Object.prototype.hasOwnProperty.call(actual.mountSnapshot, normalizeMountPath(filePath));
}

function readSnapshotContent(actual, filePath) {
  const normalized = normalizeMountPath(filePath);
  const entry = actual.mountSnapshot[normalized];
  if (entry === undefined) return "";
  if (typeof entry === "string") return entry;
  if (entry && typeof entry.content === "string") return entry.content;
  return String(entry?.content ?? "");
}

function readMetric(actual, key) {
  const value = actual.metrics?.[String(key)];
  return typeof value === "number" && Number.isFinite(value) ? value : Number.NaN;
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

function deepEqual(left, right) {
  return JSON.stringify(left) === JSON.stringify(right);
}
