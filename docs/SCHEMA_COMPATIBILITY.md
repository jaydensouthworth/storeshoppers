# Runtime schema compatibility

The compatibility fence was introduced in a preparatory **schema 8** release.
The image release uses **schema 9** and adds image metadata/assets without changing quantity meanings. Schema 10 adds weighted execution and database writer fences; see [Weighted products](WEIGHTED_PRODUCTS.md). The application compatibility checks below protect a guarded binary that remains running while another process upgrades the same database.

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
transitions, shopper assignment, scoped roster profile changes, and manager hold/release/internal notes.
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

## Recovery before upgrade

Before changing an existing file-backed database, startup keeps its SQLite
IMMEDIATE writer lock and opens a separate read-only source handle. The supported
Backup API captures the committed original state, including WAL contents, images,
custom tables and sequence marks. The archive is checked against the complete
schema/data fingerprint and synced before migration writes begin.

Archives live beside the database in `<database>.migration-backups`, with private
directory/file permissions and a separate 256 MiB budget. Matching verified
snapshots are reused on retry. Current-schema restarts, fresh empty databases and
in-memory fixtures do not create archives. Backup failure or a full budget refuses
the upgrade with the original database unchanged; no archive is automatically
purged or overwritten. Migration failure rolls back the database and reports the
recovery path. The migration and its version marker still commit atomically.

An older image cannot serve a successfully upgraded database. Prefer a forward
repair; restoring a pre-upgrade archive requires deliberately stopping database
users and reviewing any newer changes first. Never copy a backup over a running
main/WAL pair or assume an image rollback downgrades the schema.

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

Every schema10+ application connection registers an immutable compiled-version
SQL callback. Versioned INSERT/UPDATE/DELETE triggers fence all application
tables. Schema11 adds v11 fences while retaining v10 fences; a schema10 process
cannot mark a held order ready or write another table after the upgrade. The
real schema10→11 overlap smoke verifies old Ready and health return503 with the
complete database fingerprint unchanged. Earlier unguarded applications lack
the callback and are refused by row triggers, but can still have unguarded
read-only responses and readiness. The weighted overlap evidence records that
unguarded health can return200.

These fences do not cover direct file access, DDL or Backup API page copying.
Reset has its own exact-version/exclusive checks. The response check is an
observation, not an interprocess lease through socket delivery. The current guarded predecessor refuses incompatible reads and writes during
overlap. A much older unguarded binary can still serve errors or stale responses;
row fences prevent its tested mutations from changing data. Deployment checks
should focus on the current application and its health, rather than assuming that
a saved historical image is still running. No local test proves process drain.
See [Weighted products](WEIGHTED_PRODUCTS.md) and [Order review and queue](ORDER_ATTENTION.md).

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

Schema12 adds promotion price-basis snapshots and v12 writer fences while retaining v10/v11 generations. Already-open schema11 connections cannot mutate the upgraded catalog or offers. This source supports schema12. Older guarded binaries return503 instead of attempting to serve a migrated database.

The combined schema12 executable passed fresh process-overlap probes against unguarded8, guarded8, image9 and weighted10. Prior table fields and sequence marks survived, current writer fences covered all29 application tables, and every rejected mutation preserved the complete database fingerprint. Guarded requests/readiness returned503; ancient unguarded health still returned200. Local tests establish data safety during the tested overlap, not deployment routing or the absence of an old replica.
