/**
 * Vercel AI SDK agent that reads workspace Notion content via Relayfile.
 *
 * The agent has two Notion-read tools wired to the Relayfile SDK:
 *   - notion_list_tree   (browse /notion)
 *   - notion_read_file   (fetch a single file's content + metadata)
 *
 * `queryFiles` is intentionally not exposed: it filters by per-file `provider`
 * metadata which the current cloud sync does not yet populate for synced
 * records, so it would return 0 items and burn the agent's tool budget.
 *
 * Run:
 *   ANTHROPIC_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { anthropic } from "@ai-sdk/anthropic";
import { generateText, tool } from "ai";
import { z } from "zod";

import { connectWorkspace } from "./bootstrap.js";

async function main() {
  const ws = await connectWorkspace({
    scopes: ["relayfile:fs:read:/notion/**"],
  });

  console.log(`Connected to workspace ${ws.workspaceId} (cloud: ${ws.cloudWorkspaceId}).`);

  const tools = {
    notion_list_tree: tool({
      description:
        "List entries under /notion in the workspace. Returns paths, types (file|dir), and revisions.",
      parameters: z.object({
        subpath: z
          .string()
          .default("/")
          .describe("Subpath under /notion to list, e.g. '/' or '/databases'."),
        depth: z.number().int().min(1).max(4).default(2),
      }),
      async execute({ subpath, depth }) {
        const path = `/notion${subpath === "/" ? "" : subpath}`;
        const tree = await ws.client.listTree(ws.workspaceId, { path, depth });
        return tree.entries.map((e) => ({
          path: e.path,
          type: e.type,
          revision: e.revision,
        }));
      },
    }),
    notion_read_file: tool({
      description: "Read a single file's content from /notion in the workspace.",
      parameters: z.object({
        path: z.string().describe("Absolute workspace path, e.g. /notion/pages/<id>.json"),
      }),
      async execute({ path }) {
        const file = await ws.client.readFile(ws.workspaceId, path);
        return {
          path: file.path,
          revision: file.revision,
          contentType: file.contentType,
          content: file.content,
        };
      },
    }),
  };

  const result = await generateText({
    model: anthropic("claude-sonnet-4-6"),
    tools,
    maxSteps: 6,
    system:
      "You are a workspace-summarisation agent. Use the notion_* tools to explore the user's " +
      "Notion content in this Relayfile workspace and produce a concise summary of what's there.",
    prompt:
      "Summarise what's in this workspace's Notion mount. List the top-level categories, " +
      "count of files in each, and one representative title per category if available.",
  });

  console.log("\n── agent summary ──");
  console.log(result.text);
  console.log(`\n(${result.steps.length} tool-call steps)`);
}

main().catch((err) => {
  console.error("agent failed:", err);
  process.exit(1);
});
