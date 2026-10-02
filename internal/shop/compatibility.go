package shop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrSchemaIncompatible means this binary cannot safely interpret the database.
// It is retryable at the HTTP boundary while deployment replaces the old binary.
var ErrSchemaIncompatible = errors.New("database schema is incompatible with this application version")

type schemaQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkSchemaCompatibility(ctx context.Context, q schemaQuerier) error {
	var version int
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return fmt.Errorf("%w: read schema version: %w", ErrSchemaIncompatible, err)
	}
	if version != latestSchemaVersion {
		return fmt.Errorf("%w: database schema %d, supported schema %d", ErrSchemaIncompatible, version, latestSchemaVersion)
	}
	return nil
}

// CheckCompatibility is a runtime readiness/read check, not a write fence.
// A mutation must use beginWrite so migration cannot commit between its check
// and its writes. HTTP also rechecks after collecting all response data.
func (s *Store) CheckCompatibility(ctx context.Context) error {
	return checkSchemaCompatibility(ctx, s.db)
}

// beginWrite obtains SQLite's writer lock BEFORE checking compatibility. Open
// and freshDemoDatabaseAt configure _txlock=immediate on every connection, so
// Begin executes BEGIN IMMEDIATE and serializes with other process migrations.
// A transaction already in progress completes before migration; one waiting
// behind migration checks its committed version and refuses before any writes.
// Startup migrations deliberately bypass this helper. Reset uses an exact
// version check under its stronger, temporary SQLite EXCLUSIVE ownership.
func (s *Store) beginWrite() (*sql.Tx, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	if err = checkSchemaCompatibility(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}
