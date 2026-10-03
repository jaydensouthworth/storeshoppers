# Browser-task picking metrics

`web/static/picking_metrics.js` is an optional, local enhancement for the employee
picking workspace. It does not change order progress, submit
forms, send telemetry, or create a staff-performance record.

## Definition

- **Active picking:** elapsed foreground time after explicit picking activity,
  ending at the earliest of a pause, page hide, known task revocation/removal,
  report/replacement interaction, or 60 seconds without further picking activity.
- **Net lines:** the sum of this browser task session's observed, server-confirmed
  completion deltas. A newly completed counted or weighed product line is +1;
  partial counts and a changed measurement on an already complete line are 0;
  an undo that makes a line incomplete is -1. Units and grams are never combined.
- **Lines per active minute:** net lines / (active elapsed milliseconds / 60,000).
  Displayed with one decimal place after at least one active second. Before that,
  the rate is an em dash. The elapsed clock displays `mm:ss`.

Existing order progress, work by other browsers, manager changes, scans, previews,
reports, and substitution requests never manufacture completion credit. Negative
net lines/rates are possible when the current session undoes earlier completed
work. This is a local activity estimate, not order-wide throughput, real employee
monitoring, or a reconstructed historical performance figure.

The clock uses `performance.now()`, with each interval settled before a new idle
deadline is established. No wall-clock timestamp is used to fill hidden, reload,
or navigation gaps. Returning to the foreground or restoring a page requires a
fresh picking interaction. A genuinely fresh confirmation response can itself
establish/renew activity, including a zero-delta partial confirmation; it cannot
reconstruct earlier time when this script was not running. Manual pause survives
reload and is not overridden by a confirmation.

## HTML contract

Load `/static/picking_metrics.js` after `/static/handheld.js`, both deferred.
That order lets metrics observe the existing stale-response cancellation guard.

The root must be `#handheld-workspace` with both:

- `data-handheld-order`: the nonsecret order ID
- `data-handheld-assignment`: a non-authorizing assignment-session identity. The
  current backend uses `Task.MetricsKey`, a hash of demo-reset epoch, assignment
  ID/version, and shopper, so reused numeric IDs after a reset cannot inherit
  another session's totals.

Changing either value selects a separate browser-task session. Identifiers are
bounded to 100 ASCII letters, digits, colons, underscores, or hyphens. Do not use
grant tokens, pairing codes, CSRF tokens, customer information, or secrets as keys.

Only a freshly successful server mutation response may add root attributes:

- `data-pick-event`: its unique nonsecret command ID
- `data-pick-line-delta`: exactly `-1`, `0`, or `1`

The server owns all validation, completion-state calculation, replay protection,
and attribution. Emit no fresh event for a replay, failed mutation, unrelated
change, or a result superseded by a newer saved state. The client consumes and
removes these response attributes, and stores the event ID and delta result
together before displaying a known total. It never derives credit from rendered
progress totals or a client-supplied activity event.

A handled, definite refusal may instead emit `data-pick-rejected` with that
command ID. A fresh refusal clears its matching pending receipt without changing
the numerator or making accurate metrics unknown. It never restores accuracy
lost to an earlier ambiguous result, and a restored old refusal cannot settle a
newer retry of the same key. This attribute is also consumed and removed.

Set `data-picking-complete="true"` on a view where every product line is complete.
After consuming the final confirmation, the clock stops immediately instead of
adding a 60-second idle tail. A deliberate undo/correction control can start time
again for that new interaction; the resulting server view determines completion.

Optional descendants of the root:

- `[data-picking-active]`: elapsed `mm:ss`
- `[data-picking-rate]`: numeric lines/minute or em dash; label the unit in HTML
- `[data-picking-net]`: signed net product-line count or em dash
- `[data-picking-status]`: active/paused or explicit uncertainty explanation
- `[data-picking-pause]`: use a `type="button"` control; text and `aria-pressed`
  are updated by the module

Use static labels and avoid a rapidly announcing live region around the clock.
The optional status hook is recommended so unavailable attribution is explained.

## Activity scope and integration

Delegated user interactions are restricted to `.handheld-item`,
`.handheld-item-detail`, and `.handheld-pick-list`. They recognize controls in:

- `/handheld/scan`
- `/handheld/pick`
- `/handheld/pick/undo`
- `/handheld/weight/preview`
- `/handheld/weight/confirm`

Links to `/handheld/` and selected item views count as picking navigation.
Explicit `[data-picking-activity]` controls/forms may sit outside the item area,
such as the quick-undo form above it. Reporting, substitution links, and forms
outside the allowlist are excluded even if marked. `[data-picking-ignore]` can
explicitly exclude a region. Merely moving the pointer, reading the page, opening
the task, or changing saved progress in another browser does not start the clock.

Integration code can deliberately dispatch `handheld:picking-activity` on
`document`. Optional detail `{order, assignment}` must match the current root.
This event only starts/renews elapsed time and never accepts completion deltas.

`handheld:picking-uncertain` can explicitly invalidate attribution after a save
whose result was lost/suppressed. Omit detail for the current task, or provide
both `{order, assignment}` to invalidate that specific task. It performs no order
mutation. `handheld:pause` and `handheld:revoked` stop the active interval.

## Persistence and uncertainty

`sessionStorage` stores only the order/assignment key, elapsed active duration,
signed net count, consumed and pending nonsecret command IDs, manual pause, and
known/unknown status. No codes, drafts, customer details, names, authentication
material, or wall-clock activity history are saved. There are no network calls.
Closing the browser tab ends the session; another device/tab is not aggregated.
Some browsers clone session storage when duplicating a tab, so this is not a
cross-tab or independently audited metric.

Native mutation submits and scoped HTMX requests record a pending command before
sending. The matching successful event clears it. A lost response, timeout,
abort, error, suppressed stale mutation response, or restored page with an
unmatched pending command makes net count/rate unavailable. The elapsed timer
remains usable. No retry or refresh fabricates the missing completion. Known
confirmed receipts continue to de-duplicate correctly on ordinary refresh.

BFCache restoration rereads the latest tab record, so an old document cannot
overwrite newer same-task work. An unseen event from a reload or history restore
is not counted as new. Invalid/corrupt storage, storage access/write failure, or
an exhausted event budget also makes attribution explicitly unknown. The module
attempts a small unknown-status tombstone on a storage write failure; if the
browser refuses all persistence, no exact rate is claimed in that page and
restored events still fail closed.

There are at most 512 consumed event IDs and 16 pending command IDs per task.
Consumed IDs are never evicted to make room: after the cap the count/rate becomes
unknown, preventing old receipts from being counted again. Missing/uncertain
attribution stays unknown for that task session. It does not reset saved picks or
silently create a fresh metric baseline. Storage manually cleared by a user is a
new local session; prior activity cannot be recovered.

No clock timer runs while the task is absent, hidden, manually paused, revoked,
or idle. All metrics are optional: the ordinary HTML picking flow still works
without JavaScript, but that unobserved work cannot be attributed retroactively.

## Isolated verification

Run from the repository root:

```sh
node --check web/static/picking_metrics.js
node --test scripts/picking_metrics_test.cjs
```

The dependency-free Node VM harness controls DOM events, monotonic time, timers,
and session storage. It covers counted/weighed/partial/undo semantics, first-entry
confirmations, idle and hidden gaps, manual pause, reload/BFCache, task/version
isolation, strict activity scope, storage failures/corruption/caps, native POST
ambiguity, scoped HTMX failure and stale-response handling, and absence of
network or automatic form actions. This is source/local lifecycle coverage,
not browser, physical-device, live deployment, or server-integration acceptance.
