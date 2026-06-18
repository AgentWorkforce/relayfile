/**
 * LangChain agent that reads workspace Notion content via Relayfile.
 *
 * Run:
 *   ANTHROPIC_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { ChatAnthropic } from "@langchain/anthropic";
import { tool } from "@langchain/core/tools";
import { createReactAgent } from "@langchain/langgraph/prebuilt";
import { z } from "zod";

import { connectWorkspace } from "./bootstrap.js";

async function main() {
  const ws = await connectWorkspace({
    scopes: ["relayfile:fs:read:/notion/**"],
  });

  console.log(`Connected to workspace ${ws.workspaceId} (cloud: ${ws.cloudWorkspaceId}).`);

  const notionListTree = tool(
    async ({ subpath, depth }) => {
      const path = `/notion${subpath === "/" ? "" : subpath}`;
      const tree = await ws.client.listTree(ws.workspaceId, { path, depth });
      return JSON.stringify(
        tree.entries.map((e) => ({ path: e.path, type: e.type, revision: e.revision })),
      );
    },
    {
      name: "notion_list_tree",
      description:
        "List entries under /notion in the workspace. Returns paths, types (file|dir), and revisions.",
      schema: z.object({
        subpath: z
          .string()
          .default("/")
          .describe("Subpath under /notion, e.g. '/' or '/databases'."),
        depth: z.number().int().min(1).max(4).default(2),
      }),
    },
  );

  const notionReadFile = tool(
    async ({ path }) => {
      const file = await ws.client.readFile(ws.workspaceId, path);
      return JSON.stringify({
        path: file.path,
        revision: file.revision,
        contentType: file.contentType,
        content: file.content,
      });
    },
    {
      name: "notion_read_file",
      description: "Read a single file's content from /notion in the workspace.",
      schema: z.object({
        path: z.string().describe("Absolute workspace path, e.g. /notion/pages/<id>.json"),
      }),
    },
  );

  const model = new ChatAnthropic({ model: "claude-sonnet-4-6" });
  const agent = createReactAgent({
    llm: model,
    tools: [notionListTree, notionReadFile],
  });

  const result = await agent.invoke({
    messages: [
      {
        role: "system",
        content:
          "You are a workspace-summarisation agent. Use the notion_* tools to explore the user's " +
          "Notion content and produce a concise summary.",
      },
      {
        role: "user",
        content:
          "Summarise what's in this workspace's Notion mount. List the top-level categories, " +
          "count of files in each, and one representative title per category if available.",
      },
    ],
  });

  const last = result.messages[result.messages.length - 1];
  console.log("\n── agent summary ──");
  console.log(typeof last.content === "string" ? last.content : JSON.stringify(last.content));
}

main().catch((err) => {
  console.error("agent failed:", err);
  process.exit(1);
});
