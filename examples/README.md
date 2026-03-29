# relayfile SDK examples

End-to-end examples showing how agents interact with the relayfile virtual filesystem.

## Prerequisites

- **Docker** — needed to run the relayfile server locally (`docker compose up`)
- Node.js 18+

```bash
npm install @relayfile/sdk
npm install -D tsx
```

Each example reads configuration from environment variables:

```bash
export RELAYFILE_TOKEN="ey…"   # JWT for your agent
export WORKSPACE_ID="ws_demo"  # target workspace
```

Generate tokens with [relayauth](https://github.com/AgentWorkforce/relayauth):

```bash
relayauth sign --workspace ws_demo --agent my-agent --scope "fs:read" --scope "fs:write"
```

## Examples

| # | Directory | What it shows |
|---|-----------|---------------|
| 01 | [agent-reads-files](./01-agent-reads-files/) | `listTree`, `readFile`, `queryFiles` — browse a workspace |
| 02 | [agent-writes-files](./02-agent-writes-files/) | `writeFile`, `bulkWrite`, optimistic locking, conflict detection |
| 03 | [webhook-to-vfs](./03-webhook-to-vfs/) | `ingestWebhook`, `computeCanonicalPath` — external events to files |
| 04 | [realtime-events](./04-realtime-events/) | `getEvents` polling — watch for file changes with cursors |
| 05 | [relayauth-scoped-agent](./05-relayauth-scoped-agent/) | Path-scoped tokens, 403 rejection, least-privilege agents |
| 06 | [writeback-consumer](./06-writeback-consumer/) | `listPendingWritebacks`, `ackWriteback` — push VFS changes back to GitHub |

## Running

```bash
cd examples/01-agent-reads-files
npx tsx index.ts
```

Each example is self-contained — run them in any order.
Examples 01 and 04 are read-only; the others write data.
