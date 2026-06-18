/**
 * LangChain agent creates / updates / deletes Linear labels via Relayfile
 * (schema-validated, comprehensive op-status proof in the package).
 *
 * Run:
 *   ANTHROPIC_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { ChatAnthropic } from "@langchain/anthropic";
import { tool } from "@langchain/core/tools";
import { createReactAgent } from "@langchain/langgraph/prebuilt";
import { z } from "zod";

import { connect } from "@relayfile/agents";

const rf = await connect({
  scopes: [
    "relayfile:fs:read:/discovery/linear/**",
    "relayfile:fs:read:/linear/**",
    "relayfile:fs:write:/linear/**",
  ],
});

const linearCreate = tool(
  async (args: { name: string; color?: string; description?: string }) => {
    const r = await rf.writeback.create("/linear/labels", args);
    return JSON.stringify({
      linearUuid: r.externalId,
      canonicalPath: r.canonicalPath,
      draftPath: r.draftPath,
    });
  },
  {
    name: "linear_create_label",
    description: "Create a Linear label. Returns the real Linear UUID + canonical path.",
    schema: z.object({
      name: z.string().min(1),
      color: z.string().optional(),
      description: z.string().optional(),
    }),
  },
);

const linearDelete = tool(
  async (args: { canonicalPath: string; draftPath?: string }) => {
    const current = await rf.client.readFile(rf.workspaceId, args.canonicalPath);
    await rf.writeback.delete(args.canonicalPath, current.revision);
    if (args.draftPath) await rf.writeback.deleteDraft(args.draftPath);
    return JSON.stringify({ deleted: true });
  },
  {
    name: "linear_delete_label",
    description: "Delete a Linear label canonical (propagates to Linear) and its draft receipt.",
    schema: z.object({ canonicalPath: z.string(), draftPath: z.string().optional() }),
  },
);

const agent = createReactAgent({
  llm: new ChatAnthropic({ model: "claude-sonnet-4-6" }),
  tools: [linearCreate, linearDelete],
});

const result = await agent.invoke({
  messages: [
    {
      role: "system",
      content:
        "You manage Linear labels via Relayfile. Prefix label names with " +
        "`relayfile-writeback-test ` and an ISO timestamp. Delete the label and its draft after creating.",
    },
    {
      role: "user",
      content:
        "Create a temporary demo Linear label, then delete it so the workspace stays clean.",
    },
  ],
});

const last = result.messages[result.messages.length - 1];
console.log(typeof last.content === "string" ? last.content : JSON.stringify(last.content));
