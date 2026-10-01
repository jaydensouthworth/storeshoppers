# Roadmap

## Working first slice

- [x] One Go workspace and one persistent database
- [x] Seeded catalog with search, department filter and out-of-stock state
- [x] Session basket and quantity editing
- [x] Atomic demo checkout, replay protection and receipts
- [x] Manager gate, inventory adjustments and recent activity
- [x] Manager order queue, per-item picking checklist and guarded readiness
- [x] Customer status refresh and HTML fallback
- [x] Versioned additive migration with populated-v1 upgrade and restart tests
- [x] Session-scoped order privacy in shared demo mode
- [x] Server-side validation, integer money and transactional stock rules
- [x] Responsive shared design and accessible form structure

## Completed: flexible catalog and basket management

- [x] 70-product demo catalog, configurable categories and optional product types
- [x] Product create/edit/archive/restore, stable SKU/local-code identities and reusable illustrations
- [x] Immutable receipt measurement snapshots and stale-price review
- [x] Versioned v3/v4 populated-data upgrades and restart coverage
- [x] Persisted 15-minute basket reservations, expiry and explicit review/reacquisition
- [x] Available/reserved stock, scoped manager basket overrides and transactional audit
- [x] Per-visitor synthetic practice baskets with no destructive global reset
- [x] Plaintext checkout instructions and explicit shopping percentage
- [x] Task-focused manager navigation, dedicated product editors and searchable lists

## Queued: storefront and catalog presentation

- Promotions and Featured management, plus a weekly-deals homepage
- Individual product pages with package details, descriptions and optional clearly fake nutrition panels
- Non-food examples such as deodorant and razors to demonstrate catalog flexibility
- Existing reusable illustration picker is complete; constrained uploads require a separate secure design

## Next: manager fulfillment changes

Preserve requested receipt lines and placed totals. Add separate fulfillment/allocation rows, explicit substitution policy snapshots, same-unit substitutions, missing-item resolution and reasoned scoped audit. Reconcile stock transactionally without phantom returns. Keep final total nullable until Ready, allow an explicitly all-unavailable final zero and freeze it at Ready. Follow with a deliberate cancellation/Needs-attention/activity workflow. Weighted checkout remains its own bounded quantity/pricing migration before actual-weight reconciliation. See [catalog evolution](CATALOG_EVOLUTION.md).

## Later vertical slice: shopper scanning

1. Introduce actual user roles and an order assignment/claim policy before phone access leaves loopback.
2. Add a phone-first pick list for a claimed order, with remaining quantities and clear completed state.
3. Define valid barcode format(s). Current seed values are placeholders, so they must not be presented as retail-valid EAN/UPC codes.
4. Start with manual barcode entry to validate the model; add camera scanning through a maintained browser capability/library only after testing the intended phone and HTTPS behavior.
5. Reuse the existing picked quantity/version guards, then add scan events. Reject wrong-item scans, duplicate over-picking and stale competing writes.
6. Keep the existing all-items-picked Ready guard. Camera scans must use the same server-side picking service, never a second inventory or status path.
7. Add interrupted-session recovery, clear camera permission failure UX and full wrong-barcode/duplicate-scan tests.

## Further hardening

- Named demo identities and role separation; replace shared-password gate
- Continue versioned migrations; add database cleanup and backup/restore operations
- Cancellation and restocking policy
- Dependency/security scanning, load checks, fuller accessibility audit and real-device phone QA
- TLS, deployment configuration, trusted proxy strategy and operational logs
- Container build and persistence verification in CI
- Deployment configuration, private secrets and a suitable hosting boundary
- Walkthrough, screenshots and short design narrative for the portfolio
- Dark mode (low priority after core management/fulfillment flows)

## Intentionally later

Real payments, tax/shipping calculation, emails, customer personal information, multi-store inventory, delivery routing, supplier integrations and production retail use.
