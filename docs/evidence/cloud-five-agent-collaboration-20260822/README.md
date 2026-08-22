# Relayfile Cloud five-agent evidence

This directory contains the reproducible harness and retained raw evidence for
the 2026-08-22 Relayfile Cloud qualification:

- five independent Daytona sandboxes against a 7,195-file, 142,713,477-byte
  corpus; and
- a provider-neutral fleet of two E2B and three Daytona sandboxes against a
  fresh isolated Relayfile Cloud prefix; and
- three complete five-Daytona runs plus a collision test on the final deployed
  Cloud path with native rate limiting and asynchronous archive recovery.

Start with [RESULTS.md](RESULTS.md), then read [METHODOLOGY.md](METHODOLOGY.md)
and [PROVENANCE.md](PROVENANCE.md). The `qualified/` directories retain every
writer observation, peer hash sample, and conflict inspection used by the
reported aggregates.

The honest public headline is **hash-correct cross-provider filesystem
convergence in hundreds of milliseconds**. The evidence does not support a
3–9 ms end-to-end propagation claim.
