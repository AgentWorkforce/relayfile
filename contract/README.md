# Relayfile Path Contract — v1

**Status:** authoritative
**Version:** 1
**Owner:** this repository (`agentworkforce/relayfile`)
**Parent spec:** `cloud/specs/relayfile-path-contract-stabilization.md`
**Prior art:** cloud#989 (`specs/relayfile-contract-tests.md`, HTTP-level contract suite)

> **Ruling (operator, 2026-07-18):** the OSS implementation drives the contract,
> to protect self-hosters. Where hosted and OSS disagree, OSS is correct and
> hosted conforms — even when that means hosted gets stricter and something
> breaks.

This document defines, in one place, what a relayfile path means: syntax,
normalization, containment, scope-glob matching, and mount-root derivation.
The fixtures under `contract/fixtures/` are the executable form of this
document. Every implementation of the relayfile path vocabulary — the Go
server in this repo, the hosted Cloudflare Worker
(`relayfile-cloud/packages/relayfile`), the relayauth scope minter, and the
path producers in `cloud` — must run the fixtures in CI and be green.

The grammar below is **exactly what `internal/httpapi/auth.go` already
enforces**. Phase 1 documents and fences existing behavior; it does not change
any verdict the Go server returns today.

---

## 1. Definitions

- **File path** — the absolute, `/`-rooted identifier of an object in a
  relayfile workspace, e.g. `/github/repos/acme/cloud/issues/5.json`.
- **Scope** — a colon-delimited grant carried in a token's `scopes` claim:
  `plane:resource:action[:path]`, e.g. `relayfile:fs:read:/github/**`.
- **Scope path** — the optional fourth segment of a scope: either the literal
  `*` or a file path that may carry **one suffix glob**.
- **Canonical file path** — a file path in the canonical form of §2. Matching
  (§5) is defined **only** for canonical file paths; behavior on
  non-canonical input is unspecified and MUST NOT be relied upon.

## 2. Canonical file path grammar

```
canonical-path = "/" | "/" segment *( "/" segment )
segment        = 1*<any byte except "/">   ; excluding "." and ".."
```

A canonical file path:

1. begins with `/`;
2. has no trailing slash (except the root path `/` itself);
3. has no empty segments (no `//`);
4. has no `.` or `..` segments;
5. contains no `*` in any segment (globs belong to scope paths only);
6. is compared **byte-exact and case-sensitive**. No Unicode normalization,
   no percent-decoding at this layer (transport decoding happens before the
   contract applies).

## 3. Normalization

The reference normalizer is `normalizePath` in `internal/relayfile/store.go`:

1. empty string → `/`;
2. prepend `/` if missing;
3. trim **one** trailing `/` (unless the result would be empty).

That is the complete algorithm. It is deliberately minimal and is pinned
byte-for-byte by `fixtures/path-normalization.json`, including these
**flagged quirks** (pinned as current behavior, candidates for a v2 change —
which would be a versioned contract change, red across all repos on the same
day):

| input | output | note |
|---|---|---|
| `/a//` | `/a/` | only one trailing slash is trimmed |
| `/a/../b` | `/a/../b` | dot segments are **not** collapsed; `.`/`..` are treated as literal segment names |
| `/a/./b` | `/a/./b` | same |

Because dot segments survive normalization, they can never alias another
path: `/a/../b` and `/b` are two different keys in the virtual store. There
is no filesystem underneath this namespace for `..` to escape into.

### 3.1 Known in-repo divergences (flagged, not contract)

These are inputs to the Phase 2 divergence map, recorded here so nobody
mistakes them for the contract:

- `internal/mountfuse/fs.go` `normalizeRemotePath` applies `path.Clean`
  (collapses `//`, `.`, `..`) — stricter than the store normalizer.
- `internal/httpapi/server.go`: the `read_file`/`write_file`/`delete_file`
  routes auth-match the **raw trimmed** client path, while `tree`/`export`
  normalize via `normalizeRoutePath` first. A request for `path=github/x`
  (no leading slash) is therefore matched against scopes without the leading
  slash on file routes.
- `internal/httpapi/acl.go` `normalizeACLPath` collapses `.`/`..`
  (ACL-marker resolution only).
- `internal/httpapi/websocket.go` `webSocketPathMatches` implements a
  **different, segment-wise glob grammar** (mid-path `*` allowed) for
  WebSocket event *filters*. Event filters are a subscription convenience,
  not an authorization surface, and are **out of scope** for this contract
  (§9). They must never be conflated with scope paths.

## 4. Scope grammar

```
scope       = plane ":" resource ":" action [ ":" scope-path ]
plane       = "relayfile" | "workspace" | "*" | <other planes, non-matching>
resource    = token | "*"          ; e.g. "fs"
action      = "read" | "write" | "manage" | "*" | <others>
scope-path  = "*" | glob-path
```

Splitting is `SplitN(scope, ":", 4)`: the scope path may itself contain
colons (it never does in canonical form, but the split guarantees the path is
not truncated). Scopes with fewer than three segments are **not** grants in
this grammar; they participate only as opaque literals (§5.3).

Plane semantics:

- `relayfile` and `*` — segment 2 is the resource, segment 3 the action.
- `workspace` — segment 2 is a **sponsor/workspace label, not a resource**;
  the grant applies to the `fs` resource only. `workspace:<label>:read` and
  `workspace:<label>:read:<scope-path>` are fs-grants.
- Any other plane (e.g. `relaycast`) never matches relayfile requirements.

Action semantics: `*` matches any action; `manage` implies `read` and
`write` (but nothing implies `manage`).

A 3-segment grant (`relayfile:fs:read`, `workspace:x:read`) is equivalent to
a 4-segment grant with scope path `*`: full-namespace access.

## 5. Scope-path glob grammar and matching

### 5.1 Valid scope paths

A scope path is either the literal `*`, or a path that satisfies the
canonical-path rules of §2 **except** that its **final segment** may take one
of three glob forms:

| form | example | matches file `f` when |
|---|---|---|
| *(exact — no glob)* | `/a/b/c.json` | `f == "/a/b/c.json"` |
| `<dir>/**` | `/github/**` | `f == "/github"` **or** `f` starts with `/github/` |
| `<dir>/*` | `/github/*` | `f` starts with `/github/` — **any depth**, and **not** `/github` itself |
| `<name>*` | `/github/acme-*` | `f` starts with the raw byte prefix `/github/acme-` |
| `*` | `*` | any `f`, including the empty required-path |

**The `/` vs `*` asymmetry.** Both `/` and `*` are valid scope paths with
`mounts_at: /`, but their authorization behavior is opposite: `*` matches
every file path (including the empty required-path), while `/` is an exact
path like any other — it matches **only the root node itself**, nothing
beneath it. An author who writes `/` intending "the whole tree" gets a
valid-looking scope that authorizes almost nothing. Whole-namespace access
is spelled `*` (or `/**`). The fixtures pin both sides
(`root-scope-path-*` cases in `auth-matching.json`).

Points that pin known cross-implementation disagreements:

- **`/*` is not single-segment.** `/github/*` matches
  `/github/a/b/c.json`. Implementations that treat `*` as "one path
  segment" are wrong under this contract.
- **`/**` includes the directory node itself.** `/github/**` matches
  `/github`; `/github/*` does not.
- **`<name>*` is a raw byte prefix**, not a segment prefix: `/github*`
  matches `/github`, `/github2`, and `/github/x`.
- Matching order is: exact equality, then `/**`, then `/*`, then trailing
  `*`. (For valid scope paths and canonical file paths the order is not
  observable; it is recorded for implementors mirroring
  `scopePathMatches`.)

### 5.2 Invalid scope paths — mid-path globs

**A `*` anywhere other than the final segment is invalid. `**` anywhere
other than as the entire final segment is invalid.** Also invalid: missing
leading `/`, empty segments, `.`/`..` segments, trailing slash, and a `*`
that is not at the end of the final segment (`/git*hub`, `/a/re*po`,
`/a/b**`).

So `relayfile:fs:read:/github/repos/*/*/issues/**` — the exact scope hosted
cloud emits today — is **invalid**. Under the reference matcher its `*`
segments are literal bytes, so it matches no canonical file path: **a token
carrying only that scope authorizes nothing**.

Required behavior for every implementation:

1. A validity check, where implemented, MUST report these scope paths
   invalid (`valid: false` in the fixtures).
2. The matcher MUST NOT match them against any canonical file path. The
   fixtures probe this directly (`probe_files` with `auth_matches: false`) —
   an implementation that *accepts* a mid-path glob **fails the suite**.
3. **No silent drops.** A producer or client that encounters an invalid
   scope path must surface an explicit error or an explicitly reported
   degradation — never a vanished array element (cloud#2702).

### 5.3 Grant-set matching (`scopeMatchesPath`)

Given a granted scope set, a requirement `resource:action`, and a required
file path `f` (empty means "no specific path — namespace-level access"):

1. Collect every granted scope that is a path grant for the requirement per
   §4 (plane, resource, action all match).
2. If any such grant has scope path `*` (or is a 3-segment grant) → **allow**
   (even when `f` is empty).
3. If any narrow grant's scope path matches `f` per §5.1 → **allow**. An
   empty `f` never matches a narrow grant.
4. Otherwise, if the **literal** requirement string (e.g. `fs:read`) is in
   the granted set: allow **only if no narrow path grant existed** in step 1.
   **Narrow grants suppress the bare literal** — a token with
   `fs:read` + `relayfile:fs:read:/github/**` is confined to `/github/**`.
5. Otherwise → **deny**.

Scope paths are whitespace-trimmed when extracted from the scope string; the
required file path is whitespace-trimmed before matching.

## 6. Containment

`contains(base, candidate)` — reference: `withinBase` in
`internal/relayfile/store.go`. Both inputs pass through the §3 normalizer,
then:

- base `/` contains everything;
- otherwise `candidate == base`, or `candidate` starts with `base + "/"`.

Byte-exact: `/github` does not contain `/github2`; `/gith` does not contain
`/github` — segment boundaries are enforced by the appended `/`.

## 7. Mount-root derivation (`mounts_at`)

The deepest concrete directory implied by a **valid** scope path — where a
mount client may anchor the grant locally:

| scope path | mounts_at |
|---|---|
| `*` | `/` |
| `/` | `/` |
| exact `/a/b/c.json` | `/a/b/c.json` |
| `/a/b/**` | `/a/b` |
| `/a/b/*` | `/a/b` |
| `/a/acme-*` | `/a` (parent of the prefixed segment) |
| `/**`, `/*` | `/` |

An invalid scope path has **no** mount root (`mounts_at: null`). Mount
layers MUST NOT guess one (cloud#2297, cloud#2702: the silent guess is how
paths vanished).

Reference implementation: `scopePathMountRoot` in
`internal/httpapi/path_contract.go`.

## 8. Fixtures

Language-neutral JSON under `contract/fixtures/`. Every file:

```json
{ "contract": "relayfile-path-contract", "version": 1, "area": "<area>", "cases": [ ... ] }
```

| file | area | case shape → expectations |
|---|---|---|
| `path-normalization.json` | `path-normalization` | `{path}` → `{normalized}` |
| `containment.json` | `containment` | `{base, candidate}` → `{contains}` |
| `scope-paths.json` | `scope-paths` | `{scope_path, probe_files?}` → `{valid, mounts_at}`; every `probe_files` entry must **not** match when `valid` is false |
| `auth-matching.json` | `auth-matching` | `{scopes, required, file}` → `{auth_matches}` |

Case fields: `name` (unique per file), optional `description`, optional
`quirk: true` (behavior pinned as-is, flagged for a future versioned change).
Implementations without a scope-path validity function may skip `valid`
assertions but MUST still run the `probe_files` and `auth_matches`
assertions — that is the fence that makes accepting a mid-path glob a CI
failure.

### Runners

- **Go (this repo):** `make contract-test` — fixture-driven tests in
  `internal/httpapi/contract_fixtures_test.go` (scope paths, auth matching)
  and `internal/relayfile/contract_fixtures_test.go` (normalization,
  containment). Also runs under plain `go test ./...`, so the existing
  `ci.yml` / `contract.yml` workflows already gate it.
- **TS (Phase 2):** `relayfile-cloud`, `relayauth`, `relayfile-adapters`,
  and `cloud` consume these files (vendored copy or package) and report.
  Phase 2 is report-only; expected red: `relayfile-cloud` (segment-glob
  matcher is too permissive), `cloud` (emits invalid scopes).

### Stricter subsets

An implementation (typically a path **producer** or canonicalizer, e.g. a
mount-path layer that accepts terminal `/**` only and not `/*` or `name*`)
MAY accept a strict subset of this grammar — a scope path it rejects can
never be one it wrongly honors. But a subset must be a **declared choice**:
documented in that implementation, with a rationale, and its rejections must
be explicit errors or reported degradations per §5.2. An undocumented subset
is exactly the two-vocabularies drift that produced cloud#2297. Consumers
and enforcers (anything answering `auth_matches`) get no such latitude: they
must implement the full grammar and pass every fixture.

### Changing the contract

A contract change is a **versioned event**: bump `version`, change the
fixtures and the reference implementation in the same PR, and expect every
downstream repo to go red the same day (that is the acceptance test from the
parent spec, not an accident). Never change a fixture to make one
implementation pass.

## 9. Out of scope for v1

- **WebSocket event filters** (`webSocketPathMatches`): a separate,
  segment-wise glob grammar on a non-authorization surface.
- **ACL paths** (`normalizeACLPath`): ACL-marker directory resolution.
- **Local-mount ignore/readonly patterns**: gitignore semantics by design
  (see Appendix A).
- HTTP request/response shapes — that is cloud#989's suite; this contract is
  the path-vocabulary layer underneath it.

---

## Appendix A — audit of `packages/local-mount/src/mount.ts` (the fifth theory)

Audited 2026-07-18 for the parent spec. `mount.ts` operates on **local,
relative, workstation paths** (project ↔ mount sync), never on remote
relayfile paths or scope paths — so it does not implement *this* contract,
but it does hold three private path theories of its own:

1. **gitignore glob semantics** via the `ignore` npm package for
   `ignoredPatterns`, `readonlyPatterns`, and the no-sync-back matcher
   (`createPathMatcher`, `isPathMatched`). Full gitignore grammar: mid-path
   `*`/`**`, negations, anchoring, trailing-slash directory forms. This is
   intentional (the inputs are user-authored ignore rules) but it means the
   same spelling — `dir/*` — has **different meanings** in a mount ignore
   pattern (one segment, gitignore) and in a scope path (any-depth suffix,
   §5.1). Documentation that surfaces both must never present them as one
   vocabulary.
2. **`ExcludeRules`** (`createExcludeRules`/`addExcludeEntries`): a third
   mini-grammar for `excludeDirs` — a bare name matches at any depth, an
   entry containing `/` is a root-anchored prefix (`legacy` mode). No globs
   at all.
3. **`normalizeRelativePosix`**: separator swap only (`\` → `/`); no
   dot-segment handling, no case folding. Containment is enforced
   separately by `resolveSyncBackSource` → `isPathWithinRoot` via
   `path.resolve` — a fourth containment check distinct from §6.

Risk assessment: none of these touch scope paths today, so they cannot cause
an authorization divergence. The exposure is **vocabulary drift** — a future
change that forwards a mount pattern into a scope path (or vice versa) would
silently change meaning. Verdict: leave the grammars as they are (they serve
different inputs), keep them out of the contract, and treat any future
crossing of the boundary as a contract-change event requiring fixtures.
