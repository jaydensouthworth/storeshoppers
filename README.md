# Store Shoppers

A connected neighborhood-market demo built with **Go, HTMX and SQLite**. Browse a 70-product demo catalog, put together a basket, place a simulated order, and manage the catalog, shelves and fulfillment from one workspace.

## Current capabilities

- Searchable 70-product demo catalog with configurable department filters and clear stock states
- Manager product creation, editing, archiving and restoration, with stable SKUs and reusable bundled illustrations
- Configurable categories and optional product types, with reassignment, safe archive guards and label restoration
- Basket quantities, simulated checkout and session-owned receipts
- Transactional catalog change history and manager inventory adjustments with a reason and audit trail
- Manager pick tickets with per-item quantities and guarded readiness
- Customer status refresh with a normal HTML fallback
- Persistent SQLite storage and locally bundled frontend assets

This is a portfolio project with fake products and demo orders. No payment, address, email or real fulfillment is involved. Phone-first picking and barcode validation are planned next.

## Architecture

One Go server renders HTML with `html/template`; HTMX enhances the forms without a separate frontend application. SQLite keeps the domain rules close to the data:

- Prices and totals use integer cents
- Checkout commits the entire order and stock deduction atomically
- Replay protection returns the existing order after repeated checkout submissions
- Cart revisions, catalog versions and inventory versions reject stale writes
- Checkout quotes require another review after prices change; unrelated stock updates do not invalidate prices
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

For an intentionally shared, fake-data demo, explicitly set `DEMO_MODE=true` and `MANAGER_PASSWORD=password` in runtime environment settings. This permits a simple password while keeping public HTTPS, CSRF, ownership checks and manager checks in place. Anyone who knows or guesses that shared password can change the shared demo catalog, inventory and their own session’s order statuses. In shared demo mode, the manager sees and prepares only orders owned by the current browser session; inventory remains a shared fake catalog. A visible shared-demo banner explains this boundary. Leave `DEMO_MODE` unset or set it to `false` to retain the normal password requirements. Do not enable shared demo mode with real or private data.

The shared manager password is a limited demo gate, not production identity. Named accounts, hardened access control, full browser/accessibility verification and operational hardening remain before real-world use.

## Data upgrades and fulfillment

Startup applies versioned migrations transactionally. Version 2 adds picking progress; version 3 adds configurable catalog metadata, stable category/type identities, local product codes and receipt measurement snapshots without replacing products, sessions, baskets, stock or receipt amounts. Historical Ready/Completed orders retain their state; new and unfinished orders require every item to be picked before Ready. Picking does not deduct stock a second time. Back up the SQLite volume before a deployment, and preserve the entire `/data` directory.

Catalog management is implemented in version 3 at `/manager/catalog`. New manager-created products start with zero stock; restock them through the audited inventory form. Category/type names and SKUs remain reserved after archive, and historical receipts and picking tickets survive catalog edits. Archived category/type labels can be restored with their original identity; this does not restore any archived products. Product restoration is a separate, version-guarded action requiring active category/type labels. It preserves stock, SKU and local code IDs, and requires a fresh checkout quote.

Fresh stores get 70 fake products. A one-time v3 migration expands only an exact original eight-product catalog, identified by its eight original IDs and placeholder codes, while retaining existing names, prices and stock. A catalog with those eight plus any custom product is not expanded. Customized/partial legacy catalogs and established empty stores are not seeded; reopening never restores removed products.

Weighed products can be configured with grams, cents per kilogram and a quantity step, but are clearly unavailable to add to a basket or checkout. Existing numeric barcode values are legacy placeholders; the stored local `SHOPDEMO-` identifiers are not retail GTINs. Label generation, camera scanning, weighted checkout, substitutions, special instructions and timed basket reservations remain later work. Baskets currently do not reserve inventory; stock is deducted atomically at placement. See the [catalog evolution plan](docs/CATALOG_EVOLUTION.md) for completed boundaries and deferred work.

## Project layout

```text
cmd/shop/          Configuration and server lifecycle
internal/shop/     Catalog, basket, orders, inventory, sessions and tests
web/               Embedded HTML, CSS, SVG illustrations and HTMX
scripts/           Development, checks and HTTP smoke test
docs/              Architecture and roadmap
```

HTMX is bundled locally with its license. No frontend build service or runtime CDN is required.
