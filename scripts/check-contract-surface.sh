#!/usr/bin/env bash

set -euo pipefail

has_pattern() {
  local pattern="$1"
  local file="$2"

  if command -v rg >/dev/null 2>&1; then
    rg --quiet --fixed-strings "$pattern" "$file"
    return
  fi

  grep -Fq -- "$pattern" "$file"
}

require_pattern() {
  local file="$1"
  local pattern="$2"
  local label="$3"

  if ! has_pattern "$pattern" "$file"; then
    echo "contract check failed: missing '$label' in $file"
    exit 1
  fi
}

OPENAPI_FILE="openapi/relayfile-v1.openapi.yaml"
SDK_TYPES_FILE="packages/sdk/typescript/src/types.ts"
SDK_CLIENT_FILE="packages/sdk/typescript/src/client.ts"
PY_TYPES_FILE="packages/sdk/python/src/relayfile/types.py"

require_pattern "$OPENAPI_FILE" "/v1/workspaces/{workspaceId}/fs/query:" "fs query endpoint"
require_pattern "$OPENAPI_FILE" "operationId: queryFiles" "queryFiles operation"
require_pattern "$OPENAPI_FILE" "FileSemantics:" "file semantics schema"
require_pattern "$OPENAPI_FILE" "FileQueryResponse:" "file query response schema"
require_pattern "$OPENAPI_FILE" "propertyCount:" "tree property count"
require_pattern "$OPENAPI_FILE" "relationCount:" "tree relation count"
require_pattern "$OPENAPI_FILE" "permissionCount:" "tree permission count"
require_pattern "$OPENAPI_FILE" "commentCount:" "tree comment count"

require_pattern "$SDK_TYPES_FILE" "export interface FileSemantics" "sdk file semantics type"
require_pattern "$SDK_TYPES_FILE" "export interface QueryFilesOptions" "sdk query options type"
require_pattern "$SDK_TYPES_FILE" "export interface FileQueryResponse" "sdk query response type"
require_pattern "$SDK_TYPES_FILE" "propertyCount?: number;" "sdk tree property count"
require_pattern "$SDK_TYPES_FILE" "permissionCount?: number;" "sdk tree permission count"
require_pattern "$SDK_TYPES_FILE" "semantics?: FileSemantics;" "sdk semantics support"

require_pattern "$SDK_CLIENT_FILE" "async queryFiles(workspaceId: string, options: QueryFilesOptions = {})" "sdk queryFiles method"
require_pattern "$SDK_CLIENT_FILE" "/fs/query" "sdk fs query path"
require_pattern "$SDK_CLIENT_FILE" "semantics: input.semantics" "sdk write semantics body"

# ── Python SDK parity checks ──
# Ensure Python SDK declares the same core types as TypeScript SDK

require_pattern "$PY_TYPES_FILE" "class OperationStatusResponse:" "python OperationStatusResponse"
require_pattern "$PY_TYPES_FILE" "class FilesystemEvent:" "python FilesystemEvent"
require_pattern "$PY_TYPES_FILE" "class EventFeedResponse:" "python EventFeedResponse"
require_pattern "$PY_TYPES_FILE" "class OperationFeedResponse:" "python OperationFeedResponse"
require_pattern "$PY_TYPES_FILE" "class FileQueryResponse:" "python FileQueryResponse"
require_pattern "$PY_TYPES_FILE" "class FileSemantics:" "python FileSemantics"
require_pattern "$PY_TYPES_FILE" "class BulkWriteResponse:" "python BulkWriteResponse"

# ── Cross-SDK field parity: OperationStatusResponse ──
require_pattern "$SDK_TYPES_FILE" "createdAt?" "ts operation createdAt"
require_pattern "$SDK_TYPES_FILE" "updatedAt?" "ts operation updatedAt"
require_pattern "$SDK_TYPES_FILE" "completedAt?" "ts operation completedAt"
require_pattern "$PY_TYPES_FILE" "created_at:" "python operation created_at"
require_pattern "$PY_TYPES_FILE" "updated_at:" "python operation updated_at"
require_pattern "$PY_TYPES_FILE" "completed_at:" "python operation completed_at"

echo "contract check passed"
