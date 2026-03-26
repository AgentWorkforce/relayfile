/**
 * Shared utility functions used across core modules.
 *
 * Centralises helpers that were previously copy-pasted in
 * files.ts, query.ts, tree.ts, operations.ts, acl.ts, and writeback.ts.
 */

/**
 * Normalize a file path: ensure leading slash, strip trailing slashes,
 * and resolve `.` and `..` segments to prevent path traversal attacks.
 */
export function normalizePath(path: string): string {
  const trimmed = path.trim();
  if (!trimmed) {
    return "/";
  }
  const prefixed = trimmed.startsWith("/") ? trimmed : `/${trimmed}`;

  // Split into segments and resolve . and .. to prevent path traversal
  const segments: string[] = [];
  for (const segment of prefixed.split("/")) {
    if (segment === "." || segment === "") {
      continue;
    }
    if (segment === "..") {
      segments.pop();
    } else {
      segments.push(segment);
    }
  }

  const resolved = "/" + segments.join("/");
  return resolved.length > 1 ? resolved.replace(/\/+$/, "") : "/";
}
