package shop

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func populatedEmployeeV15(t *testing.T) (string, time.Time) {
	t.Helper()
	path, now := populatedMessagesV14(t)
	s := migrationTestStore(t, path, now)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = applyMigration(tx, orderMessagesMigration); err != nil {
		t.Fatal(err)
	}
	if err = installWriterFences(tx, 15); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO schema_version VALUES(15)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	return path, now
}
func TestEmployeeMigrationPreservesV15BackupFailureRetryRestart(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			path, now := populatedEmployeeV15(t)
			old := migrationTestStore(t, path, now)
			prior := shopperPriorTables(t, old.db)
			phone := migrationQuerySnapshot(t, old.db, `SELECT * FROM handheld_grants ORDER BY id`)
			if fail {
				testExec(t, old, `CREATE TRIGGER reject_employee_version BEFORE INSERT ON schema_version WHEN NEW.version=16 BEGIN SELECT RAISE(ABORT,'employee marker failure'); END`)
				before := fingerprintTest(t, old.db)
				old.Close()
				broken, err := OpenWithClock(path, func() time.Time { return now })
				if err == nil {
					broken.Close()
					t.Fatal("migration failure ignored")
				}
				if !strings.Contains(err.Error(), "employee marker failure") || !strings.Contains(err.Error(), "pre-migration data retained at") {
					t.Fatal(err)
				}
				old = migrationTestStore(t, path, now)
				if fingerprintTest(t, old.db) != before {
					t.Fatal("failed migration changed database")
				}
				archives := savedMigrationBackups(t, path)
				verifyMigrationArchive(t, archives[len(archives)-1], before, 15)
				testExec(t, old, `DROP TRIGGER reject_employee_version`)
			}
			before := fingerprintTest(t, old.db)
			old.Close()
			s := openPromotionMigrationStore(t, path, now)
			assertShopperPriorTables(t, s.db, prior)
			if !reflect.DeepEqual(phone, migrationQuerySnapshot(t, s.db, `SELECT * FROM handheld_grants ORDER BY id`)) {
				t.Fatal("legacy grants altered")
			}
			for _, table := range []string{"employee_sessions", "employee_store_orders", "employee_grant_links", "employee_commands"} {
				if testCount(t, s, table) != 0 {
					t.Fatal("migration created identity,shared work,or grant", table)
				}
			}
			assertForeignKeysEnabled(t, s.db)
			assertWriterFenceCoverage(t, s.db)
			backups := savedMigrationBackups(t, path)
			verifyMigrationArchive(t, backups[len(backups)-1], before, 15)
			fp := fingerprintTest(t, s.db)
			s.Close()
			restarted := openPromotionMigrationStore(t, path, now)
			if fingerprintTest(t, restarted.db) != fp || !reflect.DeepEqual(backups, savedMigrationBackups(t, path)) {
				t.Fatal("restart regenerated demo or archive")
			}
		})
	}
}
