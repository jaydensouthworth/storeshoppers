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

// Keep this fixture independent of schema.sql so future schema edits cannot
// silently turn the upgrade test into a fresh-database test.
const legacySchemaV1 = `
CREATE TABLE products (
 id INTEGER PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, category TEXT NOT NULL,
 barcode TEXT NOT NULL UNIQUE, icon TEXT NOT NULL, price INTEGER NOT NULL CHECK(price BETWEEN 1 AND 1000000),
 stock INTEGER NOT NULL CHECK(stock BETWEEN 0 AND 10000), version INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE sessions (
 id TEXT PRIMARY KEY, csrf TEXT NOT NULL, checkout_key TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1, manager_until INTEGER NOT NULL DEFAULT 0, expires INTEGER NOT NULL
);
CREATE TABLE cart (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 product_id INTEGER NOT NULL REFERENCES products(id), quantity INTEGER NOT NULL CHECK(quantity BETWEEN 1 AND 99),
 PRIMARY KEY(session_id,product_id)
);
CREATE TABLE orders (
 id INTEGER PRIMARY KEY AUTOINCREMENT, reference TEXT NOT NULL UNIQUE,
 session_id TEXT NOT NULL, checkout_key TEXT NOT NULL UNIQUE, total INTEGER NOT NULL CHECK(total > 0),
 status TEXT NOT NULL DEFAULT 'Placed' CHECK(status IN ('Placed','Picking','Ready','Completed')),
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now'))
);
CREATE INDEX orders_session ON orders(session_id);
CREATE TABLE order_items (
 order_id INTEGER NOT NULL REFERENCES orders(id), product_id INTEGER NOT NULL REFERENCES products(id),
 name TEXT NOT NULL, price INTEGER NOT NULL CHECK(price > 0), quantity INTEGER NOT NULL CHECK(quantity BETWEEN 1 AND 99),
 PRIMARY KEY(order_id,product_id)
);
CREATE TABLE adjustments (
 id INTEGER PRIMARY KEY, product_id INTEGER NOT NULL REFERENCES products(id), delta INTEGER NOT NULL,
 reason TEXT NOT NULL, created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now'))
);
CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
INSERT INTO schema_version VALUES(1);`

func migrationRawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func migrationExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("migration fixture SQL: %v", err)
	}
}

func populatedV1(t *testing.T) (string, Session) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db := migrationRawDB(t, path)
	migrationExec(t, db, legacySchemaV1)
	migrationExec(t, db, `INSERT INTO products VALUES
 (1,'Legacy custom apples','Saved description','Produce','000000000101','apple',723,11,17),
 (2,'Legacy custom spinach','Saved greens','Produce','000000000102','leaf',381,7,9)`)
	owner := Session{ID: strings.Repeat("a", 64), CSRF: strings.Repeat("b", 64), CheckoutKey: strings.Repeat("c", 64), Revision: 12, ManagerUntil: time.Now().Add(20 * time.Minute).Unix()}
	migrationExec(t, db, `INSERT INTO sessions VALUES(?,?,?,?,?,?)`, owner.ID, owner.CSRF, owner.CheckoutKey, owner.Revision, owner.ManagerUntil, time.Now().Add(24*time.Hour).Unix())
	migrationExec(t, db, `INSERT INTO cart VALUES(?,?,?)`, owner.ID, 2, 3)
	for i, status := range []string{"Placed", "Picking", "Ready", "Completed"} {
		id := i + 1
		migrationExec(t, db, `INSERT INTO orders(id,reference,session_id,checkout_key,total,status,created) VALUES(?,?,?,?,?,?,?)`, id, fmt.Sprintf("LEGACY-%d", id), owner.ID, fmt.Sprintf("old-key-%d", id), 847, status, "2025-03-14 12:00 UTC")
		migrationExec(t, db, `INSERT INTO order_items VALUES(?,?,?,?,?),(?,?,?,?,?)`, id, 1, "Original receipt apples", 299, 2, id, 2, "Original receipt spinach", 249, 1)
	}
	migrationExec(t, db, `INSERT INTO adjustments VALUES(1,1,-2,'Legacy stock count','2025-03-13 09:00 UTC')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, owner
}

func assertLegacyPreserved(t *testing.T, s *Store, owner Session) {
	t.Helper()
	if got := testSession(t, s, owner.ID); got != owner {
		t.Errorf("migration changed session or grant: got %+v, want %+v", got, owner)
	}
	for _, want := range []Product{
		{1, "Legacy custom apples", "Saved description", "Produce", "000000000101", "apple", 723, 11, 17},
		{2, "Legacy custom spinach", "Saved greens", "Produce", "000000000102", "leaf", 381, 7, 9},
	} {
		if got := testProduct(t, s, want.ID); got != want {
			t.Errorf("migration or seed reset product: got %+v, want %+v", got, want)
		}
	}
	if b := testBasket(t, s, owner.ID); b.Count != 3 || len(b.Lines) != 1 || b.Lines[0].Product.ID != 2 || b.Total != 1143 {
		t.Errorf("migration changed basket: %+v", b)
	}
	for table, want := range map[string]int{"products": 8, "sessions": 1, "cart": 1, "orders": 4, "order_items": 8, "adjustments": 1, "schema_version": 2} {
		if got := testCount(t, s, table); got != want {
			t.Errorf("%s count = %d, want %d", table, got, want)
		}
	}
	logs, err := s.Adjustments()
	if err != nil || len(logs) != 1 || logs[0].Delta != -2 || logs[0].Reason != "Legacy stock count" || logs[0].Created != "2025-03-13 09:00 UTC" {
		t.Errorf("migration changed adjustment history: %+v, %v", logs, err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 2 {
		t.Errorf("schema version = %d, %v, want 2", version, err)
	}
}

func TestMigrationV1PreservesHistoryAndBackfillsPicking(t *testing.T) {
	path, owner := populatedV1(t)
	s := openTestStore(t, path)
	assertLegacyPreserved(t, s, owner)
	for i, status := range []string{"Placed", "Picking", "Ready", "Completed"} {
		o := testOrder(t, s, int64(i+1), owner.ID)
		if o.Status != status || o.Reference != fmt.Sprintf("LEGACY-%d", i+1) || o.Created != "2025-03-14 12:00 UTC" || o.Total != 847 || len(o.Items) != 2 || o.RequiredCount != 3 {
			t.Fatalf("migration changed order snapshot: %+v", o)
		}
		wantPicked := int64(0)
		if status == "Ready" || status == "Completed" {
			wantPicked = 3
		}
		if o.PickedCount != wantPicked || o.AllPicked != (wantPicked == 3) {
			t.Errorf("%s progress = %d, all=%t, want %d", status, o.PickedCount, o.AllPicked, wantPicked)
		}
		for j, item := range o.Items {
			want := []OrderItem{
				{Name: "Original receipt apples", ProductID: 1, Price: 299, Quantity: 2, Subtotal: 598, PickVersion: 1},
				{Name: "Original receipt spinach", ProductID: 2, Price: 249, Quantity: 1, Subtotal: 249, PickVersion: 1},
			}[j]
			if wantPicked > 0 {
				want.Picked = want.Quantity
			}
			if item != want {
				t.Errorf("%s item = %+v, want %+v", status, item, want)
			}
		}
	}
	// Existing progress must not be reset or historically backfilled a second time.
	if err := s.RecordPicked(2, 1, 1, 1, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	before := testOrder(t, s, 2, owner.ID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	assertLegacyPreserved(t, reopened, owner)
	if after := testOrder(t, reopened, 2, owner.ID); !reflect.DeepEqual(before, after) {
		t.Errorf("reopen reset saved picking progress: before %+v, after %+v", before, after)
	}
}

func TestMigrationFailureRollsBackSchemaAndAllowsRetry(t *testing.T) {
	path, owner := populatedV1(t)
	db := migrationRawDB(t, path)
	migrationExec(t, db, `CREATE TRIGGER fail_migration_version BEFORE INSERT ON schema_version WHEN NEW.version=2 BEGIN SELECT RAISE(ABORT,'injected migration failure'); END`)
	if s, err := Open(path); err == nil {
		_ = s.Close()
		t.Fatal("migration succeeded despite injected final-statement failure")
	}
	for _, column := range []string{"picked_quantity", "pick_version"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('order_items') WHERE name=?`, column).Scan(&count); err != nil || count != 0 {
			t.Errorf("failed migration left column %s: count %d, error %v", column, count, err)
		}
	}
	var version, orders, items, products int
	if err := db.QueryRow(`SELECT MAX(version),(SELECT COUNT(*) FROM orders),(SELECT COUNT(*) FROM order_items),(SELECT COUNT(*) FROM products) FROM schema_version`).Scan(&version, &orders, &items, &products); err != nil {
		t.Fatal(err)
	}
	if version != 1 || orders != 4 || items != 8 || products != 2 {
		t.Errorf("failed migration changed legacy data or seeded prematurely: version=%d orders=%d items=%d products=%d", version, orders, items, products)
	}
	migrationExec(t, db, `DROP TRIGGER fail_migration_version`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	recovered := openTestStore(t, path)
	assertLegacyPreserved(t, recovered, owner)
	if o := testOrder(t, recovered, 3, owner.ID); !o.AllPicked || o.Status != "Ready" {
		t.Errorf("retried migration did not backfill ready order: %+v", o)
	}
}

func TestMigrationRefusesFutureSchemaWithoutChangingData(t *testing.T) {
	path, _ := populatedV1(t)
	db := migrationRawDB(t, path)
	migrationExec(t, db, `INSERT INTO schema_version VALUES(999)`)
	if s, err := Open(path); err == nil {
		_ = s.Close()
		t.Fatal("Open accepted an unsupported future schema")
	} else if !strings.Contains(err.Error(), "999") || !strings.Contains(err.Error(), "newer") {
		t.Errorf("future-version error should identify unsupported version: %v", err)
	}
	var version, products, orders, columns int
	if err := db.QueryRow(`SELECT MAX(version),(SELECT COUNT(*) FROM products),(SELECT COUNT(*) FROM orders),(SELECT COUNT(*) FROM pragma_table_info('order_items') WHERE name='picked_quantity') FROM schema_version`).Scan(&version, &products, &orders, &columns); err != nil {
		t.Fatal(err)
	}
	if version != 999 || products != 2 || orders != 4 || columns != 0 {
		t.Errorf("future-schema refusal changed database: version=%d products=%d orders=%d new-columns=%d", version, products, orders, columns)
	}
}

func TestMigrationMaintainsDatabaseIntegrity(t *testing.T) {
	path, _ := populatedV1(t)
	s := openTestStore(t, path)
	var result string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil || result != "ok" {
		t.Fatalf("integrity_check = %q, %v", result, err)
	}
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left a foreign key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationPickingConstraintsRemainEnforced(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 2)
	id := testCheckout(t, s, owner.ID)
	before := testOrder(t, s, id, owner.ID)
	for _, update := range []string{"picked_quantity=-1", "picked_quantity=3", "pick_version=0", "picked_quantity=NULL", "pick_version=NULL"} {
		if _, err := s.db.Exec(`UPDATE order_items SET `+update+` WHERE order_id=?`, id); err == nil {
			t.Errorf("schema accepted invalid %s", update)
		}
	}
	if after := testOrder(t, s, id, owner.ID); !reflect.DeepEqual(before, after) {
		t.Error("rejected SQL updates changed the receipt")
	}
}
