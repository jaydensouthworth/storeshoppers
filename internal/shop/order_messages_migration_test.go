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

const orderMessagesV14Driver = "order_messages_v14"

func init() {
	sql.Register(orderMessagesV14Driver, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		return c.RegisterFunc("app_schema_version", func() int64 { return 14 }, true)
	}})
}
func populatedMessagesV14(t *testing.T) (string, time.Time) {
	t.Helper()
	path, now := populatedHandheldV13(t)
	s := migrationTestStore(t, path, now)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = applyMigration(tx, handheldMigration); err != nil {
		t.Fatal(err)
	}
	if err = installWriterFences(tx, 14); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO schema_version VALUES(14)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `INSERT INTO handheld_pair_sessions(token_hash,csrf,epoch,expires) SELECT ?,'prior-pair-csrf',epoch,? FROM handheld_state`, strings.Repeat("1", 64), now.Add(time.Hour).Unix())
	testExec(t, s, `INSERT INTO handheld_invitations(secret_hash,epoch,owner_session_id,scope,assignment_id,assignment_version,shopper_id,created,expires,consumed,command_key,command_hash) SELECT ?,h.epoch,o.session_id,'',a.id,a.version,a.shopper_id,?,?,1,'prior-message-invite','prior-command-hash' FROM shopper_assignments a JOIN orders o ON o.id=a.order_id CROSS JOIN handheld_state h WHERE a.state='active' ORDER BY a.id LIMIT 1`, strings.Repeat("2", 64), now.Unix(), now.Add(time.Hour).Unix())
	testExec(t, s, `INSERT INTO handheld_grants(token_hash,csrf,epoch,owner_session_id,scope,assignment_id,assignment_version,shopper_id,invitation_id,created,expires,last_recorded) SELECT ?,'prior-grant-csrf',epoch,owner_session_id,scope,assignment_id,assignment_version,shopper_id,id,created,expires,created FROM handheld_invitations`, strings.Repeat("3", 64))
	testExec(t, s, `INSERT INTO handheld_pair_commands(assignment_id,command_key,command_hash,action,created) SELECT assignment_id,'prior-pair-command','prior-hash','issue',created FROM handheld_invitations`)
	testExec(t, s, `INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details,visibility) SELECT a.order_id,'prior-worker-report','prior-report-hash','report','Saved private manager reason','Saved private shopper note','internal' FROM handheld_grants g JOIN shopper_assignments a ON a.id=g.assignment_id`)
	testExec(t, s, `INSERT INTO order_event_actors(event_id,actor,source,grant_id,assignment_id,shopper_id,shopper_name) SELECT e.id,'worker','manual',g.id,g.assignment_id,g.shopper_id,a.shopper_name FROM order_events e JOIN shopper_assignments a ON a.order_id=e.order_id JOIN handheld_grants g ON g.assignment_id=a.id WHERE e.command_key='prior-worker-report'`)
	s.Close()
	return path, now
}
func TestOrderMessagesMigrationV14BackupRollbackRetryRestart(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			path, now := populatedMessagesV14(t)
			old := migrationTestStore(t, path, now)
			prior := shopperPriorTables(t, old.db)
			objects := migrationQuerySnapshot(t, old.db, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`)
			if fail {
				testExec(t, old, `CREATE TRIGGER reject_messages_version BEFORE INSERT ON schema_version WHEN NEW.version=15 BEGIN SELECT RAISE(ABORT,'message marker failure'); END`)
				before := fingerprintTest(t, old.db)
				old.Close()
				broken, err := OpenWithClock(path, func() time.Time { return now })
				if err == nil {
					broken.Close()
					t.Fatal("ignored migration failure")
				}
				if !strings.Contains(err.Error(), "message marker failure") || !strings.Contains(err.Error(), "pre-migration data retained at") {
					t.Fatal(err)
				}
				old = migrationTestStore(t, path, now)
				if fingerprintTest(t, old.db) != before {
					t.Fatal("failed migration changed populated schema14")
				}
				backups := savedMigrationBackups(t, path)
				if len(backups) != 1 {
					t.Fatal(backups)
				}
				verifyMigrationArchive(t, backups[0], before, 14)
				var partial int
				if err = old.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE 'order_messages%'`).Scan(&partial); err != nil || partial != 0 {
					t.Fatal("partial message objects", partial, err)
				}
				testExec(t, old, `DROP TRIGGER reject_messages_version`)
			}
			before := fingerprintTest(t, old.db)
			old.Close()
			s := openPromotionMigrationStore(t, path, now)
			assertShopperPriorTables(t, s.db, prior)
			if got := migrationQuerySnapshot(t, s.db, `SELECT type,name,sql FROM sqlite_schema WHERE name LIKE 'retained_%' ORDER BY type,name`); !reflect.DeepEqual(got, objects) {
				t.Fatal("extension schema changed")
			}
			if testCount(t, s, "order_messages") != 0 {
				t.Fatal("invented historical messages")
			}
			assertForeignKeysEnabled(t, s.db)
			assertWriterFenceCoverage(t, s.db)
			backups := savedMigrationBackups(t, path)
			verifyMigrationArchive(t, backups[len(backups)-1], before, 14)
			fingerprint := fingerprintTest(t, s.db)
			s.Close()
			restarted := openPromotionMigrationStore(t, path, now)
			if fingerprintTest(t, restarted.db) != fingerprint || !reflect.DeepEqual(backups, savedMigrationBackups(t, path)) {
				t.Fatal("restart changed schema/data/archives")
			}
		})
	}
}
func TestOrderMessagesMigrationOldWriterFenceAndFutureAPIs(t *testing.T) {
	path, now := populatedMessagesV14(t)
	old, err := sql.Open(orderMessagesV14Driver, "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
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
	owner, id, g := handheldFixture(t, s)
	c := messageCommand(t, s, id, owner, "retained")
	if _, err = s.SendCustomerMessage(id, owner.ID, owner.CSRF, c); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	if _, err = prepared.Exec(); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("writer %d", latestSchemaVersion)) {
		t.Fatal("prepared old writer", err)
	}
	for _, statement := range []string{`UPDATE handheld_state SET epoch='old'`, `DELETE FROM handheld_grants`, `DELETE FROM order_messages`, `UPDATE order_messages SET body='old'`, fmt.Sprintf(`INSERT INTO schema_version VALUES(%d)`, latestSchemaVersion+1)} {
		if _, err = old.Exec(statement); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("writer %d", latestSchemaVersion)) {
			t.Fatal("unfenced old writer", statement, err)
		}
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("old writer changed state")
	}
	testExec(t, s, `INSERT INTO schema_version VALUES(?)`, latestSchemaVersion+1)
	for _, check := range []func() error{
		func() error { _, e := s.CustomerMessages(id, owner.ID, MessageQuery{}); return e },
		func() error { _, e := s.HandheldMessages(g.Token, MessageQuery{}); return e },
		func() error { _, e := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c); return e },
		func() error { _, e := s.SendHandheldMessage(g.Token, g.CSRF, c); return e },
		func() error { _, e := s.FindCustomerMessage(id, owner.ID, c.ConversationKey, c.Key); return e },
		func() error { _, e := s.FindHandheldMessage(g.Token, c.ConversationKey, c.Key); return e },
	} {
		if e := check(); !errors.Is(e, ErrSchemaIncompatible) {
			t.Fatal("future API accepted", e)
		}
	}
}
func TestOrderMessagesResetArchivesAndRejectsReusedIDs(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	c := messageCommand(t, s, id, owner, "customer archive")
	if _, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c); err != nil {
		t.Fatal(err)
	}
	pc := c
	pc.Body = "worker archive"
	if _, err := s.SendHandheldMessage(g.Token, g.CSRF, pc); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	result := resetTest(t, s)
	backup, err := openBackupReadOnly(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if fingerprintTest(t, backup) != before {
		t.Fatal("reset archive lost messages")
	}
	if testCount(t, s, "order_messages") != 0 {
		t.Fatal("reset retained messages")
	}
	if _, err = s.CustomerMessages(id, owner.ID, MessageQuery{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("old customer after reset", err)
	}
	if _, err = s.HandheldMessages(g.Token, MessageQuery{}); !errors.Is(err, ErrHandheldAccess) {
		t.Fatal("old worker after reset", err)
	}
	// Recreate even the same numeric order and owner identifier to prove the
	// independently rotated epoch binds drafts, without revealing that owner.
	reused := Session{ID: owner.ID, CSRF: token(), CheckoutKey: token(), Revision: 1}
	testExec(t, s, `INSERT INTO sessions(id,csrf,checkout_key,expires) VALUES(?,?,?,?)`, reused.ID, reused.CSRF, reused.CheckoutKey, s.now().Add(24*time.Hour).Unix())
	testExec(t, s, `INSERT INTO baskets(id,owner_session_id) VALUES(?,?)`, token()[:32], reused.ID)
	testCart(t, s, reused.ID, 1, 1)
	newID := testCheckout(t, s, reused.ID)
	testAssign(t, s, newID, reused.ID, 1)
	if newID != id {
		t.Fatalf("fixture did not reuse order ID: %d %d", id, newID)
	}
	v, err := s.CustomerMessages(newID, reused.ID, MessageQuery{})
	if err != nil || v.ConversationKey == c.ConversationKey || len(v.Messages) != 0 {
		t.Fatal("epoch reused", v, err)
	}
	before = fingerprintTest(t, s.db)
	if _, err = s.SendCustomerMessage(newID, reused.ID, reused.CSRF, c); !errors.Is(err, ErrConflict) {
		t.Fatal("stale reset draft", err)
	}
	if _, err = s.FindCustomerMessage(newID, reused.ID, c.ConversationKey, c.Key); !errors.Is(err, ErrConflict) {
		t.Fatal("stale lookup", err)
	}
	if _, err = s.SendHandheldMessage(g.Token, g.CSRF, pc); !errors.Is(err, ErrHandheldAccess) {
		t.Fatal("stale grant", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("old authority changed reset database")
	}
}
func TestOrderMessagesSchemaIdentityAndSnapshotConstraints(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	saved := sendCustomerMessageTest(t, s, id, owner, "original")
	var grantID int64
	if err := s.db.QueryRow(`SELECT id FROM handheld_grants WHERE token_hash=?`, handheldHash(g.Token)).Scan(&grantID); err != nil {
		t.Fatal(err)
	}
	a := testOrder(t, s, id, owner.ID).Assignment
	for _, tc := range []struct {
		actor                        string
		customer                     any
		grant                        any
		assignment, version, shopper int64
		name, body                   string
		created, revision            int64
	}{
		{"manager", owner.ID, nil, a.ID, a.Version, a.ShopperID, "Demo customer", "text", s.now().Unix(), 2},
		{"customer", nil, nil, a.ID, a.Version, a.ShopperID, "Demo customer", "text", s.now().Unix(), 2},
		{"customer", owner.ID, grantID, a.ID, a.Version, a.ShopperID, "Demo customer", "text", s.now().Unix(), 2},
		{"worker", nil, nil, a.ID, a.Version, a.ShopperID, a.ShopperName, "text", s.now().Unix(), 2},
		{"worker", nil, grantID, a.ID, a.Version, a.ShopperID, "spoofed name", "text", s.now().Unix(), 2},
		{"customer", "missing-session", nil, a.ID, a.Version, a.ShopperID, "Demo customer", "text", s.now().Unix(), 2},
		{"customer", owner.ID, nil, 999999, a.Version, a.ShopperID, "Demo customer", "text", s.now().Unix(), 2},
		{"customer", owner.ID, nil, a.ID, 0, a.ShopperID, "Demo customer", "text", s.now().Unix(), 2},
		{"customer", owner.ID, nil, a.ID, a.Version, 999999, "Demo customer", "text", s.now().Unix(), 2},
		{"customer", owner.ID, nil, a.ID, a.Version, a.ShopperID, "spoofed name", "text", s.now().Unix(), 2},
		{"customer", owner.ID, nil, a.ID, a.Version, a.ShopperID, "Demo customer", "", s.now().Unix(), 2},
		{"customer", owner.ID, nil, a.ID, a.Version, a.ShopperID, "Demo customer", "bad\x00", s.now().Unix(), 2},
		{"customer", owner.ID, nil, a.ID, a.Version, a.ShopperID, "Demo customer", strings.Repeat("a", 501), s.now().Unix(), 2},
		{"customer", owner.ID, nil, a.ID, a.Version, a.ShopperID, "Demo customer", "text", 0, 2},
		{"customer", owner.ID, nil, a.ID, a.Version, a.ShopperID, "Demo customer", "text", s.now().Unix(), 0},
	} {
		if _, err := s.db.Exec(`INSERT INTO order_messages(order_id,revision,actor,customer_session_id,grant_id,assignment_id,assignment_version,shopper_id,shopper_name,sender_name,body,created,command_key,command_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, tc.revision, tc.actor, tc.customer, tc.grant, tc.assignment, tc.version, tc.shopper, a.ShopperName, tc.name, tc.body, tc.created, token(), strings.Repeat("a", 64)); err == nil {
			t.Fatal("invalid schema accepted", tc)
		}
	}
	v, err := s.CustomerMessages(id, owner.ID, MessageQuery{})
	if err != nil || len(v.Messages) != 1 || !reflect.DeepEqual(v.Messages[0], saved.Message) {
		t.Fatal(v, err)
	}
}
