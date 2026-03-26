/**
 * Semantic metadata model.
 *
 * Extract from workspace.ts:
 *   - normalizeSemantics()
 *   - isZeroSemantics()
 *   - semantic merge logic during file updates
 */

import type { FileSemantics } from "./storage.js";

const MAX_ARRAY_ENTRIES = 100;
const MAX_PROPERTIES_ENTRIES = 100;

export function normalizeSemantics(semantics?: FileSemantics): FileSemantics {
  return {
    properties: normalizeProperties(semantics?.properties),
    relations: normalizeStringArray(semantics?.relations),
    permissions: normalizeStringArray(semantics?.permissions),
    comments: normalizeStringArray(semantics?.comments),
  };
}

export function parseSemantics(raw?: string | null): FileSemantics {
  if (!raw) {
    return normalizeSemantics();
  }
  try {
    return normalizeSemantics(JSON.parse(raw) as FileSemantics);
  } catch {
    return normalizeSemantics();
  }
}

export function normalizeProperties(
  input?: Record<string, string>,
): Record<string, string> | undefined {
  if (!input) {
    return undefined;
  }

  const out: Record<string, string> = {};
  let count = 0;
  for (const [key, value] of Object.entries(input)) {
    if (count >= MAX_PROPERTIES_ENTRIES) break;
    const normalizedKey = key.trim();
    if (!normalizedKey) {
      continue;
    }
    out[normalizedKey] = String(value).trim();
    count++;
  }

  return Object.keys(out).length > 0 ? out : undefined;
}

export function normalizeStringArray(values?: string[]): string[] | undefined {
  if (!values || values.length === 0) {
    return undefined;
  }

  const normalized = Array.from(
    new Set(values.map((value) => value.trim()).filter(Boolean)),
  );
  normalized.sort((a, b) => a.localeCompare(b));
  const limited =
    normalized.length > MAX_ARRAY_ENTRIES
      ? normalized.slice(0, MAX_ARRAY_ENTRIES)
      : normalized;
  return limited.length > 0 ? limited : undefined;
}

export function isZeroSemantics(semantics: FileSemantics): boolean {
  const normalized = normalizeSemantics(semantics);
  return (
    !normalized.properties &&
    !normalized.relations &&
    !normalized.permissions &&
    !normalized.comments
  );
}

export function mergeSemantics(existing: FileSemantics, incoming: FileSemantics): FileSemantics {
  const a = normalizeSemantics(existing);
  const b = normalizeSemantics(incoming);
  return {
    properties: mergeProperties(a.properties, b.properties),
    relations: mergeStringArrays(a.relations, b.relations),
    permissions: mergeStringArrays(a.permissions, b.permissions),
    comments: mergeStringArrays(a.comments, b.comments),
  };
}

function mergeProperties(
  existing?: Record<string, string>,
  incoming?: Record<string, string>,
): Record<string, string> | undefined {
  if (!existing && !incoming) return undefined;
  if (!existing) return incoming;
  if (!incoming) return existing;
  const merged = { ...existing, ...incoming };
  return Object.keys(merged).length > 0 ? merged : undefined;
}

function mergeStringArrays(
  existing?: string[],
  incoming?: string[],
): string[] | undefined {
  if (!existing && !incoming) return undefined;
  return normalizeStringArray([...(existing ?? []), ...(incoming ?? [])]);
}
