#!/usr/bin/env bash
#
# Full-stack lifecycle invariants: plaintext appears only at issuance and
# rotation, rotation preserves metadata and policy, and list/get/verify never
# echo bearer secrets.

set -euo pipefail

PROJECT=keychain-e2e-lifecycle
source "$(dirname "$0")/lib.sh"
trap cleanup EXIT

compose_up

WORKSPACE=$(call CreateWorkspace '{"name":"lifecycle","ownerPrincipalId":"owner","metadata":{"suite":"lifecycle"}}')
WORKSPACE_ID=$(echo "$WORKSPACE" | jq -r '.workspace.workspaceId')
API=$(call CreateApi "$(jq -nc --arg w "$WORKSPACE_ID" '{workspaceId:$w,name:"prod",keyPrefix:"ck_lifecycle_",metadata:{kind:"primary"}}')")
API_ID=$(echo "$API" | jq -r '.api.apiId')

echo "==> API and workspace metadata round trip"
WORKSPACE_GET=$(call GetWorkspace "$(jq -nc --arg w "$WORKSPACE_ID" '{workspaceId:$w}')")
assert_eq "workspace metadata" "lifecycle" "$(echo "$WORKSPACE_GET" | jq -r '.workspace.metadata.suite')"
API_GET=$(call GetApi "$(jq -nc --arg a "$API_ID" '{apiId:$a}')")
assert_eq "api prefix" "ck_lifecycle_" "$(echo "$API_GET" | jq -r '.api.keyPrefix')"
assert_eq "api metadata" "primary" "$(echo "$API_GET" | jq -r '.api.metadata.kind')"

echo "==> create response is the only initial plaintext carrier"
KEY=$(call CreateKey "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"owner",name:"rotating",permissions:["read","write"],remainingUses:3,metadata:{team:"platform"}}')")
KEY_ID=$(echo "$KEY" | jq -r '.key.keyId')
PLAINTEXT=$(echo "$KEY" | jq -r '.plaintext')
assert_nonempty "plaintext" "$PLAINTEXT"
assert_eq "plaintext prefix" "ck_lifecycle_" "${PLAINTEXT:0:13}"
GET_BEFORE=$(call GetKey "$(jq -nc --arg k "$KEY_ID" '{keyId:$k}')")
LIST_BEFORE=$(call ListKeys "$(jq -nc --arg a "$API_ID" '{apiId:$a,pageSize:10}')")
VERIFY_BEFORE=$(verify "$PLAINTEXT")
assert_eq "get plaintext omitted" "false" "$(echo "$GET_BEFORE" | jq 'has("plaintext")')"
assert_eq "list plaintext omitted" "false" "$(echo "$LIST_BEFORE" | jq '.keys[0] | has("plaintext")')"
assert_eq "verify plaintext omitted" "false" "$(echo "$VERIFY_BEFORE" | jq 'has("plaintext")')"
assert_eq "metadata before rotate" "platform" "$(echo "$GET_BEFORE" | jq -r '.key.metadata.team')"
assert_eq "remaining after first valid verify" "2" "$(call GetKey "$(jq -nc --arg k "$KEY_ID" '{keyId:$k}')" | jq -r '.key.remainingUses')"

echo "==> rotation issues fresh plaintext and preserves key policy"
ROTATE=$(call RotateKey "$(jq -nc --arg k "$KEY_ID" '{keyId:$k}')")
ROTATED_KEY_ID=$(echo "$ROTATE" | jq -r '.key.keyId')
NEW_PLAINTEXT=$(echo "$ROTATE" | jq -r '.plaintext')
assert_eq "rotated key id stable" "$KEY_ID" "$ROTATED_KEY_ID"
if [ "$NEW_PLAINTEXT" = "$PLAINTEXT" ]; then
  echo "FAIL rotate plaintext: expected fresh plaintext"
  exit 1
fi
assert_eq "old plaintext invalidated" "VERIFY_RESULT_NOT_FOUND" "$(verify_result "$PLAINTEXT")"
assert_eq "new plaintext valid" "VERIFY_RESULT_VALID" "$(verify_result "$NEW_PLAINTEXT")"
GET_AFTER=$(call GetKey "$(jq -nc --arg k "$KEY_ID" '{keyId:$k}')")
assert_eq "metadata after rotate" "platform" "$(echo "$GET_AFTER" | jq -r '.key.metadata.team')"
assert_eq "permissions after rotate" "read,write" "$(echo "$GET_AFTER" | jq -r '.key.permissions | join(",")')"
assert_eq "remaining after rotated valid verify" "1" "$(echo "$GET_AFTER" | jq -r '.key.remainingUses')"
assert_eq "get after rotate plaintext omitted" "false" "$(echo "$GET_AFTER" | jq 'has("plaintext")')"

echo "==> revoked rotated key remains auditable but invalid"
call RevokeKey "$(jq -nc --arg k "$KEY_ID" '{keyId:$k}')" >/dev/null
REVOKED=$(verify "$NEW_PLAINTEXT")
assert_eq "revoked result" "VERIFY_RESULT_REVOKED" "$(echo "$REVOKED" | jq -r '.result')"
assert_eq "revoked response key id" "$KEY_ID" "$(echo "$REVOKED" | jq -r '.keyId')"
assert_eq "revoked response plaintext omitted" "false" "$(echo "$REVOKED" | jq 'has("plaintext")')"
GET_REVOKED=$(call GetKey "$(jq -nc --arg k "$KEY_ID" '{keyId:$k}')")
assert_eq "revoked enabled flag" "false" "$(echo "$GET_REVOKED" | jq -r '(.key.enabled // false)')"
assert_eq "revoked remaining unchanged" "1" "$(echo "$GET_REVOKED" | jq -r '.key.remainingUses')"

echo "==> docker compose lifecycle privacy e2e passed"
