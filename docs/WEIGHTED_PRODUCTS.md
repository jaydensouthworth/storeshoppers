# Weighted ordering and fulfillment

Weighted selling supports requested grams and reviewed actual weights within the
existing portfolio demo. Camera capture, scan labels, payments, customer accounts
and physical scale integration remain outside this feature.

## Quantities and prices

Counted products keep their existing limits: 99 units per line and 10,000
available units plus basket holds. Weighed products use integer grams, at most
100,000 g per requested/allocated line and 1,000,000 g available plus basket
holds. Requested grams must follow the product's configured step; actual scale
readings accept individual grams. Stock adjustments are expressed in each or g.

All rates and amounts are integer cents. Weighed rates are cents per kilogram.
Each line rounds once, using `(grams * cents_per_kg + 500) / 1000`; 527 g at
349 cents/kg is 184 cents. Bounds are checked before multiplication and totals
are checked before addition. A zero-rounded line can accompany a positive line;
an all-zero basket is rejected before new reservations, and checkout rechecks
the positive estimate. An explicitly measured/resolved final order may be zero.

Timed basket holds reserve grams with the same immediate transactions as counted
items. Editing a hold changes only its allocation delta. Expiry returns a hold
once. Checkout converts the existing hold into the working order without a
second stock deduction. Price/availability changes still require a new quote.

## Requested, allocated and measured are separate

Placed `order_items` and `orders.total` remain the immutable requested receipt:
name, SKU, selling unit, price basis, step, requested quantity, rate and rounded
subtotal. Later catalog edits do not rewrite the receipt.

Working lines retain stable IDs and the existing aggregation by product within
an order. `quantity` is the manager's working target; `allocated_quantity` is
the accepted reservation; `picked_quantity` records counted picks or actual
grams; `measurement_confirmed` distinguishes measured grams from an unmeasured
estimate. Initially target and allocation match and grams are unmeasured.

A manager enters actual grams, an optional note and, for any reduction, a choice between
returned sellable stock and unavailable/damaged stock. Preview displays the
target, old allocation, actual grams, old/new line amount, price delta and stock
delta. Preview is read-only. Confirmation binds those displayed consequences to
the exact command and current line/order versions, then changes allocation,
measurement, order state and scoped fulfillment audit in one transaction. Retries replay the
same successful command; changed payloads, stale versions and edited reviews
are rejected. Blank routine notes record “Manager confirmed scale reading”; custom notes remain bounded. Measurements keep the recorded working rate, including when the
catalog price changes or the original product is archived.

Above-allocation measurements reserve only the extra grams. Below-allocation
measurements either release the difference or write it off; a missing quantity
never silently becomes sellable stock. Explicitly confirmed zero grams is a
valid outcome with a retained positive target. Changing a target resets its
measurement and adjusts from the prior accepted allocation, requiring a new
measurement. Re-saving an unchanged target preserves its measurement.

Substitutions are whole-line and same-unit. New products require the reviewed
current catalog quote. A product already represented by a retained working
line uses that line's rate and quantity step, even after removal/re-addition.
Substituting into a weighed line adds replacement grams to both its target and
its accepted allocation: a previously measured bag remains reserved until the
combined line is remeasured. Adding to or substituting into a weighed line
invalidates its measurement. Original receipt
rows and reasoned audit events preserve provenance; independently priced split
allocations remain future work.

Ready requires every counted quantity picked and every weighed line measured.
It freezes the final sum of individually rounded actual line amounts. Explicit
partial completion resolves remaining allocations with a stock disposition;
whole-order cancellation retains actual pick history and freezes final zero.
Closed orders cannot be edited or reopened.

## Mixed views and access

Counted-only orders retain unit-count progress. An order containing weighed
lines uses complete product lines for progress, with each row showing its own
units/grams, target and allocation. Mixed baskets show product-line counts.
Shopper workload aggregates use product lines for all assigned orders when any
of them includes weighed goods. Grams are never added to unit counts or line
counts. Shared stock metrics remain explicitly counted-only where applicable.

Measurement preview and confirmation require the existing manager gate, CSRF
and order ownership. HTTP checks scope before rendering errors or drafts. Audit
reasons stay in scoped order history, not shared stock adjustments. Plain HTML
and HTMX use the same domain commands and review step. Existing counted picking
endpoints cannot mark grams picked. The schema12 follow-through supports per-kilogram promotions and featured
products with explicit unit labels and Choose grams links; see [Promotions](PROMOTIONS.md).
Both regular and discounted rates are snapshotted before actual-weight reconciliation.

## Migration and compatibility

The migration rebuilds products, cart, placed receipt items and working items on
a pinned connection. Foreign-key enforcement is disabled before the immediate
transaction, integrity is verified before commit, and enforcement is restored
afterward. IDs, sequence high-water marks, sessions, baskets, receipt values,
allocations, custom schema objects and extension tables survive. Orders are not
rebuilt. Before any upgrade writes, a verified private recovery snapshot is retained under
the migration writer lock. Failure rolls back schema, contents and version marker
together. See [Schema compatibility](SCHEMA_COMPATIBILITY.md) for archive limits
and recovery.

Pre-migration gram cart or receipt rows are ambiguous: prior software could
change metadata beneath an expired counted request, but did not support gram
sales. Migration refuses these rows unchanged rather than interpreting them as
grams. Recovery requires an operator to inspect a backup with the previous
compatible application, resolve/remove affected active basket requests, and
retry. Historical gram receipt rows require explicit data investigation; do not
delete history or reset the database to force an upgrade.

Every application write connection registers an immutable compiled-version SQL
function. INSERT, UPDATE and DELETE triggers on every application table reject
older/missing callbacks. Runtime transaction and buffered-response guards reject
incompatible schemas. Reset uses its separate exact-version and exclusive
ownership checks and installs a fully migrated, fenced fresh database.

These triggers protect against accidental legacy writers, not someone with
database-file access. DDL and Backup API page copying are outside row-trigger
enforcement. They also cannot guarantee old read-only responses or readiness.
Frozen old application probes verify actual HTTP writes against the real
rebuild, but an unguarded old health endpoint can still answer 200. Current guarded predecessors return503 on incompatible requests and health
checks. An ancient unguarded instance may still serve errors or stale reads;
local fence tests do not prove deployment availability or process removal.
After migration, verify the current schema, known retained records and the
ordinary and weighted customer/manager workflows.

## Reproducing the process-overlap check

`scripts/weighted_overlap_smoke.py` accepts three already-built executables and
creates only disposable fake databases in a unique temporary evidence directory.
It requires no frozen source tree, repository history, server access or live
DB path. Use the unguarded schema-8 application from
`b7c083a9a142ebc8bd1e6d61b1865002e937e133`, the guarded schema-8 application from
`0c4719e`, the image-only guarded schema-9 application from `01f8bd8`, and
the current integrated application:

```sh
python3 scripts/weighted_overlap_smoke.py \
  --unguarded-binary /path/to/unguarded-schema8 \
  --guarded-binary /path/to/guarded-schema8 \
  --current-binary /path/to/current-shop \
  --schema-version 12 \
  --additional-guarded-binary /path/to/image-schema9 \
  --additional-guarded-source 01f8bd8
```

The schema argument must equal the compiled application's actual schema version.
`--current-source` can identify its source commit; executable SHA-256 values and
each predecessor's observed schema version are always recorded. Optional `--evidence-directory` chooses an existing
parent directory for a new, uniquely named evidence folder. No existing database
or evidence is overwritten.

The probe starts each old process first, creates orders and live basket holds,
then starts the real new application against that database. It checks every old
table's original columns and sequence high-water marks, foreign keys and writer
fence coverage. A measured gram line makes old raw-quantity multiplication
visibly unsafe. Old checkout, picking, counted/mixed Ready, logout and reset
must refuse without changing the complete schema-and-data fingerprint. Guarded
GET/POST/health requests must return503; unguarded GET/readiness observations are
recorded separately. Evidence includes JSON results and each process's log.

`weighted_smoke.py` covers parsed forms, a 527 g/184-cent preview, exact
confirmation replay, stale/tampered review rejection, mixed partial outcomes,
zero final totals, stock dispositions, privacy and restart persistence.
`attention_smoke.py` covers review holds and customer-safe status/history;
`weighted_promotions_smoke.py` covers per-kilogram offers and retained order rates.

Any substantive migration/fence change requires a fresh overlap run against the
final executable. Local checks complement live desktop/narrow workflow review;
they do not establish deployment availability or physical-device behavior.
