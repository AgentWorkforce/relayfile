# Review live work across machines before the PR

The public launch demo is self-contained: one agent reviews live, uncommitted
code in a fresh Daytona sandbox, a second agent fixes that same file after a
hash-gated handoff, and the laptop independently verifies the final bytes.
It requires a Bash environment with `mktemp`, Node.js 22.22.0+, npm, curl, and
access to a Claude provider that can be connected during setup.

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

`validate` releases both agents and deletes the exact sandbox after the proof.
The script prints view- and drive-mode attach commands while the agents are
running. The reviewer is role-read-only today, not permission-enforced.

## Lower-level replication proof

The runnable lower-level proof is [`examples/live-workspace`](../../examples/live-workspace/README.md).
It provisions a fresh Daytona node through `agent-relay fleet spawn --sandbox`,
starts an agent in the Relayfile mount returned by Cloud, and verifies on the
laptop that the agent's uncommitted file and SHA-256 sidecar arrived intact.

```bash
cd examples/live-workspace
npm install
npm run setup  # first time only
npm run preflight
npm run proof
```

This is a terminal proof, not a localhost dashboard. A run passes only when the
remote mount is live, the nonce matches, and the sandbox and laptop SHA-256
values are identical.

For the founder recording, public self-serve commands, narration, and cleanup
guardrails, use the [launch playbook](../../examples/live-workspace/LAUNCH_PLAYBOOK.md).

The command prints the exact drive-mode attach command after the agent launches.
Replace `--mode drive` with `--mode view` for read-only access, and press
`Ctrl+C` to detach without killing the agent.

For the reviewed multi-provider evidence behind the feature, see
[`docs/evidence/cloud-five-agent-collaboration-20260822`](../evidence/cloud-five-agent-collaboration-20260822/README.md).
