# Daytona five-agent collaboration evidence

This directory contains the reproducible harness, raw samples, provenance, and
results for Relayfile's five-agent Daytona acceptance run on 2026-08-21.

- [`RESULTS.md`](RESULTS.md) states the measured claim and exact latency and
  correctness results.
- [`METHODOLOGY.md`](METHODOLOGY.md) defines the frozen workload and gates.
- [`PROVENANCE.md`](PROVENANCE.md) pins repositories, binaries, harness files,
  and the isolated fleet.
- [`raw/aggregate-summary.json`](raw/aggregate-summary.json) aggregates only
  the three consecutive qualifying runs; individual samples remain under
  `raw/large-r1`, `raw/large-r2`, and `raw/large-r3`.
- [`raw/conflict-final/inspection-summary.json`](raw/conflict-final/inspection-summary.json)
  and [`raw/restart-final/inspection-summary.json`](raw/restart-final/inspection-summary.json)
  capture conflict preservation and restart durability.
- [`CLEANUP.md`](CLEANUP.md) records scoped Daytona teardown.

Recompute the aggregate from the retained samples with:

```bash
python3 aggregate_results.py --evidence-root . --output /tmp/aggregate-summary.json
```

The evidence deliberately contains failed and invalidated attempts. Only
`large-r1` through `large-r3` contribute to the acceptance result.
