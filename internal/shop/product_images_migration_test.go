package shop

import (
	"path/filepath"
	"testing"
	"time"
)

func TestProductImagesPopulatedV8UpgradeRollbackAndRestart(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "rollback retry"}[failure], func(t *testing.T) {
			now := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
			path, _ := populatedProductDetailsV7(t, now)
			db := migrationRawDB(t, path)
			migrationExec(t, db, productDetailsMigration)
			migrationExec(t, db, `INSERT INTO product_details(product_id,kind,body,package_label) VALUES(1,'food','Keep this custom product text','Custom bag')`)
			before := shopperPriorTables(t, db)
			if failure {
				migrationExec(t, db, `CREATE TRIGGER reject_image_version BEFORE INSERT ON schema_version WHEN NEW.version=9 BEGIN SELECT RAISE(ABORT,'image migration interrupted'); END`)
			}
			db.Close()
			if failure {
				if s, err := OpenWithClock(path, func() time.Time { return now }); err == nil {
					s.Close()
					t.Fatal("migration failure accepted")
				}
				db = migrationRawDB(t, path)
				assertShopperPriorTables(t, db, before)
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('products') WHERE name='image_hash'`).Scan(&count); err != nil || count != 0 {
					t.Fatal("failed migration retained column", count, err)
				}
				migrationExec(t, db, `DROP TRIGGER reject_image_version`)
				db.Close()
			}
			s, err := OpenWithClock(path, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			assertShopperPriorTables(t, s.db, before)
			var assigned, assets int
			if err = s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM products WHERE image_hash IS NOT NULL),(SELECT COUNT(*) FROM product_images)`).Scan(&assigned, &assets); err != nil || assigned != 0 || assets != 0 {
				t.Fatal("migration invented image state", assigned, assets, err)
			}
			if err = checkSQLite(s.db); err != nil {
				t.Fatal(err)
			}
			fingerprint := fingerprintTest(t, s.db)
			s.Close()
			s, err = OpenWithClock(path, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if fingerprintTest(t, s.db) != fingerprint {
				t.Fatal("repeat migration/restart changed data")
			}
		})
	}
}

func TestProductImageCorruptionRefusesResetWithoutReplacingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	im, _ := imageFixture(t, 200)
	p := testProduct(t, s, 1)
	if err = s.AttachProductImage(1, p.CatalogVersion, token(), im); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `UPDATE product_images SET master=? WHERE hash=?`, []byte("broken"), im.SHA256)
	before := fingerprintTest(t, s.db)
	if _, err = s.ResetDemo(DemoResetOptions{}); err == nil {
		t.Fatal("corrupt retained image accepted by reset")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("failed reset replaced original data")
	}
}
