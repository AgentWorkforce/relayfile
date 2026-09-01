# Relayfile launch playbook

## The claim

> Two agents review and repair live, uncommitted code in a fresh cloud sandbox
> while your laptop verifies the exact bytes they saw. No commit, branch, PR,
> upload, or copy/paste handoff.

The demo starts with an unsafe payment webhook. A reviewer in a fresh Daytona
sandbox publishes `REQUEST_CHANGES` with the source SHA-256. The laptop verifies
that hash before it authorizes a builder. The builder fixes the shared file, the
reviewer approves the final SHA-256, and the laptop independently checks the
same security properties against those exact final bytes.

Without Relayfile, this requires a commit/push/checkout loop, a shared network
filesystem, or a custom upload API plus change notifications. Relayfile turns
the coordination layer into ordinary files: every surface works against the
same named workspace while hash evidence records exactly what was reviewed.

## Founder demo

Use two terminals. Terminal A runs the proof; Terminal B attaches to the live
agents when their commands appear.

First-time setup:

```bash
curl -fsSL https://gist.githubusercontent.com/khaliqgant/e9ee531e63a9048f612e12979f50d2ae/raw/live-review.sh -o /tmp/live-review.sh
bash /tmp/live-review.sh setup
```

Run the self-cleaning demo:

```bash
bash /tmp/live-review.sh validate
```

As soon as Terminal A prints `Watch reviewer:`, paste that command into Terminal
B. The reviewer attach is view mode. When `Drive builder:` appears, use that
command to take over the builder interactively. `Ctrl+C` detaches without
killing the agent.

Stop the recording on `PASS` and show:

- the Daytona node and Relayfile mount path
- the review-time source SHA-256 and tamper gate
- the builder's final source SHA-256
- the reviewer's approval of the same final bytes
- the run-specific review, build, and approval observations
- cleanup of both agents and the exact sandbox ID

Suggested narration:

1. “This is a fresh Daytona machine, not localhost.”
2. “The reviewer is looking at live code that has never been committed.”
3. “My laptop verifies the exact bytes the reviewer saw before another agent can touch them.”
4. “The builder fixes the same file; there is no Git or upload handoff.”
5. “The reviewer and my laptop independently approve the exact final bytes.”
6. “I can attach to either running agent while this happens.”

`validate` releases both agents and deletes the exact successful sandbox after
the proof. Use `run` only when you deliberately want the agents and sandbox to
remain available after `PASS`:

```bash
bash /tmp/live-review.sh run
```

## Public self-serve path

Users do not need a repository checkout or globally installed Relayfile tools.
They need Node.js 22+, npm, curl, and a Claude provider they can connect during
setup. The script downloads pinned CLIs in an isolated npm execution:

```bash
curl -fsSL https://gist.githubusercontent.com/khaliqgant/e9ee531e63a9048f612e12979f50d2ae/raw/live-review.sh -o /tmp/live-review.sh
bash /tmp/live-review.sh setup
bash /tmp/live-review.sh validate
```

`setup` performs one Agent Relay Cloud sign-in through `relayfile login`.
`agent-relay cloud connect claude` configures the provider; it is not a second
product-account login.

The proof fails closed unless all of these happen:

1. Cloud provisions one fresh Daytona sandbox with a confirmed Relayfile mount.
2. The reviewer publishes `REQUEST_CHANGES` against the seeded source hash.
3. The laptop re-reads the source and clears the three-way hash tamper gate.
4. The builder changes the live file and reports its exact final hash.
5. The reviewer approves that same final hash after re-reading the file.
6. The laptop independently verifies syntax and all six webhook security gates.
7. `validate` releases both agents and deletes the exact sandbox by ID.

## Claims and limits

The script polls at 250 ms and prints remote-write-to-local-read observations
when the machine clocks are sane. Quote the measurements from that run; do not
turn one observation into a universal millisecond SLA.

The reviewer is role-read-only today, not permission-enforced. Say that plainly.
The public demo uses Daytona. E2B has separate reviewed integration proof, but
is not enabled in this production path today.

The repository's `npm run proof` example remains useful as a lower-level
single-agent replication proof. For launch, lead with the live review because it
shows why shared uncommitted context matters, not merely that bytes can move.
