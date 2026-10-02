# Architecture

## One modular application

```text
Browser
  ├─ Storefront: browse → basket → demo checkout → order receipt
  └─ Manager: demo sign-in → catalog / inventory and audit trail / order queue / shopper assignments
          ↓ HTML form requests; HTMX enhances partial updates
Go net/http + html/template
  ├─ Request and form boundaries: host, origin, CSRF, session, manager guard
  ├─ Versioned product catalog, categories, optional types and local codes
  ├─ Persisted timed basket holds, scoped overrides and price quote
  ├─ Inventory adjustments with optimistic version checks
  └─ Checkout and order status transitions
          ↓ parameterized SQL; transactions
SQLite: products, categories, product_types, product_codes,
        sessions, baskets, cart, orders, order_items, adjustments, catalog_events, basket_events, working_order_items, order_events, shoppers, shopper_assignments, shopper_event_links
```

The server returns HTML rather than a JSON API. Forms work with ordinary browser requests; HTMX swaps the shared workspace for live cart, filter and management updates. The underlying behavior is server-owned. Full-page navigation is intentionally plain HTML, keeping history and access checks predictable. HTMX history snapshots are disabled so manager/session pages are not saved in its local history cache.

## Data and invariants

Prices and totals are integer USD cents, never floating point. Products have stable IDs and immutable merchant SKUs. Categories and optional product types have stable IDs, clean display names, whitespace/case-normalized unique names and optimistic versions. Their labels never determine selling units. An active product requires an active category; types are optional. Referenced categories/types must be reassigned or their active products archived before taxonomy archive. A version-guarded restore reactivates the same label ID/name without restoring archived products.

Products have separate inventory, catalog and price versions. Catalog edits do not overwrite stock, stock edits do not stale price quotes, and stale manager edits fail rather than overwriting a newer change. Manager-created products begin with zero stock and are restocked through the reasoned inventory audit. SKU and taxonomy identities stay reserved after archive. Ten bundled SVG illustration choices are reused across the demo; uploading arbitrary images is not implemented.

Order items snapshot product name, SKU, unit price, selling unit, price basis, quantity step, quantity and subtotal so later catalog edits cannot rewrite a receipt. Product archives disappear from new sales while old receipts and pick tickets remain readable. A previously added archived line stays visible and removable in its basket, with checkout blocked. Explicit product restoration requires matching catalog version and active category/type labels; it restores only that product and its stored code identities, preserving stock and SKU. Both archive and restore advance price/catalog versions, so an old pre-archive quote remains invalid after recovery.

Counted products use unit quantities, a price basis of one and a step of one. Future weighed products store base unit `g`, cents per 1000 grams and a bounded 1–1000 gram step. They cannot currently enter checkout. Unit-count totals exclude grams; a corrupt or legacy weighed basket line blocks checkout rather than treating grams as item units. Unit/price-basis changes while held units remain or after receipt use are rejected. Weighted execution, quantity/stock constraint expansion and rounding policy require their own future migration.

Available stock is reserved during explicit basket edits/review. Basket metadata has an opaque public ID, owner session, revision and 15-minute deadline; cart lines keep desired and held quantities separately. Expiry reconciliation returns held quantities once in a transaction and preserves desired contents for explicit reacquisition. Migration leaves existing carts unreserved rather than reserving stale contents. Reads and status polling never renew. Checkout validates the live hold, key/revision and current price quote, commits receipt snapshots, consumes the allocation and rotates the key without subtracting stock again. Replay returns the original order. Quote/version errors leave live allocations unchanged. See [Basket reservations](BASKET_RESERVATIONS.md) for removal, expiry, migration, practice and audit semantics.

The current app uses one DB connection per process and SQLite WAL with foreign keys, check constraints, an immediate write transaction and a five-second busy timeout. This is a deliberate single-instance demo architecture, not a horizontally scaled service. No negative stock or partially committed command is permitted. Explicit partial fulfillment is supported with recorded missing-unit outcomes. A manager’s stale inventory form cannot overwrite a checkout: the product’s version increments on reservation changes, expiry releases and manual adjustments. Manual adjustments record a reason, delta and selling-unit snapshot atomically. Every successful manager product/category/type create, edit, archive or restore, writes a catalog event in the same transaction. Audit failure rolls back the command; stale/invalid commands create no event. The catalog displays the latest 20 events with name/detail snapshots. This shared-password demo records the change, not a verified human actor.

Orders normally move Placed → Picking → Ready → Completed. Separate working lines store absolute picked quantities and optimistic pick versions; original receipt snapshots remain unchanged. Before Ready, reasoned manager commands can add, remove, resize or substitute counted products, reconciling only stock-allocation differences. A partial finish records the unpicked remainder and freezes the picked total, including zero; whole-order cancellation freezes zero and explicitly returns or writes off all allocations. Ready and Completed are immutable except Ready → collection. Order versions guard every displayed finalization, and command hashes make exact override retries idempotent. Customer status refreshes include working/final totals and exceptions without falsely marking missing units picked. See [Manager order overrides](ORDER_OVERRIDES.md).

## Request and session safeguards

- Random 256-bit server-side sessions in SQLite; a supplied unknown cookie does not select a new session ID
- HttpOnly, SameSite=Lax cookies; Secure cookies when the configured origin uses HTTPS
- Session-bound CSRF tokens on all mutating forms, plus exact Origin and trusted Host checks
- Manager access disabled by default; public management requires HTTPS. Normally a public password has a 24-character minimum and loopback development has a 12-character minimum. Explicit `DEMO_MODE=true` permits a simple shared password for fake-data demonstrations and shows a warning banner; it never relaxes HTTPS or request authorization. Constant-time digest comparison, short manager sessions and bounded attempt lockout apply.
- CSRF token rotation at manager sign-in/sign-out; handlers enforce manager authorization on every catalog, inventory and order mutation
- In shared demo mode, order lists, receipts, status fragments, picking and transitions are restricted to the current session, even after manager sign-in. Normal manager mode retains store-wide order access. Guessed IDs do not reveal another session’s receipt.
- Request body size limits, strict numeric bounds, parameterized SQL and autoescaped Go templates
- No inline scripts, no external scripts, no eval, no embedding, no-store for dynamic responses, reduced referrer exposure
- Database in a private directory; no secret/config/runtime database in source artifacts

This is a safe local demonstration boundary, **not production identity**. A shared password and global in-memory login lockout are unsuitable for public multi-user use. Before real-world use, add named accounts, hardened session renewal/rotation, individual role authorization, durable rate limits, login monitoring, CSRF/security review, migration evolution, retention/cleanup, backup/restore verification, TLS and a deployment threat model. A public fake-data storefront can run with management disabled. The application rejects configured management on a public HTTP origin. These safeguards do not turn the demo into a production retail system.

## Accessibility and mobile

Semantic headings, navigation, article and form structures; visible labels; keyboard focus treatment; skip link; live success/error messages; disabled out-of-stock actions; reduced-motion preference; responsive two-column phone catalog and stacked checkout. SVG product art is decorative and hidden from assistive technology. Color is not the sole stock/status signal.

The storefront and manager are responsive now. The Shoppers manager workspace assigns fixed simulated roster members to actual orders, with recorded picking progress and explicitly unrecorded scanner telemetry. A future shopper-facing flow will add mobile-specific picking interactions without introducing a second frontend stack prematurely.

## Versioned migrations

The original version-one schema is preserved as the baseline. Startup opens an immediate transaction, initializes an empty database if necessary, reads its schema version, applies missing migrations in order and commits. An unsupported future version is refused. Version 2 adds `picked_quantity` and `pick_version` to order items. Ready/Completed legacy orders are backfilled as picked because their manager had already declared them ready; Placed/Picking orders start with zero recorded progress. No orders, cart contents, sessions or stock records are lost or reseeded. Version 3 adds categories, types, SKU and unit metadata, archived states, version domains, product-code identities and receipt measurement snapshots. Existing product IDs, leading-zero placeholder values, stock, sessions, carts, order totals and picking progress survive. Version 4 adds owner-scoped basket identities, allocations, scoped audit and instructions while preserving old receipts and leaving existing carts unreserved for review. A transaction failure rolls back schema changes, data backfills and version markers together; unsupported future versions remain refused.

Fresh database creation seeds 72 fake products within the migration transaction. The v3 upgrade expands only an exact original eight-product catalog: the total must be eight and every original product ID and placeholder value must match. Manager changes to names, prices or stock on those eight are preserved. The eight originals plus any additional custom product form a customized catalog and receive no expansion. Partial/custom catalogs and established empty schemas are never seeded. Later opens cannot resurrect removed rows. Tests keep independent populated v1/v2 fixtures, inject failure at the final version marker, and verify empty-store, expansion, reopen and foreign-key integrity behavior.

## Runtime version guard

Every ordinary transaction checks the exact supported schema version after obtaining SQLite's immediate writer lock, including session creation, manager grants/logout and expiry reconciliation. Dynamic HTTP responses and `/healthz` check before dispatch and after collecting response data; detected incompatibility returns retryable 503 and discards buffered content/cookies/redirects. Reset repeats the exact check under exclusive ownership. Schema remains v8. This protects the guarded binary only; an incompatible migration still requires verified old-process drain or separately proven database-enforced legacy protection. See [Runtime schema compatibility](SCHEMA_COMPATIBILITY.md) for ordering, response limits and deployment boundaries.

## Public demo ownership

`DEMO_MODE=true` is an intentionally shared fake-data mode, not a full tenant system. Inventory and its audit trail are shared. Each session’s customer and manager views share the same server-side cookie and can operate that session’s orders and basket, plus explicitly initialized synthetic practice baskets. They cannot browse or change another visitor’s orders, including through status fragments or guessed picking URLs. Browser sessions currently last 24 hours; closing and reopening the server preserves them until expiry. An explicitly confirmed shared reset is available only in demo mode and preserves a private recovery snapshot; ordinary restarts never clear data. A future per-visitor store/reset requires a dedicated data-lifecycle design.

## Product identity and scanning boundary

`product_codes` keeps the old numeric values as `legacy_placeholder` and separately records a generated `demo_local` code such as `SHOPDEMO-000001`, preserving leading zeroes and uniqueness across archived rows. A custom SKU does not change the local demo code. These local codes are intended for ordinary Code 128, not GS1-128 or registered GTINs. The read-only resolver accepts the local Code 128 boundary; it does not validate legacy placeholders as retail codes, mutate orders, or mark items picked. No generated barcode labels, camera UI or scanner workflow are shipped in this milestone. All future scanning and fulfillment remain modules of this same Go/HTMX application.

## Explicit demo reset

Both normal and demo startup preserve data through additive migrations. Shared demo managers may visit `/manager/demo/reset` for a confirmation page; only an authorized, CSRF-protected and explicitly confirmed POST resets the shared fixture. A bounded request gate drains active handlers, SQLite temporarily enforces exclusive ownership, and the supported Backup API creates a durable private archive before transactional baseline installation. The live main/WAL files are never swapped or unlinked. Failure preserves recoverability, archives are bounded without automatic pruning, and successful reset invalidates every session. See [Demo reset, storage limits and recovery](DEMO_RESET.md) for details and primary SQLite references.

## Shopper assignments

Schema v6 adds a fixed simulated roster, versioned task ownership and structured assignment-audit links without assigning historical orders. Every assignment, reassignment and cancellation advances the order revision and writes its reason in the same immediate transaction. One active task per order is enforced by a partial unique index; each shopper may hold several orders. Cancelling a task preserves stock, receipt and working lines and leaves its order unassigned. Ready or Completed ends its task with the same transactional audit. Demo ownership is applied before workload aggregates, filtering, history and pagination. Scan rates remain unrecorded until real telemetry exists. See [Shoppers](SHOPPERS.md).


## Scheduled promotions (schema v7)

[Promotions](PROMOTIONS.md) add independent fixed-cent UTC-window sales and featured records without replacing catalog prices or receipt/working snapshots. Shared effective pricing and promotion-aware quotes cover storefront, baskets, checkout and first-time working-order additions. Non-overlap, optimistic versions, compatible regular-price edits and transactional audit are enforced server-side. Existing working/tombstone rates stay recorded. Ordinary startup/migration creates no examples; an explicit demo-only preview/confirmed action or the already-confirmed global reset can create a bounded current-week fixture.


## Product details (schema v8)

Optional `product_details` rows hold bounded plain-text body/package information and a food-only illustrative nutrition panel. Nutrition macros use tenths of a gram; omitted panels remain NULL rather than becoming zero. Product type/category labels and package contents never reinterpret selling quantities or rates. `SaveProductWithDetails` saves the product, details and catalog audit in the existing catalog-version transaction. The older `SaveProduct` preserves detail rows. Metadata-only changes do not advance price or inventory versions and never rewrite placed/working snapshots.

Public `/products/{id}` pages use the common effective-price resolver and reservation availability. Archived IDs return 404 publicly while manager review and historical snapshots survive. Basket forms share the existing revision/hold/checkout boundaries and a bounded internal product-page return. Catalog forms accept at most 32 KiB to accommodate bounded percent-encoded Unicode descriptions; other forms keep their existing limit.

Existing migrations create empty metadata/example tables, with no inferred nutrition or new stock. Only genuinely new stores and confirmed reset fixtures seed rich food details and two nonfood products. The demo-only `/manager/catalog/examples` action previews two fixed examples, requires explicit confirmation and a current hash, then creates labels/products/details and reasoned stock audits atomically. Provenance and replay records prevent overwrite, duplication or resurrection after archive. See [Product details](PRODUCT_DETAILS.md).
