# Review live work across machines before the PR

The public launch demo is self-contained: one agent reviews live, uncommitted
code in a fresh Daytona sandbox, a second agent fixes that same file after a
hash-gated handoff, and the laptop independently verifies the final bytes.

```bash
curl -fsSL https://gist.githubusercontent.com/khaliqgant/e9ee531e63a9048f612e12979f50d2ae/raw/live-review.sh -o /tmp/live-review.sh
bash /tmp/live-review.sh setup
bash /tmp/live-review.sh validate
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
