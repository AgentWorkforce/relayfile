/**
 * Example 06 — Writeback consumer with GitHub handler
 *
 * Polls for pending writebacks and pushes VFS changes back to GitHub
 * via the GitHub API. Demonstrates the full writeback lifecycle:
 * list → read → push → acknowledge.
 *
 * Run:  npx tsx index.ts
 * Env:  RELAYFILE_TOKEN  — JWT with fs:read + ops:read scopes
 *       WORKSPACE_ID     — target workspace ID
 *       GITHUB_TOKEN     — GitHub PAT with repo scope
 */

import { RelayFileClient } from "@relayfile/sdk";

const token = process.env.RELAYFILE_TOKEN;
const workspaceId = process.env.WORKSPACE_ID ?? "ws_demo";
const githubToken = process.env.GITHUB_TOKEN;

if (!token || !githubToken) {
  console.error("Set RELAYFILE_TOKEN and GITHUB_TOKEN before running.");
  process.exit(1);
}

const client = new RelayFileClient({ token });

// ── 1. GitHub writeback handler ────────────────────────────────────────────

interface WritebackRoute {
  owner: string;
  repo: string;
  resource: string;
  id: string;
}

function parseGitHubPath(path: string): WritebackRoute | null {
  // /github/{owner}/{repo}/{resource}/{id}.json
  const match = path.match(/^\/github\/([^/]+)\/([^/]+)\/([^/]+)\/(\d+)\.json$/);
  if (!match) return null;
  return { owner: match[1], repo: match[2], resource: match[3], id: match[4] };
}

async function pushToGitHub(route: WritebackRoute, content: string): Promise<void> {
  const body = JSON.parse(content);
  const { owner, repo, resource, id } = route;

  const endpoint =
    resource === "issues" ? `repos/${owner}/${repo}/issues/${id}` :
    resource === "pulls"  ? `repos/${owner}/${repo}/pulls/${id}` :
    resource === "comments" ? `repos/${owner}/${repo}/issues/comments/${id}` :
    null;

  if (!endpoint) {
    throw new Error(`Unsupported GitHub resource: ${resource}`);
  }

  const resp = await fetch(`https://api.github.com/${endpoint}`, {
    method: "PATCH",
    headers: {
      Authorization: `Bearer ${githubToken}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });

  if (!resp.ok) {
    const err = await resp.text();
    throw new Error(`GitHub API ${resp.status}: ${err.slice(0, 200)}`);
  }
}

// ── 2. Poll and process pending writebacks ─────────────────────────────────

console.log("── listPendingWritebacks ──");
const pending = await client.listPendingWritebacks(workspaceId);
console.log(`  ${pending.length} pending writeback(s)\n`);

for (const item of pending) {
  console.log(`── processing ${item.path} (id=${item.id}) ──`);

  const route = parseGitHubPath(item.path);
  if (!route) {
    console.log(`  skipping — not a GitHub path`);
    await client.ackWriteback({
      workspaceId,
      itemId: item.id,
      success: false,
      error: "Unrecognized path format",
    });
    continue;
  }

  // ── 3. Read file content and push to GitHub ──────────────────────────────

  try {
    const file = await client.readFile(workspaceId, item.path);
    console.log(`  read ${file.path} rev=${file.revision}`);

    await pushToGitHub(route, file.content);
    console.log(`  pushed to GitHub ${route.owner}/${route.repo}/${route.resource}/${route.id}`);

    await client.ackWriteback({ workspaceId, itemId: item.id, success: true });
    console.log(`  ack: success`);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    console.log(`  ack: failed — ${message}`);
    await client.ackWriteback({
      workspaceId,
      itemId: item.id,
      success: false,
      error: message,
    });
  }
}

console.log("\nDone.");
