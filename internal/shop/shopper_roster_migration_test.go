package shop

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

const shopperRosterV12Driver = "shopper_roster_v12"

func init() {
	sql.Register(shopperRosterV12Driver, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		return c.RegisterFunc("app_schema_version", func() int64 { return 12 }, true)
	}})
}

// Upgrade only to the actual preceding boundary. This populated fixture already
// has promotions, private review notes, retained extension data, active/ended
// assignments, immutable receipts, divergent allocations, real picks and weights.
func populatedShopperRosterV12(t *testing.T) (string, time.Time) {
	t.Helper()
	path, now := populatedWeightedPromotionV11(t)
	s := migrationTestStore(t, path, now)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = applyMigration(tx, weightedPromotionsMigration); err != nil {
		t.Fatal(err)
	}
	if err = installWriterFences(tx, 12); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO schema_version VALUES(12)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `CREATE TABLE retained_roster_probe(id INTEGER PRIMARY KEY,note TEXT NOT NULL); INSERT INTO retained_roster_probe VALUES(1,'Retain extension data'); CREATE INDEX retained_shopper_name ON shoppers(name); CREATE VIEW retained_shopper_names AS SELECT a.order_id,s.name FROM shopper_assignments a JOIN shoppers s ON s.id=a.shopper_id; CREATE TRIGGER retained_shopper_trigger AFTER UPDATE OF name ON shoppers BEGIN SELECT 1; END;
 CREATE TRIGGER retained_assignment_snapshot_guard AFTER UPDATE ON shopper_assignments BEGIN INSERT INTO retained_roster_probe(note) VALUES('An operational assignment changed'); END;
 CREATE TRIGGER retained_cross_table_shopper_reference AFTER UPDATE OF stock ON products BEGIN SELECT name FROM shoppers WHERE id=1; END`)
	var version int
	if err = s.db.QueryRow(`SELECT max(version) FROM schema_version`).Scan(&version); err != nil || version != 12 {
		t.Fatal(version, err)
	}
	if testCount(t, s, "shopper_assignments") == 0 || testCount(t, s, "shopper_event_links") == 0 {
		t.Fatal("fixture missing assignment/history")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	return path, now
}

func TestShopperRosterMigrationPopulatedV12RollbackBackupRetryRestart(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			path, now := populatedShopperRosterV12(t)
			old := migrationTestStore(t, path, now)
			prior := shopperPriorTables(t, old.db)
			objects := migrationQuerySnapshot(t, old.db, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`)
			if failure {
				testExec(t, old, `CREATE TRIGGER reject_roster_version BEFORE INSERT ON schema_version WHEN NEW.version=13 BEGIN SELECT RAISE(ABORT,'roster marker failure'); END`)
				before := fingerprintTest(t, old.db)
				old.Close()
				broken, err := OpenWithClock(path, func() time.Time { return now })
				if err == nil {
					broken.Close()
					t.Fatal("failure ignored")
				}
				if !strings.Contains(err.Error(), "roster marker failure") || !strings.Contains(err.Error(), "pre-migration data retained at") {
					t.Fatal(err)
				}
				old = migrationTestStore(t, path, now)
				if fingerprintTest(t, old.db) != before {
					t.Fatal("failed migration changed original v12")
				}
				backups := savedMigrationBackups(t, path)
				if len(backups) != 1 {
					t.Fatal("missing archive", backups)
				}
				verifyMigrationArchive(t, backups[0], before, 12)
				for _, table := range []string{"shopper_roster_profiles", "shopper_roster_events", "shoppers_v13"} {
					var count int
					old.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, table).Scan(&count)
					if count != 0 {
						t.Fatal("rollback left table", table)
					}
				}
				testExec(t, old, `DROP TRIGGER reject_roster_version`)
			}
			before := fingerprintTest(t, old.db)
			old.Close()
			s := openPromotionMigrationStore(t, path, now)
			assertShopperPriorTables(t, s.db, prior)
			if got := migrationQuerySnapshot(t, s.db, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`); !reflect.DeepEqual(objects, got) {
				t.Fatal("changed extension index/view/trigger", got)
			}
			var bad int
			if err := s.db.QueryRow(`SELECT count(*) FROM shopper_assignments a JOIN shoppers s ON s.id=a.shopper_id WHERE a.shopper_name<>s.name OR a.shopper_initials<>s.initials`).Scan(&bad); err != nil || bad != 0 {
				t.Fatal("missing assignment snapshot", bad, err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM shopper_event_links l WHERE l.from_shopper_name<>COALESCE((SELECT name FROM shoppers WHERE id=l.from_shopper_id),'') OR l.to_shopper_name<>COALESCE((SELECT name FROM shoppers WHERE id=l.to_shopper_id),'')`).Scan(&bad); err != nil || bad != 0 {
				t.Fatal("missing history snapshot", bad, err)
			}
			if testCount(t, s, "shopper_roster_profiles") != 3 || testCount(t, s, "shopper_roster_events") != 0 {
				t.Fatal("invented roster history")
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM shopper_roster_profiles WHERE capacity<>0 OR availability<>'available' OR archived<>0 OR version<>1 OR scope<>''`).Scan(&bad); err != nil || bad != 0 {
				t.Fatal("changed existing assignment eligibility")
			}
			backups := savedMigrationBackups(t, path)
			if len(backups) == 0 {
				t.Fatal("upgrade omitted backup")
			}
			verifyMigrationArchive(t, backups[len(backups)-1], before, 12)
			assertForeignKeysEnabled(t, s.db)
			assertWriterFenceCoverage(t, s.db)
			fingerprint := fingerprintTest(t, s.db)
			s.Close()
			reopened := openPromotionMigrationStore(t, path, now)
			if fingerprintTest(t, reopened.db) != fingerprint || !reflect.DeepEqual(backups, savedMigrationBackups(t, path)) {
				t.Fatal("restart mutated/repeated migration or backup")
			}
		})
	}
}

func TestShopperRosterMigrationFencesAlreadyOpenV12Writer(t *testing.T) {
	path, now := populatedShopperRosterV12(t)
	old, err := sql.Open(shopperRosterV12Driver, "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	old.SetMaxOpenConns(1)
	statement, err := old.Prepare(`UPDATE shoppers SET name='Old writer rename' WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	s := openPromotionMigrationStore(t, path, now)
	before := fingerprintTest(t, s.db)
	if _, err = statement.Exec(); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("writer %d", latestSchemaVersion)) {
		t.Fatal("prepared v12 writer accepted", err)
	}
	for _, query := range []string{`UPDATE shopper_assignments SET version=version+1`, `DELETE FROM shopper_event_links`, `UPDATE products SET stock=stock-1 WHERE id=1`, `UPDATE shopper_roster_profiles SET archived=1`, `INSERT INTO shopper_roster_events(scope,shopper_id,command_key,command_hash,name,action,details) VALUES('',1,'old-writer-command','hash','Avery','edit','Must be fenced')`, `INSERT INTO schema_version VALUES(14)`} {
		if _, err = old.Exec(query); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("writer %d", latestSchemaVersion)) {
			t.Fatal("v12 mutation accepted", query, err)
		}
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("old writer changed v13")
	}
}

func TestShopperRosterResetRecoveryRetainsProfilesHistoryAndSnapshots(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	create := rosterCommand(t, s, owner.ID, 0, "create")
	person := saveRoster(t, s, owner.ID, 0, create)
	testAssign(t, s, id, owner.ID, person)
	edit := rosterCommand(t, s, owner.ID, person, "edit")
	edit.Name = "Renamed after assignment"
	edit.Availability = "break"
	saveRoster(t, s, owner.ID, person, edit)
	saveRoster(t, s, owner.ID, 2, rosterCommand(t, s, owner.ID, 2, "archive"))
	before := fingerprintTest(t, s.db)
	result := resetTest(t, s)
	assertDemoSeed(t, s)
	assertShopperRoster(t, s.db)
	if testCount(t, s, "shopper_roster_events") != 0 || testCount(t, s, "shopper_roster_profiles") != 3 || testCount(t, s, "shopper_assignments") != 0 {
		t.Fatal("reset retained visitor roster")
	}
	backup, err := openBackupReadOnly(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if fingerprintTest(t, backup) != before {
		t.Fatal("reset archive lost roster data")
	}
	path := filepath.Join(t.TempDir(), "restored-roster.db")
	db := migrationRawDB(t, path)
	if err = copySQLite(db, backup); err != nil {
		t.Fatal(err)
	}
	db.Close()
	restored := openTestStore(t, path)
	if fingerprintTest(t, restored.db) != before {
		t.Fatal("restore/restart changed data")
	}
	if p := rosterProfile(t, restored, owner.ID, person); p.Name != edit.Name || p.Availability != "break" || p.ActiveOrders != 1 {
		t.Fatal("restored profile", p)
	}
	if testOrder(t, restored, id, owner.ID).Assignment.ShopperName != "Taylor Demo" {
		t.Fatal("lost original assignment snapshot")
	}
	if !rosterProfile(t, restored, owner.ID, 2).Archived {
		t.Fatal("lost archived seed override")
	}
}
