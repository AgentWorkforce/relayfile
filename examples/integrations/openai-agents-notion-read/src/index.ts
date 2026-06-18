/**
 * OpenAI Agents SDK agent reads workspace Notion content via Relayfile.
 *
 * Run:
 *   OPENAI_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { Agent, run } from "@openai/agents";

import { connect, tools } from "@relayfile/agents";

const rf = await connect({ scopes: ["relayfile:fs:read:/notion/**"] });

const agent = new Agent({
  name: "notion-explorer",
  instructions:
    "You are a workspace-summarisation agent. Use the relayfile_* tools to explore the " +
    "user's Notion content under /notion and produce a concise summary.",
  tools: tools.openai(rf, { readPaths: ["/notion"] }),
});

const result = await run(
  agent,
  "Summarise what's in this workspace's Notion mount. List the top-level categories, " +
    "count of files in each, and one representative title per category if available.",
);

console.log(result.finalOutput);
