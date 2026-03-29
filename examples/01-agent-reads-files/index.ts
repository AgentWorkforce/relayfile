/**
 * Example 01 — Agent reads files from a workspace
 *
 * Demonstrates the simplest relayfile flow: connect to a workspace and
 * browse its virtual filesystem using listTree, readFile, and queryFiles.
 *
 * Run:  npx tsx index.ts
 * Env:  RELAYFILE_TOKEN  — JWT with fs:read scope
 *       WORKSPACE_ID     — target workspace ID
 */

import { RelayFileClient } from "@relayfile/sdk";

const token = process.env.RELAYFILE_TOKEN;
const workspaceId = process.env.WORKSPACE_ID ?? "ws_demo";

if (!token) {
  console.error("Set RELAYFILE_TOKEN before running this example.");
  process.exit(1);
}

const client = new RelayFileClient({ token });

// ── 1. List the workspace tree ──────────────────────────────────────────────

console.log("── listTree (depth 2) ──");
const tree = await client.listTree(workspaceId, { depth: 2 });

for (const entry of tree.entries) {
  const icon = entry.type === "dir" ? "📁" : "📄";
  console.log(`  ${icon} ${entry.path}  rev=${entry.revision}`);
}

if (tree.nextCursor) {
  console.log(`  … more entries (cursor: ${tree.nextCursor})`);
}

// ── 2. Read a single file ───────────────────────────────────────────────────

const targetPath = tree.entries.find((e) => e.type === "file")?.path;

if (targetPath) {
  console.log(`\n── readFile ${targetPath} ──`);
  const file = await client.readFile(workspaceId, targetPath);
  console.log(`  contentType : ${file.contentType}`);
  console.log(`  revision    : ${file.revision}`);
  console.log(`  provider    : ${file.provider ?? "(agent-written)"}`);
  console.log(`  content     : ${file.content.slice(0, 200)}…`);
} else {
  console.log("\nNo files found in tree — skipping readFile.");
}

// ── 3. Query files by provider ──────────────────────────────────────────────

console.log("\n── queryFiles (provider=github) ──");
const query = await client.queryFiles(workspaceId, { provider: "github" });

if (query.items.length === 0) {
  console.log("  (no github files — try ingesting a webhook first)");
} else {
  for (const item of query.items.slice(0, 5)) {
    console.log(`  ${item.path}  props=${JSON.stringify(item.properties)}`);
  }
}

// ── 4. Query files by semantic property ─────────────────────────────────────

console.log("\n── queryFiles (property status=open) ──");
const byProp = await client.queryFiles(workspaceId, {
  properties: { status: "open" },
});

console.log(`  matched ${byProp.items.length} file(s)`);
for (const item of byProp.items.slice(0, 3)) {
  console.log(`  ${item.path}`);
}

console.log("\nDone.");
