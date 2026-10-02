package shop

import (
	"database/sql"

	"github.com/mattn/go-sqlite3"
)

const applicationSQLiteDriver = "shopper_sqlite3"

func init() {
	sql.Register(applicationSQLiteDriver, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			// This is the immutable COMPILED version, never the database marker.
			// Missing callbacks reject old unguarded connections at preparation;
			// callbacks compiled for older schemas fail the latest writer fence.
			return conn.RegisterFunc("app_schema_version", func() int64 {
				return int64(latestSchemaVersion)
			}, true)
		},
	})
}
