#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/relayfile-bootstrap-424.XXXXXX")
cleanup() {
	rm -rf -- "$scratch"
}
trap cleanup EXIT HUP INT TERM

baseline_src="$scratch/v0.10.39-src"
baseline_bin="$scratch/v0.10.39/relayfile"
candidate_bin="$scratch/candidate/relayfile"
mkdir -p "$baseline_src" "$(dirname -- "$baseline_bin")" "$(dirname -- "$candidate_bin")"

git -C "$repo_root" archive v0.10.39 | tar -x -C "$baseline_src"

printf 'building v0.10.39 CLI and mount helper from tag ea67a73\n'
(
	cd "$baseline_src"
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.relayfileVersion=0.10.39" -o "$baseline_bin" ./cmd/relayfile-cli
	CGO_ENABLED=0 go build -ldflags "-s -w" -o "$scratch/v0.10.39/relayfile-mount" ./cmd/relayfile-mount
)

candidate_commit=$(git -C "$repo_root" rev-parse --short=12 HEAD)
printf 'building candidate CLI and mount helper from commit %s\n' "$candidate_commit"
(
	cd "$repo_root"
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.relayfileVersion=issue-424-$candidate_commit" -o "$candidate_bin" ./cmd/relayfile-cli
	CGO_ENABLED=0 go build -ldflags "-s -w" -o "$scratch/candidate/relayfile-mount" ./cmd/relayfile-mount
)

printf 'running built-CLI convergence comparison\n'
(
	cd "$repo_root"
	RELAYFILE_BASELINE_BIN="$baseline_bin" \
	RELAYFILE_CANDIDATE_BIN="$candidate_bin" \
	go test ./cmd/relayfile-cli -run '^TestBootstrapConvergenceAgainstV01039$' -count=1 -v
)
