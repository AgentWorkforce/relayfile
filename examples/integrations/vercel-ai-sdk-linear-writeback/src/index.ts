/**
 * Vercel AI SDK agent that creates / updates / deletes Linear labels via
 * Relayfile (schema-validated, comprehensive op-status proof in the package).
 *
 * Run:
 *   ANTHROPIC_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 */

import { anthropic } from "@ai-sdk/anthropic";
import { generateText, tool } from "ai";
import { z } from "zod";

import { connect } from "@relayfile/agents";

const rf = await connect({
  scopes: [
    "relayfile:fs:read:/discovery/linear/**",
    "relayfile:fs:read:/linear/**",
    "relayfile:fs:write:/linear/**",
  ],
});

const result = await generateText({
  model: anthropic("claude-sonnet-4-6"),
  maxSteps: 8,
  system:
    "You manage Linear labels via Relayfile. Always prefix label names with " +
    "`relayfile-writeback-test ` and an ISO timestamp so they're easy to clean up. " +
    "After creating, delete both the canonical and the draft receipt.",
  prompt:
    "Create a temporary 'demo' Linear label, read it back, then delete it so the workspace stays clean.",
  tools: {
    linear_create_label: tool({
      description: "Create a Linear label (schema-validated). Returns the real Linear UUID and canonical path.",
      parameters: z.object({
        name: z.string().min(1),
        color: z.string().optional(),
        description: z.string().optional(),
      }),
      async execute(payload) {
        const r = await rf.writeback.create("/linear/labels", payload);
        return {
          linearUuid: r.externalId,
          canonicalPath: r.canonicalPath,
          draftPath: r.draftPath,
        };
      },
    }),
    linear_read_label: tool({
      description: "Read a canonical Linear label record at /linear/labels/<uuid>.json.",
      parameters: z.object({ canonicalPath: z.string() }),
      async execute({ canonicalPath }) {
        const file = await rf.client.readFile(rf.workspaceId, canonicalPath);
        return { revision: file.revision, record: JSON.parse(file.content) };
      },
    }),
    linear_delete_label: tool({
      description: "Delete a Linear label canonical (propagates to Linear) and its draft receipt (local-only).",
      parameters: z.object({ canonicalPath: z.string(), draftPath: z.string().optional() }),
      async execute({ canonicalPath, draftPath }) {
        const current = await rf.client.readFile(rf.workspaceId, canonicalPath);
        await rf.writeback.delete(canonicalPath, current.revision);
        if (draftPath) await rf.writeback.deleteDraft(draftPath);
        return { deleted: true };
      },
    }),
  },
});

console.log(result.text);
