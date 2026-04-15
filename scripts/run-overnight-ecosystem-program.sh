#!/usr/bin/env bash
set -euo pipefail
ROOT="$HOME/Projects/AgentWorkforce"
LOG_DIR="$ROOT/relayfile/.overnight"
mkdir -p "$LOG_DIR"
STAMP="$(date +%Y%m%d-%H%M%S)"
LOG="$LOG_DIR/overnight-ecosystem-$STAMP.log"
SUMMARY="$LOG_DIR/overnight-ecosystem-$STAMP-summary.md"
exec > >(tee -a "$LOG") 2>&1

echo "# Overnight ecosystem program"
echo "Started: $(date -Is)"

echo "## Wave 1: NightCTO file-backed proof + relayfile canonical schema boundary"
(
  cd "$ROOT/nightcto"
  env PATH="$HOME/.local/bin:$PATH" NODE_PATH="$HOME/Projects/AgentWorkforce/relay/node_modules" agent-relay run workflows/agent-assistant/08-nightcto-file-backed-consumption-proof.ts
) &
PID1=$!
(
  cd "$ROOT/relayfile"
  env PATH="$HOME/.local/bin:$PATH" NODE_PATH="$HOME/Projects/AgentWorkforce/relay/node_modules" agent-relay run workflows/055-canonical-file-schema-ownership-boundary.ts
) &
PID2=$!
wait $PID1 || true
wait $PID2 || true

echo "## Finished wave 1: $(date -Is)"

echo "# Overnight ecosystem summary" > "$SUMMARY"
echo "Generated: $(date -Is)" >> "$SUMMARY"
echo "- Log: $LOG" >> "$SUMMARY"
echo "- NightCTO workflow: workflows/agent-assistant/08-nightcto-file-backed-consumption-proof.ts" >> "$SUMMARY"
echo "- relayfile workflow: workflows/055-canonical-file-schema-ownership-boundary.ts" >> "$SUMMARY"

echo "Finished: $(date -Is)"
