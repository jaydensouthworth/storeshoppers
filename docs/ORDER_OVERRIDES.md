# Manager working-order overrides (schema v5)

## User-visible behavior

Open an order from **The counter → Orders**. Before Ready, managers can add a counted product, set an absolute quantity (zero removes it), substitute an entire working product, finish with the quantities actually picked, or cancel the entire order. Every override needs a reason. A manager may override customer instructions; the change and reason are shown in the order history. This is a fake-data demo, without payment or customer messaging.

The original placed receipt is always separate. It retains its names, prices, quantities, subtotals, instructions and original total. The working estimate changes as the list changes. Ready freezes the final amount. Finish-with-picked-items closes immediately when quantities remain unresolved (fully picked orders use the normal Ready/collection flow), records every unpicked unit as unavailable or cancelled, and uses only picked quantities for its final amount, including zero. Missing quantities remain in the progress denominator; 1 of 3 picked stays 33%, never 100%.

Whole-order cancellation closes with a zero final amount and disposes of **all** allocated units, including units already picked. Actual picked counts are preserved as history and clearly labeled as such. Cancelling an order is not cancelling a shopper assignment or stopping a software task. [Shopper assignments](SHOPPERS.md) are a separate task lifecycle: cancelling a picking task leaves its order open and unassigned, with no stock effect. Ready or closed orders end their active assignment in the same transaction.

## Stock and prices

Checkout already deducted the allocation through basket reservations. Picking does not deduct it again. Increasing a working quantity reserves only the increase; reducing it handles only the decrease. Substitution first reserves the replacement and then disposes of the source, all in the same immediate transaction.

For every outgoing command, choose one stock disposition:

- **Restock:** all outgoing units are physically returned and resalable; add that quantity to available stock
- **Write off:** all outgoing units are unavailable, damaged or not resalable; do not increase available stock

This choice applies uniformly to the outgoing quantities in that command. For mixed physical outcomes, reconcile subsets with separate quantity changes before finishing the remaining order. A reason must explain the actual disposition. The application never silently clamps or guesses returned stock. Available plus active basket holds must remain at most 10,000 units, even if later manual restocks have consumed room previously used by an order. An overflowing return is refused atomically; reconcile physical inventory before retrying. A write-off is not a shortcut for resalable stock.

Existing working products retain their recorded price, including removed products added again. A rendered catalog quote protects first-time working additions from stale displayed prices; a mismatch requires review before any stock/audit mutation. Stock-only changes do not invalidate that quote. Expired basket holds are reconciled before allocation checks. A product first added to this working order snapshots its current catalog name, SKU, price and measurement metadata. Substitution into a product already on the working list adds to that line at its retained recorded price and preserves its already-picked count. Newly added quantities still need picking. Only counted (`each`) products are supported; weighed ordering, actual-weight reconciliation and mixed-unit substitutions remain disabled.

## Data model and safety

- `orders.total` and receipt fields in `order_items` are never rewritten
- `working_order_items` has stable IDs, independent quantities, prices, pick versions and resolved-unavailable/cancelled counts
- One working line represents each product in an order. Removed lines are retained as zero-quantity tombstones so an old pick form cannot accidentally revive or target another line
- Whole-line substitutions keep the original placed receipt, the source tombstone and an explicit source/replacement audit event. Replacements into an existing product aggregate quantities rather than creating parallel independently priced lines
- `orders.order_version` advances for every pick, status transition and override. Rendered status/override forms must submit the displayed version
- Override command keys are unique per order and hashed with the command payload. Exact successful retries return success without another event or stock effect; changed payloads with reused keys fail
- Ownership, optimistic version, availability checks, working edits, stock changes, finalization and audit insert all share one immediate SQLite transaction. Any failure rolls everything back
- New picking controls use `/lines/{line_id}/pick`. The old `/items/{product_id}` route stays product-keyed and version-guarded; old forms are never reinterpreted as working-line IDs
- Audit records are scoped with the order. Shared Stock & Activity shows order changes only after normal/demo ownership filtering, before counts and pagination. Private order reasons are not copied to globally shared inventory-adjustment rows
- Products used by manager-added working lines remain unit-locked even if no original receipt used them

Ready is frozen except for Confirm collected. Completed, partial-completed and cancelled orders cannot reopen or be edited. Historical amendments, returns after completion and refunds require a separate workflow; none is simulated by rewriting history or returning stock twice. Named actor attribution remains outside the shared-password demo; the audit proves the recorded command, not an individual employee’s identity.

## Migration, recovery and verification

Startup applies v5 in the existing all-or-nothing migration transaction. It adds tables/columns and copies existing receipt/pick snapshots verbatim, preserving sessions, carts, basket holds, products, stock and receipt amounts. Ready and Completed retain their exact placed total as their frozen final total; collection does not recompute it. An interrupted or failed migration leaves the entire v4 schema/data intact and can be retried. Restart never replays working-line backfills or changes terminal state. Explicit demo reset creates a fresh database at the latest supported schema only after its existing private backup step.

Run `./scripts/check.sh` for formatting, vet, race tests, build and actual-server HTTP smoke. Tests cover the working/placed split, zero/partial totals, stock returns/write-offs/caps, replay and conflicting keys, stale picking/finish/advance forms, two-connection races, injected audit rollback, v4 populated upgrade failure/retry, demo ownership and CSRF, and actual HTTP stable-line/finalization controls. Browser visual/mobile verification remains separately unverified where the environment blocks the preview.
