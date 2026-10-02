# Customer and shopper messages

Schema 15 adds an independent, append-only message stream for each fake order. It connects the owning customer browser with the currently paired shopper phone. It does not create a customer account, send external notifications, request personal information or approve substitutions.

## Two views, one authorized thread

The order page links to `/orders/{id}/messages`; the phone pick list links to `/handheld/messages`. Each has a dedicated message shell, transcript, composer, appearance controls and a return link. Neither loads the scanner/picker controller. Plain HTML sends and history links work without JavaScript. HTMX enhances a separate transcript and composer.

The customer must be the exact unexpired owner session, even if that browser is a manager. The phone must have a currently valid assignment grant; the order is derived from that grant rather than a posted order or role. Reads and sends reauthorize in the same immediate SQLite transaction as their projection or append. Polls and rejected requests never create a replacement session or load the basket, so message traffic cannot expire/reconcile stock holds as an unrelated side effect.

New messages require an open Placed/Picking order with an active assignment. A customer may leave a message before the phone connects; this is saved, not evidence of live presence or delivery. Manager holds do not stop conversation. Ready/closed orders retain read-only customer history while that owner session is valid; the phone's existing authority ends at Ready or task closure. Reassignment revokes the former phone's reads/writes; the newly paired shopper sees prior thread history with original attribution. No broader post-Ready phone access is introduced.

## Independent data and immutable attribution

`order_messages` has `(order_id, revision)` as its primary key, so a cursor does not reveal another visitor's activity. The positive revision is allocated inside the writer transaction. Existing orders begin at revision 0 without backfill.

Each row stores its actor, sender name, assignment ID/version, shopper ID/name, normalized plaintext, server time and command key/hash. Assignment identity is snapshotted because reassignment updates the existing assignment row. Customer labels are fixed as Demo customer; worker labels use the assignment snapshot. Renaming a roster profile cannot rewrite earlier authorship. Foreign-key and shape constraints, plus explicit update/delete/replacement guards, protect append-only history.

A message is its own durable audit/replay outcome. It never inserts into `order_events`, copies manager notes, changes a hold, or advances order/pick/basket revisions or phone picking activity. A pending barcode/weight review remains valid across unrelated messages. Substitution decisions remain a separate domain: “yes” in free text has no authorization effect.

## Explicit send and uncertain outcomes

Each composer has an opaque conversation binding and random command key. The binding is derived from the demo epoch, order and owner, but is not a bearer credential and exposes no owner ID. It prevents a restored draft from crossing an explicit reset that reuses integer order IDs. New sends also bind the displayed assignment ID/version, so a draft cannot silently target another shopper after reassignment.

Authorization and current send eligibility precede replay. An exact successful retry returns its originally saved row without consuming quotas or creating another message. Reusing a consumed key for changed text/context conflicts. Result checking compares the normalized text and original assignment ID/version before acknowledging that exact intent; matching text under a different assignment is not silently cleared. New messages check assignment context and quotas before insertion. Closed send routes refuse further writes; the authorized customer's read-only result/history lookup can still reconcile an earlier result.

The enhanced composer locks submitted text while a request is pending. Confirmed persistence clears only the draft that belongs to that acknowledgment. Definite invalid/stale responses retain bounded text for an explicit reviewed send. Every enhanced message request has a finite 15-second timeout; sends/checks also have a request-owned deadline. A stalled transport therefore reaches recovery rather than locking the form indefinitely. Timeout/network/503 ambiguity preserves the exact key and payload, blocks edits to that uncertain payload and offers **Check send result** or **Retry same message**. Nothing auto-retries. An absent result is not proof an in-flight request cannot still commit. If assignment context changed, a read-only lookup first checks the old outcome; only a definite stale intent gets current context and a fresh key for deliberate review.

Ordinary successful POSTs use 303 back to the thread. GETs do not send. Back/Forward, visibility changes and reconnect require an authorization refresh before enabling sends. Drafts live only in the current document, not local/session storage; there is no promise of recovery after browser termination. Ended access removes the enhanced transcript and draft and stops polling. Previously seen content cannot be retroactively unseen.

## Bounded plaintext and storage

- 500 Unicode code points and 2,000 UTF-8 bytes per message
- CRLF normalized to LF; outer whitespace trimmed; empty/invalid UTF-8/NUL and disallowed controls rejected
- Ordinary line breaks allowed; no HTML/Markdown rendering, linkification, uploads or unfurling
- 8 KiB URL-encoded request limit, sufficient for the maximum valid Unicode payload plus bounded metadata
- Command keys use bounded header-safe ASCII; the acknowledged key can round-trip in HTTP response metadata
- Latest 50 messages per page, maximum 50; older/newer pagination authorizes before validating semantic cursors
- 20 persisted messages per actor role/order/minute, 40/order/minute, 120 globally/minute
- 200 retained messages or 256 KiB body bytes per order
- 10,000 retained messages or 8 MiB body bytes globally

Exact retries cost nothing. Failed attempts leave no retained rows. A new phone grant does not reset the role/order rate allowance. Reaching a hard retained limit keeps history and disables further sends; no background deletion or silent purge is introduced. These byte budgets bound bodies, not SQLite index/WAL/backup overhead. The existing explicitly confirmed reset retains a private recovery archive including message history.

## Polling and navigation

Latest-feed polling refreshes a bounded whole transcript. Older history pauses polling and has an explicit Back to latest action; the application does not pretend that a bounded latest page is gap-free delivery. Enhanced Older/Latest navigation stays in the current document and does not push browser-history entries. Dedicated-chat HTMX history restoration is disabled, including raw cache-hit/cache-miss paths, so it cannot replace the composer or restore an obsolete transcript outside the request lanes. Ordinary href links still navigate full pages without JavaScript; leaving/reloading the document may lose an unsent draft under the stated current-document-only limit. Domain `after` cursors advance only to the last row actually returned, never over omitted rows.

Feed, send and navigation have independent sequencing. Obsolete responses and navigation headers cannot replace a newer draft or authorization state. Fulfillment context version and per-order message revision are read-only lifecycle metadata, not message write preconditions. A local refusal barrier prevents a stale poll from reopening a composer after a send/check restriction, including a global quota change that does not advance this order's revision.

The composer stays outside transcript swaps. Reading position is preserved; a new-message hint defers a refreshed feed rather than jumping the reader away from an older row. Polling is suspended offline/hidden and slows when quiet. Terminal threads stop automatic polling. Expiry is checked for both customer and phone sessions. Labels say Sent only after persistence, never Delivered, Read or Online.

## Verification gates

Domain tests cover ownership and role boundaries, private-note isolation, unchanged fulfillment and still-valid pending weight reviews, immutable attribution, lifecycle races, replay, all limits/quotas, pagination, restart and fault rollback. HTTP tests cover ordinary/HX forms, escaped text, maximum Unicode transport, read-only lookup, stale/changed-payload recovery, revoked/closed access and no basket/session side effects. Deterministic browser-controller tests deliver response bodies and headers in arbitrary orders, including sends during polling, history, offline/hidden/expiry and preserved reading anchors.

The process smoke uses independent cookie jars and a real temporary app with rendered forms, then verifies SQLite read-only. Migration tests upgrade populated schema14, preserve prior private reports, verify exact recovery backup, roll back an injected failure, fence old writers, reopen, and reset with archived history/reused-ID isolation.

These checks do not replace live connected-browser or physical-phone acceptance. The earlier cloud Chromium pairing POST remains blocked with an unconfirmed `ERR_BLOCKED_BY_CLIENT` cause; it has not been retried through another route. Physical Samsung S23 Ultra camera testing is separate from messaging and remains pending.
