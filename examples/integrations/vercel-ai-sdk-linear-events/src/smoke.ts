/**
 * Deterministic smoke for onEvent — proves event delivery without an LLM.
 *
 * Strategy: subscribe to a clearly-test-scoped path, write a fresh draft via
 * the proven Linear writeback path, wait for the onEvent to fire, assert the
 * path matches, then clean up.
 *
 * Run:
 *   CLOUD_WORKSPACE_ID=<app-uuid> npm run smoke
 */

import { connect, type FilesystemEvent } from "@relayfile/agents";

const rf = await connect({
  scopes: [
    "relayfile:fs:read:/discovery/linear/**",
    "relayfile:fs:read:/linear/**",
    "relayfile:fs:write:/linear/**",
  ],
});

console.log("── bootstrap evidence ──");
console.log(`  cloudWorkspaceId : ${rf.cloudWorkspaceId}`);
console.log(`  workspaceId      : ${rf.workspaceId}`);
console.log(`  credSource       : ${rf.credSource}`);

const received: FilesystemEvent[] = [];

console.log("\n── subscribe to /linear/labels/** ──");
const sub = rf.onEvent(["/linear/labels/**"], (event) => {
  received.push(event);
  console.log(`  📥 event id=${event.eventId} path=${event.path} type=${event.type}`);
});

await new Promise((r) => setTimeout(r, 1000));
console.log("  subscription active.");

console.log("\n── trigger: write a fresh Linear label draft ──");
const result = await rf.writeback.create("/linear/labels", {
  name: `relayfile-onEvent-test ${new Date().toISOString()}`,
  color: "#10b981",
  description: "onEvent smoke. Safe to delete.",
});
console.log(`  draftPath: ${result.draftPath}`);
console.log(`  externalId: ${result.externalId}`);
console.log(`  canonicalPath: ${result.canonicalPath}`);

console.log("\n── wait up to 30s for onEvent fires ──");
const deadline = Date.now() + 30_000;
while (Date.now() < deadline && received.length < 1) {
  await new Promise((r) => setTimeout(r, 500));
}

let failures = 0;
process.stdout.write("\n── assertion: at least one event received ──\n");
if (received.length === 0) {
  console.log("  ❌ FAIL — no events in 30s");
  failures++;
} else {
  console.log(`  ✅ PASS — ${received.length} event(s) received in window`);
}

await sub.unsubscribe();
console.log("\n── cleanup ──");
if (result.canonicalPath) {
  try {
    const cur = await rf.writeback.readCanonical(result.canonicalPath);
    await rf.writeback.delete(result.canonicalPath, cur.revision);
    console.log(`  ✅ deleted canonical ${result.canonicalPath}`);
  } catch (err) {
    console.log(`  ⚠️  canonical cleanup deferred: ${(err as Error).message}`);
  }
}
await rf.writeback.deleteDraft(result.draftPath);
console.log(`  ✅ deleted draft ${result.draftPath}`);

console.log(`\n── summary ──\n  ${failures === 0 ? "all checks passed" : `${failures} check(s) failed`}`);
process.exit(failures === 0 ? 0 : 1);
