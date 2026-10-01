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

// This is a saved v2 shape, not the application migration. Keeping the fixture
// independent catches both accidental changes to v2 and v3 replaying backfills.
const legacyPickingV2 = `
ALTER TABLE order_items ADD COLUMN picked_quantity INTEGER NOT NULL DEFAULT 0
 CHECK(picked_quantity >= 0 AND picked_quantity <= quantity);
ALTER TABLE order_items ADD COLUMN pick_version INTEGER NOT NULL DEFAULT 1 CHECK(pick_version > 0);
UPDATE order_items SET picked_quantity=quantity
 WHERE order_id IN (SELECT id FROM orders WHERE status IN ('Ready','Completed'));
INSERT INTO schema_version VALUES(2);`

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
		{ID: 1, Name: "Legacy custom apples", Description: "Saved description", Category: "Produce", Barcode: "000000000101", Icon: "apple", Price: 723, Stock: 11, Version: 17},
		{ID: 2, Name: "Legacy custom spinach", Description: "Saved greens", Category: "Produce", Barcode: "000000000102", Icon: "leaf", Price: 381, Stock: 7, Version: 9},
	} {
		got := testProduct(t, s, want.ID)
		legacy := Product{ID: got.ID, Name: got.Name, Description: got.Description, Category: got.Category, Barcode: got.Barcode, Icon: got.Icon, Price: got.Price, Stock: got.Stock, Version: got.Version}
		if legacy != want {
			t.Errorf("migration or seed reset legacy product fields: got %+v, want %+v", legacy, want)
		}
		if got.SKU != fmt.Sprintf("SHOPDEMO-%06d", got.ID) || got.Archived || got.CatalogVersion != 1 || got.PriceVersion != 1 || got.SaleUnit != "each" || got.PriceBasis != 1 || got.QuantityStep != 1 {
			t.Errorf("unsafe or missing catalog metadata backfill: %+v", got)
		}
		var category string
		if err := s.db.QueryRow(`SELECT name FROM categories WHERE id=? AND archived=0`, got.CategoryID).Scan(&category); err != nil || category != want.Category {
			t.Errorf("product %d category relationship = %q, %v, want %q", got.ID, category, err, want.Category)
		}
		if got.TypeID != 0 {
			var productType string
			if err := s.db.QueryRow(`SELECT name FROM product_types WHERE id=? AND archived=0`, got.TypeID).Scan(&productType); err != nil || productType != got.ProductType {
				t.Errorf("product %d has invalid type relationship: %q, %v", got.ID, productType, err)
			}
		} else if got.ProductType != "" {
			t.Errorf("product %d has a type name without a type identity: %+v", got.ID, got)
		}
		var raw, normalized, symbology string
		var archived bool
		if err := s.db.QueryRow(`SELECT raw_value,normalized_value,archived FROM product_codes WHERE product_id=? AND scheme='legacy_placeholder'`, got.ID).Scan(&raw, &normalized, &archived); err != nil || raw != want.Barcode || normalized != want.Barcode || archived {
			t.Errorf("product %d legacy identifier changed: raw=%q normalized=%q archived=%t error=%v", got.ID, raw, normalized, archived, err)
		}
		if err := s.db.QueryRow(`SELECT raw_value,normalized_value,symbology,archived FROM product_codes WHERE product_id=? AND scheme='demo_local'`, got.ID).Scan(&raw, &normalized, &symbology, &archived); err != nil || raw != got.SKU || normalized != got.SKU || symbology != "Code128" || archived {
			t.Errorf("product %d local identifier missing or unsafe: raw=%q normalized=%q symbology=%q archived=%t error=%v", got.ID, raw, normalized, symbology, archived, err)
		}
	}
	if b := testBasket(t, s, owner.ID); b.Count != 3 || len(b.Lines) != 1 || b.Lines[0].Product.ID != 2 || b.Total != 1143 {
		t.Errorf("migration changed basket: %+v", b)
	}
	for table, want := range map[string]int{"products": 2, "product_codes": 4, "sessions": 1, "cart": 1, "orders": 4, "order_items": 8, "adjustments": 1, "schema_version": latestSchemaVersion} {
		if got := testCount(t, s, table); got != want {
			t.Errorf("%s count = %d, want %d", table, got, want)
		}
	}
	logs, err := s.Adjustments()
	if err != nil || len(logs) != 1 || logs[0].Delta != -2 || logs[0].Reason != "Legacy stock count" || logs[0].Created != "2025-03-13 09:00 UTC" || logs[0].SaleUnit != "each" {
		t.Errorf("migration changed adjustment history: %+v, %v", logs, err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != latestSchemaVersion {
		t.Errorf("schema version = %d, %v, want latest schema", version, err)
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
				{Name: "Original receipt apples", ProductID: 1, Price: 299, Quantity: 2, Subtotal: 598, PickVersion: 1, SKU: "SHOPDEMO-000001", SaleUnit: "each", PriceBasis: 1, QuantityStep: 1},
				{Name: "Original receipt spinach", ProductID: 2, Price: 249, Quantity: 1, Subtotal: 249, PickVersion: 1, SKU: "SHOPDEMO-000002", SaleUnit: "each", PriceBasis: 1, QuantityStep: 1},
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

func populatedV2(t *testing.T) (string, Session) {
	t.Helper()
	path, owner := populatedV1(t)
	db := migrationRawDB(t, path)
	migrationExec(t, db, legacyPickingV2)
	migrationExec(t, db, `UPDATE order_items SET picked_quantity=1,pick_version=7 WHERE order_id=2 AND product_id=1`)
	migrationExec(t, db, `UPDATE order_items SET pick_version=4 WHERE order_id=2 AND product_id=2`)
	migrationExec(t, db, `UPDATE order_items SET pick_version=9 WHERE order_id=4`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, owner
}

func migrationQuerySnapshot(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("migration snapshot: %v", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		for i, value := range values {
			if bytes, ok := value.([]byte); ok {
				values[i] = string(bytes)
			}
		}
		snapshot = append(snapshot, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func migrationLegacyData(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	queries := map[string]string{
		"products":    `SELECT id,name,description,category,barcode,icon,price,stock,version FROM products ORDER BY id`,
		"sessions":    `SELECT * FROM sessions ORDER BY id`,
		"cart":        `SELECT * FROM cart ORDER BY session_id,product_id`,
		"orders":      `SELECT id,reference,session_id,checkout_key,total,status,created FROM orders ORDER BY id`,
		"order_items": `SELECT order_id,product_id,name,price,quantity FROM order_items ORDER BY order_id,product_id`,
		"adjustments": `SELECT id,product_id,delta,reason,created FROM adjustments ORDER BY id`,
	}
	var hasBaskets int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='baskets'`).Scan(&hasBaskets); err != nil {
		t.Fatal(err)
	}
	if hasBaskets != 0 {
		queries["cart"] = `SELECT b.owner_session_id,c.product_id,c.quantity FROM cart c JOIN baskets b ON b.id=c.basket_id WHERE b.synthetic=0 ORDER BY b.owner_session_id,c.product_id`
	}
	var hasPicking int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('order_items') WHERE name='picked_quantity'`).Scan(&hasPicking); err != nil {
		t.Fatal(err)
	}
	if hasPicking != 0 {
		queries["order_items"] = `SELECT order_id,product_id,name,price,quantity,picked_quantity,pick_version FROM order_items ORDER BY order_id,product_id`
	}
	snapshot := make(map[string][][]any, len(queries))
	for table, query := range queries {
		snapshot[table] = migrationQuerySnapshot(t, db, query)
	}
	return snapshot
}

func TestMigrationV2PreservesPopulatedCatalogAndPicking(t *testing.T) {
	path, owner := populatedV2(t)
	raw := migrationRawDB(t, path)
	before := migrationLegacyData(t, raw)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t, path)
	assertLegacyPreserved(t, s, owner)
	if after := migrationLegacyData(t, s.db); !reflect.DeepEqual(before, after) {
		t.Fatalf("v3 changed established v2 data: before %#v, after %#v", before, after)
	}
	picking := testOrder(t, s, 2, owner.ID)
	if picking.Status != "Picking" || picking.PickedCount != 1 || picking.AllPicked || len(picking.Items) != 2 || picking.Items[0].PickVersion != 7 || picking.Items[1].PickVersion != 4 {
		t.Errorf("v3 changed saved partial picking progress: %+v", picking)
	}
	completed := testOrder(t, s, 4, owner.ID)
	if completed.Status != "Completed" || !completed.AllPicked || len(completed.Items) != 2 || completed.Items[0].PickVersion != 9 || completed.Items[1].PickVersion != 9 {
		t.Errorf("v3 changed completed receipt progress: %+v", completed)
	}
	for id := int64(1); id <= 4; id++ {
		for _, item := range testOrder(t, s, id, owner.ID).Items {
			if item.SKU != fmt.Sprintf("SHOPDEMO-%06d", item.ProductID) || item.SaleUnit != "each" || item.PriceBasis != 1 || item.QuantityStep != 1 || item.Subtotal != item.Price*item.Quantity {
				t.Errorf("v3 did not preserve integer receipt units and amounts: %+v", item)
			}
		}
	}
	catalogBeforeReopen := migrationCatalogData(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	assertLegacyPreserved(t, reopened, owner)
	if after := migrationLegacyData(t, reopened.db); !reflect.DeepEqual(before, after) {
		t.Errorf("reopening v3 changed established v2 data: before %#v, after %#v", before, after)
	}
	if after := migrationCatalogData(t, reopened.db); !reflect.DeepEqual(catalogBeforeReopen, after) {
		t.Error("reopening v3 regenerated taxonomy, local identifiers, or product metadata")
	}
}

func TestMigrationV3FailureRollsBackSchemaAndData(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("from_v%d", version), func(t *testing.T) {
			var path string
			var owner Session
			if version == 1 {
				path, owner = populatedV1(t)
			} else {
				path, owner = populatedV2(t)
			}
			db := migrationRawDB(t, path)
			migrationExec(t, db, `CREATE TRIGGER fail_catalog_migration BEFORE INSERT ON schema_version WHEN NEW.version=3 BEGIN SELECT RAISE(ABORT,'injected catalog migration failure'); END`)
			beforeData := migrationLegacyData(t, db)
			beforeSchema := migrationQuerySnapshot(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`)
			beforeVersions := migrationQuerySnapshot(t, db, `SELECT version FROM schema_version ORDER BY version`)
			if s, err := Open(path); err == nil {
				_ = s.Close()
				t.Fatal("v3 migration succeeded despite injected final-statement failure")
			} else if !strings.Contains(err.Error(), "injected catalog migration failure") {
				t.Fatalf("v3 failed before reaching injected final statement: %v", err)
			}
			if after := migrationQuerySnapshot(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`); !reflect.DeepEqual(beforeSchema, after) {
				t.Errorf("failed v3 migration left schema changes: before %#v, after %#v", beforeSchema, after)
			}
			if after := migrationLegacyData(t, db); !reflect.DeepEqual(beforeData, after) {
				t.Errorf("failed v3 migration changed legacy data: before %#v, after %#v", beforeData, after)
			}
			if after := migrationQuerySnapshot(t, db, `SELECT version FROM schema_version ORDER BY version`); !reflect.DeepEqual(beforeVersions, after) {
				t.Errorf("failed v3 migration advanced schema version: before %#v, after %#v", beforeVersions, after)
			}
			migrationExec(t, db, `DROP TRIGGER fail_catalog_migration`)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			recovered := openTestStore(t, path)
			assertLegacyPreserved(t, recovered, owner)
			if version == 2 {
				if after := migrationLegacyData(t, recovered.db); !reflect.DeepEqual(beforeData, after) {
					t.Error("retry changed previously saved v2 picking or receipt data")
				}
			} else if o := testOrder(t, recovered, 3, owner.ID); !o.AllPicked || o.Status != "Ready" {
				t.Errorf("v1 retry did not apply the v2 historical picking backfill: %+v", o)
			}
		})
	}
}

func TestMigrationNeverSeedsAnEmptyEstablishedDatabase(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "empty-established.db")
			db := migrationRawDB(t, path)
			migrationExec(t, db, legacySchemaV1)
			if version == 2 {
				migrationExec(t, db, legacyPickingV2)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				s := openTestStore(t, path)
				if count := testCount(t, s, "products"); count != 0 {
					t.Errorf("opening established empty v%d (attempt %d) seeded %d products", version, attempt+1, count)
				}
				if count := testCount(t, s, "schema_version"); count != latestSchemaVersion {
					t.Errorf("schema_version count = %d, want latest schema", count)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func migrationCatalogData(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	snapshot := make(map[string][][]any)
	for _, table := range []string{"products", "categories", "product_types", "product_codes", "catalog_sequence"} {
		snapshot[table] = migrationQuerySnapshot(t, db, `SELECT * FROM `+table+` ORDER BY id`)
	}
	return snapshot
}

func TestMigrationSeedsFreshDatabaseOnlyOnceWithoutResurrection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	s := openTestStore(t, path)
	if count := testCount(t, s, "products"); count != 70 {
		t.Fatalf("fresh catalog count = %d, want 70", count)
	}
	if count := testCount(t, s, "schema_version"); count != latestSchemaVersion {
		t.Errorf("fresh schema_version count = %d, want latest schema", count)
	}
	// Deliberately alter, archive, and delete seed rows. Opening an established
	// database must never recreate a demo row or overwrite manager changes.
	testExec(t, s, `UPDATE products SET name='Manager changed apples',price=875,stock=3,version=19,catalog_version=11,price_version=5 WHERE id=1`)
	testExec(t, s, `UPDATE products SET archived=1,catalog_version=catalog_version+1 WHERE id=2`)
	testExec(t, s, `DELETE FROM product_codes WHERE product_id=70`)
	testExec(t, s, `DELETE FROM products WHERE id=70`)
	before := migrationCatalogData(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	if after := migrationCatalogData(t, reopened.db); !reflect.DeepEqual(before, after) {
		t.Error("reopening seeded database reset edits, unarchived products, or resurrected removed rows")
	}
	if count := testCount(t, reopened, "products"); count != 69 {
		t.Errorf("reopening resurrected removed demo product: count = %d, want 69", count)
	}
	// A fully cleared current-version catalog is still established, not a new
	// database. Taxonomy and identity sequence must remain stable as well.
	testExec(t, reopened, `DELETE FROM product_codes`)
	testExec(t, reopened, `DELETE FROM products`)
	empty := migrationCatalogData(t, reopened.db)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	emptyReopened := openTestStore(t, path)
	if after := migrationCatalogData(t, emptyReopened.db); !reflect.DeepEqual(empty, after) {
		t.Error("reopening empty current-version catalog created or changed catalog data")
	}
	if count := testCount(t, emptyReopened, "products"); count != 0 {
		t.Errorf("empty established v3 catalog resurrected %d demo products", count)
	}
}

func populatedV2Demo(t *testing.T) (string, Session) {
	t.Helper()
	path, owner := populatedV2(t)
	db := migrationRawDB(t, path)
	// Recognition uses all eight original identities, not names, prices, stock,
	// or versions, which a manager may already have changed.
	for id := 3; id <= 8; id++ {
		migrationExec(t, db, `INSERT INTO products(id,name,description,category,barcode,icon,price,stock,version) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, fmt.Sprintf("Manager product %d", id), "Manager saved description", "Custom aisle", fmt.Sprintf("%012d", 100+id), "apple", 100+id, 20-id, 30+id)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, owner
}

func TestMigrationExpandsRecognizedV2DemoCatalogOnlyOnce(t *testing.T) {
	path, _ := populatedV2Demo(t)
	db := migrationRawDB(t, path)
	before := migrationLegacyData(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t, path)
	if count := testCount(t, s, "products"); count != 70 {
		t.Fatalf("recognized v2 demo catalog count = %d, want 70", count)
	}
	after := migrationLegacyData(t, s.db)
	// Additional products are expected; every original row and saved receipt,
	// cart, manager grant, and picking version must remain unchanged.
	after["products"] = migrationQuerySnapshot(t, s.db, `SELECT id,name,description,category,barcode,icon,price,stock,version FROM products WHERE id<=8 ORDER BY id`)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("expansion changed saved legacy data: before %#v, after %#v", before, after)
	}
	testExec(t, s, `DELETE FROM product_codes WHERE product_id=70`)
	testExec(t, s, `DELETE FROM products WHERE id=70`)
	testExec(t, s, `UPDATE products SET archived=1 WHERE id=9`)
	beforeReopen := migrationCatalogData(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	if after := migrationCatalogData(t, reopened.db); !reflect.DeepEqual(beforeReopen, after) {
		t.Error("reopening expanded legacy demo catalog repeated expansion or reset archives")
	}
	if count := testCount(t, reopened, "products"); count != 69 {
		t.Errorf("removed expanded demo product resurrected: count = %d, want 69", count)
	}
}

func TestMigrationDoesNotExpandUnrecognizedV2Catalog(t *testing.T) {
	path, _ := populatedV2Demo(t)
	db := migrationRawDB(t, path)
	migrationExec(t, db, `UPDATE products SET barcode='CUSTOM-008' WHERE id=8`)
	before := migrationLegacyData(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t, path)
	if count := testCount(t, s, "products"); count != 8 {
		t.Errorf("unrecognized eight-product legacy catalog was expanded to %d products", count)
	}
	if after := migrationLegacyData(t, s.db); !reflect.DeepEqual(before, after) {
		t.Error("unrecognized legacy catalog data changed during upgrade")
	}
	var legacy string
	if err := s.db.QueryRow(`SELECT raw_value FROM product_codes WHERE product_id=8 AND scheme='legacy_placeholder'`).Scan(&legacy); err != nil || legacy != "CUSTOM-008" {
		t.Errorf("custom legacy identifier was lost: %q, %v", legacy, err)
	}
}

func TestMigrationV3ExpansionFailureRollsBackSeedsAndAllowsRetry(t *testing.T) {
	path, _ := populatedV2Demo(t)
	db := migrationRawDB(t, path)
	migrationExec(t, db, `CREATE TRIGGER fail_catalog_expansion BEFORE INSERT ON schema_version WHEN NEW.version=3 BEGIN SELECT RAISE(ABORT,'injected catalog expansion failure'); END`)
	beforeData := migrationLegacyData(t, db)
	beforeSchema := migrationQuerySnapshot(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`)
	if s, err := Open(path); err == nil {
		_ = s.Close()
		t.Fatal("catalog expansion succeeded despite injected final-statement failure")
	} else if !strings.Contains(err.Error(), "injected catalog expansion failure") {
		t.Fatalf("expansion failed before reaching injected final statement: %v", err)
	}
	if after := migrationLegacyData(t, db); !reflect.DeepEqual(beforeData, after) {
		t.Error("failed catalog expansion left seeds or changed legacy data")
	}
	if after := migrationQuerySnapshot(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`); !reflect.DeepEqual(beforeSchema, after) {
		t.Error("failed catalog expansion left schema changes")
	}
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 2 {
		t.Errorf("failed catalog expansion version = %d, %v, want 2", version, err)
	}
	migrationExec(t, db, `DROP TRIGGER fail_catalog_expansion`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	recovered := openTestStore(t, path)
	if count := testCount(t, recovered, "products"); count != 70 {
		t.Fatalf("retried catalog expansion count = %d, want 70", count)
	}
	after := migrationLegacyData(t, recovered.db)
	after["products"] = migrationQuerySnapshot(t, recovered.db, `SELECT id,name,description,category,barcode,icon,price,stock,version FROM products WHERE id<=8 ORDER BY id`)
	if !reflect.DeepEqual(beforeData, after) {
		t.Error("retried catalog expansion changed legacy data")
	}
}

func TestMigrationDoesNotExpandExtendedV2DemoCatalog(t *testing.T) {
	path, owner := populatedV2Demo(t)
	db := migrationRawDB(t, path)
	// The original eight identities still exist, but a ninth bespoke product
	// makes this an established customized catalog rather than the fixed demo.
	migrationExec(t, db, `INSERT INTO products(id,name,description,category,barcode,icon,price,stock,version) VALUES(9,'Bespoke ninth product','Keep this custom row','Bespoke aisle','CUSTOM-NINTH-009','jam',1337,43,21)`)
	before := migrationLegacyData(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t, path)
	if count := testCount(t, s, "products"); count != 9 {
		t.Fatalf("extended legacy catalog expanded to %d products, want9", count)
	}
	if after := migrationLegacyData(t, s.db); !reflect.DeepEqual(before, after) {
		t.Errorf("extended catalog upgrade changed legacy rows: before %#v, after %#v", before, after)
	}
	if got := testSession(t, s, owner.ID); got != owner {
		t.Error("extended catalog upgrade changed owner session")
	}
	ninth := testProduct(t, s, 9)
	if ninth.Name != "Bespoke ninth product" || ninth.Price != 1337 || ninth.Stock != 43 || ninth.Version != 21 || ninth.Barcode != "CUSTOM-NINTH-009" {
		t.Errorf("bespoke ninth product changed: %+v", ninth)
	}
	if count := testCount(t, s, "product_codes"); count != 18 {
		t.Errorf("upgraded nine-product identity count = %d, want18", count)
	}
	catalog := migrationCatalogData(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	if count := testCount(t, reopened, "products"); count != 9 {
		t.Errorf("reopen expanded customized catalog to %d products", count)
	}
	if after := migrationLegacyData(t, reopened.db); !reflect.DeepEqual(before, after) {
		t.Error("reopen changed extended legacy history")
	}
	if after := migrationCatalogData(t, reopened.db); !reflect.DeepEqual(catalog, after) {
		t.Error("reopen changed customized catalog metadata or identities")
	}
}
