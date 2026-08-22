# Relayfile Viral Positioning & Launch Pack

**Date:** 2026-08-21  
**Audience funnel:** meme → coding-agent builders → sandbox/platform teams  
**Status:** Draft for launch use

---

## Bottom line

Do **not** compete for Blaxel’s headline.

Blaxel already owns:

> One filesystem. Every sandbox. Every agent.

That frame is a shared **remote** filesystem mounted into sandboxes.

Relayfile’s viral frame must invert theirs:

```text
Blaxel:     many sandboxes mount one remote drive
Relayfile:  one workspace lives inside every sandbox’s local disk
```

Launch a rule, not a storage feature:

> **Sandboxes should be disposable. Workspaces shouldn’t.**

---

## 1. Positioning hierarchy

### Category name (own this)

Primary:

- **Portable live state for agents**

Acceptable variants:

- **Local-first multiplayer for sandboxed agents**
- **The state layer for sandbox routing**

Avoid leading with:

- distributed filesystem
- agent drive
- shared volume
- “fastest” / millisecond races

### Primary one-liner

> **Sandboxes should be disposable. Workspaces shouldn’t.**

### Supporting lines

| Slot | Line |
|---|---|
| Contrast | Blaxel built a drive for their sandboxes. We make the workspace independent of the sandbox. |
| Builder | One live workspace across every sandbox. |
| Infra | The state layer for sandbox routing. |
| Spicy | Stop mounting the same remote disk into every agent. Give each agent a real filesystem and sync the workspace. |
| Memeable triad | Blaxel: one filesystem mounted by every agent. Amulet: one filesystem forked for every agent. Relayfile: one workspace replicated into every agent’s local filesystem. |

### Three-beat narrative

Use this shape in every post, landing page, and demo intro:

1. **Old world:** sandbox = compute + state → kill the sandbox, lose the work; switch providers, start over; multi-agent = S3 zip / git push / hope.
2. **False solution:** one shared network filesystem → great for handoffs, painful for `git status`, `rg`, `tsc`, and provider choice.
3. **New primitive:** local disk everywhere + realtime replication + conflict preservation → compute becomes interchangeable.

### Architecture contrast (always show)

**Blaxel / shared drive**

```text
Sandbox A ─┐
Sandbox B ─┼──── shared distributed filesystem
Sandbox C ─┘
```

**Relayfile / replicated workspace**

```text
Sandbox A             local filesystem
   │                         ↕
Sandbox B             local filesystem
   │                         ↕
Sandbox C             local filesystem

        ↕ replicated workspace ↕
              Relayfile
```

### Why local-first matters

Coding agents are read-heavy:

```bash
git status
git diff
rg "foo"
find src
tsc --noEmit
eslint .
npm test
```

With Relayfile:

```text
READ / STAT / READDIR / grep / compiler  → local filesystem
WRITE                                    → local filesystem + async replication
```

Hypothesis to keep repeating:

> Relayfile can be slower at propagating a single changed file and still make the full coding-agent workload faster.

---

## 2. Competitive claim map

| | Blaxel | Amulet | Mesa / Archil | **Relayfile** |
|---|---|---|---|---|
| Metaphor | Shared drive | Forked disks | Versioned / elastic disks | **Replicated workspace** |
| Hot path | Network / FUSE | CoW mount | Mount / disk API | **Local filesystem** |
| Multi-writer | Concurrent share | Isolate then publish | Branches / history | **Realtime + conflict artifacts** |
| Provider story | Inside Blaxel | Storage product | Storage product | **Across providers** |
| Killer feature | ~2 ms handoff | Instant fork | Branch / rollback | **Live migration / failover** |

### What Blaxel already won

- Public “shared filesystem for agents” category
- Credible realtime shared-mount story
- Published ~2 ms p50 small-file handoff

### What Relayfile can still own

- Local-first replicas across **heterogeneous** sandboxes
- Provider-independent workspace state
- Explicit conflict preservation
- Durable realtime replication protocol (cursors, catch-up, overflow recovery)
- Future: live migration / sandbox routing state layer

### Messaging to ban

- “The fastest agent filesystem”
- “Like NFS but for agents”
- “Shared volume 2.0”
- Leading with 151 ms p50 (invites Blaxel’s 2 ms clapback)
- Feature laundry lists before the category sentence
- Spite-first competitor dunks

---

## 3. Sequenced go-to-market funnel

### Phase 1 — Meme (AI-Twitter / general builders)

**Goal:** category recognition.

**Hero asset:** 30–45s screen recording. No product tour.

**Caption pattern:**

> Sandboxes die. Workspaces shouldn’t.  
> Real local filesystems. Realtime sync. Any provider.

**Do not** put p50 latency in the first tweet. Put the kill-and-migrate moment.

### Phase 2 — Builder proof (Claude Code / Codex / harness authors)

**Goal:** “this unblocks my multi-agent coding fleet.”

Ship / link evidence:

- 780/780 saves converged
- 2,580/2,580 expected content hashes converged
- conflict preserved exactly once as an artifact
- final mirrors byte-identical
- small saves: 151 ms p50 / 193 ms p95 / 244 ms p99
- repo-sized saves: 474 ms p50 / 764 ms p95 / 952 ms p99

Builder claim:

> Your agents keep local filesystem performance. Collaboration is replication, not FUSE.

Offer: one-command “two harnesses, two providers, one workspace” demo.

### Phase 3 — Platform narrative (E2B / Daytona / Modal / routers)

**Goal:** “OpenRouter for sandboxes needs a state layer.”

Claim:

> Compute routing without portable state is just expensive juggling.

Diagram:

```text
Relayfile = durable workspace
Sandbox Router = disposable compute (E2B | Daytona | Modal | Blaxel)
```

This makes Relayfile complementary to sandbox vendors, not their storage feature.

---

## 4. Homepage hero copy

### Option A — Primary (recommended)

**Eyebrow:** Portable live state for agents

**H1:** Sandboxes should be disposable. Workspaces shouldn’t.

**Subhead:** Relayfile replicates a live workspace into every agent’s local filesystem — across E2B, Daytona, Modal, and beyond — so compute stays interchangeable and work survives.

**Primary CTA:** Watch the cross-provider demo  
**Secondary CTA:** Read the evidence

**Proof strip:**

- Local disk in every sandbox
- Sub-second realtime sync
- Conflict artifacts preserved
- Provider-independent workspaces

### Option B — Builder-forward

**Eyebrow:** Local-first multiplayer for sandboxed agents

**H1:** One live workspace across every sandbox.

**Subhead:** Claude Code on E2B. Codex on Daytona. Same files. Real local disks. Realtime replication. No shared FUSE tax on `git status`, `rg`, or `tsc`.

**Primary CTA:** Run the two-agent demo  
**Secondary CTA:** See latency evidence

### Option C — Infra / platform-forward

**Eyebrow:** The state layer for sandbox routing

**H1:** Decouple agent state from sandbox compute.

**Subhead:** Relayfile makes the workspace the durable primitive and the sandbox the disposable one — so you can route, resize, fail over, and migrate agents without rebuilding context.

**Primary CTA:** Talk to us about routing  
**Secondary CTA:** Watch live migration sketch

### Homepage section outline

1. **Hero** — Option A copy + 45s autoplay/muted demo
2. **The broken default** — sandbox = compute + state
3. **The false fix** — one remote drive for every agent
4. **The Relayfile primitive** — local replicas + replication fabric
5. **Killer demo panels**
   - Cross-provider visibility
   - Conflict preservation
   - Kill sandbox / keep workspace
6. **Honest benchmarks**
   - Cross-agent visibility (sub-second)
   - Local coding workload (why local-first wins)
7. **Competitive triad** — mounted / forked / replicated
8. **Evidence** — link to methodology + results
9. **CTA** — waitlist / early access / self-host

### Homepage microcopy blocks

**Broken default**

> Today a sandbox usually combines compute and state. Kill the machine and the working directory dies with it. Switch providers and you start over. Run two agents and you invent a sync ritual.

**False fix**

> A shared network filesystem can make files visible quickly. It also puts `git status`, search, and compiles on the network path — and it usually locks you to one vendor’s mount.

**Relayfile fix**

> Every sandbox keeps a real local filesystem. Relayfile propagates mutations among those replicas in near realtime, preserves conflicts instead of silently clobbering them, and lets the workspace outlive any single sandbox.

**Closing line**

> Blaxel built a drive for their sandboxes. Relayfile makes the workspace independent of the sandbox.

---

## 5. Eight-tweet launch thread

### Tweet 1 — Hook + video

Sandboxes should be disposable. Workspaces shouldn’t.

We ran Claude Code and Codex on different sandbox providers against one live Relayfile workspace:

- local disks in each sandbox
- realtime sync
- conflict preserved
- kill one sandbox → continue on another

[VIDEO]

### Tweet 2 — Name the category

The industry is converging on “filesystems for agents.”

Most of that means:

many sandboxes → mount the same remote drive

Useful. Incomplete.

The missing primitive is portable live state:

one workspace → replicated into every agent’s local filesystem

### Tweet 3 — Contrast triad

Simple map:

Blaxel: one filesystem mounted by every agent  
Amulet: one filesystem forked for every agent  
Relayfile: one workspace replicated into every agent’s local filesystem

Mounted. Forked. Replicated.

Those are different products.

### Tweet 4 — Why local-first

Coding agents don’t just hand off JSON.

They thrash the filesystem:

`git status`  
`rg`  
`tsc`  
`eslint`  
`npm test`

If every read crosses a network mount, collaboration latency is not the whole bill.

Relayfile keeps the hot path local and replicates writes.

### Tweet 5 — Conflict clip

Most “shared filesystem” demos hide the hard part.

Two agents edited the same path at the same time.

Relayfile did not silently last-write-wins the loser into oblivion.

The losing version was preserved as a conflict artifact. Exactly once.

[CLIP]

### Tweet 6 — Kill / migrate moment

Then we killed a sandbox on purpose.

Started another one on a different provider.  
Attached the same workspace.  
Continued.

That’s the point:

compute is disposable  
the workspace is the product

[CLIP]

### Tweet 7 — Honest numbers

We are not claiming single-digit-millisecond shared-mount magic.

What we proved on isolated Daytona sandboxes:

- 780/780 saves converged
- mirrors byte-identical
- small writes ~151 ms p50
- repo-sized writes still sub-second at p50
- conflicts preserved

Reliable realtime local-first collaboration > fake speed race.

[link to evidence]

### Tweet 8 — CTA / platform punchline

If you’re building multi-agent coding fleets, you don’t need another zip/S3 handoff ritual.

If you’re building sandbox routing, you need a state layer that survives the route.

Relayfile:

disposable compute  
durable workspace  
local-first multiplayer

Early access + evidence: [link]

---

### Alt short posts (standalone)

**A.**  
Stop wiring agents together with S3 exports.  
Give them one live workspace and keep local disks.

**B.**  
Shared drive: every agent mounts the same remote FS.  
Relayfile: every agent keeps a local FS and the workspace syncs.

**C.**  
OpenRouter for models changed inference.  
The equivalent for sandboxes needs portable state, not just cheap VMs.

**D.**  
The demo that matters isn’t “2 ms handoff.”  
It’s: two providers, two harnesses, one workspace, visible conflict, live continuation after kill.

---

## 6. 45-second demo shot list

**Title card (0.0–0.5s, optional):**  
`Disposable compute. Durable workspace.`

### Shot 1 — Setup (0.5–6s)

**Visual:** Split screen.

```text
LEFT:  Claude Code  →  E2B sandbox
RIGHT: Codex        →  Daytona sandbox
CENTER/TOP: Relayfile workspace id
```

**On-screen text:**  
`Two harnesses. Two providers. One workspace.`

**Audio/VO (optional):**  
“Same workspace. Local disks. Different sandboxes.”

### Shot 2 — Cross-agent create (6–14s)

**Action:** Claude creates or edits `src/auth.ts` (or equivalent small typed API).

**Visual:** File appears / updates on Codex side.

**On-screen text:**  
`Write on E2B → visible on Daytona`

**Beat:** Show a subtle timestamp or “arrived” indicator if available. Do **not** lead with millisecond overlays.

### Shot 3 — Reverse edit (14–20s)

**Action:** Codex edits another file (`src/payments.ts` or similar).

**Visual:** Claude sees it locally.

**On-screen text:**  
`Realtime, both directions`

### Shot 4 — Conflict (20–30s)

**Action:** Both edit the same file nearly simultaneously.

**Visual:** Winner lands; loser preserved as conflict artifact. Briefly open/cat the conflict file.

**On-screen text:**  
`Same-path conflict preserved — not silent last-write-wins`

This is the trust moment. Hold long enough to read.

### Shot 5 — Kill and continue (30–42s)

**Action:**

1. Kill / delete the E2B sandbox.
2. Start a new sandbox on another provider (Modal or second Daytona/E2B).
3. Attach the same Relayfile workspace.
4. Show the files still there; continue an edit.

**On-screen text:**  
`Kill the sandbox. Keep the workspace.`

### Shot 6 — Punchline card (42–45s)

**Full-screen text:**

```text
Sandboxes should be disposable.
Workspaces shouldn’t.

Relayfile
local-first · realtime · provider-independent
```

### Demo production notes

- Record at 1080p+, large fonts, high contrast.
- Prefer real terminals over polished UI chrome.
- No music bed louder than keystrokes.
- Cut dead air; keep cursor motion continuous.
- Export a 15s cutdown that contains only: create → appear → conflict → kill/migrate.
- Also export stills:
  1. split-screen setup
  2. conflict artifact
  3. punchline card

### Optional extended 90s cut (for landing page)

Add after shot 4:

- `time git status` / `time rg` on local replica
- one sentence: “reads stay local; writes replicate”
- quick glance at evidence summary numbers

Do **not** turn the viral cut into a benchmark lecture.

---

## 7. Killer demo checklist (pre-launch)

Must be true on camera:

1. Claude creates a type or API.
2. Codex sees it in its local filesystem quickly.
3. Codex modifies another file.
4. Claude immediately sees the change.
5. Both edit the same file.
6. Relayfile visibly preserves the conflict.
7. Kill one sandbox.
8. Start a sandbox with another provider.
9. Attach the same workspace.
10. Continue working.

If step 6 or 8–10 is weak, do not launch the meme cut yet.

---

## 8. Benchmark story (use later, not first)

### Do not lead with

```text
p50 151 ms vs Blaxel ~2 ms
```

### Do lead with

> Reliable sub-second convergence on real local replicas across isolated sandboxes.

### Next benchmark to run publicly

Large real repo (Next.js, VS Code, or Kubernetes):

Compare:

- Blaxel sandbox → Agent Drive
- Daytona/E2B sandbox → local FS + Relayfile

Measure:

```bash
time git status
time git diff
time rg "useEffect" .
time find . -type f
time npm test
time npx tsc --noEmit
```

Plus separate cross-agent write visibility.

Expected narrative table:

```text
                         Shared drive     Relayfile
cross-agent visibility   very fast        sub-second
local coding hot path    network/FUSE     local disk
provider portability     limited          yes
conflict artifacts       unclear/LWW      preserved
```

---

## 9. Live migration north-star (platform story)

Position carefully as roadmap / architecture, not as already-shipped magic unless proven.

```text
1. freeze writes
2. wait for replication barrier
3. start destination sandbox
4. apply final delta
5. reconnect harness / PTY
6. continue
```

Unlocks to mention:

- provider failover
- region migration
- sandbox resizing
- cheaper-provider routing
- idle suspension
- spot replacement
- heterogeneous agent fleets

One-liner:

> The important product is not “shared files for agents.” It is portable live state for agents.

---

## 10. Launch kit inventory

Minimum viable viral kit:

1. Hero line: *Sandboxes should be disposable. Workspaces shouldn’t.*
2. 45s cross-provider demo
3. 15s cutdown
4. One diagram: local replicas + replication fabric
5. One honest benchmark / evidence link
6. Eight-tweet thread above
7. Homepage hero Option A
8. Waitlist / early-access destination

Nice to have:

- conflict still image
- triad comparison graphic
- “mounted vs forked vs replicated” one-pager
- founder loom walking the evidence folder

---

## 11. Final positioning sentence

Use this anywhere a single sentence must carry the strategy:

> **Relayfile is the local-first, provider-independent workspace replication layer that turns disposable sandboxes into a multiplayer, migratable agent fleet.**

Or, sharper for humans:

> **Blaxel built a drive for their sandboxes. Relayfile makes the workspace independent of the sandbox.**
