/**
 * File query and filtering engine.
 *
 * Extract from workspace.ts:
 *   - handleQueryFiles() / queryFiles()
 *   - property, relation, permission, comment, path prefix filters
 *   - cursor pagination
 */

import type { StorageAdapter, FileRow, Paginated, PaginationOptions } from "./storage.js";
import {
  filePermissionAllows,
  resolveFilePermissions,
  type TokenClaims,
} from "./acl.js";

export interface QueryOptions extends PaginationOptions {
  path?: string;
  provider?: string;
  properties?: Record<string, string>;
  relation?: string;
  permission?: string;
  comment?: string;
}

export interface QueryResultItem {
  path: string;
  revision: string;
  contentType: string;
  provider: string;
  lastEditedAt: string;
  size: number;
  properties?: Record<string, string>;
  relations?: string[];
  comments?: string[];
}

export function queryFiles(
  storage: StorageAdapter,
  options: QueryOptions,
  claims: TokenClaims | null,
): Paginated<QueryResultItem> {
  const base = normalizePath(options.path ?? "/");
  const provider = normalizeProvider(options.provider);
  const relation = options.relation?.trim() ?? "";
  const permission = options.permission?.trim() ?? "";
  const comment = options.comment?.trim() ?? "";
  const limit = Math.max(1, Math.min(options.limit ?? 100, 1000));
  const expectedProperties = normalizeProperties(options.properties);
  const workspaceId = storage.getWorkspaceId();

  let rows = storage
    .listFiles()
    .map(normalizeFileRow)
    .filter((row) => row.path === base || isWithinBase(row.path, base))
    .sort((left, right) => left.path.localeCompare(right.path));

  if (options.cursor) {
    const index = rows.findIndex((row) => row.path === normalizePath(options.cursor ?? "/"));
    if (index < 0) {
      return { items: [], nextCursor: null };
    }
    rows = rows.slice(index + 1);
  }

  const items: QueryResultItem[] = [];
  for (const row of rows) {
    if (provider && normalizeProvider(row.provider) !== provider) {
      continue;
    }

    const semantics = row.semantics;
    if (relation && !stringSliceContains(semantics.relations, relation)) {
      continue;
    }

    const effectivePermissions = resolveFilePermissions(storage, row.path, true);
    if (permission && !stringSliceContainsExact(effectivePermissions, permission)) {
      continue;
    }
    if (comment && !stringSliceContains(semantics.comments, comment)) {
      continue;
    }
    if (!propertiesMatch(semantics.properties, expectedProperties)) {
      continue;
    }
    if (!filePermissionAllows(effectivePermissions, workspaceId, claims)) {
      continue;
    }

    items.push({
      path: row.path,
      revision: row.revision,
      contentType: row.contentType,
      provider: row.provider,
      lastEditedAt: row.lastEditedAt,
      size: encodedSize(row.content, row.encoding),
      properties: semantics.properties,
      relations: semantics.relations,
      comments: semantics.comments,
    });

    if (items.length >= limit) {
      break;
    }
  }

  return {
    items,
    nextCursor:
      items.length >= limit ? (items[items.length - 1]?.path ?? null) : null,
  };
}

function normalizeFileRow(row: FileRow): FileRow {
  return {
    ...row,
    path: normalizePath(row.path),
    provider: normalizeProvider(row.provider),
  };
}

function normalizePath(path: string): string {
  const trimmed = path.trim();
  if (!trimmed) {
    return "/";
  }
  const prefixed = trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
  return prefixed.length > 1 ? prefixed.replace(/\/+$/, "") : "/";
}

function normalizeProvider(provider?: string): string {
  return provider?.trim().toLowerCase() ?? "";
}

function normalizeProperties(
  input?: Record<string, string>,
): Record<string, string> | undefined {
  if (!input) {
    return undefined;
  }

  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(input)) {
    const normalizedKey = key.trim();
    if (!normalizedKey) {
      continue;
    }
    out[normalizedKey] = String(value).trim();
  }

  return Object.keys(out).length > 0 ? out : undefined;
}

function stringSliceContains(
  values: string[] | undefined,
  needle: string,
): boolean {
  const normalizedNeedle = needle.trim();
  if (!normalizedNeedle) {
    return false;
  }
  return Boolean(values?.some((value) => value.trim() === normalizedNeedle));
}

function stringSliceContainsExact(
  values: string[] | undefined,
  needle: string,
): boolean {
  return stringSliceContains(values, needle);
}

function propertiesMatch(
  actual: Record<string, string> | undefined,
  expected: Record<string, string> | undefined,
): boolean {
  if (!expected || Object.keys(expected).length === 0) {
    return true;
  }
  if (!actual) {
    return false;
  }

  return Object.entries(expected).every(([key, value]) => actual[key] === value);
}

function isWithinBase(path: string, base: string): boolean {
  return base === "/" ? path.startsWith("/") : path.startsWith(`${base}/`);
}

function encodedSize(content: string, encoding: string): number {
  if (encoding === "base64") {
    try {
      return Uint8Array.from(atob(content), (char) => char.charCodeAt(0))
        .byteLength;
    } catch {
      return content.length;
    }
  }

  return new TextEncoder().encode(content).byteLength;
}
