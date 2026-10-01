# Catalog and fulfillment evolution

This document distinguishes the implemented version 3 catalog foundation from later fulfillment work. Weighted checkout, substitutions, generated labels and camera scanning are not current capabilities. Each later phase must preserve existing IDs, sessions, baskets, stock allocations, receipt snapshots and picking state through versioned migrations.

## Preserved version 2 boundary

The current foundation adds a picking quantity and version to order items. It does not reinterpret quantities, replace receipt rows, create tenant stores or reset inventory. Shared demo managers can prepare only their browser session’s orders; catalog inventory remains intentionally shared.

## Completed version 3: configurable catalog

- Manager `/manager/catalog` supports product create/edit/archive/restore and category/type create/rename/archive/restore inside the existing monolith. Products can reuse eight bundled illustrations; arbitrary image upload is deferred.
- Categories and optional types are ordinary rows with stable IDs, normalized unique names, archive state and optimistic versions. Product assignment follows IDs through renames; taxonomy labels never decide whether an item is weighed.
- Products retain their IDs and have an immutable, case-insensitive merchant SKU. User SKUs are bounded ASCII letters/digits/dashes/underscores; a blank create assigns a `SHOPDEMO-` SKU. That namespace is reserved for generated identities. Neither SKUs nor normalized taxonomy names can be reused after archive.
- Product fields, unit metadata and taxonomy assignments are bounded. New products always start with zero stock; adding stock requires the separate reasoned inventory adjustment. Catalog and inventory edits use independent optimistic versions.
- Receipt snapshots retain names, SKUs, unit rates, selling-unit metadata, quantities and subtotals. Archived products remain in history and can still be picked against an existing ticket. Active taxonomy references prevent archive until products are reassigned or archived. Taxonomy restore preserves the archived label’s ID/name and does not silently restore products. Product restore is an explicit, separately version-guarded command requiring active labels; it preserves stock/SKU/code IDs and advances the price quote version. It never restores unrelated products.
- Successful catalog commands write a transactional change-log event; a failed audit write rolls back the catalog change. The manager sees the latest 20 snapshot-based events. The shared demo gate does not identify individual actors.
- A basket snapshot includes its quote. Checkout checks that quote inside the same atomic placement transaction. Changed prices require explicit review and a new submission; unavailable/archived products block checkout and remain removable. Stock-only activity does not change the financial quote. Replay of an already committed checkout key still returns the original owned order.
- Fresh databases seed 70 fake products inside initialization. The one-time v3 migration expands only an exact original eight-product catalog, requiring total count eight plus all original IDs and placeholder codes. It preserves manager edits to those records; the same eight with any additional custom product receive no expansion. Partial/custom legacy catalogs and established empty schemas receive no seed products. Reopening does not recreate removed records.
- `product_codes` preserves existing numeric strings as `legacy_placeholder` and separately stores unique `demo_local` identities for ordinary Code 128. The read-only local-code resolution boundary does not change picking state or claim retail GTIN validation. Generated labels and camera UI remain deferred.

Counted products use `each`, basis 1 and step 1. Weighed metadata uses `g`, basis 1000 and step 1–1000, but weighed products are visibly unavailable for sale. The current 1–99 cart quantity and 0–10000 stock constraints have not been reinterpreted. Neither weighted checkout nor integer weighted line pricing is enabled. Unit/price-basis conversion after receipt use is rejected. Schema v4 implements timed basket reservations, scoped manager basket overrides and customer special instructions; see [Basket reservations](BASKET_RESERVATIONS.md). Actual-weight reconciliation and manager substitutions remain separate milestones.

## Deferred: integer measurement and pricing

Use integer quantities in the product’s base unit: units for counted goods and grams for weighed goods. Store weighed rates as cents per 1000 grams. Snapshot the unit, requested quantity, rate, price basis and rounded subtotal on the order.

For non-negative values, round once per line with `(grams × cents_per_kg + 500) / 1000`, using integer division and explicit multiplication bounds. At 349 cents/kg, 527 g costs 184 cents and 1250 g costs 436 cents. Never change an existing unit quantity into grams by multiplying all historical rows.

Mixed-unit views must not sum grams and units into one misleading item count. Show line progress and each line’s quantity/unit. The original 1–99 cart-quantity and 0–10000 stock constraints require a dedicated migration before supporting weighed quantities. Selling unit/price basis changes after use should be rejected unless a deliberate replacement product/version is introduced. Changing SQLite quantity/stock CHECK constraints requires the controlled table-rebuild migration procedure and a foreign-key integrity check, not just relaxed Go validation. Do not toggle foreign_keys inside an active transaction.

## Deferred: substitution and actual-weight reconciliation

Keep `order_items` and the placed `orders.total` as immutable requested snapshots. Introduce separate fulfillment/allocation rows keyed by the original `(order_id, requested_product_id)`, with actual product, reserved and picked base quantity, unit/rate/basis snapshots, resolution, final subtotal and version. Two requested lines may substitute to the same actual product; keying by replacement product would lose one of them.

Start with whole-line, same-unit substitutions. Snapshot the customer’s allowed substitution policy at checkout. A shared category alone never authorizes a replacement. Split and mixed-unit substitutions are later work.

Available stock was already reserved in the basket and that allocation was consumed at checkout. Picking must not deduct the original again. A substitution transaction should validate order ownership, Picking state, expected version and policy; reserve the replacement with a conditional stock check; reconcile the original allocation; write the new allocation and reasoned audit; and update the final estimate. Failure must roll back every step. Actual weighed quantity reconciles only the difference from the reserved amount. For the original product, use the order’s snapped rate, not the latest catalog rate. Snapshot any permitted weight/price tolerance at checkout; require review beyond it. Replacements use their separately approved rate and price policy.

Physically missing stock cannot simply be returned to available inventory. Model release plus a linked shrinkage adjustment, or explicitly consume the lost allocation. Keep the final total nullable during picking and allow a final zero for an explicitly all-unavailable order; the original placed total remains unchanged. Freeze resolutions, allocations and the final total at Ready. Completed never deducts stock again. Cancellation/restocking needs an equally explicit policy and remains deferred.

## Barcode identities and the two-screen demo

A SKU is a merchant code; a barcode carries an identifier. The implemented `product_codes` table retains product ID, scheme, raw value, normalized value, symbology and archive state, with uniqueness across the scheme/canonical value and no reuse after archive. Preserve leading zeroes.

Existing seed numbers are `legacy_placeholder` values, not validated retail UPC/GTINs. First generated demo labels should be plain Code 128 with an explicit local prefix such as `SHOPDEMO-000001`. Do not claim GS1 registration or label ordinary Code 128 as GS1-128. If GS1 aliases are added later, validate GTIN check digits and canonicalize equivalent GTIN-12/13/14 forms to a consistent 14-digit identity.

The eventual desktop “demo shop” page and product-label SVG route should read the same stored identifiers and render real, independently decodable bars with quiet zones and a human-readable value. Decorative receipt stripes are not scan labels. A separate phone web page opens a pick ticket and scans the desktop label; it remains part of this same Go/HTMX application.

A shelf code identifies a weighed product, not the bag’s weight. Begin with manual actual-gram entry. Later, optional package-label records can associate a server-issued token with a product and measured grams. Never trust barcode-supplied prices or invent a universal variable-measure prefix parser.

`ResolveProductCode(rawValue, detectedFormat)` is currently a read-only local Code 128 resolver returning a product identity. Optional package measurements, GS1 aliases and retail-code validation remain deferred. Resolution does not mark an item picked. Manual entry and camera capture must call the same authorized picking command, with CSRF, scoped order ownership, expected line version and a request idempotency key. Two deliberate scans of the same unit barcode can be valid; deduplicate network retries by request key, not forever by barcode.

Camera UI is deferred. When built, require HTTPS, user-triggered camera access, runtime format support detection, a locally bundled maintained decoding fallback when necessary and manual entry throughout. Stop camera tracks on exit. Test the intended phones rather than assuming native BarcodeDetector support.

## Acceptance gates

Gates 1–3 and 5 are covered by the current catalog foundation. The remaining weighted, substitution and label gates apply to later milestones; existing counted-item oversell and session-isolation tests remain mandatory.

1. Upgrade populated older databases and reopen repeatedly without changing receipt amounts, identifiers, sessions or fulfillment state
2. Add/reassign/archive categories, types and products without code changes or restart resurrection
3. Reject duplicate SKU/canonical identifiers and prevent reuse of archived identities
4. Test integer price rounding, bounds and mixed-unit presentation
5. Reconfirm stale quotes and reject archived products without partial inventory changes
6. Prevent competing orders from overselling the last units or grams
7. Roll back stale/failed substitutions and weight changes across allocations, stock, estimates and audit
8. Preserve original and final receipt snapshots through later catalog edits
9. Avoid phantom stock on missing-item resolution and repeat writes
10. Resolve wrong/unknown/archived codes safely; preserve cross-session privacy
11. Independently decode generated labels to the stored payload before adding a camera workflow

## References

- [GS1-128 and Code 128 distinction](https://www.gs1belu.org/en/gs1-128)
- [GS1 retail guidance, including variable measure](https://ref.gs1.org/guidelines/2d-in-retail/)
- [Barcode detection API specification](https://wicg.github.io/shape-detection-api/#barcode-detection-api)
- [Camera access and secure contexts](https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getUserMedia)
- [SQLite controlled table changes](https://www.sqlite.org/lang_altertable.html)
- [SQLite foreign key pragma](https://www.sqlite.org/pragma.html#pragma_foreign_keys)
