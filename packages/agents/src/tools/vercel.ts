import { tool } from "ai";
import { z } from "zod";

import type { RelayfileAgents } from "../connect.js";

export interface VercelToolsOptions {
  /** Restrict read tools to these workspace prefixes. Defaults to whole workspace. */
  readPaths?: string[];
}

/** Vercel AI SDK tools wired to the Relayfile workspace. */
export function vercelTools(rf: RelayfileAgents, opts: VercelToolsOptions = {}) {
  const readRoots = opts.readPaths;

  return {
    relayfile_list_tree: tool({
      description:
        "List entries in the Relayfile workspace tree. Returns each entry's path, type (file|dir), and revision.",
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
        return tree.entries.map((e) => ({
          path: e.path,
          type: e.type,
          revision: e.revision,
        }));
      },
    }),

    relayfile_read_file: tool({
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
        return {
          path: file.path,
          revision: file.revision,
          contentType: file.contentType,
          content: file.content,
        };
      },
    }),
  };
}
