---
name: verify-features
description: Select and execute Relayfile's authoritative feature-catalog procedures with strict tier prerequisites, critical paths, cleanup, diagnosis, and PASS/FAIL/SKIP/MANUAL accounting.
---

# Verify Relayfile features

Start with `npm run features:validate`. Select features from `.agentworkforce/features/manifest.yaml` by requested ID/category or by matching changed files against `locations`. Resolve every selected category through `verification.categories` and execute that exact heading in `.agentworkforce/features/verify/procedures.md`; do not invent a shorter happy-path check.

Run `npm run features:verify -- --tier 1` for static/isolated checks, `--tier 2` for the full local server/mount flow, and `--tier 4` for clean packed consumers. Tiers 3, 5, and 6 require the explicit opt-in and disposable prerequisites named by the procedure. `--require-live` makes missing live prerequisites a failing workflow gate.

Only `PASS` counts as passing. Missing credentials, FUSE, receiver, PostgreSQL, or provider fixtures are `SKIP`; publish/tag/release and unbounded interactive operations are `MANUAL`. Diagnose the first broken boundary using `.agentworkforce/features/critical-paths.md`, preserve redacted evidence, and follow the procedure's external-to-local cleanup order. Never claim hosted support from a local 404, provider support from a path helper, or digest correctness without both normal events and current digest artifacts.
