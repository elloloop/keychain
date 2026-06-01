#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"

"$ROOT/docker-compose-critical-rpcs.sh"
"$ROOT/docker-compose-key-behavior.sh"
"$ROOT/docker-compose-admin-errors.sh"
"$ROOT/docker-compose-usage-concurrency.sh"
"$ROOT/docker-compose-lifecycle-privacy.sh"
"$ROOT/docker-compose-verify-matrix.sh"
