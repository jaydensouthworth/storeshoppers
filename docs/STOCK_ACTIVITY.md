# Stock and activity workspace

`/manager/stock` is a task-focused inventory workspace. Twelve products per page can be searched by name, SKU, department or product type, filtered by department, stock state, archive state and selling unit, and sorted by name, available quantity, reserved quantity or department. Quantities keep their item/gram labels; low stock means fewer than eight **counted items** and is not applied to grams. Available quantities exclude basket allocations. Summary cards include active products only, and counted-unit summaries exclude grams.

Selecting a product opens one reasoned adjustment form. There are no per-row write forms. The selection remains explicit across filter changes and pagination, so it can be outside the currently filtered list. Closing it removes the selection; changing list filters does not write stock. Archived products can be reviewed and linked back to catalog restoration but cannot be adjusted.

## Navigation and writes

- `GET /manager/stock`: `q`, `department` (category ID), `stock` (`available`, `low`, `out`, `reserved`), `lifecycle` (`all`, `archived`; default active), `unit` (`each`, `g`), `sort` (`name`, `name_desc`, `available`, `available_desc`, `reserved`, `department`), `page`, and optional `product`
- `GET /manager/stock?view=activity`: `action`, `activity_product`, `from`, `to`, and `activity_page`; both dates are inclusive UTC dates in YYYY-MM-DD format
- `POST /manager/inventory`: existing CSRF, `product_id`, `version`, signed `delta`, reason, and validated navigation context

Sort expressions are whitelisted, query values are bound parameters, search is bounded, and page numbers are clamped to real result pages. Inventory and activity preserve independent filter state. Invalid or reversed dates show an error and no history, rather than silently broadening the query. Links are built from validated query values, not submitted return URLs.

An adjustment still calls the existing atomic stock transaction. It preserves nonnegative available stock, the available-plus-reserved 10,000-unit cap, the optimistic inventory version and transactional audit insertion. Recoverable errors retain bounded, escaped submitted values next to current stock and the latest version; retry requires another explicit submission. No order snapshots are updated. HTMX returns the workspace with a canonical push URL after success; ordinary HTML uses POST/redirect/GET. Both paths work without client-side business logic.

## History and privacy

Activity reads existing `adjustments`, `catalog_events` and `basket_events`, plus actual order creation records. It does not add or fabricate historical events. Each event is labeled by its recorded action; an order row is **Order placed**, not an inferred status transition. Stock deltas have their recorded unit and reason. Product names on stock adjustments come from the current catalog because the old adjustment schema did not snapshot names. Catalog event names and order receipts retain their existing snapshots.

Before/after inventory balances, precise cross-source ordering within the same minute, order status transitions, automatic hold-expiry events and customer basket edit events were not recorded and are not reconstructed. A specific product filter includes its adjustment/product-catalog events and orders containing that product. It excludes label changes and historical basket events because those rows do not have structured product IDs. Matching a product number in old free-text details or using today's basket contents would give misleading historical matches.

The shared catalog and stock remain global fake data. In `DEMO_MODE`, basket and order ownership is enforced inside the source queries **before** filtering, counting, pagination or constructing resource links. Normal local mode can show all order/basket records. Session IDs are neither selected nor presented as actors. The shared manager gate has no named staff identity. Existing target handlers enforce their own authorization when an activity link is opened.

No schema migration or seed change is required. Nothing in this workspace adds analytics, payments, external fulfillment or physical-store operations.
