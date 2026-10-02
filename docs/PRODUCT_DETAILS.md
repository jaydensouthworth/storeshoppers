# Product details and nonfood examples

Schema v8 adds full `/products/{id}` pages and manager-editable presentation metadata inside the same Go/HTMX application. The shelf description remains a short teaser. A full description, package label and package details can explain the individual product without changing how its inventory is sold.

## Metadata and editing

`product_details` is an optional one-to-one row. Existing products without a row have unspecified kind and no nutrition; public pages fall back to the existing shelf description and omit unprovided package information. Product kind is `unspecified`, `food` or `nonfood`, independent of editable department/type labels.

- Full description: up to 1,600 Unicode characters
- Package label: up to 80 characters
- Package details: up to 240 characters
- Optional nutrition serving label: 1–80 characters
- Illustrative nutrition energy: integer 0–10,000 kcal; fat, carbohydrate and protein: 0–10,000 g with at most one decimal; sodium: integer 0–1,000,000 mg

Nutrition is available only for an explicitly food product. Enabling it requires all fields; blank values are not converted to zero. Disabled panels are stored as NULL. Macros use integer tenths of a gram, never binary floating point. Every rendered nutrition panel is labelled as illustrative demo data, not a real product label. The demo includes no ingredients, allergen guarantees, health claims, daily-value calculations or dietary advice.

Text stays plain and is escaped by `html/template`. Manager create/edit uses one save for base product fields and details. A catalog-version conflict discards the stale draft and shows current data for deliberate review; validation errors retain escaped submitted values. The combined write and snapshot-based catalog event commit atomically. `SaveProduct` callers that do not supply details keep existing metadata. Metadata-only edits advance the catalog version while leaving price/inventory versions, holds and placed/working receipts untouched. Archive/restore retains the detail row and original product/code identities.

## Shopping behavior

Product pages share the storefront's current effective-price resolver and stock availability. Sale prices, regular prices and end times remain consistent with basket/checkout review rules. Counted add-to-basket forms reuse the same versioned reservation command, with ordinary POST/redirect and HTMX responses. If a manager archives the viewed product before Add, the HTMX POST renders the unavailable state with status 200 so the stale action is removed; direct unavailable GETs remain 404. Their return destination is assembled only from the product ID and bounded shelf filters; it is never an arbitrary URL.

One package is one counted item. A 75 g deodorant stick and a three-pack of razors use `each`, price basis 1 and step 1. Package grams or contained-unit counts do not alter quantities, rates, stock constraints or receipt arithmetic. Configured weighed products can be ordered in grams under the separate [weighted execution rules](WEIGHTED_PRODUCTS.md); package display metadata does not determine that behavior. Archived products return 404 publicly; manager archive review and historical receipts remain accessible through their existing guarded routes.

Ten fixed bundled illustrations include deodorant and razor choices. The allowlist remains server-enforced. There are no uploads, arbitrary URL fetches, image services, scanner/camera features, customer accounts, personal data or real payments in this phase.

## Migration and demo lifecycle

The additive schema-v8 migration runs in the existing immediate transaction. An established database gets empty detail, example-provenance and replay tables; no product rows, package claims, nutrition, stock, prices, archives or existing audit records are rewritten. Failure, including the final version marker, rolls the whole upgrade back. Ordinary reopen preserves the result.

Genuinely new databases and explicitly confirmed reset fixtures seed 72 products: the previous 70 food examples plus Everyday deodorant and Three-blade razors · 3 pack. Food packages and four optional nutrition panels are illustrative. Seeding metadata does not advance the original products' catalog versions, preserving the independent reset sale-example guards. Existing custom/partial/empty catalogs are never silently expanded by v8.

In demo mode, managers can visit `/manager/catalog/examples` to review the exact two nonfood products, price and initial stock. The CSRF-protected POST requires `confirm=1`, a replay key and the current preview hash. The reviewed action creates any missing active labels, products, metadata and reasoned stock adjustments in one transaction. The reserved example SKUs are `DEMO-DEODORANT-75G` and `DEMO-RAZORS-3PK`; unrelated products using either SKU and archived required labels block the action without side effects. Example provenance keeps repeat visits in an already-added state even after later manager edits or archive. Exact retries return the original successful outcome; they do not duplicate or restock anything.

The global reset keeps the existing explicit confirmation and complete private backup. Recovery includes detail values, nutrition, archived rows, example provenance and replay records alongside all prior catalog, order, hold, assignment and promotion data. Reset creates the same fresh product fixture before the existing confirmed-reset weekly-sale examples. Later restarts do not refresh examples.

## Verification

Focused tests cover metadata validation/version isolation, older caller compatibility, stale and injected-failure rollback, archive/restore, unchanged quotes/receipts, an independent populated-v7 upgrade, established empty/70-product databases, repeated reopen, confirmed reset and complete backup recovery. HTTP and fresh-build smoke checks pass. Run `./scripts/check.sh` for formatting, vet, race tests, build and all seven HTTP smoke suites; `python3 scripts/product_details_smoke.py` independently builds and checks this feature against a temporary database. Publication and browser acceptance are tracked in the [roadmap](ROADMAP.md).
