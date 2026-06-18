import { tool } from "@langchain/core/tools";
import { z } from "zod";

import type { RelayfileAgents } from "../connect.js";

export interface LangChainToolsOptions {
  /** Restrict read tools to these workspace prefixes. Defaults to whole workspace. */
  readPaths?: string[];
}

/** LangChain tools wired to the Relayfile workspace. */
export function langchainTools(rf: RelayfileAgents, opts: LangChainToolsOptions = {}) {
  const readRoots = opts.readPaths;

  return [
    tool(
      async ({ path, depth }: { path: string; depth: number }) => {
        if (readRoots && !readRoots.some((r) => path.startsWith(r))) {
          throw new Error(`path ${path} is outside the allowed read roots`);
        }
        const tree = await rf.client.listTree(rf.workspaceId, { path, depth });
        return JSON.stringify(
          tree.entries.map((e) => ({ path: e.path, type: e.type, revision: e.revision })),
        );
      },
      {
        name: "relayfile_list_tree",
        description:
          "List entries in the Relayfile workspace tree. Returns each entry's path, type, and revision.",
        schema: z.object({
          path: z.string(),
          depth: z.number().int().min(1).max(4).default(2),
        }),
      },
    ),
    tool(
      async ({ path }: { path: string }) => {
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
      {
        name: "relayfile_read_file",
        description: "Read a single file's content from the Relayfile workspace.",
        schema: z.object({ path: z.string() }),
      },
    ),
  ];
}
