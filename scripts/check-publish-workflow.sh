#!/usr/bin/env bash

set -euo pipefail

workflow_file="${1:-.github/workflows/publish.yml}"
expected_tag_pushes="${2:-1}"

# Release jobs may publish only an immutable, namespaced release tag.  Keep
# this check deliberately narrow: it is a source-level control against a
# future reintroduction of a branch push (especially HEAD:main), not a shell
# parser.  The word boundary catches command substitutions, subshells, quoted
# snippets, and redirections that are easy for a line-prefix matcher to miss.
# shellcheck disable=SC2016 # these are literal workflow source patterns
allowed_tag_pushes=(
  'git push origin "refs/tags/v${NEW_VERSION}:refs/tags/v${NEW_VERSION}"'
  'git push origin "refs/tags/sdk-python-v${NEW_VERSION}:refs/tags/sdk-python-v${NEW_VERSION}"'
)

tag_pushes=0
while IFS= read -r source_line; do
  line="${source_line#"${source_line%%[![:space:]]*}"}"
  case "$line" in
    \#*|'') continue ;;
  esac

  # Strip YAML comments only when they begin outside the command text; the
  # workflow's release commands contain no literal '#' arguments.
  line="${line%%[[:space:]]#*}"
  folded_run_regex='^run:[[:space:]]*>[+-]?[[:space:]]*$'
  if [[ "$line" =~ $folded_run_regex ]]; then
    echo "publish workflow check failed: folded run scalars are forbidden" >&2
    exit 1
  fi
  if [[ "$line" =~ (^|[^[:alnum:]_])git[[:space:]]+push[[:space:]]+ ]]; then
    is_allowed=false
    for allowed in "${allowed_tag_pushes[@]}"; do
      if [[ "$line" == "$allowed" ]]; then
        is_allowed=true
        break
      fi
    done
    if [[ "$is_allowed" != true ]]; then
      echo "publish workflow check failed: only an explicit release-tag push is allowed: $line" >&2
      exit 1
    fi
    tag_pushes=$((tag_pushes + 1))
  fi
done < "$workflow_file"

# The line matcher above intentionally keeps the positive allowlist exact.
# These fail-closed guards cover shell continuations and indirect command
# names, which otherwise span lines or never contain the literal `git` token.
workflow_source=$(<"$workflow_file")
if [[ "$workflow_source" =~ (^|[^[:alnum:]_])git[[:space:]]*\\[[:space:]]*push[[:space:]]+ ]]; then
  echo "publish workflow check failed: multiline git push is forbidden" >&2
  exit 1
fi
if [[ "$workflow_source" =~ (^|[^[:alnum:]_])(\"?\$\{?[A-Za-z_][A-Za-z0-9_]*\}?\"?)[[:space:]]+push[[:space:]]+ ]]; then
  echo "publish workflow check failed: indirect git push is forbidden" >&2
  exit 1
fi
if [[ "$workflow_source" == *'"git" push '* || "$workflow_source" == *"'git' push "* ]]; then
  echo "publish workflow check failed: quoted git push is forbidden" >&2
  exit 1
fi

case "$expected_tag_pushes" in
  allow-zero)
    expected_max=1
    ;;
  1)
    expected_max=1
    ;;
  *)
    echo "publish workflow check failed: unsupported expected tag push count: $expected_tag_pushes" >&2
    exit 1
    ;;
esac

if [[ "$expected_tag_pushes" == 1 && "$tag_pushes" -ne 1 ]]; then
  echo "publish workflow check failed: expected exactly one release-tag push, found $tag_pushes" >&2
  exit 1
fi
if [[ "$tag_pushes" -gt "$expected_max" ]]; then
  echo "publish workflow check failed: expected at most one release-tag push, found $tag_pushes" >&2
  exit 1
fi

echo "publish workflow check passed"
