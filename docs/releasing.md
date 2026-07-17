# Releasing relayfile

Operational runbook for cutting a release. For the pipeline's design intent, see
[`ci-cd-design.md`](./ci-cd-design.md).

## Cutting a release

`publish.yml` (`workflow_dispatch`) is the only supported way to publish. One run
version-bumps every package, publishes them, commits `chore(release): vX.Y.Z`,
and creates the GitHub release.

```bash
gh workflow run publish.yml --ref main \
  -f package=all -f version=patch -f tag=latest -f dry_run=false
```

| Input | Notes |
| --- | --- |
| `package` | Must be `all` for a real publish. The workflow hard-fails otherwise: it rewrites every manifest, and lockfile regeneration needs every `@relayfile/mount-*` package already published at the new version so npm can resolve tarball integrity metadata. |
| `version` | `patch` / `minor` / `major`, or `prerelease` with `preid=rc` for a release candidate. |
| `tag` | `latest` for stable, `next` for prereleases. |

The bump is computed from the root `package.json` **on the ref you dispatch
from**, so check it before dispatching. Note `patch` on a prerelease drops the
prerelease rather than bumping the patch digit: `0.10.27-rc.0` → `0.10.27`.

## Promoting an RC, and the `next` tag

**Promoting an RC to stable strands the `next` tag.** `next` keeps pointing at
the RC you just superseded, so `@next` resolves *older* than `@latest`:

```
latest: 0.10.27
next:   0.10.27-rc.0   # older than latest — anyone on @next is behind
```

`npm dist-tag add relayfile@0.10.27 next` would fix this in one command, but it
**cannot run in this repo's CI**. `publish.yml` authenticates with npm OIDC
trusted publishing, and npm's OIDC covers only `npm publish` / `npm stage
publish` — every other command, `dist-tag` included, still needs a traditional
token ([npm/cli#8547](https://github.com/npm/cli/issues/8547)). This repo holds
no npm token secret by design, so there is nothing for `dist-tag` to
authenticate with.

The fix is a second dispatch that publishes a new version *forward* onto `next`
— `npm publish` is the one command OIDC does cover:

```bash
gh workflow run publish.yml --ref main \
  -f package=all -f version=patch -f tag=next -f dry_run=false
```

```
latest: 0.10.27   # stable, unchanged
next:   0.10.28   # newer than latest
```

Run this after **every** RC → stable promotion, or `next` stays stranded until
the next RC.

The trade-off: this publishes a version whose content is identical to the stable
release, so version numbers advance faster than shipped changes. That is the
cost of not holding a long-lived npm token. The alternative is a granular npm
token scoped to `relayfile` + `@relayfile/*`, which would allow a single
`dist-tag` step instead — a deliberate security trade, not an oversight.

## After the release

Verify against the registry rather than the workflow log — a green run does not
prove the tags are right:

```bash
npm view relayfile dist-tags
npm view @relayfile/client dist-tags
```

The `next` dispatch creates its own GitHub release and GitHub marks the newest
tag "Latest", even though npm's `latest` is the stable version. Realign so the
releases page matches the registry:

```bash
gh release edit v0.10.27 --latest   # the stable version, not the next-tag one
```

## Consumers

Downstream `^x.y.z` ranges resolve by semver across **all** published versions —
dist-tags do not gate them. A version published only to `next` will still
satisfy a caret range in a consumer's manifest on any lockfile refresh. Committed
lockfiles pin exact versions, so `npm ci` is unaffected.
