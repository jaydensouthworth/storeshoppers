package shop

import (
	"reflect"
	"testing"
)

func populatedV4(t *testing.T) (string, Session) {
	t.Helper()
	path, owner := populatedV1(t)
	db := migrationRawDB(t, path)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = applyMigration(tx, pickingMigration); err != nil {
		t.Fatal(err)
	}
	if err = migrateCatalog(tx); err != nil {
		t.Fatal(err)
	}
	if err = applyMigration(tx, reservationsMigration); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE order_items SET picked_quantity=1,pick_version=7 WHERE order_id=2 AND product_id=1`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return path, owner
}
func TestOrderOverridesV4MigrationPreservesAllHistoryAndRetries(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "rollback and retry"}[failure], func(t *testing.T) {
			path, owner := populatedV4(t)
			db := migrationRawDB(t, path)
			old := map[string][][]any{}
			projections := map[string]string{}
			for _, table := range []string{"products", "sessions", "cart", "baskets", "order_items", "orders", "adjustments"} {
				projections[table] = priorTableProjection(t, db, table)
				old[table] = migrationQuerySnapshot(t, db, `SELECT `+projections[table]+` FROM `+table)
			}
			if failure {
				migrationExec(t, db, `CREATE TRIGGER fail_v5 BEFORE INSERT ON schema_version WHEN NEW.version=5 BEGIN SELECT RAISE(ABORT,'injected v5 failure'); END`)
			}
			db.Close()
			if failure {
				broken, err := Open(path)
				if err == nil {
					broken.Close()
					t.Fatal("migration should fail")
				}
				db = migrationRawDB(t, path)
				for table, want := range old {
					if got := migrationQuerySnapshot(t, db, `SELECT `+projections[table]+` FROM `+table); !reflect.DeepEqual(want, got) {
						t.Fatalf("rollback changed %s", table)
					}
				}
				var tables int
				if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('working_order_items','order_events')`).Scan(&tables); err != nil || tables != 0 {
					t.Fatal("partial v5 tables retained", err)
				}
				migrationExec(t, db, `DROP TRIGGER fail_v5`)
				db.Close()
			}
			s := openTestStore(t, path)
			for table, want := range old {
				if table == "orders" {
					continue
				}
				if got := migrationQuerySnapshot(t, s.db, `SELECT `+projections[table]+` FROM `+table); !reflect.DeepEqual(want, got) {
					t.Fatalf("upgrade changed %s", table)
				}
			}
			if got := migrationQuerySnapshot(t, s.db, `SELECT id,reference,session_id,checkout_key,total,status,created,instructions FROM orders`); !reflect.DeepEqual(old["orders"], got) {
				t.Fatal("upgrade changed original orders")
			}
			for id := int64(1); id <= 4; id++ {
				o := testOrder(t, s, id, owner.ID)
				if o.Total != 847 || len(o.WorkingItems) != 2 || o.WorkingItems[0].Name != "Original receipt apples" {
					t.Fatal("snapshot backfill changed", o)
				}
				if id == 2 && (o.WorkingItems[0].Picked != 1 || o.WorkingItems[0].PickVersion != 7) {
					t.Fatal("picks reset")
				}
				if (id == 3 || id == 4) && (!o.Finalized || o.FinalTotal != 847) {
					t.Fatal("historical final total not retained")
				}
			}
			if err := s.Advance(3, "Ready"); err != nil {
				t.Fatal(err)
			}
			if testOrder(t, s, 3, owner.ID).FinalTotal != 847 {
				t.Fatal("collect recomputed legacy final total")
			}
			// Reopening must not replay the backfill and discard manager working changes.
			c := overrideCommand(t, s, 1, owner.ID, "set", 1, 3)
			if err := s.OverrideOrder(1, owner.ID, false, c); err != nil {
				t.Fatal(err)
			}
			before, _ := databaseFingerprint(s.db)
			s.Close()
			reopened := openTestStore(t, path)
			after, _ := databaseFingerprint(reopened.db)
			if before != after {
				t.Fatal("retry/restart mutated upgraded state")
			}
		})
	}
}
