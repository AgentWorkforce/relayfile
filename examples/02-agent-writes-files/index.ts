/**
 * Example 02 — Agent writes files and reads them back
 *
 * Demonstrates writing files into the relayfile virtual filesystem:
 * single writes with writeFile, atomic multi-file writes with bulkWrite,
 * and reading files back to verify.
 *
 * Run:  npx tsx index.ts
 * Env:  RELAYFILE_TOKEN  — JWT with fs:read + fs:write scopes
 *       WORKSPACE_ID     — target workspace ID
 */

import { RelayFileClient, RevisionConflictError } from "@relayfile/sdk";

const token = process.env.RELAYFILE_TOKEN;
const workspaceId = process.env.WORKSPACE_ID ?? "ws_demo";

if (!token) {
  console.error("Set RELAYFILE_TOKEN before running this example.");
  process.exit(1);
}

const client = new RelayFileClient({ token });

// ── 1. Write a single file ──────────────────────────────────────────────────

console.log("── writeFile /agents/summariser/config.json ──");

const writeResult = await client.writeFile({
  workspaceId,
  path: "/agents/summariser/config.json",
  baseRevision: "*", // create-or-overwrite
  content: JSON.stringify(
    {
      name: "summariser",
      model: "claude-sonnet-4-6",
      maxTokens: 4096,
      schedule: "on-webhook",
    },
    null,
    2
  ),
  contentType: "application/json",
  semantics: {
    properties: { owner: "platform-team", env: "staging" },
    relations: ["agent:summariser"],
  },
});

console.log(`  opId     : ${writeResult.opId}`);
console.log(`  revision : ${writeResult.targetRevision}`);

// ── 2. Read it back ─────────────────────────────────────────────────────────

console.log("\n── readFile /agents/summariser/config.json ──");
const file = await client.readFile(
  workspaceId,
  "/agents/summariser/config.json"
);
console.log(`  revision : ${file.revision}`);
console.log(`  content  : ${file.content}`);

// ── 3. Optimistic update (If-Match) ────────────────────────────────────────

console.log("\n── writeFile with If-Match (optimistic update) ──");

const updated = JSON.parse(file.content);
updated.maxTokens = 8192;

const updateResult = await client.writeFile({
  workspaceId,
  path: "/agents/summariser/config.json",
  baseRevision: file.revision, // only succeeds if nobody else changed it
  content: JSON.stringify(updated, null, 2),
  contentType: "application/json",
});

console.log(`  new revision : ${updateResult.targetRevision}`);

// ── 4. Demonstrate conflict detection ───────────────────────────────────────

console.log("\n── writeFile with stale revision (expect 409) ──");

try {
  await client.writeFile({
    workspaceId,
    path: "/agents/summariser/config.json",
    baseRevision: file.revision, // stale — already updated above
    content: '{"stale": true}',
    contentType: "application/json",
  });
} catch (err) {
  if (err instanceof RevisionConflictError) {
    console.log(`  Caught RevisionConflictError`);
    console.log(`    expected : ${err.expectedRevision}`);
    console.log(`    current  : ${err.currentRevision}`);
  } else {
    throw err;
  }
}

// ── 5. Bulk write multiple files atomically ─────────────────────────────────

console.log("\n── bulkWrite 3 analysis files ──");

const bulkResult = await client.bulkWrite({
  workspaceId,
  files: [
    {
      path: "/analysis/2026-03-29/sentiment.json",
      content: JSON.stringify({ score: 0.82, label: "positive" }),
      contentType: "application/json",
    },
    {
      path: "/analysis/2026-03-29/topics.json",
      content: JSON.stringify({ topics: ["auth", "performance", "ux"] }),
      contentType: "application/json",
    },
    {
      path: "/analysis/2026-03-29/summary.md",
      content:
        "# Daily Summary\n\nSentiment is positive. Key topics: auth, performance, UX.",
      contentType: "text/markdown",
    },
  ],
});

console.log(`  written    : ${bulkResult.written}`);
console.log(`  errorCount : ${bulkResult.errorCount}`);

// ── 6. Read back a bulk-written file ────────────────────────────────────────

console.log("\n── readFile /analysis/2026-03-29/summary.md ──");
const summary = await client.readFile(
  workspaceId,
  "/analysis/2026-03-29/summary.md"
);
console.log(`  content:\n${summary.content}`);

console.log("\nDone.");
