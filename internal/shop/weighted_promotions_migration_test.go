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

const weightedPromotionV11Driver = "weighted_promotion_v11"

func init() {
	sql.Register(weightedPromotionV11Driver, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		return c.RegisterFunc("app_schema_version", func() int64 { return 11 }, true)
	}})
}

func populatedWeightedPromotionV11(t *testing.T) (string, time.Time) {
	t.Helper()
	path, now, _ := populatedAttentionV10(t)
	s := migrationTestStore(t, path, now)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = applyMigration(tx, attentionMigration); err != nil {
		t.Fatal(err)
	}
	if err = installWriterFences(tx, 11); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO schema_version VALUES(11); UPDATE orders SET attention_reason='Keep this private hold',attention_since=123 WHERE id=5; INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details,visibility) VALUES(5,'private-v11','hash','note','Keep this internal note','Private saved detail','internal')`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version, columns int
	if err = s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 11 {
		t.Fatal("invalid fixture version", version, err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('promotions') WHERE name IN('sale_unit','price_basis')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatal("fixture already upgraded", columns, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	return path, now
}

func TestWeightedPromotionMigrationV11PreservesRowsRollbackAndRestart(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			path, now := populatedWeightedPromotionV11(t)
			old := migrationTestStore(t, path, now)
			before := shopperPriorTables(t, old.db)
			roots := migrationQuerySnapshot(t, old.db, `SELECT name,rootpage FROM sqlite_schema WHERE type='table' AND name NOT IN ('shoppers','shopper_roster_profiles','shopper_roster_events') ORDER BY name`)
			if fail {
				testExec(t, old, `CREATE TRIGGER fail_promotion_unit_version BEFORE INSERT ON schema_version WHEN NEW.version=12 BEGIN SELECT RAISE(ABORT,'promotion basis failure'); END`)
				fingerprint := fingerprintTest(t, old.db)
				old.Close()
				broken, err := OpenWithClock(path, func() time.Time { return now })
				if err == nil {
					broken.Close()
					t.Fatal("injected migration failure ignored")
				}
				if !strings.Contains(err.Error(), "promotion basis failure") {
					t.Fatal(err)
				}
				old = migrationTestStore(t, path, now)
				if fingerprintTest(t, old.db) != fingerprint {
					t.Fatal("failed migration changed data/schema")
				}
				testExec(t, old, `DROP TRIGGER fail_promotion_unit_version`)
			}
			old.Close()
			s := openPromotionMigrationStore(t, path, now)
			assertShopperPriorTables(t, s.db, before)
			if !reflect.DeepEqual(roots, migrationQuerySnapshot(t, s.db, `SELECT name,rootpage FROM sqlite_schema WHERE type='table' AND name NOT IN ('shoppers','shopper_roster_profiles','shopper_roster_events') ORDER BY name`)) {
				t.Fatal("additive migration rebuilt tables")
			}
			var count int
			if err := s.db.QueryRow(`SELECT count(*) FROM promotions WHERE sale_unit<>'each' OR price_basis<>1`).Scan(&count); err != nil || count != 0 {
				t.Fatal("historical counted basis reinterpreted", count, err)
			}
			for _, version := range []int{10, 11, 12} {
				for _, op := range []string{"insert", "update", "delete"} {
					var sqlText string
					if err := s.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name=?`, fmt.Sprintf("app_writer_v%d_promotions_%s", version, op)).Scan(&sqlText); err != nil || !strings.Contains(sqlText, fmt.Sprintf("<%d", version)) {
						t.Fatal("missing generation fence", version, op, err)
					}
				}
			}
			assertForeignKeysEnabled(t, s.db)
			fingerprint := fingerprintTest(t, s.db)
			s.Close()
			s = openPromotionMigrationStore(t, path, now)
			if fingerprintTest(t, s.db) != fingerprint {
				t.Fatal("restart changed migrated rows")
			}
		})
	}
}

func TestWeightedPromotionMigrationRejectsAlreadyOpenV11Writer(t *testing.T) {
	path, now := populatedWeightedPromotionV11(t)
	old, err := sql.Open(weightedPromotionV11Driver, "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	old.SetMaxOpenConns(1)
	statement, err := old.Prepare(`UPDATE promotions SET sale_price=sale_price-1`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	s := openPromotionMigrationStore(t, path, now)
	before := fingerprintTest(t, s.db)
	if _, err = statement.Exec(); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("writer %d", latestSchemaVersion)) {
		t.Fatal("prepared old promotion write accepted", err)
	}
	for _, q := range []string{`UPDATE products SET price=price+1 WHERE id=1`, `DELETE FROM promotions`, `INSERT INTO promotions(product_id,sale_price,starts,ends) VALUES(1,199,9000000000,9000000060)`} {
		if _, err = old.Exec(q); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("writer %d", latestSchemaVersion)) {
			t.Fatal("old writer accepted", q, err)
		}
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("old writer changed rows or sequences")
	}
}

func TestWeightedPromotionResetArchiveKeepsRatesAndActualWeights(t *testing.T) {
	s, clock, _ := promotionTestStore(t)
	now := clock.now().Unix()
	p := weightedProduct(t, s, 3000, 499, 1)
	sale := saveTestPromotion(t, s, p.ID, 349, now-60, now+3600)
	if err := s.SetFeatured(p.ID, 0, true); err != nil {
		t.Fatal(err)
	}
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, 500)
	id := testCheckout(t, s, owner.ID)
	confirmWeight(t, s, id, owner.ID, p.ID, 527, "")
	before := fingerprintTest(t, s.db)
	result := resetTest(t, s)
	backup, err := openBackupReadOnly(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if fingerprintTest(t, backup) != before {
		t.Fatal("reset archive changed weighted sale or receipt")
	}
	var unit string
	var basis, price int64
	if err = backup.QueryRow(`SELECT sale_unit,price_basis,sale_price FROM promotions WHERE id=?`, sale.ID).Scan(&unit, &basis, &price); err != nil || unit != "g" || basis != 1000 || price != 349 {
		t.Fatal("lost recovery rate basis", unit, basis, price, err)
	}
	assertResetPromotionSamples(t, s, clock.now())
	assertDemoSeed(t, s)
}
