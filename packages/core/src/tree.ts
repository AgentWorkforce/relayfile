/**
 * Tree listing and directory traversal.
 *
 * Extract from workspace.ts:
 *   - listTree() / handleListTree()
 *   - directory inference from file paths
 *   - depth limiting
 *   - cursor pagination
 *   - ancestor/descendant path utilities
 */

import type { StorageAdapter, PaginationOptions } from "./storage.js";
import {
  filePermissionAllows,
  resolveFilePermissions,
  type TokenClaims,
} from "./acl.js";

export interface TreeEntry {
  path: string;
  type: "file" | "dir";
  revision?: string;
  provider?: string;
  size?: number;
  updatedAt?: string;
  propertyCount?: number;
  relationCount?: number;
  commentCount?: number;
}

export interface TreeResult {
  path: string;
  entries: TreeEntry[];
  nextCursor: string | null;
}

export interface ListTreeOptions extends PaginationOptions {
  path?: string;
  depth?: number;
}

export function listTree(
  storage: StorageAdapter,
  options: ListTreeOptions,
  claims: TokenClaims | null,
): TreeResult {
  const base = normalizePath(options.path ?? "/");
  const maxDepth = options.depth && options.depth > 0 ? options.depth : 1;
  const workspaceId = storage.getWorkspaceId();
  const entryMap = new Map<string, TreeEntry>();

  for (const row of storage.listFiles()) {
    const filePath = normalizePath(row.path);
    if (filePath === base || !isWithinBase(filePath, base)) {
      continue;
    }
    if (
      !filePermissionAllows(
        resolveFilePermissions(storage, filePath, true),
        workspaceId,
        claims,
      )
    ) {
      continue;
    }

    const rest = filePath.slice(base === "/" ? 1 : base.length + 1);
    if (!rest) {
      continue;
    }

    const parts = rest.split("/").filter(Boolean);
    for (let level = 1; level <= Math.min(parts.length, maxDepth); level += 1) {
      const childPath = joinPath(base, parts.slice(0, level).join("/"));
      if (level === parts.length) {
        entryMap.set(childPath, {
          path: childPath,
          type: "file",
          revision: row.revision,
          provider: row.provider,
          size: encodedSize(row.content, row.encoding),
          updatedAt: row.lastEditedAt,
          propertyCount: Object.keys(row.semantics.properties ?? {}).length,
          relationCount: row.semantics.relations?.length ?? 0,
          commentCount: row.semantics.comments?.length ?? 0,
        });
      } else if (!entryMap.has(childPath)) {
        entryMap.set(childPath, {
          path: childPath,
          type: "dir",
          revision: "dir",
        });
      }
    }
  }

  let entries = Array.from(entryMap.values()).sort((left, right) =>
    left.path.localeCompare(right.path),
  );
  if (options.cursor) {
    const index = entries.findIndex((entry) => entry.path === options.cursor);
    if (index >= 0) {
      entries = entries.slice(index + 1);
    }
  }

  return {
    path: base,
    entries,
    nextCursor: null,
  };
}

export function ancestorDirectories(path: string): string[] {
  const normalized = normalizePath(path);
  const parts = normalized.split("/").filter(Boolean);
  const dirs = ["/"];
  let current = "";

  for (let index = 0; index < Math.max(0, parts.length - 1); index += 1) {
    current = joinPath(current || "/", parts[index] ?? "");
    dirs.push(current);
  }

  return dirs;
}

export function joinPath(dir: string, filename: string): string {
  const normalizedDir = normalizePath(dir);
  return normalizedDir === "/"
    ? normalizePath(`/${filename}`)
    : normalizePath(`${normalizedDir}/${filename}`);
}

function normalizePath(path: string): string {
  const trimmed = path.trim();
  if (!trimmed) {
    return "/";
  }
  const prefixed = trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
  return prefixed.length > 1 ? prefixed.replace(/\/+$/, "") : "/";
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
