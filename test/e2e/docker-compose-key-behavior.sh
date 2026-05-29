#!/usr/bin/env bash
#
# Full-stack API-key behavior checks inspired by mature API-key platforms:
# usage limits, permission checks, expiration, rate-limit decisions,
# pagination, concurrent single-use verification, and Postgres persistence.

set -euo pipefail

PROJECT=keychain-e2e-behavior
source "$(dirname "$0")/lib.sh"

TMP_DIR=$(mktemp -d)
cleanup_all() {
  rm -rf "$TMP_DIR"
  cleanup
}
trap cleanup_all EXIT

create_key() {
  local body=$1
  call CreateKey "$body"
}

verify_result() {
  local plaintext=$1
  local body=${2:-}
  if [ -z "$body" ]; then
    body=$(jq -nc --arg p "$plaintext" '{plaintext:$p}')
  fi
  call VerifyKey "$body" | jq -r '.result'
}

compose_up

echo "==> metrics health"
HEALTH=$(curl -fsS "http://$METRICS_TARGET/healthz")
assert_eq "healthz.status" "ok" "$(echo "$HEALTH" | jq -r '.status')"

echo "==> CreateWorkspace"
WORKSPACE=$(call CreateWorkspace '{"name":"e2e-behavior","ownerPrincipalId":"e2e_owner","metadata":{"suite":"behavior"}}')
WORKSPACE_ID=$(echo "$WORKSPACE" | jq -r '.workspace.workspaceId')
assert_nonempty "workspace_id" "$WORKSPACE_ID"

echo "==> CreateApi"
API=$(call CreateApi "$(jq -nc --arg w "$WORKSPACE_ID" '{workspaceId:$w,name:"prod",keyPrefix:"ck_e2e_behavior_",metadata:{env:"test"}}')")
API_ID=$(echo "$API" | jq -r '.api.apiId')
assert_nonempty "api_id" "$API_ID"

echo "==> permission and metadata round trip"
PERM_KEY=$(create_key "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"e2e_permissions",name:"permission-key",permissions:["chat:read","chat:write"],metadata:{plan:"pro"}}')")
PERM_KEY_ID=$(echo "$PERM_KEY" | jq -r '.key.keyId')
PERM_PLAINTEXT=$(echo "$PERM_KEY" | jq -r '.plaintext')
assert_nonempty "permission key id" "$PERM_KEY_ID"
assert_nonempty "permission plaintext" "$PERM_PLAINTEXT"

PERM_GET=$(call GetKey "$(jq -nc --arg k "$PERM_KEY_ID" '{keyId:$k}')")
assert_eq "permission metadata" "pro" "$(echo "$PERM_GET" | jq -r '.key.metadata.plan')"
assert_eq "permission count" "2" "$(echo "$PERM_GET" | jq -r '.key.permissions | length')"
assert_eq "get key plaintext leak" "false" "$(echo "$PERM_GET" | jq 'has("plaintext")')"

PERM_VALID=$(call VerifyKey "$(jq -nc --arg p "$PERM_PLAINTEXT" '{plaintext:$p,requiredPermissions:["chat:write"]}')")
assert_eq "permission valid result" "VERIFY_RESULT_VALID" "$(echo "$PERM_VALID" | jq -r '.result')"

PERM_FORBIDDEN=$(call VerifyKey "$(jq -nc --arg p "$PERM_PLAINTEXT" '{plaintext:$p,requiredPermissions:["admin:write"]}')")
assert_eq "permission forbidden result" "VERIFY_RESULT_FORBIDDEN" "$(echo "$PERM_FORBIDDEN" | jq -r '.result')"
assert_eq "permission forbidden valid flag" "false" "$(echo "$PERM_FORBIDDEN" | jq -r '(.valid // false)')"

echo "==> usage-limited key depletes after one verify"
LIMITED_KEY=$(create_key "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"e2e_limited",name:"single-use",remainingUses:1}')")
LIMITED_KEY_ID=$(echo "$LIMITED_KEY" | jq -r '.key.keyId')
LIMITED_PLAINTEXT=$(echo "$LIMITED_KEY" | jq -r '.plaintext')
assert_eq "limited first verify" "VERIFY_RESULT_VALID" "$(verify_result "$LIMITED_PLAINTEXT")"
assert_eq "limited second verify" "VERIFY_RESULT_DEPLETED" "$(verify_result "$LIMITED_PLAINTEXT")"
LIMITED_GET=$(call GetKey "$(jq -nc --arg k "$LIMITED_KEY_ID" '{keyId:$k}')")
assert_eq "limited remaining uses" "0" "$(echo "$LIMITED_GET" | jq -r '(.key.remainingUses // 0)')"
assert_nonempty "limited last_verified_at" "$(echo "$LIMITED_GET" | jq -r '.key.lastVerifiedAt // empty')"

echo "==> concurrent single-use verify admits only one request"
CONCURRENT_KEY=$(create_key "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"e2e_concurrent",name:"single-use-concurrent",remainingUses:1}')")
CONCURRENT_PLAINTEXT=$(echo "$CONCURRENT_KEY" | jq -r '.plaintext')
CONCURRENT_BODY=$(jq -nc --arg p "$CONCURRENT_PLAINTEXT" '{plaintext:$p}')
(call VerifyKey "$CONCURRENT_BODY" > "$TMP_DIR/concurrent-1.json") &
pid1=$!
(call VerifyKey "$CONCURRENT_BODY" > "$TMP_DIR/concurrent-2.json") &
pid2=$!
wait "$pid1"
wait "$pid2"
CONCURRENT_RESULTS=$(jq -r '.result' "$TMP_DIR"/concurrent-*.json | sort | tr '\n' ' ')
assert_eq "concurrent results" "VERIFY_RESULT_DEPLETED VERIFY_RESULT_VALID " "$CONCURRENT_RESULTS"

echo "==> expired key rejects verification"
EXPIRED_KEY=$(create_key "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"e2e_expired",name:"expired",expiresAt:"2000-01-01T00:00:00Z"}')")
EXPIRED_PLAINTEXT=$(echo "$EXPIRED_KEY" | jq -r '.plaintext')
assert_eq "expired verify" "VERIFY_RESULT_EXPIRED" "$(verify_result "$EXPIRED_PLAINTEXT")"

echo "==> limit refs fail closed through docker-compose service wiring"
LIMIT_KEY=$(create_key "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"e2e_limit",name:"limited-by-ref",limitRefs:[{limitId:"requests",scopeKey:"workspace:e2e"}]}')")
LIMIT_PLAINTEXT=$(echo "$LIMIT_KEY" | jq -r '.plaintext')
LIMIT_VERIFY=$(call VerifyKey "$(jq -nc --arg p "$LIMIT_PLAINTEXT" '{plaintext:$p,cost:3,requestId:"e2e-rate-limit"}')")
assert_eq "limit result" "VERIFY_RESULT_RATE_LIMITED" "$(echo "$LIMIT_VERIFY" | jq -r '.result')"
assert_eq "limit decision allowed" "false" "$(echo "$LIMIT_VERIFY" | jq -r '(.limitDecisions[0].allowed // false)')"
assert_eq "limit decision id" "requests" "$(echo "$LIMIT_VERIFY" | jq -r '.limitDecisions[0].limitId')"

echo "==> ListKeys paginates and filters by owner"
for i in $(seq 1 5); do
  create_key "$(jq -nc --arg a "$API_ID" --arg name "page-$i" '{apiId:$a,ownerPrincipalId:"e2e_page",name:$name}')" >/dev/null
done
PAGE1=$(call ListKeys "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"e2e_page",pageSize:2}')")
assert_eq "page1 count" "2" "$(echo "$PAGE1" | jq -r '.keys | length')"
PAGE_TOKEN=$(echo "$PAGE1" | jq -r '.nextPageToken')
assert_nonempty "page token" "$PAGE_TOKEN"
PAGE2=$(call ListKeys "$(jq -nc --arg a "$API_ID" --arg t "$PAGE_TOKEN" '{apiId:$a,ownerPrincipalId:"e2e_page",pageSize:10,pageToken:$t}')")
assert_eq "page2 count" "3" "$(echo "$PAGE2" | jq -r '.keys | length')"
assert_eq "page2 final token" "" "$(echo "$PAGE2" | jq -r '.nextPageToken // ""')"

echo "==> Postgres-backed keys survive keychain restart"
PERSIST_KEY=$(create_key "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"e2e_persist",name:"persist"}')")
PERSIST_PLAINTEXT=$(echo "$PERSIST_KEY" | jq -r '.plaintext')
assert_eq "persist before restart" "VERIFY_RESULT_VALID" "$(verify_result "$PERSIST_PLAINTEXT")"
restart_keychain
assert_eq "persist after restart" "VERIFY_RESULT_VALID" "$(verify_result "$PERSIST_PLAINTEXT")"

echo "==> docker compose key behavior e2e passed"
