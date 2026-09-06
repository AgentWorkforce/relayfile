# Untagged releases (0.10.51, 0.10.53, 0.10.54)

Three versions are live on npm with no git tag and no GitHub release. All three
were published by a `Create Release` job that died at **Regenerate release
lockfiles** — *after* the publish matrix had already made the packages public,
but *before* the step that commits the version bump and pushes the tag.

| npm version | published (registry) | source commit | workflow run | tag |
|---|---|---|---|---|
| 0.10.51 | 2026-08-27 | [`0b69080`](https://github.com/AgentWorkforce/relayfile/commit/0b69080) | [33114371583](https://github.com/AgentWorkforce/relayfile/actions/runs/33114371583) | missing |
| 0.10.52 | 2026-09-01 | [`77e44b3`](https://github.com/AgentWorkforce/relayfile/commit/77e44b3) | [33532262935](https://github.com/AgentWorkforce/relayfile/actions/runs/33532262935) | `v0.10.52` |
| 0.10.53 | 2026-09-02 | [`fdc112d`](https://github.com/AgentWorkforce/relayfile/commit/fdc112d) | [33624732208](https://github.com/AgentWorkforce/relayfile/actions/runs/33624732208) | missing |
| 0.10.54 | 2026-09-06 | [`b315e9c`](https://github.com/AgentWorkforce/relayfile/commit/b315e9c) | [34034791408](https://github.com/AgentWorkforce/relayfile/actions/runs/34034791408) | missing |

Every one of the three failed with the same error, on the same step:

    npm error code ETARGET
    npm error notarget No matching version found for @relayfile/core@<version>.

The cause and the fix are described in
`scripts/release/regenerate-release-lockfiles.mjs`.

## Why 0.10.51 and 0.10.53 are not being backfilled

**No commit can honestly carry those tags.** The version bump lives only in the
build artifact; it is committed by the step that never ran. `0b69080` and
`fdc112d` are the commits the artifacts were *built from*, but their
`package.json` says `0.10.50` and `0.10.52`. Tagging them would make
`git show v0.10.53:package.json` report `0.10.52`.

**Their release assets cannot be reproduced.** A relayfile GitHub release is not
just a marker — it hosts the `relayfile-mount` binaries and the `checksums.txt`
that downstream consumers verify against (agent-relay's
`relayfile-binary.ts verifyChecksum`, and the cloud Daytona mount daemon
download). The Actions artifacts for both runs have expired, so the binaries
that actually shipped are gone. Rebuilding them locally would produce different
bytes and therefore a `checksums.txt` that disagrees with what was published — a
release that *looks* authoritative while failing verification is worse than no
release at all.

So the record lives here instead. npm remains the source of truth for what
0.10.51 and 0.10.53 contain; this table is the source of truth for where they
came from.

## Backfilling v0.10.54

v0.10.54 *is* recoverable: run 34034791408's artifacts have not expired, so the
exact published binaries and their real checksums can still be retrieved. Its
correct tag target is the version-resync commit on `main` (the commit whose tree
actually reads `0.10.54`), so this must run **after** that commit lands:

```bash
set -euo pipefail
RUN=34034791408
gh run download "$RUN" -R AgentWorkforce/relayfile --pattern 'relayfile-mount-*' --dir mount-binaries
gh run download "$RUN" -R AgentWorkforce/relayfile --name build-output --dir build-output
# gh nests each artifact in a directory named after the artifact, which is also
# the filename — so flatten via a staging dir, or mv walks into the directory.
mkdir -p mount-staging
find mount-binaries -mindepth 2 -type f -name 'relayfile-mount-*' -exec mv -f {} mount-staging/ \;
rm -rf mount-binaries && mv mount-staging mount-binaries

# Never checksum a partial download — Create Release verifies the same four.
for SUFFIX in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
  test -f "mount-binaries/relayfile-mount-${SUFFIX}" \
    || { echo "missing relayfile-mount-${SUFFIX}" >&2; exit 1; }
done
test "$(ls build-output/packages/cli/bin/relayfile-cli-* | wc -l)" -eq 6

( cd mount-binaries && sha256sum relayfile-mount-* ) > checksums.txt
( cd build-output/packages/cli/bin && sha256sum relayfile-cli-* ) >> checksums.txt
# Confirm against the sums recorded below before pushing anything.
grep -c . checksums.txt   # expect 10

git tag -a v0.10.54 -m "Release v0.10.54" <resync-commit>
git push origin v0.10.54
gh release create v0.10.54 --title v0.10.54 --generate-notes \
  mount-binaries/relayfile-mount-* build-output/packages/cli/bin/relayfile-cli-* checksums.txt
```

The recovered binaries carry these SHA-256 sums, which is what a correct
`checksums.txt` for v0.10.54 must contain for the mount entries:

    2e8f4f8eb10947090b65b723012c0c2e4d635a04b62aa98e850ea22a9f8f96dd  relayfile-mount-darwin-amd64
    ed441eae1ebb01707bae8839cf7dc641570c2dccdc98ed276aadf0c9497d1f81  relayfile-mount-darwin-arm64
    ba37b997478f2386023e6389fa9a970fea4a0e266678b2bc553217f3a7502549  relayfile-mount-linux-amd64
    b5bb0bde152da562aad60c34a1ad38cc59583a5c4197a2de82fcfa9e0e97dcd4  relayfile-mount-linux-arm64

Artifacts expire 90 days after the run, so this window closes 2026-12-05.
