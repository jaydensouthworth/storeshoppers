# Runtime schema compatibility

The compatibility fence was introduced in a preparatory **schema 8** release.
The current image release uses **schema 9**, adding only image metadata and
retained assets; quantity meanings and receipt data are unchanged. The fence
protects a guarded binary that remains running while another process upgrades
the same database.

## Mutation boundary

Every ordinary application transaction starts through `Store.beginWrite`.
The SQLite connections opened by the application use `_txlock=immediate`, so
the helper obtains **BEGIN IMMEDIATE before reading MAX(schema_version.version)**.
It requires the database version to equal the binary's supported version.
A newer, older, empty or unreadable version marker refuses the transaction
before any application write. The transaction releases its lock on refusal.

This covers session creation, manager sign-in/sign-out and CSRF rotation,
reservation expiry, basket edits/renewal/practice setup, checkout, stock/audit,
catalog/taxonomy/details/example commands, promotions/features, picking (including
the atomic start-and-pick working-line command), working-order overrides, order
transitions and shopper assignment.
Existing transaction, ownership, optimistic-version and replay rules remain.

If the old command acquires the lock first, its entire compatible transaction
commits or rolls back before the migration can proceed. If migration acquires
the lock first, the waiting old command reads the new committed marker after
acquiring the lock and refuses. A separate check before acquiring the writer
lock would not provide this guarantee.

Two deliberate exceptions remain:

- Startup migration obtains its own immediate transaction so it can upgrade an
  older database. It rejects a newer marker before running baseline SQL and
  commits migration changes and version markers together
- Explicit demo reset uses its existing temporary SQLite EXCLUSIVE ownership
  and Backup API procedure. It checks exact compatibility both before taking
  ownership and again under ownership, before creating an archive or installing
  the baseline. Reset cannot downgrade a newer database or repair an older one;
  ordinary supported startup migrations must run first. The private fresh
  baseline's source version is also checked before the Backup API can replace
  live pages; archival copies retain the source schema and data

There is no persistent application lock and no lock held merely because a
process is running. Another compatible process can start normally.

## Requests, rendering and readiness

All dynamic routes, including `/healthz`, product media and scoped manager product-picker GETs,
check compatibility before dispatch.
HTML, status, cookies and redirect headers are buffered until the handler
finishes and compatibility is checked again. This prevents a response assembled
across a detected upgrade from displaying quantities with the old binary's
interpretation. Template output and the dynamic response buffer each have a
4 MiB limit; oversize responses fail without sending partial content.
Static assets bypass compatibility buffering and remain available. Dynamic responses report the serving binary’s supported version in `X-Shop-Schema`; this is a diagnostic, not proof that every other replica has stopped.

A mismatch or unreadable marker returns **503 Service Unavailable**, a plain
temporary-update message, `Retry-After: 3` and `Cache-Control: no-store`.
Buffered `Set-Cookie`, `Location`, `HX-*`, CSRF-recovery and content headers are
discarded.
HTMX follows its existing request-error notice behavior. Health is no longer a
constant success response when the running binary cannot use its database.

The final check is an observation point, not an interprocess lease through
socket delivery. A command can finish successfully on schema 8, then migration
can commit before its response check, yielding 503 even though that earlier
command committed. Refresh authoritative state before a new command; existing
checkout and command replay identities are preserved. A 503 is not proof that
the earlier compatible transaction did nothing.

## Deployment boundary before an incompatible migration

**This guard cannot protect an older, unguarded binary.** Publishing a guarded
schema-8 release, observing its healthy page or waiting an arbitrary interval
does not prove that every previous process has stopped using the database.
An unguarded process can still write after a future migration and reinterpret
quantities incorrectly. The current deployment's replica overlap and drain
behavior have not been established by these source tests.

Before introducing weighted quantity semantics or rebuilding tables, verify an
operational way to stop/drain all unguarded processes, or separately implement
and prove a database-enforced compatibility boundary that covers those clients.
Do not infer this prerequisite from the preparatory release alone. No deployment
configuration changes, live database writes, connection-specific SQL functions
or compatibility triggers are included here.

The marker is a contract: supported migrations must advance it monotonically
and atomically with their schema/data changes. The guard does not inspect every
table definition or detect manual unmarked changes. Reset's existing exclusive
request gate and one-connection assumptions remain in force.

## Verification

`compatibility_test.go` exercises every mutation family against a future marker
and compares the complete logical database fingerprint after each refusal,
including expired holds, stock, sessions and audit. It covers lower/empty/missing
markers, existing Stores opened before upgrade, both writer-lock orderings on
separate connections, and a separate already-running process.

HTTP tests cover GET, POST and HTMX, scoped product pickers, health, static assets,
clean header/body discard (including CSRF-recovery headers) when migration commits
during a handler, bounded output, and a checkout that committed before response
refusal while preserving its replay identity.
Startup refusal and incompatible reset leave data unchanged; reset creates no
archive and never reaches baseline installation. An incompatible fresh source is
also refused before copying and leaves both source and destination unchanged.
These use isolated temporary databases and simulated future version advancement,
not a production migration.
