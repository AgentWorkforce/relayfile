#!/usr/bin/env bash

set -euo pipefail

workflow_file="${1:-.github/workflows/publish.yml}"
allowed_tag_push='git push origin "refs/tags/v${NEW_VERSION}:refs/tags/v${NEW_VERSION}"'
push_count=0

while IFS= read -r source_line; do
  line="${source_line#"${source_line%%[![:space:]]*}"}"

  case "$line" in
    \#*|'')
      continue
      ;;
  esac

  if [[ "$line" == git\ add* || "$line" == git\ commit* ]]; then
    echo "publish workflow check failed: release workflow may not create commits: $line" >&2
    exit 1
  fi

  if [[ "$line" == git\ push* ]]; then
    if [[ "$line" != "$allowed_tag_push" ]]; then
      echo "publish workflow check failed: only the explicit release-tag refspec may be pushed: $line" >&2
      exit 1
    fi
    push_count=$((push_count + 1))
  fi
done < "$workflow_file"

if [[ "$push_count" -ne 1 ]]; then
  echo "publish workflow check failed: expected exactly one explicit release-tag push, found $push_count" >&2
  exit 1
fi

echo "publish workflow check passed"
