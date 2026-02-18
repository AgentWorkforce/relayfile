#!/usr/bin/env bash

set -euo pipefail

require_pattern() {
  local file="$1"
  local pattern="$2"
  local label="$3"

  if ! rg --quiet --fixed-strings "$pattern" "$file"; then
    echo "contract check failed: missing '$label' in $file"
    exit 1
  fi
}

OPENAPI_FILE="openapi/relayfile-v1.openapi.yaml"
SDK_TYPES_FILE="sdk/relayfile-sdk/src/types.ts"
SDK_CLIENT_FILE="sdk/relayfile-sdk/src/client.ts"

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

echo "contract check passed"
