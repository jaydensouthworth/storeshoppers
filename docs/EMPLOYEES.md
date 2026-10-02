# Employee shopping app

## Entry and task flow

The prominent **Test employee shopping app** storefront link opens `/handheld/employee`. A fictional “Demo employee 0004”-style identity is generated for an eight-hour browser shift and assigned to Neighborhood Market. Refreshing retains it. This is a fake-data capability, not production authentication or an employee account.

The mini dashboard shows your active-order count, available count and the first 100 open orders, with your orders first. **Start shopping** claims unassigned work in one SQLite writer transaction. **Continue shopping** reopens your saved task. Three concurrent orders are allowed per employee. The handheld item view retains scanning, absolute count confirmation, correction down to zero, and measured-weight review. Finished picking links back to the dashboard’s **Finish shopping · mark ready**. Ready requires every line complete and no manager hold. **Release this order** returns it to the shared queue with picks, measurements and stock intact.

The phone view contains no manager controls. Existing customer order pages and manager gates keep their original scope.

## One shared store, independent visitors

New checkouts made while DEMO_MODE=true enroll in the store queue in the same transaction as checkout. Their checkout form explains that fake orders/instructions become available to demo employees. Migration never backfills existing visitor orders, messages or instructions. Non-demo checkouts remain private. The queue exposes only reference, status and progress; a valid assigned capability is required for task contents and messages.

Every employee sees the same enrolled queue. Competing claims cannot create two active assignments. A losing or stale command shows an explicit refresh/review error, without overwriting newer work. Pick commands remain absolute, versioned and replay-safe. Exact claim retry returns the same live grant rather than revoking it or minting duplicates; a replay cannot reconnect a later assignment lifecycle or replace a newer explicit connection. Browser refresh and Back do not auto-submit commands.

Expired/revoked/archived employee shifts return enrolled unfinished work to the queue with an audit and new order/assignment version. Stock and recorded items stay unchanged. Employee work remains accessible after the original customer session expires, but the customer’s receipt/message authority is not extended. Legacy paired grants still expire with their original owner. All grants check current epoch, assignment, live employee membership where applicable, and CSRF on writes.

## Shared practice work

**Load shared practice orders** deliberately creates at most three one-line counted orders from currently available active products. It reserves one unit per selected product, records immutable receipts and working allocations, and commits a global one-time flag atomically. Concurrent visitors and exact repeats resolve to the same set. Refresh, restart, new employees and empty queues never auto-replenish, overwrite, reset, or reuse active work. If less than three products are available, only available products are used; a zero-result attempt is recorded without retrying later invisibly.

The synthetic customer session is inaccessible and already expired. Practice tasks therefore do not expose customer chat or manager-report controls; server-side guards prevent creating unresolvable manager holds or messages. Use a new storefront order to test a genuine two-browser customer conversation or manager review. Practice stock is ordinary shared demo stock, not a per-visitor clone. No live reset is required or performed by this feature.

## Security, migration and verification

Employee cookies are HttpOnly, Secure on HTTPS, SameSite Strict and scoped to `/handheld/`. Public plaintext deployment is refused. Document responses use same-origin referrer policy so native same-origin POSTs retain a valid Origin; null/foreign origins and spoofed forwarding headers are still rejected. The eight-hour shift does not renew on reads. Active identities are capped; no raw employee or grant bearer is stored in HTML/URLs or the database.

Schema16 is additive and uses the existing verified pre-migration backup, transaction rollback, old-writer fences, runtime compatibility check and reset epoch. Existing products, stock, orders, shoppers and phone grants remain untouched at migration time. Automated coverage includes concurrent creation/claiming, two Store connections, stale/duplicate/reconnected commands, zero/unavailable seeds, scope/CSRF/origin, expiry/revocation, release/reclaim, Ready holds/weighted completeness, requested receipt retention, migration failure/backup/restart, independent cookie jars and real-process restart.

Physical camera optics and a real phone’s permission UX require physical-device acceptance. Structured substitution proposals/customer approvals remain a separate next phase; sending chat text is never approval to substitute.
