package shop

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"errors"
	"fmt"
)

const latestSchemaVersion = 12
const weightedSchemaVersion = 10

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

//go:embed migrations/010_weighted.sql
var weightedMigration string

//go:embed migrations/011_attention.sql
var attentionMigration string

//go:embed migrations/012_weighted_promotions.sql
var weightedPromotionsMigration string

// migrate pins the connection so foreign_keys is disabled BEFORE BEGIN
// IMMEDIATE. Rebuilds, data copies, writer fences and version markers commit
// together. An interrupted or failed upgrade leaves the prior schema intact.
func (s *Store) migrate() (err error) {
	return s.migrateWithBackup(preserveMigrationDatabase)
}

func (s *Store) migrateWithBackup(preserve func(*sql.Tx) (string, error)) (err error) {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var trusted int
	if err = conn.QueryRowContext(ctx, `PRAGMA trusted_schema`).Scan(&trusted); err != nil {
		return err
	}
	if trusted != 1 {
		return fmt.Errorf("%w: SQLite writer fence cannot operate with trusted_schema disabled", ErrSchemaIncompatible)
	}
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		_, restoreErr := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		var enabled int
		if restoreErr == nil {
			restoreErr = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled)
			if restoreErr == nil && enabled != 1 {
				restoreErr = errors.New("foreign key enforcement was not restored")
			}
		}
		if restoreErr != nil {
			// Never put a connection without enforcement back in the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, fmt.Errorf("restore SQLite foreign keys: %w", restoreErr))
		}
	}()
	var enabled int
	if err = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return err
	}
	if enabled != 0 {
		return errors.New("disable SQLite foreign keys before migration")
	}
	// All application connections use _txlock=immediate, including reset's
	// fresh database. Pinning preserves that immediate transaction behavior.
	tx, err := conn.BeginTx(ctx, nil)
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
	var priorVersion int
	if hasVersion != 0 {
		if err = tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&priorVersion); err != nil {
			return err
		}
		if priorVersion > latestSchemaVersion {
			return fmt.Errorf("%w: database schema %d is newer than supported schema %d", ErrSchemaIncompatible, priorVersion, latestSchemaVersion)
		}
	}
	// The writer lock prevents other connections changing the committed source
	// while a separate read-only handle captures it. No schema/content writes,
	// including the baseline's IF NOT EXISTS statements, may precede this step.
	var existingObjects int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&existingObjects); err != nil {
		return err
	}
	if existingObjects > 0 && priorVersion < latestSchemaVersion {
		backup, backupErr := preserve(tx)
		if backupErr != nil {
			return fmt.Errorf("migration backup refused; original data retained: %w", backupErr)
		}
		if backup != "" {
			defer func() {
				if err != nil {
					err = fmt.Errorf("upgrade from schema %d failed; pre-migration data retained at %q: %w", priorVersion, backup, err)
				}
			}()
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
	if version < weightedSchemaVersion {
		if err = migrateWeighted(tx); err != nil {
			return fmt.Errorf("migration %d: %w", weightedSchemaVersion, err)
		}
	}
	if version < 11 {
		if err = applyMigration(tx, attentionMigration); err != nil {
			return fmt.Errorf("migration 11: %w", err)
		}
		if err = installWriterFences(tx, 11); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO schema_version VALUES(11)`); err != nil {
			return err
		}
	}
	if version < 12 {
		if err = applyMigration(tx, weightedPromotionsMigration); err != nil {
			return fmt.Errorf("migration 12: %w", err)
		}
		if err = installWriterFences(tx, latestSchemaVersion); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO schema_version VALUES(12)`); err != nil {
			return err
		}
	}
	// Run this inside the transaction so an invalid parent/child relationship
	// rolls back the complete rebuild rather than reporting after commitment.
	rows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	if rows.Next() {
		rows.Close()
		return errors.New("migration foreign key check failed")
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
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
