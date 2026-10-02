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

As of 2 October 2026: [live demo](https://instoreshopperexample-site-3az7di-fdadf8-2-25-70-220.sslip.io/), [published header foundation](https://github.com/jaydensouthworth/storeshoppers/tree/041eff9b49dce0f08daa1cb9c55131dc6244f7ac). That checkpoint contains 178 verified published files; subsequent release evidence is recorded below.

- 72 fake products; configurable categories/types; create, edit, archive and restore; dedicated editors and a manager sidebar
- Bundled illustration picker and bounded, previewed JPEG/PNG uploads; immutable SKU and separate demo-local product codes; generated scan labels and camera scanning remain later work
- Session baskets, atomic simulated checkout, replay protection, ownership checks, immutable receipt/measurement snapshots and stale-price review
- Persisted 15-minute stock holds, expiry/reacquisition, available/reserved counts, scoped practice baskets and audited manager basket overrides
- Plaintext checkout instructions capped at 500 characters; Picking progress percentages/counts and a distinct Ready status. The default Orders view already shows these states
- Reasoned stock audit, per-line picking, guarded readiness, customer status refresh and normal HTML fallback
- Scheduled fixed-cent sales, independent Featured choices, sale-aware price review and a weekly-sales storefront
- Simulated Shoppers roster with reasoned assignment, reassignment and task cancellation, truthful order progress and explicit unrecorded scanner telemetry
- Working-order quantity changes and substitutions, explicit partial finish/cancellation, stock disposition and immutable original receipts
- Versioned populated-data migrations and restart persistence; server-side validation, CSRF and non-negative transactional inventory
- The public known-password hint appears only when the configured password matches the demo value; this remains intentionally limited demo access

The schema12 foundation passed 349 top-level Go tests, 23 JavaScript checks,
race/vet/build and eleven HTTP/restart suites, plus [CI and Docker persistence
checks](https://github.com/jaydensouthworth/storeshoppers/actions/runs/37005228199).
Live desktop and 391 CSS-pixel checks covered gram catalog/sales, mixed baskets,
quantity edits, reviewed actual weight, immutable receipts, private review holds,
queue filters, inline picking and owned-order cancellation/restocking. The
quantity-field follow-up is verified live. Physical-phone testing and a fuller
accessibility audit remain open.

## Ordered implementation backlog

**Live schema13 release:** simulated staff management includes scoped roster editing,
explicit availability, assignment capacity and recoverable archive/restore. Basket
instructions and quantity drafts survive updates/renewals, and checkout refuses to
ignore an unapplied quantity. [Published source](https://github.com/jaydensouthworth/storeshoppers/tree/a3ba30a2f93a8674d1ed7c9126193402a9b93a2d)
and [CI](https://github.com/jaydensouthworth/storeshoppers/actions/runs/37036138721)
passed 372 top-level Go tests, 54 JavaScript checks, race/vet/build, twelve real
HTTP/restart suites and Docker persistence checks. Live desktop and 388 CSS-pixel
workflows passed. All 72 products, four sales and five featured entries were
preserved; only the synthetic QA order/profile were cleaned up.

**Deployed foundation, with connected-browser acceptance still incomplete:** schema14 phone-first
workspace, one-time assignment pairing, real product Code 128 labels,
camera/photo/manual recognition and explicit counted-pick confirmation. Keep the
public fake-data boundary and independent visitor/phone authorization. See
[Handheld demo](HANDHELD.md) and the [expanded sourced project plan](https://chatgpt.com/space/page_6abd4f41ae448191a351b73099da8658).
The foundation passed [CI](https://github.com/jaydensouthworth/storeshoppers/actions/runs/37049056947), and live welcome/label desktop/narrow checks passed. Chromium blocked the pairing POST with ERR_BLOCKED_BY_CLIENT; no cause or permission requirement was inferred, no bypass was attempted, and connected camera/pick browser acceptance remains open. Physical S23 Ultra optics are a distinct check.

**Deployed phone weights/reports:** [ce0737e](https://github.com/jaydensouthworth/storeshoppers/commit/ce0737ea8e802d589c0db0a5a46c4dbd2c683d2a) passed 419 Go tests, 107 JavaScript checks, 14 process suites, independent review and [CI](https://github.com/jaydensouthworth/storeshoppers/actions/runs/37052613149). Changed assets match live and all 77 storefront records remain unchanged. The connected-browser/physical-phone acceptance limitation still applies.

**Current candidate:** schema15 independent customer/shopper message threads, strict owner/assignment scope, separate revisions and immutable attribution, bounded plain text/history/rates/storage, and explicit uncertain-send recovery. Message traffic must never invalidate pick/weight reviews, reconcile baskets, copy manager-only notes or imply substitution consent. See [Order messages](ORDER_MESSAGES.md). Aggregate checks, independent review, migration/process tests and release verification remain required before calling this candidate deployed.

### 1. Manager usability and explicit reset

**Implemented in this iteration:** Products search by name/SKU/barcode, category/type/availability and active/archive filters, sorting, result counts, pagination and preserved list/editor context. Put Add product in the Products header, not the sidebar. Make Stock and Activity queries/filters functional, with selected-product quick adjustments, useful links and retained context. Keep the already-working Picking/Ready distinctions; the new regression check does not represent new user-facing behavior.

**Implemented and tested in this iteration:** an explicit demo-only reset page at `/manager/demo/reset`. GET only presents the confirmation; a CSRF-protected POST requires explicit confirmation of the global fake-data impact. Retain a private, consistent, recoverable snapshot, drain active requests safely, support rollback and invalidate affected sessions. Ordinary restarts continue to preserve data. This replaces the earlier reset-on-restart idea.

**Acceptance:** real queries narrow results accurately, controls remain usable at narrow widths, editor return links preserve context, and adjustment errors leave stock/audit consistent. Reset must preserve its recovery path and clearly state the shared impact before submission. Publish only after integrated checks and live verification.

### 2. Give managers broad control over confirmed working orders

**Implemented and live-verified in schema v5:** confirmed working-order additions/removals, quantity changes, whole-line counted substitutions and explicit reasoned manager overrides. Incomplete orders can finish with unavailable/cancelled remainders and deliberate stock disposition, while actual picked counts remain truthful. Whole-order cancellation and scoped order history are included. See [Manager order overrides](ORDER_OVERRIDES.md).

Preserve requested receipt lines and the placed total. Use separate fulfillment/allocation records, resolution history and a final fulfillment total. Checkout instructions retain the shopper’s guidance, while explicit, reasoned manager overrides remain permitted. A structured substitution-preference field is a later refinement; this increment supports counted same-unit changes. Missing physical stock must not produce phantom returns. Validate scope, state and versions; update allocations, stock, totals and audit in one transaction. Final totals stay nullable until Ready, allow an explicitly all-unavailable final zero and freeze at Ready. Ready remains frozen except collection; historical amendments/refunds remain future work. Explicit manager holds are deployed in the schema12 foundation; see [Order review and queue](ORDER_ATTENTION.md). See [catalog and fulfillment evolution](CATALOG_EVOLUTION.md).

**Acceptance:** additions, removals, partial quantities, substitutions, override reasons, unavailable items and cancellation paths work end to end; failed/stale/repeated writes cannot oversell, double-deduct or double-return stock. Original receipts remain unchanged and final outcomes are understandable to both manager and customer.

### 3. Add the Shoppers workspace

**Implemented and live-verified in schema v6:** Shoppers appears directly below Orders. The original three simulated shoppers have working, audited assign/reassign/cancel-task controls and progress derived from actual order picking records. Cancelling a task leaves its order open and unassigned, with stock and picked counts intact. Ready or completion ends the active task in the same transaction. One active task per order is enforced; a shopper can hold several orders. Demo ownership applies before workload counts, filters, history and pagination. Selected editors preserve filter context and recover failed drafts; the order ticket links to its current assignment. Closed-order searches use structured historical assignee records. Scan rates remain explicitly unrecorded until real scanner telemetry exists. See [Shoppers](SHOPPERS.md).

**Schema13 roster increment implemented:** add/edit fictional shopper names and initials;
Available, On break and Off shift states; active-order limits; guarded
archive/restore; scoped roster search and history. Existing identities begin with
no set limit to preserve their prior behavior; new custom shoppers start at three
active orders. Unavailable/full/archived shoppers cannot receive new assignments.
Changing availability or lowering capacity preserves current work; archive requires
reassigning, cancelling or finishing active tasks first. Demo profiles and custom
identities belong to the visitor session, including before counts and errors.
Names saved on assignments/events preserve historical identity across profile edits.

**Acceptance:** task changes preserve order/stock integrity and session scope, expose the current assignment clearly, and remain recoverable after interruption.

### 4. Manage promotions and make weekly sales drive the homepage

Implemented and live-verified in schema v7: Promotions and Featured management with exact integer-cent sale prices, explicit UTC intervals, overlap guards, audit and dedicated editors. Weekly-sales hero, Shop all sales and featured shelves use active data with an honest empty fallback. Customer and first-time working additions use the same effective price rules, while placed and retained working snapshots remain immutable. Demo examples require an explicit preview/confirmation or confirmed global reset. See [Promotions](PROMOTIONS.md).

**Acceptance:** active/scheduled/expired promotions display consistently; stale sale prices require review and totals match the current valid quote.

Schema12 extends sales and Featured to gram products, with saved cents-per-kilogram offer bases, explicit Choose grams actions, and the same stale-quote/immutable-receipt rules.

### 5. Complete individual product pages and catalog presentation

Implemented and live-verified in schema v8: full product pages, manager-editable body and package details, optional clearly illustrative food nutrition, and counted deodorant/razor examples with bundled illustrations. Existing catalog migration invents no metadata or stock; a demo-only reviewed action can add the two examples without overwriting edits. Details remain separate from selling-unit execution and immutable order snapshots. See [Product details](PRODUCT_DETAILS.md). The schema9 image foundation is integrated: bounded JPEG/PNG previews, confirmed attachment, normalized immutable variants and retained recovery storage. No arbitrary URL fetching or SVG upload.

**Acceptance:** product details, available stock, units and basket actions agree; archived/referenced products retain their history, and both food and non-food items render sensibly on mobile.

### 6. Implement weighted selling and actual-weight reconciliation

Implemented in schema10: gram checkout, bounded once-per-line rounding, separate requested/allocated/measured quantities, explicit actual-weight amount/stock preview, same-unit substitutions and unit-aware totals. See [Weighted products](WEIGHTED_PRODUCTS.md). Migration/overlap tests protect data; live desktop and narrow workflow checks verify the release. No camera/scanning UI is included.

**Acceptance:** populated migrations, rounding/bounds, competing orders and above/below-requested weights preserve history and stock; failed reconciliation rolls back every effect.

### 7. Persistent dark mode

Implemented and live-verified: System, Light and Dark choices on storefront and manager, persistent explicit choice, system-default behavior, HTMX/history continuity and a no-JavaScript system fallback. Desktop and 402/321 CSS-pixel layouts preserve readable controls and the bold palette. See [Appearance](DARK_MODE.md). Continue checking new manager controls and product imagery in both themes.

### 8. Build the connected phone-first shopping workflow

Authorized and in progress in schema14: a visually separate `/handheld/` workspace
within the same Go app, clear storefront entry, short-lived one-time pairing,
assignment-scoped grants, real Code 128 labels, explicit user-triggered camera and
photo decoding, manual fallback and shared counted-pick transactions. Scan recognition
never implies a pick. Private manager notes and unrelated visitor orders remain
inaccessible. See [Handheld architecture and acceptance](HANDHELD.md).

The current increment adds phone measured-weight review and explicit item reports. The staged follow-ups are in-app customer/shopper messaging, structured substitution preferences and approvals,
unavailable handling, staging/pickup handoff, then batch operations. Chat has its own
revision and does not itself authorize a substitution. Ready closes the picking task,
so staging requires an explicit new authority boundary. Real notifications, customer
PII, payments and production employee accounts remain outside this fake-data demo.

Acceptance includes wrong/unknown/archived codes, concurrency, duplicate/reordered
commands, offline/retry/Back, grant expiry/revocation, secure cross-device separation,
stock/receipt invariants and real process restart. The actual Samsung S23 Ultra camera
must be tested against desktop labels; simulated media tests cannot establish that.

### 9. Continue release hardening and portfolio evidence

Keep versioned migrations, recoverable backup/restore and deliberate cleanup policy. Never delete referenced catalog or receipt history or perform a silent global purge. Retain the completed Docker/CI and persistence checks; extend dependency/security scanning, load checks, accessibility and physical-phone QA. Revisit TLS, trusted-proxy configuration, operational logs and private deployment secrets as the deployment evolves. Before real-world use, replace the shared gate with named identities and explicit role permissions. Capture a short walkthrough, screenshots and design narrative for the portfolio.

## Outside the current demo

Real payments, tax/shipping calculation, emails, customer personal information, multi-store inventory, delivery routing, supplier integrations and production retail use remain outside this scope.

### 7. Explicit manager review and a usable order queue

Implemented in schema11: private reasoned hold/release/internal-note
commands, customer-safe history queries, factual held/unassigned filters,
reference/product/SKU search, accurate scoped counts and stable 20-row pages.
Working items, weights and assignments stay available while held; Ready/partial
finish require release, and cancellation closes the hold atomically. Queue
context survives all ticket forms and the assignment round-trip. Browser visual
QA complements the migration, scope and command tests. No analytics, risk score,
physical logistics, payments, refunds or customer accounts were added.
