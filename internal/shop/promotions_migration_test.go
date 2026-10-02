package shop

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Saved v6 DDL, intentionally independent of schema.sql, migration strings and
// Open. New application migrations cannot silently upgrade this fixture before
// the test observes the old schema, populated working state and audit history.
const legacyPromotionsV6 = `
CREATE TABLE adjustments (
 id INTEGER PRIMARY KEY, product_id INTEGER NOT NULL REFERENCES products(id), delta INTEGER NOT NULL,
 reason TEXT NOT NULL, created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now'))
, sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN ('each','g')));
CREATE TABLE basket_events (
 id INTEGER PRIMARY KEY,
 basket_id TEXT NOT NULL REFERENCES baskets(id),
 action TEXT NOT NULL,
 reason TEXT NOT NULL,
 details TEXT NOT NULL,
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now'))
);
CREATE TABLE baskets (
 id TEXT PRIMARY KEY,
 owner_session_id TEXT NOT NULL REFERENCES sessions(id),
 synthetic INTEGER NOT NULL DEFAULT 0 CHECK(synthetic IN (0,1)),
 label TEXT NOT NULL DEFAULT 'Customer basket',
 practice_slot INTEGER NOT NULL DEFAULT 0 CHECK(practice_slot BETWEEN 0 AND 2),
 revision INTEGER NOT NULL DEFAULT 1,
 hold_until INTEGER NOT NULL DEFAULT 0 CHECK(hold_until>=0),
 UNIQUE(owner_session_id,practice_slot),
 CHECK((synthetic=0 AND practice_slot=0) OR (synthetic=1 AND practice_slot>0))
);
CREATE TABLE cart (
 basket_id TEXT NOT NULL REFERENCES baskets(id),
 product_id INTEGER NOT NULL REFERENCES products(id),
 quantity INTEGER NOT NULL CHECK(quantity BETWEEN 1 AND 99),
 reserved INTEGER NOT NULL DEFAULT 0 CHECK(reserved>=0 AND reserved<=quantity),
 PRIMARY KEY(basket_id,product_id)
);
CREATE TABLE catalog_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 kind TEXT NOT NULL CHECK(kind IN ('product','category','type')),
 entity_id INTEGER NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('create','edit','archive','restore')),
 name TEXT NOT NULL, details TEXT NOT NULL,
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now'))
);
CREATE TABLE catalog_sequence (id INTEGER PRIMARY KEY CHECK(id=1), next_product_id INTEGER NOT NULL CHECK(next_product_id>0));
CREATE TABLE categories (
 id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
 normalized_name TEXT NOT NULL UNIQUE, archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0)
);
CREATE TABLE order_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,order_id INTEGER NOT NULL REFERENCES orders(id),
 command_key TEXT NOT NULL,command_hash TEXT NOT NULL,
 action TEXT NOT NULL,reason TEXT NOT NULL,details TEXT NOT NULL,
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now')),
 UNIQUE(order_id,command_key)
);
CREATE TABLE order_items (
 order_id INTEGER NOT NULL REFERENCES orders(id), product_id INTEGER NOT NULL REFERENCES products(id),
 name TEXT NOT NULL, price INTEGER NOT NULL CHECK(price > 0), quantity INTEGER NOT NULL CHECK(quantity BETWEEN 1 AND 99), picked_quantity INTEGER NOT NULL DEFAULT 0
    CHECK(picked_quantity >= 0 AND picked_quantity <= quantity), pick_version INTEGER NOT NULL DEFAULT 1
    CHECK(pick_version > 0), sku TEXT NOT NULL DEFAULT '', sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN ('each','g')), price_basis INTEGER NOT NULL DEFAULT 1 CHECK(price_basis > 0), quantity_step INTEGER NOT NULL DEFAULT 1 CHECK(quantity_step > 0), subtotal INTEGER NOT NULL DEFAULT 0 CHECK(subtotal >= 0),
 PRIMARY KEY(order_id,product_id)
);
CREATE TABLE orders (
 id INTEGER PRIMARY KEY AUTOINCREMENT, reference TEXT NOT NULL UNIQUE,
 session_id TEXT NOT NULL, checkout_key TEXT NOT NULL UNIQUE, total INTEGER NOT NULL CHECK(total > 0),
 status TEXT NOT NULL DEFAULT 'Placed' CHECK(status IN ('Placed','Picking','Ready','Completed')),
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now'))
, instructions TEXT NOT NULL DEFAULT '' CHECK(length(instructions)<=500), order_version INTEGER NOT NULL DEFAULT 1 CHECK(order_version>0), final_total INTEGER CHECK(final_total>=0), completion_kind TEXT NOT NULL DEFAULT '' CHECK(completion_kind IN ('','full','partial','cancelled')));
CREATE TABLE product_codes (
 id INTEGER PRIMARY KEY AUTOINCREMENT, product_id INTEGER NOT NULL REFERENCES products(id),
 scheme TEXT NOT NULL CHECK(scheme IN ('legacy_placeholder','demo_local')),
 raw_value TEXT NOT NULL, normalized_value TEXT NOT NULL, symbology TEXT NOT NULL,
 archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
 UNIQUE(scheme,normalized_value)
);
CREATE TABLE product_types (
 id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
 normalized_name TEXT NOT NULL UNIQUE, archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0)
);
CREATE TABLE products (
 id INTEGER PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, category TEXT NOT NULL,
 barcode TEXT NOT NULL UNIQUE, icon TEXT NOT NULL, price INTEGER NOT NULL CHECK(price BETWEEN 1 AND 1000000),
 stock INTEGER NOT NULL CHECK(stock BETWEEN 0 AND 10000), version INTEGER NOT NULL DEFAULT 1
, category_id INTEGER REFERENCES categories(id), type_id INTEGER REFERENCES product_types(id), sku TEXT NOT NULL DEFAULT '', archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)), catalog_version INTEGER NOT NULL DEFAULT 1 CHECK(catalog_version > 0), price_version INTEGER NOT NULL DEFAULT 1 CHECK(price_version > 0), sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN ('each','g')), price_basis INTEGER NOT NULL DEFAULT 1 CHECK((sale_unit='each' AND price_basis=1) OR (sale_unit='g' AND price_basis=1000)), quantity_step INTEGER NOT NULL DEFAULT 1 CHECK((sale_unit='each' AND quantity_step=1) OR (sale_unit='g' AND quantity_step BETWEEN 1 AND 1000)));
CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
CREATE TABLE sessions (
 id TEXT PRIMARY KEY, csrf TEXT NOT NULL, checkout_key TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1, manager_until INTEGER NOT NULL DEFAULT 0, expires INTEGER NOT NULL
);
CREATE TABLE shopper_assignments (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 order_id INTEGER NOT NULL REFERENCES orders(id),
 shopper_id INTEGER NOT NULL REFERENCES shoppers(id),
 state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','cancelled','ended')),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>0),
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now')),
 ended TEXT NOT NULL DEFAULT '',
 CHECK((state='active' AND ended='') OR (state<>'active' AND ended<>''))
);
CREATE TABLE shopper_event_links (
 event_id INTEGER PRIMARY KEY REFERENCES order_events(id),
 assignment_id INTEGER NOT NULL REFERENCES shopper_assignments(id),
 from_shopper_id INTEGER REFERENCES shoppers(id),
 to_shopper_id INTEGER REFERENCES shoppers(id)
);
CREATE TABLE shoppers (
 id INTEGER PRIMARY KEY,
 name TEXT NOT NULL UNIQUE,
 initials TEXT NOT NULL
);
CREATE TABLE working_order_items (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 order_id INTEGER NOT NULL REFERENCES orders(id),
 product_id INTEGER NOT NULL REFERENCES products(id),
 name TEXT NOT NULL,price INTEGER NOT NULL CHECK(price>0),
 quantity INTEGER NOT NULL CHECK(quantity BETWEEN 0 AND 99),
 picked_quantity INTEGER NOT NULL DEFAULT 0 CHECK(picked_quantity>=0 AND picked_quantity<=quantity),
 pick_version INTEGER NOT NULL DEFAULT 1 CHECK(pick_version>0),
 sku TEXT NOT NULL,sale_unit TEXT NOT NULL CHECK(sale_unit IN ('each','g')),
 price_basis INTEGER NOT NULL CHECK(price_basis>0),quantity_step INTEGER NOT NULL CHECK(quantity_step>0),
 unavailable_quantity INTEGER NOT NULL DEFAULT 0 CHECK(unavailable_quantity>=0),
 cancelled_quantity INTEGER NOT NULL DEFAULT 0 CHECK(cancelled_quantity>=0),
 CHECK(picked_quantity+unavailable_quantity+cancelled_quantity<=quantity),
 UNIQUE(order_id,product_id)
);
CREATE INDEX baskets_expiry ON baskets(hold_until) WHERE hold_until>0;
CREATE INDEX cart_product ON cart(product_id);
CREATE INDEX orders_session ON orders(session_id);
CREATE UNIQUE INDEX products_sku_unique ON products(sku COLLATE NOCASE);
CREATE INDEX shopper_active_roster ON shopper_assignments(shopper_id,state);
CREATE UNIQUE INDEX shopper_one_active_order ON shopper_assignments(order_id) WHERE state='active';
INSERT INTO schema_version VALUES(1),(2),(3),(4),(5),(6);
`

func populatedPromotionsV6(t *testing.T, now time.Time) (string, Session) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "populated-v6.db")
	db := migrationRawDB(t, path)
	migrationExec(t, db, legacyPromotionsV6)
	migrationExec(t, db, `PRAGMA foreign_keys=ON`)
	migrationExec(t, db, `INSERT INTO categories(id,name,normalized_name,version) VALUES(1,'Saved produce','saved produce',7)`)
	migrationExec(t, db, `INSERT INTO product_types(id,name,normalized_name,version) VALUES(1,'Saved local fruit','saved local fruit',4)`)
	migrationExec(t, db, `INSERT INTO products(id,name,description,category,barcode,icon,price,stock,version,category_id,type_id,sku,archived,catalog_version,price_version) VALUES
 (1,'Saved manager apples','Keep this description','Saved produce','000000000101','apple',811,8,23,1,1,'CUSTOM-APPLE',0,7,4),
 (2,'Saved manager spinach','Keep these greens','Saved produce','000000000102','leaf',381,5,17,1,NULL,'CUSTOM-SPINACH',0,5,3),
 (3,'Saved archived pears','Keep archived identity','Saved produce','000000000103','apple',529,4,12,1,1,'CUSTOM-PEAR',1,9,8)`)
	migrationExec(t, db, `INSERT INTO catalog_sequence VALUES(1,104)`)
	migrationExec(t, db, `INSERT INTO product_codes(product_id,scheme,raw_value,normalized_value,symbology,archived)
 SELECT id,'legacy_placeholder',barcode,barcode,'unvalidated',archived FROM products
 UNION ALL SELECT id,'demo_local','SHOPDEMO-'||printf('%06d',id),'SHOPDEMO-'||printf('%06d',id),'Code128',archived FROM products`)
	owner := Session{ID: strings.Repeat("a", 64), CSRF: strings.Repeat("b", 64), CheckoutKey: strings.Repeat("c", 64), Revision: 12, ManagerUntil: now.Add(time.Hour).Unix()}
	migrationExec(t, db, `INSERT INTO sessions VALUES(?,?,?,?,?,?)`, owner.ID, owner.CSRF, owner.CheckoutKey, owner.Revision, owner.ManagerUntil, now.Add(48*time.Hour).Unix())
	migrationExec(t, db, `INSERT INTO baskets(id,owner_session_id,revision,hold_until) VALUES('saved-customer-basket',?,19,?)`, owner.ID, now.Add(24*time.Hour).Unix())
	migrationExec(t, db, `INSERT INTO baskets(id,owner_session_id,synthetic,label,practice_slot,revision,hold_until) VALUES('saved-practice-basket',?,1,'Saved practice basket',1,8,?)`, owner.ID, now.Add(12*time.Hour).Unix())
	migrationExec(t, db, `INSERT INTO cart VALUES('saved-customer-basket',2,3,2),('saved-practice-basket',1,2,1)`)
	migrationExec(t, db, `INSERT INTO basket_events VALUES(1,'saved-customer-basket','reserve','Retain active hold','Two held units with one desired remainder','2026-09-30 09:01 UTC')`)
	migrationExec(t, db, `INSERT INTO adjustments VALUES(1,1,-2,'Saved stock correction','2026-09-30 09:02 UTC','each')`)
	migrationExec(t, db, `INSERT INTO catalog_events(kind,entity_id,action,name,details,created) VALUES('product',1,'edit','Saved manager apples','Keep custom catalog edit','2026-09-30 09:03 UTC')`)
	for index, status := range []string{"Placed", "Picking", "Ready", "Completed"} {
		id := index + 1
		migrationExec(t, db, `INSERT INTO orders(id,reference,session_id,checkout_key,total,status,created,instructions,order_version) VALUES(?,?,?,?,847,?,'2026-09-30 10:00 UTC','Keep customer instructions',?)`, id, "SAVED-V6-"+status, owner.ID, "saved-v6-key-"+status, status, 10+id)
		migrationExec(t, db, `INSERT INTO order_items(order_id,product_id,name,price,quantity,picked_quantity,pick_version,sku,sale_unit,price_basis,quantity_step,subtotal) VALUES
 (?,1,'Original receipt apples',299,2,0,3,'ORIGINAL-APPLE','each',1,1,598),
 (?,2,'Original receipt spinach',249,1,0,2,'ORIGINAL-SPINACH','each',1,1,249)`, id, id)
	}
	migrationExec(t, db, `UPDATE order_items SET picked_quantity=quantity WHERE order_id IN(3,4)`)
	migrationExec(t, db, `UPDATE orders SET final_total=847,completion_kind='full' WHERE id=3`)
	migrationExec(t, db, `UPDATE orders SET final_total=299,completion_kind='partial',order_version=31 WHERE id=4`)
	migrationExec(t, db, `INSERT INTO working_order_items(order_id,product_id,name,price,quantity,picked_quantity,pick_version,sku,sale_unit,price_basis,quantity_step)
 SELECT order_id,product_id,name,price,quantity,picked_quantity,pick_version,sku,sale_unit,price_basis,quantity_step FROM order_items`)
	migrationExec(t, db, `UPDATE working_order_items SET quantity=3,picked_quantity=1,pick_version=8 WHERE order_id=2 AND product_id=1`)
	migrationExec(t, db, `UPDATE working_order_items SET quantity=0,picked_quantity=0,pick_version=11 WHERE order_id=1 AND product_id=2`)
	migrationExec(t, db, `INSERT INTO working_order_items(order_id,product_id,name,price,quantity,picked_quantity,pick_version,sku,sale_unit,price_basis,quantity_step) VALUES(1,3,'Saved removed manager-added pears',479,0,0,13,'CUSTOM-PEAR','each',1,1)`)
	migrationExec(t, db, `UPDATE working_order_items SET picked_quantity=CASE product_id WHEN 1 THEN 1 ELSE 0 END,unavailable_quantity=1,pick_version=12 WHERE order_id=4`)
	migrationExec(t, db, `INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details,created) VALUES
 (1,'saved-remove','saved-remove-hash','remove','Retain tombstone reason','Spinach removed while receipt retained','2026-09-30 10:01 UTC'),
 (4,'saved-partial','saved-partial-hash','finish','Only one apple available','Keep partial total and unavailable units','2026-09-30 10:02 UTC')`)
	migrationExec(t, db, `INSERT INTO shoppers VALUES(1,'Avery Morgan','AM'),(2,'Jordan Lee','JL'),(3,'Casey Rivera','CR')`)
	populateShopperHistory(t, &Store{db: db, now: func() time.Time { return now }})
	migrationExec(t, db, `CREATE TABLE custom_promotion_recovery_notes(id INTEGER PRIMARY KEY,note TEXT NOT NULL)`)
	migrationExec(t, db, `INSERT INTO custom_promotion_recovery_notes VALUES(1,'Preserve unrelated extension data and high-water marks')`)
	// A deleted high ID must not be reused because migration reset a sequence.
	migrationExec(t, db, `INSERT INTO catalog_events(id,kind,entity_id,action,name,details,created) VALUES(90,'product',2,'edit','Deleted audit fixture','Reserve sequence high-water mark','2026-09-30 10:03 UTC')`)
	migrationExec(t, db, `DELETE FROM catalog_events WHERE id=90`)
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 6 {
		t.Fatalf("saved fixture must stop at v6: version=%d error=%v", version, err)
	}
	if err := checkSQLite(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, owner
}

func openPromotionMigrationStore(t *testing.T, path string, now time.Time) *Store {
	t.Helper()
	s, err := OpenWithClock(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func assertPromotionTablesEmpty(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"promotions", "product_features", "promotion_events", "promotion_commands"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Errorf("ordinary startup invented %s rows: count=%d error=%v", table, count, err)
		}
	}
}

func TestPromotionsPopulatedV6UpgradePreservesDataAndRetries(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "final-version-write rollback and retry"}[failure], func(t *testing.T) {
			now := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
			path, owner := populatedPromotionsV6(t, now)
			db := migrationRawDB(t, path)
			before := shopperPriorTables(t, db)
			if failure {
				migrationExec(t, db, `CREATE TRIGGER reject_promotions_version BEFORE INSERT ON schema_version WHEN NEW.version=7 BEGIN SELECT RAISE(ABORT,'injected v7 final version failure'); END`)
				fingerprint := fingerprintTest(t, db)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				broken, err := OpenWithClock(path, func() time.Time { return now })
				if err == nil {
					_ = broken.Close()
					t.Fatal("v7 succeeded despite its final version-write failure")
				}
				if !strings.Contains(err.Error(), "injected v7 final version failure") {
					t.Fatalf("migration failed before the injected final statement: %v", err)
				}
				db = migrationRawDB(t, path)
				if got := fingerprintTest(t, db); got != fingerprint {
					t.Fatal("failed v7 changed existing schema, rows, sequences, assignments or event links")
				}
				var retained int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN('promotions','product_features','promotion_events','promotion_commands','promotions_product_window','promotions_no_overlap_insert','promotions_no_overlap_update')`).Scan(&retained); err != nil || retained != 0 {
					t.Fatalf("failed v7 left additive tables, indices or triggers: count=%d error=%v", retained, err)
				}
				migrationExec(t, db, `DROP TRIGGER reject_promotions_version`)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			s := openPromotionMigrationStore(t, path, now)
			assertShopperPriorTables(t, s.db, before)
			assertPromotionTablesEmpty(t, s.db)
			var version, versions int
			if err := s.db.QueryRow(`SELECT MAX(version),COUNT(*) FROM schema_version`).Scan(&version, &versions); err != nil || version != latestSchemaVersion || versions != latestSchemaVersion {
				t.Fatalf("upgraded versions=%d max=%d error=%v, want %d", versions, version, err, latestSchemaVersion)
			}
			if order := testOrder(t, s, 4, owner.ID); order.Total != 847 || order.FinalTotal != 299 || order.PickedCount != 1 || order.CompletionKind != "partial" {
				t.Fatalf("v7 rewrote historical receipt or partial completion: %+v", order)
			}
			if err := checkSQLite(s.db); err != nil {
				t.Fatal(err)
			}
			fingerprint := fingerprintTest(t, s.db)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openPromotionMigrationStore(t, path, now.Add(time.Hour))
			if got := fingerprintTest(t, reopened.db); got != fingerprint {
				t.Fatal("ordinary restart replayed v7 or altered preserved live holds, tombstones or audits")
			}
			if len(savedBackups(t, path)) != 0 {
				t.Fatal("ordinary migration or restart created a reset archive")
			}
		})
	}
}

func TestPromotionsOrdinaryOpenNeverSeedsOffers(t *testing.T) {
	for _, established := range []bool{false, true} {
		t.Run(map[bool]string{false: "new database", true: "empty established v6"}[established], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "no-invented-offers.db")
			if established {
				db := migrationRawDB(t, path)
				migrationExec(t, db, legacyPromotionsV6)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				s := openTestStore(t, path)
				assertPromotionTablesEmpty(t, s.db)
				if established && testCount(t, s, "products") != 0 {
					t.Fatal("v7 populated an empty established catalog")
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func populatePromotionRecoveryHistory(t *testing.T, s *Store, now time.Time) {
	t.Helper()
	// Direct inserts keep this storage/recovery test independent of manager
	// command code. Preserve live, scheduled, expired and cancelled offers, and
	// an explicit unfeatured row whose optimistic version must not be forgotten.
	testExec(t, s, `INSERT INTO promotions(id,product_id,sale_price,starts,ends,version,cancelled) VALUES
 (101,1,611,?,?,4,0),(102,2,281,?,?,3,0),
 (103,1,501,?,?,7,0),(104,3,379,?,?,8,1)`,
		now.Add(-time.Hour).Unix(), now.Add(time.Hour).Unix(),
		now.Add(48*time.Hour).Unix(), now.Add(72*time.Hour).Unix(),
		now.Add(-72*time.Hour).Unix(), now.Add(-48*time.Hour).Unix(),
		now.Add(-time.Hour).Unix(), now.Add(time.Hour).Unix())
	testExec(t, s, `INSERT INTO product_features VALUES(1,1,9),(2,0,12),(3,1,6)`)
	testExec(t, s, `INSERT INTO promotion_events(id,product_id,promotion_id,action,name,details,created) VALUES
 (201,1,101,'edit','Saved manager apples','Keep custom sale price and saved audit','2026-09-30 12:00 UTC'),
 (202,2,NULL,'unfeature','Saved manager spinach','Keep independent featured history','2026-09-30 12:01 UTC'),
 (203,3,104,'cancel','Saved archived pears','Keep cancelled promotion history','2026-09-30 12:02 UTC')`)
	testExec(t, s, `INSERT INTO promotion_commands VALUES('saved-promotion-command','saved-promotion-command-hash'),('saved-feature-command','saved-feature-command-hash')`)
	if err := checkSQLite(s.db); err != nil {
		t.Fatal(err)
	}
}

func promotionRecoveryRows(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	rows := make(map[string][][]any)
	for _, table := range []string{"promotions", "product_features", "promotion_events", "promotion_commands"} {
		rows[table] = migrationQuerySnapshot(t, db, `SELECT * FROM `+table+` ORDER BY rowid`)
	}
	return rows
}

func resetPromotionWeek(now time.Time) (int64, int64) {
	utc := now.UTC()
	midnight := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	monday := midnight.AddDate(0, 0, -(int(midnight.Weekday())+6)%7)
	return monday.Unix(), monday.AddDate(0, 0, 7).Unix()
}

func assertResetPromotionSamples(t *testing.T, s *Store, now time.Time) {
	t.Helper()
	starts, ends := resetPromotionWeek(now)
	wantOffers := [][]any{
		{int64(1), int64(249), starts, ends, int64(1), int64(0)},
		{int64(3), int64(399), starts, ends, int64(1), int64(0)},
		{int64(4), int64(329), starts, ends, int64(1), int64(0)},
	}
	if got := migrationQuerySnapshot(t, s.db, `SELECT product_id,sale_price,starts,ends,version,cancelled FROM promotions ORDER BY product_id`); !reflect.DeepEqual(got, wantOffers) {
		t.Fatalf("explicit reset offers=%v, want only the UTC-week sample offers %v", got, wantOffers)
	}
	wantFeatures := [][]any{{int64(1), int64(1), int64(1)}, {int64(3), int64(1), int64(1)}, {int64(4), int64(1), int64(1)}}
	if got := migrationQuerySnapshot(t, s.db, `SELECT product_id,featured,version FROM product_features ORDER BY product_id`); !reflect.DeepEqual(got, wantFeatures) {
		t.Fatalf("explicit reset features=%v, want fresh sample features %v", got, wantFeatures)
	}
	var invalid int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM promotions o JOIN products p ON p.id=o.product_id WHERE o.sale_price>=p.price OR p.archived<>0 OR p.sale_unit<>'each'`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatalf("reset samples are not eligible real discounts: invalid=%d error=%v", invalid, err)
	}
	wantCommands := [][]any{{"explicit-reset-examples", int64(64)}}
	if got := migrationQuerySnapshot(t, s.db, `SELECT command_key,length(command_hash) FROM promotion_commands ORDER BY command_key`); !reflect.DeepEqual(got, wantCommands) {
		t.Fatalf("fresh reset command keys=%v, want only its new sample command marker %v", got, wantCommands)
	}
}

func TestPromotionsRestartAndExplicitResetPreserveRecoveryHistory(t *testing.T) {
	// This Sunday evening is already Monday in UTC. Reset must use the current
	// UTC week, regardless of the supplied clock's local zone or calendar date.
	now := time.Date(2026, time.October, 4, 20, 30, 0, 0, time.FixedZone("UTC-07", -7*60*60))
	path, _ := populatedPromotionsV6(t, now)
	s := openPromotionMigrationStore(t, path, now)
	populatePromotionRecoveryHistory(t, s, now)
	before := fingerprintTest(t, s.db)
	offersBefore := promotionRecoveryRows(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openPromotionMigrationStore(t, path, now)
	if got := fingerprintTest(t, s.db); got != before {
		t.Fatal("restart changed promotions, featured versions, held stock, receipts or shopper history")
	}
	if len(savedBackups(t, path)) != 0 {
		t.Fatal("ordinary restart created a reset archive")
	}
	result := resetTest(t, s)
	if !result.Reset || result.BackupPath == "" {
		t.Fatalf("explicit reset did not report its archive: %+v", result)
	}
	assertDemoSeed(t, s)
	assertShopperRoster(t, s.db)
	assertResetPromotionSamples(t, s, now)
	for _, table := range []string{"shopper_assignments", "shopper_event_links", "working_order_items", "order_events"} {
		if n := testCount(t, s, table); n != 0 {
			t.Errorf("explicit reset retained %d %s rows", n, table)
		}
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
		t.Fatal("reset archive changed promotions, features, command keys, old receipts or shopper event links")
	}
	if got := promotionRecoveryRows(t, backup); !reflect.DeepEqual(got, offersBefore) {
		t.Fatalf("reset archive lost promotion recovery rows: got=%v want=%v", got, offersBefore)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored-promotions.db")
	restoredDB := migrationRawDB(t, restoredPath)
	if err := copySQLite(restoredDB, backup); err != nil {
		t.Fatal(err)
	}
	if err := restoredDB.Close(); err != nil {
		t.Fatal(err)
	}
	restored := openPromotionMigrationStore(t, restoredPath, now)
	if got := fingerprintTest(t, restored.db); got != before {
		t.Fatal("opening the restored archive rewrote promotion, featured or prior v6 history")
	}
	baseline := fingerprintTest(t, s.db)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Samples expire naturally; a later restart never refreshes or extends the
	// sample week. Only another explicitly requested reset installs new offers.
	reopened := openPromotionMigrationStore(t, path, now.AddDate(0, 0, 14))
	if got := fingerprintTest(t, reopened.db); got != baseline {
		t.Fatal("ordinary restart refreshed reset samples or changed the fresh baseline")
	}
	assertResetPromotionSamples(t, reopened, now)
}
