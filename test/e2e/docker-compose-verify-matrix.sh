#!/usr/bin/env bash
#
# Full-stack VerifyKey decision matrix. This keeps the hot-path response
# contract stable across found/not-found, valid, forbidden, depleted, expired,
# and rate-limited outcomes.

set -euo pipefail

PROJECT=keychain-e2e-verify-matrix
source "$(dirname "$0")/lib.sh"
trap cleanup EXIT

create_key() {
  local body=$1
  call CreateKey "$body"
}

compose_up

WORKSPACE=$(create_workspace "verify-matrix" "owner")
WORKSPACE_ID=$(echo "$WORKSPACE" | jq -r '.workspace.workspaceId')
API=$(create_api "$WORKSPACE_ID" "prod" "ck_matrix_")
API_ID=$(echo "$API" | jq -r '.api.apiId')

assert_found_identity() {
  local label=$1
  local body=$2
  local key_id=$3
  assert_eq "$label key id" "$key_id" "$(echo "$body" | jq -r '.keyId')"
  assert_eq "$label api id" "$API_ID" "$(echo "$body" | jq -r '.apiId')"
  assert_eq "$label workspace id" "$WORKSPACE_ID" "$(echo "$body" | jq -r '.workspaceId')"
}

echo "==> not found responses omit identity"
MALFORMED=$(call VerifyKey '{"plaintext":"not-a-key"}')
assert_eq "malformed result" "VERIFY_RESULT_NOT_FOUND" "$(echo "$MALFORMED" | jq -r '.result')"
assert_eq "malformed valid" "false" "$(echo "$MALFORMED" | jq -r '(.valid // false)')"
assert_eq "malformed key id omitted" "" "$(echo "$MALFORMED" | jq -r '.keyId // ""')"

UNKNOWN=$(call VerifyKey '{"plaintext":"ck_matrix_missing"}')
assert_eq "unknown result" "VERIFY_RESULT_NOT_FOUND" "$(echo "$UNKNOWN" | jq -r '.result')"
assert_eq "unknown api id omitted" "" "$(echo "$UNKNOWN" | jq -r '.apiId // ""')"

echo "==> valid responses carry identity, permissions, and no plaintext"
VALID_KEY=$(create_key "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"matrix_valid",name:"valid",permissions:["read","write"],remainingUses:2}')")
VALID_KEY_ID=$(echo "$VALID_KEY" | jq -r '.key.keyId')
VALID_PLAINTEXT=$(echo "$VALID_KEY" | jq -r '.plaintext')
VALID=$(call VerifyKey "$(jq -nc --arg p "$VALID_PLAINTEXT" '{plaintext:$p,requiredPermissions:["write"]}')")
assert_eq "valid result" "VERIFY_RESULT_VALID" "$(echo "$VALID" | jq -r '.result')"
assert_eq "valid flag" "true" "$(echo "$VALID" | jq -r '.valid')"
assert_found_identity "valid" "$VALID" "$VALID_KEY_ID"
assert_eq "valid permissions" "read,write" "$(echo "$VALID" | jq -r '.permissions | join(",")')"
assert_eq "valid plaintext omitted" "false" "$(echo "$VALID" | jq 'has("plaintext")')"

echo "==> forbidden responses preserve credit and omit last_verified_at"
FORBIDDEN_KEY=$(create_key "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"matrix_forbidden",name:"forbidden",permissions:["read"],remainingUses:2}')")
FORBIDDEN_KEY_ID=$(echo "$FORBIDDEN_KEY" | jq -r '.key.keyId')
FORBIDDEN_PLAINTEXT=$(echo "$FORBIDDEN_KEY" | jq -r '.plaintext')
FORBIDDEN=$(call VerifyKey "$(jq -nc --arg p "$FORBIDDEN_PLAINTEXT" '{plaintext:$p,requiredPermissions:["write"]}')")
assert_eq "forbidden result" "VERIFY_RESULT_FORBIDDEN" "$(echo "$FORBIDDEN" | jq -r '.result')"
assert_eq "forbidden valid" "false" "$(echo "$FORBIDDEN" | jq -r '(.valid // false)')"
assert_found_identity "forbidden" "$FORBIDDEN" "$FORBIDDEN_KEY_ID"
FORBIDDEN_GET=$(call GetKey "$(jq -nc --arg k "$FORBIDDEN_KEY_ID" '{keyId:$k}')")
assert_eq "forbidden remaining unchanged" "2" "$(echo "$FORBIDDEN_GET" | jq -r '.key.remainingUses')"
assert_eq "forbidden last verified omitted" "" "$(echo "$FORBIDDEN_GET" | jq -r '.key.lastVerifiedAt // ""')"

echo "==> depleted responses preserve found identity"
DEPLETED_KEY=$(create_key "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"matrix_depleted",name:"depleted",remainingUses:1}')")
DEPLETED_KEY_ID=$(echo "$DEPLETED_KEY" | jq -r '.key.keyId')
DEPLETED_PLAINTEXT=$(echo "$DEPLETED_KEY" | jq -r '.plaintext')
assert_eq "depleted setup first verify" "VERIFY_RESULT_VALID" "$(verify_result "$DEPLETED_PLAINTEXT")"
DEPLETED=$(verify "$DEPLETED_PLAINTEXT")
assert_eq "depleted result" "VERIFY_RESULT_DEPLETED" "$(echo "$DEPLETED" | jq -r '.result')"
assert_found_identity "depleted" "$DEPLETED" "$DEPLETED_KEY_ID"

echo "==> expired responses preserve found identity without spending credit"
EXPIRED_KEY=$(create_key "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"matrix_expired",name:"expired",expiresAt:"2000-01-01T00:00:00Z",remainingUses:2}')")
EXPIRED_KEY_ID=$(echo "$EXPIRED_KEY" | jq -r '.key.keyId')
EXPIRED_PLAINTEXT=$(echo "$EXPIRED_KEY" | jq -r '.plaintext')
EXPIRED=$(verify "$EXPIRED_PLAINTEXT")
assert_eq "expired result" "VERIFY_RESULT_EXPIRED" "$(echo "$EXPIRED" | jq -r '.result')"
assert_found_identity "expired" "$EXPIRED" "$EXPIRED_KEY_ID"
EXPIRED_GET=$(call GetKey "$(jq -nc --arg k "$EXPIRED_KEY_ID" '{keyId:$k}')")
assert_eq "expired remaining unchanged" "2" "$(echo "$EXPIRED_GET" | jq -r '.key.remainingUses')"
assert_eq "expired last verified omitted" "" "$(echo "$EXPIRED_GET" | jq -r '.key.lastVerifiedAt // ""')"

echo "==> rate-limited responses return all limit decisions without spending credit"
LIMITED_KEY=$(create_key "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"matrix_limited",name:"limited",remainingUses:2,limitRefs:[{limitId:"requests",scopeKey:"workspace:matrix"},{limitId:"tokens",scopeKey:"user:matrix"}]}')")
LIMITED_KEY_ID=$(echo "$LIMITED_KEY" | jq -r '.key.keyId')
LIMITED_PLAINTEXT=$(echo "$LIMITED_KEY" | jq -r '.plaintext')
LIMITED=$(call VerifyKey "$(jq -nc --arg p "$LIMITED_PLAINTEXT" '{plaintext:$p,cost:7,requestId:"verify-matrix"}')")
assert_eq "limited result" "VERIFY_RESULT_RATE_LIMITED" "$(echo "$LIMITED" | jq -r '.result')"
assert_eq "limited decisions count" "2" "$(echo "$LIMITED" | jq -r '.limitDecisions | length')"
assert_eq "limited decision ids" "requests,tokens" "$(echo "$LIMITED" | jq -r '.limitDecisions | map(.limitId) | join(",")')"
assert_found_identity "limited" "$LIMITED" "$LIMITED_KEY_ID"
LIMITED_GET=$(call GetKey "$(jq -nc --arg k "$LIMITED_KEY_ID" '{keyId:$k}')")
assert_eq "limited remaining unchanged" "2" "$(echo "$LIMITED_GET" | jq -r '.key.remainingUses')"
assert_eq "limited last verified omitted" "" "$(echo "$LIMITED_GET" | jq -r '.key.lastVerifiedAt // ""')"

echo "==> skipRatelimit bypasses decisions but still records a valid verify"
SKIP=$(call VerifyKey "$(jq -nc --arg p "$LIMITED_PLAINTEXT" '{plaintext:$p,skipRatelimit:true,cost:7,requestId:"verify-matrix-skip"}')")
assert_eq "skip result" "VERIFY_RESULT_VALID" "$(echo "$SKIP" | jq -r '.result')"
assert_eq "skip decisions omitted" "0" "$(echo "$SKIP" | jq -r '(.limitDecisions // []) | length')"
LIMITED_AFTER_SKIP=$(call GetKey "$(jq -nc --arg k "$LIMITED_KEY_ID" '{keyId:$k}')")
assert_eq "skip remaining spent" "1" "$(echo "$LIMITED_AFTER_SKIP" | jq -r '.key.remainingUses')"
assert_nonempty "skip last_verified_at" "$(echo "$LIMITED_AFTER_SKIP" | jq -r '.key.lastVerifiedAt // empty')"

echo "==> docker compose verify matrix e2e passed"
