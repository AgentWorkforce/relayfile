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

For every rerun, start each process in the background and immediately record a
PID-scoped identity under the run directory's `pids/` subdirectory. A PID alone
is insufficient because the operating system can reuse it. This helper records
the process start time and executable without listing any other process or
capturing command-line arguments:

```sh
record_identity() {
  name=$1 expected_executable=$2 pid=$3
  case "$pid" in *[!0-9]*|"") return 2 ;; esac
  start=$(ps -p "$pid" -o lstart= | sed 's/^ *//; s/ *$//')
  executable=$(ps -p "$pid" -o comm= | sed 's/^ *//; s/ *$//')
  test -n "$start" && test -n "$executable" || return 2
  test "${executable##*/}" = "$expected_executable" || return 2
  printf '%s\n%s\n%s\n' "$pid" "$start" "$executable" \
    > "$run_dir/pids/$name.identity"
}

# Examples, immediately after each background launch:
record_identity mount relayfile-cli "$mount_pid"
record_identity watcher python3 "$watcher_pid"
record_identity clock-offset python3 "$clock_pid"
record_identity relayfile-server relayfile-server "$server_pid"
record_identity dev-authd python3 "$auth_pid"
```

Use the actual executable basename on a host if its Python binary has a
different name. Cleanup validates the exact recorded start time and executable
before signaling and never uses process-name matching.

The unauthenticated network `QUIT` command was removed from
`clock-offset.py`. Its listener is stopped only by its recorded local PID,
and the server now requires an explicit bind address.

## Teardown

Receiver (the variables are configured outside the evidence repository):

```sh
ssh "$RECEIVER_SSH_ALIAS" '
  run_dir="${RELAYFILE_RECEIVER_SCRATCH:?}/mount-latency-20260807"
  case "$run_dir" in */mount-latency-20260807) ;; *) exit 2 ;; esac
  stop_recorded() {
    identity="$run_dir/pids/$1.identity"
    test -f "$identity" || return 0
    { IFS= read -r pid; IFS= read -r recorded_start; IFS= read -r recorded_executable; } < "$identity"
    case "$pid" in *[!0-9]*|"") return 2 ;; esac
    current_start=$(ps -p "$pid" -o lstart= | sed "s/^ *//; s/ *$//") || return 0
    current_executable=$(ps -p "$pid" -o comm= | sed "s/^ *//; s/ *$//") || return 0
    test "$current_start" = "$recorded_start" || return 3
    test "$current_executable" = "$recorded_executable" || return 3
    kill "$pid"
    attempts=0
    while test "$attempts" -lt 100; do
      current_start=$(ps -p "$pid" -o lstart= | sed "s/^ *//; s/ *$//") || return 0
      current_executable=$(ps -p "$pid" -o comm= | sed "s/^ *//; s/ *$//") || return 0
      test "$current_start" = "$recorded_start" || return 0
      test "$current_executable" = "$recorded_executable" || return 0
      sleep 0.1
      attempts=$((attempts + 1))
    done
    return 4
  }
  for name in mount watcher clock-offset; do
    stop_recorded "$name" || exit $?
  done
  if mount | grep -F "$run_dir/mount" >/dev/null; then exit 5; fi
  rm -rf -- "$run_dir"
'
```

Sender:

```sh
run_dir="${RELAYFILE_SENDER_SCRATCH:?}/mount-latency-20260807"
case "$run_dir" in */mount-latency-20260807) ;; *) exit 2 ;; esac
stop_recorded() {
  identity="$run_dir/pids/$1.identity"
  test -f "$identity" || return 0
  { IFS= read -r pid; IFS= read -r recorded_start; IFS= read -r recorded_executable; } < "$identity"
  case "$pid" in *[!0-9]*|"") return 2 ;; esac
  current_start=$(ps -p "$pid" -o lstart= | sed 's/^ *//; s/ *$//') || return 0
  current_executable=$(ps -p "$pid" -o comm= | sed 's/^ *//; s/ *$//') || return 0
  test "$current_start" = "$recorded_start" || return 3
  test "$current_executable" = "$recorded_executable" || return 3
  kill "$pid"
  attempts=0
  while test "$attempts" -lt 100; do
    current_start=$(ps -p "$pid" -o lstart= | sed 's/^ *//; s/ *$//') || return 0
    current_executable=$(ps -p "$pid" -o comm= | sed 's/^ *//; s/ *$//') || return 0
    test "$current_start" = "$recorded_start" || return 0
    test "$current_executable" = "$recorded_executable" || return 0
    sleep 0.1
    attempts=$((attempts + 1))
  done
  return 4
}
for name in relayfile-server dev-authd; do
  stop_recorded "$name" || exit $?
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
