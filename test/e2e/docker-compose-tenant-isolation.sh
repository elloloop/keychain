#!/usr/bin/env bash

set -euo pipefail

PROJECT=keychain-e2e-isolation
source "$(dirname "$0")/lib.sh"
trap cleanup EXIT

compose_up

WORKSPACE_A=$(create_workspace "isolation-a" "owner-a")
WORKSPACE_A_ID=$(echo "$WORKSPACE_A" | jq -r '.workspace.workspaceId')
API_A=$(create_api "$WORKSPACE_A_ID" "prod-a" "ck_iso_a_")
API_A_ID=$(echo "$API_A" | jq -r '.api.apiId')

WORKSPACE_B=$(create_workspace "isolation-b" "owner-b")
WORKSPACE_B_ID=$(echo "$WORKSPACE_B" | jq -r '.workspace.workspaceId')
API_B=$(create_api "$WORKSPACE_B_ID" "prod-b" "ck_iso_b_")
API_B_ID=$(echo "$API_B" | jq -r '.api.apiId')

echo "==> create isolated key sets"
KEY_A1=$(create_key_for_api "$API_A_ID" "owner-a" "a-one")
KEY_A1_ID=$(echo "$KEY_A1" | jq -r '.key.keyId')
KEY_A1_PLAINTEXT=$(echo "$KEY_A1" | jq -r '.plaintext')
KEY_A2=$(create_key_for_api "$API_A_ID" "owner-a-alt" "a-two")
KEY_A2_ID=$(echo "$KEY_A2" | jq -r '.key.keyId')
KEY_A2_PLAINTEXT=$(echo "$KEY_A2" | jq -r '.plaintext')
KEY_B1=$(create_key_for_api "$API_B_ID" "owner-b" "b-one")
KEY_B1_ID=$(echo "$KEY_B1" | jq -r '.key.keyId')
KEY_B1_PLAINTEXT=$(echo "$KEY_B1" | jq -r '.plaintext')

assert_nonempty "key a1" "$KEY_A1_ID"
assert_nonempty "key a2" "$KEY_A2_ID"
assert_nonempty "key b1" "$KEY_B1_ID"

echo "==> list keys stays scoped to api and owner"
LIST_A=$(call ListKeys "$(jq -nc --arg a "$API_A_ID" '{apiId:$a,pageSize:10}')")
assert_eq "api a count" "2" "$(echo "$LIST_A" | jq -r '.keys | length')"
assert_eq "api a ids" "$API_A_ID" "$(echo "$LIST_A" | jq -r '[.keys[].apiId] | unique | join(",")')"
assert_eq "api a workspaces" "$WORKSPACE_A_ID" "$(echo "$LIST_A" | jq -r '[.keys[].workspaceId] | unique | join(",")')"

LIST_A_OWNER=$(call ListKeys "$(jq -nc --arg a "$API_A_ID" '{apiId:$a,ownerPrincipalId:"owner-a",pageSize:10}')")
assert_eq "api a owner count" "1" "$(echo "$LIST_A_OWNER" | jq -r '.keys | length')"
assert_eq "api a owner key" "$KEY_A1_ID" "$(echo "$LIST_A_OWNER" | jq -r '.keys[0].keyId')"

LIST_B=$(call ListKeys "$(jq -nc --arg a "$API_B_ID" '{apiId:$a,pageSize:10}')")
assert_eq "api b count" "1" "$(echo "$LIST_B" | jq -r '.keys | length')"
assert_eq "api b key" "$KEY_B1_ID" "$(echo "$LIST_B" | jq -r '.keys[0].keyId')"
assert_eq "api b workspace" "$WORKSPACE_B_ID" "$(echo "$LIST_B" | jq -r '.keys[0].workspaceId')"

echo "==> verify responses report the owning boundary"
VERIFY_A1=$(verify "$KEY_A1_PLAINTEXT")
assert_eq "verify a1 result" "VERIFY_RESULT_VALID" "$(echo "$VERIFY_A1" | jq -r '.result')"
assert_eq "verify a1 api" "$API_A_ID" "$(echo "$VERIFY_A1" | jq -r '.apiId')"
assert_eq "verify a1 workspace" "$WORKSPACE_A_ID" "$(echo "$VERIFY_A1" | jq -r '.workspaceId')"

VERIFY_B1=$(verify "$KEY_B1_PLAINTEXT")
assert_eq "verify b1 result" "VERIFY_RESULT_VALID" "$(echo "$VERIFY_B1" | jq -r '.result')"
assert_eq "verify b1 api" "$API_B_ID" "$(echo "$VERIFY_B1" | jq -r '.apiId')"
assert_eq "verify b1 workspace" "$WORKSPACE_B_ID" "$(echo "$VERIFY_B1" | jq -r '.workspaceId')"

echo "==> revoking and rotating api a keys leaves api b untouched"
call RevokeKey "$(jq -nc --arg k "$KEY_A1_ID" '{keyId:$k}')" >/dev/null
assert_eq "a1 revoked" "VERIFY_RESULT_REVOKED" "$(verify_result "$KEY_A1_PLAINTEXT")"
assert_eq "b1 still valid after revoke" "VERIFY_RESULT_VALID" "$(verify_result "$KEY_B1_PLAINTEXT")"

ROTATE_A2=$(call RotateKey "$(jq -nc --arg k "$KEY_A2_ID" '{keyId:$k}')")
KEY_A2_ROTATED=$(echo "$ROTATE_A2" | jq -r '.plaintext')
assert_eq "a2 old invalid" "VERIFY_RESULT_NOT_FOUND" "$(verify_result "$KEY_A2_PLAINTEXT")"
assert_eq "a2 new valid" "VERIFY_RESULT_VALID" "$(verify_result "$KEY_A2_ROTATED")"
assert_eq "b1 still valid after rotate" "VERIFY_RESULT_VALID" "$(verify_result "$KEY_B1_PLAINTEXT")"

LIST_A_AFTER=$(call ListKeys "$(jq -nc --arg a "$API_A_ID" '{apiId:$a,pageSize:10}')")
assert_eq "api a count after mutations" "2" "$(echo "$LIST_A_AFTER" | jq -r '.keys | length')"
LIST_B_AFTER=$(call ListKeys "$(jq -nc --arg a "$API_B_ID" '{apiId:$a,pageSize:10}')")
assert_eq "api b count after mutations" "1" "$(echo "$LIST_B_AFTER" | jq -r '.keys | length')"

echo "==> docker compose tenant isolation e2e passed"
