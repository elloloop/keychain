#!/usr/bin/env bash
#
# Full-stack usage limit pressure: sequential depletion, denied requests not
# spending credit, and concurrent verification against Postgres atomic updates.

set -euo pipefail

PROJECT=keychain-e2e-usage
source "$(dirname "$0")/lib.sh"

TMP_DIR=$(mktemp -d)
cleanup_all() {
  rm -rf "$TMP_DIR"
  cleanup
}
trap cleanup_all EXIT

count_result() {
  local result=$1
  jq -r '.result' "$TMP_DIR"/"$2"-*.json | grep -c "^${result}$" || true
}

compose_up

WORKSPACE=$(create_workspace "usage" "owner")
WORKSPACE_ID=$(echo "$WORKSPACE" | jq -r '.workspace.workspaceId')
API=$(create_api "$WORKSPACE_ID" "prod" "ck_usage_")
API_ID=$(echo "$API" | jq -r '.api.apiId')

echo "==> sequential key spends one use per valid verify"
SEQ_KEY=$(call CreateKey "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"usage_seq",name:"three-use",remainingUses:3}')")
SEQ_KEY_ID=$(echo "$SEQ_KEY" | jq -r '.key.keyId')
SEQ_PLAINTEXT=$(echo "$SEQ_KEY" | jq -r '.plaintext')
for i in 1 2 3; do
  assert_eq "sequential verify $i" "VERIFY_RESULT_VALID" "$(verify_result "$SEQ_PLAINTEXT")"
done
assert_eq "sequential depleted" "VERIFY_RESULT_DEPLETED" "$(verify_result "$SEQ_PLAINTEXT")"
SEQ_GET=$(call GetKey "$(jq -nc --arg k "$SEQ_KEY_ID" '{keyId:$k}')")
assert_eq "sequential remaining" "0" "$(echo "$SEQ_GET" | jq -r '(.key.remainingUses // 0)')"

echo "==> explicit zero remaining uses is unlimited at the API boundary"
ZERO_KEY=$(call CreateKey "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"usage_zero",name:"zero-means-unlimited",remainingUses:0}')")
ZERO_KEY_ID=$(echo "$ZERO_KEY" | jq -r '.key.keyId')
ZERO_PLAINTEXT=$(echo "$ZERO_KEY" | jq -r '.plaintext')
for i in 1 2 3; do
  assert_eq "zero unlimited verify $i" "VERIFY_RESULT_VALID" "$(verify_result "$ZERO_PLAINTEXT")"
done
ZERO_GET=$(call GetKey "$(jq -nc --arg k "$ZERO_KEY_ID" '{keyId:$k}')")
assert_eq "zero remaining stored unlimited" "-1" "$(echo "$ZERO_GET" | jq -r '.key.remainingUses')"

echo "==> forbidden checks do not spend remaining uses"
FORBIDDEN_KEY=$(call CreateKey "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"usage_forbidden",name:"forbidden-credit",permissions:["read"],remainingUses:2}')")
FORBIDDEN_KEY_ID=$(echo "$FORBIDDEN_KEY" | jq -r '.key.keyId')
FORBIDDEN_PLAINTEXT=$(echo "$FORBIDDEN_KEY" | jq -r '.plaintext')
for i in 1 2 3; do
  DENIED=$(call VerifyKey "$(jq -nc --arg p "$FORBIDDEN_PLAINTEXT" '{plaintext:$p,requiredPermissions:["write"]}')")
  assert_eq "forbidden result $i" "VERIFY_RESULT_FORBIDDEN" "$(echo "$DENIED" | jq -r '.result')"
done
FORBIDDEN_GET=$(call GetKey "$(jq -nc --arg k "$FORBIDDEN_KEY_ID" '{keyId:$k}')")
assert_eq "forbidden remaining unchanged" "2" "$(echo "$FORBIDDEN_GET" | jq -r '.key.remainingUses')"
assert_eq "forbidden last verified omitted" "" "$(echo "$FORBIDDEN_GET" | jq -r '.key.lastVerifiedAt // ""')"
assert_eq "forbidden valid verify" "VERIFY_RESULT_VALID" "$(verify_result "$FORBIDDEN_PLAINTEXT")"
FORBIDDEN_AFTER_VALID=$(call GetKey "$(jq -nc --arg k "$FORBIDDEN_KEY_ID" '{keyId:$k}')")
assert_eq "forbidden remaining after valid" "1" "$(echo "$FORBIDDEN_AFTER_VALID" | jq -r '.key.remainingUses')"

echo "==> rate-limited checks do not spend remaining uses"
RATE_KEY=$(call CreateKey "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"usage_rate",name:"rate-credit",limitRefs:[{limitId:"requests",scopeKey:"usage"}],remainingUses:2}')")
RATE_KEY_ID=$(echo "$RATE_KEY" | jq -r '.key.keyId')
RATE_PLAINTEXT=$(echo "$RATE_KEY" | jq -r '.plaintext')
for i in 1 2; do
  RATE_DENIED=$(call VerifyKey "$(jq -nc --arg p "$RATE_PLAINTEXT" '{plaintext:$p,cost:1,requestId:"rate-denied"}')")
  assert_eq "rate limited result $i" "VERIFY_RESULT_RATE_LIMITED" "$(echo "$RATE_DENIED" | jq -r '.result')"
done
RATE_GET=$(call GetKey "$(jq -nc --arg k "$RATE_KEY_ID" '{keyId:$k}')")
assert_eq "rate-limited remaining unchanged" "2" "$(echo "$RATE_GET" | jq -r '.key.remainingUses')"
assert_eq "rate-limited last verified omitted" "" "$(echo "$RATE_GET" | jq -r '.key.lastVerifiedAt // ""')"

echo "==> skipRatelimit bypasses limiter but still spends credit"
SKIP_KEY=$(call CreateKey "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"usage_skip",name:"skip-ratelimit",limitRefs:[{limitId:"requests",scopeKey:"usage"}],remainingUses:2}')")
SKIP_KEY_ID=$(echo "$SKIP_KEY" | jq -r '.key.keyId')
SKIP_PLAINTEXT=$(echo "$SKIP_KEY" | jq -r '.plaintext')
SKIP_VERIFY=$(call VerifyKey "$(jq -nc --arg p "$SKIP_PLAINTEXT" '{plaintext:$p,skipRatelimit:true,cost:1,requestId:"skip-limiter"}')")
assert_eq "skip rate result" "VERIFY_RESULT_VALID" "$(echo "$SKIP_VERIFY" | jq -r '.result')"
assert_eq "skip rate decisions omitted" "0" "$(echo "$SKIP_VERIFY" | jq -r '(.limitDecisions // []) | length')"
SKIP_GET=$(call GetKey "$(jq -nc --arg k "$SKIP_KEY_ID" '{keyId:$k}')")
assert_eq "skip rate remaining spent" "1" "$(echo "$SKIP_GET" | jq -r '.key.remainingUses')"

echo "==> concurrent verifies never overspend a five-use key"
CONCURRENT_KEY=$(call CreateKey "$(jq -nc --arg a "$API_ID" '{apiId:$a,ownerPrincipalId:"usage_concurrent",name:"five-use-concurrent",remainingUses:5}')")
CONCURRENT_KEY_ID=$(echo "$CONCURRENT_KEY" | jq -r '.key.keyId')
CONCURRENT_PLAINTEXT=$(echo "$CONCURRENT_KEY" | jq -r '.plaintext')
CONCURRENT_BODY=$(jq -nc --arg p "$CONCURRENT_PLAINTEXT" '{plaintext:$p}')
for i in $(seq 1 16); do
  (call VerifyKey "$CONCURRENT_BODY" > "$TMP_DIR/burst-$i.json") &
done
wait
VALID_COUNT=$(count_result VERIFY_RESULT_VALID burst)
DEPLETED_COUNT=$(count_result VERIFY_RESULT_DEPLETED burst)
assert_eq "concurrent valid count" "5" "$VALID_COUNT"
assert_eq "concurrent depleted count" "11" "$DEPLETED_COUNT"
CONCURRENT_GET=$(call GetKey "$(jq -nc --arg k "$CONCURRENT_KEY_ID" '{keyId:$k}')")
assert_eq "concurrent remaining" "0" "$(echo "$CONCURRENT_GET" | jq -r '(.key.remainingUses // 0)')"

echo "==> docker compose usage concurrency e2e passed"
