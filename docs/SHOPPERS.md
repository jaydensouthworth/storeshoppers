# Shoppers, roster management and assignments (schema v13)

The manager **Shoppers** workspace connects a small simulated roster to the application's existing orders and picking records. Avery Morgan, Jordan Lee and Casey Rivera are the three baseline fake identities. Managers can add other fictional shoppers and edit their scoped display profiles. They are not employee accounts, authenticated actors, device connections or a live workforce integration. The demo sends no assignments, messages or payments outside this application.

## Assignment and fulfillment behavior

An open order may have at most one active shopper assignment. A shopper may have multiple active orders up to their configured limit. A limit of zero means no set limit; migrated baseline identities keep this behavior. New custom shoppers default to three active orders, and the editable range is zero through twenty. Assigning a shopper does not start picking, change the order's placed receipt or reserve stock again. Managers continue using the order's existing fulfillment controls to record picked quantities and change its working allocation.

Assignment commands require a reason. Reassigning keeps the same assignment task, changes its shopper, advances its version and preserves the order's actual picking progress. Cancelling a **shopper task** ends that assignment and leaves the order unassigned so it can be assigned again. It preserves the order status, working quantities, recorded picks, placed receipt and stock. This is separate from cancelling an entire order, whose existing stock-disposition and final-total rules still apply.

Ready freezes fulfillment and automatically ends the active shopper task. Finish-with-picked-items and whole-order cancellation also end it when closing the order. Assignment closure, order closure, relevant stock changes and audit records commit together. An error in any of those writes rolls them all back. A completed assignment never reopens merely because the application restarts.

Progress is derived from recorded working-order quantities and picked counts. No timed animation, fabricated scanning speed, online status, location or scanner telemetry is presented as live activity. Reassignment does not attribute previously recorded picks to a real person. Partial fulfillment retains the requested denominator: one of three picked remains one of three, including when the remainder is unavailable or cancelled. Cancelling a task does not invent picked units or mark the order complete.

## Scope, history and command safety

In normal manager mode the workspace can display all orders. In demo mode its orders, assignments, workload counts, search results and audit history belong to the current visitor's session. Ownership filtering must happen before calculating counts or applying result limits. The three baseline identity records remain shared, but profile overrides, custom shoppers, roster events and availability/capacity belong to the current scope. Another visitor's identities, settings, work or reasons must not influence a visitor's roster, eligibility, workload display or error messages. A direct order or assignment URL does not bypass that ownership check.

Commands carry both the displayed order version and current assignment version. Order changes, picks, status transitions and assignment edits invalidate stale forms. A unique command key and payload hash make an exact successful retry harmless; reusing a key for different details is a conflict. Manager authorization and CSRF checks apply at the HTTP boundary, while ownership, versions, one-active-assignment enforcement and audit writes share the same SQLite transaction.

Assignment events are recorded in the existing `order_events` history. `shopper_event_links` adds the assignment identity and from/to shopper identities for each assignment event. Historical shopper filters use these saved identities rather than the shopper currently attached to a task, so reassignment does not rewrite who appeared in older events. Open-order filters match current assignment; text search accepts both the current scoped roster name and the saved assignment name after a rename. Ready/closed order filters and shopper-name searches match their recorded assignment history without duplicating order rows or counts. Reasons remain scoped with their orders and are not copied into shared stock-adjustment records. The shared-password manager gate still cannot prove an individual employee's identity.

## Roster workflow and assignment eligibility

The Shoppers page has one focused create/edit panel, a searchable current/archived
roster, and the existing assignment queue. Only fictional names and short display
initials are accepted. Initials are generated from the name if left blank. This
is not an employee directory, a login, a time clock or live presence.

Availability is explicitly manager-set: Available, On break or Off shift. Only an
Available, unarchived shopper below their active-order limit can receive a new
assignment. The store checks eligibility and counts under the same immediate
SQLite write transaction as assignment creation/reassignment; simultaneous requests
cannot overfill a limited shopper. Reassignment preserves the old task if the new
shopper is no longer eligible. Setting a shopper unavailable or reducing capacity
never silently cancels work. The roster continues to show their active tasks so a
manager can reassign or finish them deliberately.

Archiving is refused while active tasks remain. After those tasks are reassigned,
cancelled or finished, archive removes the person from the default roster and
prevents new assignments, while preserving identity and history. Restore is
explicit and uses the same version/replay guards. Archived names remain reserved
within the scope to avoid confusing duplicate identities. Profile edits, state
changes, capacity changes and archive/restore have scoped audit records.

Demo profile overrides are keyed by browser session; custom identities are owned
by that session. The normal manager mode uses a separate global scope. Changing a
baseline profile in one scope never changes defaults in another. Assignment/event
name snapshots keep historical names intact after an edit. Counts and eligibility
never use another visitor's orders in demo mode.

## Persistence, migration and reset

Schema v6 adds `shoppers`, `shopper_assignments` and `shopper_event_links`. Assignment states are `active`, `cancelled` and `ended`; a partial unique index allows only one active task per order. Active rows have an empty end timestamp and finished rows have a nonempty one. Assignment versions are positive and advance when ownership or state changes.

The original v6 migration seeds only the three fake roster identities. Schema13 adds scoped profiles and roster events, marks the three baseline identities, and backfills assignment/event name snapshots from their existing identities. It does not change stock, order state, tasks, assigned shopper IDs or receipt amounts. It does not assign historical orders or fabricate assignment history. Existing catalogs, stock, sessions and grants, basket holds, customer instructions, original receipts, working allocations, actual picks, frozen totals and audit records remain intact. Migration runs inside the existing all-or-nothing startup transaction; a failure leaves the populated v5 database unchanged and retryable. Ordinary startup never resets demo data or replays an assignment backfill.

Explicit shared-demo reset uses the existing protected confirmation and verified private archive workflow. The fresh database contains the three baseline roster identities, baseline catalog and empty assignments, event links, orders and operational history. The archive retains the complete previous assignment and order history, including event links, and can be restored using the instructions in [DEMO_RESET.md](DEMO_RESET.md). This is a global fake-data reset, not a way to cancel one assignment.

## Verification

`shoppers_migration_test.go` exercises a populated v5 upgrade with edited catalog data, live basket holds, receipt/working divergence, actual partial fulfillment and existing audit records. It injects failure at the final schema-version write, verifies complete logical rollback and a successful retry, and checks that no historical assignments are invented. It also verifies assignment and event-link persistence across restart, the explicit reset baseline, complete recovery-archive contents and a restored archive reopened by the application.

Run `./scripts/check.sh` and `python3 scripts/smoke.py` for the repository's required verification. Store and HTTP shopper tests cover assignment behavior and ownership separately from these migration and recovery fixtures. A passing data test is not evidence of a scanner integration or production employee authentication.

Schema13 verification adds a populated v12 upgrade, final-version-write rollback,
verified pre-upgrade archives, retained custom indices/views/triggers and no
backfill-generated operational audit rows. Existing prepared v12 writers are
refused after upgrade. Restart, explicit reset recovery archives and restored
profiles/history/snapshots are tested. Domain tests race two independent SQLite
connections against capacity and archive changes, verify cross-session/global
isolation, preserve existing work while unavailable/over capacity, and cover
invalid, stale and exact-replay commands. HTTP tests cover authorization, CSRF,
scope-before-validation, bounded draft recovery and rendered assignment eligibility.

The real-process shoppers smoke follows rendered forms through custom shopper
creation, retry, rename, availability and capacity changes, archive blocking,
reassignment, archive/restore and restart. A second visitor cannot see that roster
or its private workload/history. This complements live UI checks; it does not
claim physical-phone, camera or production identity verification.
