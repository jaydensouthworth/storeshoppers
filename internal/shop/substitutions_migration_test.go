package shop

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

const substitutionsV16Driver = "substitutions_v16"

func init() {
	sql.Register(substitutionsV16Driver, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		return c.RegisterFunc("app_schema_version", func() int64 { return 16 }, true)
	}})
}

func populatedSubstitutionsV16(t *testing.T) (string, time.Time) {
	t.Helper()
	path, now := populatedEmployeeV15(t)
	s := migrationTestStore(t, path, now)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = applyMigration(tx, employeeStoreMigration); err != nil {
		t.Fatal(err)
	}
	if err = installWriterFences(tx, 16); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO schema_version VALUES(16)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Fill every schema-16 addition, alongside the heavily populated legacy
	// fixture's receipts, allocations, images, taxonomy, sales, carts and audit.
	testExec(t, s, `INSERT INTO order_messages(order_id,revision,actor,customer_session_id,assignment_id,assignment_version,shopper_id,shopper_name,sender_name,body,created,command_key,command_hash) SELECT a.order_id,1,'customer',o.session_id,a.id,a.version,a.shopper_id,a.shopper_name,'Demo customer','Retained customer question',?,'prior-customer-message',? FROM shopper_assignments a JOIN orders o ON o.id=a.order_id WHERE a.state='active' ORDER BY a.id LIMIT 1`, now.Unix(), strings.Repeat("a", 64))
	testExec(t, s, `INSERT INTO employee_sessions(token_hash,csrf,epoch,shopper_id,created,expires) SELECT ?,'retained-employee-csrf',h.epoch,g.shopper_id,?,? FROM handheld_grants g CROSS JOIN handheld_state h ORDER BY g.id LIMIT 1`, strings.Repeat("e", 64), now.Unix(), now.Add(24*time.Hour).Unix())
	testExec(t, s, `INSERT INTO employee_store_orders(order_id,epoch) SELECT a.order_id,h.epoch FROM handheld_grants g JOIN shopper_assignments a ON a.id=g.assignment_id CROSS JOIN handheld_state h ORDER BY g.id LIMIT 1`)
	testExec(t, s, `INSERT INTO employee_grant_links(grant_id,employee_hash) SELECT g.id,e.token_hash FROM handheld_grants g CROSS JOIN employee_sessions e ORDER BY g.id LIMIT 1`)
	testExec(t, s, `INSERT INTO employee_commands(employee_hash,command_key,command_hash,order_id,assignment_id,assignment_version,grant_id,result,created) SELECT e.token_hash,'retained-claim-command',?,a.order_id,a.id,a.version,g.id,'claimed',? FROM handheld_grants g JOIN shopper_assignments a ON a.id=g.assignment_id CROSS JOIN employee_sessions e ORDER BY g.id LIMIT 1`, strings.Repeat("c", 64), now.Unix())
	testExec(t, s, `UPDATE employee_store_state SET seeded=1`)
	s.Close()
	return path, now
}
func TestSubstitutionMigrationPopulatedV16PreservedBackupFailureRetryRestart(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			path, now := populatedSubstitutionsV16(t)
			old := migrationTestStore(t, path, now)
			prior := shopperPriorTables(t, old.db)
			objects := migrationQuerySnapshot(t, old.db, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`)
			if fail {
				testExec(t, old, `CREATE TRIGGER reject_substitution_version BEFORE INSERT ON schema_version WHEN NEW.version=17 BEGIN SELECT RAISE(ABORT,'substitution marker failure'); END`)
				before := fingerprintTest(t, old.db)
				old.Close()
				broken, err := OpenWithClock(path, func() time.Time { return now })
				if err == nil {
					broken.Close()
					t.Fatal("ignored migration failure")
				}
				if !strings.Contains(err.Error(), "substitution marker failure") || !strings.Contains(err.Error(), "pre-migration data retained at") {
					t.Fatal(err)
				}
				old = migrationTestStore(t, path, now)
				if fingerprintTest(t, old.db) != before {
					t.Fatal("failed migration changed populated schema16")
				}
				backups := savedMigrationBackups(t, path)
				if len(backups) != 1 {
					t.Fatal(backups)
				}
				verifyMigrationArchive(t, backups[0], before, 16)
				var partial int
				if err = old.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE 'substitution_%'`).Scan(&partial); err != nil || partial != 0 {
					t.Fatal("partial substitution schema", partial, err)
				}
				testExec(t, old, `DROP TRIGGER reject_substitution_version`)
			}
			before := fingerprintTest(t, old.db)
			old.Close()
			s := openPromotionMigrationStore(t, path, now)
			assertShopperPriorTables(t, s.db, prior)
			if got := migrationQuerySnapshot(t, s.db, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`); !reflect.DeepEqual(got, objects) {
				t.Fatal("extension schema changed")
			}
			if testCount(t, s, "substitution_proposals") != 0 {
				t.Fatal("invented proposals")
			}
			assertForeignKeysEnabled(t, s.db)
			assertWriterFenceCoverage(t, s.db)
			backups := savedMigrationBackups(t, path)
			verifyMigrationArchive(t, backups[len(backups)-1], before, 16)
			fp := fingerprintTest(t, s.db)
			s.Close()
			restarted := openPromotionMigrationStore(t, path, now)
			if fingerprintTest(t, restarted.db) != fp || !reflect.DeepEqual(backups, savedMigrationBackups(t, path)) {
				t.Fatal("restart changed schema, data or archives")
			}
		})
	}
}
func TestSubstitutionMigrationOldPreparedWritersAndFutureAPIsFailClosed(t *testing.T) {
	path, now := populatedSubstitutionsV16(t)
	old, err := sql.Open(substitutionsV16Driver, "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	old.SetMaxOpenConns(1)
	prepared, err := old.Prepare(`UPDATE products SET stock=0 WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	s := openPromotionMigrationStore(t, path, now)
	// Use a new owner/assignment rather than exposing migrated historical work.
	category, err := s.SaveTaxonomy("category", 0, 0, "New migration test products")
	if err != nil {
		t.Fatal(err)
	}
	var productIDs []int64
	for _, name := range []string{"Fresh source", "Fresh replacement"} {
		pid, e := s.SaveProduct(Product{Name: name, Description: "Migration test stock", CategoryID: category, Icon: "apple", Price: 299, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1})
		if e != nil {
			t.Fatal(e)
		}
		testExec(t, s, `UPDATE products SET stock=10 WHERE id=?`, pid)
		productIDs = append(productIDs, pid)
	}
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, productIDs[0], 2)
	id := testCheckout(t, s, owner.ID)
	enrollEmployeeOrderTest(t, s, id)
	employee := employeeFixture(t, s)
	g := employeeClaimTest(t, s, employee, id)
	p, d := substitutionProposalTest(t, s, owner, id, g, productIDs[0], productIDs[1], 1, "restock")
	before := fingerprintTest(t, s.db)
	if _, err = prepared.Exec(); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("writer %d", latestSchemaVersion)) {
		t.Fatal("prepared old writer", err)
	}
	for _, stmt := range []string{`UPDATE employee_store_state SET seeded=0`, `DELETE FROM employee_commands`, `DELETE FROM substitution_proposals`, `UPDATE substitution_proposals SET status='rejected'`, `INSERT INTO substitution_proposals SELECT * FROM substitution_proposals`, fmt.Sprintf(`INSERT INTO schema_version VALUES(%d)`, latestSchemaVersion+1)} {
		if _, err = old.Exec(stmt); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("writer %d", latestSchemaVersion)) {
			t.Fatal("old writer unfenced", stmt, err)
		}
	}
	substitutionAssertUnchanged(t, s, before)
	testExec(t, s, `INSERT INTO schema_version VALUES(?)`, latestSchemaVersion+1)
	before = fingerprintTest(t, s.db)
	for _, check := range []func() error{
		func() error { _, e := s.PreviewSubstitution(g.Token, g.CSRF, p.Preview.Command); return e },
		func() error { _, e := s.SendSubstitution(g.Token, g.CSRF, p.Preview.Command); return e },
		func() error { return s.DecideSubstitution(id, owner.ID, owner.CSRF, d) },
		func() error { return s.WithdrawSubstitution(g.Token, g.CSRF, p.ID, p.Review) },
		func() error { _, e := s.Substitutions(id, owner.ID, "", false); return e },
		func() error { _, e := s.Substitutions(id, "", g.Token, true); return e },
	} {
		if err = check(); !errors.Is(err, ErrSchemaIncompatible) {
			t.Fatal("future schema operation", err)
		}
	}
	substitutionAssertUnchanged(t, s, before)
}
func TestSubstitutionResetArchivesHistoryAndRotatesBindingEvenWithReusedIDs(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	p, d := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
	before := fingerprintTest(t, s.db)
	result := resetTest(t, s)
	archive, err := openBackupReadOnly(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if fingerprintTest(t, archive) != before {
		t.Fatal("reset archive lost proposal or original data")
	}
	if testCount(t, s, "substitution_proposals") != 0 {
		t.Fatal("reset retained old proposals")
	}
	reused := Session{ID: owner.ID, CSRF: token(), CheckoutKey: token(), Revision: 1}
	testExec(t, s, `INSERT INTO sessions(id,csrf,checkout_key,expires) VALUES(?,?,?,?)`, reused.ID, reused.CSRF, reused.CheckoutKey, s.now().Add(24*time.Hour).Unix())
	testExec(t, s, `INSERT INTO baskets(id,owner_session_id) VALUES(?,?)`, token()[:32], reused.ID)
	testCart(t, s, reused.ID, 1, 2)
	testCart(t, s, reused.ID, 2, 1)
	newID := testCheckout(t, s, reused.ID)
	testAssign(t, s, newID, reused.ID, 1)
	next := connectHandheldFixture(t, s, reused, newID)
	newP, newD := substitutionProposalTest(t, s, reused, newID, next, 1, 3, 1, "restock")
	if newID != id || newP.ID != p.ID {
		t.Fatalf("fixture did not reuse numeric identities: %d/%d vs %d/%d", newID, newP.ID, id, p.ID)
	}
	if newD.Binding == d.Binding || newD.Review == d.Review || newP.Epoch == p.Epoch {
		t.Fatal("reset reused approval authority")
	}
	before = fingerprintTest(t, s.db)
	if err = s.DecideSubstitution(newID, reused.ID, reused.CSRF, d); !errors.Is(err, ErrSubstitutionStale) {
		t.Fatal("old customer draft authorized new order", err)
	}
	if _, err = s.SendSubstitution(g.Token, g.CSRF, p.Preview.Command); !errors.Is(err, ErrHandheldAccess) {
		t.Fatal("old shopper draft authorized new order", err)
	}
	if err = s.WithdrawSubstitution(g.Token, g.CSRF, p.ID, p.Review); !errors.Is(err, ErrHandheldAccess) {
		t.Fatal("old shopper withdrew new proposal", err)
	}
	substitutionAssertUnchanged(t, s, before)
	if err = s.DecideSubstitution(newID, reused.ID, reused.CSRF, newD); err != nil {
		t.Fatal("new reset approval failed", err)
	}
}
