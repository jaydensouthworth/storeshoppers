package shop

import "database/sql"

// Preserve custom schema objects and old writer fences. Suppress existing
// assignment/history UPDATE triggers during snapshot backfill: a migration is
// not an operational assignment edit and must not invent extension audit rows.
// No table RENAME is used while shoppers is absent, so referencing views and
// cross-table triggers keep their original SQL throughout the rebuild.
func migrateShopperRoster(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT type,name,sql FROM sqlite_schema WHERE sql IS NOT NULL AND ((tbl_name='shoppers' AND type IN ('trigger','index')) OR (type='trigger' AND tbl_name IN ('shopper_assignments','shopper_event_links'))) ORDER BY type,name`)
	if err != nil {
		return err
	}
	type schemaObject struct{ kind, name, statement string }
	var saved []schemaObject
	for rows.Next() {
		var object schemaObject
		if err = rows.Scan(&object.kind, &object.name, &object.statement); err != nil {
			rows.Close()
			return err
		}
		saved = append(saved, object)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, object := range saved {
		if object.kind == "trigger" {
			if _, err = tx.Exec(`DROP TRIGGER ` + quoteIdentifier(object.name)); err != nil {
				return err
			}
		}
	}
	if err = applyMigration(tx, shopperRosterMigration); err != nil {
		return err
	}
	for _, object := range saved {
		if _, err = tx.Exec(object.statement); err != nil {
			return err
		}
	}
	return nil
}
