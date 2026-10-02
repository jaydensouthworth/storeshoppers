package shop

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Stop at the saved v5 boundary instead of Open, which always upgrades to the
// current schema. Include edited allocations, actual picks, terminal totals,
// active basket holds and audit history so this is not a fresh-seed test.
func populatedShopperV5(t *testing.T) (string, Session) {
	t.Helper()
	path, owner := populatedV4(t)
	db := migrationRawDB(t, path)
	migrationExec(t, db, orderOverridesMigration)
	migrationExec(t, db, `UPDATE products SET name='Manager retained apples',price=811,stock=stock-1,version=18,catalog_version=4,price_version=3 WHERE id=1`)
	migrationExec(t, db, `INSERT INTO product_types(name,normalized_name,version) VALUES('Local fruit','local fruit',3)`)
	migrationExec(t, db, `UPDATE products SET type_id=(SELECT id FROM product_types WHERE normalized_name='local fruit') WHERE id=1`)
	migrationExec(t, db, `UPDATE categories SET version=4 WHERE id=(SELECT category_id FROM products WHERE id=1)`)
	migrationExec(t, db, `INSERT INTO catalog_events(kind,entity_id,action,name,details,created) VALUES('product',1,'edit','Manager retained apples','Retain catalog audit','2025-03-15 09:00 UTC')`)
	migrationExec(t, db, `UPDATE baskets SET revision=19,hold_until=? WHERE owner_session_id=?`, time.Now().Add(24*time.Hour).Unix(), owner.ID)
	migrationExec(t, db, `UPDATE cart SET reserved=2 WHERE product_id=2`)
	migrationExec(t, db, `UPDATE products SET stock=stock-2 WHERE id=2`)
	migrationExec(t, db, `INSERT INTO basket_events(basket_id,action,reason,details,created) SELECT id,'reserve','Saved manager hold','Retain basket audit','2025-03-15 09:01 UTC' FROM baskets WHERE owner_session_id=?`, owner.ID)
	migrationExec(t, db, `UPDATE orders SET instructions='Keep the customer receipt and instructions',order_version=9 WHERE id=1`)
	migrationExec(t, db, `UPDATE working_order_items SET quantity=3,pick_version=8 WHERE order_id=1 AND product_id=1`)
	migrationExec(t, db, `UPDATE orders SET order_version=13,final_total=299,completion_kind='partial' WHERE id=4`)
	migrationExec(t, db, `UPDATE working_order_items SET picked_quantity=CASE WHEN product_id=1 THEN 1 ELSE 0 END,unavailable_quantity=1,pick_version=12 WHERE order_id=4`)
	migrationExec(t, db, `INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details,created) VALUES
 (1,'retained-v5-quantity-command','retained-hash','set','Saved allocation change','Apples increased to three','2025-03-15 09:02 UTC'),
 (4,'retained-v5-finish-command','retained-finish-hash','finish','Only one apple available','Retain actual partial fulfillment','2025-03-15 09:03 UTC')`)
	migrationExec(t, db, `CREATE TABLE custom_shop_notes(id INTEGER PRIMARY KEY,note TEXT NOT NULL)`)
	migrationExec(t, db, `INSERT INTO custom_shop_notes VALUES(1,'Preserve extension data during ordinary upgrades')`)
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("fixture must be schema v5: version=%d error=%v", version, err)
	}
	if err := checkSQLite(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, owner
}

func shopperPriorTables(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	tables := migrationQuerySnapshot(t, db, `SELECT name FROM sqlite_master WHERE type='table' AND (name NOT LIKE 'sqlite_%' OR name='sqlite_sequence') AND name!='schema_version' ORDER BY name`)
	before := make(map[string][][]any, len(tables))
	for _, table := range tables {
		name := table[0].(string)
		before[name] = migrationQuerySnapshot(t, db, `SELECT * FROM `+quoteIdentifier(name)+` ORDER BY rowid`)
	}
	return before
}

func assertShopperPriorTables(t *testing.T, db *sql.DB, before map[string][][]any) {
	t.Helper()
	for name, want := range before {
		got := migrationQuerySnapshot(t, db, `SELECT * FROM `+quoteIdentifier(name)+` ORDER BY rowid`)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("shopper upgrade changed existing %s: got %v, want %v", name, got, want)
		}
	}
}

func assertShopperRoster(t *testing.T, db *sql.DB) {
	t.Helper()
	want := [][]any{
		{int64(1), "Avery Morgan", "AM"},
		{int64(2), "Jordan Lee", "JL"},
		{int64(3), "Casey Rivera", "CR"},
	}
	if got := migrationQuerySnapshot(t, db, `SELECT id,name,initials FROM shoppers ORDER BY id`); !reflect.DeepEqual(got, want) {
		t.Fatalf("fake shopper roster = %v, want %v", got, want)
	}
}

func TestShoppersPopulatedV5UpgradePreservesDataAndRetries(t *testing.T) {
	for _, injectFailure := range []bool{false, true} {
		name := "upgrade"
		if injectFailure {
			name = "final statement rollback and retry"
		}
		t.Run(name, func(t *testing.T) {
			path, owner := populatedShopperV5(t)
			db := migrationRawDB(t, path)
			before := shopperPriorTables(t, db)
			if injectFailure {
				migrationExec(t, db, fmt.Sprintf(`CREATE TRIGGER reject_shopper_migration BEFORE INSERT ON schema_version WHEN NEW.version=%d BEGIN SELECT RAISE(ABORT,'injected shopper migration failure'); END`, latestSchemaVersion))
				fingerprint := fingerprintTest(t, db)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				broken, err := Open(path)
				if err == nil {
					broken.Close()
					t.Fatal("shopper migration succeeded despite final-statement failure")
				}
				if !strings.Contains(err.Error(), "injected shopper migration failure") {
					t.Fatalf("migration failed for an unrelated reason: %v", err)
				}
				db = migrationRawDB(t, path)
				if got := fingerprintTest(t, db); got != fingerprint {
					t.Fatal("failed shopper migration changed schema, rows, sequences or audit")
				}
				var retained int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('shoppers','shopper_assignments','shopper_event_links')`).Scan(&retained); err != nil || retained != 0 {
					t.Fatalf("failed migration left shopper tables: count=%d error=%v", retained, err)
				}
				migrationExec(t, db, `DROP TRIGGER reject_shopper_migration`)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			s := openTestStore(t, path)
			assertShopperPriorTables(t, s.db, before)
			assertShopperRoster(t, s.db)
			for _, table := range []string{"shopper_assignments", "shopper_event_links"} {
				if n := testCount(t, s, table); n != 0 {
					t.Fatalf("migration invented %d %s rows for historical orders", n, table)
				}
			}
			var version, versions int
			if err := s.db.QueryRow(`SELECT MAX(version),COUNT(*) FROM schema_version`).Scan(&version, &versions); err != nil || version != latestSchemaVersion || versions != latestSchemaVersion {
				t.Fatalf("migration versions=%d max=%d error=%v, want %d", versions, version, err, latestSchemaVersion)
			}
			if o := testOrder(t, s, 4, owner.ID); o.FinalTotal != 299 || o.PickedCount != 1 || o.RequiredCount != 3 || o.Percent == 100 || o.CompletionKind != "partial" {
				t.Fatalf("migration changed actual partial fulfillment: %+v", o)
			}
			if err := checkSQLite(s.db); err != nil {
				t.Fatal(err)
			}
			fingerprint := fingerprintTest(t, s.db)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openTestStore(t, path)
			if got := fingerprintTest(t, reopened.db); got != fingerprint {
				t.Fatal("ordinary restart replayed the shopper migration or changed existing state")
			}
			if len(savedBackups(t, path)) != 0 {
				t.Fatal("ordinary migration or restart created a demo-reset archive")
			}
		})
	}
}

// A saved assignment history is deliberately populated independently of the
// command implementation: this checks storage/recovery rather than API behavior.
func populateShopperHistory(t *testing.T, s *Store) {
	t.Helper()
	testExec(t, s, `INSERT INTO shopper_assignments(order_id,shopper_id,state,version,created,ended) VALUES
 (1,2,'cancelled',3,'2025-03-15 10:00 UTC','2025-03-15 10:01 UTC'),
 (1,1,'active',5,'2025-03-15 10:02 UTC',''),
 (2,1,'active',2,'2025-03-15 10:03 UTC',''),
 (4,3,'ended',4,'2025-03-15 10:04 UTC','2025-03-15 10:05 UTC')`)
	testExec(t, s, `INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details,created) VALUES
 (1,'saved-shopper-cancel-command','saved-shopper-cancel-hash','shopper-cancel','Retain assignment cancellation','Jordan Lee assignment cancelled; order retained','2025-03-15 10:01 UTC'),
 (1,'saved-shopper-assign-command','saved-shopper-assign-hash','shopper-assign','Retain active assignment','Avery Morgan assigned','2025-03-15 10:02 UTC'),
 (4,'saved-shopper-ended-command','saved-shopper-ended-hash','shopper-ended','Order reached terminal state','Casey Rivera assignment ended','2025-03-15 10:05 UTC')`)
	testExec(t, s, `INSERT INTO shopper_event_links(event_id,assignment_id,from_shopper_id,to_shopper_id)
 SELECT e.id,a.id,2,NULL FROM order_events e JOIN shopper_assignments a ON a.order_id=e.order_id AND a.state='cancelled' WHERE e.command_key='saved-shopper-cancel-command'
 UNION ALL SELECT e.id,a.id,NULL,1 FROM order_events e JOIN shopper_assignments a ON a.order_id=e.order_id AND a.state='active' WHERE e.command_key='saved-shopper-assign-command'
 UNION ALL SELECT e.id,a.id,3,NULL FROM order_events e JOIN shopper_assignments a ON a.order_id=e.order_id AND a.state='ended' WHERE e.command_key='saved-shopper-ended-command'`)
	if n := testCount(t, s, "shopper_event_links"); n != 3 {
		t.Fatalf("assignment audit fixture has %d links, want 3", n)
	}
}

func TestShoppersRestartAndExplicitResetPreserveRecoveryHistory(t *testing.T) {
	path, _ := populatedShopperV5(t)
	s := openTestStore(t, path)
	populateShopperHistory(t, s)
	before := fingerprintTest(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, path)
	if got := fingerprintTest(t, s.db); got != before {
		t.Fatal("restart changed assignments, cancellation history or preexisting records")
	}
	if len(savedBackups(t, path)) != 0 {
		t.Fatal("ordinary restart created an archive or implicitly reset demo data")
	}
	result := resetTest(t, s)
	if !result.Reset || result.BackupPath == "" {
		t.Fatalf("explicit reset did not report its recovery archive: %+v", result)
	}
	assertDemoSeed(t, s)
	assertShopperRoster(t, s.db)
	for _, table := range []string{"shopper_assignments", "shopper_event_links", "working_order_items", "order_events"} {
		if n := testCount(t, s, table); n != 0 {
			t.Errorf("explicit reset retained %d %s rows", n, table)
		}
	}
	var version int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != latestSchemaVersion {
		t.Fatalf("reset schema=%d error=%v, want %d", version, err, latestSchemaVersion)
	}
	backup, err := openBackupReadOnly(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := checkSQLite(backup); err != nil {
		t.Fatal(err)
	}
	if got := fingerprintTest(t, backup); got != before {
		t.Fatal("reset archive lost assignments, history or preexisting v5 data")
	}
	restoredPath := filepath.Join(t.TempDir(), "restored-shopper-history.db")
	restoredDB := migrationRawDB(t, restoredPath)
	if err := copySQLite(restoredDB, backup); err != nil {
		t.Fatal(err)
	}
	if err := restoredDB.Close(); err != nil {
		t.Fatal(err)
	}
	restored := openTestStore(t, restoredPath)
	if got := fingerprintTest(t, restored.db); got != before {
		t.Fatal("opening the restored archive changed shopper assignment history")
	}
	if n := testCount(t, restored, "shopper_assignments"); n != 4 {
		t.Fatalf("restored assignment history=%d, want 4", n)
	}
	baseline := fingerprintTest(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	if got := fingerprintTest(t, reopened.db); got != baseline {
		t.Fatal("restart after reset mutated the fresh shopper baseline")
	}
}
