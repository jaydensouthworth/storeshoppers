# Product goals and roadmap

## Design intent and acceptance

Build a distinctive, believable corner-store demo that shows the storefront, manager tools and fulfillment working together. Keep the design intent visible through every phase, rather than reducing the project to a feature checklist.

- **Storefront:** an authored grocery-circular look using bright yellow, deep navy, red and crisp white, with strong typography, product illustrations and clear price hierarchy. Avoid generic cream/sage cards. It should feel like a specific shop, with useful shopping paths on mobile and desktop. Keep the desktop weekly circular compact enough that shoppers can see catalog controls/products without scrolling through a full-screen hero; review desktop and phone proportions separately.
- **Manager:** a task-based dashboard with purposeful navigation, focused editors, useful search/filter controls and clear feedback. Each control must do real work; printed lists and decorative metrics are not the goal.
- **Fulfillment:** broad manager control over working orders, with explicit reasons, audit history and transactional stock changes. Preserve the original receipt and show the final fulfillment total separately. Missing items must never silently become picked.
- **Portfolio boundary:** approximate a real shop while remaining an explicit fake-data demo, with no customer accounts, real personal information or payments. The catalog/inventory are shared; visitor baskets and orders are session-scoped. The manager password is only a demo gate.
- **Technical shape:** one repository and Go application, server-rendered HTML with HTMX progressive enhancement, SQLite, integer money and accessible responsive layouts. Keep ordinary HTML fallbacks and release small, tested phases.

Each phase is accepted only when the intended task works end to end, relevant invalid/stale/concurrent actions are tested, stock and receipt invariants hold, and desktop/mobile layouts remain usable. Check plain HTML behavior, keyboard access, labels, focus, contrast and empty/error states. A local implementation or passing unit test alone is not a live release.

## Verified live foundation

As of 2 October 2026: [live demo](https://instoreshopperexample-site-3az7di-fdadf8-2-25-70-220.sslip.io/), [published source](https://github.com/jaydensouthworth/storeshoppers/tree/b43a76e95cded1c8ce0fe1c851dfb7ab6b717dd7). This checkpoint contains 127 verified published files.

- 72 fake products; configurable categories/types; create, edit, archive and restore; dedicated editors and a manager sidebar
- Safe bundled illustration picker, immutable SKU and separate demo-local product codes; uploads, generated scan labels and camera scanning are not implemented
- Session baskets, atomic simulated checkout, replay protection, ownership checks, immutable receipt/measurement snapshots and stale-price review
- Persisted 15-minute stock holds, expiry/reacquisition, available/reserved counts, scoped practice baskets and audited manager basket overrides
- Plaintext checkout instructions capped at 500 characters; Picking progress percentages/counts and a distinct Ready status. The default Orders view already shows these states
- Reasoned stock audit, per-line picking, guarded readiness, customer status refresh and normal HTML fallback
- Scheduled fixed-cent sales, independent Featured choices, sale-aware price review and a weekly-sales storefront
- Simulated Shoppers roster with reasoned assignment, reassignment and task cancellation, truthful order progress and explicit unrecorded scanner telemetry
- Working-order quantity changes and substitutions, explicit partial finish/cancellation, stock disposition and immutable original receipts
- Versioned populated-data migrations and restart persistence; server-side validation, CSRF and non-negative transactional inventory
- The public known-password hint appears only when the configured password matches the demo value; this remains intentionally limited demo access

Verification for this release: 246 top-level Go tests, 21 JavaScript interaction/theme tests, 83.8% core coverage, race checks, vet, build, seven HTTP/restart smoke suites and [passing CI with Docker checks](https://github.com/jaydensouthworth/storeshoppers/actions/runs/36963942277). Live desktop and 402 CSS-pixel actions covered basket edits, visible expired-form feedback, direct Mark picked/start, partial picking, searched substitutions, working-item additions/removals and own-order cancellation with stock audit. Physical-phone testing and a fuller accessibility audit remain open.

## Ordered implementation backlog

**Current priority:** publish the tested schema-8 compatibility safeguard, then implement weighted execution and safe image uploads in isolated, reviewed phases. Mixed regular/sale Featured rows and persistent System/Light/Dark appearance are live and checked on desktop and narrow screens. The compact circular is live: 306 CSS pixels at 1366×768 and 1440×901, with catalog controls/products above the fold. Routine manager controls and visible stale-form recovery are verified; see [Manager workflows](MANAGER_WORKFLOWS.md). The missing hosting volume was added and exact sale/audit records survived a deployment. Continue compatibility safeguards, weighted execution, safe image uploads and remaining manager workflows in tested phases without overwriting visitor changes.

### 1. Manager usability and explicit reset

**Implemented in this iteration:** Products search by name/SKU/barcode, category/type/availability and active/archive filters, sorting, result counts, pagination and preserved list/editor context. Put Add product in the Products header, not the sidebar. Make Stock and Activity queries/filters functional, with selected-product quick adjustments, useful links and retained context. Keep the already-working Picking/Ready distinctions; the new regression check does not represent new user-facing behavior.

**Implemented and tested in this iteration:** an explicit demo-only reset page at `/manager/demo/reset`. GET only presents the confirmation; a CSRF-protected POST requires explicit confirmation of the global fake-data impact. Retain a private, consistent, recoverable snapshot, drain active requests safely, support rollback and invalidate affected sessions. Ordinary restarts continue to preserve data. This replaces the earlier reset-on-restart idea.

**Acceptance:** real queries narrow results accurately, controls remain usable at narrow widths, editor return links preserve context, and adjustment errors leave stock/audit consistent. Reset must preserve its recovery path and clearly state the shared impact before submission. Publish only after integrated checks and live verification.

### 2. Give managers broad control over confirmed working orders

**Implemented and live-verified in schema v5:** confirmed working-order additions/removals, quantity changes, whole-line counted substitutions and explicit reasoned manager overrides. Incomplete orders can finish with unavailable/cancelled remainders and deliberate stock disposition, while actual picked counts remain truthful. Whole-order cancellation and scoped order history are included. See [Manager order overrides](ORDER_OVERRIDES.md).

Preserve requested receipt lines and the placed total. Use separate fulfillment/allocation records, resolution history and a final fulfillment total. Checkout instructions retain the shopper’s guidance, while explicit, reasoned manager overrides remain permitted. A structured substitution-preference field is a later refinement; this increment supports counted same-unit changes. Missing physical stock must not produce phantom returns. Validate scope, state and versions; update allocations, stock, totals and audit in one transaction. Final totals stay nullable until Ready, allow an explicitly all-unavailable final zero and freeze at Ready. Ready remains frozen except collection; historical amendments/refunds and a Needs attention workflow remain future work. See [catalog and fulfillment evolution](CATALOG_EVOLUTION.md).

**Acceptance:** additions, removals, partial quantities, substitutions, override reasons, unavailable items and cancellation paths work end to end; failed/stale/repeated writes cannot oversell, double-deduct or double-return stock. Original receipts remain unchanged and final outcomes are understandable to both manager and customer.

### 3. Add the Shoppers workspace

**Implemented and live-verified in schema v6:** Shoppers appears directly below Orders. A fixed simulated roster has working, audited assign/reassign/cancel-task controls and progress derived from actual order picking records. Cancelling a task leaves its order open and unassigned, with stock and picked counts intact. Ready or completion ends the active task in the same transaction. One active task per order is enforced; a shopper can hold several orders. Demo ownership applies before workload counts, filters, history and pagination. Selected editors preserve filter context and recover failed drafts; the order ticket links to its current assignment. Closed-order searches use structured historical assignee records. Scan rates remain explicitly unrecorded until real scanner telemetry exists. See [Shoppers](SHOPPERS.md).

**Acceptance:** task changes preserve order/stock integrity and session scope, expose the current assignment clearly, and remain recoverable after interruption.

### 4. Manage promotions and make weekly sales drive the homepage

Implemented and live-verified in schema v7: Promotions and Featured management with exact integer-cent sale prices, explicit UTC intervals, overlap guards, audit and dedicated editors. Weekly-sales hero, Shop all sales and featured shelves use active data with an honest empty fallback. Customer and first-time working additions use the same effective price rules, while placed and retained working snapshots remain immutable. Demo examples require an explicit preview/confirmation or confirmed global reset. See [Promotions](PROMOTIONS.md).

**Acceptance:** active/scheduled/expired promotions display consistently; stale sale prices require review and totals match the current valid quote.

### 5. Complete individual product pages and catalog presentation

Implemented and live-verified in schema v8: full product pages, manager-editable body and package details, optional clearly illustrative food nutrition, and counted deodorant/razor examples with bundled illustrations. Existing catalog migration invents no metadata or stock; a demo-only reviewed action can add the two examples without overwriting edits. Details remain separate from selling-unit execution and immutable order snapshots. See [Product details](PRODUCT_DETAILS.md). Constrained local uploads remain later work; no arbitrary server-side URL fetching or unsanitized SVG.

**Acceptance:** product details, available stock, units and basket actions agree; archived/referenced products retain their history, and both food and non-food items render sensibly on mobile.

### 6. Implement weighted selling and actual-weight reconciliation

Gram measurement metadata already exists, but weighted checkout and actual-weight reconciliation do not. Use a dedicated quantity/pricing migration with integer cents and grams, bounded line rounding, immutable unit/rate snapshots and unit-specific totals. Implement weighted checkout before reconciling actual picked weight, with explicit tolerance/review and transactional allocation changes. Do not mix grams and units into one misleading item count.

**Acceptance:** populated migrations, rounding/bounds, competing orders and above/below-requested weights preserve history and stock; failed reconciliation rolls back every effect.

### 7. Persistent dark mode

Implemented and live-verified: System, Light and Dark choices on storefront and manager, persistent explicit choice, system-default behavior, HTMX/history continuity and a no-JavaScript system fallback. Desktop and 402/321 CSS-pixel layouts preserve readable controls and the bold palette. See [Appearance](DARK_MODE.md). Continue checking new manager controls and product imagery in both themes.

### 8. Build the later two-screen barcode demo

The barcode identity foundation is complete; legacy seed numbers are placeholders, not validated retail UPC/GTINs. Generate independently decodable demo-local Code 128 labels from stored identities, then add a desktop fake-shop label display and phone-friendly pick list. Begin with manual entry; add user-triggered camera scanning only after HTTPS and intended-device testing. Keep these screens in the same app.

Both entry methods must use the same authorized picking service, version checks and idempotent request handling. Test unknown/wrong/archived codes, over-picking, retries, competing writes, permission failures and interrupted sessions. Preserve leading zeroes and distinguish product identity from package weight. No camera workflow, real retail barcode support or scan-rate analytics is claimed today. Named roles and an assignment policy are required before access extends beyond the bounded fake-data demo.

### 9. Continue release hardening and portfolio evidence

Keep versioned migrations, recoverable backup/restore and deliberate cleanup policy. Never delete referenced catalog or receipt history or perform a silent global purge. Retain the completed Docker/CI and persistence checks; extend dependency/security scanning, load checks, accessibility and physical-phone QA. Revisit TLS, trusted-proxy configuration, operational logs and private deployment secrets as the deployment evolves. Before real-world use, replace the shared gate with named identities and explicit role permissions. Capture a short walkthrough, screenshots and design narrative for the portfolio.

## Outside the current demo

Real payments, tax/shipping calculation, emails, customer personal information, multi-store inventory, delivery routing, supplier integrations and production retail use remain outside this scope.
