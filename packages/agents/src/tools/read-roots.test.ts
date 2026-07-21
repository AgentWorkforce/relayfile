import { describe, expect, it, vi } from "vitest";

import type { RelayfileAgents } from "../connect.js";
import { langchainTools } from "./langchain.js";
import { openaiTools } from "./openai.js";
import { vercelTools } from "./vercel.js";

function fakeRelayfile() {
  const readFile = vi.fn(async () => ({ path: "/allowed/file.md", content: "ok" }));
  const listTree = vi.fn(async () => ({ entries: [] }));
  return {
    readFile,
    listTree,
    rf: { workspaceId: "workspace", client: { readFile, listTree } } as unknown as RelayfileAgents,
  };
}

describe("agent framework read-root filters", () => {
  it("Vercel rejects an out-of-prefix read before calling Relayfile", async () => {
    const { rf, readFile } = fakeRelayfile();
    const tools = vercelTools(rf, { readPaths: ["/allowed"] });

    await expect(tools.relayfile_read_file.execute?.({ path: "/denied/file.md" }, {} as never))
      .rejects.toThrow("outside the allowed read roots");
    expect(readFile).not.toHaveBeenCalled();
  });

  it("OpenAI rejects an out-of-prefix read before calling Relayfile", async () => {
    const { rf, readFile } = fakeRelayfile();
    const tool = openaiTools(rf, { readPaths: ["/allowed"] })[1];

    const result = await tool.invoke(undefined as never, JSON.stringify({ path: "/denied/file.md" }));
    expect(result).toContain("outside the allowed read roots");
    expect(readFile).not.toHaveBeenCalled();
  });

  it("LangChain rejects an out-of-prefix read before calling Relayfile", async () => {
    const { rf, readFile } = fakeRelayfile();
    const tool = langchainTools(rf, { readPaths: ["/allowed"] })[1];

    await expect(tool.invoke({ path: "/denied/file.md" })).rejects.toThrow("outside the allowed read roots");
    expect(readFile).not.toHaveBeenCalled();
  });
});
