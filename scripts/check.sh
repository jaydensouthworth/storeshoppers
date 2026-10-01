#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -x .tools/go/bin/go ]]; then export PATH="$PWD/.tools/go/bin:$PATH"; fi
export GOCACHE="${GOCACHE:-/tmp/instore-gocache}"
export GOPATH="${GOPATH:-/tmp/instore-gopath}"
test -z "$(gofmt -l cmd internal web/embed.go)"
go vet ./...
go test -race -coverprofile=coverage.out ./...
go build -o bin/shop ./cmd/shop
python3 scripts/smoke.py
