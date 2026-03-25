# @relayfile/sdk

TypeScript SDK for relayfile — real-time filesystem for humans and agents.

## Install

```bash
npm install @relayfile/sdk
```

## Quick Example

```ts
import { RelayFileClient } from "@relayfile/sdk";

const client = new RelayFileClient({
  baseUrl: "https://api.relayfile.com",
  token: process.env.RELAYFILE_TOKEN ?? ""
});

const workspaceId = "workspace_123";

const tree = await client.listTree(workspaceId, {
  path: "/",
  depth: 2
});
console.log(tree.entries.map((entry) => entry.path));

const file = await client.readFile(workspaceId, "/notes/todo.md");
console.log(file.content);

await client.writeFile({
  workspaceId,
  path: "/notes/todo.md",
  baseRevision: file.revision,
  content: `${file.content}\n- Follow up with SDK publish`,
  contentType: "text/markdown"
});
```

## Full Docs

Full documentation is available at https://github.com/AgentWorkforce/relayfile/tree/main/docs
