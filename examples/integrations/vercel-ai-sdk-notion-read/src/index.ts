/**
 * Vercel AI SDK agent that reads workspace Notion content via Relayfile.
 *
 * Run:
 *   ANTHROPIC_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { anthropic } from "@ai-sdk/anthropic";
import { generateText } from "ai";

import { connect, tools } from "@relayfile/agents";

const rf = await connect({ scopes: ["relayfile:fs:read:/notion/**"] });

const result = await generateText({
  model: anthropic("claude-sonnet-4-6"),
  tools: tools.vercel(rf, { readPaths: ["/notion"] }),
  maxSteps: 6,
  system:
    "You are a workspace-summarisation agent. Use the relayfile_* tools to explore the " +
    "user's Notion content under /notion and produce a concise summary.",
  prompt:
    "Summarise what's in this workspace's Notion mount. List the top-level categories, " +
    "count of files in each, and one representative title per category if available.",
});

console.log(result.text);
console.log(`\n(${result.steps.length} tool-call steps)`);
