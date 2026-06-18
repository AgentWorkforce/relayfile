/**
 * LangChain agent reads workspace Notion content via Relayfile.
 *
 * Run:
 *   ANTHROPIC_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { ChatAnthropic } from "@langchain/anthropic";
import { createReactAgent } from "@langchain/langgraph/prebuilt";

import { connect, tools } from "@relayfile/agents";

const rf = await connect({ scopes: ["relayfile:fs:read:/notion/**"] });

const agent = createReactAgent({
  llm: new ChatAnthropic({ model: "claude-sonnet-4-6" }),
  tools: tools.langchain(rf, { readPaths: ["/notion"] }),
});

const result = await agent.invoke({
  messages: [
    {
      role: "system",
      content:
        "You are a workspace-summarisation agent. Use the relayfile_* tools to explore the user's " +
        "Notion content under /notion.",
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
console.log(typeof last.content === "string" ? last.content : JSON.stringify(last.content));
