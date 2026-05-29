#!/usr/bin/env bash
#
# docker-compose-critical-rpcs.sh — boots the full keychain stack (postgres
# + keychain container) via docker-compose and walks the critical RPC path:
# CreateWorkspace -> CreateApi -> CreateKey -> VerifyKey VALID ->
# RotateKey -> VerifyKey on old plaintext NOT_FOUND -> VerifyKey on new
# plaintext VALID -> RevokeKey -> VerifyKey REVOKED. Requires grpcurl
# (gRPC reflection client) and jq (JSON parsing) on the host.

set -euo pipefail

cd "$(dirname "$0")/../.."

PROJECT=keychain-e2e
TARGET=localhost:28080

cleanup() {
  echo "==> tearing down"
  docker compose -p "$PROJECT" -f docker-compose.yml down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> docker compose up -d --build"
docker compose -p "$PROJECT" -f docker-compose.yml up -d --build >/dev/null

echo "==> waiting for keychain gRPC reflection ..."
for i in $(seq 1 60); do
  if grpcurl -plaintext "$TARGET" list >/dev/null 2>&1; then
    echo "==> ready after ${i}s"
    break
  fi
  if [ "$i" -eq 60 ]; then
    echo "keychain did not come up"
    docker compose -p "$PROJECT" -f docker-compose.yml logs keychain
    exit 1
  fi
  sleep 1
done

call() {
  local method=$1
  local body=$2
  grpcurl -plaintext -format json -d "$body" "$TARGET" "apikey.v1.ApiKeyService/$method"
}

assert_eq() {
  local label=$1 expected=$2 actual=$3
  if [ "$expected" != "$actual" ]; then
    echo "FAIL $label: expected $expected, got $actual"
    exit 1
  fi
}

echo "==> CreateWorkspace"
WORKSPACE=$(call CreateWorkspace '{"name":"e2e","ownerPrincipalId":"e2e_owner"}')
WORKSPACE_ID=$(echo "$WORKSPACE" | jq -r '.workspace.workspaceId')
echo "    workspace_id=$WORKSPACE_ID"
[ -n "$WORKSPACE_ID" ] && [ "$WORKSPACE_ID" != "null" ] || { echo "missing workspace_id"; exit 1; }

echo "==> CreateApi"
API=$(call CreateApi "$(jq -nc --arg w "$WORKSPACE_ID" '{workspaceId:$w,name:"prod",keyPrefix:"ck_e2e_"}')")
API_ID=$(echo "$API" | jq -r '.api.apiId')
echo "    api_id=$API_ID"
[ -n "$API_ID" ] && [ "$API_ID" != "null" ] || { echo "missing api_id"; exit 1; }

echo "==> CreateKey"
KEY=$(call CreateKey "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"e2e_user",name:"e2e-key"}')")
KEY_ID=$(echo "$KEY" | jq -r '.key.keyId')
PLAINTEXT=$(echo "$KEY" | jq -r '.plaintext')
echo "    key_id=$KEY_ID"
echo "    plaintext=${PLAINTEXT:0:16}..."
[ -n "$KEY_ID" ] && [ "$KEY_ID" != "null" ] || { echo "missing key_id"; exit 1; }
[ -n "$PLAINTEXT" ] && [ "$PLAINTEXT" != "null" ] || { echo "missing plaintext"; exit 1; }

echo "==> VerifyKey (expect VALID)"
VERIFY=$(call VerifyKey "$(jq -nc --arg p "$PLAINTEXT" '{plaintext:$p}')")
assert_eq "verify.result" "VERIFY_RESULT_VALID" "$(echo "$VERIFY" | jq -r '.result')"

echo "==> VerifyKey on junk plaintext (expect NOT_FOUND)"
JUNK=$(call VerifyKey '{"plaintext":"ck_e2e_clearly_not_a_real_key"}')
assert_eq "junk.result" "VERIFY_RESULT_NOT_FOUND" "$(echo "$JUNK" | jq -r '.result')"

echo "==> RotateKey"
ROTATE=$(call RotateKey "$(jq -nc --arg k "$KEY_ID" '{keyId:$k}')")
NEW_PLAINTEXT=$(echo "$ROTATE" | jq -r '.plaintext')
[ -n "$NEW_PLAINTEXT" ] && [ "$NEW_PLAINTEXT" != "$PLAINTEXT" ] || { echo "rotate did not change plaintext"; exit 1; }
echo "    new plaintext issued"

echo "==> VerifyKey on old plaintext (expect NOT_FOUND)"
OLD=$(call VerifyKey "$(jq -nc --arg p "$PLAINTEXT" '{plaintext:$p}')")
assert_eq "old.result" "VERIFY_RESULT_NOT_FOUND" "$(echo "$OLD" | jq -r '.result')"

echo "==> VerifyKey on new plaintext (expect VALID)"
NEW=$(call VerifyKey "$(jq -nc --arg p "$NEW_PLAINTEXT" '{plaintext:$p}')")
assert_eq "new.result" "VERIFY_RESULT_VALID" "$(echo "$NEW" | jq -r '.result')"

echo "==> RevokeKey"
call RevokeKey "$(jq -nc --arg k "$KEY_ID" '{keyId:$k}')" >/dev/null

echo "==> VerifyKey after revoke (expect REVOKED)"
REVOKED=$(call VerifyKey "$(jq -nc --arg p "$NEW_PLAINTEXT" '{plaintext:$p}')")
assert_eq "revoked.result" "VERIFY_RESULT_REVOKED" "$(echo "$REVOKED" | jq -r '.result')"

echo "==> docker compose critical RPC e2e passed"
