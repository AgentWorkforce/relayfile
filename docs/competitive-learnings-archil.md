# Competitive Learnings: Archil

**Date:** 2026-03-25
**Source:** [Archil Documentation](https://docs.archil.com)
**Status:** Proposal

## Context

Archil is a cloud filesystem designed for AI workloads — a POSIX-compatible
caching layer over S3. While they operate in a different segment (cloud storage
infrastructure vs. agent collaboration), their go-to-market execution surfaces
several learnings we should apply to RelayFile.

## Positioning Overlap

Both products sit in the "AI agents need files" space, but at different layers:

- **Archil** = fast disk (throughput, POSIX compliance, S3 sync)
- **RelayFile** = shared workspace (revision control, conflict detection, multi-system integration)

There is no direct feature competition. The risk is narrative competition — if
teams associate "filesystem for AI" with Archil's infrastructure framing, our
collaboration story becomes harder to land.

## Actionable Proposals

### 1. Sharpen first-run experience to two commands

**What they do:** `curl | sh` then `archil mount`. Two commands, working
filesystem.

**What we should do:**
- Audit the current `relayfile mount` onboarding path end-to-end
- Ensure a new user can go from zero to a syncing workspace in under 60 seconds
- Ship a `curl https://relayfile.dev/install | sh` installer if we don't have one
- The mount binary is already a single Go binary — verify no hidden dependencies
  or config steps slow down first use

**Effort:** Small. Mostly packaging and docs work.

### 2. Lead messaging with outcomes, not mechanisms

**What they do:** "Infinite, shareable disks" — no mention of caching protocols
or sync algorithms on the landing page. Every heading is a user outcome.

**What we should do:**
- Reframe top-level messaging around outcomes:
  - "Edit on your laptop, your agent sees it in 2 seconds"
  - "No git push. No git pull. Just files."
  - "Conflicts detected, never silently overwritten"
- Push mechanism details (queues, webhooks, writeback) to architecture docs
- Adopt a single positioning line and use it everywhere. Proposal:
  **"The workspace where agents and humans share files in real-time."**

**Effort:** Medium. Requires docs rewrite and alignment on positioning.

### 3. Publish performance characteristics

**What they do:** Concrete numbers in docs — sub-ms cached reads, 10 Gbps
throughput, 10K IOPS, 99.999% durability.

**What we should do:**
- Benchmark and document:
  - Webhook-to-mount propagation latency (p50, p95, p99)
  - Mount polling interval and sync latency
  - Maximum file size and workspace file count
  - Throughput for bulk operations
  - Webhook ingestion throughput (events/sec)
- Publish these in a dedicated "Performance" doc page
- Even modest numbers build trust when stated transparently

**Effort:** Medium. Requires benchmarking infrastructure and test harness.

### 4. Build a Terraform provider

**What they do:** Terraform provider for disk lifecycle management. Signals
"production-ready for platform teams."

**What we should do:**
- Build a Terraform provider for workspace CRUD:
  - `relayfile_workspace` resource (create, configure, destroy)
  - `relayfile_token` resource (JWT provisioning for agents)
  - `relayfile_acl` resource (permission rules)
- This unlocks platform teams who provision per-agent or per-tenant workspaces
  as part of their infra-as-code workflows

**Effort:** Large. New codebase, but Terraform provider SDK is well-documented.

### 5. Design a simple, transparent pricing model

**What they do:** $0.20/GiB-month, billed per-minute. One number, easy to
compare against the incumbent (EBS).

**What we should do:**
- When we design pricing, anchor on a single comparable metric
- Candidates: per-workspace-month, per-active-agent-month, or per-GiB stored
- Include a "compare to the alternative" frame (e.g., "vs. managing git sync
  scripts across N agents")
- Avoid multi-axis pricing (per-seat + per-GB + per-API-call) — it creates
  purchase friction

**Effort:** N/A until pricing phase. Worth documenting the principle now.

## What We Should NOT Copy

| Archil Approach | Why It's Wrong for Us |
|---|---|
| POSIX kernel-level mount | Overkill for collaboration; our HTTP+polling is simpler, portable, no root required |
| S3 as canonical store | Locks you into one storage paradigm; our pluggable backends are more flexible |
| Pessimistic checkout/checkin for writes | Blocks collaboration; our optimistic concurrency with conflict detection is the right model |
| Cloud-only (AWS/GCP Linux only) | We run anywhere — laptop, Docker, CI — this is a strength to protect |

## Recommended Priority Order

1. **Messaging reframe** — highest leverage, lowest effort
2. **First-run experience audit** — directly impacts conversion
3. **Performance documentation** — builds credibility with evaluators
4. **Terraform provider** — unlocks platform team adoption
5. **Pricing model principles** — future phase, document now

## Success Metrics

- Time from install to first synced file < 60 seconds
- Landing page communicates value without requiring architecture knowledge
- Performance page exists with real benchmarked numbers
- Terraform provider available on registry (stretch goal, Q3)
