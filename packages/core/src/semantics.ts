/**
 * Semantic metadata model.
 *
 * Extract from workspace.ts:
 *   - normalizeSemantics()
 *   - isZeroSemantics()
 *   - semantic merge logic during file updates
 */

import type { FileSemantics } from "./storage.js";

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
  for (const [key, value] of Object.entries(input)) {
    const normalizedKey = key.trim();
    if (!normalizedKey) {
      continue;
    }
    out[normalizedKey] = String(value).trim();
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
  return normalized.length > 0 ? normalized : undefined;
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
  void existing;
  return normalizeSemantics(incoming);
}
