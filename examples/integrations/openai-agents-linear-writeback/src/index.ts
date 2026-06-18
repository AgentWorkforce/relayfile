/**
 * OpenAI Agents SDK agent creates / updates / deletes Linear labels via
 * Relayfile (schema-validated, comprehensive op-status proof in the package).
 *
 * Run:
 *   OPENAI_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { Agent, run, tool } from "@openai/agents";
import { z } from "zod";

import { connect } from "@relayfile/agents";

const rf = await connect({
  scopes: [
    "relayfile:fs:read:/discovery/linear/**",
    "relayfile:fs:read:/linear/**",
    "relayfile:fs:write:/linear/**",
  ],
});

const linearCreate = tool({
  name: "linear_create_label",
  description: "Create a Linear label. Returns the real Linear UUID + canonical path.",
  parameters: z.object({
    name: z.string().min(1),
    color: z.string().optional(),
    description: z.string().optional(),
  }),
  async execute(args: { name: string; color?: string; description?: string }) {
    const r = await rf.writeback.create("/linear/labels", args);
    return JSON.stringify({
      linearUuid: r.externalId,
      canonicalPath: r.canonicalPath,
      draftPath: r.draftPath,
    });
  },
});

const linearDelete = tool({
  name: "linear_delete_label",
  description: "Delete a Linear label canonical (propagates to Linear) and its draft receipt.",
  parameters: z.object({ canonicalPath: z.string(), draftPath: z.string().optional() }),
  async execute(args: { canonicalPath: string; draftPath?: string }) {
    const current = await rf.client.readFile(rf.workspaceId, args.canonicalPath);
    await rf.writeback.delete(args.canonicalPath, current.revision);
    if (args.draftPath) await rf.writeback.deleteDraft(args.draftPath);
    return JSON.stringify({ deleted: true });
  },
});

const agent = new Agent({
  name: "linear-label-manager",
  instructions:
    "You manage Linear labels via Relayfile. Prefix label names with " +
    "`relayfile-writeback-test ` and an ISO timestamp. Delete the label and its draft after creating.",
  tools: [linearCreate, linearDelete],
});

const result = await run(
  agent,
  "Create a temporary demo Linear label, then delete it so the workspace stays clean.",
);

console.log(result.finalOutput);
