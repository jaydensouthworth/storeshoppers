# Manager review holds and the Orders queue

Schema11 adds manager review holds and queue tools while retaining the existing
image, weighted-quantity and receipt contracts.

## Holds and notes

An explicit hold is an overlay on Placed/Picking, never a fifth fulfillment
state. Place hold needs a trimmed, valid UTF-8, control-free reason of 3–240
characters. HTML-like text is ordinary escaped text. Release accepts an optional
note; blank records “Manager released the review hold”. Add an internal note is
a separate, optional disclosure with its own required text. Ready/Completed
orders are read-only for these commands.

Hold, release and note use the existing order_version and command-key/hash in
one immediate, scoped transaction. Scope precedes validation, stale errors and
recovery. Exact retries replay; a changed payload under the same key or stale
version refuses. The attention hash has its own domain prefix to prevent a key
from colliding with another command family. Hold/release/note never change
status, picked quantities, allocation, stock or original receipts. A fresh hold
requires no active hold; a release requires one. Reholding after release starts
a new reason/time with the earlier private history retained.

Held orders still permit working additions/removals/substitutions, counted picks,
actual-weight preview/confirmation and shopper assignment. Ready and partial
finish refuse with no command effects and link to review/release. Whole-order
cancellation still applies its explicit stock disposition once and closes the
hold and active assignment in the same transaction. Override expiry housekeeping
now lives inside that transaction: a refused command rolls it back. Ordinary
page rendering retains existing basket expiry housekeeping.

## Private reads

order_events.visibility defaults to customer, preserving every existing event.
Only the new review commands write internal events. Store.Order is always
customer-safe, even when its allOrders scope flag permits another order.
Customer SQL returns only a Held boolean, blank reason/time and customer-visible
events; filtering occurs before the history LIMIT. Store.ManagerOrder explicitly
opts into private data after the manager HTTP gate. A browser manager grant does
not turn /orders or its HTMX tracker into manager views.

The customer tracker says “Under manager review”; no internal text, time or
history is rendered. Manager events carry a Manager only label. Demo access is
session-scoped before selected-order errors, counts, pages and draft rendering;
normal manager access retains its existing global scope. This remains a shared
fake-data demo gate, not production identity or tenant isolation.

## Queue and navigation

Default All orders includes Placed, Picking, Ready and Completed/cancelled.
The manager can combine state, active hold and active assignment filters, search
reference/original or working product name/SKU, and sort newest, oldest or
reference. An active hold means a nonempty explicit reason on Placed/Picking.
Unassigned is factual absence of an active assignment, including closed orders
when the state filter includes them; it is not an age/risk score. The Needs
attention count and Awaiting completion metric cover the complete authorized
scope; the latter includes Ready awaiting collection. Filtered totals/pages use
the same scoped snapshot as row summaries. Baskets remain a separate workspace.

Twenty rows per page use stable ID tie-breaks, bounded page numbers and
out-of-range clamping. Customer order lists also paginate beyond 100 orders with
customer-safe rows. There is no hidden LIMIT 100 manager count. Search is bounded
to 100 valid display characters; state/held/assigned/sort are fixed allowlists.
URLs are built with url.Values; templates escape both links and hidden fields.
The queue parameter is state so it cannot collide with advance's status field.

All six query fields survive tickets, product search and selection, item and
status changes, review commands, weight preview/confirm/cancel, refresh and back.
The Shoppers round-trip carries a separately parsed/normalized nested queue
context, never an arbitrary return URL. Full-page and HTMX actions use the same
commands. Failed review drafts survive with an explicit unsaved notice, including
a stale hold/release whose current state changed. Session/CSRF interruption uses
the existing visible recovery behavior and never automatically resubmits. A partial-finish draft interrupted by a
concurrent hold stays visible; bounded display-only fields travel through hold
release and restore the finish editor with fresh command versions. Release does
not finish the order. Assignment roster/view/history/clear links preserve the
originating queue as well as the direct assignment form.

The UI follows existing panel/disclosure patterns, labelled controls, minimum
44px actions, responsive one/two-column narrow filters and inherited theme
colors. Structural/form validation is automated. Desktop/narrow visual checks
are separate from the automated structure and behavior checks.

## Migration and verification

Migration10→11 adds orders.attention_reason and attention_since with empty/zero
defaults, order_events.visibility with customer default, queue indices and v11
writer fences. It does not rebuild prior tables or reinterpret image blobs,
quotas, weights, sessions or receipts. All prior columns and visibility meanings
survive. Schema, fields, indices, fence installation and version marker share one
transaction. Failure rolls everything back. v10 fences remain, with v11 fences
covering all 29 application tables (87 additional triggers).

Domain tests cover lifecycle/replay/key-conflict/validation, both hold-versus
Ready outcomes across connections, stale races, transactional rollback, preserved
stock/receipts, held editing/picking/measuring/assignment, cancellation once, 132
orders, stable page boundaries/search/filter scope, and customer privacy.
Populated schema10 fixtures cover image bytes/quotas, weighted measurements,
active sessions/holds, original rows, custom objects/rootpages, failed migration
retry, restart and old v10 callbacks. HTTP tests cover ordinary/HTMX forms,
HTML-like text, invalid foreign submissions, optional release notes, stale drafts,
query escaping/context including weight and assignment, and customer paging.

Run the full check with bounded concurrency:

```sh
GOMAXPROCS=2 GOFLAGS=-p=2 ./scripts/check.sh
```

It includes weighted_smoke.py and attention_smoke.py. To verify actual schema10
process overlap with already-built frozen executables:

```sh
python3 scripts/attention_smoke.py \
  --previous-binary /path/to/schema10-shop \
  --previous-source 81c2662
```

The probe uses a new disposable fake database and retains its logs/results in a
unique /tmp evidence directory. It creates an order with the old process, starts
the actual schema11 app to migrate it, places a hold through the current HTTP
form, and proves the still-running old Ready form and health return503 without
changing the complete fingerprint. It also checks blank release notes, exact
replay, changed-key payload rejection, stale recovery, private customer trackers,
foreign scope, rehold/restart, Ready after release and cancellation stock once.
Neither this test nor database fences establish deployment routing/drain.
