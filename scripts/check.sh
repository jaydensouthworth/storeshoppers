#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -x .tools/go/bin/go ]]; then export PATH="$PWD/.tools/go/bin:$PATH"; fi
export GOCACHE="${GOCACHE:-/tmp/instore-gocache}"
export GOPATH="${GOPATH:-/tmp/instore-gopath}"
node --check web/static/app.js
node --check web/static/theme.js
node --check web/static/demo_guide.js
node --check web/static/scanner.js
node --check web/static/handheld.js
node --check web/static/order_messages.js
node --test scripts/interaction_test.cjs
node --test scripts/cart_drafts_test.cjs
node --test scripts/theme_test.cjs
node --test scripts/demo_guide_test.cjs
node --test scripts/scanner_test.cjs
node --test scripts/handheld_test.cjs
node --test scripts/order_messages_test.cjs
test -z "$(gofmt -l cmd internal web/embed.go)"
go vet ./...
go test -race -coverprofile=coverage.out ./...
go build -o bin/shop ./cmd/shop
python3 scripts/smoke.py
python3 scripts/cart_drafts_smoke.py
python3 scripts/demo_reset_smoke.py
python3 scripts/stock_tools_smoke.py
python3 scripts/order_overrides_smoke.py
python3 scripts/shoppers_smoke.py
python3 scripts/promotions_smoke.py
python3 scripts/product_details_smoke.py
python3 scripts/product_images_smoke.py
python3 scripts/weighted_smoke.py
python3 scripts/attention_smoke.py
python3 scripts/weighted_promotions_smoke.py

python3 scripts/handheld_smoke.py

python3 scripts/handheld_weight_smoke.py

python3 scripts/order_messages_smoke.py

python3 scripts/employee_smoke.py

python3 scripts/substitutions_smoke.py
