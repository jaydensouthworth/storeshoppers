package shop

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func populatedMigrationArchiveV9(t *testing.T) (string, time.Time) {
	t.Helper()
	path, now := populatedWeightedV8(t)
	db := migrationRawDB(t, path)
	migrationExec(t, db, legacyImageV9ForWeighted)
	asset, _ := imageFixture(t, 120)
	migrationExec(t, db, `INSERT INTO product_images(hash,normalization_version,mime,width,height,thumbnail_width,thumbnail_height,master,thumbnail) VALUES(?,?,'image/jpeg',?,?,?,?,?,?)`, asset.SHA256, asset.Version, asset.Master.Width, asset.Master.Height, asset.Thumbnail.Width, asset.Thumbnail.Height, asset.Master.Data, asset.Thumbnail.Data)
	migrationExec(t, db, `UPDATE products SET image_hash=? WHERE id=1`, asset.SHA256)
	migrationExec(t, db, `INSERT INTO product_image_commands VALUES('archive-image-command','archive-image-hash'); UPDATE product_image_limits SET day_bucket=20720,day_count=27 WHERE id=1`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, now
}

func savedMigrationBackups(t *testing.T, path string) []string {
	t.Helper()
	paths, err := filepath.Glob(path + ".migration-backups/*.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func verifyMigrationArchive(t *testing.T, path, fingerprint string, version int) {
	t.Helper()
	backup, err := openBackupReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err = checkSQLite(backup); err != nil {
		t.Fatal(err)
	}
	if fingerprintTest(t, backup) != fingerprint {
		t.Fatal("archive differs from complete pre-migration schema, rows, BLOBs or high-water IDs")
	}
	if version > 0 {
		var actual int
		if err = backup.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&actual); err != nil || actual != version {
			t.Fatalf("archived schema version=%d, want %d: %v", actual, version, err)
		}
	}
	for target, mode := range map[string]os.FileMode{filepath.Dir(path): 0700, path: 0600} {
		info, err := os.Stat(target)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("archive permissions %q = %v: %v", target, info, err)
		}
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal", ".incomplete"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("completed archive depends on %s: %v", suffix, err)
		}
	}
}

func TestMigrationArchivePreservesCompleteV9WALAndRestarts(t *testing.T) {
	path, now := populatedMigrationArchiveV9(t)
	old := migrationRawDB(t, path)
	migrationExec(t, old, `PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE products SET description='Latest committed old WAL edit' WHERE id=1`)
	before := fingerprintTest(t, old)
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("fixture has no committed WAL: %v", err)
	}
	s := migrationTestStore(t, path, now)
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	backups := savedMigrationBackups(t, path)
	if len(backups) != 1 {
		t.Fatalf("migration archives=%v", backups)
	}
	verifyMigrationArchive(t, backups[0], before, 9)
	backup, err := openBackupReadOnly(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	// Reconstruct a separate database using only the standalone archive. It
	// must reproduce the original snapshot, without current migrations/seeds.
	restored := migrationRawDB(t, filepath.Join(t.TempDir(), "restored.db"))
	if err = copySQLite(restored, backup); err != nil {
		t.Fatal(err)
	}
	if fingerprintTest(t, restored) != before {
		t.Fatal("standalone restore differs from original database")
	}
	var sequence int
	if err = restored.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='working_order_items'`).Scan(&sequence); err != nil || sequence != 9000 {
		t.Fatalf("deleted ID high-water mark=%d: %v", sequence, err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openPromotionMigrationStore(t, path, now)
	if err = reopened.migrate(); err != nil {
		t.Fatal(err)
	}
	if got := savedMigrationBackups(t, path); !reflect.DeepEqual(got, backups) {
		t.Fatalf("current-schema restart made another archive: %v", got)
	}
	verifyMigrationArchive(t, backups[0], before, 9)
}

func TestMigrationArchiveFailedUpgradeReusesVerifiedSnapshot(t *testing.T) {
	path, now := populatedMigrationArchiveV9(t)
	s := migrationTestStore(t, path, now)
	testExec(t, s, `CREATE TRIGGER fail_archive_upgrade BEFORE INSERT ON schema_version WHEN NEW.version=12 BEGIN SELECT RAISE(ABORT,'archive upgrade interrupted'); END`)
	before := fingerprintTest(t, s.db)
	var first string
	for range 2 {
		err := s.migrate()
		if err == nil || !strings.Contains(err.Error(), "archive upgrade interrupted") || !strings.Contains(err.Error(), "pre-migration data retained at") {
			t.Fatalf("injected failure=%v", err)
		}
		if fingerprintTest(t, s.db) != before {
			t.Fatal("failed upgrade modified original state")
		}
		backups := savedMigrationBackups(t, path)
		if len(backups) != 1 || (first != "" && first != backups[0]) {
			t.Fatalf("unchanged retry duplicated or replaced backup: %v", backups)
		}
		first = backups[0]
		verifyMigrationArchive(t, first, before, 9)
		assertForeignKeysEnabled(t, s.db)
	}
	testExec(t, s, `DROP TRIGGER fail_archive_upgrade`)
	corrected := fingerprintTest(t, s.db)
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	backups := savedMigrationBackups(t, path)
	if len(backups) != 2 {
		t.Fatalf("changed source needs a distinct recovery point: %v", backups)
	}
	verifyMigrationArchive(t, first, before, 9)
	for _, path := range backups {
		if path != first {
			verifyMigrationArchive(t, path, corrected, 9)
		}
	}
}

func TestMigrationArchiveFailureRetainsOriginalAndPriorFiles(t *testing.T) {
	for _, failure := range []string{"file", "symlink", "public directory", "budget"} {
		t.Run(failure, func(t *testing.T) {
			path, now := populatedMigrationArchiveV9(t)
			s := migrationTestStore(t, path, now)
			before := fingerprintTest(t, s.db)
			directory := path + ".migration-backups"
			var err error
			switch failure {
			case "file":
				err = os.WriteFile(directory, []byte("retain existing file"), 0600)
			case "symlink":
				err = os.Symlink(t.TempDir(), directory)
			case "public directory":
				err = os.Mkdir(directory, 0755)
			case "budget":
				if err = os.Mkdir(directory, 0700); err == nil {
					var file *os.File
					file, err = os.OpenFile(filepath.Join(directory, "retain.incomplete"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
					if err == nil {
						err = errors.Join(file.Truncate(migrationBackupMaxBytes), file.Close())
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			err = s.migrate()
			if err == nil || !strings.Contains(err.Error(), "migration backup refused; original data retained") {
				t.Fatalf("unsafe archive accepted: %v", err)
			}
			if failure == "budget" {
				if !strings.Contains(err.Error(), "budget") || strings.Contains(err.Error(), "DEMO_BACKUP_MAX_BYTES") {
					t.Fatalf("wrong migration budget remedy: %v", err)
				}
				if info, err := os.Stat(filepath.Join(directory, "retain.incomplete")); err != nil || info.Size() != migrationBackupMaxBytes {
					t.Fatalf("budget refusal purged/replaced recovery file: %v", err)
				}
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("backup failure changed original schema or data")
			}
			assertForeignKeysEnabled(t, s.db)
		})
	}
}

func TestMigrationArchiveRejectsCorruptMatchingSnapshot(t *testing.T) {
	path, now := populatedMigrationArchiveV9(t)
	s := migrationTestStore(t, path, now)
	testExec(t, s, `CREATE TRIGGER fail_archive_upgrade BEFORE INSERT ON schema_version WHEN NEW.version=12 BEGIN SELECT RAISE(ABORT,'archive upgrade interrupted'); END`)
	before := fingerprintTest(t, s.db)
	if err := s.migrate(); err == nil {
		t.Fatal("injected upgrade unexpectedly succeeded")
	}
	backups := savedMigrationBackups(t, path)
	if len(backups) != 1 {
		t.Fatal(backups)
	}
	corrupt := migrationRawDB(t, backups[0])
	migrationExec(t, corrupt, `UPDATE products SET description='Altered archive' WHERE id=1`)
	corrupt.Close()
	if err := s.migrate(); err == nil || !strings.Contains(err.Error(), "matching migration backup failed verification") {
		t.Fatalf("corrupt matching backup accepted: %v", err)
	}
	if fingerprintTest(t, s.db) != before || !reflect.DeepEqual(savedMigrationBackups(t, path), backups) {
		t.Fatal("invalid archive retry changed source or replaced recovery point")
	}
}

func TestMigrationArchiveRetryReusesSnapshotAtFullBudget(t *testing.T) {
	path, now := populatedMigrationArchiveV9(t)
	s := migrationTestStore(t, path, now)
	testExec(t, s, `CREATE TRIGGER fail_archive_upgrade BEFORE INSERT ON schema_version WHEN NEW.version=12 BEGIN SELECT RAISE(ABORT,'archive upgrade interrupted'); END`)
	before := fingerprintTest(t, s.db)
	if err := s.migrate(); err == nil {
		t.Fatal("injected upgrade unexpectedly succeeded")
	}
	backups := savedMigrationBackups(t, path)
	if len(backups) != 1 {
		t.Fatal(backups)
	}
	info, err := os.Stat(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	paddingPath := filepath.Join(path+".migration-backups", "retain.incomplete")
	padding, err := os.OpenFile(paddingPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(padding.Truncate(migrationBackupMaxBytes-info.Size()), padding.Close()); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after final-name publication/fsync but before the
	// incomplete-name removal. Both names belong to one verified snapshot.
	incompletePath := backups[0] + ".incomplete"
	if err = os.Link(backups[0], incompletePath); err != nil {
		t.Fatal(err)
	}
	err = s.migrate()
	if err == nil || !strings.Contains(err.Error(), "archive upgrade interrupted") || strings.Contains(err.Error(), "budget") {
		t.Fatalf("full budget prevented verified snapshot reuse: %v", err)
	}
	if fingerprintTest(t, s.db) != before || !reflect.DeepEqual(savedMigrationBackups(t, path), backups) {
		t.Fatal("full-budget retry changed source or duplicated archive")
	}
	if info, err := os.Stat(paddingPath); err != nil || info.Size() == 0 {
		t.Fatalf("full-budget retry removed existing recovery file: %v", err)
	}
	completeInfo, err := os.Stat(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	incompleteInfo, err := os.Stat(incompletePath)
	if err != nil || !os.SameFile(completeInfo, incompleteInfo) {
		t.Fatalf("retry removed or replaced a retained snapshot name: %v", err)
	}
	backup, err := openBackupReadOnly(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err = checkSQLite(backup); err != nil {
		t.Fatal(err)
	}
	if fingerprintTest(t, backup) != before {
		t.Fatal("retry modified the verified linked snapshot")
	}
}

func TestMigrationArchiveSkipsFreshMemoryCurrentAndFuture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	s := openTestStore(t, path)
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".migration-backups"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh/current database created archive directory: %v", err)
	}
	memory, err := sql.Open(applicationSQLiteDriver, ":memory:?_foreign_keys=on&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	memory.SetMaxOpenConns(1)
	migrationExec(t, memory, legacySchemaV1)
	memoryStore := &Store{db: memory, now: time.Now}
	if err = memoryStore.migrateWithBackup(func(tx *sql.Tx) (string, error) {
		path, err := preserveMigrationDatabase(tx)
		if path != "" {
			return "", fmt.Errorf("memory database created archive %q", path)
		}
		return path, err
	}); err != nil {
		t.Fatal(err)
	}
	futurePath, _ := populatedV1(t)
	future := migrationTestStore(t, futurePath, time.Now())
	testExec(t, future, `INSERT INTO schema_version VALUES(?)`, latestSchemaVersion+1)
	before := fingerprintTest(t, future.db)
	if err = future.migrate(); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatalf("future schema accepted: %v", err)
	}
	if fingerprintTest(t, future.db) != before {
		t.Fatal("future database changed")
	}
	if _, err = os.Stat(futurePath + ".migration-backups"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("future database created archive directory: %v", err)
	}
}

func TestMigrationArchivePreservesPopulatedUnversionedDatabase(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			path, _ := populatedV1(t)
			s := migrationTestStore(t, path, time.Now())
			if missing {
				testExec(t, s, `DROP TABLE schema_version`)
			} else {
				testExec(t, s, `DELETE FROM schema_version`)
			}
			before := fingerprintTest(t, s.db)
			if err := s.migrate(); err != nil {
				t.Fatal(err)
			}
			backups := savedMigrationBackups(t, path)
			if len(backups) != 1 {
				t.Fatalf("unversioned database was not archived: %v", backups)
			}
			verifyMigrationArchive(t, backups[0], before, 0)
		})
	}
}

func TestMigrationArchiveWriterLockExcludesOldWriterUntilFence(t *testing.T) {
	path, now := populatedMigrationArchiveV9(t)
	old, err := sql.Open(legacyImageVersionDriver, path+"?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	old.SetMaxOpenConns(1)
	statement, err := old.Prepare(`UPDATE products SET description='Must never reach archived or migrated data' WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	before := fingerprintTest(t, old)
	s := migrationTestStore(t, path, now)
	archived := make(chan string, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	finishSnapshot := func() { releaseOnce.Do(func() { close(release) }) }
	defer finishSnapshot()
	upgraded := make(chan error, 1)
	go func() {
		upgraded <- s.migrateWithBackup(func(tx *sql.Tx) (string, error) {
			path, err := preserveMigrationDatabase(tx)
			if err != nil {
				return "", err
			}
			archived <- path
			<-release
			return path, nil
		})
	}()
	var archive string
	select {
	case archive = <-archived:
	case err = <-upgraded:
		t.Fatalf("upgrade stopped before archive: %v", err)
	case <-time.After(6 * time.Second):
		t.Fatal("snapshot blocked on migration's own single-connection pool")
	}
	verifyMigrationArchive(t, archive, before, 9)
	written := make(chan error, 1)
	go func() { _, err := statement.Exec(); written <- err }()
	select {
	case err = <-written:
		t.Fatalf("old writer was not blocked while snapshot owns writer lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	finishSnapshot()
	select {
	case err = <-upgraded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("migration failed to finish after snapshot")
	}
	select {
	case err = <-written:
		if err == nil || !strings.Contains(err.Error(), "requires writer") {
			t.Fatalf("waiting legacy writer bypassed committed schema fence: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("old writer did not leave busy wait")
	}
	verifyMigrationArchive(t, archive, before, 9)
	var changed int
	if err = s.db.QueryRow(`SELECT count(*) FROM products WHERE description='Must never reach archived or migrated data'`).Scan(&changed); err != nil || changed != 0 {
		t.Fatalf("old write reached migrated data: %d %v", changed, err)
	}
}
