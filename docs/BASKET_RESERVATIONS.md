# Basket holds and manager basket desk (schema v4)

## Inventory contract

`products.stock` is **available stock**, not physical stock. Each `cart` line stores its desired quantity and currently held `reserved` quantity. Available + all held quantities is bounded at 10,000 per product; manager adjustments change available only and cannot use held units or overflow the eventual release. Product selling-unit/basis changes are blocked while available stock, held stock, or historical order use remains.

A successful explicit basket edit or review reserves the entire resulting basket atomically for 15 minutes. Counted quantities remain integers 0–99; weighed requests use integer grams up to 100,000 in the product’s step. Zero removes a line. A stale revision, insufficient stock, archived product or failed write cannot partly reserve a basket. Removing a line is always possible: if the remaining basket cannot renew, only that line is released/removed and the old deadline remains. An empty basket has no deadline. Manager overrides use the same service, expected basket revision and a 3–120-character reason.

Expiry is deadline-based and persisted. Startup, basket/catalog/manager reads and relevant inventory writes reconcile overdue allocations in one immediate SQLite transaction. Stock release, clearing held quantities and advancing revisions commit together. No background process is needed, and restart/duplicate reads cannot release twice. A database at rest may contain expired allocations until the next reconciliation; application inventory reads reconcile before reporting availability. Expired owner sessions release their real and practice allocations as well. Reading or polling never extends a deadline.

Checkout validates session key/revision, immutable price quote, product eligibility and live held quantities inside its transaction. It creates immutable receipt snapshots, deletes the consumed basket allocation and rotates the checkout key. It does **not** subtract available stock again. Order allocations are final for this stage and never expire. Existing checkout-key replays return the original session-owned order, including its original instructions. Picking and state changes do not move inventory.

## Upgrade and restart

Ordinary restarts preserve data in both normal and demo mode. In `DEMO_MODE=true`, an explicitly confirmed manager POST at `/manager/demo/reset` restores the seed after a complete private backup, clearing baskets, holds, orders and sessions for all visitors. This is distinct from the per-visitor practice setup action. See [Demo reset and recovery](DEMO_RESET.md).

Version 4 adds independent opaque basket IDs, owner-scoped basket metadata, per-line allocations, basket audit and checkout instruction snapshots. The cart table is rebuilt transactionally; existing session cart contents and revisions are copied to a real basket with zero held quantities and no deadline. They are visibly review-required. Migration never reserves every stale cart, seeds extra customer baskets, drops contents or changes historical receipts. Old orders get empty instructions. Tests cover an independent populated-v3 fixture, failed migration rollback, foreign-key integrity and reopening.

## Manager isolation and practice data

The manager desk uses random 128-bit basket identifiers, never authentication cookie/session IDs. Normal manager mode can inspect and edit current baskets store-wide. Shared demo mode sees only the visitor's real basket and their explicitly created synthetic practice baskets. It cannot access foreign basket IDs or their audit, order instructions or receipts.

The practice action idempotently creates two labeled synthetic baskets for the current visitor. It does not reserve shared stock until an explicit review/edit and never resets global data or regenerates previously changed practice baskets. All manager basket changes write scoped `basket_events` in the same transaction; audit failure rolls back inventory, quantities, deadline and revision. The shared password is not verified actor identity. Synthetic reservations use the same shared fake stock as real demo baskets and expire normally.

## Instructions and progress

Checkout accepts up to 500 Unicode characters of plain text, normalizes CRLF, rejects invalid UTF-8 and control characters other than newline/tab, and HTML-escapes output. Use only fake grocery instructions; the demo is not a place for health, payment or other private information. Validation retains the entered text in the checkout form. The placed snapshot appears on the session-owned receipt and authorized manager pick ticket; it does not enter the public inventory/catalog logs.

Counted-only progress displays floor(picked units × 100 / required units). Mixed orders use completed product lines, with each line’s units or grams shown separately. Ready requires counted picks and confirmed actual grams. See [Weighted products](WEIGHTED_PRODUCTS.md) and [Manager order overrides](ORDER_OVERRIDES.md); scanner execution remains deferred.

## Manager navigation

Manager pages share task-focused navigation: Orders, Baskets, Products, Categories & Types, Stock & Activity, plus an obvious Add product action. Products use a compact searchable list and dedicated create/edit views, rather than rendering dozens of hidden forms. Stock and orders have dedicated searchable screens. Product edit links, back links and mutations retain the search context. The mobile navigation is an ordinary keyboard-operable disclosure; all routes/forms work without JavaScript. Shared-demo logout remains visible. Recoverable basket and stock form errors retain bounded, escaped input drafts only after the manager and basket scope are revalidated. The current saved quantities/stock and fresh concurrency versions remain authoritative; retry requires another explicit submission. Overlong or unsupported display values are clearly marked, and draft data is not persisted.

## Verification boundary

Run `./scripts/check.sh` for formatting, vet, race tests, build and real HTTP/restart smoke checks. Deterministic tests inject a concurrency-safe clock rather than sleeping 15 minutes. Automated HTML checks complement live desktop/mobile browser QA; they do not replace real-device testing. Named identities, cleanup/retention, production authorization, full accessibility/real-device testing and operational hardening remain outstanding.

An all-zero estimate cannot create or renew reservations. Removing an item still works if it leaves a zero-rounded gram line: only the removed allocation is released, the remaining hold is not renewed, and checkout stays disabled until the total is positive.
