package shop

import (
	"database/sql"
	_ "embed"
	"fmt"
)

const latestSchemaVersion = 3

//go:embed migrations/002_picking.sql
var pickingMigration string

//go:embed migrations/003_catalog.sql
var catalogMigration string

// migrate applies the baseline and every additive upgrade under one immediate
// transaction. An interrupted or failed upgrade leaves the prior schema intact.
func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	if version > latestSchemaVersion {
		return fmt.Errorf("database schema %d is newer than supported schema %d", version, latestSchemaVersion)
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
