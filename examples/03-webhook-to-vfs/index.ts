/**
 * Example 03 — Webhook ingestion into the virtual filesystem
 *
 * Demonstrates how external events (e.g. a GitHub pull request) flow into
 * relayfile via ingestWebhook, and how computeCanonicalPath maps provider
 * objects to deterministic file paths.
 *
 * Run:  npx tsx index.ts
 * Env:  RELAYFILE_TOKEN  — JWT with sync:trigger + fs:read scopes
 *       WORKSPACE_ID     — target workspace ID
 */

import {
  RelayFileClient,
  computeCanonicalPath,
} from "@relayfile/sdk";

const token = process.env.RELAYFILE_TOKEN;
const workspaceId = process.env.WORKSPACE_ID ?? "ws_demo";

if (!token) {
  console.error("Set RELAYFILE_TOKEN before running this example.");
  process.exit(1);
}

const client = new RelayFileClient({ token });

// ── 1. Show canonical path mapping ──────────────────────────────────────────

console.log("── computeCanonicalPath examples ──");

const paths = [
  computeCanonicalPath("github", "pulls", "42"),
  computeCanonicalPath("github", "issues", "108"),
  computeCanonicalPath("slack", "messages", "C04QZ1234-1711700000.000100"),
  computeCanonicalPath("zendesk", "tickets", "98765"),
  computeCanonicalPath("stripe", "invoices", "inv_abc123"),
];

for (const p of paths) {
  console.log(`  ${p}`);
}

// ── 2. Ingest a GitHub PR webhook ───────────────────────────────────────────

console.log("\n── ingestWebhook (GitHub PR opened) ──");

const prPayload = {
  provider: "github",
  event_type: "pull_request.opened",
  path: computeCanonicalPath("github", "pulls", "42"),
  delivery_id: `ghd_${Date.now()}`,
  timestamp: new Date().toISOString(),
  data: {
    number: 42,
    title: "Add JWT auth middleware",
    state: "open",
    user: { login: "khaliqgant" },
    head: { ref: "feat/jwt-auth", sha: "a1b2c3d" },
    base: { ref: "main", sha: "e4f5g6h" },
    body: "Implements JWT-based auth with refresh tokens.\n\nCloses #37",
    created_at: "2026-03-29T10:00:00Z",
    updated_at: "2026-03-29T10:00:00Z",
    labels: [{ name: "auth" }, { name: "security" }],
    requested_reviewers: [{ login: "reviewer-1" }],
  },
};

const ingestResult = await client.ingestWebhook({
  workspaceId,
  ...prPayload,
});

console.log(`  status        : ${ingestResult.status}`);
console.log(`  envelopeId    : ${ingestResult.id}`);
console.log(`  correlationId : ${ingestResult.correlationId}`);

// ── 3. Ingest a second event (PR review submitted) ──────────────────────────

console.log("\n── ingestWebhook (GitHub PR review submitted) ──");

const reviewResult = await client.ingestWebhook({
  workspaceId,
  provider: "github",
  event_type: "pull_request_review.submitted",
  path: computeCanonicalPath("github", "pull_reviews", "42-1"),
  delivery_id: `ghd_${Date.now()}_review`,
  data: {
    pull_number: 42,
    review_id: 1,
    state: "approved",
    user: { login: "reviewer-1" },
    body: "LGTM — auth flow looks solid.",
    submitted_at: "2026-03-29T11:30:00Z",
  },
});

console.log(`  envelopeId : ${reviewResult.id}`);

// ── 4. Read the ingested file back ──────────────────────────────────────────

console.log("\n── readFile (the ingested PR) ──");

// Give the server a moment to process the envelope
await new Promise((r) => setTimeout(r, 1000));

try {
  const pr = await client.readFile(
    workspaceId,
    computeCanonicalPath("github", "pulls", "42")
  );
  console.log(`  revision    : ${pr.revision}`);
  console.log(`  provider    : ${pr.provider}`);
  console.log(`  contentType : ${pr.contentType}`);

  const data = JSON.parse(pr.content);
  console.log(`  PR title    : ${data.title}`);
  console.log(`  PR state    : ${data.state}`);
} catch (err: unknown) {
  const msg = err instanceof Error ? err.message : String(err);
  console.log(`  (file not yet available — ${msg})`);
  console.log("  This is normal if the server is still processing the envelope.");
}

console.log("\nDone.");
