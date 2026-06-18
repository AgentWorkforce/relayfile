/**
 * LangChain agent that creates / updates / deletes Linear labels via
 * Relayfile (schema-validated, comprehensive op-status proof under the hood).
 *
 * Run:
 *   ANTHROPIC_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { ChatAnthropic } from "@langchain/anthropic";
import { tool } from "@langchain/core/tools";
import { createReactAgent } from "@langchain/langgraph/prebuilt";
import { z } from "zod";

import { connectWorkspace } from "./bootstrap.js";
import {
  LABEL_ROOT,
  LABEL_SCHEMA_PATH,
  createLabel,
  deleteDraftIfPresent,
  deleteLabel,
  readCanonicalLabel,
  updateLabel,
} from "./labels.js";

async function main() {
  const ws = await connectWorkspace({
    scopes: [
      "relayfile:fs:read:/discovery/linear/**",
      "relayfile:fs:read:/linear/**",
      "relayfile:fs:write:/linear/**",
    ],
  });

  console.log(`Connected to workspace ${ws.workspaceId} (cloud: ${ws.cloudWorkspaceId}).`);

  const linearReadSchema = tool(
    async () => {
      const file = await ws.client.readFile(ws.workspaceId, LABEL_SCHEMA_PATH);
      return file.content;
    },
    {
      name: "linear_read_labels_schema",
      description: "Read the Linear labels writeback schema.",
      schema: z.object({}),
    },
  );

  const linearListLabels = tool(
    async ({ depth }) => {
      const tree = await ws.client.listTree(ws.workspaceId, { path: LABEL_ROOT, depth });
      return JSON.stringify(
        tree.entries.map((e) => ({ path: e.path, type: e.type, revision: e.revision })),
      );
    },
    {
      name: "linear_list_labels",
      description: "List existing Linear labels.",
      schema: z.object({ depth: z.number().int().min(1).max(3).default(1) }),
    },
  );

  const linearCreateLabel = tool(
    async (payload) => {
      const result = await createLabel(ws, payload);
      return JSON.stringify({
        opStatus: result.receipt.status,
        linearUuid: result.externalId,
        canonicalPath: result.canonicalPath,
        draftPath: result.draftPath,
      });
    },
    {
      name: "linear_create_label",
      description:
        "Create a Linear label. Writes a schema-validated draft, polls op status, returns the real Linear UUID.",
      schema: z.object({
        name: z.string().min(1),
        color: z.string().optional(),
        description: z.string().optional(),
        teamId: z.string().uuid().optional(),
      }),
    },
  );

  const linearReadLabel = tool(
    async ({ canonicalPath }) => {
      const { revision, record } = await readCanonicalLabel(ws, canonicalPath);
      return JSON.stringify({ revision, record });
    },
    {
      name: "linear_read_label",
      description: "Read a canonical Linear label record.",
      schema: z.object({ canonicalPath: z.string() }),
    },
  );

  const linearUpdateLabel = tool(
    async ({ canonicalPath, patch }) => {
      const current = await readCanonicalLabel(ws, canonicalPath);
      const r = await updateLabel(ws, canonicalPath, current.revision, patch);
      return JSON.stringify({ opStatus: r.receipt.status, newRevision: r.targetRevision });
    },
    {
      name: "linear_update_label",
      description: "Update a Linear label via PATCH on the canonical path.",
      schema: z.object({
        canonicalPath: z.string(),
        patch: z.object({
          name: z.string().optional(),
          color: z.string().optional(),
          description: z.string().optional(),
        }),
      }),
    },
  );

  const linearDeleteLabel = tool(
    async ({ canonicalPath, draftPath }) => {
      const current = await readCanonicalLabel(ws, canonicalPath);
      const r = await deleteLabel(ws, canonicalPath, current.revision);
      if (draftPath) await deleteDraftIfPresent(ws, draftPath);
      return JSON.stringify({ opStatus: r.receipt.status, deleted: true });
    },
    {
      name: "linear_delete_label",
      description: "Delete a Linear label and its draft receipt.",
      schema: z.object({ canonicalPath: z.string(), draftPath: z.string().optional() }),
    },
  );

  const model = new ChatAnthropic({ model: "claude-sonnet-4-6" });
  const agent = createReactAgent({
    llm: model,
    tools: [
      linearReadSchema,
      linearListLabels,
      linearCreateLabel,
      linearReadLabel,
      linearUpdateLabel,
      linearDeleteLabel,
    ],
  });

  const result = await agent.invoke({
    messages: [
      {
        role: "system",
        content:
          "You manage Linear labels for the user's workspace through Relayfile. ALWAYS read the " +
          "schema before creating, and ALWAYS prefix label names with `relayfile-writeback-test ` " +
          "and an ISO timestamp so they're easy to clean up. Delete drafts after creating canonicals.",
      },
      {
        role: "user",
        content:
          "Create a temporary 'demo' label called 'relayfile-writeback-test " +
          new Date().toISOString() +
          "' with color #6366f1 and description 'created from agent'. Then read it back to confirm, " +
          "and finally delete it (and its draft receipt) so the workspace stays clean.",
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
