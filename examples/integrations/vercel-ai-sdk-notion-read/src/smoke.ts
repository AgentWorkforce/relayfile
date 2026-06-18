/**
 * Deterministic smoke verifier — no LLM in the loop.
 *
 * Run:
 *   CLOUD_WORKSPACE_ID=<app-uuid> npm run smoke
 */

import { connect } from "@relayfile/agents";

const rf = await connect({ scopes: ["relayfile:fs:read:/notion/**"] });

console.log("── bootstrap evidence ──");
console.log(`  cloudWorkspaceId : ${rf.cloudWorkspaceId}`);
console.log(`  workspaceId      : ${rf.workspaceId}`);
console.log(`  credSource       : ${rf.credSource}`);

let failures = 0;
const step = async (name: string, fn: () => Promise<Record<string, unknown>>) => {
  process.stdout.write(`\n── ${name} ──\n`);
  try {
    const evidence = await fn();
    console.log(`  result   : ✅ PASS`);
    console.log(
      `  evidence : ${JSON.stringify(evidence, null, 2).replace(/\n/g, "\n             ")}`,
    );
  } catch (err) {
    console.log(`  result   : ❌ FAIL\n  error    : ${(err as Error).message}`);
    failures++;
  }
};

await step("listTree /notion depth=2 returns ≥1 entry", async () => {
  const tree = await rf.client.listTree(rf.workspaceId, { path: "/notion", depth: 2 });
  if (tree.entries.length === 0) throw new Error("no entries under /notion");
  return {
    entryCount: tree.entries.length,
    firstThree: tree.entries.slice(0, 3).map((e) => ({
      path: e.path,
      type: e.type,
      revision: e.revision,
    })),
  };
});

await step("readFile /notion/_index.json round-trips a parseable JSON catalog", async () => {
  const file = await rf.client.readFile(rf.workspaceId, "/notion/_index.json");
  const parsed = JSON.parse(file.content) as Array<{ id: string; title: string }>;
  if (!Array.isArray(parsed) || parsed.length === 0) throw new Error("empty catalog");
  return {
    revision: file.revision,
    categoryCount: parsed.length,
    categories: parsed.map((c) => c.id),
  };
});

console.log(`\n── summary ──\n  ${failures === 0 ? "all checks passed" : `${failures} check(s) failed`}`);
process.exit(failures === 0 ? 0 : 1);
