#!/bin/bash
# Heartbeat-advance gate: sf-mini is live iff its OWN lastHeartbeatAt advances.
# Absence from a fleet listing is NOT evidence of offline (the list returns
# nondeterministic subsets), and status/live are registration fields, not
# liveness fields. Only monotonic advance of lastHeartbeatAt counts.
OUT="$1"; DURATION="${2:-150}"; INTERVAL="${3:-10}"
END=$(( $(date +%s) + DURATION ))
while [ "$(date +%s)" -lt "$END" ]; do
  TS=$(python3 -c "import datetime;print(datetime.datetime.now(datetime.timezone.utc).isoformat())")
  HB=$(timeout 25 agent-relay fleet nodes --name sf-mini --all 2>/dev/null \
       | python3 -c 'import sys,json;d=json.load(sys.stdin);n=[x for x in d.get("nodes",[]) if x.get("name")=="sf-mini"];print(json.dumps({"present":bool(n),"lastHeartbeatAt":n[0].get("lastHeartbeatAt") if n else None,"status":n[0].get("status") if n else None,"live":n[0].get("live") if n else None,"activeAgents":n[0].get("activeAgents") if n else None}) if True else "")' 2>/dev/null)
  [ -z "$HB" ] && HB='{"present":false,"lastHeartbeatAt":null,"status":null,"live":null,"error":"query_failed"}'
  echo "{\"sampledAtLocalUtc\":\"$TS\",\"node\":$HB}" >> "$OUT"
  sleep "$INTERVAL"
done
