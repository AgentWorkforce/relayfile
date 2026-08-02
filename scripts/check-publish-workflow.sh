#!/usr/bin/env bash

set -euo pipefail

workflow_file="${1:-.github/workflows/publish.yml}"
expected_push_count="${2:-1}"
allowed_tag_push='git push origin "refs/tags/v${NEW_VERSION}:refs/tags/v${NEW_VERSION}"'
allowed_python_tag_push='git push origin "refs/tags/sdk-python-v${NEW_VERSION}:refs/tags/sdk-python-v${NEW_VERSION}"'
push_count=0

while IFS= read -r source_line; do
  line="${source_line#"${source_line%%[![:space:]]*}"}"

  case "$line" in
    \#*|'')
      continue
      ;;
  esac

  if [[ "$line" =~ (^|[[:space:]:;|\&])git([[:space:]]+[^[:space:];|\&]+)*[[:space:]]+(add|commit|push)([[:space:];|\&]|$) ]]; then
    if [[ "$line" == "$allowed_tag_push" || "$line" == "$allowed_python_tag_push" ]]; then
      push_count=$((push_count + 1))
      continue
    fi
    echo "publish workflow check failed: only the explicit release-tag refspec may mutate git state: $line" >&2
    exit 1
  fi
done < "$workflow_file"

case "$expected_push_count" in
  allow-zero)
    if [[ "$push_count" -gt 1 ]]; then
      echo "publish workflow check failed: expected at most one explicit release-tag push, found $push_count" >&2
      exit 1
    fi
    ;;
  1)
    if [[ "$push_count" -ne 1 ]]; then
      echo "publish workflow check failed: expected exactly one explicit release-tag push, found $push_count" >&2
      exit 1
    fi
    ;;
  *)
    echo "publish workflow check failed: unsupported expected push count: $expected_push_count" >&2
    exit 1
    ;;
esac

echo "publish workflow check passed"
