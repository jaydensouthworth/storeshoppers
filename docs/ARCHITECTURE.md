# Architecture

## One modular application

```text
Browser
  ├─ Storefront: browse → basket → demo checkout → order receipt
  └─ Manager: demo sign-in → inventory and audit trail → order queue
          ↓ HTML form requests; HTMX enhances partial updates
Go net/http + html/template
  ├─ Request and form boundaries: host, origin, CSRF, session, manager guard
  ├─ Catalog and cart
  ├─ Inventory adjustments with optimistic version checks
  └─ Checkout and order status transitions
          ↓ parameterized SQL; transactions
SQLite: products, sessions, cart, orders, order_items, adjustments
```

The server returns HTML rather than a JSON API. Forms work with ordinary browser requests; HTMX swaps the shared workspace for live cart, filter and management updates. The underlying behavior is server-owned. Full-page navigation is intentionally plain HTML, keeping history and access checks predictable. HTMX history snapshots are disabled so manager/session pages are not saved in its local history cache.

## Data and invariants

Prices and totals are integer USD cents, never floating point. Products have stable IDs and placeholder barcode values. Order items snapshot product name, unit price and quantity so later catalog edits cannot rewrite a receipt.

Available stock is deducted at order placement, not during browsing or cart editing. Checkout runs under a SQLite immediate transaction, rereads the basket, checks the session checkout key and cart revision, conditionally decrements every product, creates the order and item snapshots, clears the basket and rotates its checkout key. The commit is all-or-nothing. A repeat with an already committed key returns the original session-owned order. Insufficient stock leaves the basket and database unchanged.

The current app uses one DB connection per process and SQLite WAL with foreign keys, check constraints, an immediate write transaction and a five-second busy timeout. This is a deliberate single-instance demo architecture, not a horizontally scaled service. No negative stock or partial order is permitted. A manager’s stale inventory form cannot overwrite a checkout: the product’s version increments on both checkout and manual adjustments. Manual adjustments record a reason and delta atomically.

Orders move only Placed → Picking → Ready → Completed. Each line stores an absolute picked quantity and an optimistic pick version. Picking only edits progress; prices, ordered quantities, totals and stock deductions remain unchanged. Ready is checked inside a transaction and refused until all lines are fully picked. Expected-state and version guards reject stale or repeated actions. Customer status refreshes as an HTMX fragment, while an ordinary refresh link and full-page route remain available. Cancellation/restocking and substitutions need separate policies before implementation.

## Request and session safeguards

- Random 256-bit server-side sessions in SQLite; a supplied unknown cookie does not select a new session ID
- HttpOnly, SameSite=Lax cookies; Secure cookies when the configured origin uses HTTPS
- Session-bound CSRF tokens on all mutating forms, plus exact Origin and trusted Host checks
- Manager access disabled by default; public management requires HTTPS. Normally a public password has a 24-character minimum and loopback development has a 12-character minimum. Explicit `DEMO_MODE=true` permits a simple shared password for fake-data demonstrations and shows a warning banner; it never relaxes HTTPS or request authorization. Constant-time digest comparison, short manager sessions and bounded attempt lockout apply.
- CSRF token rotation at manager sign-in/sign-out; handlers enforce manager authorization on every inventory/order mutation
- In shared demo mode, order lists, receipts, status fragments, picking and transitions are restricted to the current session, even after manager sign-in. Normal manager mode retains store-wide order access. Guessed IDs do not reveal another session’s receipt.
- Request body size limits, strict numeric bounds, parameterized SQL and autoescaped Go templates
- No inline scripts, no external scripts, no eval, no embedding, no-store for dynamic responses, reduced referrer exposure
- Database in a private directory; no secret/config/runtime database in source artifacts

This is a safe local demonstration boundary, **not production identity**. A shared password and global in-memory login lockout are unsuitable for public multi-user use. Before real-world use, add named accounts, hardened session renewal/rotation, individual role authorization, durable rate limits, login monitoring, CSRF/security review, migration evolution, retention/cleanup, backup/restore verification, TLS and a deployment threat model. A public fake-data storefront can run with management disabled. The application rejects configured management on a public HTTP origin. These safeguards do not turn the demo into a production retail system.

## Accessibility and mobile

Semantic headings, navigation, article and form structures; visible labels; keyboard focus treatment; skip link; live success/error messages; disabled out-of-stock actions; reduced-motion preference; responsive two-column phone catalog and stacked checkout. SVG product art is decorative and hidden from assistive technology. Color is not the sole stock/status signal.

The storefront and manager are responsive now. The future shopper flow will add mobile-specific picking interactions without introducing a second frontend stack prematurely.

## Versioned migrations

The original version-one schema is preserved as the baseline. Startup opens an immediate transaction, initializes an empty database if necessary, reads its schema version, applies missing migrations in order and commits. An unsupported future version is refused. Version 2 adds `picked_quantity` and `pick_version` to order items. Ready/Completed legacy orders are backfilled as picked because their manager had already declared them ready; Placed/Picking orders start with zero recorded progress. No orders, cart rows, sessions or stock records are dropped or reseeded. Migration and restart tests use a frozen populated v1 fixture, including injected failure rollback.

## Public demo ownership

`DEMO_MODE=true` is an intentionally shared fake-data mode, not a full tenant system. Inventory and its audit trail are shared. Each session’s customer and manager views share the same server-side cookie and can operate that session’s orders. They cannot browse or change another visitor’s orders, including through status fragments or guessed picking URLs. Browser sessions currently last 24 hours; closing and reopening the server preserves them until expiry. There is no destructive global reset. A future per-visitor store/reset requires a dedicated data-lifecycle design rather than silently clearing the current store.
