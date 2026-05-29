#!/usr/bin/env bash

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$E2E_DIR/../.." && pwd)"

: "${PROJECT:=keychain-e2e}"
: "${TARGET:=localhost:28080}"
: "${METRICS_TARGET:=localhost:29090}"

COMPOSE_FILE="$REPO_ROOT/docker-compose.yml"

cd "$REPO_ROOT"

cleanup() {
  echo "==> tearing down"
  docker compose -p "$PROJECT" -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
}

compose_up() {
  echo "==> docker compose up -d --build"
  docker compose -p "$PROJECT" -f "$COMPOSE_FILE" up -d --build >/dev/null
  wait_for_keychain
}

restart_keychain() {
  echo "==> restarting keychain"
  docker compose -p "$PROJECT" -f "$COMPOSE_FILE" restart keychain >/dev/null
  wait_for_keychain
}

wait_for_keychain() {
  echo "==> waiting for keychain gRPC reflection ..."
  for i in $(seq 1 60); do
    if grpcurl -plaintext "$TARGET" list >/dev/null 2>&1; then
      echo "==> ready after ${i}s"
      return
    fi
    if [ "$i" -eq 60 ]; then
      echo "keychain did not come up"
      docker compose -p "$PROJECT" -f "$COMPOSE_FILE" logs keychain
      exit 1
    fi
    sleep 1
  done
}

call() {
  local method=$1
  local body=$2
  grpcurl -plaintext -format json -d "$body" "$TARGET" "apikey.v1.ApiKeyService/$method"
}

call_error() {
  local method=$1
  local body=$2
  local out status
  set +e
  out=$(grpcurl -plaintext -format json -d "$body" "$TARGET" "apikey.v1.ApiKeyService/$method" 2>&1)
  status=$?
  set -e
  if [ "$status" -eq 0 ]; then
    echo "FAIL $method: expected gRPC error, got success: $out"
    exit 1
  fi
  printf '%s' "$out"
}

assert_grpc_error() {
  local label=$1 method=$2 body=$3 code=$4 message=$5
  local out
  out=$(call_error "$method" "$body")
  if ! echo "$out" | grep -F "Code: $code" >/dev/null; then
    echo "FAIL $label: expected code $code"
    echo "$out"
    exit 1
  fi
  if [ -n "$message" ] && ! echo "$out" | grep -F "$message" >/dev/null; then
    echo "FAIL $label: expected message containing $message"
    echo "$out"
    exit 1
  fi
}

assert_eq() {
  local label=$1 expected=$2 actual=$3
  if [ "$expected" != "$actual" ]; then
    echo "FAIL $label: expected $expected, got $actual"
    exit 1
  fi
}

create_workspace() {
  local name=${1:-e2e}
  local owner=${2:-e2e_owner}
  call CreateWorkspace "$(jq -nc --arg name "$name" --arg owner "$owner" '{name:$name,ownerPrincipalId:$owner}')"
}

create_api() {
  local workspace_id=$1
  local name=${2:-prod}
  local prefix=${3:-ck_e2e_}
  call CreateApi "$(jq -nc --arg w "$workspace_id" --arg name "$name" --arg prefix "$prefix" '{workspaceId:$w,name:$name,keyPrefix:$prefix}')"
}

create_key_for_api() {
  local api_id=$1
  local owner=${2:-e2e_user}
  local name=${3:-e2e-key}
  call CreateKey "$(jq -nc --arg a "$api_id" --arg owner "$owner" --arg name "$name" '{apiId:$a,ownerPrincipalId:$owner,name:$name}')"
}

verify() {
  local plaintext=$1
  call VerifyKey "$(jq -nc --arg p "$plaintext" '{plaintext:$p}')"
}

verify_result() {
  local plaintext=$1
  verify "$plaintext" | jq -r '.result'
}

assert_nonempty() {
  local label=$1 value=$2
  if [ -z "$value" ] || [ "$value" = "null" ]; then
    echo "FAIL $label: missing value"
    exit 1
  fi
}
