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

if ! git -C "$repo_root" rev-parse --verify --quiet refs/tags/v0.10.39 >/dev/null; then
	printf 'fetching missing tag v0.10.39\n'
	git -C "$repo_root" fetch --quiet origin tag v0.10.39
fi
git -C "$repo_root" archive v0.10.39 | tar -x -C "$baseline_src"

printf 'building v0.10.39 CLI from tag ea67a73\n'
(
	cd "$baseline_src"
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.relayfileVersion=0.10.39" -o "$baseline_bin" ./cmd/relayfile-cli
)

candidate_commit=$(git -C "$repo_root" rev-parse --short=12 HEAD)
printf 'building candidate CLI from commit %s\n' "$candidate_commit"
(
	cd "$repo_root"
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.relayfileVersion=issue-424-$candidate_commit" -o "$candidate_bin" ./cmd/relayfile-cli
)

printf 'running built-CLI convergence comparison\n'
(
	cd "$repo_root"
	RELAYFILE_BASELINE_BIN="$baseline_bin" \
	RELAYFILE_CANDIDATE_BIN="$candidate_bin" \
	go test ./cmd/relayfile-cli -run '^TestBootstrapConvergenceAgainstV01039$' -count=1 -v
)
