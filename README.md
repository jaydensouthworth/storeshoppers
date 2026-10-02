# Store Shoppers

A connected neighborhood-market demo built with **Go, HTMX and SQLite**. Browse a 72-product demo catalog, put together a basket, place a simulated order, and manage the catalog, shelves and fulfillment from one workspace.

## Current capabilities

- Scheduled fixed-price sales, featured choices and a data-driven weekly-sales storefront with safe price review
- Individual product pages with descriptions, package details, optional illustrative nutrition and nonfood examples
- Searchable 72-product demo catalog with configurable department filters and clear stock states
- Manager product creation, editing, archiving and restoration, with stable SKUs and reusable bundled illustrations
- Catalog search across names/SKUs/codes, combined filters, sorting and 20-row pages with preserved editor context
- Inventory filters, 12-row pages and a single selected adjustment form; scoped, filterable activity links
- Configurable categories and optional product types, with reassignment, safe archive guards and label restoration
- Persisted 15-minute basket stock holds, explicit review after expiry and session-owned receipts
- Scoped manager basket overrides, reasoned audit and per-visitor synthetic practice baskets
- Bounded plaintext checkout instructions and explicit shopping percentages
- Task-focused manager sections with searchable lists and dedicated product editors
- Transactional catalog change history and manager inventory adjustments with a reason and audit trail
- Inline manager Mark picked, quantity, substitution and removal controls with compact product search
- Partial finish and whole-order cancellation with immutable placed receipts, separate final totals and scoped audit
- Shoppers workspace with scoped simulated roster editing, manager-set availability, assignment capacity, archive/restore and actual recorded progress
- Reasoned, replay-safe assign/reassign/cancel-task controls, scoped task history and automatic task closure at Ready or completion
- Manager pick tickets with per-item quantities, current assignment links and guarded readiness
- Visible form recovery after manager login, retained quantity/instruction drafts and no automatic mutation replay
- Customer status refresh with a normal HTML fallback
- Compact light/dark toggle with saved preferences and a system appearance option
- Dismissible demo guide pointing visitors to the Employees workspace
- Separate phone-first in-store demo with one-time assignment pairing and independent customer/phone sessions
- Real Code 128 product labels, user-triggered camera/photo decoding and manual fallback
- Explicit replay-safe counted-pick and measured-weight confirmation with shared manager progress and revocable task access
- Phone actual-gram amount/stock review and private item reports routed to the manager’s review queue
- Persistent SQLite storage and locally bundled frontend assets

This is a portfolio project with fake products and demo orders. No payment, address, email or real fulfillment is involved. Weighted selling uses reviewed actual grams and immutable requested receipts; its quantity and recovery rules are recorded in [Weighted products](docs/WEIGHTED_PRODUCTS.md). The phone workflow, privacy boundaries and physical-device acceptance checklist are in [Handheld demo](docs/HANDHELD.md); later connected workflows remain in the [ordered roadmap](docs/ROADMAP.md).

## Architecture

One Go server renders HTML with `html/template`; HTMX enhances the forms without a separate frontend application. SQLite keeps the domain rules close to the data:

- Prices and totals use integer cents
- Basket edits reserve available stock atomically; checkout converts the hold into an order without another deduction
- Replay protection returns the existing order after repeated checkout submissions
- Cart revisions, catalog versions and inventory versions reject stale writes
- Checkout quotes require another review after prices change; unrelated stock updates do not invalidate prices
- Order items preserve product names and prices as immutable snapshots
- CSRF tokens, session ownership and server-side manager checks protect writes

See [Appearance preferences](docs/DARK_MODE.md) for theme behavior and verification.

See [Architecture](docs/ARCHITECTURE.md) for the design and its limits, and [Manager workflows](docs/MANAGER_WORKFLOWS.md) for item controls and interrupted-form recovery.

## Development

Requirements: Go 1.24+, a C compiler for SQLite, Python 3 for HTTP smoke tests, and Node.js 18+ for the dependency-free interaction checks. Node.js is test tooling only; the deployed app remains one Go server.

```sh
./scripts/dev.sh
```

Open `http://127.0.0.1:8090`. The development script uses the intentionally public, loopback-only manager password `local-demo-only`. Never reuse a real account password.

```sh
./scripts/check.sh
```

The check script verifies formatting, runs `go vet` and the tests with the race detector, builds the server, and exercises actual HTTP order/manager flows with restart persistence and an explicitly confirmed demo reset. Tests use temporary databases. Container checks run in CI.

## Container configuration

The root Dockerfile builds a non-root, single-service image. It listens on port **8090** and stores SQLite at **/data/shop.db**. Mount a persistent volume at **/data** and run **one replica**. Preserve the whole directory, including SQLite WAL files and private demo-reset backups; use a consistent backup strategy.

| Variable | Purpose |
| --- | --- |
| `APP_ADDR` | Listener, `0.0.0.0:8090` in the container |
| `APP_ORIGIN` | Exact public origin, including `http://` or `https://`, without a trailing slash |
| `DATABASE_PATH` | SQLite path, `/data/shop.db` in the container |
| `MANAGER_PASSWORD` | Optional demo manager gate; unset disables management |
| `DEMO_MODE` | Set exactly `true` for the shared demo gate and optional manager-confirmed reset page; off by default |
| `DEMO_BACKUP_MAX_BYTES` | Private pre-reset archive budget in bytes; default `268435456` (256 MiB), no automatic pruning |

Public HTTP is suitable only for a fake-data storefront demonstration with management disabled. The app refuses public HTTP manager access. To enable management, first configure working HTTPS and use a unique password of at least 24 characters supplied through the hosting platform’s environment settings. HTTPS origins force Secure cookies. Do not commit real secrets or expose the container port directly around a TLS reverse proxy.

For an intentionally shared, fake-data demo, explicitly set `DEMO_MODE=true` and `MANAGER_PASSWORD=password` in runtime environment settings. This permits a simple password while keeping public HTTPS, CSRF, ownership checks and manager checks in place. Anyone who knows or guesses that shared password can change the shared demo catalog, inventory and their own session’s order statuses. In shared demo mode, the manager sees and prepares only orders owned by the current browser session; inventory remains a shared fake catalog. A visible shared-demo banner explains this boundary. Leave `DEMO_MODE` unset or set it to `false` to retain the normal password requirements. Do not enable shared demo mode with real or private data. Ordinary restarts and deployments retain data. Managers can explicitly restore the seeded catalog at `/manager/demo/reset`: GET shows a global-consequence confirmation and a CSRF-protected POST preserves a complete private SQLite backup before clearing sessions, baskets, orders, picking and visitor edits. Exhausted backup storage refuses the reset instead of deleting history. See [Demo reset and recovery](docs/DEMO_RESET.md).

The shared manager password is a limited demo gate, not production identity. Named accounts, hardened access control, full browser/accessibility verification and operational hardening remain before real-world use.

## Data upgrades and fulfillment

Startup applies versioned migrations transactionally. Version 2 adds picking progress; version 3 adds configurable catalog metadata, stable category/type identities, local product codes and receipt measurement snapshots without replacing products, sessions, baskets, stock or receipt amounts. Historical Ready/Completed orders retain their state; new and unfinished orders require every item to be picked before Ready. Picking does not deduct stock a second time. Back up the SQLite volume before a deployment, and preserve the entire `/data` directory.

Version 5 adds separate working-order lines, reasoned manager overrides and explicit partial/cancelled outcomes without rewriting placed receipts. Ready and Completed orders remain frozen. See [Manager order overrides](docs/ORDER_OVERRIDES.md).

Version 8 adds optional product-detail metadata, complete public product pages and a guarded nonfood-example action. Metadata edits share catalog concurrency and audit while keeping price/inventory versions and receipt snapshots unchanged. Established migrations add no product rows, stock or invented nutrition.

Version 7 adds fixed-cent promotions, independent featured choices and transactional merchandising audit. Normal migration/restarts create no offers. Customer and new working-item quotes require review across sale boundaries; placed and retained working rates stay unchanged. See [Promotions](docs/PROMOTIONS.md).

Version 6 adds a fixed simulated shopper roster, versioned order assignments and structured task history. Existing orders remain unassigned until a manager assigns them. Cancelling a task preserves order status, working items and stock; Ready or completion ends the task in the same transaction. See [Shoppers](docs/SHOPPERS.md).

Version 4 adds persisted basket allocations, scoped basket management and instruction snapshots. Existing baskets migrate with their contents intact and no reservation; the customer must explicitly review and reserve before checkout. Expired holds release exactly once on startup or the next relevant request, and reads never renew a hold. See [Basket reservations](docs/BASKET_RESERVATIONS.md).

Catalog management is implemented in version 3 at `/manager/catalog`. New manager-created products start with zero stock; restock them through the audited inventory form. Category/type names and SKUs remain reserved after archive, and historical receipts and picking tickets survive catalog edits. Archived category/type labels can be restored with their original identity; this does not restore any archived products. Product restoration is a separate, version-guarded action requiring active category/type labels. It preserves stock, SKU and local code IDs, and requires a fresh checkout quote.

Fresh stores get 72 fake products, including deodorant and razors. The schema-v8 detail seed runs only for fresh databases and explicitly confirmed resets; existing catalogs are never silently enriched. A demo-only reviewed action can add the two nonfood examples without overwriting existing products. See [Product details](docs/PRODUCT_DETAILS.md). A one-time v3 migration expands only an exact original eight-product catalog, identified by its eight original IDs and placeholder codes, while retaining existing names, prices and stock. A catalog with those eight plus any custom product is not expanded. In normal mode, customized/partial legacy catalogs and established empty stores are not seeded; reopening never restores removed products. An explicitly confirmed demo reset instead restores the full seed after safely preserving the prior database.

Existing file-backed databases receive a verified recovery snapshot before an upgrade in `<database>.migration-backups`. Archives have a separate 256 MiB cap and are never automatically purged; backup failure stops migration with the original data unchanged. Current-schema restarts preserve data and create no new snapshot. Older application images refuse a newer schema, so recovery uses a forward fix or a deliberately restored archive, not an image-only rollback. See [Schema compatibility and recovery](docs/SCHEMA_COMPATIBILITY.md).

Schema 10 enables weighed products with integer grams, cents per kilogram and a requested quantity step. Actual weights use an explicit preview and confirmation; accepted allocations, measurements and final amounts remain separate from the immutable requested receipt. Baskets hold counted units or grams for 15 minutes; checkout converts those holds without a second deduction. Mixed views use product-line progress. See [Weighted products](docs/WEIGHTED_PRODUCTS.md) for limits, migration and compatibility safeguards. Existing numeric barcode values remain legacy placeholders; local `SHOPDEMO-` identifiers are not retail GTINs. Schema 14 renders these exact stored identities as independently decodable Code 128 demo labels; they are still not retail GTINs.

Schema 11 adds explicit manager review holds and private internal notes. The Orders
queue searches references, original/working product names and SKUs, with scoped
counts and stable 20-row pages. Holds leave editing, picking, measured-weight
review and assignment usable; Ready and partial completion require release.
Customer trackers show only “Under manager review”. See [Order review and queue](docs/ORDER_ATTENTION.md).

## Project layout

```text
cmd/shop/          Configuration and server lifecycle
internal/shop/     Catalog, baskets, orders, shoppers, inventory, sessions and tests
web/               Embedded HTML, CSS, SVG illustrations and HTMX
scripts/           Development, checks and HTTP smoke test
docs/              Architecture and roadmap
```

HTMX is bundled locally with its license. No frontend build service or runtime CDN is required.

### Product images

Open a product in Products and choose **Manage product image**. Preview a JPEG or PNG, review the normalized orientation/colors, then confirm that the demo artwork may be public. The app stores resized JPEG variants and strips filenames/embedded metadata. Existing illustrations stay available. Uploads have strict input, pixel, concurrency, rate and retained-storage limits; resets preserve recovery assets and budgets. See [Product images](docs/PRODUCT_IMAGES.md) for limits and recovery behavior.

Schema 12 extends sales and featured choices to weighed products. Offers record cents per kilogram separately from counted prices, gram quantities are chosen on product pages, and placed order rates remain fixed during measurement.

Schema 13 adds visitor-scoped simulated shopper profiles, availability and active-order
capacity. New assignments check eligibility transactionally, while current work and
historical identities survive profile edits and archive/restore. See [Shoppers](docs/SHOPPERS.md).

Schema 14 adds one-time, short-lived phone pairing and revocable assignment-scoped grants. The separate `/handheld/` workspace shares authorized counted picking with the manager, preserves stock and placed receipts, and supports local camera/photo decoding without uploads. Product pages expose real demo scan labels. Phone weight review and bounded item reports reuse the existing measurement and manager-hold rules. Messaging, structured substitution approvals and staging remain staged follow-ups. See [Handheld demo](docs/HANDHELD.md).
