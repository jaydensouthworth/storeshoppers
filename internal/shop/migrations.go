package shop

import (
	"database/sql"
	_ "embed"
	"fmt"
)

const latestSchemaVersion = 9

//go:embed migrations/002_picking.sql
var pickingMigration string

//go:embed migrations/003_catalog.sql
var catalogMigration string

//go:embed migrations/004_reservations.sql
var reservationsMigration string

//go:embed migrations/005_order_overrides.sql
var orderOverridesMigration string

//go:embed migrations/006_shoppers.sql
var shoppersMigration string

//go:embed migrations/007_promotions.sql
var promotionsMigration string

//go:embed migrations/008_product_details.sql
var productDetailsMigration string

//go:embed migrations/009_product_images.sql
var productImagesMigration string

// migrate applies the baseline and every additive upgrade under one immediate
// transaction. An interrupted or failed upgrade leaves the prior schema intact.
func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Refuse a future database before even running the baseline's IF NOT EXISTS
	// statements. Runtime commands use beginWrite; migration must allow older
	// versions so it intentionally obtains the immediate transaction directly.
	var hasVersion int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_version'`).Scan(&hasVersion); err != nil {
		return err
	}
	if hasVersion != 0 {
		var version int
		if err = tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
			return err
		}
		if version > latestSchemaVersion {
			return fmt.Errorf("%w: database schema %d is newer than supported schema %d", ErrSchemaIncompatible, version, latestSchemaVersion)
		}
	}
	var existing int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='products'`).Scan(&existing); err != nil {
		return err
	}
	if _, err = tx.Exec(schema); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	var version int
	if err = tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if existing == 0 {
		if err = seedOriginal(tx); err != nil {
			return fmt.Errorf("initial seed: %w", err)
		}
	}
	if version < 2 {
		if err = applyMigration(tx, pickingMigration); err != nil {
			return fmt.Errorf("migration 2: %w", err)
		}
	}
	if version < 3 {
		if err = migrateCatalog(tx); err != nil {
			return fmt.Errorf("migration 3: %w", err)
		}
	}
	if version < 4 {
		if err = applyMigration(tx, reservationsMigration); err != nil {
			return fmt.Errorf("migration 4: %w", err)
		}
	}
	if version < 5 {
		if err = applyMigration(tx, orderOverridesMigration); err != nil {
			return fmt.Errorf("migration 5: %w", err)
		}
	}
	if version < 6 {
		if err = applyMigration(tx, shoppersMigration); err != nil {
			return fmt.Errorf("migration 6: %w", err)
		}
	}
	if version < 7 {
		if err = applyMigration(tx, promotionsMigration); err != nil {
			return fmt.Errorf("migration 7: %w", err)
		}
	}
	if version < 8 {
		if err = applyMigration(tx, productDetailsMigration); err != nil {
			return fmt.Errorf("migration 8: %w", err)
		}
		if existing == 0 {
			if err = seedProductDetails(tx); err != nil {
				return fmt.Errorf("product detail seed: %w", err)
			}
		}
	}
	if version < 9 {
		if err = applyMigration(tx, productImagesMigration); err != nil {
			return fmt.Errorf("migration 9: %w", err)
		}
	}
	return tx.Commit()
}
func applyMigration(tx *sql.Tx, script string) error { _, err := tx.Exec(script); return err }

func migrateCatalog(tx *sql.Tx) error {
	expand, err := originalDemoCatalog(tx)
	if err != nil {
		return err
	}
	if err = applyMigration(tx, catalogMigration); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id,category,barcode FROM products ORDER BY id`)
	if err != nil {
		return err
	}
	type legacyProduct struct {
		id                int64
		category, barcode string
	}
	var products []legacyProduct
	for rows.Next() {
		var p legacyProduct
		if err = rows.Scan(&p.id, &p.category, &p.barcode); err != nil {
			rows.Close()
			return err
		}
		products = append(products, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range products {
		cid, e := seedTaxonomy(tx, "category", p.category)
		if e != nil {
			return e
		}
		sku := fmt.Sprintf("SHOPDEMO-%06d", p.id)
		if _, err = tx.Exec(`UPDATE products SET category_id=?,sku=? WHERE id=?`, cid, sku, p.id); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO product_codes(product_id,scheme,raw_value,normalized_value,symbology) VALUES(?,'legacy_placeholder',?,?,'unvalidated')`, p.id, p.barcode, p.barcode); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO product_codes(product_id,scheme,raw_value,normalized_value,symbology) VALUES(?,'demo_local',?,?,'Code128')`, p.id, sku, sku); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`CREATE UNIQUE INDEX products_sku_unique ON products(sku COLLATE NOCASE)`); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE order_items SET sku=(SELECT sku FROM products WHERE id=product_id)`); err != nil {
		return err
	}
	if expand {
		if err = expandDemoCatalog(tx); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT INTO schema_version VALUES(3)`)
	return err
}
