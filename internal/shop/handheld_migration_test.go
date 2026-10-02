package shop

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

const handheldV13Driver = "handheld_v13"

func init() {
	sql.Register(handheldV13Driver, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		return c.RegisterFunc("app_schema_version", func() int64 { return 13 }, true)
	}})
}
func populatedHandheldV13(t *testing.T) (string, time.Time) {
	t.Helper()
	path, now := populatedShopperRosterV12(t)
	s := migrationTestStore(t, path, now)
	testExec(t, s, `PRAGMA foreign_keys=OFF`)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = migrateShopperRoster(tx); err != nil {
		t.Fatal(err)
	}
	if err = installWriterFences(tx, 13); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO schema_version VALUES(13)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `PRAGMA foreign_keys=ON`)
	testExec(t, s, `INSERT INTO shopper_roster_events(scope,shopper_id,command_key,command_hash,name,action,details) VALUES('',1,'roster-v13-fixture','hash','Avery Morgan','edit','Retain existing roster audit'); UPDATE shopper_roster_profiles SET availability='break',version=version+1 WHERE scope='' AND shopper_id=1`)
	s.Close()
	return path, now
}
func TestHandheldMigrationPopulatedV13BackupRollbackRetryRestart(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			path, now := populatedHandheldV13(t)
			old := migrationTestStore(t, path, now)
			prior := shopperPriorTables(t, old.db)
			objects := migrationQuerySnapshot(t, old.db, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`)
			if fail {
				testExec(t, old, `CREATE TRIGGER reject_handheld_version BEFORE INSERT ON schema_version WHEN NEW.version=14 BEGIN SELECT RAISE(ABORT,'handheld marker failure'); END`)
				before := fingerprintTest(t, old.db)
				old.Close()
				broken, err := OpenWithClock(path, func() time.Time { return now })
				if err == nil {
					broken.Close()
					t.Fatal("migration failure ignored")
				}
				if !strings.Contains(err.Error(), "handheld marker failure") || !strings.Contains(err.Error(), "pre-migration data retained at") {
					t.Fatal(err)
				}
				old = migrationTestStore(t, path, now)
				if fingerprintTest(t, old.db) != before {
					t.Fatal("failed migration changed schema13")
				}
				backups := savedMigrationBackups(t, path)
				if len(backups) != 1 {
					t.Fatal(backups)
				}
				verifyMigrationArchive(t, backups[0], before, 13)
				var leftover int
				old.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name LIKE 'handheld_%'`).Scan(&leftover)
				if leftover != 0 {
					t.Fatal("partial phone tables", leftover)
				}
				testExec(t, old, `DROP TRIGGER reject_handheld_version`)
			}
			before := fingerprintTest(t, old.db)
			old.Close()
			s := openPromotionMigrationStore(t, path, now)
			assertShopperPriorTables(t, s.db, prior)
			if got := migrationQuerySnapshot(t, s.db, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`); !reflect.DeepEqual(objects, got) {
				t.Fatal("extension schema changed")
			}
			for _, table := range []string{"handheld_invitations", "handheld_grants", "handheld_pick_commands", "handheld_pair_commands", "order_event_actors"} {
				if testCount(t, s, table) != 0 {
					t.Fatal("invented existing worker history", table)
				}
			}
			if testCount(t, s, "handheld_state") != 1 {
				t.Fatal("missing epoch")
			}
			assertForeignKeysEnabled(t, s.db)
			assertWriterFenceCoverage(t, s.db)
			backups := savedMigrationBackups(t, path)
			verifyMigrationArchive(t, backups[len(backups)-1], before, 13)
			fingerprint := fingerprintTest(t, s.db)
			s.Close()
			restarted := openPromotionMigrationStore(t, path, now)
			if fingerprintTest(t, restarted.db) != fingerprint || !reflect.DeepEqual(backups, savedMigrationBackups(t, path)) {
				t.Fatal("restart changed phone state/backup")
			}
		})
	}
}
func TestHandheldMigrationFencesOldV13WritersAndFutureReads(t *testing.T) {
	path, now := populatedHandheldV13(t)
	old, err := sql.Open(handheldV13Driver, "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	old.SetMaxOpenConns(1)
	statement, err := old.Prepare(`UPDATE shopper_assignments SET version=version+1 WHERE state='active'`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	s := openPromotionMigrationStore(t, path, now)
	_, _, g := handheldFixture(t, s)
	c := handheldCommand(t, s, g, 0, 1)
	if _, err = s.ConfirmHandheldPick(g.Token, g.CSRF, c); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	if _, err = statement.Exec(); err == nil || !strings.Contains(err.Error(), "writer 14") {
		t.Fatal("old prepared writer accepted", err)
	}
	for _, q := range []string{`UPDATE handheld_state SET epoch='stale'`, `DELETE FROM handheld_grants`, `UPDATE products SET stock=0 WHERE id=1`, `DELETE FROM order_event_actors`, `INSERT INTO schema_version VALUES(15)`} {
		if _, err = old.Exec(q); err == nil || !strings.Contains(err.Error(), "writer 14") {
			t.Fatal("old write accepted", q, err)
		}
	}
	if before != fingerprintTest(t, s.db) {
		t.Fatal("old binary changed data")
	}
	testExec(t, s, `INSERT INTO schema_version VALUES(15)`)
	if _, _, err = s.HandheldTask(g.Token); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatal("future projection accepted", err)
	}
	if _, err = s.ConfirmHandheldPick(g.Token, g.CSRF, HandheldPick{}); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatal("future mutation accepted", err)
	}
}
func TestHandheldResetArchiveKeepsAuditAndRevokesReusedIDs(t *testing.T) {
	s := newTestStore(t)
	_, _, g := handheldFixture(t, s)
	c := handheldCommand(t, s, g, 0, 1)
	if _, err := s.ConfirmHandheldPick(g.Token, g.CSRF, c); err != nil {
		t.Fatal(err)
	}
	var oldEpoch string
	s.db.QueryRow(`SELECT epoch FROM handheld_state`).Scan(&oldEpoch)
	before := fingerprintTest(t, s.db)
	result := resetTest(t, s)
	backup, err := openBackupReadOnly(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if fingerprintTest(t, backup) != before {
		t.Fatal("archive lost grant/command/audit")
	}
	for _, table := range []string{"handheld_invitations", "handheld_grants", "handheld_pair_sessions", "handheld_pair_commands", "handheld_pick_commands", "order_event_actors"} {
		if testCount(t, s, table) != 0 {
			t.Fatal("reset retained authority", table)
		}
	}
	var newEpoch string
	s.db.QueryRow(`SELECT epoch FROM handheld_state`).Scan(&newEpoch)
	if newEpoch == oldEpoch {
		t.Fatal("reset reused epoch")
	}
	_, _, newGrant := handheldFixture(t, s)
	if newGrant.Token == g.Token {
		t.Fatal("reset reused token")
	}
	assertHandheldDenied(t, s, g, c)
}
