package shop

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"instore-shopper/internal/productimage"

	"github.com/mattn/go-sqlite3"
)

// Saved schema-8 additions. The old fixture must never use current migration
// strings or Open, which would turn upgrade checks into fresh-schema checks.
const legacyWeightedV8 = `
CREATE TABLE product_details (
 product_id INTEGER PRIMARY KEY REFERENCES products(id),
 kind TEXT NOT NULL DEFAULT 'unspecified' CHECK(kind IN('unspecified','food','nonfood')),
 body TEXT NOT NULL DEFAULT '' CHECK(length(body)<=1600),
 package_label TEXT NOT NULL DEFAULT '' CHECK(length(package_label)<=80),
 package_details TEXT NOT NULL DEFAULT '' CHECK(length(package_details)<=240),
 nutrition_serving TEXT,
 nutrition_energy INTEGER CHECK(nutrition_energy BETWEEN 0 AND 10000),
 nutrition_fat INTEGER CHECK(nutrition_fat BETWEEN 0 AND 100000),
 nutrition_carbs INTEGER CHECK(nutrition_carbs BETWEEN 0 AND 100000),
 nutrition_protein INTEGER CHECK(nutrition_protein BETWEEN 0 AND 100000),
 nutrition_sodium INTEGER CHECK(nutrition_sodium BETWEEN 0 AND 1000000),
 CHECK((nutrition_serving IS NULL AND nutrition_energy IS NULL AND nutrition_fat IS NULL AND nutrition_carbs IS NULL AND nutrition_protein IS NULL AND nutrition_sodium IS NULL)
 OR (kind='food' AND nutrition_serving IS NOT NULL AND length(nutrition_serving) BETWEEN 1 AND 80 AND nutrition_energy IS NOT NULL AND nutrition_fat IS NOT NULL AND nutrition_carbs IS NOT NULL AND nutrition_protein IS NOT NULL AND nutrition_sodium IS NOT NULL))
);
CREATE TABLE product_example_products (example_key TEXT PRIMARY KEY, product_id INTEGER NOT NULL UNIQUE REFERENCES products(id));
CREATE TABLE product_detail_commands (command_key TEXT PRIMARY KEY, command_hash TEXT NOT NULL);
INSERT INTO schema_version VALUES(8);
`

func populatedWeightedV8(t *testing.T) (string, time.Time) {
	t.Helper()
	now := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	path, _ := populatedProductDetailsV7(t, now)
	db := migrationRawDB(t, path)
	migrationExec(t, db, legacyWeightedV8)
	migrationExec(t, db, `INSERT INTO product_details(product_id,kind,body,package_label) VALUES(1,'food','Saved detail body','Two each'),(3,'nonfood','Archived product detail','Keep unchanged')`)
	migrationExec(t, db, `INSERT INTO product_example_products VALUES('saved-example',3); INSERT INTO product_detail_commands VALUES('saved-details-key','saved-details-hash')`)
	migrationExec(t, db, `INSERT INTO working_order_items(id,order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step) VALUES(9000,2,3,'Deleted high ID',479,0,'CUSTOM-PEAR','each',1,1); DELETE FROM working_order_items WHERE id=9000`)
	migrationExec(t, db, `CREATE INDEX retained_product_description ON products(description); CREATE INDEX retained_working_version ON working_order_items(pick_version)`)
	migrationExec(t, db, `CREATE TRIGGER retained_product_trigger AFTER UPDATE OF description ON products BEGIN SELECT 1; END`)
	migrationExec(t, db, `CREATE TRIGGER retained_external_trigger AFTER INSERT ON custom_promotion_recovery_notes BEGIN SELECT count(*) FROM products; END`)
	migrationExec(t, db, `CREATE VIEW retained_product_view AS SELECT id,name,stock FROM products`)
	migrationExec(t, db, `CREATE VIEW retained_write_view AS SELECT id,description FROM products`)
	migrationExec(t, db, `CREATE TRIGGER retained_view_update INSTEAD OF UPDATE ON retained_write_view BEGIN UPDATE products SET description=NEW.description WHERE id=OLD.id; END`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, now
}

func migrationTestStore(t *testing.T, path string, now time.Time) *Store {
	t.Helper()
	db, err := sql.Open(applicationSQLiteDriver, "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return &Store{db: db, now: func() time.Time { return now }}
}

func assertForeignKeysEnabled(t *testing.T, db *sql.DB) {
	t.Helper()
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign key enforcement=%d: %v", enabled, err)
	}
	if err := checkSQLite(db); err != nil {
		t.Fatal(err)
	}
}

func TestWeightedMigrationPreservesPopulatedV8AndRestarts(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%t", fail), func(t *testing.T) {
			path, now := populatedWeightedV8(t)
			old := migrationRawDB(t, path)
			if fail {
				migrationExec(t, old, fmt.Sprintf(`CREATE TRIGGER fail_weighted_version BEFORE INSERT ON schema_version WHEN NEW.version=%d BEGIN SELECT RAISE(ABORT,'injected weighted final failure'); END`, weightedSchemaVersion))
			}
			before := shopperPriorTables(t, old)
			objects := migrationQuerySnapshot(t, old, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`)
			orderRoot := migrationQuerySnapshot(t, old, `SELECT rootpage FROM sqlite_schema WHERE name='orders'`)
			orderColumns := migrationQuerySnapshot(t, old, `PRAGMA table_info(orders)`)
			fingerprint := fingerprintTest(t, old)
			_ = old.Close()
			s := migrationTestStore(t, path, now)
			if fail {
				if err := s.migrate(); err == nil || !strings.Contains(err.Error(), "injected weighted final failure") {
					t.Fatalf("failure=%v", err)
				}
				if fingerprintTest(t, s.db) != fingerprint {
					t.Fatal("failed four-table rebuild changed schema, data, sequences, history or custom objects")
				}
				assertForeignKeysEnabled(t, s.db)
				testExec(t, s, `DROP TRIGGER fail_weighted_version`)
			}
			if err := s.migrate(); err != nil {
				t.Fatal(err)
			}
			assertShopperPriorTables(t, s.db, before)
			if got := migrationQuerySnapshot(t, s.db, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`); !reflect.DeepEqual(got, objects) {
				t.Fatalf("custom schema objects changed: %v", got)
			}
			if got := migrationQuerySnapshot(t, s.db, `SELECT rootpage FROM sqlite_schema WHERE name='orders'`); !reflect.DeepEqual(got, orderRoot) {
				t.Fatal("orders table was rebuilt")
			}
			// Later additive migrations may append columns; every original column
			// definition and its ordinal must remain byte-for-byte equivalent.
			gotColumns := migrationQuerySnapshot(t, s.db, `PRAGMA table_info(orders)`)
			if len(gotColumns) < len(orderColumns) || !reflect.DeepEqual(gotColumns[:len(orderColumns)], orderColumns) {
				t.Fatal("original order column definitions changed")
			}
			var mismatch int
			if err := s.db.QueryRow(`SELECT count(*) FROM working_order_items WHERE allocated_quantity<>quantity OR measurement_confirmed<>0`).Scan(&mismatch); err != nil || mismatch != 0 {
				t.Fatalf("allocation backfill mismatch=%d: %v", mismatch, err)
			}
			assertForeignKeysEnabled(t, s.db)
			assertWriterFenceCoverage(t, s.db)
			after := fingerprintTest(t, s.db)
			_ = s.Close()
			reopened := openPromotionMigrationStore(t, path, now)
			if fingerprintTest(t, reopened.db) != after {
				t.Fatal("restart replayed migration or changed historical data")
			}
			testExec(t, reopened, `UPDATE retained_write_view SET description='Write through the preserved view' WHERE id=1`)
			var description string
			if err := reopened.db.QueryRow(`SELECT description FROM products WHERE id=1`).Scan(&description); err != nil || description != "Write through the preserved view" {
				t.Fatalf("preserved INSTEAD OF view trigger stopped working: %q %v", description, err)
			}
			result, err := reopened.db.Exec(`INSERT INTO working_order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,allocated_quantity) VALUES(2,3,'New line after deleted ID',479,0,'CUSTOM-PEAR','each',1,1,0)`)
			if err != nil {
				t.Fatal(err)
			}
			id, err := result.LastInsertId()
			if err != nil || id <= 9000 {
				t.Fatalf("deleted working ID reused: %d %v", id, err)
			}
		})
	}
}

func TestWeightedMigrationRefusesAmbiguousLegacyGramRows(t *testing.T) {
	for _, kind := range []string{"cart", "receipt", "working"} {
		t.Run(kind, func(t *testing.T) {
			path, now := populatedWeightedV8(t)
			s := migrationTestStore(t, path, now)
			switch kind {
			case "cart":
				testExec(t, s, `UPDATE products SET sale_unit='g',price_basis=1000,quantity_step=1 WHERE id=2`)
			case "receipt":
				testExec(t, s, `UPDATE order_items SET sale_unit='g',price_basis=1000 WHERE order_id=1 AND product_id=2`)
			case "working":
				testExec(t, s, `UPDATE working_order_items SET sale_unit='g',price_basis=1000 WHERE order_id=1 AND product_id=2`)
			}
			before := fingerprintTest(t, s.db)
			if err := s.migrate(); !errors.Is(err, ErrSchemaIncompatible) || !strings.Contains(err.Error(), "pre-weighted gram") {
				t.Fatalf("ambiguous %s=%v", kind, err)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("ambiguous data was converted or deleted")
			}
			assertForeignKeysEnabled(t, s.db)
		})
	}
}

func TestWeightedMigrationRollsBackFailedForeignKeyCheck(t *testing.T) {
	path, now := populatedWeightedV8(t)
	s := migrationTestStore(t, path, now)
	testExec(t, s, `PRAGMA foreign_keys=OFF; INSERT INTO product_details(product_id,body) VALUES(987654,'Orphan must not be silently retained')`)
	before := fingerprintTest(t, s.db)
	if err := s.migrate(); err == nil || !strings.Contains(err.Error(), "foreign key check") {
		t.Fatalf("foreign key migration=%v", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("foreign key failure committed partial migration")
	}
	var enabled int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("enforcement not restored: %d %v", enabled, err)
	}
	testExec(t, s, `DELETE FROM product_details WHERE product_id=987654`)
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	assertForeignKeysEnabled(t, s.db)
}

func assertWriterFenceCoverage(t *testing.T, db *sql.DB) {
	t.Helper()
	tables := migrationQuerySnapshot(t, db, `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	for _, table := range tables {
		for _, op := range []string{"insert", "update", "delete"} {
			name := fmt.Sprintf("app_writer_v%d_%s_%s", weightedSchemaVersion, table[0], op)
			var statement string
			if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=? AND tbl_name=?`, name, table[0]).Scan(&statement); err != nil {
				t.Fatalf("missing %s fence on %s: %v", op, table[0], err)
			}
			if !strings.Contains(statement, "app_schema_version()") || !strings.Contains(statement, fmt.Sprintf("<%d", weightedSchemaVersion)) {
				t.Fatalf("bad fence: %s", statement)
			}
		}
	}
}

func TestWeightedSchemaBoundsAndIndependentMeasurement(t *testing.T) {
	s := newTestStore(t)
	session := testSession(t, s, "")
	testExec(t, s, `INSERT INTO products(id,name,description,category,barcode,icon,price,stock,category_id,sku,sale_unit,price_basis,quantity_step) VALUES(1001,'Weighed test','Test','Produce','WEIGHT-TEST','apple',349,1000000,1,'WEIGHT-TEST','g',1000,50)`)
	basket := testBasket(t, s, session.ID)
	testExec(t, s, `INSERT INTO cart VALUES(?,1001,100000,100000)`, basket.ID)
	for _, query := range []string{
		`UPDATE products SET stock=1000001 WHERE id=1001`,
		`UPDATE products SET stock=10001 WHERE id=1`,
		`UPDATE cart SET quantity=100001 WHERE product_id=1001`,
		`UPDATE cart SET quantity=99999,reserved=0 WHERE product_id=1001`,
		`UPDATE products SET quantity_step=999 WHERE id=1001`,
		`INSERT INTO cart(basket_id,product_id,quantity) SELECT id,1,100 FROM baskets LIMIT 1`,
	} {
		before := fingerprintTest(t, s.db)
		if _, err := s.db.Exec(query); err == nil {
			t.Fatalf("invalid quantity accepted: %s", query)
		}
		if fingerprintTest(t, s.db) != before {
			t.Fatalf("failed quantity write changed database: %s", query)
		}
	}
	testExec(t, s, `INSERT INTO orders(reference,session_id,checkout_key,total) VALUES('WEIGHT-SNAPSHOT',?,'weight-snapshot-key',349)`, session.ID)
	var oid int64
	if err := s.db.QueryRow(`SELECT id FROM orders WHERE reference='WEIGHT-SNAPSHOT'`).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `INSERT INTO order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,subtotal) VALUES(?,1001,'Weight snapshot',349,1000,'WEIGHT-TEST','g',1000,50,349)`, oid)
	testExec(t, s, `INSERT INTO working_order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,allocated_quantity) VALUES(?,1001,'Weight snapshot',349,1000,'WEIGHT-TEST','g',1000,50,1000)`, oid)
	testExec(t, s, `UPDATE working_order_items SET allocated_quantity=1251,picked_quantity=1251,measurement_confirmed=1 WHERE order_id=?`, oid)
	testExec(t, s, `UPDATE working_order_items SET allocated_quantity=0,picked_quantity=0,measurement_confirmed=1 WHERE order_id=?`, oid)
	for _, set := range []string{"allocated_quantity=100001", "picked_quantity=1", "measurement_confirmed=2", "unavailable_quantity=1", "cancelled_quantity=1"} {
		if _, err := s.db.Exec(`UPDATE working_order_items SET `+set+` WHERE order_id=?`, oid); err == nil {
			t.Fatalf("invalid working measurement accepted: %s", set)
		}
	}
	var target, price, basis int64
	if err := s.db.QueryRow(`SELECT quantity,price,price_basis FROM order_items WHERE order_id=?`, oid).Scan(&target, &price, &basis); err != nil || target != 1000 || price != 349 || basis != 1000 {
		t.Fatalf("receipt changed: %d %d %d %v", target, price, basis, err)
	}
	for _, query := range []string{`UPDATE order_items SET quantity=100001 WHERE order_id=?`, `INSERT INTO order_items(order_id,product_id,name,price,quantity) VALUES(?,1,'Too many each',349,100)`} {
		if _, err := s.db.Exec(query, oid); err == nil {
			t.Fatalf("receipt limit accepted: %s", query)
		}
	}
	assertForeignKeysEnabled(t, s.db)
}

const legacyVersionDriver = "shopper_test_compiled_v8"
const legacyImageVersionDriver = "shopper_test_compiled_v9"

func init() {
	sql.Register(legacyVersionDriver, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		return c.RegisterFunc("app_schema_version", func() int64 { return 8 }, true)
	}})
	sql.Register(legacyImageVersionDriver, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		return c.RegisterFunc("app_schema_version", func() int64 { return 9 }, true)
	}})
}

func TestWriterFencesRejectOldAndUnregisteredConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fences.db")
	s := openTestStore(t, path)
	session := testSession(t, s, "")
	assertWriterFenceCoverage(t, s.db)
	for _, driverName := range []string{"sqlite3", legacyVersionDriver, legacyImageVersionDriver} {
		t.Run(driverName, func(t *testing.T) {
			db, err := sql.Open(driverName, path+"?_foreign_keys=on&_txlock=immediate")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			queries := []string{
				`INSERT INTO sessions(id,csrf,checkout_key,expires) VALUES('old-writer','c','k',9999999999)`,
				`UPDATE sessions SET revision=revision+1 WHERE id='` + session.ID + `'`,
				`DELETE FROM sessions WHERE id='` + session.ID + `'`,
				`DELETE FROM products`,
				`INSERT INTO sessions(id,csrf,checkout_key,expires) VALUES('` + session.ID + `','c','k',9999999999) ON CONFLICT(id) DO UPDATE SET revision=revision+1`,
				`REPLACE INTO sessions(id,csrf,checkout_key,expires) VALUES('` + session.ID + `','c','k',9999999999)`,
				fmt.Sprintf(`INSERT INTO schema_version VALUES(%d)`, latestSchemaVersion+1),
			}
			for _, query := range queries {
				before := fingerprintTest(t, s.db)
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				_, err = tx.Exec(query)
				_ = tx.Rollback()
				if err == nil || (!strings.Contains(err.Error(), "app_schema_version") && !strings.Contains(err.Error(), "requires writer")) {
					t.Fatalf("old writer accepted or wrong refusal: %s: %v", query, err)
				}
				if fingerprintTest(t, s.db) != before {
					t.Fatalf("old write changed data: %s", query)
				}
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM products`).Scan(&n); err != nil || n != 72 {
				t.Fatalf("read-only legacy inspection failed: %d %v", n, err)
			}
		})
	}
	// The hook must run on replacement physical connections, not just startup.
	s.db.SetMaxIdleConns(0)
	for range 3 {
		if _, err := s.db.Exec(`UPDATE sessions SET revision=revision+1 WHERE id=?`, session.ID); err != nil {
			t.Fatal(err)
		}
	}
	assertForeignKeysEnabled(t, s.db)
}

func TestWeightedMigrationRejectsPreparedOldWriterAcrossProcesses(t *testing.T) {
	for _, driverName := range []string{"sqlite3", legacyVersionDriver, legacyImageVersionDriver} {
		t.Run(driverName, func(t *testing.T) {
			path, now := populatedWeightedV8(t)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, executable, "-test.run=^TestWeightedLegacyProcessHelper$")
			child.Env = append(os.Environ(), "SHOP_WEIGHTED_LEGACY_PATH="+path, "SHOP_WEIGHTED_LEGACY_DRIVER="+driverName)
			var stderr bytes.Buffer
			child.Stderr = &stderr
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Process.Kill() })
			reader := bufio.NewReader(stdout)
			if line, err := reader.ReadString('\n'); err != nil || line != "prepared\n" {
				t.Fatalf("old preparation: %q %v %s", line, err, stderr.String())
			}
			s := migrationTestStore(t, path, now)
			if err = s.migrate(); err != nil {
				t.Fatal(err)
			}
			before := fingerprintTest(t, s.db)
			_, err = stdin.Write([]byte("attempt\n"))
			if err != nil {
				t.Fatal(err)
			}
			_ = stdin.Close()
			if line, err := reader.ReadString('\n'); err != nil || line != "refused\n" {
				t.Fatalf("old refusal: %q %v %s", line, err, stderr.String())
			}
			if err = child.Wait(); err != nil {
				t.Fatalf("old process: %v %s", err, stderr.String())
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("prepared old writer changed migrated database")
			}
		})
	}
}

func TestWeightedLegacyProcessHelper(t *testing.T) {
	path := os.Getenv("SHOP_WEIGHTED_LEGACY_PATH")
	if path == "" {
		t.Skip("separate process helper")
	}
	db, err := sql.Open(os.Getenv("SHOP_WEIGHTED_LEGACY_DRIVER"), path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	statement, err := db.Prepare(`UPDATE products SET stock=stock-1 WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	_, _ = os.Stdout.WriteString("prepared\n")
	if _, err = bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err = statement.Exec(); err == nil || (!strings.Contains(err.Error(), "app_schema_version") && !strings.Contains(err.Error(), "requires writer")) {
		t.Fatalf("prepared legacy statement=%v", err)
	}
	_, _ = os.Stdout.WriteString("refused\n")
}

func TestWeightedMigrationPreservesTrustedSchemaSecurityChoice(t *testing.T) {
	path, now := populatedWeightedV8(t)
	s := migrationTestStore(t, path, now)
	testExec(t, s, `PRAGMA trusted_schema=OFF`)
	before := fingerprintTest(t, s.db)
	if err := s.migrate(); !errors.Is(err, ErrSchemaIncompatible) || !strings.Contains(err.Error(), "trusted_schema") {
		t.Fatalf("trusted_schema off=%v", err)
	}
	var trusted int
	if err := s.db.QueryRow(`PRAGMA trusted_schema`).Scan(&trusted); err != nil || trusted != 0 {
		t.Fatalf("security setting changed: %d %v", trusted, err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("trusted_schema refusal changed schema or data")
	}
}

func TestWeightedMigrationSerializesBehindLegacyWriter(t *testing.T) {
	path, now := populatedWeightedV8(t)
	old, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	old.SetMaxOpenConns(1)
	tx, err := old.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE products SET description='Last compatible old write' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	s := migrationTestStore(t, path, now)
	result := make(chan error, 1)
	go func() { result <- s.migrate() }()
	select {
	case err := <-result:
		t.Fatalf("migration passed old immediate writer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("migration did not resume after old writer")
	}
	var description string
	if err = s.db.QueryRow(`SELECT description FROM products WHERE id=1`).Scan(&description); err != nil || description != "Last compatible old write" {
		t.Fatalf("old committed write lost: %q %v", description, err)
	}
	before := fingerprintTest(t, s.db)
	if _, err = old.Exec(`UPDATE products SET description='Late incompatible old write' WHERE id=1`); err == nil {
		t.Fatal("old write accepted after migration")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("late old write changed data")
	}
	assertForeignKeysEnabled(t, s.db)
}

func TestWeightedMigrationRollbackKeepsPreparedLegacyWriterValid(t *testing.T) {
	path, now := populatedWeightedV8(t)
	old, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	old.SetMaxOpenConns(1)
	statement, err := old.Prepare(`UPDATE products SET description='Valid old write after rollback' WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	s := migrationTestStore(t, path, now)
	testExec(t, s, fmt.Sprintf(`CREATE TRIGGER fail_weighted_retry BEFORE INSERT ON schema_version WHEN NEW.version=%d BEGIN SELECT RAISE(ABORT,'injected retry failure'); END`, weightedSchemaVersion))
	if err = s.migrate(); err == nil {
		t.Fatal("migration unexpectedly committed")
	}
	if _, err = statement.Exec(); err != nil {
		t.Fatalf("rolled-back rebuild broke existing legacy statement: %v", err)
	}
	testExec(t, s, `DROP TRIGGER fail_weighted_retry`)
	if err = s.migrate(); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	if _, err = statement.Exec(); err == nil {
		t.Fatal("same legacy statement accepted after committed retry")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("prepared legacy writer changed committed retry")
	}
	assertForeignKeysEnabled(t, s.db)
}

func TestWriterFencesSurviveDemoResetAndBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reset-fences.db")
	s := openTestStore(t, path)
	_ = testSession(t, s, "")
	result := resetTest(t, s)
	assertWriterFenceCoverage(t, s.db)
	backup, err := openBackupReadOnly(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	assertWriterFenceCoverage(t, backup)
	for _, target := range []string{path, result.BackupPath} {
		db, err := sql.Open("sqlite3", target)
		if err != nil {
			t.Fatal(err)
		}
		before := fingerprintTest(t, db)
		_, err = db.Exec(`UPDATE products SET price=price+1 WHERE id=1`)
		if err == nil || !strings.Contains(err.Error(), "app_schema_version") {
			t.Fatalf("unregistered reset/backup write=%v", err)
		}
		if fingerprintTest(t, db) != before {
			t.Fatal("unregistered reset/backup write changed database")
		}
		_ = db.Close()
	}
}

// Frozen image-only v9 predecessor. Keep this independent of the live image
// migration so v9-to-v10 preservation cannot silently start at the new schema.
const legacyImageV9ForWeighted = `
CREATE TABLE product_images (
 hash TEXT PRIMARY KEY CHECK(length(hash)=64),
 normalization_version TEXT NOT NULL,
 mime TEXT NOT NULL CHECK(mime='image/jpeg'),
 width INTEGER NOT NULL CHECK(width BETWEEN 1 AND 1024),
 height INTEGER NOT NULL CHECK(height BETWEEN 1 AND 1024),
 thumbnail_width INTEGER NOT NULL CHECK(thumbnail_width BETWEEN 1 AND 256),
 thumbnail_height INTEGER NOT NULL CHECK(thumbnail_height BETWEEN 1 AND 256),
 master BLOB NOT NULL CHECK(length(master) BETWEEN 1 AND 524288),
 thumbnail BLOB NOT NULL CHECK(length(thumbnail) BETWEEN 1 AND 98304),
 created TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
ALTER TABLE products ADD COLUMN image_hash TEXT REFERENCES product_images(hash);
CREATE TABLE product_image_commands (command_key TEXT PRIMARY KEY, command_hash TEXT NOT NULL);
CREATE TABLE product_image_limits (
 id INTEGER PRIMARY KEY CHECK(id=1),
 minute_bucket INTEGER NOT NULL DEFAULT 0,
 minute_count INTEGER NOT NULL DEFAULT 0 CHECK(minute_count>=0),
 day_bucket INTEGER NOT NULL DEFAULT 0,
 day_count INTEGER NOT NULL DEFAULT 0 CHECK(day_count>=0)
);
INSERT INTO product_image_limits(id) VALUES(1);
INSERT INTO schema_version VALUES(9);
`

func TestWeightedMigrationPreservesImageV9AssetsAndQuota(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%t", failure), func(t *testing.T) {
			path, now := populatedWeightedV8(t)
			db := migrationRawDB(t, path)
			migrationExec(t, db, legacyImageV9ForWeighted)
			first, _ := imageFixture(t, 120)
			second, _ := imageFixture(t, 80)
			// Actual normalized JPEG variants, attached to both active and archived
			// products. Preserve immutable bytes, hashes and normalization metadata.
			for _, entry := range []struct {
				productID int64
				asset     productimage.Result
			}{{1, first}, {3, second}} {
				asset := entry.asset
				migrationExec(t, db, `INSERT INTO product_images(hash,normalization_version,mime,width,height,thumbnail_width,thumbnail_height,master,thumbnail,created) VALUES(?,?,'image/jpeg',?,?,?,?,?,?,'2026-10-01T11:22:33Z')`, asset.SHA256, asset.Version, asset.Master.Width, asset.Master.Height, asset.Thumbnail.Width, asset.Thumbnail.Height, asset.Master.Data, asset.Thumbnail.Data)
				migrationExec(t, db, `UPDATE products SET image_hash=? WHERE id=?`, asset.SHA256, entry.productID)
			}
			migrationExec(t, db, `INSERT INTO product_image_commands VALUES('saved-image-command-one','saved-image-hash-one'),('saved-image-command-two','saved-image-hash-two')`)
			migrationExec(t, db, `UPDATE product_image_limits SET minute_bucket=29844000,minute_count=3,day_bucket=20720,day_count=27 WHERE id=1`)
			if failure {
				migrationExec(t, db, fmt.Sprintf(`CREATE TRIGGER fail_image_weighted BEFORE INSERT ON schema_version WHEN NEW.version=%d BEGIN SELECT RAISE(ABORT,'image to weighted interrupted'); END`, weightedSchemaVersion))
			}
			before := shopperPriorTables(t, db)
			original := fingerprintTest(t, db)
			_ = db.Close()
			s := migrationTestStore(t, path, now)
			if failure {
				if err := s.migrate(); err == nil || !strings.Contains(err.Error(), "image to weighted interrupted") {
					t.Fatalf("image-preservation rollback=%v", err)
				}
				if fingerprintTest(t, s.db) != original {
					t.Fatal("failed weighted rebuild changed image bytes, associations, quota, command history or predecessor schema")
				}
				assertForeignKeysEnabled(t, s.db)
				testExec(t, s, `DROP TRIGGER fail_image_weighted`)
			}
			if err := s.migrate(); err != nil {
				t.Fatal(err)
			}
			assertShopperPriorTables(t, s.db, before)
			assertWriterFenceCoverage(t, s.db)
			assertForeignKeysEnabled(t, s.db)
			for _, asset := range []productimage.Result{first, second} {
				got, err := imageAt(s.db, asset.SHA256)
				// SourceFormat/Width/Height belong only to the upload preview;
				// the image schema deliberately does not persist source metadata.
				if err != nil || got.SHA256 != asset.SHA256 || got.Version != asset.Version || !reflect.DeepEqual(got.Master, asset.Master) || !reflect.DeepEqual(got.Thumbnail, asset.Thumbnail) {
					t.Fatalf("normalized image changed: hash=%s error=%v", asset.SHA256, err)
				}
				if err = productimage.ValidateResult(got); err != nil {
					t.Fatalf("preserved normalized image failed integrity validation: %v", err)
				}
			}
			var references int
			if err := s.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_list('products') WHERE "table"='product_images' AND "from"='image_hash' AND "to"='hash'`).Scan(&references); err != nil || references != 1 {
				t.Fatalf("product image FK missing after rebuild: %d %v", references, err)
			}
			stable := fingerprintTest(t, s.db)
			if _, err := s.db.Exec(`UPDATE products SET image_hash=? WHERE id=1`, strings.Repeat("f", 64)); err == nil {
				t.Fatal("rebuilt product accepted an image association with no asset")
			}
			if fingerprintTest(t, s.db) != stable {
				t.Fatal("failed image FK update changed preserved state")
			}
			_ = s.Close()
			reopened := openPromotionMigrationStore(t, path, now)
			if fingerprintTest(t, reopened.db) != stable {
				t.Fatal("restart changed retained image bytes, hashes, associations, quotas or command history")
			}
			assertShopperPriorTables(t, reopened.db, before)
			assertForeignKeysEnabled(t, reopened.db)
		})
	}
}
