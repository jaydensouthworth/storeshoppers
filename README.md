# Store Shoppers

A connected neighborhood-market demo built with **Go, HTMX and SQLite**. Browse a small catalog, put together a basket, place a simulated order, and manage the shelves and fulfillment from one workspace.

## The first slice

- Searchable catalog with department filters and clear stock states
- Basket quantities, simulated checkout and session-owned receipts
- Manager inventory adjustments with a reason and audit trail
- A shared order queue with guarded fulfillment transitions
- Persistent SQLite storage and locally bundled frontend assets

This is a portfolio project with fake products and demo orders. No payment, address, email or real fulfillment is involved. Phone-first picking and barcode validation are planned next.

## Architecture

One Go server renders HTML with `html/template`; HTMX enhances the forms without a separate frontend application. SQLite keeps the domain rules close to the data:

- Prices and totals use integer cents
- Checkout commits the entire order and stock deduction atomically
- Replay protection returns the existing order after repeated checkout submissions
- Cart revisions and inventory versions reject stale writes
- Order items preserve product names and prices as immutable snapshots
- CSRF tokens, session ownership and server-side manager checks protect writes

See [Architecture](docs/ARCHITECTURE.md) for the design and its limits.

## Development

Requirements: Go 1.24+, a C compiler for SQLite, and Python 3 for the HTTP smoke test.

```sh
./scripts/dev.sh
```

Open `http://127.0.0.1:8090`. The development script uses the intentionally public, loopback-only manager password `local-demo-only`. Never reuse a real account password.

```sh
./scripts/check.sh
```

The check script verifies formatting, runs `go vet` and the tests with the race detector, builds the server, and exercises an actual HTTP order/manager flow with restart persistence. Tests use temporary databases. Container checks run in CI.

## Container configuration

The root Dockerfile builds a non-root, single-service image. It listens on port **8090** and stores SQLite at **/data/shop.db**. Mount a persistent volume at **/data** and run **one replica**. Preserve the whole directory, including SQLite WAL files; use a consistent backup strategy.

| Variable | Purpose |
| --- | --- |
| `APP_ADDR` | Listener, `0.0.0.0:8090` in the container |
| `APP_ORIGIN` | Exact public origin, including `http://` or `https://`, without a trailing slash |
| `DATABASE_PATH` | SQLite path, `/data/shop.db` in the container |
| `MANAGER_PASSWORD` | Optional demo manager gate; unset disables management |
| `DEMO_MODE` | Set exactly `true` to allow a simple shared demo password; off by default |

Public HTTP is suitable only for a fake-data storefront demonstration with management disabled. The app refuses public HTTP manager access. To enable management, first configure working HTTPS and use a unique password of at least 24 characters supplied through the hosting platform’s environment settings. HTTPS origins force Secure cookies. Do not commit real secrets or expose the container port directly around a TLS reverse proxy.

For an intentionally shared, fake-data demo, explicitly set `DEMO_MODE=true` and `MANAGER_PASSWORD=password` in runtime environment settings. This permits a simple password while keeping public HTTPS, CSRF, ownership checks and manager checks in place. Anyone who knows or guesses that shared password can change demo inventory and order statuses. A visible shared-demo banner is shown. Leave `DEMO_MODE` unset or set it to `false` to retain the normal password requirements. Do not enable shared demo mode with real or private data.

The shared manager password is a limited demo gate, not production identity. Named accounts, hardened access control, full browser/accessibility verification, versioned migrations and operational hardening remain before real-world use.

## Project layout

```text
cmd/shop/          Configuration and server lifecycle
internal/shop/     Catalog, basket, orders, inventory, sessions and tests
web/               Embedded HTML, CSS, SVG illustrations and HTMX
scripts/           Development, checks and HTTP smoke test
docs/              Architecture and roadmap
```

HTMX is bundled locally with its license. No frontend build service or runtime CDN is required.
