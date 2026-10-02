package shop

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

const attentionLegacyV10Driver = "attention_legacy_sqlite_v10"

func init() {
	sql.Register(attentionLegacyV10Driver, &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		return conn.RegisterFunc("app_schema_version", func() int64 { return 10 }, true)
	}})
}

// Build the saved v10 boundary without Open or the current migration chain.
// The established v8 fixture already contains edited catalog data, receipts,
// partial picks, assignments, promotion history, custom objects and sequences.
func populatedAttentionV10(t *testing.T) (string, time.Time, string) {
	t.Helper()
	path, now := populatedWeightedV8(t)
	s := migrationTestStore(t, path, now)
	testExec(t, s, `PRAGMA foreign_keys=OFF`)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = applyMigration(tx, productImagesMigration); err != nil {
		t.Fatal(err)
	}
	if err = migrateWeighted(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `PRAGMA foreign_keys=ON`)
	var owner string
	if err = s.db.QueryRow(`SELECT session_id FROM orders WHERE id=1`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	// Preserve an actual weight independently of the immutable requested weight.
	testExec(t, s, `INSERT INTO products(id,name,description,category,barcode,icon,price,stock,category_id,sku,sale_unit,price_basis,quantity_step) VALUES(99,'Saved weighted apples','Retain measured stock','Saved produce','WEIGHT-V10','apple',349,473,1,'WEIGHT-V10','g',1000,50)`)
	testExec(t, s, `INSERT INTO orders(id,reference,session_id,checkout_key,total,status,created,order_version) VALUES(5,'SAVED-WEIGHT-V10',?,'saved-weight-v10-key',175,'Picking','2026-10-02 11:00 UTC',7)`, owner)
	testExec(t, s, `INSERT INTO order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,subtotal) VALUES(5,99,'Requested weight snapshot',349,500,'WEIGHT-V10','g',1000,50,175)`)
	testExec(t, s, `INSERT INTO working_order_items(order_id,product_id,name,price,quantity,picked_quantity,pick_version,sku,sale_unit,price_basis,quantity_step,allocated_quantity,measurement_confirmed) VALUES(5,99,'Requested weight snapshot',349,500,527,4,'WEIGHT-V10','g',1000,50,527,1)`)
	testExec(t, s, `INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details,created) VALUES(5,'saved-weight-v10-command','saved-weight-v10-hash','measure','Measured before upgrade','Actual 527g retained','2026-10-02 11:01 UTC')`)
	testExec(t, s, `CREATE INDEX retained_order_reference ON orders(reference); CREATE INDEX retained_event_reason ON order_events(reason)`)
	var version, attentionColumns int
	if err = s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 10 {
		t.Fatalf("fixture must stop at v10: version=%d error=%v", version, err)
	}
	if err = s.db.QueryRow(`SELECT (SELECT count(*) FROM pragma_table_info('orders') WHERE name IN('attention_reason','attention_since'))+(SELECT count(*) FROM pragma_table_info('order_events') WHERE name='visibility')`).Scan(&attentionColumns); err != nil || attentionColumns != 0 {
		t.Fatalf("fixture already contains attention schema: count=%d error=%v", attentionColumns, err)
	}
	assertForeignKeysEnabled(t, s.db)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	return path, now, owner
}

func TestAttentionMigrationPopulatedV10AdditiveUpgradeRollbackAndRestart(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%t", failure), func(t *testing.T) {
			path, now, owner := populatedAttentionV10(t)
			old := migrationTestStore(t, path, now)
			before := shopperPriorTables(t, old.db)
			roots := migrationQuerySnapshot(t, old.db, `SELECT name,rootpage FROM sqlite_schema WHERE type='table' ORDER BY name`)
			objects := migrationQuerySnapshot(t, old.db, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`)
			oldEvents := migrationQuerySnapshot(t, old.db, `SELECT order_id,action,reason,details,created FROM order_events ORDER BY id`)
			if failure {
				testExec(t, old, `CREATE TRIGGER reject_attention_version BEFORE INSERT ON schema_version WHEN NEW.version=11 BEGIN SELECT RAISE(ABORT,'injected attention final version failure'); END`)
				fingerprint := fingerprintTest(t, old.db)
				if err := old.Close(); err != nil {
					t.Fatal(err)
				}
				broken, err := OpenWithClock(path, func() time.Time { return now })
				if err == nil {
					_ = broken.Close()
					t.Fatal("attention migration ignored final-statement failure")
				}
				if !strings.Contains(err.Error(), "injected attention final version failure") {
					t.Fatalf("migration failed before injected final statement: %v", err)
				}
				old = migrationTestStore(t, path, now)
				if fingerprintTest(t, old.db) != fingerprint {
					t.Fatal("failed attention upgrade changed schema, rows, audit or high-water marks")
				}
				testExec(t, old, `DROP TRIGGER reject_attention_version`)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			s := openPromotionMigrationStore(t, path, now)
			assertShopperPriorTables(t, s.db, before)
			if got := migrationQuerySnapshot(t, s.db, `SELECT name,rootpage FROM sqlite_schema WHERE type='table' ORDER BY name`); !reflect.DeepEqual(roots, got) {
				t.Fatal("attention migration rebuilt existing tables or added unrelated tables")
			}
			if got := migrationQuerySnapshot(t, s.db, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`); !reflect.DeepEqual(objects, got) {
				t.Fatal("attention migration changed retained indexes, views or triggers")
			}
			var version, versions, held, privateEvents int
			if err := s.db.QueryRow(`SELECT MAX(version),count(*) FROM schema_version`).Scan(&version, &versions); err != nil || version != latestSchemaVersion || versions != latestSchemaVersion {
				t.Fatalf("attention schema version=%d markers=%d latest=%d error=%v", version, versions, latestSchemaVersion, err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM orders WHERE attention_reason<>'' OR attention_since<>0`).Scan(&held); err != nil || held != 0 {
				t.Fatal("migration invented attention holds", held, err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM order_events WHERE visibility<>'customer'`).Scan(&privateEvents); err != nil || privateEvents != 0 {
				t.Fatal("migration reclassified established customer history", privateEvents, err)
			}
			for id := int64(1); id <= 5; id++ {
				public := testOrder(t, s, id, owner)
				manager := attentionManagerOrder(t, s, id, owner)
				want := 0
				for _, row := range oldEvents {
					if row[0].(int64) == id {
						want++
					}
				}
				if len(public.Events) != want || !reflect.DeepEqual(public.Events, manager.Events) || public.Held || manager.Held {
					t.Fatalf("upgrade changed saved visibility for order %d: public=%+v manager=%+v", id, public.Events, manager.Events)
				}
			}
			weighted := testOrder(t, s, 5, owner)
			if weighted.Total != 175 || weighted.WorkingTotal != 184 || weighted.WorkingItems[0].Quantity != 500 || weighted.WorkingItems[0].Allocated != 527 || !weighted.WorkingItems[0].Measured || weighted.Version != 7 {
				t.Fatal("upgrade changed requested or measured weight", weighted)
			}
			assertForeignKeysEnabled(t, s.db)
			assertWriterFenceCoverage(t, s.db)
			attentionApply(t, s, 2, owner, "hold", "New private reason after v10 upgrade")
			attentionApply(t, s, 2, owner, "note", "Preserve private note across restart")
			fingerprint := fingerprintTest(t, s.db)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openPromotionMigrationStore(t, path, now.Add(time.Hour))
			if fingerprintTest(t, reopened.db) != fingerprint {
				t.Fatal("restart repeated attention migration or changed saved private history")
			}
			if public := testOrder(t, reopened, 2, owner); !public.Held || public.AttentionReason != "" || public.AttentionSince != 0 {
				t.Fatal("restart exposed internal attention state", public)
			}
			if private := attentionManagerOrder(t, reopened, 2, owner); !private.Held || private.AttentionReason != "New private reason after v10 upgrade" || len(private.Events) != 2 {
				t.Fatal("restart lost internal attention history", private)
			}
			if len(savedBackups(t, path)) != 0 {
				t.Fatal("ordinary attention upgrade created a reset archive")
			}
		})
	}
}

func TestAttentionMigrationFencesAlreadyOpenV10Writers(t *testing.T) {
	path, now, _ := populatedAttentionV10(t)
	legacy, err := sql.Open(attentionLegacyV10Driver, "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	legacy.SetMaxOpenConns(1)
	prepared, err := legacy.Prepare(`UPDATE products SET stock=stock-1 WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	s := openPromotionMigrationStore(t, path, now)
	before := fingerprintTest(t, s.db)
	if _, err = prepared.Exec(); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("writer %d", latestSchemaVersion)) {
		t.Fatal("prepared schema-10 connection mutated schema 11", err)
	}
	for _, statement := range []string{
		`UPDATE orders SET status='Ready' WHERE id=2`,
		`DELETE FROM order_events WHERE order_id=1`,
		`INSERT INTO schema_version VALUES(12)`,
	} {
		if _, err = legacy.Exec(statement); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("writer %d", latestSchemaVersion)) {
			t.Fatalf("legacy statement bypassed v11 fence: %s: %v", statement, err)
		}
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("rejected v10 writer changed upgraded database")
	}
	tables := migrationQuerySnapshot(t, s.db, `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	for _, table := range tables {
		for _, op := range []string{"insert", "update", "delete"} {
			name := fmt.Sprintf("app_writer_v11_%s_%s", table[0], op)
			var statement string
			if err := s.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=? AND tbl_name=?`, name, table[0]).Scan(&statement); err != nil || !strings.Contains(statement, "app_schema_version()") || !strings.Contains(statement, "<11") {
				t.Fatalf("missing schema-11 %s fence on %s: %q %v", op, table[0], statement, err)
			}
		}
	}
}
