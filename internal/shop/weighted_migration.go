package shop

import (
	"database/sql"
	"fmt"
	"strings"
)

func migrateWeighted(tx *sql.Tx) error {
	// Earlier applications could keep stale, unreserved counted requests after
	// a metadata-only unit edit. No pre-weighted gram cart/receipt was a supported
	// weighted sale. Refuse ambiguous data instead of reinterpreting or deleting it.
	var ambiguous int
	if err := tx.QueryRow(`SELECT
 (SELECT COUNT(*) FROM cart c JOIN products p ON p.id=c.product_id WHERE p.sale_unit='g')+
 (SELECT COUNT(*) FROM order_items WHERE sale_unit='g')+
 (SELECT COUNT(*) FROM working_order_items WHERE sale_unit='g')`).Scan(&ambiguous); err != nil {
		return err
	}
	if ambiguous != 0 {
		return fmt.Errorf("%w: pre-weighted gram requests or receipt lines require review; no quantities were converted", ErrSchemaIncompatible)
	}
	// Preserve extension objects too. Even a trigger attached to another table
	// or a view can reference a rebuilt table: SQLite validates these during
	// rename. Remove them transactionally and restore their exact definitions.
	// Autoindices are recreated by the new table constraints.
	rows, err := tx.Query(`SELECT type,name,sql FROM sqlite_schema WHERE sql IS NOT NULL AND
 (type IN('trigger','view') OR (type='index' AND tbl_name IN('products','cart','order_items','working_order_items'))) ORDER BY type,name`)
	if err != nil {
		return err
	}
	type schemaObject struct{ kind, name, statement string }
	var objects []schemaObject
	for rows.Next() {
		var object schemaObject
		if err = rows.Scan(&object.kind, &object.name, &object.statement); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, object)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, object := range objects {
		if object.kind != "index" {
			if _, err = tx.Exec(`DROP ` + strings.ToUpper(object.kind) + ` ` + quoteIdentifier(object.name)); err != nil {
				return fmt.Errorf("prepare migration schema object: %w", err)
			}
		}
	}
	type sequence struct {
		rowid, value int64
		name         string
	}
	rows, err = tx.Query(`SELECT rowid,name,seq FROM sqlite_sequence ORDER BY rowid`)
	if err != nil {
		return err
	}
	var sequences []sequence
	for rows.Next() {
		var seq sequence
		if err = rows.Scan(&seq.rowid, &seq.name, &seq.value); err != nil {
			rows.Close()
			return err
		}
		sequences = append(sequences, seq)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = applyMigration(tx, weightedMigration); err != nil {
		return err
	}
	// Triggers attached to views require their target view to exist at CREATE
	// time. Drop order remains trigger-before-view; restore reverses that
	// dependency after all rebuilt tables and their indices are available.
	for _, kind := range []string{"index", "view", "trigger"} {
		for _, object := range objects {
			if object.kind == kind {
				if _, err = tx.Exec(object.statement); err != nil {
					return fmt.Errorf("restore migration schema object: %w", err)
				}
			}
		}
	}
	// Rebuilding an AUTOINCREMENT table must not reuse an ID from a deleted
	// row. Preserve the sequence table exactly, including unaffected row order.
	if _, err = tx.Exec(`DELETE FROM sqlite_sequence`); err != nil {
		return err
	}
	for _, seq := range sequences {
		if _, err = tx.Exec(`INSERT INTO sqlite_sequence(rowid,name,seq) VALUES(?,?,?)`, seq.rowid, seq.name, seq.value); err != nil {
			return err
		}
	}
	if err = installWriterFences(tx, weightedSchemaVersion); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO schema_version VALUES(?)`, weightedSchemaVersion)
	return err
}

func installWriterFences(tx *sql.Tx, minimum int) error {
	rows, err := tx.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, table := range tables {
		for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
			name := fmt.Sprintf("app_writer_v%d_%s_%s", minimum, table, strings.ToLower(operation))
			statement := fmt.Sprintf(`CREATE TRIGGER %s BEFORE %s ON %s
 WHEN COALESCE(app_schema_version(),0)<%d
 BEGIN SELECT RAISE(ABORT,'schema requires writer %d'); END`, quoteIdentifier(name), operation, quoteIdentifier(table), minimum, minimum)
			if _, err = tx.Exec(statement); err != nil {
				return fmt.Errorf("install writer fence on %s: %w", table, err)
			}
		}
	}
	return nil
}
