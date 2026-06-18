import { tool } from "@openai/agents";
import { z } from "zod";

import type { RelayfileAgents } from "../connect.js";

export interface OpenAIToolsOptions {
  /** Restrict read tools to these workspace prefixes. Defaults to whole workspace. */
  readPaths?: string[];
}

/** OpenAI Agents SDK tools wired to the Relayfile workspace. */
export function openaiTools(rf: RelayfileAgents, opts: OpenAIToolsOptions = {}) {
  const readRoots = opts.readPaths;

  return [
    tool({
      name: "relayfile_list_tree",
      description:
        "List entries in the Relayfile workspace tree. Returns each entry's path, type, and revision.",
      parameters: z.object({
        path: z.string().describe("Absolute workspace path, e.g. /notion or /linear/labels."),
        depth: z.number().int().min(1).max(4).default(2),
      }),
      async execute(args: { path: string; depth: number }) {
        const { path, depth } = args;
        if (readRoots && !readRoots.some((r) => path.startsWith(r))) {
          throw new Error(`path ${path} is outside the allowed read roots`);
        }
        const tree = await rf.client.listTree(rf.workspaceId, { path, depth });
        return JSON.stringify(
          tree.entries.map((e) => ({ path: e.path, type: e.type, revision: e.revision })),
        );
      },
    }),
    tool({
      name: "relayfile_read_file",
      description: "Read a single file's content from the Relayfile workspace.",
      parameters: z.object({
        path: z.string().describe("Absolute workspace path."),
      }),
      async execute(args: { path: string }) {
        const { path } = args;
        if (readRoots && !readRoots.some((r) => path.startsWith(r))) {
          throw new Error(`path ${path} is outside the allowed read roots`);
        }
        const file = await rf.client.readFile(rf.workspaceId, path);
        return JSON.stringify({
          path: file.path,
          revision: file.revision,
          contentType: file.contentType,
          content: file.content,
        });
      },
    }),
  ];
}
