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

Run this self-contained command in Terminal A:

```bash
LIVE_REVIEW_REV=ed2a9ae1e96fbe474861c33b95cd7bcae1ab640c
LIVE_REVIEW_SHA256=398214e8c0208b968018f6ddf85f8f1fb8d1582c35e05ba24140cfeaad14d42e
LIVE_REVIEW_SCRIPT="$(mktemp "${TMPDIR:-/tmp}/relayfile-live-review.XXXXXX")" &&
curl -fsSL "https://gist.githubusercontent.com/khaliqgant/e9ee531e63a9048f612e12979f50d2ae/raw/$LIVE_REVIEW_REV/live-review.sh" -o "$LIVE_REVIEW_SCRIPT" &&
node -e 'const fs=require("fs"),c=require("crypto"),[p,w]=process.argv.slice(1),g=c.createHash("sha256").update(fs.readFileSync(p)).digest("hex");if(g!==w)throw Error("SHA-256 mismatch");console.log("SHA-256 verified")' "$LIVE_REVIEW_SCRIPT" "$LIVE_REVIEW_SHA256" &&
bash "$LIVE_REVIEW_SCRIPT" setup &&
bash "$LIVE_REVIEW_SCRIPT" validate
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
the proof. To deliberately leave the agents and sandbox available after
`PASS`, replace the final `validate` in the self-contained command with `run`.

## Public self-serve path

Users do not need a repository checkout or globally installed Relayfile tools.
They need a Bash environment with `mktemp`, Node.js 22.22.0+, npm, curl, and a
Claude provider they can connect during setup. The script downloads pinned CLIs
in an isolated npm execution:

That immutable script pins agent-relay 11.10.0 and Relayfile/SDK 0.10.51, the
exact combination validated by the revision. The separate checked-in
lower-level proof tracks Relayfile 0.10.52.

```bash
LIVE_REVIEW_REV=ed2a9ae1e96fbe474861c33b95cd7bcae1ab640c
LIVE_REVIEW_SHA256=398214e8c0208b968018f6ddf85f8f1fb8d1582c35e05ba24140cfeaad14d42e
LIVE_REVIEW_SCRIPT="$(mktemp "${TMPDIR:-/tmp}/relayfile-live-review.XXXXXX")" &&
curl -fsSL "https://gist.githubusercontent.com/khaliqgant/e9ee531e63a9048f612e12979f50d2ae/raw/$LIVE_REVIEW_REV/live-review.sh" -o "$LIVE_REVIEW_SCRIPT" &&
node -e 'const fs=require("fs"),c=require("crypto"),[p,w]=process.argv.slice(1),g=c.createHash("sha256").update(fs.readFileSync(p)).digest("hex");if(g!==w)throw Error("SHA-256 mismatch");console.log("SHA-256 verified")' "$LIVE_REVIEW_SCRIPT" "$LIVE_REVIEW_SHA256" &&
bash "$LIVE_REVIEW_SCRIPT" setup &&
bash "$LIVE_REVIEW_SCRIPT" validate
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
