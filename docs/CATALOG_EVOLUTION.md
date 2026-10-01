# Catalog and fulfillment evolution

This is an implementation roadmap. Weighted checkout, substitutions and camera scanning are not current capabilities. Each phase must preserve existing IDs, sessions, baskets, stock allocations, receipt snapshots and picking state through versioned migrations.

## Version 2 boundary

The current foundation adds a picking quantity and version to order items. It does not reinterpret quantities, replace receipt rows, create tenant stores or reset inventory. Shared demo managers can prepare only their browser session’s orders; catalog inventory remains intentionally shared.

## Next foundation: configurable catalog

- Categories and optional product types become ordinary rows with stable IDs, normalized unique names, archive state and optimistic versions. They are configurable without rebuilding the application.
- Taxonomy labels never decide whether an item is weighed. Products separately declare counted (`each`) or weighed (`g`) measurement metadata.
- A stable merchant SKU is separate from any barcode identity. Do not reuse either identifier after archive.
- Manager create/edit/archive uses bounded fields and existing CSRF/access/version guards. Products referenced by receipts are archived rather than deleted. Archiving a category/type requires active products to be reassigned or archived first.
- Seed only during initial store creation. The current fixed-ID `INSERT OR IGNORE` seed behavior must not resurrect deliberately removed records after restart.
- Archives disappear from new sales; old receipts and pick tickets remain readable with their original item descriptions and prices.
- A price edit or archive between basket display and checkout must cause explicit review/reconfirmation. Use a price version or quote fingerprint rather than the inventory version, which changes for unrelated stock activity. Checkout must reject unavailable/archived items atomically.

A catalog foundation may store weighed-item metadata while keeping those products unavailable to order until weighted quantity and quote semantics are implemented. The interface must label this clearly rather than offering a nonfunctional weighted checkout.

## Integer measurement and pricing

Use integer quantities in the product’s base unit: units for counted goods and grams for weighed goods. Store weighed rates as cents per 1000 grams. Snapshot the unit, requested quantity, rate, price basis and rounded subtotal on the order.

For non-negative values, round once per line with `(grams × cents_per_kg + 500) / 1000`, using integer division and explicit multiplication bounds. At 349 cents/kg, 527 g costs 184 cents and 1250 g costs 436 cents. Never change an existing unit quantity into grams by multiplying all historical rows.

Mixed-unit views must not sum grams and units into one misleading item count. Show line progress and each line’s quantity/unit. The original 1–99 cart-quantity and 0–10000 stock constraints require a dedicated migration before supporting weighed quantities. Selling unit/price basis changes after use should be rejected unless a deliberate replacement product/version is introduced. Changing SQLite quantity/stock CHECK constraints requires the controlled table-rebuild migration procedure and a foreign-key integrity check, not just relaxed Go validation. Do not toggle foreign_keys inside an active transaction.

## Substitution and actual-weight reconciliation

Keep `order_items` and the placed `orders.total` as immutable requested snapshots. Introduce separate fulfillment/allocation rows keyed by the original `(order_id, requested_product_id)`, with actual product, reserved and picked base quantity, unit/rate/basis snapshots, resolution, final subtotal and version. Two requested lines may substitute to the same actual product; keying by replacement product would lose one of them.

Start with whole-line, same-unit substitutions. Snapshot the customer’s allowed substitution policy at checkout. A shared category alone never authorizes a replacement. Split and mixed-unit substitutions are later work.

Available stock was already deducted at checkout. Picking must not deduct the original again. A substitution transaction should validate order ownership, Picking state, expected version and policy; reserve the replacement with a conditional stock check; reconcile the original allocation; write the new allocation and reasoned audit; and update the final estimate. Failure must roll back every step. Actual weighed quantity reconciles only the difference from the reserved amount. For the original product, use the order’s snapped rate, not the latest catalog rate. Snapshot any permitted weight/price tolerance at checkout; require review beyond it. Replacements use their separately approved rate and price policy.

Physically missing stock cannot simply be returned to available inventory. Model release plus a linked shrinkage adjustment, or explicitly consume the lost allocation. Keep the final total nullable during picking and allow a final zero for an explicitly all-unavailable order; the original placed total remains unchanged. Freeze resolutions, allocations and the final total at Ready. Completed never deducts stock again. Cancellation/restocking needs an equally explicit policy and remains deferred.

## Barcode identities and the two-screen demo

A SKU is a merchant code; a barcode carries an identifier. A future `product_codes` table should retain product ID, scheme, raw value, normalized value, symbology and archive state, with uniqueness across the scheme/canonical value and no reuse after archive. Preserve leading zeroes.

Existing seed numbers are `legacy_placeholder` values, not validated retail UPC/GTINs. First generated demo labels should be plain Code 128 with an explicit local prefix such as `SHOPDEMO-000001`. Do not claim GS1 registration or label ordinary Code 128 as GS1-128. If GS1 aliases are added later, validate GTIN check digits and canonicalize equivalent GTIN-12/13/14 forms to a consistent 14-digit identity.

The eventual desktop “demo shop” page and product-label SVG route should read the same stored identifiers and render real, independently decodable bars with quiet zones and a human-readable value. Decorative receipt stripes are not scan labels. A separate phone web page opens a pick ticket and scans the desktop label; it remains part of this same Go/HTMX application.

A shelf code identifies a weighed product, not the bag’s weight. Begin with manual actual-gram entry. Later, optional package-label records can associate a server-issued token with a product and measured grams. Never trust barcode-supplied prices or invent a universal variable-measure prefix parser.

`ResolveProductCode(rawValue, detectedFormat)` should be read-only, returning a product identity and optional package measurement. Resolution does not mark an item picked. Manual entry and camera capture must call the same authorized picking command, with CSRF, scoped order ownership, expected line version and a request idempotency key. Two deliberate scans of the same unit barcode can be valid; deduplicate network retries by request key, not forever by barcode.

Camera UI is deferred. When built, require HTTPS, user-triggered camera access, runtime format support detection, a locally bundled maintained decoding fallback when necessary and manual entry throughout. Stop camera tracks on exit. Test the intended phones rather than assuming native BarcodeDetector support.

## Acceptance gates

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
