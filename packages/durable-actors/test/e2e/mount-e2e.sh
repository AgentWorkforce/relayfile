#!/usr/bin/env bash
# End-to-end proof that the real Go relayfile-mount works against a relayfile
# gateway backed by durable-actors Workspace actors.
#
#   BASE_URL=http://127.0.0.1:28480 TOKEN=<jwt> WORKSPACE=ws_e2e \
#   MOUNT_BIN=./relayfile-mount WORK_DIR=/tmp/rf-e2e bash mount-e2e.sh
#
# Optional: RESTART_CMD="docker restart rf-da" proves state survives a runtime restart.
# Checks: remote->local pull, local->remote push (op succeeds), live WebSocket
# delivery to a running daemon, delete propagation, large-file round trip,
# and (with RESTART_CMD) durability across a durable-actors restart.
set -euo pipefail

: "${BASE_URL:?}" "${TOKEN:?}" "${WORKSPACE:?}" "${MOUNT_BIN:?}" "${WORK_DIR:?}"
rm -rf "$WORK_DIR" && mkdir -p "$WORK_DIR/a" "$WORK_DIR/b" "$WORK_DIR/state-a" "$WORK_DIR/state-b"
H=(-H "authorization: Bearer $TOKEN" -H "x-correlation-id: e2e-$$")
API="$BASE_URL/v1/workspaces/$WORKSPACE"
pass() { printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1"; exit 1; }
mount_once() { # $1 dir $2 state
  "$MOUNT_BIN" --base-url "$BASE_URL" --token "$TOKEN" --workspace "$WORKSPACE" --local-dir "$1" \
    --state-dir "$2" --once --startup-jitter 0 --timeout 30s >>"$WORK_DIR/mount-once.log" 2>&1
}
wait_for() { # $1 description, $2.. command
  local desc=$1; shift
  for _ in $(seq 1 60); do if "$@" >/dev/null 2>&1; then pass "$desc"; return 0; fi; sleep 0.5; done
  fail "$desc"
}

curl -fsS "$BASE_URL/health" | grep -q '"symlink-v1"' && pass "health advertises symlink-v1"

# 1. remote -> local
curl -fsS -X PUT "${H[@]}" -H 'if-match: *' -H 'content-type: application/json' \
  "$API/fs/file?path=/e2e/remote.md" -d '{"content":"from the API"}' >/dev/null
mount_once "$WORK_DIR/a" "$WORK_DIR/state-a"
[ "$(cat "$WORK_DIR/a/e2e/remote.md")" = "from the API" ] && pass "mount pulled remote file" || fail "mount pulled remote file"

# 2. local -> remote (writeback through the mount outbox)
printf 'written locally\n' > "$WORK_DIR/a/e2e/local.md"
mount_once "$WORK_DIR/a" "$WORK_DIR/state-a"
remote=$(curl -fsS "${H[@]}" "$API/fs/file?path=/e2e/local.md")
echo "$remote" | grep -q 'written locally' && pass "mount pushed local file" || fail "mount pushed local file: $remote"
op=$(curl -fsS "${H[@]}" "$API/ops?limit=1")
echo "$op" | grep -q '"status":"succeeded"' && pass "writeback op succeeded" || fail "writeback op: $op"

# 3. live WebSocket delivery to a running daemon
"$MOUNT_BIN" --base-url "$BASE_URL" --token "$TOKEN" --workspace "$WORKSPACE" --local-dir "$WORK_DIR/b" \
  --state-dir "$WORK_DIR/state-b" --interval 60s --startup-jitter 0 --log-http-status >"$WORK_DIR/daemon.log" 2>&1 &
DAEMON=$!
trap 'kill $DAEMON 2>/dev/null || true' EXIT
wait_for "daemon initial pull" test -f "$WORK_DIR/b/e2e/local.md"
sleep 2
curl -fsS -X PUT "${H[@]}" -H 'if-match: *' -H 'content-type: application/json' \
  "$API/fs/file?path=/e2e/live.md" -d '{"content":"pushed over the actor socket"}' >/dev/null
# The daemon polls every 60s, so arriving within 30s means the WebSocket delivered it.
wait_for "daemon received live update (websocket)" grep -q 'pushed over the actor socket' "$WORK_DIR/b/e2e/live.md"

# 4. delete propagation
rev=$(curl -fsS "${H[@]}" "$API/fs/file?path=/e2e/live.md" | sed -E 's/.*"revision":"([^"]+)".*/\1/')
curl -fsS -X DELETE "${H[@]}" -H "if-match: $rev" "$API/fs/file?path=/e2e/live.md" >/dev/null
wait_for "daemon applied remote delete" test ! -e "$WORK_DIR/b/e2e/live.md"

# 5. large file round trip (exercises the blob store when one is configured)
head -c 300000 /dev/urandom | base64 > "$WORK_DIR/a/e2e/large.txt"
mount_once "$WORK_DIR/a" "$WORK_DIR/state-a"
wait_for "large file reached the daemon" cmp -s "$WORK_DIR/a/e2e/large.txt" "$WORK_DIR/b/e2e/large.txt"

kill $DAEMON 2>/dev/null || true
trap - EXIT

# 6. durability across a runtime restart
if [ -n "${RESTART_CMD:-}" ]; then
  eval "$RESTART_CMD"
  wait_for "state survived runtime restart" sh -c "curl -fsS -H 'authorization: Bearer $TOKEN' -H 'x-correlation-id: r' '$API/fs/file?path=/e2e/local.md' | grep -q 'written locally'"
fi
echo "ALL PASS"
