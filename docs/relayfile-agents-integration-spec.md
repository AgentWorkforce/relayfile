# Relayfile Agents Integration Spec

**Status:** Draft / Proposal (for blog post reference)  
**Audience:** Blog post readers, future implementers, framework authors  
**Related docs:**
- `docs/guides/agent-vfs-usage.md` (the canonical "how an agent should think about relayfile")
- `docs/agent-workspace-golden-path.md`
- `docs/guides/post-auth-mount-session.md`
- `docs/skills/relayfile-workspace.md`
- `README.md` (current "wrap the client as one tool" guidance)
- `packages/sdk/typescript/` (current client + setup surface)

**Goal of this document:** Provide a clean, referenceable specification so the blog post can say "see the spec" instead of scattering implementation details. This spec outlines current state, proposed patterns for popular agent frameworks, and a potential lightweight "agents" layer. **No implementation is performed here.**

## 1. Philosophy & Positioning

Relayfile's core thesis (repeated across docs and README):

> "Just give the agent files."

LLMs are far better at reading/writing files and using familiar filesystem semantics (`ls`, `cat`, `grep`, pipes, `echo > file`) than they are at juggling dozens of MCP tool schemas or bespoke SDK calls.

Relayfile materializes SaaS integrations (Notion, Linear, GitHub, Slack, Gmail, etc.) as a real, versioned, ACL'd, real-time-synced virtual filesystem.

This is **complementary** to (not a replacement for):
- MCP servers (great for typed, validated single actions)
- Code Mode / agent code execution (great for orchestration inside a sandbox)
- Skills (procedural guidance, can live alongside or inside the FS)
- Full agent harnesses (Flue, Eve, OpenAI Agents SDK, LangChain, etc.)

**Key primitives relayfile already gives agents:**
- Exhaustive discovery via directory listings + `_index.json` + `LAYOUT.md` + per-provider `.layout.md`
- Cheap context via deterministic digests (`/digests/yesterday.md`, etc.)
- Natural cross-provider composition (pipes, `grep -r`, `cp` between providers)
- Writeback as ordinary file writes (with schema discovery in `_PERMISSIONS.md`)
- Real-time events (file.created / file.updated) for proactivity
- Multi-agent coordination through the same shared, ACL'd filesystem (no extra message bus required for many cases)
- Sandbox-friendly mounts (local FUSE, poll-based mirror, post-auth mount sessions for Daytona/E2B/etc.)

## 2. Current State (What Already Works Today)

### 2.1 Mount-First (Strongly Recommended)

The golden path for most agents:

1. `npx relayfile setup --provider ... --local-dir ./relayfile-mount`
2. Set `RELAYFILE_LOCAL_DIR` (or pass the path)
3. Agent uses native file tools / `fs` / shell.

This is the "just give the agent files" experience. It works in:
- Claude Code / Codex / any coding agent with filesystem access
- Local Node processes
- Sandboxes that expose a real filesystem

See:
- `docs/guides/agent-vfs-usage.md`
- `docs/skills/relayfile-workspace.md` (the skill agents should load)
- `examples/` (direct usage demos)

### 2.2 Programmatic Client (When You Need a Tool or No Mount)

```ts
import { RelayFileClient } from "@relayfile/sdk";

const client = new RelayFileClient({ token: process.env.RELAYFILE_TOKEN! });

export async function readRelayfile(path: string) {
  return client.readFile(workspaceId, path);
}

export async function writeRelayfile(path: string, content: string, contentType = "application/json") {
  return client.writeFile({ workspaceId, path, content, contentType, baseRevision: "*" });
}
```

The README already calls this out explicitly as the way to integrate with:
- Vercel AI SDK
- Claude Agent SDK / MCP tools
- OpenAI Agents SDK
- Any custom harness

See `README.md` "Programmatic agents" section.

### 2.3 Sandbox / Post-Auth Mount Surface

For cloud sandboxes (Daytona, E2B, Modal, etc.):

- `RelayfileSetup` + `workspace.mountEnv()`
- `mountWorkspace()` / `ensureMountedWorkspace()` (see `docs/guides/post-auth-mount-session.md`)
- Returns a scoped token + mount handle without leaking full cloud credentials.

This already targets the exact use case of "run my agent inside someone else's sandbox and give it relayfile context."

### 2.4 Multi-Agent + Relaycast

- `workspace.agentInvite({ agentName })` (or scoped variant)
- Same workspace = same relayfile mount + shared relaycast room
- Agents coordinate by writing files + sending messages (or just by observing each other's writes in the FS)

See golden path docs.

### 2.5 Gaps in Current Story

- No first-class, framework-specific adapter packages (Mirage has `@struktoai/mirage-agents`).
- Documentation and examples are scattered (README + agent-vfs-usage + examples/ + golden path).
- No standardized "relayfile tool" shape that frameworks can auto-discover.
- No dedicated examples or recipes for Flue or Eve yet.
- Skills exist, but no packaged "agent framework starter" that pulls in the right skill + tool wrappers + mount bootstrap.

## 3. Proposed Integration Patterns (Framework by Framework)

All patterns should follow these principles:
- Prefer the mount when the framework/sandboxes support real filesystem access.
- Fall back to 1-2 thin tools (`relayfile_read`, `relayfile_write`) when the framework is purely tool-calling.
- Always surface `LAYOUT.md`, digests, and `_PERMISSIONS.md` for self-description.
- Make the skill (`relayfile-workspace.md`) loadable or auto-included.
- Support both "lead agent sets up the workspace" and "invited agent joins an existing one."

### 3.1 Eve (Vercel) – Filesystem-First

Eve's model: an agent *is* a directory (`agent/instructions.md`, `agent/tools/*.ts`, `agent/skills/*.md`, `agent/connections/*.ts`, `agent/sandbox/`).

**Primary recommended pattern: Mount into the sandbox filesystem.**

In Eve's sandbox bootstrap or `sandbox/sandbox.ts`:

```ts
import { ensureMountedWorkspace } from "@relayfile/sdk";

export default async function bootstrapSandbox() {
  const handle = await ensureMountedWorkspace({
    workspaceId: process.env.RELAYFILE_WORKSPACE_ID!,
    localDir: "/workspace/relayfile",   // appears inside Eve's sandbox FS
    provider: "notion",                  // optional readiness gate
  });

  return {
    relayfileMount: "/workspace/relayfile",
    // Eve can now use its native fs/shell tools against this path
  };
}
```

Then in Eve tools or directly in the sandbox:

```bash
# inside the agent's sandbox shell / tools
cat /workspace/relayfile/notion/pages/abc123/content.md
echo '{"title":"Updated"}' > /workspace/relayfile/linear/issues/BUG-42/metadata.json
```

**Secondary pattern (if staying purely in `defineTool`):**

```ts
// agent/tools/relayfile.ts
import { defineTool } from "eve/tools";
import { z } from "zod";
import { RelayFileClient } from "@relayfile/sdk";

const client = new RelayFileClient({ token: process.env.RELAYFILE_TOKEN! });

export const readRelayfile = defineTool({
  description: "Read a file from the relayfile integration filesystem.",
  inputSchema: z.object({ path: z.string() }),
  execute: ({ path }) => client.readFile(workspaceId, path),
});

export const writeRelayfile = defineTool({ ... });
```

Also define a connection file for any relayfile-as-MCP surface if desired.

**Eve-specific niceties to document:**
- How to seed the sandbox with a relayfile mount at startup.
- Using Eve's subagent + sandbox isolation with scoped relayfile tokens.
- Surfacing relayfile events as Eve schedules/channels.

### 3.2 Flue (Astro) – Sandbox Harness + Skills + MCP

Flue provides a programmable TypeScript harness with sandboxes (local, virtual, remote), tools, skills (`.SKILL.md`), instructions, durable execution, and explicit MCP support.

**Primary pattern: Give the sandbox a relayfile mount + use Flue's native FS access.**

In the Flue agent definition:

```ts
import { createAgent } from '@flue/runtime';
import { local } from '@flue/runtime/node';
import { ensureMountedWorkspace } from "@relayfile/sdk";

const mountHandle = await ensureMountedWorkspace({
  workspaceId: "...",
  localDir: "/workspace/relayfile",
});

export default createAgent(() => ({
  model: "...",
  sandbox: local({ root: mountHandle.localDir }), // or virtual sandbox + seed
  skills: [import('../skills/relayfile-workspace.SKILL.md')],
  instructions: `... Use the files under /workspace/relayfile ...`,
  tools: [ /* optional thin wrappers if needed */ ],
}));
```

Flue already has strong filesystem tooling inside sandboxes (grep, edit, etc.). Relayfile just populates that filesystem with live integration data.

**Alternative: MCP + tools layer**

Flue supports `connectMcpServer`. We can expose relayfile as an MCP server (or use the client wrapped as tools) for frameworks that prefer that surface.

**Flue-specific:**
- Leverage Flue's skill loading for `relayfile-workspace.SKILL.md`.
- Use Flue's subagents with different scoped tokens on the same workspace.
- Combine with Flue's Code Mode / shell execution inside the relayfile-mounted tree.

### 3.3 Other Popular Frameworks (Broad Patterns)

**Vercel AI SDK / OpenAI Agents SDK / similar "defineTool" style**

Exactly as currently documented in the README:

```ts
import { tool } from "ai"; // or openai agents equivalent
import { z } from "zod";
import { RelayFileClient } from "@relayfile/sdk";

const client = new RelayFileClient({ token });

export const relayfileTool = tool({
  description: "Read or write files in the relayfile integration filesystem.",
  parameters: z.object({
    action: z.enum(["read", "write", "list"]),
    path: z.string(),
    content: z.string().optional(),
  }),
  execute: async ({ action, path, content }) => {
    if (action === "read") return client.readFile(workspaceId, path);
    if (action === "write") return client.writeFile({ workspaceId, path, content });
    return client.listTree(workspaceId, { path });
  },
});
```

**LangChain / LlamaIndex style**

Provide a `RelayfileLoader`, `RelayfileTool`, or `RelayfileRetriever` that uses the client under the hood. Or just document "mount the workspace and use LangChain's `DirectoryLoader` + `TextLoader`".

**Claude Code / Codex / Coding Agents**

- Use the mount (`RELAYFILE_LOCAL_DIR`).
- Drop the `relayfile-workspace` skill into `.claude/skills/` or equivalent.
- Optionally provide a thin MCP server wrapper so the coding agent can discover relayfile as a connected MCP server.

**General "Agent as Code" (Mastra, CrewAI ports, etc.)**

Two clean options:
1. Mount + let the agent use `fs` / shell inside its runtime.
2. Export a small set of typed tools from `@relayfile/agents` (or a thin local wrapper).

### 3.4 Proposed `@relayfile/agents` (Lightweight) Surface

We do **not** need a heavy new framework. A small, optional package can dramatically improve discoverability and DX, modeled after Mirage's `@struktoai/mirage-agents`.

Suggested structure (spec only):

```
@relayfile/agents
  /eve
    mountIntoSandbox.ts
    createRelayfileTools.ts   # returns Eve-compatible tool defs
  /flue
    createRelayfileTools.ts
    relayfileWorkspaceSkill.ts
  /vercel-ai
  /openai-agents
  /langchain
  index.ts                    # re-exports + common types
  types.ts                    # RelayfileTool, RelayfileContext, etc.
```

Common helpers that could live here:
- `createRelayfileReadTool(workspaceId, clientOptions?)`
- `createRelayfileWriteTool(...)`
- `createRelayfileListTool(...)`
- `getRelayfileContext(workspaceId)` → returns digests + LAYOUT summary for prompt injection
- Sandbox mount helpers that are framework-aware

This package would be **thin** — mostly sugar + documentation + framework-specific glue. The heavy lifting stays in the core SDK + the mount daemon.

## 4. Example Layout Recommendations

To support the blog post and future users, we should have:

```
relayfile/
  examples/
    frameworks/
      eve-basic-mount/
      eve-tool-wrapper/
      flue-sandbox-mount/
      flue-with-skills/
      vercel-ai-sdk-tool/
      openai-agents-tool/
      langchain-retriever/
  docs/
    integrations/
      eve.md
      flue.md
      vercel-ai-sdk.md
      ...
    relayfile-agents-integration-spec.md   # this document
```

Each example should be minimal, runnable, and show:
- How the workspace is obtained (golden path or assumed env)
- The mount vs. tool decision
- A tiny agent loop demonstrating read + synthesis + writeback
- How the skill / instructions are provided

## 5. Open Questions / Decisions for Later

- Should we ship a real `@relayfile/agents` package in v1, or just docs + examples for now?
- MCP server wrapper for relayfile (expose the VFS as MCP resources + tools)? Useful for frameworks that only speak MCP.
- Automatic skill injection (e.g., when a coding agent connects relayfile, auto-drop the workspace skill)?
- Observability hooks (tie relayfile events into Flue/Eve tracing)?
- Conflict / writeback UX inside agent frameworks (should the framework surface dead-letter handling nicely)?

## 6. How to Reference This From the Blog Post

Suggested phrasing:

> "For concrete integration recipes with Eve, Flue, Vercel AI SDK, OpenAI Agents, LangChain, and others, see the [Relayfile Agents Integration Spec](https://github.com/.../relayfile/docs/relayfile-agents-integration-spec.md). The spec shows both the 'mount into the sandbox' path (Eve's favorite) and the thin 'one or two tools' path, plus how the existing `RelayfileSetup` + `agent-vfs-usage` skill compose with each harness."

Also link the skill, the golden path, and the agent-vfs-usage guide.

---

This spec is intentionally implementation-free. When we're ready, the actual work would be:
- More examples
- Possibly a small `@relayfile/agents` package
- Dedicated docs pages under `docs/integrations/`
- Updates to the main README and skills

Location of this spec: `relayfile/docs/relayfile-agents-integration-spec.md` (inside the relayfile subtree).