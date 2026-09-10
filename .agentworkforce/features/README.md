# Relayfile feature catalog

`manifest.yaml` is the authoritative implementation-derived catalog. Its checked summary is **18 categories and 122 features**: 82 critical, 35 hot, and 5 standard; tier counts are 55/27/1/9/19/11 for tiers 1–6.

Run:

```bash
npm run features:validate
npm run features:test
npm run features:verify -- --tier 1
```

The validator recomputes totals, checks unique stable IDs and real repository paths, enforces a one-to-one category/procedure route, verifies procedure completeness, and maps every machine-enumerated public surface to exactly one feature. The independent inventory includes canonical CLI leaves and aliases, both OpenAPI operation/schema sets, hosted SDK paths, SDK methods and parity exports, Python `__all__`, package export/bin maps, mount flags, environment/config fields, provider paths, lifecycle/safety areas, observability, and release automation.

Tier meanings:

| Tier | Environment | Automation rule |
|---|---|---|
| 1 | Static and isolated local | Required in deterministic CI |
| 2 | Local server/mount or optional local backend | Required when the host supports the named local prerequisite; otherwise explicit SKIP |
| 3 | Privileged FUSE host | Explicit opt-in; unsupported is SKIP |
| 4 | Clean packed consumer | Deterministic release gate; never publishes |
| 5 | Disposable hosted workspace/service | Explicit opt-in and credentials |
| 6 | Interactive provider or destructive release action | MANUAL unless a bounded disposable harness exists |

Use `critical-paths.md` to select product flows and `verify/procedures.md` for exact setup, commands, positive/negative assertions, cleanup, limits, and result semantics. Do not add a prose list of feature IDs; update the manifest and machine surface inventory together.

The hourly guardian under `.agentworkforce/agents/relayfile-feature-guardian/` reads only the cloned catalog paths. Slack is optional, input-gated, and write-only. Its exact revisioned state fails closed on malformed data, ambiguous manifest shrink, HTTP/auth/CAS ambiguity, and receiptless delivery.
