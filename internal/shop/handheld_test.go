package shop

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func handheldFixture(t *testing.T, s *Store) (Session, int64, HandheldSession) {
	t.Helper()
	owner, id := pickingFixture(t, s)
	testAssign(t, s, id, owner.ID, 1)
	return owner, id, connectHandheldFixture(t, s, owner, id)
}
func connectHandheldFixture(t *testing.T, s *Store, owner Session, id int64) HandheldSession {
	t.Helper()
	o := testOrder(t, s, id, owner.ID)
	pair, err := s.ChangeHandheldPairing(id, owner.ID, false, HandheldPairCommand{Version: o.Version, AssignmentVersion: o.Assignment.Version, Key: token()}, "issue")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := s.HandheldPairSession("")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.RedeemHandheld(bootstrap.Token, bootstrap.CSRF, pair.PairingCode)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}
func handheldTaskTest(t *testing.T, s *Store, g HandheldSession) *HandheldTask {
	t.Helper()
	task, session, err := s.HandheldTask(g.Token)
	if err != nil {
		t.Fatal(err)
	}
	if session.CSRF != g.CSRF || session.Token != "" {
		t.Fatal("session/token projection mismatch")
	}
	return task
}
func handheldCommand(t *testing.T, s *Store, g HandheldSession, line int, picked int64) HandheldPick {
	t.Helper()
	task := handheldTaskTest(t, s, g)
	l := task.Lines[line]
	var code string
	if err := s.db.QueryRow(`SELECT normalized_value FROM product_codes WHERE product_id=? AND scheme='demo_local' AND archived=0`, l.ProductID).Scan(&code); err != nil {
		t.Fatal(err)
	}
	return HandheldPick{HandheldScan: HandheldScan{LineID: l.LineID, AssignmentVersion: task.Assignment.Version, Version: task.Version, PickVersion: l.PickVersion, Code: code, Source: "manual", Format: "Code128"}, Picked: picked, Key: token()}
}
func assertHandheldDenied(t *testing.T, s *Store, g HandheldSession, c HandheldPick) {
	t.Helper()
	if _, _, err := s.HandheldTask(g.Token); !errors.Is(err, ErrHandheldAccess) {
		t.Fatalf("read not denied: %v", err)
	}
	if _, err := s.PreviewHandheldScan(g.Token, g.CSRF, c.HandheldScan); !errors.Is(err, ErrHandheldAccess) {
		t.Fatalf("scan not denied: %v", err)
	}
	if _, err := s.ConfirmHandheldPick(g.Token, g.CSRF, c); !errors.Is(err, ErrHandheldAccess) {
		t.Fatalf("pick/replay not denied: %v", err)
	}
}
func TestHandheldPairingScopeSingleUseExpiryRotationAndNoOrderMutation(t *testing.T) {
	s, clock, _ := newReservationStore(t)
	owner, id := pickingFixture(t, s)
	testAssign(t, s, id, owner.ID, 1)
	other := testSession(t, s, "")
	before := testOrder(t, s, id, owner.ID)
	products, _ := s.Products("", "")
	cmd := HandheldPairCommand{Version: before.Version, AssignmentVersion: before.Assignment.Version, Key: token()}
	if _, err := s.ChangeHandheldPairing(id, other.ID, false, HandheldPairCommand{}, "issue"); !errors.Is(err, ErrNotFound) {
		t.Fatal("scope before validation", err)
	}
	pair, err := s.ChangeHandheldPairing(id, owner.ID, false, cmd, "issue")
	if err != nil {
		t.Fatal(err)
	}
	if pair.ActiveGrant || pair.PairingCode == "" || len(normalizedInvitation(pair.PairingCode)) != 26 || pair.Expires != clock.now().Add(handheldInvitationTTL).Unix() {
		t.Fatal(pair)
	}
	var hash string
	s.db.QueryRow(`SELECT secret_hash FROM handheld_invitations`).Scan(&hash)
	if hash != handheldHash(normalizedInvitation(pair.PairingCode)) || strings.Contains(hash, pair.PairingCode) {
		t.Fatal("not hash-only")
	}
	if _, err = s.ChangeHandheldPairing(id, owner.ID, false, cmd, "issue"); !errors.Is(err, ErrHandheldIssued) {
		t.Fatal("issue replay", err)
	}
	cmd.AssignmentVersion++
	if _, err = s.ChangeHandheldPairing(id, owner.ID, false, cmd, "issue"); !errors.Is(err, ErrConflict) {
		t.Fatal("payload replay", err)
	}
	boot, _ := s.HandheldPairSession("")
	if _, err = s.RedeemHandheld(boot.Token, "wrong", pair.PairingCode); !errors.Is(err, ErrHandheldCSRF) {
		t.Fatal(err)
	}
	grant, err := s.RedeemHandheld(boot.Token, boot.CSRF, strings.ToLower(pair.PairingCode))
	if err != nil {
		t.Fatal(err)
	}
	if grant.Token == boot.Token || grant.CSRF == boot.CSRF || grant.Expires != clock.now().Add(handheldGrantTTL).Unix() {
		t.Fatal("grant did not rotate/bound")
	}
	second, _ := s.HandheldPairSession("")
	if _, err = s.RedeemHandheld(second.Token, second.CSRF, pair.PairingCode); !errors.Is(err, ErrHandheldPairing) {
		t.Fatal("used invitation accepted", err)
	}
	if _, err = s.RedeemHandheld(boot.Token, boot.CSRF, pair.PairingCode); !errors.Is(err, ErrHandheldCSRF) {
		t.Fatal("consumed bootstrap accepted", err)
	}
	if got := testOrder(t, s, id, owner.ID); !reflect.DeepEqual(before, got) {
		t.Fatal("pairing mutated order", got)
	}
	afterProducts, _ := s.Products("", "")
	if !reflect.DeepEqual(products, afterProducts) {
		t.Fatal("pairing changed stock")
	}
	cmd = HandheldPairCommand{Version: before.Version, AssignmentVersion: before.Assignment.Version, Key: token()}
	next, err := s.ChangeHandheldPairing(id, owner.ID, false, cmd, "issue")
	if err != nil {
		t.Fatal(err)
	}
	assertHandheldDenied(t, s, grant, HandheldPick{})
	clock.at(clock.now().Add(handheldInvitationTTL).Unix())
	if _, err = s.RedeemHandheld(second.Token, second.CSRF, next.PairingCode); !errors.Is(err, ErrHandheldPairing) {
		t.Fatal("expired invitation accepted", err)
	}
}
func TestHandheldRecognizeConfirmAbsoluteReplayAndRollback(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	testExec(t, s, `UPDATE orders SET attention_reason='PRIVATE_MANAGER_HOLD',attention_since=1 WHERE id=?`, id)
	before := testOrder(t, s, id, owner.ID)
	stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
	task := handheldTaskTest(t, s, g)
	if !task.Held || strings.Contains(fmt.Sprintf("%+v", task), "PRIVATE_MANAGER_HOLD") {
		t.Fatal("private hold leaked")
	}
	c := handheldCommand(t, s, g, 0, 1)
	fingerprint := fingerprintTest(t, s.db)
	recognition, err := s.PreviewHandheldScan(g.Token, g.CSRF, c.HandheldScan)
	if err != nil || !recognition.CanPick || recognition.CanMeasure || recognition.State != "recognized" {
		t.Fatal(recognition, err)
	}
	if fingerprintTest(t, s.db) != fingerprint {
		t.Fatal("recognition mutated database")
	}
	result, err := s.ConfirmHandheldPick(g.Token, g.CSRF, c)
	if err != nil || result.Version != c.Version+1 || result.PickVersion != c.PickVersion+1 {
		t.Fatal(result, err)
	}
	after := testOrder(t, s, id, owner.ID)
	if after.Status != "Picking" {
		t.Fatal("first confirmation did not start picking")
	}
	if !reflect.DeepEqual(receiptSnapshot(before), receiptSnapshot(after)) {
		t.Fatal("receipt altered")
	}
	if got := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`); !reflect.DeepEqual(stock, got) {
		t.Fatal("counted pick deducted stock")
	}
	fingerprint = fingerprintTest(t, s.db)
	retry, err := s.ConfirmHandheldPick(g.Token, g.CSRF, c)
	if err != nil || !retry.Replayed || retry.Picked != result.Picked || fingerprintTest(t, s.db) != fingerprint {
		t.Fatal("retry not exact", retry, err)
	}
	changed := c
	changed.Picked++
	if _, err = s.ConfirmHandheldPick(g.Token, g.CSRF, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("payload conflict", err)
	}
	changed = c
	changed.Key = token()
	if _, err = s.ConfirmHandheldPick(g.Token, g.CSRF, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("stale command accepted", err)
	}
	var actor, source, name string
	s.db.QueryRow(`SELECT actor,source,shopper_name FROM order_event_actors WHERE actor='worker'`).Scan(&actor, &source, &name)
	if actor != "worker" || source != "manual" || name != "Avery Morgan" {
		t.Fatal("missing attribution", actor, source, name)
	}
	c2 := handheldCommand(t, s, g, 0, 0)
	testExec(t, s, `CREATE TRIGGER reject_worker_actor BEFORE INSERT ON order_event_actors BEGIN SELECT RAISE(ABORT,'test worker audit failure'); END`)
	fingerprint = fingerprintTest(t, s.db)
	if _, err = s.ConfirmHandheldPick(g.Token, g.CSRF, c2); err == nil {
		t.Fatal("ignored actor audit failure")
	}
	if fingerprintTest(t, s.db) != fingerprint {
		t.Fatal("partial mutation after actor audit failure")
	}
	testExec(t, s, `DROP TRIGGER reject_worker_actor`)
	if _, err = s.ConfirmHandheldPick(g.Token, g.CSRF, c2); err != nil {
		t.Fatal(err)
	}
	// The original command's outcome stays stable after later valid correction.
	retry, err = s.ConfirmHandheldPick(g.Token, g.CSRF, c)
	if err != nil || !retry.Replayed || retry.Picked != 1 {
		t.Fatal("outcome changed after later correction", retry, err)
	}
}
func TestHandheldScanRecoveryCasesNeverMutate(t *testing.T) {
	s := newTestStore(t)
	_, _, g := handheldFixture(t, s)
	c := handheldCommand(t, s, g, 0, 1)
	task := handheldTaskTest(t, s, g)
	var otherCode string
	s.db.QueryRow(`SELECT normalized_value FROM product_codes WHERE product_id=? AND scheme='demo_local'`, task.Lines[1].ProductID).Scan(&otherCode)
	for _, tc := range []struct{ name, code, format, state string }{{"other line", otherCode, "Code128", "wrong-item"}, {"not in task", "SHOPDEMO-000003", "Code128", "not-in-task"}, {"unknown", "SHOPDEMO-999999", "Code128", "unknown"}, {"legacy", "123456789012", "Code128", "unknown"}, {"unsupported", c.Code, "EAN13", "unknown"}, {"malformed", "bad", "Code128", "unknown"}} {
		t.Run(tc.name, func(t *testing.T) {
			input := c.HandheldScan
			input.Code = tc.code
			input.Format = tc.format
			before := fingerprintTest(t, s.db)
			recognition, err := s.PreviewHandheldScan(g.Token, g.CSRF, input)
			if err != nil || recognition.State != tc.state || recognition.CanPick || recognition.CanMeasure {
				t.Fatal(recognition, err)
			}
			pick := c
			pick.HandheldScan = input
			if _, err = s.ConfirmHandheldPick(g.Token, g.CSRF, pick); !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
			if before != fingerprintTest(t, s.db) {
				t.Fatal("rejected scan/pick mutated")
			}
		})
	}
	for _, query := range []string{`UPDATE product_codes SET archived=1 WHERE normalized_value=?`, `UPDATE products SET archived=1 WHERE id=(SELECT product_id FROM product_codes WHERE normalized_value=?)`} {
		testExec(t, s, query, c.Code)
		before := fingerprintTest(t, s.db)
		r, err := s.PreviewHandheldScan(g.Token, g.CSRF, c.HandheldScan)
		if err != nil || r.State != "archived" || r.CanPick || r.CanMeasure {
			t.Fatal(r, err)
		}
		if _, err = s.ConfirmHandheldPick(g.Token, g.CSRF, c); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
		if fingerprintTest(t, s.db) != before {
			t.Fatal("archived mutated")
		}
		testExec(t, s, `UPDATE product_codes SET archived=0 WHERE normalized_value=?`, c.Code)
	}
}
func TestHandheldWeighedCodeRequiresExplicitWeight(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	pid := weightedProduct(t, s, 5000, 349, 50).ID
	testCart(t, s, owner.ID, pid, 1000)
	id := testCheckout(t, s, owner.ID)
	testAssign(t, s, id, owner.ID, 1)
	g := connectHandheldFixture(t, s, owner, id)
	c := handheldCommand(t, s, g, 0, 1)
	before := fingerprintTest(t, s.db)
	r, err := s.PreviewHandheldScan(g.Token, g.CSRF, c.HandheldScan)
	if err != nil || r.State != "weight-required" || r.CanPick || !r.CanMeasure {
		t.Fatal(r, err)
	}
	if _, err = s.ConfirmHandheldPick(g.Token, g.CSRF, c); !errors.Is(err, ErrHandheldWeight) {
		t.Fatal(err)
	}
	if before != fingerprintTest(t, s.db) {
		t.Fatal("barcode invented weight")
	}
}
func TestHandheldRevocationPrecedesReplayAndValidation(t *testing.T) {
	for _, mode := range []string{"reassign", "cancel", "ready", "complete", "revoke", "disconnect", "owner-expiry", "grant-expiry", "reset", "epoch"} {
		t.Run(mode, func(t *testing.T) {
			s, clock, _ := newReservationStore(t)
			owner, id, g := handheldFixture(t, s)
			c := handheldCommand(t, s, g, 0, 1)
			if _, err := s.ConfirmHandheldPick(g.Token, g.CSRF, c); err != nil {
				t.Fatal(err)
			}
			o := testOrder(t, s, id, owner.ID)
			switch mode {
			case "reassign", "cancel":
				action := "reassign"
				target := int64(2)
				if mode == "cancel" {
					action = "cancel"
					target = 0
				}
				if err := s.AssignShopper(id, owner.ID, false, ShopperCommand{Action: action, Key: token(), Reason: "Changed task for test", Version: o.Version, AssignmentVersion: o.Assignment.Version, ShopperID: target}); err != nil {
					t.Fatal(err)
				}
			case "ready", "complete":
				for _, line := range o.WorkingItems {
					current := testOrder(t, s, id, owner.ID)
					for _, nowLine := range current.WorkingItems {
						if nowLine.LineID == line.LineID {
							if err := s.MarkWorkingLinePicked(id, line.LineID, line.Quantity, nowLine.PickVersion, current.Version, token(), owner.ID, false); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				if err := s.AdvanceScoped(id, "Picking", owner.ID, false); err != nil {
					t.Fatal(err)
				}
				if mode == "complete" {
					if err := s.AdvanceScoped(id, "Ready", owner.ID, false); err != nil {
						t.Fatal(err)
					}
				}
			case "revoke":
				if _, err := s.ChangeHandheldPairing(id, owner.ID, false, HandheldPairCommand{Version: o.Version, AssignmentVersion: o.Assignment.Version, Key: token()}, "revoke"); err != nil {
					t.Fatal(err)
				}
			case "disconnect":
				if err := s.DisconnectHandheld(g.Token, g.CSRF); err != nil {
					t.Fatal(err)
				}
			case "owner-expiry":
				testExec(t, s, `UPDATE sessions SET expires=? WHERE id=?`, clock.now().Unix(), owner.ID)
			case "grant-expiry":
				clock.at(clock.now().Add(handheldGrantTTL).Unix())
			case "reset":
				resetTest(t, s)
			case "epoch":
				testExec(t, s, `UPDATE handheld_state SET epoch='new epoch' WHERE id=1`)
			}
			before := fingerprintTest(t, s.db)
			assertHandheldDenied(t, s, g, c)
			assertHandheldDenied(t, s, g, HandheldPick{})
			if before != fingerprintTest(t, s.db) {
				t.Fatal("denied/replayed access mutated")
			}
		})
	}
}
func TestHandheldProfileAvailabilityKeepsTaskAndExpiryIsBounded(t *testing.T) {
	s, clock, _ := newReservationStore(t)
	owner, id, g := handheldFixture(t, s)
	for _, availability := range []string{"break", "off_shift", "available"} {
		c := rosterCommand(t, s, owner.ID, 1, "edit")
		c.Availability = availability
		c.Name = "Renamed profile"
		saveRoster(t, s, owner.ID, 1, c)
		task := handheldTaskTest(t, s, g)
		if task.Assignment.ShopperName != "Avery Morgan" {
			t.Fatal("historical actor renamed")
		}
		pick := handheldCommand(t, s, g, 0, 1)
		if _, err := s.ConfirmHandheldPick(g.Token, g.CSRF, pick); err != nil {
			t.Fatal(availability, err)
		}
	}
	expiry := clock.now().Add(20 * time.Minute).Unix()
	testExec(t, s, `UPDATE sessions SET expires=? WHERE id=?`, expiry, owner.ID)
	replacement := connectHandheldFixture(t, s, owner, id)
	if replacement.Expires != expiry {
		t.Fatal("grant exceeds owner expiry")
	}
	clock.at(clock.now().Add(20 * time.Minute).Unix())
	assertHandheldDenied(t, s, replacement, HandheldPick{})
}
func TestHandheldConcurrentRedeemPickAndReassignment(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	testAssign(t, s, id, owner.ID, 1)
	o := testOrder(t, s, id, owner.ID)
	invite, err := s.ChangeHandheldPairing(id, owner.ID, false, HandheldPairCommand{Version: o.Version, AssignmentVersion: o.Assignment.Version, Key: token()}, "issue")
	if err != nil {
		t.Fatal(err)
	}
	boots := make([]HandheldSession, 2)
	for i := range boots {
		boots[i], err = s.HandheldPairSession("")
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	grants := make(chan HandheldSession, 2)
	errs := make(chan error, 2)
	for _, b := range boots {
		wg.Add(1)
		go func(b HandheldSession) {
			defer wg.Done()
			g, e := s.RedeemHandheld(b.Token, b.CSRF, invite.PairingCode)
			if e == nil {
				grants <- g
			}
			errs <- e
		}(b)
	}
	wg.Wait()
	close(grants)
	var g HandheldSession
	for v := range grants {
		if g.Token != "" {
			t.Fatal("two devices redeemed")
		}
		g = v
	}
	if g.Token == "" {
		t.Fatal("no winner")
	}
	c := handheldCommand(t, s, g, 0, 1)
	for i := 0; i < 2; i++ {
		<-errs
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.ConfirmHandheldPick(g.Token, g.CSRF, c); errs <- e }()
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		if e := <-errs; e != nil {
			t.Fatal("same command race", e)
		}
	}
	if testCount(t, s, "handheld_pick_commands") != 1 {
		t.Fatal("duplicated command")
	}
	c = handheldCommand(t, s, g, 0, 0)
	o = testOrder(t, s, id, owner.ID)
	wg.Add(2)
	go func() { defer wg.Done(); _, e := s.ConfirmHandheldPick(g.Token, g.CSRF, c); errs <- e }()
	go func() {
		defer wg.Done()
		errs <- s.AssignShopper(id, owner.ID, false, ShopperCommand{Action: "reassign", Key: token(), Reason: "Race reassignment", Version: o.Version, AssignmentVersion: o.Assignment.Version, ShopperID: 2})
	}()
	wg.Wait()
	wins := 0
	for i := 0; i < 2; i++ {
		e := <-errs
		if e == nil {
			wins++
		} else if !errors.Is(e, ErrConflict) && !errors.Is(e, ErrHandheldAccess) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal("race winners", wins)
	}
}
func TestHandheldAttemptLimitsAndRestartLostResponseRecovery(t *testing.T) {
	s, clock, _ := newReservationStore(t)
	b, err := s.HandheldPairSession("")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err = s.RedeemHandheld(b.Token, b.CSRF, "BAD"); !errors.Is(err, ErrHandheldPairing) {
			t.Fatal(i, err)
		}
	}
	if _, err = s.RedeemHandheld(b.Token, b.CSRF, "BAD"); !errors.Is(err, ErrHandheldAttempts) {
		t.Fatal(err)
	}
	clock.at(clock.now().Add(5 * time.Minute).Unix())
	if _, err = s.RedeemHandheld(b.Token, b.CSRF, "BAD"); !errors.Is(err, ErrHandheldPairing) {
		t.Fatal("limit did not reopen", err)
	}
	path := filepath.Join(t.TempDir(), "phone.db")
	persistent, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	owner, id, g := handheldFixture(t, persistent)
	c := handheldCommand(t, persistent, g, 0, 1)
	if _, err = persistent.ConfirmHandheldPick(g.Token, g.CSRF, c); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, persistent.db)
	persistent.Close()
	persistent, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer persistent.Close()
	retry, err := persistent.ConfirmHandheldPick(g.Token, g.CSRF, c)
	if err != nil || !retry.Replayed || fingerprintTest(t, persistent.db) != before {
		t.Fatal("lost-response restart replay", retry, err)
	}
	// An interrupted/lost phone cookie is replaced explicitly, retaining picks.
	newGrant := connectHandheldFixture(t, persistent, owner, id)
	assertHandheldDenied(t, persistent, g, c)
	if task := handheldTaskTest(t, persistent, newGrant); task.Status != "Picking" {
		t.Fatal("session recovery lost task")
	}
}

func TestHandheldIndependentConnectionsFenceStaleCodeAndRevokedReplay(t *testing.T) {
	for _, mode := range []string{"archive-code", "replace-line", "revoke-replay"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "parallel.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, _, g := handheldFixture(t, s)
			c := handheldCommand(t, s, g, 0, 1)
			phone, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer phone.Close()
			if mode == "revoke-replay" {
				if _, err = s.ConfirmHandheldPick(g.Token, g.CSRF, c); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := s.beginWrite()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			done := make(chan error, 1)
			go func() { _, e := phone.ConfirmHandheldPick(g.Token, g.CSRF, c); done <- e }()
			select {
			case e := <-done:
				t.Fatal("phone bypassed writer lock", e)
			case <-time.After(25 * time.Millisecond):
			}
			switch mode {
			case "archive-code":
				_, err = tx.Exec(`UPDATE product_codes SET archived=1 WHERE normalized_value=?`, c.Code)
			case "replace-line":
				_, err = tx.Exec(`UPDATE working_order_items SET quantity=0,allocated_quantity=0,pick_version=pick_version+1 WHERE id=?`, c.LineID)
			case "revoke-replay":
				_, err = tx.Exec(`UPDATE handheld_grants SET revoked=? WHERE token_hash=?`, s.now().Unix(), handheldHash(g.Token))
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			err = <-done
			want := ErrInvalid
			if mode == "replace-line" {
				want = ErrConflict
			}
			if mode == "revoke-replay" {
				want = ErrHandheldAccess
			}
			if !errors.Is(err, want) {
				t.Fatal("stale pre-transaction lookup was used", mode, err)
			}
			var picks int
			if e := s.db.QueryRow(`SELECT count(*) FROM handheld_pick_commands`).Scan(&picks); e != nil {
				t.Fatal(e)
			}
			expected := 0
			if mode == "revoke-replay" {
				expected = 1
			}
			if picks != expected {
				t.Fatal("late pick mutated", picks)
			}
		})
	}
}
func TestHandheldIndependentConnectionsCompetingKeysHaveOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parallel.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, _, g := handheldFixture(t, s)
	phone, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	c := handheldCommand(t, s, g, 0, 1)
	other := c
	other.Key = token()
	other.Picked = 2
	begin := make(chan struct{})
	done := make(chan error, 2)
	for i, store := range []*Store{s, phone} {
		cmd := c
		if i == 1 {
			cmd = other
		}
		go func(store *Store, cmd HandheldPick) {
			<-begin
			_, e := store.ConfirmHandheldPick(g.Token, g.CSRF, cmd)
			done <- e
		}(store, cmd)
	}
	close(begin)
	winners := 0
	for i := 0; i < 2; i++ {
		e := <-done
		if e == nil {
			winners++
		} else if !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	}
	if winners != 1 || testCount(t, s, "handheld_pick_commands") != 1 {
		t.Fatal("concurrent revision duplicate", winners)
	}
}
func TestHandheldGlobalAttemptBudgetCannotBeBypassedWithNewCookie(t *testing.T) {
	s, clock, _ := newReservationStore(t)
	owner, id := pickingFixture(t, s)
	testAssign(t, s, id, owner.ID, 1)
	o := testOrder(t, s, id, owner.ID)
	invite, err := s.ChangeHandheldPairing(id, owner.ID, false, HandheldPairCommand{Version: o.Version, AssignmentVersion: o.Assignment.Version, Key: token()}, "issue")
	if err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `UPDATE handheld_state SET attempts=120,attempt_window=? WHERE id=1`, clock.now().Unix())
	for i := 0; i < 2; i++ {
		b, err := s.HandheldPairSession("")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.RedeemHandheld(b.Token, b.CSRF, invite.PairingCode); !errors.Is(err, ErrHandheldAttempts) {
			t.Fatal("new cookie bypassed budget", err)
		}
	}
	if testCount(t, s, "handheld_grants") != 0 {
		t.Fatal("budget issued grant")
	}
	clock.at(clock.now().Add(5 * time.Minute).Unix())
	b, _ := s.HandheldPairSession("")
	if _, err = s.RedeemHandheld(b.Token, b.CSRF, invite.PairingCode); err != nil {
		t.Fatal("budget did not reopen safely", err)
	}
}
func TestHandheldPickCommandAuditFailureRollsBackProgressAndAttribution(t *testing.T) {
	s := newTestStore(t)
	_, _, g := handheldFixture(t, s)
	c := handheldCommand(t, s, g, 0, 1)
	testExec(t, s, `CREATE TRIGGER reject_phone_command BEFORE INSERT ON handheld_pick_commands BEGIN SELECT RAISE(ABORT,'command audit failure'); END`)
	before := fingerprintTest(t, s.db)
	if _, err := s.ConfirmHandheldPick(g.Token, g.CSRF, c); err == nil {
		t.Fatal("ignored command failure")
	}
	if before != fingerprintTest(t, s.db) {
		t.Fatal("event/progress/actor partially committed")
	}
}
