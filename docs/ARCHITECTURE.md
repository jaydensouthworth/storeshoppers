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

Orders move only Placed → Picking → Ready → Completed. Updates include the expected current state, preventing stale/repeated transition submissions. A cancellation/restock policy will be a separate design decision before it is implemented.

## Request and session safeguards

- Random 256-bit server-side sessions in SQLite; a supplied unknown cookie does not select a new session ID
- HttpOnly, SameSite=Lax cookies; Secure cookies when the configured origin uses HTTPS
- Session-bound CSRF tokens on all mutating forms, plus exact Origin and trusted Host checks
- Manager access disabled by default; public management requires HTTPS and a unique password of at least 24 characters. Loopback development permits a 12-character minimum. Constant-time digest comparison, short manager sessions and bounded attempt lockout apply.
- CSRF token rotation at manager sign-in/sign-out; handlers enforce manager authorization on every inventory/order mutation
- Order reads limited to their owning session or a manager, including guessed numeric IDs
- Request body size limits, strict numeric bounds, parameterized SQL and autoescaped Go templates
- No inline scripts, no external scripts, no eval, no embedding, no-store for dynamic responses, reduced referrer exposure
- Database in a private directory; no secret/config/runtime database in source artifacts

This is a safe local demonstration boundary, **not production identity**. A shared password and global in-memory login lockout are unsuitable for public multi-user use. Before real-world use, add named accounts, hardened session renewal/rotation, individual role authorization, durable rate limits, login monitoring, CSRF/security review, migration tooling, retention/cleanup, backup/restore verification, TLS and a deployment threat model. A public fake-data storefront can run with management disabled. The application rejects configured management on a public HTTP origin. These safeguards do not turn the demo into a production retail system.

## Accessibility and mobile

Semantic headings, navigation, article and form structures; visible labels; keyboard focus treatment; skip link; live success/error messages; disabled out-of-stock actions; reduced-motion preference; responsive two-column phone catalog and stacked checkout. SVG product art is decorative and hidden from assistive technology. Color is not the sole stock/status signal.

The storefront and manager are responsive now. The future shopper flow will add mobile-specific picking interactions without introducing a second frontend stack prematurely.
