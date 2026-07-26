# Trajectory: Repair PR #377 review findings and finish proven merge

> **Status:** ✅ Completed
> **Confidence:** 96%
> **Started:** July 26, 2026 at 09:21 PM
> **Completed:** July 26, 2026 at 09:25 PM

---

## Summary

Audited all six unresolved review threads on PR #377 and addressed every valid finding. Replaced invalid cross-host CLOCK_REALTIME subtraction with a unique-prefix ping/ack protocol measured only by each initiator's monotonic clock; reran 12 sf-initiated and 12 finn-initiated samples through a fresh real two-machine relayfile mount (median RTT 315.526 ms and 373.230 ms). Added stale-prefix rejection, sequence-range validation, sequence-based deduplication, correct even-sample medians, corrected Region ownership, and removed the absolute developer path from committed trajectory metadata. Revalidated JSON, privacy scans, CSV statistics, helper smoke/stale tests, contract surface, and go test ./.... Stopped exact review mount/tunnel/server/auth processes and removed fresh scratch on both machines. Final merge remains gated on the CI and review rerun for this repair commit.

**Approach:** Standard approach

---

## Key Decisions

### Replace cross-host wall-clock deltas with initiator-monotonic ping/ack round trips
- **Chose:** Replace cross-host wall-clock deltas with initiator-monotonic ping/ack round trips
- **Reasoning:** One-way subtraction across sf and finn included unbounded clock skew. A relayfile round trip measured by one process's CLOCK_MONOTONIC removes that error, and a unique prefix plus sequence validation prevents stale samples.

---

## Chapters

### 1. Work
*Agent: default*

- Replace cross-host wall-clock deltas with initiator-monotonic ping/ack round trips: Replace cross-host wall-clock deltas with initiator-monotonic ping/ack round trips
- All six unresolved review threads were audited. Four behavior clusters were valid: username/path exposure, ownership wording, cross-clock latency, and stale/even-median handling. The latency evidence was rerun on a fresh physical two-machine mount with unique ping/ack probes and initiator-monotonic timing; both review scratch stacks were then torn down.
