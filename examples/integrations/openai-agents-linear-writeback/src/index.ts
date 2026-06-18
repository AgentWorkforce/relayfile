/**
 * OpenAI Agents SDK agent that creates / updates / deletes Linear labels via
 * Relayfile (schema-validated, comprehensive op-status proof under the hood).
 *
 * Run:
 *   OPENAI_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { Agent, run, tool } from "@openai/agents";
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

  const linearReadSchema = tool({
    name: "linear_read_labels_schema",
    description: "Read the Linear labels writeback schema.",
    parameters: z.object({}),
    async execute() {
      const file = await ws.client.readFile(ws.workspaceId, LABEL_SCHEMA_PATH);
      return file.content;
    },
  });

  const linearListLabels = tool({
    name: "linear_list_labels",
    description: "List existing Linear labels (paths under /linear/labels).",
    parameters: z.object({ depth: z.number().int().min(1).max(3).default(1) }),
    async execute({ depth }) {
      const tree = await ws.client.listTree(ws.workspaceId, { path: LABEL_ROOT, depth });
      return JSON.stringify(
        tree.entries.map((e) => ({ path: e.path, type: e.type, revision: e.revision })),
      );
    },
  });

  const linearCreateLabel = tool({
    name: "linear_create_label",
    description:
      "Create a Linear label. Writes a schema-validated draft, polls op status, returns the real Linear UUID.",
    parameters: z.object({
      name: z.string().min(1),
      color: z.string().optional(),
      description: z.string().optional(),
      teamId: z.string().uuid().optional(),
    }),
    async execute(payload) {
      const result = await createLabel(ws, payload);
      return JSON.stringify({
        opStatus: result.receipt.status,
        linearUuid: result.externalId,
        canonicalPath: result.canonicalPath,
        draftPath: result.draftPath,
      });
    },
  });

  const linearReadLabel = tool({
    name: "linear_read_label",
    description: "Read a canonical Linear label record.",
    parameters: z.object({ canonicalPath: z.string() }),
    async execute({ canonicalPath }) {
      const { revision, record } = await readCanonicalLabel(ws, canonicalPath);
      return JSON.stringify({ revision, record });
    },
  });

  const linearUpdateLabel = tool({
    name: "linear_update_label",
    description: "Update a Linear label via PATCH on the canonical path.",
    parameters: z.object({
      canonicalPath: z.string(),
      patch: z.object({
        name: z.string().optional(),
        color: z.string().optional(),
        description: z.string().optional(),
      }),
    }),
    async execute({ canonicalPath, patch }) {
      const current = await readCanonicalLabel(ws, canonicalPath);
      const r = await updateLabel(ws, canonicalPath, current.revision, patch);
      return JSON.stringify({
        opStatus: r.receipt.status,
        newRevision: r.targetRevision,
      });
    },
  });

  const linearDeleteLabel = tool({
    name: "linear_delete_label",
    description: "Delete a Linear label and its draft receipt.",
    parameters: z.object({ canonicalPath: z.string(), draftPath: z.string().optional() }),
    async execute({ canonicalPath, draftPath }) {
      const current = await readCanonicalLabel(ws, canonicalPath);
      const r = await deleteLabel(ws, canonicalPath, current.revision);
      if (draftPath) await deleteDraftIfPresent(ws, draftPath);
      return JSON.stringify({ opStatus: r.receipt.status, deleted: true });
    },
  });

  const agent = new Agent({
    name: "linear-label-manager",
    instructions:
      "You manage Linear labels for the user's workspace through Relayfile. ALWAYS read the " +
      "schema before creating, and ALWAYS prefix label names with `relayfile-writeback-test ` " +
      "and an ISO timestamp so they're easy to clean up. After creating or updating, read the " +
      "canonical record back to confirm. Delete drafts after creating canonicals.",
    tools: [
      linearReadSchema,
      linearListLabels,
      linearCreateLabel,
      linearReadLabel,
      linearUpdateLabel,
      linearDeleteLabel,
    ],
  });

  const result = await run(
    agent,
    "Create a temporary 'demo' label called 'relayfile-writeback-test " +
      new Date().toISOString() +
      "' with color #6366f1 and description 'created from agent'. Then read it back to confirm, " +
      "and finally delete it (and its draft receipt) so the workspace stays clean.",
  );

  console.log("\n── agent output ──");
  console.log(result.finalOutput);
}

main().catch((err) => {
  console.error("agent failed:", err);
  process.exit(1);
});
