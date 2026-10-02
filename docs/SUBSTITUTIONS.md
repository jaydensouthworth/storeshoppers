# Counted replacements with explicit customer approval (schema17)

## The two-browser flow

On an active assigned, genuine storefront order, the phone/employee pick list links to **Replacement requests**. A counted item's own screen links directly to its proposal form. Choose the replacement product and number of replacement units, explain the reason in bounded plain text, and explicitly choose what happened to every outgoing original unit:

- **Restock:** physically returned and resalable
- **Write off:** missing or damaged; none returned to available stock

The read-only preview shows the exact original and replacement names, units, saved rates, line amounts, amount change, whole working estimate, any existing destination quantity/picks, and stock handling. **Send this exact request to customer** persists the proposal without changing stock or working lines.

The owning customer browser opens **Review replacement requests** from its order. The order tracker includes a pending-request link on its normal eight-second refresh. The separate request workspace is deliberately plain HTML: **Refresh requests** checks current state; it does not poll, auto-send, persist drafts, or load scanner/chat controllers. The customer explicitly chooses **Approve this exact replacement** or **Reject replacement**. Chat, including a message saying “yes,” has no approval effect.

Approval exchanges allocations once in a single transaction. The picker refreshes the pick list and scans/picks the replacement normally. Rejection or withdrawal preserves stock and the working order. Historical approved/rejected/withdrawn/stale proposals remain visible. Ready or closed orders retain customer history, but cannot approve or replay a mutation into a later task. Original placed receipts, amounts and instructions are never rewritten.

Synthetic practice tasks do not offer proposals because they have no reachable customer. If a real order's owner session has expired, the employee sees a read-only explanation rather than a dead-end request. Employee picking itself still works under its own live shift authority.

## Review and concurrency boundaries

The stored snapshot binds the original line/pick version, destination line/pick version if present, whole-order economic allocations, catalog price quote, proposer, physical disposition, quantity, reason, assignment version, demo epoch and originating authority. A source or destination correction, working-list edit, changed price/sale/unit or assignment lifecycle requires a fresh review. Global order versions still protect the initial rendered picker form.

Picking an unrelated line while waiting does not invalidate customer approval. Ordinary shelf-stock movement also does not invalidate an unchanged product/quantity/price. Approval rechecks current availability; insufficient replacement stock or an overflowing original return refuses the whole exchange. Preview projects expired reservations without modifying them; approval uses the existing expiry reconciliation and allocation/stock helpers. It does not duplicate the manager mutation logic.

A normal employee **Continue shopping** reopens the same shift/assignment with a new phone grant. Pending proposals retain their authority only through a live successor bound to that exact employee capability, assignment/version and epoch. A different shift, released/reassigned task, revoked/expired employee, archived roster member, or ended order cannot inherit it. Legacy paired-phone proposals remain bound to the explicit paired grant.

One stored pending proposal is allowed per original line. A new reviewed request may retire a demonstrably stale proposal in the same transaction. Successful send/decision keys hash the exact payload; exact retries return the saved outcome, changed-payload reuse conflicts, and competing commands serialize. Explicitly previewing a new HTTP draft receives a fresh key, so browser Back/edit after a consumed command does not trap the next request. The separate Send form retains its exact key for native retry. Page loading, refresh and Back never perform a command. If a network response is uncertain, inspect saved requests or retry the same native submission before creating another request.

## Scope, storage and migration

Customer access checks the exact unexpired owner session and CSRF. Manager sign-in does not grant another customer's approval authority. Employee access derives the order from the currently valid phone capability rather than a submitted order or role. All writes require exact Origin plus CSRF; request bodies are bounded at 8 KiB. The workspace never creates customer sessions or loads/reconciles a basket during reads/previews. Manager notes stay private and are not copied into proposals.

Reasons are 3–240 Unicode code points, plain text, with no controls. Keys are bounded safe ASCII. There are at most 100 retained proposals per order and 10,000 globally, with each JSON snapshot capped at 16,000 bytes. History cannot be deleted or rewritten through ordinary application code: database shape constraints and immutable-snapshot/outcome guards enforce retention. Approved substitutions add a customer-labelled scoped order audit event; the proposal itself records the immutable origin and exact decision outcome. No external notifications, payment, account identity guarantees, or weighted/mixed-unit substitutions are introduced.

Schema17 is additive, with no existing-data backfill, resets, reseeding or stock effects. It uses the verified pre-migration backup, all-or-nothing migration, latest writer fences and runtime compatibility response guard. Existing private messages, manager notes, employee shifts and all prior order data are retained. Explicit demo reset still uses its existing confirmed private archival flow and fresh epoch; old proposals/forms cannot cross it.

Run `./scripts/check.sh` for the full verification gate. Coverage includes migration backup/failure/retry/restart, old-writer fencing, quotas/shape/immutability, two-connection races, tampering/replay, ownership/expiry/CSRF/Origin, receipt and stock atomicity, fault rollback, unrelated picking, employee Continue, plain-HTML Back/edit, and an independent-cookie-jar real-process flow. Browser visual acceptance and physical camera testing remain separate checks.
