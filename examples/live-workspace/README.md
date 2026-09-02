# Relayfile cross-machine proof

## Launch demo: live review before the PR

For the human-facing launch, use the self-contained live reviewer/builder demo.
It needs no repository checkout and cleans up its exact Daytona sandbox in
`validate` mode. It requires a Bash environment with `mktemp`, Node.js 22.22.0+,
npm, curl, and access to a Claude provider that can be connected during setup:

The immutable launch script pins agent-relay 11.10.1 and Relayfile/SDK 0.10.52,
the exact combination validated by that revision. The checked-in lower-level
proof below uses the same versions.

```bash
LIVE_REVIEW_REV=53dfd49f532d362bbced37b12b1c41725cee4b5a
LIVE_REVIEW_SHA256=69272a35ed3ec3d3294fe9f9bdc14ab0dc9b5f642e379098591d0b378e0c9863
LIVE_REVIEW_SCRIPT="$(mktemp "${TMPDIR:-/tmp}/relayfile-live-review.XXXXXX")" &&
curl -fsSL "https://gist.githubusercontent.com/khaliqgant/e9ee531e63a9048f612e12979f50d2ae/raw/$LIVE_REVIEW_REV/live-review.sh" -o "$LIVE_REVIEW_SCRIPT" &&
node -e 'const fs=require("fs"),c=require("crypto"),[p,w]=process.argv.slice(1),g=c.createHash("sha256").update(fs.readFileSync(p)).digest("hex");if(g!==w)throw Error("SHA-256 mismatch");console.log("SHA-256 verified")' "$LIVE_REVIEW_SCRIPT" "$LIVE_REVIEW_SHA256" &&
bash "$LIVE_REVIEW_SCRIPT" setup &&
bash "$LIVE_REVIEW_SCRIPT" validate
```

One agent reviews an unsafe, uncommitted payment webhook. The laptop verifies
the review-time source hash, then authorizes a second agent to fix the same live
file. The reviewer and laptop independently approve the exact final bytes. The
script prints commands to attach to both agents while the proof runs.

See the [launch playbook](./LAUNCH_PLAYBOOK.md) for the two-terminal recording
flow, narration, claims, and caveats.

## The human version

A cloud agent saves a file in its normal folder. That same file appears on your
laptop. Relayfile checks that the bytes match, then gives you a command to jump
into the still-running agent.

No Git commit. No upload button. No localhost simulation.

## Couldn't I do this without Relayfile?

Yes, but you have to operate the handoff yourself:

- Git needs the agent to commit and push, then the human to pull.
- `scp`, rsync, and file-sync tools need machine addresses, credentials, and
  path-specific setup.
- S3 or a custom API makes both sides translate files into upload/download calls.
- A shared disk usually requires both machines to live inside the same
  infrastructure boundary.

Relayfile removes that choreography. The agent uses ordinary filesystem calls;
the human uses ordinary files or the Relayfile CLI; the workspace identity—not
the machine—is what they share. Uncommitted work can cross the boundary without
turning every handoff into a deployment or an integration project.

## What this unlocks

Available today:

- inspect a cloud agent's live, uncommitted work from your laptop
- hand files between local tools and sandboxed agents without Git
- let multiple surfaces work against one named workspace
- verify and audit the exact artifact that crossed the boundary

Natural next steps:

- move or resume an agent on another machine with its working context intact
- keep long-running agents alive while local controllers disconnect
- let specialist agents collaborate through shared artifacts instead of pasted
  prompts and bespoke APIs
- make approvals, tests, digests, and human edits visible wherever the work runs

The current demo proves cross-machine byte replication. Long-running Agent37
reattach and live teleport are a separate fast-follow and should not be claimed
until their full dev acceptance flow passes.

This lower-level proof provisions a fresh Daytona sandbox, mounts the current
Relayfile workspace, starts one agent inside that mount, and waits for the
agent's uncommitted proof file to arrive on this machine.

There is no localhost app and no simulated event stream. The command prints
`PASS` only after this machine independently verifies the remote file against a
SHA-256 sidecar produced in the sandbox.

The lower-level example below proves the replication primitive with one agent.

## Run it

Prerequisites:

- Node.js 22.22.0 or newer
- `agent-relay` 11.10.1 or newer (the example pins it)
- `relayfile` 0.10.52 or newer (the example pins it)
- `relayfile` authenticated to the same Agent Relay Cloud workspace
- a connected Claude provider in Agent Relay Cloud

```bash
cd examples/live-workspace
npm install
npm run setup  # first time only
npm run proof
```

`npm run setup` signs in to Relayfile's shared Agent Relay Cloud session and
connects Claude. `npm run preflight` is read-only and fails before sandbox
provisioning if the CLI versions, Cloud session, or active Relayfile workspace
are missing.

The command prints an attach command as soon as the agent launches:

```bash
npx agent-relay@11.10.1 node agent attach <agent> --node <sandbox> --mode drive
```

Use `--mode view` for a read-only session. `Ctrl+C` detaches without killing the
agent.

The proof requires all of these gates:

1. `agent-relay fleet spawn claude --sandbox` reports a real mounted Daytona node.
2. The worker's current directory contains `.relay/state.json` and a live
   `relayfile-mount` process exists.
3. The worker writes a nonce-bearing JSON artifact and SHA-256 sidecar using
   ordinary filesystem calls only—no Git, curl, host path, or network API.
4. This machine reads both artifacts through the Relayfile CLI.
5. The nonce matches and the independently computed SHA-256 equals the remote
   sidecar.

Any missing gate is `FAIL`, not a degraded preview.

## Options

```bash
# Pick another supported agent CLI.
npm run proof -- --provider codex

# Assert the active Relayfile workspace name before provisioning.
npm run proof -- --workspace default

# Keep waiting for five minutes after the agent launches.
npm run proof -- --timeout 300

# Reuse a known mounted node without provisioning another sandbox.
npm run proof -- \
  --node relayfile-wow-daytona-0831b \
  --mount-path /home/daytona/workspace

# Inspect the exact spawn/task plan without changing external state.
npm run dry-run

# Verify tooling, Cloud auth, and workspace selection without provisioning.
npm run preflight
```

Reuse mode deliberately skips the broker's placement-result wait. The
nonce-and-SHA proof itself confirms that the agent launched and wrote from the
mounted machine. Fresh-sandbox mode still requires Cloud to confirm the mount
and agent launch before polling.

## Cleanup

The proof intentionally leaves the agent running so you can attach to it. When
finished, release it with the exact command printed by the demo:

```bash
npx agent-relay@11.10.1 fleet release <agent-name>
```

The lower-level proof deliberately keeps its successful sandbox alive for
attach. Reuse the named node for additional takes and remove it from Cloud fleet
controls when finished. For a fresh sandbox on every run with deterministic
agent and sandbox cleanup, use the launch demo's `validate` mode above.

## What the result proves

The result proves that a specific byte sequence written by an agent in a fresh
Daytona machine became readable through the same Relayfile workspace on this
machine, without a commit or Git handoff. The printed propagation figure is
derived from the remote write timestamp and the local successful read; clock
skew is reported instead of hidden.
