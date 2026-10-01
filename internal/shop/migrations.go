package shop

import (
	"database/sql"
	_ "embed"
	"fmt"
)

const latestSchemaVersion = 2

//go:embed migrations/002_picking.sql
var pickingMigration string

// migrate applies the baseline and every additive upgrade under one immediate
// transaction. An interrupted or failed upgrade leaves the prior schema intact.
func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	if version < 2 {
		if err = applyMigration(tx, pickingMigration); err != nil {
			return fmt.Errorf("migration 2: %w", err)
		}
	}
	return tx.Commit()
}
func applyMigration(tx *sql.Tx, script string) error { _, err := tx.Exec(script); return err }
