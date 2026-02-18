#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

if [[ -f "${ROOT_DIR}/.env" ]]; then
  # shellcheck disable=SC1090
  set -a; source "${ROOT_DIR}/.env"; set +a
fi

RELAYFILE_BASE_URL="${RELAYFILE_BASE_URL:-http://127.0.0.1:${RELAYFILE_PORT:-8080}}"
RELAYFILE_WORKSPACE="${RELAYFILE_WORKSPACE:-ws_live}"
RELAYFILE_INTERNAL_HMAC_SECRET="${RELAYFILE_INTERNAL_HMAC_SECRET:-dev-internal-secret}"
RELAYFILE_REMOTE_PATH="${RELAYFILE_REMOTE_PATH:-/notion}"

NOTION_TOKEN="${NOTION_TOKEN:-${RELAYFILE_NOTION_TOKEN:-}}"
NOTION_API_BASE_URL="${NOTION_API_BASE_URL:-https://api.notion.com}"
NOTION_API_VERSION="${NOTION_API_VERSION:-2022-06-28}"
NOTION_DATABASE_ID="${NOTION_DATABASE_ID:-}"
NOTION_QUERY="${NOTION_QUERY:-}"
NOTION_PAGE_SIZE="${NOTION_PAGE_SIZE:-25}"
NOTION_MAX_PAGES="${NOTION_MAX_PAGES:-50}"
NOTION_IMPORT_DELAY_MS="${NOTION_IMPORT_DELAY_MS:-75}"
FETCH_CONTENT=1
DRY_RUN=0

usage() {
  cat <<USAGE
Usage: scripts/import-notion-pages.sh [options]

Imports real Notion pages into RelayFile by emitting signed internal envelopes.
This is for local testing when a Notion bridge/proxy is not available.

Options:
  --database-id <id>      Query a specific Notion database (recommended)
  --query <text>          Search text for /v1/search mode
  --max-pages <n>         Maximum pages to import (default: 50)
  --page-size <n>         Notion page size per API call (default: 25)
  --remote-root <path>    Remote root path (default: /notion)
  --workspace <id>        RelayFile workspace ID (default: ws_live)
  --base-url <url>        RelayFile base URL (default: http://127.0.0.1:8080)
  --internal-secret <s>   RelayFile internal HMAC secret
  --no-content            Do not fetch block children; import title metadata only
  --delay-ms <n>          Delay between envelope submissions (default: 75)
  --dry-run               Print actions without sending envelopes
  -h, --help              Show this help

Required env/inputs:
  - NOTION_TOKEN or RELAYFILE_NOTION_TOKEN
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --database-id)
      NOTION_DATABASE_ID="$2"
      shift 2
      ;;
    --query)
      NOTION_QUERY="$2"
      shift 2
      ;;
    --max-pages)
      NOTION_MAX_PAGES="$2"
      shift 2
      ;;
    --page-size)
      NOTION_PAGE_SIZE="$2"
      shift 2
      ;;
    --remote-root)
      RELAYFILE_REMOTE_PATH="$2"
      shift 2
      ;;
    --workspace)
      RELAYFILE_WORKSPACE="$2"
      shift 2
      ;;
    --base-url)
      RELAYFILE_BASE_URL="$2"
      shift 2
      ;;
    --internal-secret)
      RELAYFILE_INTERNAL_HMAC_SECRET="$2"
      shift 2
      ;;
    --no-content)
      FETCH_CONTENT=0
      shift
      ;;
    --delay-ms)
      NOTION_IMPORT_DELAY_MS="$2"
      shift 2
      ;;
    --dry-run)
      DRY_RUN=1
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

require_bin() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required dependency: $1" >&2
    exit 1
  fi
}

require_bin curl
require_bin jq

if [[ -z "${NOTION_TOKEN}" ]]; then
  echo "NOTION_TOKEN (or RELAYFILE_NOTION_TOKEN) is required" >&2
  exit 1
fi
if [[ ! "${NOTION_PAGE_SIZE}" =~ ^[0-9]+$ ]] || (( NOTION_PAGE_SIZE < 1 || NOTION_PAGE_SIZE > 100 )); then
  echo "NOTION_PAGE_SIZE must be an integer in [1,100]" >&2
  exit 1
fi
if [[ ! "${NOTION_MAX_PAGES}" =~ ^[0-9]+$ ]] || (( NOTION_MAX_PAGES < 1 )); then
  echo "NOTION_MAX_PAGES must be a positive integer" >&2
  exit 1
fi
if [[ ! "${NOTION_IMPORT_DELAY_MS}" =~ ^[0-9]+$ ]]; then
  echo "NOTION_IMPORT_DELAY_MS must be a non-negative integer" >&2
  exit 1
fi

SEND_SCRIPT="${ROOT_DIR}/scripts/send-internal-envelope.sh"
if [[ ! -x "${SEND_SCRIPT}" ]]; then
  echo "missing helper script: ${SEND_SCRIPT}" >&2
  exit 1
fi

normalize_remote_root() {
  local raw="$1"
  raw="/${raw#/}"
  while [[ "${raw}" == *"//"* ]]; do
    raw="${raw//\/\//\/}"
  done
  raw="${raw%/}"
  if [[ -z "${raw}" ]]; then
    raw="/"
  fi
  printf '%s' "${raw}"
}

slugify() {
  local title="$1"
  local slug
  slug="$(printf '%s' "${title}" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9._-]+/-/g; s/-+/-/g; s/^-+//; s/-+$//')"
  if [[ -z "${slug}" ]]; then
    slug="notion-page"
  fi
  printf '%s' "${slug}"
}

notion_api() {
  local method="$1"
  local path="$2"
  local payload="${3:-}"

  local args=(
    curl -fsS -X "${method}" "${NOTION_API_BASE_URL%/}${path}"
    -H "Authorization: Bearer ${NOTION_TOKEN}"
    -H "Notion-Version: ${NOTION_API_VERSION}"
    -H "Content-Type: application/json"
  )
  if [[ -n "${payload}" ]]; then
    args+=(--data "${payload}")
  fi
  "${args[@]}"
}

build_list_payload() {
  local cursor="$1"
  if [[ -n "${NOTION_DATABASE_ID}" ]]; then
    jq -nc --argjson page_size "${NOTION_PAGE_SIZE}" --arg cursor "${cursor}" '
      {page_size: $page_size}
      + (if $cursor != "" then {start_cursor: $cursor} else {} end)
    '
  else
    jq -nc --arg query "${NOTION_QUERY}" --argjson page_size "${NOTION_PAGE_SIZE}" --arg cursor "${cursor}" '
      {
        query: $query,
        filter: {property: "object", value: "page"},
        page_size: $page_size
      }
      + (if $cursor != "" then {start_cursor: $cursor} else {} end)
    '
  fi
}

fetch_block_markdown() {
  local page_id="$1"
  local response
  response="$(notion_api GET "/v1/blocks/${page_id}/children?page_size=100")"

  printf '%s' "${response}" | jq -r '
    [
      .results[]
      | . as $b
      | (
          if ($b.type == "paragraph") then ($b.paragraph.rich_text // [])
          elif ($b.type == "heading_1") then ($b.heading_1.rich_text // [])
          elif ($b.type == "heading_2") then ($b.heading_2.rich_text // [])
          elif ($b.type == "heading_3") then ($b.heading_3.rich_text // [])
          elif ($b.type == "bulleted_list_item") then ($b.bulleted_list_item.rich_text // [])
          elif ($b.type == "numbered_list_item") then ($b.numbered_list_item.rich_text // [])
          elif ($b.type == "to_do") then ($b.to_do.rich_text // [])
          elif ($b.type == "toggle") then ($b.toggle.rich_text // [])
          elif ($b.type == "quote") then ($b.quote.rich_text // [])
          elif ($b.type == "callout") then ($b.callout.rich_text // [])
          elif ($b.type == "code") then ($b.code.rich_text // [])
          else []
          end
        )
      | map(.plain_text)
      | join("")
      | select(length > 0)
    ]
    | join("\n\n")
  '
}

remote_root="$(normalize_remote_root "${RELAYFILE_REMOTE_PATH}")"
if [[ "${remote_root}" == "/" ]]; then
  remote_root="/notion"
fi

used_paths_file="$(mktemp)"
cleanup() {
  rm -f "${used_paths_file}"
}
trap cleanup EXIT

path_seen() {
  local value="$1"
  grep -Fxq -- "${value}" "${used_paths_file}"
}

mark_path() {
  local value="$1"
  printf '%s\n' "${value}" >> "${used_paths_file}"
}

imported=0
cursor=""

while (( imported < NOTION_MAX_PAGES )); do
  payload="$(build_list_payload "${cursor}")"
  if [[ -n "${NOTION_DATABASE_ID}" ]]; then
    list_response="$(notion_api POST "/v1/databases/${NOTION_DATABASE_ID}/query" "${payload}")"
  else
    list_response="$(notion_api POST "/v1/search" "${payload}")"
  fi

  page_lines=$(printf '%s' "${list_response}" | jq -r '
    def page_title:
      (
        (.properties // {})
        | to_entries
        | map(
            select(.value.type == "title")
            | (.value.title // [])
            | map(.plain_text)
            | join("")
          )
        | map(select(length > 0))
        | .[0]
      ) // "Untitled";

    (.results // [])
    | map(select(.object == "page"))
    | .[]
    | [
        (.id // ""),
        ((.url // "") | gsub("[\t\r\n]+"; " ")),
        (page_title | gsub("[\t\r\n]+"; " "))
      ]
    | @tsv
  ')

  if [[ -z "${page_lines}" ]]; then
    break
  fi

  while IFS=$'\t' read -r page_id page_url page_title; do
    if [[ -z "${page_id}" ]]; then
      continue
    fi
    if (( imported >= NOTION_MAX_PAGES )); then
      break
    fi

    slug="$(slugify "${page_title}")"
    path="${remote_root}/${slug}.md"
    if path_seen "${path}"; then
      short_id="${page_id//-/}"
      short_id="${short_id:0:8}"
      path="${remote_root}/${slug}-${short_id}.md"
    fi
    mark_path "${path}"

    content="# ${page_title}"
    if [[ -n "${page_url}" ]]; then
      content+=$'\n\n'
      content+="Source: ${page_url}"
    fi
    if [[ "${FETCH_CONTENT}" -eq 1 ]]; then
      block_text="$(fetch_block_markdown "${page_id}" || true)"
      if [[ -n "${block_text}" ]]; then
        content+=$'\n\n---\n\n'
        content+="${block_text}"
      fi
    fi

    if [[ "${DRY_RUN}" -eq 1 ]]; then
      printf '[dry-run] import %s -> %s\n' "${page_id}" "${path}"
    else
      RELAYFILE_BASE_URL="${RELAYFILE_BASE_URL}" \
      RELAYFILE_WORKSPACE="${RELAYFILE_WORKSPACE}" \
      RELAYFILE_INTERNAL_HMAC_SECRET="${RELAYFILE_INTERNAL_HMAC_SECRET}" \
      RELAYFILE_OBJECT_ID="${page_id}" \
      RELAYFILE_CONTENT_TYPE="text/markdown" \
      "${SEND_SCRIPT}" "${path}" "${content}" >/dev/null
      printf '[imported] %s -> %s\n' "${page_id}" "${path}"
      if (( NOTION_IMPORT_DELAY_MS > 0 )); then
        sleep "0.$(printf '%03d' "${NOTION_IMPORT_DELAY_MS}")"
      fi
    fi

    imported=$((imported + 1))
  done <<< "${page_lines}"

  has_more="$(printf '%s' "${list_response}" | jq -r '.has_more // false')"
  cursor="$(printf '%s' "${list_response}" | jq -r '.next_cursor // ""')"
  if [[ "${has_more}" != "true" || -z "${cursor}" ]]; then
    break
  fi
done

echo "import complete: ${imported} page(s) processed"
if [[ "${DRY_RUN}" -eq 1 ]]; then
  echo "dry-run mode: no envelopes were sent"
fi
