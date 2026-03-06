#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

ENV_FILE="${ROOT_DIR}/.env"
SEED_PATH=""
SEED_CONTENT=""
WAIT_SECONDS=90
SKIP_BUILD=0
FOLLOW_LOGS=0

usage() {
  cat <<USAGE
Usage: scripts/live-e2e.sh [options]

Options:
  --env-file <path>       Env file for docker compose (default: .env)
  --seed-path <path>      Remote file path to seed (default: <remote-root>/AgentGuide.md)
  --seed-content <text>   Seed markdown content (default: timestamped heading)
  --wait-seconds <n>      Max wait for health/materialization (default: 90)
  --skip-build            Start compose without --build
  --follow-logs           Tail relayfile and mountsync logs at the end
  -h, --help              Show this help
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --seed-path)
      SEED_PATH="$2"
      shift 2
      ;;
    --seed-content)
      SEED_CONTENT="$2"
      shift 2
      ;;
    --wait-seconds)
      WAIT_SECONDS="$2"
      shift 2
      ;;
    --skip-build)
      SKIP_BUILD=1
      shift
      ;;
    --follow-logs)
      FOLLOW_LOGS=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [[ "${ENV_FILE}" != /* ]]; then
  ENV_FILE="${ROOT_DIR}/${ENV_FILE}"
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required" >&2
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "docker compose is required" >&2
  exit 1
fi

if [[ ! -f "${ENV_FILE}" ]]; then
  if [[ -f "${ROOT_DIR}/compose.env.example" ]]; then
    cp "${ROOT_DIR}/compose.env.example" "${ENV_FILE}"
    echo "created ${ENV_FILE} from compose.env.example"
  else
    echo "missing env file: ${ENV_FILE}" >&2
    exit 1
  fi
fi

# shellcheck disable=SC1090
set -a; source "${ENV_FILE}"; set +a

RELAYFILE_PORT="${RELAYFILE_PORT:-8080}"
RELAYFILE_WORKSPACE="${RELAYFILE_WORKSPACE:-ws_live}"
RELAYFILE_REMOTE_PATH="${RELAYFILE_REMOTE_PATH:-/}"
RELAYFILE_JWT_SECRET="${RELAYFILE_JWT_SECRET:-dev-secret}"
RELAYFILE_INTERNAL_HMAC_SECRET="${RELAYFILE_INTERNAL_HMAC_SECRET:-dev-internal-secret}"

if [[ -z "${SEED_PATH}" ]]; then
  remote_root_trimmed="${RELAYFILE_REMOTE_PATH%/}"
  if [[ -z "${remote_root_trimmed}" ]]; then
    remote_root_trimmed="/"
  fi
  if [[ "${remote_root_trimmed}" == "/" ]]; then
    SEED_PATH="/AgentGuide.md"
  else
    SEED_PATH="${remote_root_trimmed}/AgentGuide.md"
  fi
fi
if [[ -z "${SEED_CONTENT}" ]]; then
  SEED_CONTENT="# agent walkthrough $(date -u +%Y-%m-%dT%H:%M:%SZ)"
fi

if [[ -z "${RELAYFILE_TOKEN:-}" ]]; then
  RELAYFILE_TOKEN="$(${ROOT_DIR}/scripts/generate-dev-token.sh "${RELAYFILE_WORKSPACE}")"
fi

mkdir -p "${ROOT_DIR}/.livefs"

compose() {
  env RELAYFILE_TOKEN="${RELAYFILE_TOKEN}" docker compose --env-file "${ENV_FILE}" "$@"
}

echo "[info] Provider writeback is handled externally via the generic webhook API and writeback queue."
echo "[info] External services should poll /v1/workspaces/{ws}/writeback/pending and acknowledge via /v1/workspaces/{ws}/writeback/{id}/ack"

echo "[step] starting compose stack"
if [[ "${SKIP_BUILD}" -eq 1 ]]; then
  compose up -d
else
  compose up --build -d
fi

base_url="http://127.0.0.1:${RELAYFILE_PORT}"
echo "[step] waiting for relayfile health at ${base_url}/health"
health_deadline=$((SECONDS + WAIT_SECONDS))
until curl -fsS "${base_url}/health" >/dev/null 2>&1; do
  if (( SECONDS >= health_deadline )); then
    echo "timed out waiting for relayfile health" >&2
    compose logs --tail=200 relayfile mountsync || true
    exit 1
  fi
  sleep 2
done

echo "[step] starting agent-workspace profile"
compose --profile agent up -d agent-workspace

echo "[step] sending signed internal webhook envelope"
RELAYFILE_BASE_URL="${base_url}" \
RELAYFILE_WORKSPACE="${RELAYFILE_WORKSPACE}" \
RELAYFILE_INTERNAL_HMAC_SECRET="${RELAYFILE_INTERNAL_HMAC_SECRET}" \
"${ROOT_DIR}/scripts/send-internal-envelope.sh" "${SEED_PATH}" "${SEED_CONTENT}" >/dev/null

target_rel="${SEED_PATH}"
remote_prefix="${RELAYFILE_REMOTE_PATH%/}"
if [[ -n "${remote_prefix}" && "${remote_prefix}" != "/" && "${target_rel}" == "${remote_prefix}"* ]]; then
  target_rel="${target_rel#"${remote_prefix}"}"
fi
target_rel="${target_rel#/}"
if [[ -z "${target_rel}" ]]; then
  target_rel="AgentGuide.md"
fi
local_target="${ROOT_DIR}/.livefs/${target_rel}"

echo "[step] waiting for mount materialization: ${local_target}"
file_deadline=$((SECONDS + WAIT_SECONDS))
until [[ -f "${local_target}" ]]; do
  if (( SECONDS >= file_deadline )); then
    echo "timed out waiting for mounted file" >&2
    compose logs --tail=200 relayfile mountsync || true
    exit 1
  fi
  sleep 1
done

echo "[step] agent container traverses mounted workspace"
compose exec -T agent-workspace sh -lc 'echo "[agent] files under /workspace:"; find /workspace -maxdepth 4 -type f | sort'

echo "[step] agent container reads seeded file"
compose exec -T agent-workspace sh -lc "echo '[agent] preview /workspace/${target_rel}:'; sed -n '1,80p' '/workspace/${target_rel}'"

echo "[step] agent container edits file"
agent_line="# agent edit $(date -u +%Y-%m-%dT%H:%M:%SZ)"
compose exec -T agent-workspace sh -lc "printf '%s\n' '${agent_line}' >> '/workspace/${target_rel}'"

sleep 3

ops_corr="corr_live_e2e_ops_$(date +%s)"
admin_corr="corr_live_e2e_admin_$(date +%s)"

echo "[step] latest ops feed"
curl -fsS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${ops_corr}" \
  "${base_url}/v1/workspaces/${RELAYFILE_WORKSPACE}/ops?limit=20"
echo

echo "[step] backend status"
curl -fsS \
  -H "Authorization: Bearer ${RELAYFILE_TOKEN}" \
  -H "X-Correlation-Id: ${admin_corr}" \
  "${base_url}/v1/admin/backends"
echo

echo "[done] live e2e flow complete"
echo "- mounted workspace: ${ROOT_DIR}/.livefs"
echo "- agent shell: env RELAYFILE_TOKEN='${RELAYFILE_TOKEN}' docker compose --env-file '${ENV_FILE}' exec agent-workspace sh"

echo "- stop stack: docker compose --env-file '${ENV_FILE}' down"

if [[ "${FOLLOW_LOGS}" -eq 1 ]]; then
  echo "[step] following relayfile + mountsync logs"
  compose logs -f relayfile mountsync
fi
