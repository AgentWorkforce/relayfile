#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
checker="$repo_root/scripts/check-publish-workflow.sh"
fixture_dir=$(mktemp -d)
trap 'rm -rf "$fixture_dir"' EXIT

allowed='git push origin "refs/tags/v${NEW_VERSION}:refs/tags/v${NEW_VERSION}"'

assert_rejected() {
  local name=$1
  local bypass=$2
  local fixture="$fixture_dir/$name.yml"
  printf 'run: |\n  %s\n  %s\n' "$allowed" "$bypass" > "$fixture"
  if "$checker" "$fixture" >/dev/null 2>&1; then
    echo "publish workflow checker accepted forbidden form: $bypass" >&2
    exit 1
  fi
}

valid_fixture="$fixture_dir/valid.yml"
printf 'run: |\n  %s\n' "$allowed" > "$valid_fixture"
"$checker" "$valid_fixture" >/dev/null

assert_rejected run-prefix 'run: git push origin main'
assert_rejected command-prefix 'command git push origin main'
assert_rejected config-prefix 'git -c push.default=current push origin main'
assert_rejected add-command 'env git add package.json'
assert_rejected commit-command 'command git commit -m release'

echo "publish workflow checker tests passed"
