import { RelayFileClient } from './client.js';
import { execSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import * as tar from 'tar';

interface BulkWriteResponseShape {
  written?: number;
  errorCount?: number;
  errors?: unknown;
}

interface SeedFile {
  path: string;
  content: string;
  encoding?: 'utf-8' | 'base64';
}

interface SeedFileResult {
  written: number;
  errorCount: number;
  errors: unknown;
}

const DEFAULT_EXCLUDED_DIRS = ['.relay', '.git', 'node_modules'];
const DEFAULT_EXCLUDED_FILES = new Set(['.relayfile-mount-state.json']);
const BATCH_SIZE = 50;
const utf8Decoder = new TextDecoder('utf-8', { fatal: true });

interface WorkflowAclAgent {
  name: string;
  acl: Record<string, string[]>;
}

interface SeedWorkflowAclsOptions {
  relayfileUrl: string;
  adminToken: string;
  workspace: string;
  agents: WorkflowAclAgent[];
}

function normalizeBaseUrl(baseUrl: string): string {
  const url = String(baseUrl ?? '').trim();
  let end = url.length;
  while (end > 0 && url.charCodeAt(end - 1) === 0x2f) {
    end--;
  }
  return end === url.length ? url : url.slice(0, end);
}

function normalizeWorkspaceId(workspaceId: string): string {
  const value = String(workspaceId ?? '').trim();
  if (!value) {
    throw new Error('workspaceId is required');
  }
  return value;
}

function normalizeExcludeDirs(excludeDirs: string[]): Set<string> {
  const result = new Set<string>();
  for (const dir of excludeDirs) {
    const normalized = String(dir ?? '')
      .trim()
      .replace(/^[/\\]+|[/\\]+$/g, '');
    if (!normalized) {
      continue;
    }
    result.add(normalized);
  }
  return result;
}

function normalizeAclDirectory(dirPath: string): string {
  const normalized = String(dirPath ?? '')
    .trim()
    .replace(/\\/gu, '/')
    .replace(/\/+$/u, '');

  if (!normalized || normalized === '/') {
    return '/';
  }

  return normalized.startsWith('/') ? normalized : `/${normalized}`;
}

function isReviewerAgent(agentName: string): boolean {
  return /reviewer/iu.test(String(agentName ?? '').trim());
}

function createClient(baseUrl: string, token: string): RelayFileClient {
  return new RelayFileClient({
    baseUrl: normalizeBaseUrl(baseUrl),
    token,
    retry: { maxRetries: 0 },
  });
}

function isUtf8(raw: Buffer): boolean {
  try {
    utf8Decoder.decode(raw);
    return true;
  } catch {
    return false;
  }
}

function buildSeedFilePayload(filePath: string, rootDir: string): SeedFile {
  const relative = path.relative(rootDir, filePath).split(path.sep).join('/');
  const raw = fs.readFileSync(filePath);
  if (isUtf8(raw)) {
    return { path: `/${relative}`, content: raw.toString('utf8'), encoding: 'utf-8' };
  }
  return { path: `/${relative}`, content: raw.toString('base64'), encoding: 'base64' };
}

function collectSeedPaths(
  rootDir: string,
  currentRelative: string,
  excludeDirs: Set<string>,
  output: string[],
  visited: Set<string> = new Set()
): void {
  const absoluteDir = path.join(rootDir, currentRelative);
  let realDir: string;
  try {
    realDir = fs.realpathSync(absoluteDir);
  } catch {
    return;
  }
  if (visited.has(realDir)) {
    // Cycle guard: a symlinked directory pointed back to an ancestor.
    return;
  }
  visited.add(realDir);

  const entries = fs.readdirSync(absoluteDir, { withFileTypes: true });

  for (const entry of entries) {
    if (excludeDirs.has(entry.name)) {
      continue;
    }
    if (DEFAULT_EXCLUDED_FILES.has(entry.name)) {
      continue;
    }

    const nextRelative = currentRelative ? `${currentRelative}/${entry.name}` : entry.name;
    const absolutePath = path.join(rootDir, nextRelative);

    if (excludeDirs.has(nextRelative)) {
      continue;
    }

    if (entry.isDirectory()) {
      collectSeedPaths(rootDir, nextRelative, excludeDirs, output, visited);
      continue;
    }

    if (entry.isFile()) {
      output.push(absolutePath);
      continue;
    }

    if (entry.isSymbolicLink()) {
      try {
        const resolved = fs.realpathSync(absolutePath);
        if (!resolved.startsWith(rootDir + path.sep) && resolved !== rootDir) {
          continue;
        }
        const stat = fs.statSync(resolved);
        if (stat.isDirectory()) {
          collectSeedPaths(rootDir, nextRelative, excludeDirs, output, visited);
          continue;
        }
        if (stat.isFile()) {
          output.push(absolutePath);
        }
      } catch {
        // Ignore symlinks that cannot be resolved.
      }
    }
  }
}

function parseBulkWriteResponse(payload: unknown): SeedFileResult {
  if (!payload || typeof payload !== 'object') {
    return { written: 0, errorCount: 0, errors: [] };
  }
  const parsed = payload as BulkWriteResponseShape;
  return {
    written: typeof parsed.written === 'number' ? parsed.written : 0,
    errorCount: typeof parsed.errorCount === 'number' ? parsed.errorCount : 0,
    errors: parsed.errors ?? [],
  };
}

async function postBulkWrite(
  baseUrl: string,
  token: string,
  workspaceId: string,
  files: SeedFile[],
  correlationId: string
): Promise<SeedFileResult> {
  const response = await fetch(
    `${normalizeBaseUrl(baseUrl)}/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/bulk`,
    {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${token}`,
        'Content-Type': 'application/json',
        'X-Correlation-Id': correlationId,
      },
      body: JSON.stringify({ files }),
    }
  );

  const body = await response.text();
  if (!response.ok) {
    throw new Error(`failed to seed workspace ${workspaceId}: HTTP ${response.status} ${body}`.trim());
  }

  if (!body) {
    return { written: files.length, errorCount: 0, errors: [] };
  }
  try {
    return parseBulkWriteResponse(JSON.parse(body));
  } catch {
    return { written: files.length, errorCount: 0, errors: [] };
  }
}

async function writeBulkWrite(
  baseUrl: string,
  token: string,
  workspaceId: string,
  files: SeedFile[],
  correlationId: string
): Promise<SeedFileResult> {
  const client = createClient(baseUrl, token);
  try {
    const response = await client.bulkWrite({
      workspaceId,
      files,
      correlationId,
    });
    return parseBulkWriteResponse(response);
  } catch (error) {
    if (typeof (error as { status?: number }).status === 'number') {
      throw error;
    }
  }

  return postBulkWrite(baseUrl, token, workspaceId, files, correlationId);
}

export async function createWorkspaceIfNeeded(
  baseUrl: string,
  token: string,
  workspaceId: string
): Promise<void> {
  const workspace = normalizeWorkspaceId(workspaceId);
  const client = createClient(baseUrl, token);

  const maybeCreateWorkspace = client as unknown as {
    createWorkspace?: (...input: unknown[]) => Promise<unknown>;
  };
  if (typeof maybeCreateWorkspace.createWorkspace === 'function') {
    for (const arg of [workspace, { id: workspace }, { workspaceId: workspace }, { name: workspace }]) {
      try {
        await maybeCreateWorkspace.createWorkspace(arg);
        return;
      } catch {
        // Continue to the next overload candidate, then fallback to HTTP.
      }
    }
  }

  const endpoint = `${normalizeBaseUrl(baseUrl)}/v1/workspaces`;
  const bodyCandidates: Array<Record<string, string>> = [
    { name: workspace },
    { workspace: workspace },
    { workspaceId: workspace },
    { id: workspace },
  ];
  let lastFailure: string | null = null;

  for (const body of bodyCandidates) {
    try {
      const response = await fetch(endpoint, {
        method: 'POST',
        headers: {
          Authorization: `Bearer ${token}`,
          'Content-Type': 'application/json',
          'X-Correlation-Id': `create-workspace-${Date.now()}`,
        },
        body: JSON.stringify(body),
      });

      if (
        response.status === 200 ||
        response.status === 201 ||
        response.status === 204 ||
        response.status === 409
      ) {
        return;
      }

      const responseBody = await response.text().catch(() => '');
      lastFailure = `HTTP ${response.status} ${responseBody}`.trim();
      if (response.status < 500 && response.status !== 409) {
        continue;
      }
    } catch (error) {
      lastFailure = String(error);
    }
  }

  if (lastFailure) {
    throw new Error(`Failed to create workspace ${workspace}: ${lastFailure}`);
  }
}

export async function seedAclRules(
  baseUrl: string,
  token: string,
  workspaceId: string,
  aclRules: Record<string, string[]>
): Promise<void> {
  const workspace = normalizeWorkspaceId(workspaceId);
  const files = Object.entries(aclRules).map(([dirPath, rules]) => {
    const normalizedDir = String(dirPath ?? '')
      .trim()
      .replace(/\/+$/, '');
    const aclPath =
      normalizedDir === '' || normalizedDir === '/' ? '/.relayfile.acl' : `${normalizedDir}/.relayfile.acl`;
    return {
      path: aclPath,
      content: JSON.stringify({ semantics: { permissions: rules } }),
      encoding: 'utf-8' as const,
    };
  });

  if (files.length === 0) {
    return;
  }

  const result = await writeBulkWrite(
    baseUrl,
    token,
    workspace,
    files,
    `seed-acl-${workspace}-${Date.now()}`
  );
  if (result.errorCount > 0) {
    const details = result.errors ? JSON.stringify(result.errors) : '[]';
    throw new Error(`ACL seeding had ${result.errorCount} error(s) for workspace ${workspace}: ${details}`);
  }
}

export async function seedWorkspace(
  baseUrl: string,
  token: string,
  workspaceId: string,
  projectDir: string,
  excludeDirs: string[]
): Promise<number> {
  const workspace = normalizeWorkspaceId(workspaceId);
  const rootDir = path.resolve(projectDir);
  const excludes = normalizeExcludeDirs([...DEFAULT_EXCLUDED_DIRS, ...excludeDirs]);
  const seedPaths: string[] = [];
  collectSeedPaths(rootDir, '', excludes, seedPaths);
  const allFiles = seedPaths
    .sort((left, right) => left.localeCompare(right))
    .map((filePath) => buildSeedFilePayload(filePath, rootDir));

  let seededCount = 0;
  let totalErrorCount = 0;
  const collectedErrors: unknown[] = [];
  for (let index = 0; index < allFiles.length; index += BATCH_SIZE) {
    const batch = allFiles.slice(index, index + BATCH_SIZE);
    const batchIndex = Math.floor(index / BATCH_SIZE);
    const result = await writeBulkWrite(
      baseUrl,
      token,
      workspace,
      batch,
      `seed-workspace-${workspace}-${Date.now()}-${batchIndex}`
    );
    seededCount += result.written;
    totalErrorCount += result.errorCount;
    if (Array.isArray(result.errors)) {
      collectedErrors.push(...result.errors);
    } else if (result.errors && result.errorCount > 0) {
      collectedErrors.push(result.errors);
    }
  }

  if (totalErrorCount > 0) {
    const details = collectedErrors.length > 0 ? JSON.stringify(collectedErrors) : '[]';
    throw new Error(
      `seedWorkspace had ${totalErrorCount} error(s) for workspace ${workspace}: ${details}`
    );
  }

  return seededCount;
}

function buildWorkflowAclRules(agents: WorkflowAclAgent[]): Record<string, string[]> {
  const directories = new Set<string>();
  const normalizedAgents = agents.map((agent) => ({
    name: String(agent.name ?? '').trim(),
    acl: Object.fromEntries(
      Object.entries(agent.acl ?? {}).map(([dirPath, rules]) => [
        normalizeAclDirectory(dirPath),
        Array.isArray(rules) ? rules : [],
      ])
    ),
  }));
  const reviewerNames = normalizedAgents
    .map((agent) => agent.name)
    .filter((name) => name !== '' && isReviewerAgent(name));

  for (const agent of normalizedAgents) {
    for (const dirPath of Object.keys(agent.acl)) {
      directories.add(dirPath);
    }
  }

  const merged = new Map<string, Set<string>>();

  for (const dirPath of [...directories].sort((left, right) => left.localeCompare(right))) {
    const rules = new Set<string>();

    for (const reviewerName of reviewerNames) {
      rules.add(`allow:agent:${reviewerName}:read`);
    }

    for (const agent of normalizedAgents) {
      if (!agent.name) {
        continue;
      }

      const agentRules = agent.acl[dirPath] ?? [];
      const hasRead = agentRules.includes('read') || agentRules.includes('write');
      const hasWrite = agentRules.includes('write');

      if (hasRead) {
        rules.add(`allow:agent:${agent.name}:read`);
      } else if (!isReviewerAgent(agent.name)) {
        rules.add(`deny:agent:${agent.name}`);
      }

      if (hasWrite) {
        rules.add(`allow:agent:${agent.name}:write`);
      }
    }

    if (rules.size > 0) {
      merged.set(dirPath, rules);
    }
  }

  return Object.fromEntries([...merged.entries()].map(([dirPath, rules]) => [dirPath, [...rules].sort()]));
}

export async function seedWorkflowAcls({
  relayfileUrl,
  adminToken,
  workspace,
  agents,
}: SeedWorkflowAclsOptions): Promise<void> {
  const aclRules = buildWorkflowAclRules(agents);

  if (Object.keys(aclRules).length === 0) {
    return;
  }

  await seedAclRules(relayfileUrl, adminToken, workspace, aclRules);
}

// ── Tar-based bulk upload ───────────────────────────────────────────────────

interface ImportResponseShape {
  imported?: number;
}

/**
 * Test whether a workspace-relative file path is covered by an exclude entry.
 *
 * Exclude entries can be either a single directory/file name (matched against
 * any path segment) or a nested relative path like `build/output` (matched
 * against any contiguous run of segments at any depth). Both single-name and
 * nested matches must align on segment boundaries.
 */
function isExcludedRelativePath(relativePath: string, excludes: Set<string>): boolean {
  const segments = relativePath.split('/');
  if (DEFAULT_EXCLUDED_FILES.has(segments[segments.length - 1] ?? '')) {
    return true;
  }
  for (const exclude of excludes) {
    if (!exclude) continue;
    if (!exclude.includes('/')) {
      if (segments.some((seg) => seg === exclude)) {
        return true;
      }
      continue;
    }
    const excludeSegments = exclude.split('/').filter(Boolean);
    if (excludeSegments.length === 0) continue;
    const limit = segments.length - excludeSegments.length;
    for (let start = 0; start <= limit; start++) {
      let matched = true;
      for (let i = 0; i < excludeSegments.length; i++) {
        if (segments[start + i] !== excludeSegments[i]) {
          matched = false;
          break;
        }
      }
      if (matched) {
        return true;
      }
    }
  }
  return false;
}

function getGitTrackedFiles(rootDir: string): string[] | null {
  try {
    const output = execSync('git ls-files -z --cached --others --exclude-standard', {
      cwd: rootDir,
      encoding: 'utf-8',
      maxBuffer: 50 * 1024 * 1024,
    });
    const files = output.split('\0').filter(Boolean);
    return files;
  } catch {
    return null;
  }
}

function collectAllFiles(rootDir: string, excludeDirs: Set<string>): string[] {
  const files: string[] = [];
  const stack = [''];

  while (stack.length > 0) {
    const currentRelative = stack.pop()!;
    const absoluteDir = path.join(rootDir, currentRelative);
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(absoluteDir, { withFileTypes: true });
    } catch {
      continue;
    }

    for (const entry of entries) {
      if (excludeDirs.has(entry.name)) continue;
      if (DEFAULT_EXCLUDED_FILES.has(entry.name)) continue;
      const nextRelative = currentRelative ? `${currentRelative}/${entry.name}` : entry.name;
      if (excludeDirs.has(nextRelative)) continue;

      if (entry.isDirectory()) {
        stack.push(nextRelative);
      } else if (entry.isFile()) {
        files.push(nextRelative);
      }
    }
  }

  return files;
}

async function createTarBuffer(rootDir: string, files: string[]): Promise<Buffer> {
  const tarStream = tar.create({ gzip: true, cwd: rootDir, portable: true, follow: true }, files);
  const chunks: Buffer[] = [];
  for await (const chunk of tarStream) {
    chunks.push(Buffer.from(chunk as Uint8Array));
  }
  return Buffer.concat(chunks);
}

export async function seedWorkspaceTar(
  baseUrl: string,
  token: string,
  workspaceId: string,
  projectDir: string,
  excludeDirs: string[]
): Promise<number> {
  const workspace = normalizeWorkspaceId(workspaceId);
  const rootDir = path.resolve(projectDir);
  const excludes = normalizeExcludeDirs([...DEFAULT_EXCLUDED_DIRS, ...excludeDirs]);

  const gitFiles = getGitTrackedFiles(rootDir);
  const rawFiles = gitFiles ?? collectAllFiles(rootDir, excludes);
  const files = gitFiles ? rawFiles.filter((f) => !isExcludedRelativePath(f, excludes)) : rawFiles;

  if (files.length === 0) {
    return 0;
  }

  const tarball = await createTarBuffer(rootDir, files);
  // Detach into a plain Uint8Array view so we don't expose the underlying
  // Node Buffer pool through the request body.
  const body = new Uint8Array(tarball.buffer, tarball.byteOffset, tarball.byteLength).slice();

  const url = `${normalizeBaseUrl(baseUrl)}/v1/workspaces/${encodeURIComponent(workspace)}/fs/import`;
  const response = await fetch(url, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/gzip',
      'X-Correlation-Id': `seed-tar-${workspace}-${Date.now()}`,
    },
    body,
  });

  if (response.status === 404) {
    // Tar import not supported — fall back to batch upload
    return seedWorkspace(baseUrl, token, workspaceId, projectDir, excludeDirs);
  }

  if (!response.ok) {
    const body = await response.text().catch(() => '');
    throw new Error(`tar import failed for workspace ${workspace}: HTTP ${response.status} ${body}`.trim());
  }

  const raw = await response.text();
  if (!raw.trim()) {
    return files.length;
  }

  try {
    const parsed = JSON.parse(raw) as ImportResponseShape;
    return typeof parsed.imported === 'number' ? parsed.imported : files.length;
  } catch {
    return files.length;
  }
}
