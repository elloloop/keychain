#!/usr/bin/env bash
#
# Full-stack admin RPC error contracts: required fields, unknown parents,
# unknown resources, idempotent revoke, and malformed verify inputs.

set -euo pipefail

PROJECT=keychain-e2e-admin-errors
source "$(dirname "$0")/lib.sh"
trap cleanup EXIT

compose_up

echo "==> required field errors"
assert_grpc_error "CreateWorkspace missing name" CreateWorkspace '{"ownerPrincipalId":"owner"}' InvalidArgument "name is required"
assert_grpc_error "CreateWorkspace missing owner" CreateWorkspace '{"name":"acme"}' InvalidArgument "owner_principal_id is required"
assert_grpc_error "GetWorkspace missing id" GetWorkspace '{}' InvalidArgument "workspace_id is required"
assert_grpc_error "CreateApi missing workspace" CreateApi '{"name":"prod"}' InvalidArgument "workspace_id is required"
assert_grpc_error "CreateApi missing name" CreateApi '{"workspaceId":"ws_missing"}' InvalidArgument "name is required"
assert_grpc_error "GetApi missing id" GetApi '{}' InvalidArgument "api_id is required"
assert_grpc_error "CreateKey missing api" CreateKey '{"ownerPrincipalId":"owner"}' InvalidArgument "api_id is required"
assert_grpc_error "CreateKey missing owner" CreateKey '{"apiId":"api_missing"}' InvalidArgument "owner_principal_id is required"
assert_grpc_error "GetKey missing id" GetKey '{}' InvalidArgument "key_id is required"
assert_grpc_error "RevokeKey missing id" RevokeKey '{}' InvalidArgument "key_id is required"
assert_grpc_error "RotateKey missing id" RotateKey '{}' InvalidArgument "key_id is required"
assert_grpc_error "ListKeys missing api" ListKeys '{}' InvalidArgument "api_id is required"

echo "==> not found errors"
assert_grpc_error "GetWorkspace unknown" GetWorkspace '{"workspaceId":"ws_missing"}' NotFound "store: not found"
assert_grpc_error "CreateApi unknown workspace" CreateApi '{"workspaceId":"ws_missing","name":"prod"}' NotFound "store: not found"
assert_grpc_error "GetApi unknown" GetApi '{"apiId":"api_missing"}' NotFound "store: not found"
assert_grpc_error "CreateKey unknown api" CreateKey '{"apiId":"api_missing","ownerPrincipalId":"owner"}' NotFound "store: not found"
assert_grpc_error "GetKey unknown" GetKey '{"keyId":"key_missing"}' NotFound "store: not found"
assert_grpc_error "RevokeKey unknown" RevokeKey '{"keyId":"key_missing"}' NotFound "store: not found"
assert_grpc_error "RotateKey unknown" RotateKey '{"keyId":"key_missing"}' NotFound "store: not found"

echo "==> verify malformed and unknown plaintext report response data"
EMPTY_VERIFY=$(call VerifyKey '{}')
assert_eq "empty verify status result" "VERIFY_RESULT_NOT_FOUND" "$(echo "$EMPTY_VERIFY" | jq -r '.result')"
assert_eq "empty verify valid flag" "false" "$(echo "$EMPTY_VERIFY" | jq -r '(.valid // false)')"
JUNK_VERIFY=$(call VerifyKey '{"plaintext":"ck_e2e_not_real"}')
assert_eq "junk verify result" "VERIFY_RESULT_NOT_FOUND" "$(echo "$JUNK_VERIFY" | jq -r '.result')"
assert_eq "junk verify key id omitted" "" "$(echo "$JUNK_VERIFY" | jq -r '.keyId // ""')"

echo "==> successful resources make revoke idempotent"
WORKSPACE=$(create_workspace "admin-errors" "owner")
WORKSPACE_ID=$(echo "$WORKSPACE" | jq -r '.workspace.workspaceId')
API=$(create_api "$WORKSPACE_ID" "prod" "ck_admin_")
API_ID=$(echo "$API" | jq -r '.api.apiId')
KEY=$(create_key_for_api "$API_ID" "owner" "revoke-me")
KEY_ID=$(echo "$KEY" | jq -r '.key.keyId')
PLAINTEXT=$(echo "$KEY" | jq -r '.plaintext')
assert_nonempty "key id" "$KEY_ID"
assert_eq "pre-revoke verify" "VERIFY_RESULT_VALID" "$(verify_result "$PLAINTEXT")"
call RevokeKey "$(jq -nc --arg k "$KEY_ID" '{keyId:$k}')" >/dev/null
call RevokeKey "$(jq -nc --arg k "$KEY_ID" '{keyId:$k}')" >/dev/null
assert_eq "post-revoke verify" "VERIFY_RESULT_REVOKED" "$(verify_result "$PLAINTEXT")"

echo "==> docker compose admin error e2e passed"
