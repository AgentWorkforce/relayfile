/**
 * Example 04 — Watch for file changes via event polling
 *
 * Demonstrates how an agent can watch for filesystem changes using the
 * getEvents polling API. Events are delivered in order with cursor-based
 * pagination, so you never miss a change.
 *
 * Run:  npx tsx index.ts
 * Env:  RELAYFILE_TOKEN  — JWT with fs:read scope
 *       WORKSPACE_ID     — target workspace ID
 */

import { RelayFileClient, type FilesystemEvent } from "@relayfile/sdk";

const token = process.env.RELAYFILE_TOKEN;
const workspaceId = process.env.WORKSPACE_ID ?? "ws_demo";

if (!token) {
  console.error("Set RELAYFILE_TOKEN before running this example.");
  process.exit(1);
}

const client = new RelayFileClient({ token });

// ── 1. Fetch recent events ──────────────────────────────────────────────────

console.log("── getEvents (last 10) ──");

const initial = await client.getEvents(workspaceId, { limit: 10 });

if (initial.events.length === 0) {
  console.log("  (no events yet — write some files first)");
} else {
  for (const evt of initial.events) {
    printEvent(evt);
  }
}

// ── 2. Poll for new events ──────────────────────────────────────────────────

console.log("\n── polling for new events (3 rounds, 2s apart) ──");

let cursor = initial.nextCursor;
const POLL_ROUNDS = 3;
const POLL_INTERVAL_MS = 2000;

for (let round = 1; round <= POLL_ROUNDS; round++) {
  await sleep(POLL_INTERVAL_MS);
  console.log(`\n  [poll ${round}/${POLL_ROUNDS}]`);

  const feed = await client.getEvents(workspaceId, { cursor, limit: 20 });

  if (feed.events.length === 0) {
    console.log("    (no new events)");
  } else {
    for (const evt of feed.events) {
      printEvent(evt);
    }
  }

  cursor = feed.nextCursor;
}

// ── 3. Filter events by provider ────────────────────────────────────────────

console.log("\n── getEvents (provider=github) ──");

const githubEvents = await client.getEvents(workspaceId, {
  provider: "github",
  limit: 5,
});

if (githubEvents.events.length === 0) {
  console.log("  (no github events)");
} else {
  for (const evt of githubEvents.events) {
    printEvent(evt);
  }
}

console.log("\nDone.");

// ── Helpers ─────────────────────────────────────────────────────────────────

function printEvent(evt: FilesystemEvent) {
  const ts = new Date(evt.timestamp).toLocaleTimeString();
  console.log(
    `    ${ts}  ${padRight(evt.type, 14)}  ${evt.path}  (${evt.origin ?? "unknown"})`
  );
}

function padRight(s: string, len: number): string {
  return s.length >= len ? s : s + " ".repeat(len - s.length);
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}
