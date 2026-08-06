#!/usr/bin/env bash
set -euo pipefail

vf_start() {
  local name="${1:?procedure name required}"
  local tmp_parent="${TMPDIR:-/tmp}"
  VF_CASE_ROOT="$(mktemp -d "${tmp_parent%/}/relayfile-feature-${name}.XXXXXX")"
  export VF_CASE_ROOT
  : >"$VF_CASE_ROOT/.relayfile-feature-fixture"
  export XDG_CONFIG_HOME="$VF_CASE_ROOT/xdg"
  mkdir -p "$XDG_CONFIG_HOME" "$VF_CASE_ROOT/state" "$VF_CASE_ROOT/data"
}

vf_cleanup() {
  local root="${VF_CASE_ROOT:-}"
  if [[ -z "$root" || ! -f "$root/.relayfile-feature-fixture" ]]; then
    printf '%s\n' 'FAIL cleanup refused: fixture marker is absent' >&2
    return 1
  fi
  case "$(basename "$root")" in
    relayfile-feature-*.??????) ;;
    *) printf 'FAIL cleanup refused unexpected root: %s\n' "$root" >&2; return 1 ;;
  esac
  rm -rf -- "$root"
  unset VF_CASE_ROOT
}

vf_result() {
  local status="${1:?status required}"
  local procedure="${2:?procedure required}"
  local detail="${3:-}"
  case "$status" in PASS|FAIL|SKIP|MANUAL) ;; *) return 2 ;; esac
  node -e '
    const [status, procedure, detail] = process.argv.slice(1);
    process.stdout.write(`${JSON.stringify({ status, procedure, detail })}\n`);
  ' "$status" "$procedure" "$detail"
}

vf_wait_http() {
  local url="${1:?url required}"
  local attempts="${2:-50}"
  local i
  for ((i = 1; i <= attempts; i++)); do
    if curl --fail --silent --show-error --max-time 1 "$url" >/dev/null; then return 0; fi
    sleep 0.1
  done
  return 1
}

vf_free_port() {
  node -e 'const net=require("node:net");const s=net.createServer();s.listen(0,"127.0.0.1",()=>{console.log(s.address().port);s.close()})'
}
