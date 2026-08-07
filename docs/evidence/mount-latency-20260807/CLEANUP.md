# Cleanup for the 2026-08-07 mount latency run

Everything this run created is disposable. Nothing here touches the
pre-existing `.dev-collab-stack/` or `.salvaged-from-minis/` directories,
their processes, their ports, or the existing sf-mini mounts — those were
deliberately left alone and must stay that way.

## What this run started

**On the sender host (`khaliqs-macbook-pro`, Tailscale `100.89.219.17`)**

| Thing | Where |
|---|---|
| `relayfile-server` | bound to `100.89.219.17:18299` (Tailscale address only, not `0.0.0.0`) |
| `dev-authd.py serve` (JWKS) | `127.0.0.1:19091`, loopback only |
| Server state file | `<scratch>/latency-run/state/state.json` — outside the repo |
| Throwaway RSA private key + minted tokens | `<scratch>/latency-run/keys/` — outside the repo, mode 0600 |

**On the receiver host (`sf-mac-mini`, Tailscale `100.102.30.76`)**

| Thing | Where |
|---|---|
| `relayfile-cli mount ws_latency_20260807` | mirror at `~/relayfile-latency-mount-20260807` |
| `receiver-watch.py` | writing `~/.relayfile-latency-harness/raw/` |
| `clock-offset.py server` | port `19299` |
| Deployed harness + receiver token | `~/.relayfile-latency-harness/` |

## Teardown

Receiver:

```sh
ssh sf-mini '
  pkill -f "relayfile-cli mount ws_latency_20260807"
  pkill -f receiver-watch.py
  pkill -f "clock-offset.py server"
  rm -rf ~/relayfile-latency-mount-20260807
  rm -rf ~/.relayfile-latency-harness
'
```

Sender:

```sh
pkill -f "bin/relayfile-server"
pkill -f "dev-authd.py serve"
rm -rf <scratch>/latency-run
```

`pkill -f "relayfile-cli mount ws_latency_20260807"` is deliberately matched on
the full workspace name. sf-mini also runs unrelated pre-existing mounts
(`relayfile-dev-collab`, `relay-dev-collab`); a looser pattern would kill them.

## Verifying nothing else was disturbed

```sh
lsof -nP -iTCP:8299 -sTCP:LISTEN     # dev-collab server port: expected untouched
ssh sf-mini 'pgrep -fl "relayfile-cli.*dev-collab"'   # pre-existing mounts still up
git -C <repo> status --short         # .dev-collab-stack/ and .salvaged-from-minis/ still untracked, unmodified
```

## Credentials

The RSA key and the bearer tokens minted for this run are throwaway, scoped to
workspace `ws_latency_20260807`, short-lived, and were never written into any
committed artifact or sent over Relay. Deleting the scratch directory and
`~/.relayfile-latency-harness` on the receiver destroys them.

Separately, and unrelated to this run: a routine `ps` on sf-mini exposes live
`RELAY_API_KEY` and agent-token values in broker process argv, because they are
passed as command-line arguments. Those are pre-existing production credentials,
readable by any local process, and were reported for rotation. This run
deliberately passed its own receiver token via the `RELAYFILE_TOKEN` environment
variable rather than `--token` so as not to add to that exposure.
