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

## Next foundation: a flexible catalog

Add configurable category and product-type records, product creation/editing/archiving, stable barcode identities and explicit measurement metadata. Archive referenced records rather than breaking receipt history. Counted quantities and weighed quantities must have explicit units; use integer grams and integer cents, never floating point. Implement this as a separate migration after the current picking foundation.

Weighted checkout and substitutions need immutable quantity/price-basis snapshots and a separate order-change ledger with transactional stock reconciliation. Merely adding fields does not mean these checkout behaviors exist.

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
- Product creation/editing and a price update policy if needed for the story
- Cancellation and restocking policy; substitutions only if they support the demo
- Dependency/security scanning, load checks, fuller accessibility audit and real-device phone QA
- TLS, deployment configuration, trusted proxy strategy and operational logs
- Container build and persistence verification in CI
- Deployment configuration, private secrets and a suitable hosting boundary
- Walkthrough, screenshots and short design narrative for the portfolio

## Intentionally later

Real payments, tax/shipping calculation, emails, customer personal information, multi-store inventory, delivery routing, supplier integrations and production retail use.
