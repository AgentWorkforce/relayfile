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

/**
 * True when the caller holds `required` for at least some path: an
 * unrestricted grant, or any path-narrowed one (`relayfile:fs:write:/team/**`,
 * `workspace:<ws>:write:/x`). Routes that check each path themselves (bulk
 * writes, the event feed) use it as their coarse gate.
 */
export function hasAnyScopeGrant(granted: ScopeSet, required: string): boolean {
  return scopeMatches(granted, required) || scopeMatchesPath(granted, required, "") || hasNarrowGrant(granted, required);
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

/**
 * One `scope:<value>` ACL rule against the caller's grants, ported from
 * aclScopeRuleMatches in internal/httpapi/acl.go. Filesystem rules are
 * semantic: `relayfile:fs:read:/team/**` matches a caller holding the broader
 * `relayfile:fs:read:*` for a path under /team; any other value must be held
 * literally.
 */
export function aclScopeRuleMatches(granted: ScopeSet, scope: string, action: "read" | "write", path: string): boolean {
  const parsed = parseAclFilesystemScope(scope);
  if (!parsed) return granted.has(scope);
  if (!scopeActionMatches(parsed.action, action)) return false;
  if (parsed.path !== "*" && !scopePathMatches(parsed.path, path)) return false;
  return scopeMatchesPath(granted, `fs:${action}`, path);
}

const ACL_WORKSPACE_RESOURCE = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/;
const isFsAction = (action: string) => action === "read" || action === "write" || action === "manage" || action === "*";

function parseAclFilesystemScope(scope: string): { action: string; path: string } | null {
  const segments = splitN(scope, ":", 4);
  if (segments.length === 2 && segments[0] === "fs") return isFsAction(segments[1]!) ? { action: segments[1]!, path: "*" } : null;
  if (segments.length !== 4) return null;
  const [plane, resource, action, path] = segments as [string, string, string, string];
  if (!path.trim()) return null;
  if (plane === "relayfile" || plane === "*") {
    if (resource !== "fs" && resource !== "*") return null;
  } else if (plane === "workspace") {
    if (!ACL_WORKSPACE_RESOURCE.test(resource)) return null;
  } else {
    return null;
  }
  return isFsAction(action) ? { action, path } : null;
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
