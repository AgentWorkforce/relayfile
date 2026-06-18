/**
 * OpenAI Agents SDK agent that reads workspace Notion content via Relayfile.
 *
 * Run:
 *   OPENAI_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { Agent, run, tool } from "@openai/agents";
import { z } from "zod";

import { connectWorkspace } from "./bootstrap.js";

async function main() {
  const ws = await connectWorkspace({
    scopes: ["relayfile:fs:read:/notion/**"],
  });

  console.log(`Connected to workspace ${ws.workspaceId} (cloud: ${ws.cloudWorkspaceId}).`);

  const notionListTree = tool({
    name: "notion_list_tree",
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
      return JSON.stringify(
        tree.entries.map((e) => ({ path: e.path, type: e.type, revision: e.revision })),
      );
    },
  });

  const notionReadFile = tool({
    name: "notion_read_file",
    description: "Read a single file's content from /notion in the workspace.",
    parameters: z.object({
      path: z.string().describe("Absolute workspace path, e.g. /notion/pages/<id>.json"),
    }),
    async execute({ path }) {
      const file = await ws.client.readFile(ws.workspaceId, path);
      return JSON.stringify({
        path: file.path,
        revision: file.revision,
        contentType: file.contentType,
        content: file.content,
      });
    },
  });

  const agent = new Agent({
    name: "notion-explorer",
    instructions:
      "You are a workspace-summarisation agent. Use the notion_* tools to explore the user's " +
      "Notion content in this Relayfile workspace and produce a concise summary of what's there.",
    tools: [notionListTree, notionReadFile],
  });

  const result = await run(
    agent,
    "Summarise what's in this workspace's Notion mount. List the top-level categories, " +
      "count of files in each, and one representative title per category if available.",
  );

  console.log("\n── agent output ──");
  console.log(result.finalOutput);
}

main().catch((err) => {
  console.error("agent failed:", err);
  process.exit(1);
});
