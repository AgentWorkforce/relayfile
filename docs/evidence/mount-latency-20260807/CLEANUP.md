# Cleanup for the 2026-08-07 mount latency run

Everything this run created is disposable. The run used distinct ports, state,
workspace, and mount paths rather than reusing the pre-existing development
stack. Hostnames, addresses, and home paths are represented by variables that
must be supplied outside this repository.

## What this run started

**On the `sender` host**

| Thing | Where |
|---|---|
| `relayfile-server` | `${SENDER_TAILNET_ADDRESS}:18299` (Tailscale address only, not `0.0.0.0`) |
| `dev-authd.py serve` (JWKS) | `127.0.0.1:19091`, loopback only |
| Server state file | `$RELAYFILE_SENDER_SCRATCH/mount-latency-20260807/state/state.json` |
| Throwaway RSA private key + minted tokens | `$RELAYFILE_SENDER_SCRATCH/mount-latency-20260807/keys/`, mode 0600 |

**On the `receiver` host**

| Thing | Where |
|---|---|
| `relayfile-cli mount ws_latency_20260807` | `$RELAYFILE_RECEIVER_SCRATCH/mount-latency-20260807/mount` |
| `receiver-watch.py` | `$RELAYFILE_RECEIVER_SCRATCH/mount-latency-20260807/raw/` |
| `clock-offset.py server` | explicit `$RECEIVER_TAILNET_ADDRESS` bind, port `19299` |
| Deployed harness + receiver token | `$RELAYFILE_RECEIVER_SCRATCH/mount-latency-20260807/harness/` |

For every rerun, start each process in the background and immediately record
its exact PID under the run directory's `pids/` subdirectory, using
`printf '%s\n' "$!" > "$pidfile"`. Cleanup must use only those PID files;
process-name matching is deliberately not part of the revised procedure.

The unauthenticated network `QUIT` command was removed from
`clock-offset.py`. Its listener is stopped only by its recorded local PID,
and the server now requires an explicit bind address.

## Teardown

Receiver (the variables are configured outside the evidence repository):

```sh
ssh "$RECEIVER_SSH_ALIAS" '
  run_dir="${RELAYFILE_RECEIVER_SCRATCH:?}/mount-latency-20260807"
  case "$run_dir" in */mount-latency-20260807) ;; *) exit 2 ;; esac
  for name in mount watcher clock-offset; do
    pidfile="$run_dir/pids/$name.pid"
    test -f "$pidfile" || continue
    IFS= read -r pid < "$pidfile"
    case "$pid" in *[!0-9]*|"") exit 2 ;; esac
    kill "$pid"
  done
  rm -rf -- "$run_dir"
'
```

Sender:

```sh
run_dir="${RELAYFILE_SENDER_SCRATCH:?}/mount-latency-20260807"
case "$run_dir" in */mount-latency-20260807) ;; *) exit 2 ;; esac
for name in relayfile-server dev-authd; do
  pidfile="$run_dir/pids/$name.pid"
  test -f "$pidfile" || continue
  IFS= read -r pid < "$pidfile"
  case "$pid" in *[!0-9]*|"") exit 2 ;; esac
  kill "$pid"
done
rm -rf -- "$run_dir"
```

This avoids the earlier broad `pkill` examples, which could have matched an
unrelated server or watcher on either host.

## Post-cleanup checks and limitation

```sh
lsof -nP -iTCP:8299 -sTCP:LISTEN
ssh "$RECEIVER_SSH_ALIAS" 'test -d "${PREEXISTING_MOUNT_ROOT:?}"'
git -C "$RELAYFILE_REPO" status --short
```

These post-run checks show that the separate service and paths still exist;
they do **not** prove that untracked directory contents were unchanged. No
before/after content snapshot or hash was captured, so no stronger isolation
claim is made.

## Credentials

The RSA key and bearer tokens minted for this run are throwaway, scoped to
workspace `ws_latency_20260807`, short-lived, and were never written into a
committed artifact or sent over Relay. Deleting the two validated run
directories destroys them. The receiver token was passed through the
`RELAYFILE_TOKEN` environment variable, never as a command-line argument.
