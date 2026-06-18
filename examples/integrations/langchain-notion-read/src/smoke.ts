/**
 * Deterministic smoke verifier — no LLM in the loop.
 *
 * Proves: bootstrap → /notion listTree → readFile → queryFiles all work
 * end-to-end against a real workspace. Exit code 0 = pass; non-zero = fail.
 *
 * Run:
 *   CLOUD_WORKSPACE_ID=<app-uuid> npm run smoke
 */

import { connectWorkspace } from "./bootstrap.js";

const NOTION_ROOT = "/notion";

interface Check {
  name: string;
  run: () => Promise<{ ok: boolean; evidence: Record<string, unknown> }>;
}

async function main(): Promise<number> {
  const ws = await connectWorkspace({
    scopes: ["relayfile:fs:read:/notion/**"],
  });

  console.log("── bootstrap evidence ──");
  console.log(`  cloudWorkspaceId : ${ws.cloudWorkspaceId}`);
  console.log(`  workspaceId      : ${ws.workspaceId}`);
  console.log(`  credSource       : ${ws.credSource}`);

  const checks: Check[] = [
    {
      name: "listTree /notion depth=2 returns ≥1 entry",
      async run() {
        const tree = await ws.client.listTree(ws.workspaceId, {
          path: NOTION_ROOT,
          depth: 2,
        });
        return {
          ok: tree.entries.length > 0,
          evidence: {
            path: tree.path,
            entryCount: tree.entries.length,
            firstThree: tree.entries.slice(0, 3).map((e) => ({
              path: e.path,
              type: e.type,
              revision: e.revision,
            })),
          },
        };
      },
    },
    {
      name: "readFile on first /notion file returns non-empty content",
      async run() {
        const tree = await ws.client.listTree(ws.workspaceId, {
          path: NOTION_ROOT,
          depth: 4,
        });
        const firstFile = tree.entries.find((e) => e.type === "file");
        if (!firstFile) {
          return {
            ok: false,
            evidence: { reason: "no file entries under /notion" },
          };
        }
        const file = await ws.client.readFile(ws.workspaceId, firstFile.path);
        return {
          ok: file.content.length > 0,
          evidence: {
            path: file.path,
            revision: file.revision,
            contentType: file.contentType,
            contentLen: file.content.length,
            excerpt: file.content.slice(0, 160),
          },
        };
      },
    },
    {
      name: "readFile on /notion/_index.json round-trips a parseable JSON catalog",
      async run() {
        const file = await ws.client.readFile(ws.workspaceId, "/notion/_index.json");
        const parsed = JSON.parse(file.content) as Array<{ id: string; title: string }>;
        return {
          ok: Array.isArray(parsed) && parsed.length > 0,
          evidence: {
            revision: file.revision,
            categoryCount: parsed.length,
            categories: parsed.map((c) => c.id),
          },
        };
      },
    },
    {
      name: "queryFiles provider=notion (informational only — may be 0 if no semantic indexing)",
      async run() {
        const result = await ws.client.queryFiles(ws.workspaceId, {
          provider: "notion",
        });
        return {
          ok: true,
          evidence: {
            itemCount: result.items.length,
            firstThreePaths: result.items.slice(0, 3).map((i) => i.path),
            note:
              result.items.length === 0
                ? "queryFiles returned 0 — provider files exist (listTree shows them) but aren't semantically indexed yet. Not a failure of the read path."
                : "indexed and queryable",
          },
        };
      },
    },
  ];

  let failures = 0;
  for (const c of checks) {
    process.stdout.write(`\n── ${c.name} ──\n`);
    try {
      const r = await c.run();
      console.log(`  result   : ${r.ok ? "✅ PASS" : "❌ FAIL"}`);
      console.log(`  evidence : ${JSON.stringify(r.evidence, null, 2).replace(/\n/g, "\n             ")}`);
      if (!r.ok) failures++;
    } catch (err) {
      console.log(`  result   : ❌ THREW`);
      console.log(`  error    : ${(err as Error).message}`);
      failures++;
    }
  }

  console.log(`\n── summary ──`);
  console.log(`  ${checks.length - failures} / ${checks.length} passed`);
  return failures === 0 ? 0 : 1;
}

main()
  .then((code) => process.exit(code))
  .catch((err) => {
    console.error("smoke crashed:", err);
    process.exit(2);
  });
