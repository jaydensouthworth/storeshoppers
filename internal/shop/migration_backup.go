package shop

import (
	"database/sql"
	"errors"
)

// Migration archives have a separate fixed budget. Exhaustion stops startup;
// it never expires, overwrites or removes a previous recovery point.
const migrationBackupMaxBytes int64 = 256 << 20

// The caller owns BEGIN IMMEDIATE and has made no content/schema writes. The
// read-only handle sees the original committed state, including WAL contents,
// while SQLite excludes other writers until migration commits or rolls back.
// Do not use Store.db here: migrate owns its sole connection.
func preserveMigrationDatabase(tx *sql.Tx) (path string, err error) {
	var sourcePath string
	if err = tx.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&sourcePath); err != nil {
		return "", err
	}
	if sourcePath == "" {
		return "", nil
	}
	source, err := openBackupReadOnly(sourcePath)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	fingerprint, err := databaseFingerprint(source)
	if err != nil {
		return "", err
	}
	return preserveSQLiteDatabase(source, sourcePath+".migration-backups", fingerprint, migrationBackupMaxBytes, "migration")
}
