#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -x .tools/go/bin/go ]]; then export PATH="$PWD/.tools/go/bin:$PATH"; fi
export GOCACHE="${GOCACHE:-/tmp/instore-gocache}"
export GOPATH="${GOPATH:-/tmp/instore-gopath}"
export MANAGER_PASSWORD="${MANAGER_PASSWORD:-local-demo-only}"
export APP_ADDR="${APP_ADDR:-127.0.0.1:8090}"
export APP_ORIGIN="${APP_ORIGIN:-http://127.0.0.1:8090}"
if [[ "$APP_ADDR" != 127.0.0.1:* && "$MANAGER_PASSWORD" == local-demo-only ]]; then
  echo 'Refusing non-loopback use with the documented demo password.' >&2
  exit 1
fi
exec go run ./cmd/shop
