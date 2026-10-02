package shop

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

// DefaultDemoBackupMaxBytes is an archive budget, not a retention policy. No
// existing backup is automatically removed, even when this budget is exhausted.
const DefaultDemoBackupMaxBytes int64 = 256 << 20

type DemoResetOptions struct{ BackupMaxBytes int64 }
type DemoResetResult struct {
	BackupPath string
	Reset      bool
}

// ResetDemo is only for an explicitly confirmed demo-manager action, never
// startup. The caller must exclusively drain its application request gate first.
// SQLite EXCLUSIVE ownership also refuses other binaries/database tools during
// backup and replacement. No database connection is closed or swapped.
func (s *Store) ResetDemo(opts DemoResetOptions) (DemoResetResult, error) {
	return s.resetDemo(opts, copySQLite)
}

func (s *Store) resetDemo(opts DemoResetOptions, install func(*sql.DB, *sql.DB) error) (result DemoResetResult, err error) {
	if opts.BackupMaxBytes == 0 {
		opts.BackupMaxBytes = DefaultDemoBackupMaxBytes
	}
	if opts.BackupMaxBytes < 0 {
		return result, errors.New("DEMO_BACKUP_MAX_BYTES must be positive")
	}
	db := s.db
	var path string
	if err = db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		return result, err
	}
	if path == "" {
		return result, errors.New("demo reset requires a file-backed SQLite database")
	}
	// Open has one live connection in NORMAL WAL mode. Temporarily acquire the
	// actual SQLite ownership lock without dropping the application's handle.
	var synchronous int
	if err = db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		return result, err
	}
	if _, err = db.Exec("PRAGMA busy_timeout=250; PRAGMA synchronous=FULL; PRAGMA locking_mode=EXCLUSIVE"); err != nil {
		return result, err
	}
	defer func() {
		// A failed BEGIN leaves no transaction; ROLLBACK is harmless in that case.
		_, _ = db.Exec("ROLLBACK")
		_, releaseErr := db.Exec(fmt.Sprintf("PRAGMA locking_mode=NORMAL; SELECT count(*) FROM sqlite_schema; PRAGMA busy_timeout=5000; PRAGMA synchronous=%d", synchronous))
		if releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release demo reset ownership: %w", releaseErr))
		}
	}()
	if _, err = db.Exec("BEGIN EXCLUSIVE; COMMIT;"); err != nil {
		return result, fmt.Errorf("demo reset ownership unavailable; another database user must disconnect before retrying: %w", err)
	}
	if err = checkSQLite(db); err != nil {
		return result, fmt.Errorf("demo reset refused: %w", err)
	}
	var version int
	if err = db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return result, err
	}
	if version > latestSchemaVersion {
		return result, fmt.Errorf("demo reset refused: database schema %d is newer than supported schema %d", version, latestSchemaVersion)
	}
	fingerprint, err := databaseFingerprint(db)
	if err != nil {
		return result, err
	}
	result.BackupPath, err = preserveDemoDatabase(db, path+".demo-backups", fingerprint, opts.BackupMaxBytes)
	if err != nil {
		return result, fmt.Errorf("demo reset refused; original data retained: %w", err)
	}
	var pageSize int
	if err = db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return result, err
	}
	fresh, err := freshDemoDatabaseAt(pageSize, s.now)
	if err != nil {
		return result, fmt.Errorf("prepare demo baseline; original data retained: %w", err)
	}
	defer fresh.Close()
	if err = install(db, fresh); err != nil {
		return result, fmt.Errorf("install demo baseline failed; pre-reset state retained at %q: %w", result.BackupPath, err)
	}
	result.Reset = true
	return result, nil
}

func freshDemoDatabase(pageSize int) (*sql.DB, error) { return freshDemoDatabaseAt(pageSize, time.Now) }
func freshDemoDatabaseAt(pageSize int, now func() time.Time) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	failed := true
	defer func() {
		if failed {
			_ = db.Close()
		}
	}()
	if _, err = db.Exec(fmt.Sprintf("PRAGMA page_size=%d", pageSize)); err != nil {
		return nil, err
	}
	fresh := &Store{db: db, now: now}
	if err = fresh.migrate(); err != nil {
		return nil, err
	}
	// Only an explicitly confirmed reset enriches the new fixture.
	if err = fresh.seedExampleSales(); err != nil {
		return nil, err
	}
	if err = checkSQLite(db); err != nil {
		return nil, err
	}
	failed = false
	return db, nil
}

// Include all schema objects and all tables, including sqlite_sequence and
// unknown extension tables. Typed, length-delimited JSON rows and a total-order
// SQL sort make the digest independent of pages, WAL state and insertion order.
func databaseFingerprint(db *sql.DB) (string, error) {
	h := sha256.New()
	rows, err := db.Query(`SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name`)
	if err != nil {
		return "", err
	}
	var tables []string
	for rows.Next() {
		var kind, name, table string
		var statement sql.NullString
		if err = rows.Scan(&kind, &name, &table, &statement); err != nil {
			rows.Close()
			return "", err
		}
		hashValue(h, []any{kind, name, table, statement.String})
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		rows, err = db.Query("SELECT * FROM " + quoted + " LIMIT 0")
		if err != nil {
			return "", err
		}
		columns, e := rows.Columns()
		rows.Close()
		if e != nil {
			return "", e
		}
		var ordering []string
		for i := range columns {
			ordering = append(ordering, fmt.Sprintf("typeof(%s),quote(%s) COLLATE BINARY", quoteIdentifier(columns[i]), quoteIdentifier(columns[i])))
		}
		rows, err = db.Query("SELECT * FROM " + quoted + " ORDER BY " + strings.Join(ordering, ","))
		if err != nil {
			return "", err
		}
		if err = hashValue(h, table); err != nil {
			return "", err
		}
		if err = hashValue(h, columns); err != nil {
			return "", err
		}
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		for rows.Next() {
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
				return "", err
			}
			// []byte JSON encodes differently from string; integers and strings also
			// remain distinct. SQLite has no NaN values (they become NULL).
			typed := make([]any, len(values))
			for i, value := range values {
				typed[i] = []any{fmt.Sprintf("%T", value), value}
			}
			if err = hashValue(h, typed); err != nil {
				rows.Close()
				return "", err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func hashValue(h hash.Hash, value any) error { return json.NewEncoder(h).Encode(value) }

func checkSQLite(db *sql.DB) error {
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite integrity check: %s", result)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("SQLite foreign key check failed")
	}
	return rows.Err()
}

func preserveDemoDatabase(source *sql.DB, directory, fingerprint string, budget int64) (string, error) {
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("demo backup directory must be a private directory (mode 0700), not a symlink")
	}
	// Persist the directory's name too, not just its later contents. Otherwise a
	// newly-created archive directory could disappear after a power failure.
	if err = syncPath(filepath.Dir(directory)); err != nil {
		return "", err
	}
	var used int64
	var matching []string
	err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("demo backup directory contains a symlink; review it manually")
		}
		if info.Mode().IsRegular() {
			if info.Size() > budget-used {
				return fmt.Errorf("demo backup budget %d bytes is exhausted; retain/archive backups elsewhere or explicitly raise DEMO_BACKUP_MAX_BYTES; no backups were removed", budget)
			}
			used += info.Size()
			if strings.HasSuffix(path, "-"+fingerprint+".sqlite3") {
				matching = append(matching, path)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	for _, path := range matching {
		backup, e := openBackupReadOnly(path)
		if e != nil {
			return "", e
		}
		e = checkSQLite(backup)
		var actual string
		if e == nil {
			actual, e = databaseFingerprint(backup)
		}
		_ = backup.Close()
		if e == nil && actual == fingerprint {
			if e = syncPath(path); e != nil {
				return "", e
			}
			if e = syncPath(directory); e != nil {
				return "", e
			}
			return path, nil
		}
		return "", fmt.Errorf("existing matching demo backup failed verification at %q; review it manually", path)
	}
	var pages, pageSize int64
	if err = source.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		return "", err
	}
	if err = source.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return "", err
	}
	if pages > (budget-used)/pageSize {
		return "", fmt.Errorf("demo backup budget exceeded: %d bytes used, %d bytes needed, %d byte cap; retain/archive backups elsewhere or explicitly raise DEMO_BACKUP_MAX_BYTES; no backups were removed", used, pages*pageSize, budget)
	}
	name := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + fingerprint + ".sqlite3"
	final := filepath.Join(directory, name)
	partial := final + ".incomplete"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	// Failed/incomplete files are deliberately retained and count toward budget.
	u := url.URL{Scheme: "file", Path: partial}
	u.RawQuery = "_journal_mode=DELETE&_synchronous=FULL"
	backup, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return "", err
	}
	backup.SetMaxOpenConns(1)
	err = copySQLite(backup, source)
	if err == nil {
		_, err = backup.Exec("PRAGMA journal_mode=DELETE")
	}
	if err == nil {
		err = checkSQLite(backup)
	}
	var actual string
	if err == nil {
		actual, err = databaseFingerprint(backup)
	}
	if err == nil && actual != fingerprint {
		err = errors.New("demo backup fingerprint mismatch")
	}
	err = errors.Join(err, backup.Close())
	if err != nil {
		return "", fmt.Errorf("backup incomplete at %q: %w", partial, err)
	}
	if err = syncPath(partial); err != nil {
		return "", err
	}
	if err = os.Rename(partial, final); err != nil {
		return "", err
	}
	if err = syncPath(directory); err != nil {
		return "", err
	}
	return final, nil
}

func openBackupReadOnly(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	u.RawQuery = "mode=ro&_query_only=on"
	db, err := sql.Open("sqlite3", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func syncPath(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func copySQLite(destination, source *sql.DB) error {
	return copySQLitePages(destination, source, 128, nil)
}

// SQLite's supported Backup API holds a destination write transaction, commits
// only when Step completes, and rolls it back when Finish aborts an incomplete
// copy. No file copying/renaming of the live database or WAL is involved.
func copySQLitePages(destination, source *sql.DB, pages int, afterStep func(bool) error) error {
	ctx := context.Background()
	dest, err := destination.Conn(ctx)
	if err != nil {
		return err
	}
	defer dest.Close()
	src, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	defer src.Close()
	return dest.Raw(func(d any) error {
		return src.Raw(func(s any) error {
			backup, err := d.(*sqlite3.SQLiteConn).Backup("main", s.(*sqlite3.SQLiteConn), "main")
			if err != nil {
				return err
			}
			var stepErr error
			previousRemaining := -1
			for {
				done, e := backup.Step(pages)
				if e != nil {
					stepErr = e
					break
				}
				if afterStep != nil {
					if e = afterStep(done); e != nil {
						stepErr = e
						break
					}
				}
				if done {
					break
				}
				if backup.PageCount() == 0 || backup.Remaining() == backup.PageCount() || backup.Remaining() == previousRemaining {
					stepErr = errors.New("SQLite backup could not obtain exclusive access; reset refused")
					break
				}
				previousRemaining = backup.Remaining()
			}
			return errors.Join(stepErr, backup.Finish())
		})
	})
}
