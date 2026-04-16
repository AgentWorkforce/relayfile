#!/usr/bin/env bash
set -euo pipefail
ROOT="$HOME/Projects/AgentWorkforce"
LOG_DIR="$ROOT/relayfile/.overnight"
mkdir -p "$LOG_DIR"
STAMP="$(date +%Y%m%d-%H%M%S)"
LOG="$LOG_DIR/overnight-ecosystem-v2-$STAMP.log"
SUMMARY="$LOG_DIR/overnight-ecosystem-v2-$STAMP-summary.md"
exec > >(tee -a "$LOG") 2>&1

echo "# Overnight ecosystem program v2"
echo "Started: $(date '+%Y-%m-%dT%H:%M:%S%z')"

run_wf() {
  local dir="$1"
  local wf="$2"
  (
    cd "$dir"
    env PATH="$HOME/.local/bin:$PATH" NODE_PATH="$HOME/Projects/AgentWorkforce/relay/node_modules" agent-relay run "$wf"
  )
}

echo "## Wave 1"
run_wf "$ROOT/nightcto" workflows/agent-assistant/08-nightcto-file-backed-consumption-proof.ts &
PID1=$!
run_wf "$ROOT/relayfile" workflows/055-canonical-file-schema-ownership-boundary.ts &
PID2=$!
wait $PID1 || true
wait $PID2 || true

echo "## Wave 2"
run_wf "$ROOT/nightcto" workflows/agent-assistant/09-nightcto-live-retrieval-readiness.ts &
PID3=$!
run_wf "$ROOT/relayfile" workflows/056-first-canonical-schema-proof.ts &
PID4=$!
wait $PID3 || true
wait $PID4 || true

echo "## Wave 3"
run_wf "$ROOT/relayfile" workflows/057-remediate-first-canonical-schema-proof.ts &
PID5=$!
wait $PID5 || true

echo "# Overnight ecosystem v2 summary" > "$SUMMARY"
echo "Generated: $(date '+%Y-%m-%dT%H:%M:%S%z')" >> "$SUMMARY"
echo "- Log: $LOG" >> "$SUMMARY"
echo "- Wave 1: NightCTO file-backed consumption proof + relayfile canonical schema ownership boundary" >> "$SUMMARY"
echo "- Wave 2: NightCTO live retrieval readiness + relayfile first canonical schema proof" >> "$SUMMARY"
echo "- Wave 3: relayfile canonical schema proof remediation" >> "$SUMMARY"

echo "Finished: $(date '+%Y-%m-%dT%H:%M:%S%z')"
