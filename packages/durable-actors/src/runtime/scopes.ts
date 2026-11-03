/**
 * Scope matching, ported from internal/httpapi/auth.go so the actor backend
 * grants exactly what the Go server grants.
 */

export type ScopeSet = ReadonlySet<string>;

export function scopeMatches(granted: ScopeSet, required: string): boolean {
  if (granted.has(required)) return true;
  const idx = required.indexOf(":");
  if (idx < 0) return false;
  const resource = required.slice(0, idx);
  const action = required.slice(idx + 1);
  for (const scope of granted) {
    const segments = splitN(scope, ":", 4);
    if (segments.length < 3) continue;
    const [plane, res, act] = segments as [string, string, string];
    if (plane !== "relayfile" && plane !== "*") continue;
    if (res !== resource && res !== "*") continue;
    if (act !== action && act !== "*") {
      if (act !== "manage" || (action !== "read" && action !== "write")) continue;
    }
    return true;
  }
  return false;
}

export function scopeMatchesPath(granted: ScopeSet, required: string, filePath: string): boolean {
  filePath = filePath.trim();
  const idx = required.indexOf(":");
  if (idx < 0) return false;
  const resource = required.slice(0, idx);
  const action = required.slice(idx + 1);
  let hasNarrowPathGrant = false;
  for (const scope of granted) {
    const scopePath = pathScopeForRequired(scope, resource, action);
    if (scopePath === null) continue;
    if (scopePath === "*") return true;
    hasNarrowPathGrant = true;
    if (filePath === "") continue;
    if (scopePathMatches(scopePath, filePath)) return true;
  }
  if (granted.has(required)) return !hasNarrowPathGrant;
  return false;
}

/** True when the caller holds an unrestricted (not path-narrowed) grant. */
export function hasUnrestrictedScope(granted: ScopeSet, required: string): boolean {
  return scopeMatchesPath(granted, required, "") || (scopeMatches(granted, required) && !hasNarrowGrant(granted, required));
}

function hasNarrowGrant(granted: ScopeSet, required: string): boolean {
  const idx = required.indexOf(":");
  if (idx < 0) return false;
  for (const scope of granted) {
    const scopePath = pathScopeForRequired(scope, required.slice(0, idx), required.slice(idx + 1));
    if (scopePath !== null && scopePath !== "*") return true;
  }
  return false;
}

function pathScopeForRequired(scope: string, resource: string, action: string): string | null {
  const segments = splitN(scope, ":", 4);
  if (segments.length < 3) return null;
  switch (segments[0]) {
    case "relayfile":
    case "*": {
      const res = segments[1]!;
      const act = segments[2]!;
      if (res !== resource && res !== "*") return null;
      if (!scopeActionMatches(act, action)) return null;
      return segments.length === 4 ? segments[3]!.trim() : "*";
    }
    case "workspace": {
      const act = segments[2]!;
      if (resource !== "fs" || !scopeActionMatches(act, action)) return null;
      return segments.length === 4 ? segments[3]!.trim() : "*";
    }
    default:
      return null;
  }
}

function scopeActionMatches(granted: string, required: string): boolean {
  if (granted === required || granted === "*") return true;
  return granted === "manage" && (required === "read" || required === "write");
}

export function scopePathMatches(scopePath: string, filePath: string): boolean {
  if (scopePath === filePath) return true;
  if (scopePath.endsWith("/**")) {
    const dir = scopePath.slice(0, -3);
    return filePath === dir || filePath.startsWith(`${dir}/`);
  }
  if (scopePath.endsWith("/*")) {
    return filePath.startsWith(`${scopePath.slice(0, -2)}/`);
  }
  if (scopePath.endsWith("*")) {
    return filePath.startsWith(scopePath.slice(0, -1));
  }
  return false;
}

/** Go's strings.SplitN semantics. */
function splitN(value: string, sep: string, n: number): string[] {
  const out: string[] = [];
  let rest = value;
  while (out.length < n - 1) {
    const idx = rest.indexOf(sep);
    if (idx < 0) break;
    out.push(rest.slice(0, idx));
    rest = rest.slice(idx + sep.length);
  }
  out.push(rest);
  return out;
}

export function parseScopes(value: unknown): Set<string> {
  const out = new Set<string>();
  if (Array.isArray(value)) {
    for (const item of value) if (typeof item === "string" && item) out.add(item);
  } else if (typeof value === "string") {
    for (const item of value.split(/\s+/)) if (item) out.add(item);
  }
  return out;
}
