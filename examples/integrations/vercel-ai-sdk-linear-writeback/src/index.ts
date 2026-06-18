/**
 * Vercel AI SDK agent that creates / updates / deletes Linear labels via
 * Relayfile. The agent reads the labels schema, builds a valid payload, writes
 * a draft, polls for the adapter rewrite, then can read/update/delete the
 * resulting canonical record.
 *
 * Run:
 *   ANTHROPIC_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { anthropic } from "@ai-sdk/anthropic";
import { generateText, tool } from "ai";
import { z } from "zod";

import { connectWorkspace } from "./bootstrap.js";
import {
  LABEL_ROOT,
  LABEL_SCHEMA_PATH,
  createLabel,
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

  const tools = {
    linear_read_labels_schema: tool({
      description: "Read the Linear labels writeback schema and its create example.",
      parameters: z.object({}),
      async execute() {
        const file = await ws.client.readFile(ws.workspaceId, LABEL_SCHEMA_PATH);
        return JSON.parse(file.content);
      },
    }),
    linear_list_labels: tool({
      description: "List existing Linear labels (paths under /linear/labels).",
      parameters: z.object({
        depth: z.number().int().min(1).max(3).default(1),
      }),
      async execute({ depth }) {
        const tree = await ws.client.listTree(ws.workspaceId, {
          path: LABEL_ROOT,
          depth,
        });
        return tree.entries.map((e) => ({
          path: e.path,
          type: e.type,
          revision: e.revision,
        }));
      },
    }),
    linear_create_label: tool({
      description:
        "Create a Linear label. Writes a schema-validated draft and polls for the adapter to materialise the canonical record.",
      parameters: z.object({
        name: z.string().min(1),
        color: z.string().optional(),
        description: z.string().optional(),
        teamId: z.string().uuid().optional(),
      }),
      async execute(payload) {
        const result = await createLabel(ws, payload);
        return {
          opStatus: result.receipt.status,
          linearUuid: result.externalId,
          canonicalPath: result.canonicalPath,
          url: result.url,
          draftPath: result.draftPath,
          note:
            "The draft file at draftPath persists as a receipt; delete it after reading or use sweepWritebackDrafts to clean residue.",
        };
      },
    }),
    linear_read_label: tool({
      description: "Read a canonical Linear label record.",
      parameters: z.object({
        canonicalPath: z.string().describe("e.g. /linear/labels/<uuid>.json"),
      }),
      async execute({ canonicalPath }) {
        const { revision, record } = await readCanonicalLabel(ws, canonicalPath);
        return { revision, record };
      },
    }),
    linear_update_label: tool({
      description:
        "Update mutable fields on a Linear label via PATCH. Reads the current revision first to avoid conflicts.",
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
        return { previousRevision: current.revision, newRevision: r.targetRevision };
      },
    }),
    linear_delete_label: tool({
      description: "Delete a Linear label (requires current revision for If-Match).",
      parameters: z.object({
        canonicalPath: z.string(),
      }),
      async execute({ canonicalPath }) {
        const current = await readCanonicalLabel(ws, canonicalPath);
        await deleteLabel(ws, canonicalPath, current.revision);
        return { deletedPath: canonicalPath };
      },
    }),
  };

  const result = await generateText({
    model: anthropic("claude-sonnet-4-6"),
    tools,
    maxSteps: 8,
    system:
      "You manage Linear labels for the user's workspace through Relayfile. ALWAYS read the " +
      "schema before creating, and ALWAYS prefix label names with `relayfile-writeback-test ` " +
      "and an ISO timestamp so they're easy to clean up. After creating or updating, read the " +
      "canonical record back to confirm.",
    prompt:
      "Create a temporary 'demo' label called 'relayfile-writeback-test " +
      new Date().toISOString() +
      "' with color #6366f1 and description 'created from agent'. Then read it back to confirm, " +
      "and finally delete it so the workspace stays clean. Report each step.",
  });

  console.log("\n── agent summary ──");
  console.log(result.text);
  console.log(`\n(${result.steps.length} tool-call steps)`);
}

main().catch((err) => {
  console.error("agent failed:", err);
  process.exit(1);
});
