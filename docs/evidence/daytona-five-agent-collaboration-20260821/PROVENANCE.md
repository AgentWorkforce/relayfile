# Build, corpus, and Daytona provenance

Date: 2026-08-21
PR: `AgentWorkforce/relayfile#435`
Benchmark source parent: `b7b45e8cb0300f3c97544510a10bffdf9ba375a6`

## Pinned repository corpus

The archives were produced from exact Git commits, so local dirty and untracked
files were not included.

| Repository | Commit | Extracted regular files | Extracted bytes | Archive SHA-256 |
| --- | --- | ---: | ---: | --- |
| `relayfile` | `f89d152502d3bc15161e0673b96cb84f419cd30a` | 1,272 | 11,321,811 | `e6a1f7a8bbdc399259c0c61fb5612020249c40d9708d5631d23943b4b1eb6376` |
| `../relay` | `9cb8a5e0972f7013d035838c763fce4a50a92dd9` | 1,830 | 21,022,674 | `39053bd723bae7a698c442cb94626a413544ade5ab77735d8a0c00e2523f9b20` |
| `../cloud` | `04392ad080d44573f3a4c32c1e02eb5f2a221a0e` | 4,093 | 110,368,992 | `de59d5a9fe9cd02bd58cd8abde052eced0aede648184e9a6087413a00a31118c` |

The exact combined baseline was 7,195 files and 142,713,477 bytes, laid out
with Relayfile at the root, Relay under `/scale/relay`, and Cloud under
`/scale/cloud`.

## Deployed product artifacts

Both binaries were built for `linux/amd64` with CGO disabled, `-trimpath`, and
`-ldflags="-s -w"`.

| Artifact | SHA-256 |
| --- | --- |
| `cmd/relayfile` | `00e6748db66541a78dd467f655cf48fa88977d92ba7050d5abe84dc4a38af66e` |
| `cmd/relayfile-mount` | `9e1ab85bb7174aa809a93f214d03ec9c5cfe824e2b85ad09004b044f3782f8c6` |

Every qualifying sandbox reported these exact hashes. A fresh build from the
final product source reproduced both hashes byte-for-byte. The deployed source
is the product source carried by this PR and evidence commit.

## Harness artifacts

| Artifact | SHA-256 |
| --- | --- |
| `barrier_server.py` | `a76e0b8fde02447b74ad1f5fa70102e5663559ba51dc26bfd7bac26f305e79b1` |
| `conflict_write.py` | `d831f1618979cd0ff8a8dd8ab3f3da7896e87daf342e17039a5f6a9bbb566d4e` |
| `fanout_trial.py` | `31289f8d6d703e126b9cfd27f026183da9f652ea3c4c71f3b6ca02756c6a5ecb` |
| `orchestrate.py` | `b83279f3dcb81e5cbbffe7ae3aaa6e6ce7d9c0ee6c164dcd1af6890d15efb2b6` |
| `orchestrate_conflict.py` | `b3a5c6167a21350d2bd8162f9bf9b74578d6f6918abf1a96a0d95330059cb0b7` |
| `orchestrate_inspection.py` | see `SHA256SUMS` |
| `probe_server.py` | `1f4e5f630e78bba6355c0b1b3899dbee0617ee6448afb4b6f0765f2c8183bfd2` |
| `seed-workspace.py` | `e2bee3a48adf7ca32e40dcf2472f890dd5709f0e9195d50438720f317e7a44e8` |
| `aggregate_results.py` | `e492ce0dadf57b14c010d1ad35837d2f9aaad393777a08f932137ac0e02c7f33` |

The final read-only inspector was strengthened after the timed sequence to
make the already-frozen no-staging-file gate explicit in JSON; that did not
alter or rerun any latency sample. Its deployed and retained SHA-256 is
`70535ed13bf9b78d56a77df3dc4e0608f76a9e75f107ed9334eda746096b0f53`.

## Isolated Daytona fleet

All six sandboxes used snapshot `rf-five-agent-20260821-fix6`, Daytona region
`us`, 2 CPU, 2 GiB memory, and 5 GiB disk. Their TTL was four hours and mount
auto-stop was disabled during the run.

| Role | Name | Sandbox ID |
| --- | --- | --- |
| server | `acceptance-server` | `f64ab293-4a30-4856-bf07-bdcbed7efab7` |
| agent A | `acceptance-a` | `a749ae4c-d912-48c6-a442-6a1a78765a26` |
| agent B | `acceptance-b` | `abb6c8f9-09a3-4a3d-94c3-be9866b7c588` |
| agent C | `acceptance-c` | `925880a5-6028-491a-acaa-ae6469c4f098` |
| agent D | `acceptance-d` | `4549e42b-7878-48d4-b9e6-89b0a7620374` |
| agent E | `acceptance-e` | `c35ce175-ab1d-4236-91aa-f1a56b253032` |

The server used `segmented-file:///root/relayfile/state`, a 64 MiB request-body
limit, external writeback disabled, and localhost JWKS validation. Mounts used
private state outside `/root/shared-repo`, WebSocket delivery, and independent
signed HTTPS endpoints. Runtime tokens, signed URLs, and HMAC material are not
retained.
