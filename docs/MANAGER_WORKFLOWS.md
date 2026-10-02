# Manager working-list controls and recovery

Routine actions live on each working-list row: mark picked, edit the picked count, change the requested quantity, substitute, and remove. Order-level completion and cancellation remain separate because they close the order. Product selection uses a server-side search with at most six results, rather than a full-catalog dropdown.

## Picking and item changes

Mark picked records the full requested quantity. Edit picked count supports partial quantities. Either action can start a Placed order and update the count in one immediate transaction. The command checks order ownership, order and pick versions, terminal state, and a request key; an exact retry returns the recorded outcome. It does not deduct stock again. Ready and Completed orders remain closed to item edits.

Add only lists products not currently on the active working list. Removed product identities may return with their retained recorded price. Substitution may merge into an existing working product, using that product's recorded rate. New products use the quoted current effective price. Price changes between search and submission require another review before stock moves.

Increasing a quantity does not ask a stock-return question. Removing only unpicked units normally releases their reservation. When a change removes picked units, the manager must choose their stock outcome. Substitution asks whether the original units remain available, using wording that distinguishes unpicked reservations from physical picked returns. One disposition applies to every unit removed by that command; mixed physical outcomes require separate quantity changes.

Routine basket and working-item changes get a generated audit reason when the optional note is blank. Short notes are retained with a descriptive prefix. Stock effects, old/new quantities and original receipt snapshots remain recorded. Finishing a short order or cancelling an entire order still requires an explicit reason and stock outcome.

## Forms that survive interruptions

Manager sign-in and sign-out rotate the session's CSRF token. A previously open cart or manager form can therefore become stale. A rejected request still fails CSRF validation. For the same existing session only, the response provides a refreshed token and a fixed recovery code. The client updates hidden tokens, retains entered values and shows an explanation beside the submitted form. The visitor reviews and submits again; the client never automatically replays the mutation.

A reset, expired cookie or unknown session cannot obtain a replacement identity from a rejected POST, even after repeated clicks. An explicit GET refresh must establish the new context. Manager-access expiry shows a separate explanation with a sign-in link that opens in a new tab so the current draft can stay visible.

Stale basket revisions also preserve the attempted quantity while displaying the current saved quantity and authoritative total. The attempted quantity is labelled unsaved. Validation and connection feedback appears near the active form and moves into view. After adding or substituting, success feedback follows the resulting working item. Background tracker failures do not repeatedly steal keyboard focus.

## Verification

Run `./scripts/check.sh`. It includes dependency-free JavaScript interaction checks, the Go race suite, and seven real-process HTTP smoke suites. The manager workflow smoke exercises ordinary HTML and HTMX search, choice and mutation forms, automatic picking start, exact retries, optional notes, stock and receipt assertions, partial/cancelled/Ready outcomes, stale-session recovery and restart persistence. Live browser checks should repeat the interrupted cart flow and full item workflow at desktop and narrow widths; a passing server test alone is not evidence that the mobile interface is usable.

## Manager review and the order queue

The schema11 local candidate adds a separate Manager review panel on each ticket.
Place a hold with a 3–240 character reason; use Release hold with an optional note
when resolved. Internal notes are a separate disclosure, and every private event
is labelled Manager only. See [Order review and queue](ORDER_ATTENTION.md) for
privacy boundaries, filters, query recovery and verification limits.
