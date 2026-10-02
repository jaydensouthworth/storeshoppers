package shop

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func messageCommand(t *testing.T, s *Store, id int64, owner Session, body string) MessageCommand {
	t.Helper()
	v, err := s.CustomerMessages(id, owner.ID, MessageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	return MessageCommand{ConversationKey: v.ConversationKey, Key: token(), Body: body, AssignmentID: v.AssignmentID, AssignmentVersion: v.AssignmentVersion}
}
func sendCustomerMessageTest(t *testing.T, s *Store, id int64, owner Session, body string) MessageResult {
	t.Helper()
	out, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, messageCommand(t, s, id, owner, body))
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func messagePriorTables(t *testing.T, s *Store) map[string]priorTableSnapshot {
	t.Helper()
	out := shopperPriorTables(t, s.db)
	delete(out, "order_messages")
	return out
}
func TestOrderMessagesSeparateAuthPlaintextAndNoFulfillmentEffects(t *testing.T) {
	s := newTestStore(t)
	owner, id, g, p := handheldWeightFixture(t, s, 2000, 349, 500)
	review, err := s.PreviewHandheldWeight(g.Token, g.CSRF, handheldWeightInput(t, s, g, 527, ""))
	if err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `UPDATE orders SET attention_reason='PRIVATE_MANAGER_REASON',attention_since=123 WHERE id=?`, id)
	testExec(t, s, `INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details,visibility) VALUES(?,?,'hash','report','PRIVATE_REPORT_REASON','PRIVATE_REPORT_NOTE','internal')`, id, token())
	before := messagePriorTables(t, s)
	c := messageCommand(t, s, id, owner, "  Hi 👋\r\n<script>alert('text')</script>  ")
	customer, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c)
	if err != nil {
		t.Fatal(err)
	}
	if customer.Replayed || customer.Message.Revision != 1 || customer.Message.Actor != "customer" || customer.Message.SenderName != "Demo customer" || customer.Message.Body != "Hi 👋\n<script>alert('text')</script>" {
		t.Fatal(customer)
	}
	c.Key = token()
	c.Body = "I can check the shelf"
	worker, err := s.SendHandheldMessage(g.Token, g.CSRF, c)
	if err != nil {
		t.Fatal(err)
	}
	if worker.Message.Revision != 2 || worker.Message.Actor != "worker" || worker.Message.SenderName != "Avery Morgan" || worker.Message.ShopperID != 1 {
		t.Fatal(worker)
	}
	a, err := s.CustomerMessages(id, owner.ID, MessageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.HandheldMessages(g.Token, MessageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if !a.CanSend || a.ConversationKey != b.ConversationKey || a.CSRF != owner.CSRF || b.CSRF != g.CSRF || !reflect.DeepEqual(a.Messages, b.Messages) || a.ContextVersion != review.Command.Version || a.Revision != 2 || a.Cursor != 2 || a.HasOlder || a.HasMore {
		t.Fatalf("customer=%+v phone=%+v", a, b)
	}
	for _, secret := range []string{owner.ID, g.Token, "PRIVATE_MANAGER_REASON", "PRIVATE_REPORT_REASON", "PRIVATE_REPORT_NOTE"} {
		if strings.Contains(fmt.Sprintf("%+v %+v", a, b), secret) {
			t.Fatal("private projection", secret)
		}
	}
	assertShopperPriorTables(t, s.db, before)
	// Messaging does not invalidate an explicit weight review that was open.
	if _, err = s.ConfirmHandheldWeight(g.Token, g.CSRF, review.Command); err != nil {
		t.Fatal("message invalidated scale review", err)
	}
	if testProduct(t, s, p.ID).Stock != 1473 {
		t.Fatal("unexpected weighed stock")
	}
}
func TestOrderMessagesAuthBeforeValidationAndOwnSenderLookup(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	stranger := testSession(t, s, "")
	testExec(t, s, `UPDATE sessions SET manager_until=? WHERE id=?`, s.now().Add(time.Hour).Unix(), stranger.ID)
	c := messageCommand(t, s, id, owner, "customer text")
	saved, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c)
	if err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	badQuery := MessageQuery{After: -1, Limit: 100}
	for _, sid := range []string{"", stranger.ID, g.Token} {
		if _, err = s.CustomerMessages(id, sid, badQuery); !errors.Is(err, ErrNotFound) {
			t.Fatal("scope validation leak", err)
		}
		if _, err = s.SendCustomerMessage(id, sid, "", MessageCommand{}); !errors.Is(err, ErrNotFound) {
			t.Fatal("send scope", err)
		}
		if _, err = s.FindCustomerMessage(id, sid, "", ""); !errors.Is(err, ErrNotFound) {
			t.Fatal("lookup scope", err)
		}
	}
	for _, raw := range []string{"", owner.ID, stranger.ID} {
		if _, err = s.HandheldMessages(raw, badQuery); !errors.Is(err, ErrHandheldAccess) {
			t.Fatal(err)
		}
		if _, err = s.SendHandheldMessage(raw, "", MessageCommand{}); !errors.Is(err, ErrHandheldAccess) {
			t.Fatal(err)
		}
		if _, err = s.FindHandheldMessage(raw, "", ""); !errors.Is(err, ErrHandheldAccess) {
			t.Fatal(err)
		}
	}
	if _, err = s.SendCustomerMessage(id, owner.ID, g.CSRF, c); !errors.Is(err, ErrMessageCSRF) {
		t.Fatal(err)
	}
	if _, err = s.SendHandheldMessage(g.Token, owner.CSRF, c); !errors.Is(err, ErrHandheldCSRF) {
		t.Fatal(err)
	}
	found, err := s.FindCustomerMessage(id, owner.ID, c.ConversationKey, c.Key)
	if err != nil || found == nil || !reflect.DeepEqual(*found, saved.Message) {
		t.Fatal(found, err)
	}
	if found, err = s.FindHandheldMessage(g.Token, c.ConversationKey, c.Key); err != nil || found != nil {
		t.Fatal("other sender lookup", found, err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("read/reject changed state")
	}
	// Same key is valid in the independently authenticated worker namespace.
	c.Body = "worker text"
	if _, err = s.SendHandheldMessage(g.Token, g.CSRF, c); err != nil {
		t.Fatal(err)
	}
	if testCount(t, s, "order_messages") != 2 {
		t.Fatal("sender namespaces collided")
	}
}
func TestOrderMessagesReplaySnapshotsReassignAndReconnect(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	c := messageCommand(t, s, id, owner, "hello")
	saved, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c)
	if err != nil {
		t.Fatal(err)
	}
	pc := c
	pc.Key = token()
	pc.Body = "old shopper"
	worker, err := s.SendHandheldMessage(g.Token, g.CSRF, pc)
	if err != nil {
		t.Fatal(err)
	}
	// Renaming a roster profile cannot rewrite the assignment or sent snapshot.
	testExec(t, s, `UPDATE shopper_roster_profiles SET name='Renamed simulated shopper',version=version+1 WHERE shopper_id=1`)
	o := testOrder(t, s, id, owner.ID)
	if err = s.AssignShopper(id, owner.ID, false, ShopperCommand{Action: "reassign", Key: token(), Reason: "Different simulated task owner", Version: o.Version, AssignmentVersion: o.Assignment.Version, ShopperID: 2}); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	replay, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(saved.Message, replay.Message) {
		t.Fatal("replay should precede stale assignment", replay, err)
	}
	for _, mutate := range []func(*MessageCommand){
		func(c *MessageCommand) { c.Body = "invalid\x00" },
		func(c *MessageCommand) { c.AssignmentID = 0 },
		func(c *MessageCommand) { c.AssignmentVersion = 0 },
	} {
		bad := c
		mutate(&bad)
		if _, err = s.SendCustomerMessage(id, owner.ID, owner.CSRF, bad); !errors.Is(err, ErrConflict) {
			t.Fatal("changed consumed payload not a conflict", err)
		}
	}
	changed := c
	changed.Body = "changed"
	if _, err = s.SendCustomerMessage(id, owner.ID, owner.CSRF, changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	changed = c
	changed.Key = token()
	if _, err = s.SendCustomerMessage(id, owner.ID, owner.CSRF, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("stale new message", err)
	}
	if _, err = s.SendHandheldMessage(g.Token, g.CSRF, pc); !errors.Is(err, ErrHandheldAccess) {
		t.Fatal("revoked original replay", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("rejected/replayed commands mutated state")
	}
	next := connectHandheldFixture(t, s, owner, id)
	v, err := s.HandheldMessages(next.Token, MessageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if v.AssignmentName != "Jordan Lee" || v.Messages[1].SenderName != "Avery Morgan" || v.Messages[1].AssignmentVersion != worker.Message.AssignmentVersion {
		t.Fatal(v)
	}
	nc := messageCommand(t, s, id, owner, "new shopper")
	nc.Key = pc.Key
	if m, err := s.FindHandheldMessage(next.Token, nc.ConversationKey, pc.Key); err != nil || m != nil {
		t.Fatal("new grant read former sender lookup", m, err)
	}
	result, err := s.SendHandheldMessage(next.Token, next.CSRF, nc)
	if err != nil || result.Message.SenderName != "Jordan Lee" || result.Message.Revision != 3 {
		t.Fatal(result, err)
	}
}
func TestOrderMessagesClosedHistoryAndExpiry(t *testing.T) {
	for _, state := range []string{"Ready", "Completed", "unassigned", "revoked", "grant-expired", "owner-expired"} {
		t.Run(state, func(t *testing.T) {
			s, clock, _ := newReservationStore(t)
			owner, id, g := handheldFixture(t, s)
			c := messageCommand(t, s, id, owner, "retained history")
			if _, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "unassigned":
				testExec(t, s, `UPDATE shopper_assignments SET state='cancelled',ended='2026-10-02 19:00 UTC',version=version+1 WHERE order_id=?`, id)
			case "revoked":
				testExec(t, s, `UPDATE handheld_grants SET revoked=1`)
			case "grant-expired":
				clock.at(g.Expires)
			case "owner-expired":
				testExec(t, s, `UPDATE sessions SET expires=? WHERE id=?`, clock.now().Unix(), owner.ID)
			default:
				testExec(t, s, `UPDATE orders SET status=? WHERE id=?`, state, id)
			}
			before := fingerprintTest(t, s.db)
			if _, err := s.HandheldMessages(g.Token, MessageQuery{}); !errors.Is(err, ErrHandheldAccess) {
				t.Fatal("phone closed read", err)
			}
			if _, err := s.SendHandheldMessage(g.Token, g.CSRF, c); !errors.Is(err, ErrHandheldAccess) {
				t.Fatal("phone closed replay", err)
			}
			v, err := s.CustomerMessages(id, owner.ID, MessageQuery{})
			if state == "owner-expired" {
				if !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
				if _, err = s.SendCustomerMessage(id, owner.ID, owner.CSRF, c); !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
			} else {
				if err != nil || len(v.Messages) != 1 {
					t.Fatal(v, err)
				}
				if state == "revoked" || state == "grant-expired" {
					if !v.CanSend {
						t.Fatal(v)
					}
				} else {
					if v.CanSend || v.ReadOnlyReason == "" {
						t.Fatal(v)
					}
					if _, err = s.SendCustomerMessage(id, owner.ID, owner.CSRF, c); !errors.Is(err, ErrMessageClosed) {
						t.Fatal("closed replay", err)
					}
				}
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("read/auth mutated state")
			}
		})
	}
}
func TestOrderMessagesUnicodeValidationAndAppendOnly(t *testing.T) {
	s := newTestStore(t)
	owner, id, _ := handheldFixture(t, s)
	for _, body := range []string{"", " \n ", "bad\x00", "bad\r", "bad\t", "\x1fhi", "bad\u0085", "\xff", strings.Repeat("a", 501), strings.Repeat("😀", 501)} {
		before := fingerprintTest(t, s.db)
		if _, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, messageCommand(t, s, id, owner, body)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("body=%q err=%v", body, err)
		}
		if fingerprintTest(t, s.db) != before {
			t.Fatal("invalid body changed state")
		}
	}
	for _, body := range []string{strings.Repeat("a", 500), strings.Repeat("😀", 500), "a\r\nb", "a\nb"} {
		sendCustomerMessageTest(t, s, id, owner, body)
	}
	c := messageCommand(t, s, id, owner, "valid")
	for _, key := range []string{"short", strings.Repeat("a", 101), "123456789012345\x00", "123456789012345 ", "123456789012345😀", "123456789012345é", "123456789012345.", "123456789012345:", "123456789012345/", "123456789012345\r\n"} {
		c.Key = key
		if _, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid key", err)
		}
	}
	for _, q := range []MessageQuery{{After: -1}, {Before: -1}, {After: 1, Before: 1}, {Limit: -1}, {Limit: 51}} {
		if _, err := s.CustomerMessages(id, owner.ID, q); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid query", q, err)
		}
	}
	before := fingerprintTest(t, s.db)
	for _, sql := range []string{`UPDATE order_messages SET body='edited'`, `DELETE FROM order_messages`, `INSERT OR REPLACE INTO order_messages SELECT * FROM order_messages`, `INSERT OR REPLACE INTO order_messages SELECT order_id,999,actor,customer_session_id,grant_id,assignment_id,assignment_version,shopper_id,shopper_name,sender_name,body,created,command_key,command_hash FROM order_messages WHERE revision=1`} {
		if _, err := s.db.Exec(sql); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatal("mutable history", err)
		}
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("history changed")
	}
}
func seedOrderMessages(t *testing.T, s *Store, id int64, owner Session, count int, body string, created int64) {
	t.Helper()
	o := testOrder(t, s, id, owner.ID)
	var offset int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(revision),0) FROM order_messages WHERE order_id=?`, id).Scan(&offset); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for n := 1; n <= count; n++ {
		_, err = tx.Exec(`INSERT INTO order_messages(order_id,revision,actor,customer_session_id,assignment_id,assignment_version,shopper_id,shopper_name,sender_name,body,created,command_key,command_hash) VALUES(?,?,'customer',?,?,?,?,?,'Demo customer',?,?,?,?)`, id, offset+int64(n), owner.ID, o.Assignment.ID, o.Assignment.Version, o.Assignment.ShopperID, o.Assignment.ShopperName, body, created, fmt.Sprintf("seed-message-command-%06d", offset+int64(n)), strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func TestOrderMessagesPaginationNoSkippedCursor(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	seedOrderMessages(t, s, id, owner, 123, "historical message", s.now().Unix()-61)
	v, err := s.CustomerMessages(id, owner.ID, MessageQuery{})
	if err != nil || len(v.Messages) != 50 || v.Revision != 123 || v.OldestRevision != 74 || v.Cursor != 123 || !v.HasOlder || v.HasMore {
		t.Fatal(v, err)
	}
	v, err = s.HandheldMessages(g.Token, MessageQuery{Before: v.OldestRevision})
	if err != nil || v.OldestRevision != 24 || v.Cursor != 73 || !v.HasOlder || !v.HasMore {
		t.Fatal(v, err)
	}
	v, err = s.CustomerMessages(id, owner.ID, MessageQuery{Before: v.OldestRevision})
	if err != nil || len(v.Messages) != 23 || v.OldestRevision != 1 || v.Cursor != 23 || v.HasOlder || !v.HasMore {
		t.Fatal(v, err)
	}
	// An incremental client must advance only to the last delivered row, never
	// the stream's current revision when the page omits subsequent messages.
	cursor := int64(1)
	for cursor < 123 {
		v, err = s.CustomerMessages(id, owner.ID, MessageQuery{After: cursor, Limit: 17})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range v.Messages {
			if m.Revision != cursor+1 {
				t.Fatal("gap", cursor, m)
			}
			cursor = m.Revision
		}
		if v.Cursor != cursor {
			t.Fatal("skipped cursor", v)
		}
	}
	v, err = s.CustomerMessages(id, owner.ID, MessageQuery{After: 123})
	if err != nil || len(v.Messages) != 0 || v.Cursor != 0 || v.Revision != 123 {
		t.Fatal(v, err)
	}
	// Gaps can arise in imported data; booleans use actual existence, not +/-1.
	owner2, id2, _ := handheldFixture(t, s)
	c := messageCommand(t, s, id2, owner2, "first")
	if _, err = s.SendCustomerMessage(id2, owner2.ID, owner2.CSRF, c); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `INSERT INTO order_messages SELECT order_id,10,actor,customer_session_id,grant_id,assignment_id,assignment_version,shopper_id,shopper_name,sender_name,body,created,'gap-message-command',command_hash FROM order_messages WHERE order_id=?`, id2)
	v, err = s.CustomerMessages(id2, owner2.ID, MessageQuery{After: 1, Limit: 1})
	if err != nil || len(v.Messages) != 1 || v.Cursor != 10 || !v.HasOlder || v.HasMore {
		t.Fatal(v, err)
	}
}
func TestOrderMessagesRatesCapsAndReplayCostsNothing(t *testing.T) {
	t.Run("actor and order rate", func(t *testing.T) {
		s, clock, _ := newReservationStore(t)
		owner, id, g := handheldFixture(t, s)
		c := messageCommand(t, s, id, owner, "first")
		first, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < 20; i++ {
			sendCustomerMessageTest(t, s, id, owner, "customer")
		}
		if _, err = s.SendCustomerMessage(id, owner.ID, owner.CSRF, messageCommand(t, s, id, owner, "rate")); !errors.Is(err, ErrMessageRate) {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			pc := messageCommand(t, s, id, owner, "worker")
			if _, err = s.SendHandheldMessage(g.Token, g.CSRF, pc); err != nil {
				t.Fatal(err)
			}
		}
		before := fingerprintTest(t, s.db)
		replay, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c)
		if err != nil || !replay.Replayed || !reflect.DeepEqual(first.Message, replay.Message) || fingerprintTest(t, s.db) != before {
			t.Fatal("replay consumed rate", replay, err)
		}
		// A new grant still shares the worker role's order limit.
		g = connectHandheldFixture(t, s, owner, id)
		if _, err = s.SendHandheldMessage(g.Token, g.CSRF, messageCommand(t, s, id, owner, "new phone")); !errors.Is(err, ErrMessageRate) {
			t.Fatal("grant rotated through quota", err)
		}
		clock.at(clock.now().Add(time.Minute).Unix())
		sendCustomerMessageTest(t, s, id, owner, "new window")
	})
	for _, tc := range []struct {
		name  string
		count int
		body  string
	}{{"order count", 200, "small"}, {"order bytes", 131, strings.Repeat("😀", 500)}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			owner, id, _ := handheldFixture(t, s)
			seedOrderMessages(t, s, id, owner, tc.count, tc.body, s.now().Unix()-61)
			before := fingerprintTest(t, s.db)
			body := "a"
			if tc.name == "order bytes" {
				body = strings.Repeat("😀", 500)
			}
			if _, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, messageCommand(t, s, id, owner, body)); !errors.Is(err, ErrMessageLimit) {
				t.Fatal(err)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("cap purged or retained failed row")
			}
		})
	}
	t.Run("global rate", func(t *testing.T) {
		s := newTestStore(t)
		var owner Session
		var id int64
		for i := 0; i < 4; i++ {
			owner, id, _ = handheldFixture(t, s)
			if i < 3 {
				seedOrderMessages(t, s, id, owner, 40, "rate fixture", s.now().Unix())
			}
		}
		if _, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, messageCommand(t, s, id, owner, "global rate")); !errors.Is(err, ErrMessageRate) {
			t.Fatal(err)
		}
	})
	for _, tc := range []struct {
		name          string
		orders, count int
		body          string
	}{{"global count", 50, 200, "small"}, {"global bytes", 42, 100, strings.Repeat("😀", 500)}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			var owner Session
			var id int64
			testExec(t, s, `UPDATE products SET stock=10000 WHERE sale_unit='each'`)
			for i := 0; i < tc.orders; i++ {
				owner, id, _ = handheldFixture(t, s)
				seedOrderMessages(t, s, id, owner, tc.count, tc.body, s.now().Unix()-61)
			}
			owner, id, _ = handheldFixture(t, s)
			before := testCount(t, s, "order_messages")
			v, err := s.CustomerMessages(id, owner.ID, MessageQuery{})
			if err != nil || v.CanSend || v.ReadOnlyReason != ErrMessageLimit.Error() {
				t.Fatal("global cap projection advertised sending", v, err)
			}
			if _, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, messageCommand(t, s, id, owner, "cap")); !errors.Is(err, ErrMessageLimit) {
				t.Fatal(err)
			}
			if testCount(t, s, "order_messages") != before {
				t.Fatal("failed global cap retained row")
			}
		})
	}
}
func TestOrderMessagesConcurrentReplayRestartAndRollback(t *testing.T) {
	s, clock, path := newReservationStore(t)
	owner, id, g := handheldFixture(t, s)
	other := reservationStore(t, path, clock)
	c := messageCommand(t, s, id, owner, "exact concurrent command")
	start := make(chan struct{})
	results := make(chan MessageResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, store := range []*Store{s, other} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			<-start
			m, e := store.SendCustomerMessage(id, owner.ID, owner.CSRF, c)
			results <- m
			errs <- e
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	replays := 0
	for m := range results {
		if m.Message.Revision != 1 {
			t.Fatal(m)
		}
		if m.Replayed {
			replays++
		}
	}
	if replays != 1 || testCount(t, s, "order_messages") != 1 {
		t.Fatal("not exactly once", replays)
	}
	before := fingerprintTest(t, s.db)
	reopened := reservationStore(t, path, clock)
	if m, e := reopened.SendCustomerMessage(id, owner.ID, owner.CSRF, c); e != nil || !m.Replayed || fingerprintTest(t, s.db) != before {
		t.Fatal("restart replay", m, e)
	}
	testExec(t, s, `CREATE TRIGGER fail_message BEFORE INSERT ON order_messages BEGIN SELECT RAISE(ABORT,'message fault'); END`)
	before = fingerprintTest(t, s.db)
	c.Key = token()
	if _, e := s.SendHandheldMessage(g.Token, g.CSRF, c); e == nil || fingerprintTest(t, s.db) != before {
		t.Fatal("failed message mutated state", e)
	}
	testExec(t, s, `DROP TRIGGER fail_message`)
	if _, e := s.SendHandheldMessage(g.Token, g.CSRF, c); e != nil {
		t.Fatal("failed command consumed", e)
	}
}
func TestOrderMessagesLifecycleRacesSerialize(t *testing.T) {
	for _, action := range []string{"reassign", "revoke", "ready", "expire"} {
		t.Run(action, func(t *testing.T) {
			s, clock, path := newReservationStore(t)
			owner, id, g := handheldFixture(t, s)
			other := reservationStore(t, path, clock)
			c := messageCommand(t, s, id, owner, "racing lifecycle")
			start := make(chan struct{})
			sent := make(chan error, 1)
			changed := make(chan error, 1)
			go func() { <-start; _, err := s.SendHandheldMessage(g.Token, g.CSRF, c); sent <- err }()
			go func() {
				<-start
				tx, err := other.beginWrite()
				if err != nil {
					changed <- err
					return
				}
				defer tx.Rollback()
				switch action {
				case "reassign":
					_, err = tx.Exec(`UPDATE shopper_assignments SET shopper_id=2,shopper_name='Jordan Lee',version=version+1 WHERE order_id=?`, id)
				case "revoke":
					_, err = tx.Exec(`UPDATE handheld_grants SET revoked=1`)
				case "ready":
					_, err = tx.Exec(`UPDATE orders SET status='Ready' WHERE id=?`, id)
				case "expire":
					_, err = tx.Exec(`UPDATE sessions SET expires=? WHERE id=?`, clock.now().Unix(), owner.ID)
				}
				if err == nil {
					err = tx.Commit()
				}
				changed <- err
			}()
			close(start)
			sendErr, changeErr := <-sent, <-changed
			if changeErr != nil {
				t.Fatal(changeErr)
			}
			count := testCount(t, s, "order_messages")
			if sendErr == nil {
				if count != 1 {
					t.Fatal("success not recorded")
				}
			} else if !errors.Is(sendErr, ErrHandheldAccess) || count != 0 {
				t.Fatal("invalid race result", sendErr, count)
			}
			before := fingerprintTest(t, s.db)
			if _, err := s.SendHandheldMessage(g.Token, g.CSRF, c); !errors.Is(err, ErrHandheldAccess) {
				t.Fatal("lifecycle replay permitted", err)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("stale replay changed state")
			}
		})
	}
}

func TestOrderMessagesHardCapReadOnlyStillAllowsExactReplay(t *testing.T) {
	for _, mode := range []string{"count", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestStore(t)
			owner, id, g := handheldFixture(t, s)
			body := "a"
			if mode == "bytes" {
				body = strings.Repeat("😀", 500)
			}
			c := messageCommand(t, s, id, owner, body)
			saved, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "count" {
				seedOrderMessages(t, s, id, owner, 199, "a", s.now().Unix()-61)
			} else {
				seedOrderMessages(t, s, id, owner, 130, body, s.now().Unix()-61)
				seedOrderMessages(t, s, id, owner, 1, strings.Repeat("a", 144), s.now().Unix()-61)
			}
			before := fingerprintTest(t, s.db)
			for _, read := range []func() (*MessageConversation, error){
				func() (*MessageConversation, error) { return s.CustomerMessages(id, owner.ID, MessageQuery{}) },
				func() (*MessageConversation, error) { return s.HandheldMessages(g.Token, MessageQuery{}) },
			} {
				v, e := read()
				if e != nil || v.CanSend || v.ReadOnlyReason != ErrMessageLimit.Error() {
					t.Fatal("full projection writable", v, e)
				}
			}
			replay, err := s.SendCustomerMessage(id, owner.ID, owner.CSRF, c)
			if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Message, saved.Message) {
				t.Fatal("hard cap blocked exact replay", replay, err)
			}
			c.Key = token()
			c.Body = "a"
			if _, err = s.SendCustomerMessage(id, owner.ID, owner.CSRF, c); !errors.Is(err, ErrMessageLimit) {
				t.Fatal("cap accepted new message", err)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("read/replay/reject changed full history")
			}
		})
	}
}
func TestOrderMessagesRealReadyRaceKeepsFulfillmentVersionIndependent(t *testing.T) {
	for _, phone := range []bool{false, true} {
		t.Run(fmt.Sprint(phone), func(t *testing.T) {
			s, clock, path := newReservationStore(t)
			owner, id, g := handheldFixture(t, s)
			if err := s.AdvanceScoped(id, "Placed", owner.ID, false); err != nil {
				t.Fatal(err)
			}
			for _, line := range testOrder(t, s, id, owner.ID).WorkingItems {
				if err := s.RecordPicked(id, line.ProductID, line.Quantity, line.PickVersion, owner.ID, false); err != nil {
					t.Fatal(err)
				}
			}
			other := reservationStore(t, path, clock)
			c := messageCommand(t, s, id, owner, "checking before collection")
			o := testOrder(t, s, id, owner.ID)
			start := make(chan struct{})
			sent := make(chan error, 1)
			ready := make(chan error, 1)
			go func() {
				<-start
				var err error
				if phone {
					_, err = s.SendHandheldMessage(g.Token, g.CSRF, c)
				} else {
					_, err = s.SendCustomerMessage(id, owner.ID, owner.CSRF, c)
				}
				sent <- err
			}()
			go func() { <-start; ready <- other.AdvanceVersioned(id, "Picking", o.Version, owner.ID, false) }()
			close(start)
			sendErr, readyErr := <-sent, <-ready
			if readyErr != nil {
				t.Fatal("message invalidated pending Ready", readyErr)
			}
			if sendErr != nil && !errors.Is(sendErr, ErrMessageClosed) && !errors.Is(sendErr, ErrHandheldAccess) {
				t.Fatal(sendErr)
			}
			count := testCount(t, s, "order_messages")
			if (sendErr == nil && count != 1) || (sendErr != nil && count != 0) {
				t.Fatal("unexpected append", sendErr, count)
			}
			final := testOrder(t, s, id, owner.ID)
			if final.Status != "Ready" || !final.Finalized || final.Assignment != nil || final.Version != o.Version+1 {
				t.Fatal("message altered fulfillment", final)
			}
			v, err := s.CustomerMessages(id, owner.ID, MessageQuery{})
			if err != nil || v.CanSend || len(v.Messages) != count {
				t.Fatal("closed history", v, err)
			}
		})
	}
}

func TestOrderMessagesCommandKeysAreHeaderSafeASCII(t *testing.T) {
	for _, key := range []string{strings.Repeat("a", 16), strings.Repeat("Z", 100), "0123456789_-aAzZ" + "0"} {
		if !validMessageKey(key) {
			t.Fatalf("valid ASCII key rejected: %q", key)
		}
	}
	for _, key := range []string{strings.Repeat("a", 15), strings.Repeat("a", 101), "123456789012345😀", "123456789012345é", "123456789012345.", "123456789012345:", "123456789012345/", "123456789012345\r\n", "123456789012345\x7f", "123456789012345\xff"} {
		if validMessageKey(key) {
			t.Fatalf("unsafe header key accepted: %q", key)
		}
	}
}
