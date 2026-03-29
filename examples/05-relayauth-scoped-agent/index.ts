/**
 * Example 05 — Agent with scoped permissions
 *
 * Demonstrates how relayfile tokens restrict agent access using scopes.
 * A token scoped to `fs:read:/github/*` can read GitHub files but cannot
 * write to any path, and cannot read files outside /github/.
 *
 * This example uses two clients:
 *   1. A scoped "reader" agent — can only read /github/*
 *   2. A full-access "admin" agent — can read and write everything
 *
 * Run:  npx tsx index.ts
 * Env:  RELAYFILE_TOKEN_SCOPED  — JWT with scope fs:read:/github/*
 *       RELAYFILE_TOKEN_ADMIN   — JWT with scopes fs:read + fs:write
 *       WORKSPACE_ID            — target workspace ID
 */

import { RelayFileClient, RelayFileApiError } from "@relayfile/sdk";

const scopedToken = process.env.RELAYFILE_TOKEN_SCOPED;
const adminToken = process.env.RELAYFILE_TOKEN_ADMIN;
const workspaceId = process.env.WORKSPACE_ID ?? "ws_demo";

if (!scopedToken || !adminToken) {
  console.error(
    "Set both RELAYFILE_TOKEN_SCOPED and RELAYFILE_TOKEN_ADMIN before running."
  );
  console.error("");
  console.error("Generate tokens with relayauth:");
  console.error(
    '  Scoped:  relayauth sign --workspace ws_demo --agent reader --scope "fs:read:/github/*"'
  );
  console.error(
    '  Admin:   relayauth sign --workspace ws_demo --agent admin --scope "fs:read" --scope "fs:write"'
  );
  process.exit(1);
}

const scopedClient = new RelayFileClient({ token: scopedToken });
const adminClient = new RelayFileClient({ token: adminToken });

// ── 1. Admin writes a file the scoped agent can read ────────────────────────

console.log("── admin: writeFile /github/pulls/99.json ──");

await adminClient.writeFile({
  workspaceId,
  path: "/github/pulls/99.json",
  baseRevision: "*",
  content: JSON.stringify(
    {
      number: 99,
      title: "Upgrade to Node 22",
      state: "open",
      user: { login: "khaliqgant" },
    },
    null,
    2
  ),
  contentType: "application/json",
  semantics: {
    properties: { status: "open", priority: "high" },
    relations: ["repo:relayfile"],
  },
});

console.log("  written.");

// ── 2. Scoped agent reads /github/* — should succeed ────────────────────────

console.log("\n── scoped agent: readFile /github/pulls/99.json ──");

const pr = await scopedClient.readFile(
  workspaceId,
  "/github/pulls/99.json"
);
console.log(`  title    : ${JSON.parse(pr.content).title}`);
console.log(`  revision : ${pr.revision}`);

// ── 3. Scoped agent lists /github tree — should succeed ─────────────────────

console.log("\n── scoped agent: listTree /github ──");

const tree = await scopedClient.listTree(workspaceId, { path: "/github" });

for (const entry of tree.entries) {
  console.log(`  ${entry.type === "dir" ? "📁" : "📄"} ${entry.path}`);
}

// ── 4. Scoped agent tries to write — expect 403 ────────────────────────────

console.log("\n── scoped agent: writeFile (expect 403 Forbidden) ──");

try {
  await scopedClient.writeFile({
    workspaceId,
    path: "/github/pulls/99.json",
    baseRevision: "*",
    content: '{"hijacked": true}',
    contentType: "application/json",
  });
  console.log("  ERROR: write should have been rejected!");
} catch (err) {
  if (err instanceof RelayFileApiError && err.status === 403) {
    console.log(`  Blocked: ${err.message}`);
    console.log("  (scope fs:read:/github/* does not grant write access)");
  } else {
    throw err;
  }
}

// ── 5. Scoped agent tries to read outside /github — expect 403 ─────────────

console.log("\n── scoped agent: readFile /agents/config.json (expect 403) ──");

// First, make sure the file exists
await adminClient.writeFile({
  workspaceId,
  path: "/agents/config.json",
  baseRevision: "*",
  content: '{"secret": "do-not-leak"}',
  contentType: "application/json",
});

try {
  await scopedClient.readFile(workspaceId, "/agents/config.json");
  console.log("  ERROR: read should have been rejected!");
} catch (err) {
  if (err instanceof RelayFileApiError && err.status === 403) {
    console.log(`  Blocked: ${err.message}`);
    console.log("  (scope fs:read:/github/* does not cover /agents/)");
  } else {
    throw err;
  }
}

// ── Summary ─────────────────────────────────────────────────────────────────

console.log("\n── Summary ──");
console.log("  Scoped token (fs:read:/github/*):");
console.log("    ✓ Read  /github/pulls/99.json");
console.log("    ✓ List  /github/");
console.log("    ✗ Write /github/pulls/99.json  → 403");
console.log("    ✗ Read  /agents/config.json    → 403");
console.log("\nDone.");
