# E2B ↔ Daytona portability evidence

This directory contains the methodology, raw samples, reproducible aggregation,
and provenance for Relayfile synchronization between isolated E2B and Daytona
sandboxes on 2026-08-21.

- [`RESULTS.md`](RESULTS.md) gives the exact public result and its limits.
- [`METHODOLOGY.md`](METHODOLOGY.md) defines clocks, workloads, and frozen gates.
- [`PROVENANCE.md`](PROVENANCE.md) identifies the sandboxes and deployed binaries.
- [`PORTABILITY.md`](PORTABILITY.md) defines what provider portability means and
  what another provider must pass before it is called certified.
- [`CLEANUP.md`](CLEANUP.md) records scoped teardown of benchmark resources.
- [`aggregate-summary.json`](aggregate-summary.json) is recomputed from the raw
  qualifying samples by [`aggregate_results.py`](aggregate_results.py).

Recompute all retained gates with:

```bash
python3 aggregate_results.py
```

The cross-provider result and the controlled core-floor result are intentionally
separate. The former includes two provider gateways and a receiver hash-probe
round trip. The latter removes those costs to measure Relayfile's protocol,
watcher, fanout, and materialization floor.
