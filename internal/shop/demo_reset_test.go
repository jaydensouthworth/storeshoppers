package shop

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fingerprintTest(t *testing.T, db *sql.DB) string {
	t.Helper()
	value, err := databaseFingerprint(db)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func savedBackups(t *testing.T, path string) []string {
	t.Helper()
	paths, err := filepath.Glob(path + ".demo-backups/*.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	return paths
}
func assertDemoSeed(t *testing.T, s *Store) {
	t.Helper()
	if n := testCount(t, s, "products"); n != 72 {
		t.Fatalf("products = %d, want 72", n)
	}
	p := testProduct(t, s, 1)
	if p.Name != "Honeycrisp apples" || p.Price != 349 || p.Stock != 24 {
		t.Fatalf("wrong seed: %+v", p)
	}
	for _, table := range []string{"sessions", "baskets", "cart", "orders", "order_items", "adjustments", "basket_events"} {
		if n := testCount(t, s, table); n != 0 {
			t.Fatalf("%s retains %d rows", table, n)
		}
	}
	if err := checkSQLite(s.db); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA locking_mode").Scan(&mode); err != nil || mode != "normal" {
		t.Fatalf("ownership not released: %s %v", mode, err)
	}
}
func resetTest(t *testing.T, s *Store) DemoResetResult {
	t.Helper()
	result, err := s.ResetDemo(DemoResetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestDemoOrdinaryRestartsRetainData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s := openTestStore(t, path)
	session := testSession(t, s, "")
	testCart(t, s, session.ID, 1, 2)
	order := testCheckout(t, s, session.ID)
	testCart(t, s, session.ID, 2, 3)
	testExec(t, s, `UPDATE products SET name='Retained across restarts',price=777 WHERE id=1`)
	before := fingerprintTest(t, s.db)
	s.Close()
	for range 3 {
		s = openTestStore(t, path)
		if fingerprintTest(t, s.db) != before {
			t.Fatal("ordinary restart changed state")
		}
		if testOrder(t, s, order, session.ID).Total != 698 {
			t.Fatal("order lost")
		}
		if testBasket(t, s, session.ID).Count != 3 {
			t.Fatal("basket lost")
		}
		s.Close()
	}
	if len(savedBackups(t, path)) != 0 {
		t.Fatal("ordinary startup created reset archive")
	}
}

// Deliberately exit without Close so committed fixture data remains in WAL.
func TestDemoCrashFixtureHelper(t *testing.T) {
	path := os.Getenv("SHOP_DEMO_CRASH_TEST_PATH")
	if path == "" {
		return
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	testExec(t, s, "PRAGMA wal_autocheckpoint=0")
	session := testSession(t, s, "")
	testCart(t, s, session.ID, 1, 2)
	testCheckout(t, s, session.ID)
	testCart(t, s, session.ID, 2, 3)
	testExec(t, s, `UPDATE products SET name='Preserve WAL catalog',price=777 WHERE id=1`)
	testExec(t, s, `CREATE TABLE custom_recovery_data(id INTEGER PRIMARY KEY,note TEXT NOT NULL)`)
	testExec(t, s, `INSERT INTO custom_recovery_data VALUES(1,'Preserve extension data too')`)
	os.Exit(0)
}
func TestDemoCrashWALBackupIsCompleteAndRestorable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	command := exec.Command(os.Args[0], "-test.run=^TestDemoCrashFixtureHelper$")
	command.Env = append(os.Environ(), "SHOP_DEMO_CRASH_TEST_PATH="+path)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture %v %s", err, out)
	}
	info, err := os.Stat(path + "-wal")
	if err != nil || info.Size() == 0 {
		t.Fatalf("no committed WAL: %v", err)
	}
	s := openTestStore(t, path)
	result := resetTest(t, s)
	assertDemoSeed(t, s)
	backup, err := openBackupReadOnly(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err = checkSQLite(backup); err != nil {
		t.Fatal(err)
	}
	var name string
	if err = backup.QueryRow(`SELECT name FROM products WHERE id=1`).Scan(&name); err != nil || name != "Preserve WAL catalog" {
		t.Fatalf("WAL missing: %s %v", name, err)
	}
	for table, want := range map[string]int{"orders": 1, "sessions": 1, "baskets": 1, "cart": 1, "order_items": 1, "custom_recovery_data": 1} {
		var n int
		if err = backup.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != want {
			t.Fatalf("backup %s=%d: %v", table, n, err)
		}
	}
	var stock, reserved int
	backup.QueryRow(`SELECT stock FROM products WHERE id=2`).Scan(&stock)
	backup.QueryRow(`SELECT reserved FROM cart WHERE product_id=2`).Scan(&reserved)
	if stock != 15 || reserved != 3 {
		t.Fatalf("inconsistent hold backup %d/%d", stock, reserved)
	}
	info, err = os.Stat(result.BackupPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("archive permissions %v %v", info, err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err = os.Stat(result.BackupPath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("backup depends on %s: %v", suffix, err)
		}
	}
	restoredPath := filepath.Join(t.TempDir(), "restore.db")
	restored, err := sql.Open("sqlite3", restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = copySQLite(restored, backup); err != nil {
		t.Fatal(err)
	}
	restored.Close()
	restoredStore := openTestStore(t, restoredPath)
	if testProduct(t, restoredStore, 1).Name != "Preserve WAL catalog" || testCount(t, restoredStore, "orders") != 1 {
		t.Fatal("restore lost data")
	}
}
func TestDemoOldSchemaBackupPrecedesFreshBaseline(t *testing.T) {
	path, _ := populatedV1(t)
	db := migrationRawDB(t, path)
	testExec(t, &Store{db: db}, "PRAGMA journal_mode=WAL")
	s := &Store{db: db, now: time.Now}
	result := resetTest(t, s)
	assertDemoSeed(t, s)
	backup, err := openBackupReadOnly(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var version, products, orders int
	backup.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version)
	backup.QueryRow(`SELECT COUNT(*) FROM products`).Scan(&products)
	backup.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&orders)
	if version != 1 || products != 2 || orders != 4 {
		t.Fatalf("old state lost: version%d products%d orders%d", version, products, orders)
	}
}
func TestDemoFailedInstallRollsBackAndReusesBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s := openTestStore(t, path)
	testExec(t, s, `UPDATE products SET name='Retain on failure' WHERE id=1`)
	before := fingerprintTest(t, s.db)
	fail := errors.New("injected interrupted install")
	install := func(destination, source *sql.DB) error {
		return copySQLitePages(destination, source, 1, func(done bool) error {
			if done {
				t.Fatal("must interrupt before commit")
			}
			return fail
		})
	}
	var backupPath string
	for range 2 {
		result, err := s.resetDemo(DemoResetOptions{}, install)
		if result.Reset || !errors.Is(err, fail) {
			t.Fatalf("wrong injected failure %v", err)
		}
		if fingerprintTest(t, s.db) != before {
			t.Fatal("failed install changed state")
		}
		backups := savedBackups(t, path)
		if len(backups) != 1 {
			t.Fatalf("duplicate backups %v", backups)
		}
		if backupPath != "" && backupPath != backups[0] {
			t.Fatal("backup replaced")
		}
		backupPath = backups[0]
	}
	info, _ := os.Stat(backupPath)
	result, err := s.ResetDemo(DemoResetOptions{BackupMaxBytes: info.Size()})
	if err != nil || result.BackupPath != backupPath {
		t.Fatalf("retry failed to reuse full-budget backup: %v", err)
	}
	assertDemoSeed(t, s)
}
func TestDemoCapRefusalDoesNotChangeDataOrPurge(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty archive", true: "existing archive"}[prior], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "shop.db")
			s := openTestStore(t, path)
			before := fingerprintTest(t, s.db)
			if prior {
				os.Mkdir(path+".demo-backups", 0700)
				os.WriteFile(filepath.Join(path+".demo-backups", "retain.incomplete"), []byte("keep prior recovery data"), 0600)
			}
			result, err := s.ResetDemo(DemoResetOptions{BackupMaxBytes: 1})
			if result.Reset || err == nil || !strings.Contains(err.Error(), "budget") {
				t.Fatalf("cap not enforced %v", err)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("cap changed original")
			}
			if prior {
				bytes, err := os.ReadFile(filepath.Join(path+".demo-backups", "retain.incomplete"))
				if err != nil || string(bytes) != "keep prior recovery data" {
					t.Fatal("existing recovery data changed")
				}
			}
		})
	}
}
func TestDemoArchiveFailureLeavesOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s := openTestStore(t, path)
	before := fingerprintTest(t, s.db)
	blocker := path + ".demo-backups"
	os.WriteFile(blocker, []byte("unrelated file"), 0600)
	if result, err := s.ResetDemo(DemoResetOptions{}); result.Reset || err == nil {
		t.Fatal("bad archive path did not fail")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("original changed")
	}
	data, err := os.ReadFile(blocker)
	if err != nil || string(data) != "unrelated file" {
		t.Fatal("unrelated file lost")
	}
}
func TestDemoResetRefusesLegacyReadersAndWritersAndReleasesLock(t *testing.T) {
	for _, activity := range []string{"idle", "reader", "writer"} {
		t.Run(activity, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "shop.db")
			s := openTestStore(t, path)
			old, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			if activity == "reader" {
				testExec(t, old, "BEGIN; SELECT count(*) FROM products;")
			}
			if activity == "writer" {
				testExec(t, old, "BEGIN IMMEDIATE; UPDATE products SET name='Pending write' WHERE id=1;")
			}
			result, err := s.ResetDemo(DemoResetOptions{})
			if result.Reset || err == nil || !strings.Contains(err.Error(), "ownership unavailable") {
				t.Fatalf("competing DB not refused: %v", err)
			}
			if activity != "idle" {
				testExec(t, old, "ROLLBACK")
			}
			old.Close()
			resetTest(t, s)
			assertDemoSeed(t, s)
			// Temporary ownership must be released without needing an application restart.
			other, err := Open(path)
			if err != nil {
				t.Fatalf("ownership not released: %v", err)
			}
			other.Close()
		})
	}
}
func TestDemoNonDefaultSQLitePageSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA page_size=8192; CREATE TABLE retain_me(note TEXT); INSERT INTO retain_me VALUES('retained')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s := openTestStore(t, path)
	resetTest(t, s)
	assertDemoSeed(t, s)
	var size int
	if err = s.db.QueryRow("PRAGMA page_size").Scan(&size); err != nil || size != 8192 {
		t.Fatalf("page size %d %v", size, err)
	}
}
