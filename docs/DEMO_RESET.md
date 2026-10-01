# Explicit demo reset and recovery

Ordinary restarts and deployments preserve data in **both normal and demo mode**. Resetting is an explicit manager action, separate from per-visitor practice-basket setup.

With `DEMO_MODE=true`, open **`/manager/demo/reset`** after signing into the manager workspace, or use its **Reset shared demo** link. GET only displays the confirmation page. The POST requires the current manager session, its CSRF token and the explicit confirmation checkbox. Both routes return 404 in normal mode.

The action restores the current seeded demo: 70 products and their starting prices/stock, standard labels, and empty sessions, baskets, orders, picking, stock adjustments and visitor edits. All visitors lose their old sessions and managers must sign in again. The page warns that the shared demo password is intentionally public and this reset affects **every visitor's fake data**. There is no unauthenticated reset action and no reset on a simple link visit.

## Concurrency and durable reset

An in-process request gate lets existing requests finish, prevents new checkouts from starting during a reset, and waits at most two seconds for the drain. A timeout/cancelled wait returns a retryable response without changing the database and reopens ordinary traffic. Concurrent reset requests are refused. Manager authorization, bounded body parsing, CSRF and confirmation are checked under an ordinary read lease before requesting maintenance. The existing session/CSRF is checked again **after obtaining that gate**, so a prior reset cannot leave an old confirmation valid. Unauthenticated or slow-body requests never acquire exclusive maintenance.

Under that exclusive application gate, the existing single SQLite connection temporarily takes **real SQLite EXCLUSIVE locking-mode ownership**. Other connected SQLite readers/writers, including older application binaries, cause reset to fail safely rather than racing a replacement. The same connection is kept throughout; normal locking mode is restored afterward. Run one replica on a local persistent volume with reliable SQLite locking/fsync; do not use a network filesystem for the volume.

1. Check the database's integrity, foreign keys and supported schema version
2. Use SQLite's Online Backup API to capture the complete committed database, including crash-left WAL data, in `DATABASE_PATH.demo-backups/`
3. Check the snapshot's integrity/foreign keys and full logical fingerprint; close it as a standalone rollback-journal database, sync the file and directory entries, and retain it under a UTC timestamp plus content digest
4. Build a fresh in-memory database using the existing migrations and seeds, matching the original SQLite page size
5. Copy that baseline into the already-open database using SQLite's Backup API destination transaction with full synchronous durability; an unfinished copy is rolled back by SQLite
6. Release SQLite/application ownership, invalidate the old browser cookie and display the completed-reset page

The live main database, WAL and SHM files are **never renamed, unlinked or manually copied** to implement a reset. The only rename finalizes a closed, separately verified archive snapshot. Backup/preparation failure leaves the current demo unchanged; an interrupted destination transaction rolls back rather than leaving a partial schema. Complete diagnostic paths are logged privately. The page gives a concise retryable message, not a database path or snapshot download.

SQLite primary references: [locking mode](https://sqlite.org/pragma.html#pragma_locking_mode), [exclusive ownership in WAL](https://sqlite.org/wal.html#use_of_wal_without_shared_memory), [Online Backup API and rollback](https://sqlite.org/c3ref/backup_finish.html), and [go-sqlite3 Backup](https://pkg.go.dev/github.com/mattn/go-sqlite3#SQLiteConn.Backup).

## Bounded archives, without deletion

The default `DEMO_BACKUP_MAX_BYTES` is **268435456 bytes (256 MiB)**. It counts all regular files inside the archive directory, including retained incomplete attempts. A new snapshot must fit the remaining budget; otherwise reset refuses with an explicit budget message and preserves the current demo. Ordinary startup still works. The budget does not reserve space and excludes the live DB/WAL and transient SQLite journal overhead; leave free disk capacity beyond it.

No automatic pruning, expiry, overwrite or deletion of old archives is implemented. To resolve a cap refusal, an operator must retain/move selected archives elsewhere or deliberately increase the positive integer byte limit. Never remove recoverable history blindly. Failed `.incomplete` files are retained for operator review and counted toward the budget; they are not valid backups.

The fingerprint covers all schema objects and table data, including unknown extension tables. A verified identical snapshot is reused after a failed reset, avoiding repeated full archives of unchanged data. Every successful explicit reset builds the current fresh fixture. Demo-only future fixture additions belong immediately after migration in `freshDemoDatabase`; ordinary Open/migrations must keep established stores intact.

Archives are created in a private `0700` directory with `0600` files. They are outside embedded public assets and have no HTTP download route. They contain the previous database's complete session and order data: do not upload or publish them. Repository/build ignores exclude archives.

## Manual recovery

1. Stop all application replicas and database tools. Keep the existing database and its entire directory, including any WAL/SHM files, intact
2. Select a completed `.sqlite3` archive from the logged private path. Open it read-only with SQLite and run `PRAGMA integrity_check;` and `PRAGMA foreign_key_check;` (expect `ok` and no foreign-key rows)
3. Copy that standalone archive to a **new, previously unused database filename**, or use SQLite's `.backup` command to create that new file. Preserve the original database and archive; do not overwrite them or separate a live database from its WAL
4. Ensure the app's non-root user can read/write the new database and its directory. Point `DATABASE_PATH` at that file and start one replica. Use `DEMO_MODE=false` if shared demo access/reset should be disabled
5. Verify the restored data. Normal additive migrations may upgrade an older archive without discarding its contents

## Verification

`./scripts/check.sh` covers unit/race tests, normal HTTP persistence and `scripts/demo_reset_smoke.py`: real-process demo restart persistence followed by GET confirmation and protected POST reset. Fixtures cover old schemas, crash-left WAL, complete backup restore, competing SQLite ownership, alternate page sizes, cap refusal, deliberately interrupted replacement rollback, authorization/session invalidation, concurrent checkout, duplicate reset and bounded request-drain recovery. `scripts/container-check.sh` additionally verifies the non-root image keeps data on ordinary container restart and performs explicit reset with a readable snapshot on the same persistent volume.
