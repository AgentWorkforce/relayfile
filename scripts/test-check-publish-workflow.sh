#!/usr/bin/env bash
# shellcheck disable=SC2016 # fixtures intentionally contain literal workflow expressions

set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
checker="$repo_root/scripts/check-publish-workflow.sh"
fixture_dir=$(mktemp -d)
trap 'rm -rf "$fixture_dir"' EXIT

# shellcheck disable=SC2016 # these are literal workflow fixture patterns
allowed='git push origin "refs/tags/v${NEW_VERSION}:refs/tags/v${NEW_VERSION}"'
python_allowed='git push origin "refs/tags/sdk-python-v${NEW_VERSION}:refs/tags/sdk-python-v${NEW_VERSION}"'

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

python_fixture="$fixture_dir/python.yml"
printf 'run: |\n  %s\n' "$python_allowed" > "$python_fixture"
"$checker" "$python_fixture" >/dev/null

assert_rejected branch-push 'git push origin main'
assert_rejected head-push 'git push origin HEAD:main'
assert_rejected command-substitution 'echo "$(git push origin main)"'
assert_rejected subshell '(git push origin main)'
assert_rejected backticks 'echo `git push origin main`'
assert_rejected grouping '{ git push origin main; }'
assert_rejected redirect '>out git push origin main'

multiline="$fixture_dir/multiline.yml"
printf 'run: |\n  git \\\n  push origin main\n' > "$multiline"
if "$checker" "$multiline" allow-zero >/dev/null 2>&1; then
  echo "checker accepted a multiline git push" >&2
  exit 1
fi

indirect="$fixture_dir/indirect.yml"
printf 'run: |\n  GIT=git\n  $GIT push origin main\n' > "$indirect"
if "$checker" "$indirect" allow-zero >/dev/null 2>&1; then
  echo "checker accepted an indirect git push" >&2
  exit 1
fi

no_push="$fixture_dir/no-push.yml"
printf 'run: echo release\n' > "$no_push"
"$checker" "$no_push" allow-zero >/dev/null
if "$checker" "$no_push" >/dev/null 2>&1; then
  echo "checker accepted a workflow with no release-tag push in strict mode" >&2
  exit 1
fi

two_push="$fixture_dir/two-push.yml"
printf 'run: |\n  %s\n  %s\n' "$allowed" "$python_allowed" > "$two_push"
if "$checker" "$two_push" allow-zero >/dev/null 2>&1; then
  echo "checker accepted two release-tag pushes" >&2
  exit 1
fi

echo "publish workflow checker tests passed"
