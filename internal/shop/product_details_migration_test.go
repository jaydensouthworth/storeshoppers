package shop

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Saved v7 additions on top of legacyPromotionsV6. This literal deliberately
// does not use the application's migrations, Open, or current schema to build
// the old database: a future migration edit must not upgrade the fixture.
const legacyProductDetailsV7 = `
CREATE TABLE promotions (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 product_id INTEGER NOT NULL REFERENCES products(id),
 sale_price INTEGER NOT NULL CHECK(sale_price BETWEEN 1 AND 1000000),
 starts INTEGER NOT NULL CHECK(starts>=0),
 ends INTEGER NOT NULL CHECK(ends>starts),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>0),
 cancelled INTEGER NOT NULL DEFAULT 0 CHECK(cancelled IN(0,1))
);
CREATE INDEX promotions_product_window ON promotions(product_id,starts,ends) WHERE cancelled=0;
CREATE TRIGGER promotions_no_overlap_insert BEFORE INSERT ON promotions
 WHEN NEW.cancelled=0 AND EXISTS(SELECT 1 FROM promotions WHERE product_id=NEW.product_id AND cancelled=0 AND starts<NEW.ends AND ends>NEW.starts)
 BEGIN SELECT RAISE(ABORT,'overlapping promotion'); END;
CREATE TRIGGER promotions_no_overlap_update BEFORE UPDATE ON promotions
 WHEN NEW.cancelled=0 AND EXISTS(SELECT 1 FROM promotions WHERE id<>NEW.id AND product_id=NEW.product_id AND cancelled=0 AND starts<NEW.ends AND ends>NEW.starts)
 BEGIN SELECT RAISE(ABORT,'overlapping promotion'); END;
CREATE TABLE product_features (
 product_id INTEGER PRIMARY KEY REFERENCES products(id),
 featured INTEGER NOT NULL CHECK(featured IN(0,1)),
 version INTEGER NOT NULL CHECK(version>0)
);
CREATE TABLE promotion_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 product_id INTEGER NOT NULL REFERENCES products(id),
 promotion_id INTEGER REFERENCES promotions(id),
 action TEXT NOT NULL CHECK(action IN('create','edit','cancel','feature','unfeature','example')),
 name TEXT NOT NULL,details TEXT NOT NULL,
 created TEXT NOT NULL
);
CREATE TABLE promotion_commands (
 command_key TEXT PRIMARY KEY,
 command_hash TEXT NOT NULL
);
INSERT INTO schema_version VALUES(7);
`

func populatedProductDetailsV7(t *testing.T, now time.Time) (string, Session) {
	t.Helper()
	path, owner := populatedPromotionsV6(t, now)
	db := migrationRawDB(t, path)
	migrationExec(t, db, legacyProductDetailsV7)
	populatePromotionRecoveryHistory(t, &Store{db: db, now: func() time.Time { return now }}, now)
	var version, detailsTables int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("saved fixture must stop at v7: version=%d error=%v", version, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='product_details'`).Scan(&detailsTables); err != nil || detailsTables != 0 {
		t.Fatalf("saved v7 fixture already has product details: count=%d error=%v", detailsTables, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, owner
}

func assertProductDetailsAbsent(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"product_details", "product_example_products", "product_detail_commands"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("ordinary migration invented %s data: count=%d error=%v", table, count, err)
		}
	}
}

func assertProductDetailsSchema8(t *testing.T, db *sql.DB) {
	t.Helper()
	var version, count int
	if err := db.QueryRow(`SELECT MAX(version),COUNT(*) FROM schema_version`).Scan(&version, &count); err != nil || version != latestSchemaVersion || count != latestSchemaVersion {
		t.Fatalf("product details schema: max=%d count=%d latest=%d error=%v; want current version", version, count, latestSchemaVersion, err)
	}
	if err := checkSQLite(db); err != nil {
		t.Fatal(err)
	}
}

func TestProductDetailsPopulatedV7UpgradePreservesDataAndRetries(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "final-version-write rollback and retry"}[failure], func(t *testing.T) {
			now := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
			path, owner := populatedProductDetailsV7(t, now)
			db := migrationRawDB(t, path)
			before := shopperPriorTables(t, db)
			if failure {
				migrationExec(t, db, `CREATE TRIGGER reject_product_details_version BEFORE INSERT ON schema_version WHEN NEW.version=8 BEGIN SELECT RAISE(ABORT,'injected v8 final version failure'); END`)
				fingerprint := fingerprintTest(t, db)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				broken, err := OpenWithClock(path, func() time.Time { return now })
				if err == nil {
					_ = broken.Close()
					t.Fatal("v8 succeeded despite its final version-write failure")
				}
				if !strings.Contains(err.Error(), "injected v8 final version failure") {
					t.Fatalf("migration failed before the injected final statement: %v", err)
				}
				db = migrationRawDB(t, path)
				if got := fingerprintTest(t, db); got != fingerprint {
					t.Fatal("failed v8 changed existing schema, catalog, holds, receipts, promotion history or sequences")
				}
				var retained int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE tbl_name IN('product_details','product_example_products','product_detail_commands')`).Scan(&retained); err != nil || retained != 0 {
					t.Fatalf("failed v8 retained detail schema objects: count=%d error=%v", retained, err)
				}
				migrationExec(t, db, `DROP TRIGGER reject_product_details_version`)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			s := openPromotionMigrationStore(t, path, now)
			assertShopperPriorTables(t, s.db, before)
			assertProductDetailsAbsent(t, s.db)
			assertProductDetailsSchema8(t, s.db)
			for _, id := range []int64{1, 2, 3} {
				details, err := s.ProductDetails(id)
				if expected := (ProductDetails{ProductID: id, Kind: "unspecified"}); err != nil || !reflect.DeepEqual(details, expected) {
					t.Fatalf("ordinary migration invented metadata for existing product %d: got=%+v error=%v", id, details, err)
				}
			}
			if order := testOrder(t, s, 4, owner.ID); order.Total != 847 || order.FinalTotal != 299 || order.PickedCount != 1 || order.CompletionKind != "partial" {
				t.Fatalf("v8 changed historical receipt or actual fulfillment: %+v", order)
			}
			fingerprint := fingerprintTest(t, s.db)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openPromotionMigrationStore(t, path, now.Add(time.Hour))
			if got := fingerprintTest(t, reopened.db); got != fingerprint {
				t.Fatal("ordinary restart replayed detail migration or changed preserved v7 state")
			}
			if len(savedBackups(t, path)) != 0 {
				t.Fatal("ordinary migration or restart created a reset archive")
			}
		})
	}
}

func TestProductDetailsEmptyEstablishedV7NeverSeeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-v7.db")
	db := migrationRawDB(t, path)
	migrationExec(t, db, legacyPromotionsV6)
	migrationExec(t, db, legacyProductDetailsV7)
	before := shopperPriorTables(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s := openTestStore(t, path)
		assertShopperPriorTables(t, s.db, before)
		assertProductDetailsAbsent(t, s.db)
		assertProductDetailsSchema8(t, s.db)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProductDetailsEstablishedSeventyProductV7NeverExpands(t *testing.T) {
	// An established catalog can still contain all eight recognizable original
	// demo identities and the old 70-product count. Those are not permission to
	// install fresh examples or fictional nutrition during a normal upgrade.
	path := filepath.Join(t.TempDir(), "seventy-product-v7.db")
	db := migrationRawDB(t, path)
	migrationExec(t, db, legacyPromotionsV6)
	migrationExec(t, db, legacyProductDetailsV7)
	migrationExec(t, db, `INSERT INTO categories(id,name,normalized_name) VALUES(1,'Produce','produce'),(2,'Bakery','bakery'),(3,'Dairy','dairy'),(4,'Pantry','pantry')`)
	migrationExec(t, db, `INSERT INTO catalog_sequence VALUES(1,71)`)
	migrationExec(t, db, `INSERT INTO products(id,name,description,category,barcode,icon,price,stock,category_id,sku) VALUES
 (1,'Honeycrisp apples','Crisp, sweet and ready for the fruit bowl.','Produce','000000000101','apple',349,24,1,'SHOPDEMO-000001'),
 (2,'Baby spinach','Tender leaves for quick lunches and green dinners.','Produce','000000000102','leaf',299,18,1,'SHOPDEMO-000002'),
 (3,'Sourdough loaf','A golden crust and a soft, tangy center.','Bakery','000000000103','bread',599,10,2,'SHOPDEMO-000003'),
 (4,'Whole milk','A half gallon of the everyday essential.','Dairy','000000000104','milk',429,16,3,'SHOPDEMO-000004'),
 (5,'Free range eggs','A dozen large eggs for a well-stocked kitchen.','Dairy','000000000105','egg',649,6,3,'SHOPDEMO-000005'),
 (6,'Penne pasta','A pantry staple made for your favorite sauce.','Pantry','000000000106','pasta',249,30,4,'SHOPDEMO-000006'),
 (7,'Extra virgin olive oil','Smooth and peppery. Finish something delicious.','Pantry','000000000107','oil',1099,8,4,'SHOPDEMO-000007'),
 (8,'Strawberry jam','Small-batch style preserves for your morning toast.','Pantry','000000000108','jam',479,0,4,'SHOPDEMO-000008')`)
	migrationExec(t, db, `WITH RECURSIVE ids(id) AS (SELECT 9 UNION ALL SELECT id+1 FROM ids WHERE id<70)
 INSERT INTO products(id,name,description,category,barcode,icon,price,stock,category_id,sku)
 SELECT id,'Saved demo product '||id,'Keep saved pack information','Produce','SHOPDEMO-'||printf('%06d',id),'apple',249,12,1,'SHOPDEMO-'||printf('%06d',id) FROM ids`)
	migrationExec(t, db, `INSERT INTO product_codes(product_id,scheme,raw_value,normalized_value,symbology)
 SELECT id,'demo_local',sku,sku,'Code128' FROM products
 UNION ALL SELECT id,'legacy_placeholder',barcode,barcode,'unvalidated' FROM products WHERE id<=8`)
	before := shopperPriorTables(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t, path)
	assertShopperPriorTables(t, s.db, before)
	assertProductDetailsAbsent(t, s.db)
	assertProductDetailsSchema8(t, s.db)
	if n := testCount(t, s, "products"); n != 70 {
		t.Fatalf("ordinary v7 upgrade expanded the established catalog: products=%d, want 70", n)
	}
}

func productDetailsRecoveryValues() []ProductDetails {
	return []ProductDetails{
		{ProductID: 1, Kind: "food", Body: "Saved manager product details.\nKeep the second paragraph.", PackageLabel: "Two fruit pieces", PackageDetails: "Loose in a paper bag", Nutrition: &DemoNutrition{ServingLabel: "One demo portion", EnergyKcal: 97, FatTenths: 3, CarbohydrateTenths: 251, ProteinTenths: 7, SodiumMG: 2}},
		{ProductID: 2, Kind: "nonfood", Body: "Saved nonfood demonstration details", PackageLabel: "One sealed pouch", PackageDetails: "Keep away from heat"},
	}
}

func assertProductDetailsRecoveryValues(t *testing.T, s *Store, want []ProductDetails) {
	t.Helper()
	for _, expected := range want {
		got, err := s.ProductDetails(expected.ProductID)
		if err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatalf("product %d metadata = %+v, error=%v; want %+v", expected.ProductID, got, err, expected)
		}
	}
}

func TestProductDetailsRestartArchiveAndResetPreserveRecoveryHistory(t *testing.T) {
	now := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	path, _ := populatedProductDetailsV7(t, now)
	s := openPromotionMigrationStore(t, path, now)
	want := productDetailsRecoveryValues()
	for _, details := range want {
		p := testCatalogProduct(t, s, details.ProductID)
		if id, err := s.SaveProductWithDetails(p, details); err != nil || id != p.ID {
			t.Fatalf("save product %d details: id=%d error=%v", p.ID, id, err)
		}
	}
	// A second metadata save must update the existing row, and the legacy
	// SaveProduct API must preserve details when changing ordinary catalog text.
	want[0].Body = "Edited manager product details.\nRetain this across restart and restore."
	want[0].Nutrition.SodiumMG = 11
	p := testCatalogProduct(t, s, 1)
	if _, err := s.SaveProductWithDetails(p, want[0]); err != nil {
		t.Fatal(err)
	}
	p = testCatalogProduct(t, s, 1)
	p.Description = "Ordinary catalog edit must retain saved metadata"
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	p = testCatalogProduct(t, s, 2)
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	// Preserve optional-example identity and replay state alongside metadata,
	// including the provenance of a now-archived product.
	testExec(t, s, `INSERT INTO product_example_products VALUES('saved-custom-example',2)`)
	testExec(t, s, `INSERT INTO product_detail_commands VALUES('saved-detail-command','saved-detail-command-hash')`)
	assertProductDetailsRecoveryValues(t, s, want)
	if n := testCount(t, s, "product_details"); n != len(want) {
		t.Fatalf("saved details rows=%d, want %d without adding metadata for the pre-existing archive", n, len(want))
	}
	before := fingerprintTest(t, s.db)
	metadataBefore := productDetailsRecoveryRows(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openPromotionMigrationStore(t, path, now)
	assertProductDetailsRecoveryValues(t, s, want)
	if got := fingerprintTest(t, s.db); got != before {
		t.Fatal("restart changed edited descriptions, nonfood metadata, archived metadata or prior history")
	}
	if !testCatalogProduct(t, s, 2).Archived {
		t.Fatal("restart restored the product whose metadata was archived")
	}
	result := resetTest(t, s)
	if !result.Reset || result.BackupPath == "" {
		t.Fatalf("explicit reset did not report its archive: %+v", result)
	}
	assertProductDetailsFreshSeed(t, s)
	assertResetPromotionSamples(t, s, now)
	backup, err := openBackupReadOnly(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := checkSQLite(backup); err != nil {
		t.Fatal(err)
	}
	if got := fingerprintTest(t, backup); got != before {
		t.Fatal("reset archive lost edited or archived product metadata, live holds, working orders, receipts or promotion history")
	}
	if got := productDetailsRecoveryRows(t, backup); !reflect.DeepEqual(got, metadataBefore) {
		t.Fatalf("reset archive changed metadata: got=%v want=%v", got, metadataBefore)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored-product-details.db")
	restoredDB := migrationRawDB(t, restoredPath)
	if err := copySQLite(restoredDB, backup); err != nil {
		t.Fatal(err)
	}
	if err := restoredDB.Close(); err != nil {
		t.Fatal(err)
	}
	restored := openPromotionMigrationStore(t, restoredPath, now)
	assertProductDetailsRecoveryValues(t, restored, want)
	if got := fingerprintTest(t, restored.db); got != before {
		t.Fatal("opening the restored archive reseeded metadata or changed prior state")
	}
	baseline := fingerprintTest(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openPromotionMigrationStore(t, path, now.AddDate(0, 0, 14))
	if got := fingerprintTest(t, reopened.db); got != baseline {
		t.Fatal("ordinary restart changed reset metadata or refreshed sample promotions")
	}
}

func productDetailsRecoveryRows(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	rows := make(map[string][][]any)
	for _, table := range []string{"product_details", "product_example_products", "product_detail_commands"} {
		rows[table] = migrationQuerySnapshot(t, db, `SELECT * FROM `+table+` ORDER BY rowid`)
	}
	return rows
}

func assertProductDetailsFreshSeed(t *testing.T, s *Store) {
	t.Helper()
	assertProductDetailsSchema8(t, s.db)
	if n := testCount(t, s, "products"); n != 72 {
		t.Fatalf("fresh/reset product count=%d, want 72", n)
	}
	var changed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM products WHERE id<=70 AND (catalog_version<>1 OR version<>1 OR archived<>0)`).Scan(&changed); err != nil || changed != 0 {
		t.Fatalf("detail seed changed original catalog or inventory versions: count=%d error=%v", changed, err)
	}
	for id, sku := range map[int64]string{71: "DEMO-DEODORANT-75G", 72: "DEMO-RAZORS-3PK"} {
		p := testCatalogProduct(t, s, id)
		details, err := s.ProductDetails(id)
		if err != nil || details.ProductID != id || details.Kind != "nonfood" || details.Nutrition != nil || details.Body == "" || details.PackageLabel == "" {
			t.Fatalf("new nonfood seed %d details=%+v error=%v", id, details, err)
		}
		if p.SKU != sku || p.Archived || p.Stock <= 0 || p.SaleUnit != "each" || p.CatalogVersion != 1 || p.PriceVersion != 1 || p.Version != 1 {
			t.Fatalf("new nonfood seed %d is not an untouched sellable product: %+v", id, p)
		}
	}
}

func TestProductDetailsFreshSeedAddsNonfoodOnlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh-details.db")
	s := openTestStore(t, path)
	assertProductDetailsFreshSeed(t, s)
	assertPromotionTablesEmpty(t, s.db)
	var changed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM products WHERE price_version<>1`).Scan(&changed); err != nil || changed != 0 {
		t.Fatalf("fresh metadata enrichment changed price versions: count=%d error=%v", changed, err)
	}
	// Preserve the original identities and stock as well as their optimistic
	// versions; a description seed must not edit the familiar demo products.
	wantOriginal := [][]any{
		{int64(1), "Honeycrisp apples", "000000000101", int64(349), int64(24)},
		{int64(2), "Baby spinach", "000000000102", int64(299), int64(18)},
		{int64(3), "Sourdough loaf", "000000000103", int64(599), int64(10)},
		{int64(4), "Whole milk", "000000000104", int64(429), int64(16)},
		{int64(5), "Free range eggs", "000000000105", int64(649), int64(6)},
		{int64(6), "Penne pasta", "000000000106", int64(249), int64(30)},
		{int64(7), "Extra virgin olive oil", "000000000107", int64(1099), int64(8)},
		{int64(8), "Strawberry jam", "000000000108", int64(479), int64(0)},
	}
	if got := migrationQuerySnapshot(t, s.db, `SELECT id,name,barcode,price,stock FROM products WHERE id<=8 ORDER BY id`); !reflect.DeepEqual(got, wantOriginal) {
		t.Fatalf("fresh metadata seed changed original products: got=%v want=%v", got, wantOriginal)
	}
	p := testCatalogProduct(t, s, 71)
	details, err := s.ProductDetails(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	details.Body = "Manager's retained nonfood product copy"
	details.PackageLabel = "Manager's retained pack"
	if _, err := s.SaveProductWithDetails(p, details); err != nil {
		t.Fatal(err)
	}
	p = testCatalogProduct(t, s, 72)
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	if got := fingerprintTest(t, reopened.db); got != before {
		t.Fatal("reopening a fresh database overwrote edited metadata or resurrected its archived nonfood sample")
	}
}
