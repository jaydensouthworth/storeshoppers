package shop

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func securityWeightReview(t *testing.T, s *Store, g HandheldSession, c HandheldWeight) HandheldWeight {
	t.Helper()
	preview, err := s.PreviewHandheldWeight(g.Token, g.CSRF, c)
	if err != nil {
		t.Fatal(err)
	}
	return preview.Command
}

func securityWeightInputForProduct(t *testing.T, s *Store, g HandheldSession, productID, actual int64) HandheldWeight {
	t.Helper()
	for i, line := range handheldTaskTest(t, s, g).Lines {
		if line.ProductID == productID {
			return HandheldWeight{HandheldScan: handheldCommand(t, s, g, i, 0).HandheldScan, Actual: actual, Key: token()}
		}
	}
	t.Fatal("product missing from paired task")
	return HandheldWeight{}
}

func TestHandheldWeightSecurityIdentityAndScopeNeverMutate(t *testing.T) {
	for _, mode := range []string{"wrong-line-label", "not-in-task-label", "archived-code", "archived-product", "counted-line", "foreign-line", "removed-line"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestStore(t)
			p := weightedProduct(t, s, 3000, 349, 1)
			otherWeight := weightedProduct(t, s, 2000, 599, 1)
			owner, other := testSession(t, s, ""), testSession(t, s, "")
			testCart(t, s, owner.ID, p.ID, 500)
			testCart(t, s, owner.ID, otherWeight.ID, 500)
			testCart(t, s, owner.ID, 1, 1)
			id := testCheckout(t, s, owner.ID)
			testCart(t, s, other.ID, p.ID, 500)
			foreignID := testCheckout(t, s, other.ID)
			testAssign(t, s, id, owner.ID, 1)
			g := connectHandheldFixture(t, s, owner, id)
			c := securityWeightReview(t, s, g, securityWeightInputForProduct(t, s, g, p.ID, 527))
			want := ErrInvalid
			switch mode {
			case "wrong-line-label":
				c.Code = securityWeightInputForProduct(t, s, g, otherWeight.ID, 527).Code
			case "not-in-task-label":
				if err := s.db.QueryRow(`SELECT normalized_value FROM product_codes WHERE product_id=3 AND scheme='demo_local' AND archived=0`).Scan(&c.Code); err != nil {
					t.Fatal(err)
				}
			case "archived-code":
				testExec(t, s, `UPDATE product_codes SET archived=1 WHERE normalized_value=?`, c.Code)
			case "archived-product":
				testExec(t, s, `UPDATE products SET archived=1 WHERE id=?`, p.ID)
			case "counted-line":
				counted := securityWeightInputForProduct(t, s, g, 1, 1)
				c.HandheldScan, c.Actual = counted.HandheldScan, counted.Actual
			case "foreign-line":
				c.LineID = testOrder(t, s, foreignID, other.ID).WorkingItems[0].LineID
				want = ErrConflict
			case "removed-line":
				if err := s.OverrideOrder(id, owner.ID, false, overrideCommand(t, s, id, owner.ID, "set", p.ID, 0)); err != nil {
					t.Fatal(err)
				}
				want = ErrConflict
			}
			before := fingerprintTest(t, s.db)
			if _, err := s.PreviewHandheldWeight(g.Token, g.CSRF, c); !errors.Is(err, want) {
				t.Fatalf("preview accepted wrong identity/scope: %v, want %v", err, want)
			}
			if _, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, c); !errors.Is(err, want) {
				t.Fatalf("confirmation accepted wrong identity/scope: %v, want %v", err, want)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("rejected identity/scope changed inventory, progress, or audit")
			}
		})
	}
}

func TestHandheldWeightSecurityAuthorizationPrecedesValidationAndReplay(t *testing.T) {
	s := newTestStore(t)
	_, _, g, _ := handheldWeightFixture(t, s, 2000, 349, 500)
	c := securityWeightReview(t, s, g, handheldWeightInput(t, s, g, 527, ""))
	if _, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, c); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, raw, csrf string
		want            error
	}{
		{"missing CSRF", g.Token, "", ErrHandheldCSRF},
		{"wrong CSRF", g.Token, "wrong", ErrHandheldCSRF},
		{"missing grant", "", g.CSRF, ErrHandheldAccess},
		{"unknown grant", token(), g.CSRF, ErrHandheldAccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := fingerprintTest(t, s.db)
			for _, input := range []HandheldWeight{c, {}} {
				if _, err := s.PreviewHandheldWeight(tc.raw, tc.csrf, input); !errors.Is(err, tc.want) {
					t.Fatalf("preview authorization precedence: %v, want %v", err, tc.want)
				}
				if _, err := s.ConfirmHandheldWeight(tc.raw, tc.csrf, input); !errors.Is(err, tc.want) {
					t.Fatalf("replay/validation authorization precedence: %v, want %v", err, tc.want)
				}
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("unauthorized preview/replay changed database")
			}
		})
	}
}

func TestHandheldWeightSecurityEndedGrantCannotPreviewConfirmOrReplay(t *testing.T) {
	for _, mode := range []string{"reassign", "cancel", "ready", "complete", "revoke", "disconnect", "owner-expiry", "grant-expiry", "reset", "epoch"} {
		t.Run(mode, func(t *testing.T) {
			s, clock, _ := newReservationStore(t)
			owner, id, g, _ := handheldWeightFixture(t, s, 2000, 349, 500)
			c := securityWeightReview(t, s, g, handheldWeightInput(t, s, g, 527, ""))
			if _, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, c); err != nil {
				t.Fatal(err)
			}
			o := testOrder(t, s, id, owner.ID)
			switch mode {
			case "reassign", "cancel":
				target := int64(2)
				if mode == "cancel" {
					target = 0
				}
				if err := s.AssignShopper(id, owner.ID, false, ShopperCommand{Action: mode, Key: token(), Reason: "Change paired task for test", Version: o.Version, AssignmentVersion: o.Assignment.Version, ShopperID: target}); err != nil {
					t.Fatal(err)
				}
			case "ready", "complete":
				if err := s.AdvanceVersioned(id, "Picking", o.Version, owner.ID, false); err != nil {
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
				clock.at(g.Expires)
			case "reset":
				resetTest(t, s)
			case "epoch":
				testExec(t, s, `UPDATE handheld_state SET epoch='replacement epoch' WHERE id=1`)
			}
			before := fingerprintTest(t, s.db)
			for _, input := range []HandheldWeight{c, {}} {
				if _, err := s.PreviewHandheldWeight(g.Token, g.CSRF, input); !errors.Is(err, ErrHandheldAccess) {
					t.Fatalf("ended grant preview allowed: %v", err)
				}
				if _, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, input); !errors.Is(err, ErrHandheldAccess) {
					t.Fatalf("ended grant confirmation/replay allowed: %v", err)
				}
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("ended grant changed database")
			}
		})
	}
}

func TestHandheldWeightSecurityConcurrentManagerCorrectionHasOneWinner(t *testing.T) {
	s, clock, path := newReservationStore(t)
	owner, id, g, p := handheldWeightFixture(t, s, 2000, 349, 500)
	phone := reservationStore(t, path, clock)
	c := securityWeightReview(t, phone, g, handheldWeightInput(t, phone, g, 750, ""))
	manager, err := s.PreviewWeight(id, owner.ID, false, weightCommand(t, s, id, owner.ID, p.ID, 800, ""))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	phoneDone, managerDone := make(chan error, 1), make(chan error, 1)
	go func() { <-start; _, err := phone.ConfirmHandheldWeight(g.Token, g.CSRF, c); phoneDone <- err }()
	go func() { <-start; managerDone <- s.ConfirmWeight(id, owner.ID, false, manager.Command) }()
	close(start)
	phoneErr, managerErr := <-phoneDone, <-managerDone
	wantActual, wantLedger := int64(750), 1
	if phoneErr != nil {
		if !errors.Is(phoneErr, ErrConflict) || managerErr != nil {
			t.Fatalf("manager race outcomes: phone=%v manager=%v", phoneErr, managerErr)
		}
		wantActual, wantLedger = 800, 0
	} else if !errors.Is(managerErr, ErrConflict) {
		t.Fatalf("both writers accepted same snapshot: phone=%v manager=%v", phoneErr, managerErr)
	}
	o := testOrder(t, s, id, owner.ID)
	line := o.WorkingItems[0]
	if line.Allocated != wantActual || line.Picked != wantActual || !line.Measured || o.Version != c.Version+1 || line.PickVersion != c.PickVersion+1 || testProduct(t, s, p.ID).Stock != 2000-wantActual || testCount(t, s, "handheld_pick_commands") != wantLedger {
		t.Fatalf("race partially applied or duplicated: %+v", o)
	}
	var measures int
	if err := s.db.QueryRow(`SELECT count(*) FROM order_events WHERE order_id=? AND action='measure'`, id).Scan(&measures); err != nil || measures != 1 {
		t.Fatalf("measurement event count=%d err=%v", measures, err)
	}
}

func TestHandheldWeightSecurityConcurrentReadyFencesLateMeasurement(t *testing.T) {
	s, clock, path := newReservationStore(t)
	owner, id, g, p := handheldWeightFixture(t, s, 2000, 349, 500)
	confirmWeight(t, s, id, owner.ID, p.ID, 500, "")
	phone := reservationStore(t, path, clock)
	c := securityWeightReview(t, phone, g, handheldWeightInput(t, phone, g, 527, ""))
	start := make(chan struct{})
	phoneDone, readyDone := make(chan error, 1), make(chan error, 1)
	go func() { <-start; _, err := phone.ConfirmHandheldWeight(g.Token, g.CSRF, c); phoneDone <- err }()
	go func() { <-start; readyDone <- s.AdvanceVersioned(id, "Picking", c.Version, owner.ID, false) }()
	close(start)
	phoneErr, readyErr := <-phoneDone, <-readyDone
	o := testOrder(t, s, id, owner.ID)
	if phoneErr == nil {
		if !errors.Is(readyErr, ErrConflict) || o.Status != "Picking" || o.Finalized || o.WorkingItems[0].Picked != 527 || testProduct(t, s, p.ID).Stock != 1473 || testCount(t, s, "handheld_pick_commands") != 1 {
			t.Fatalf("phone winner was not atomic: ready=%v order=%+v", readyErr, o)
		}
	} else {
		if !errors.Is(phoneErr, ErrHandheldAccess) || readyErr != nil || o.Status != "Ready" || !o.Finalized || o.FinalTotal != 175 || o.WorkingItems[0].Picked != 500 || testProduct(t, s, p.ID).Stock != 1500 || testCount(t, s, "handheld_pick_commands") != 0 {
			t.Fatalf("Ready winner accepted a late measurement: phone=%v ready=%v order=%+v", phoneErr, readyErr, o)
		}
	}
}

func TestHandheldWeightSecurityCompetingOrdersCannotOversell(t *testing.T) {
	s, clock, path := newReservationStore(t)
	owner, id, g, p := handheldWeightFixture(t, s, 1300, 349, 500)
	other := testSession(t, s, "")
	testCart(t, s, other.ID, p.ID, 500)
	otherID := testCheckout(t, s, other.ID)
	testAssign(t, s, otherID, other.ID, 2)
	otherGrant := connectHandheldFixture(t, s, other, otherID)
	second := reservationStore(t, path, clock)
	c := securityWeightReview(t, s, g, handheldWeightInput(t, s, g, 750, ""))
	otherCommand := securityWeightReview(t, second, otherGrant, handheldWeightInput(t, second, otherGrant, 750, ""))
	start, done := make(chan struct{}), make(chan error, 2)
	go func() { <-start; _, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, c); done <- err }()
	go func() {
		<-start
		_, err := second.ConfirmHandheldWeight(otherGrant.Token, otherGrant.CSRF, otherCommand)
		done <- err
	}()
	close(start)
	wins, shortages := 0, 0
	for range 2 {
		if err := <-done; err == nil {
			wins++
		} else if errors.Is(err, ErrStock) {
			shortages++
		} else {
			t.Fatal(err)
		}
	}
	a, b := testOrder(t, s, id, owner.ID), testOrder(t, s, otherID, other.ID)
	if wins != 1 || shortages != 1 || testProduct(t, s, p.ID).Stock != 50 || a.WorkingItems[0].Allocated+b.WorkingItems[0].Allocated != 1250 || testCount(t, s, "handheld_pick_commands") != 1 {
		t.Fatalf("competing bags oversold or partially saved: wins=%d shortages=%d a=%+v b=%+v", wins, shortages, a, b)
	}
	if a.WorkingItems[0].Measured == b.WorkingItems[0].Measured {
		t.Fatal("losing order gained a measurement")
	}
}

func TestHandheldWeightSecurityRestartRetryRetainsSavedOutcomeAfterManagerCorrection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weight-retry.db")
	clock := newReservationClock()
	s, err := OpenWithClock(path, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	owner, id, g, p := handheldWeightFixture(t, s, 2000, 349, 500)
	c := securityWeightReview(t, s, g, handheldWeightInput(t, s, g, 527, ""))
	saved, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, c)
	if err != nil {
		t.Fatal(err)
	}
	confirmWeight(t, s, id, owner.ID, p.ID, 480, "restock")
	current := testOrder(t, s, id, owner.ID)
	if current.WorkingItems[0].Picked != 480 || current.Version == saved.Version || current.WorkingItems[0].PickVersion == saved.PickVersion {
		t.Fatal("manager correction did not change current snapshot")
	}
	before := fingerprintTest(t, s.db)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithClock(path, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, c)
	want := saved
	want.Replayed = true
	if err != nil || !reflect.DeepEqual(retry, want) || retry.Actual != 527 || retry.LineID != c.LineID || retry.Version != c.Version+1 || retry.PickVersion != c.PickVersion+1 {
		t.Fatalf("restart retry reported current correction instead of saved outcome: got=%+v want=%+v err=%v", retry, want, err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("restart replay mutated stock, progress, attribution, or grant activity")
	}
	changed := c
	changed.Actual++
	if _, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed payload reused successful command key", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("changed replay committed partial changes")
	}
}

func TestHandheldWeightSecurityExpiredHoldReconciliationAndShortageRollback(t *testing.T) {
	for _, actual := range []int64{527, 1001} {
		t.Run(fmt.Sprint(actual), func(t *testing.T) {
			s, clock, _ := newReservationStore(t)
			owner, id, g, p := handheldWeightFixture(t, s, 1000, 349, 500)
			other := testSession(t, s, "")
			testCart(t, s, other.ID, p.ID, 500)
			c := securityWeightReview(t, s, g, handheldWeightInput(t, s, g, actual, ""))
			clock.at(clock.now().Add(basketHold).Unix())
			before := fingerprintTest(t, s.db)
			if _, err := s.PreviewHandheldWeight(g.Token, g.CSRF, c); err != nil || fingerprintTest(t, s.db) != before {
				t.Fatal("preview reconciled expired holds or reserved more stock", err)
			}
			result, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, c)
			if actual == 1001 {
				if !errors.Is(err, ErrStock) || fingerprintTest(t, s.db) != before {
					t.Fatalf("shortage failed to roll back expiry/progress/stock/audit: %v", err)
				}
				return
			}
			if err != nil || result.Actual != actual {
				t.Fatalf("expired hold incorrectly caused shortage: %+v %v", result, err)
			}
			assertReservedStock(t, s, p.ID, 473, 0)
			o := testOrder(t, s, id, owner.ID)
			if o.WorkingItems[0].Allocated != actual || o.WorkingItems[0].Subtotal != 184 || o.Items[0].Quantity != 500 || o.Total != 175 {
				t.Fatalf("reconciliation changed requested receipt: %+v", o)
			}
			committed := fingerprintTest(t, s.db)
			if retry, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, c); err != nil || !retry.Replayed || fingerprintTest(t, s.db) != committed {
				t.Fatalf("retry repeated expiry or stock deduction: %+v %v", retry, err)
			}
		})
	}
}

func TestHandheldWeightSecurityAuditFaultRollsBackEveryWrite(t *testing.T) {
	for _, fault := range []struct{ name, trigger string }{
		{"event", `CREATE TRIGGER reject_phone_weight BEFORE INSERT ON order_events WHEN NEW.action='measure' BEGIN SELECT RAISE(ABORT,'weight event failure'); END`},
		{"actor", `CREATE TRIGGER reject_phone_weight BEFORE INSERT ON order_event_actors WHEN NEW.actor='worker' BEGIN SELECT RAISE(ABORT,'weight actor failure'); END`},
		{"command-ledger", `CREATE TRIGGER reject_phone_weight BEFORE INSERT ON handheld_pick_commands BEGIN SELECT RAISE(ABORT,'weight command failure'); END`},
		{"grant-activity", `CREATE TRIGGER reject_phone_weight BEFORE UPDATE OF last_recorded ON handheld_grants BEGIN SELECT RAISE(ABORT,'weight activity failure'); END`},
	} {
		for _, actual := range []int64{480, 527} {
			t.Run(fmt.Sprintf("%s/%d", fault.name, actual), func(t *testing.T) {
				s, clock, _ := newReservationStore(t)
				_, _, g, p := handheldWeightFixture(t, s, 1000, 349, 500)
				other := testSession(t, s, "")
				testCart(t, s, other.ID, p.ID, 500)
				disposition := ""
				if actual < 500 {
					disposition = "restock"
				}
				c := securityWeightReview(t, s, g, handheldWeightInput(t, s, g, actual, disposition))
				clock.at(clock.now().Add(basketHold).Unix())
				testExec(t, s, fault.trigger)
				before := fingerprintTest(t, s.db)
				if _, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, c); err == nil {
					t.Fatal("ignored injected write failure")
				}
				if fingerprintTest(t, s.db) != before {
					t.Fatal("failed confirmation retained expiry, stock, progress, event, actor, command, or activity changes")
				}
				testExec(t, s, `DROP TRIGGER reject_phone_weight`)
				if _, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, c); err != nil {
					t.Fatal("rolled-back command could not be retried", err)
				}
				assertReservedStock(t, s, p.ID, 1000-actual, 0)
				if testCount(t, s, "handheld_pick_commands") != 1 {
					t.Fatal("failed attempt consumed or duplicated command")
				}
			})
		}
	}
}

func TestHandheldWeightSecurityRestockCapacityIncludesReservedGrams(t *testing.T) {
	for _, stock := range []int64{999880, 999881} {
		t.Run(fmt.Sprint(stock), func(t *testing.T) {
			s := newTestStore(t)
			owner, id, g, p := handheldWeightFixture(t, s, 1000, 349, 500)
			other := testSession(t, s, "")
			testCart(t, s, other.ID, p.ID, 100)
			// Available stock alone has room for the 20 g return. Only the
			// active 100 g basket reservation makes the second case overflow.
			testExec(t, s, `UPDATE products SET stock=? WHERE id=?`, stock, p.ID)
			c := securityWeightReview(t, s, g, handheldWeightInput(t, s, g, 480, "restock"))
			before := fingerprintTest(t, s.db)
			_, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, c)
			wantStock := stock + 20
			if stock == 999881 {
				if !errors.Is(err, ErrStockCapacity) || fingerprintTest(t, s.db) != before {
					t.Fatalf("restock exceeded stock plus reservation capacity or partially committed: %v", err)
				}
				c.Disposition = "writeoff"
				c = securityWeightReview(t, s, g, c)
				if _, err = s.ConfirmHandheldWeight(g.Token, g.CSRF, c); err != nil {
					t.Fatal("explicit writeoff could not recover capacity failure", err)
				}
				wantStock = stock
			} else if err != nil {
				t.Fatal("exact capacity boundary incorrectly rejected", err)
			}
			assertReservedStock(t, s, p.ID, wantStock, 100)
			order := testOrder(t, s, id, owner.ID)
			if order.WorkingItems[0].Picked != 480 || order.WorkingItems[0].Allocated != 480 || !order.WorkingItems[0].Measured || testCount(t, s, "handheld_pick_commands") != 1 {
				t.Fatal("capacity result partially saved", order)
			}
		})
	}
}

func TestHandheldWeightSecurityConcurrentExactRetryDeductsOnce(t *testing.T) {
	s, clock, path := newReservationStore(t)
	_, _, g, p := handheldWeightFixture(t, s, 2000, 349, 500)
	second := reservationStore(t, path, clock)
	c := securityWeightReview(t, s, g, handheldWeightInput(t, s, g, 527, ""))
	type outcome struct {
		result HandheldWeightResult
		err    error
	}
	start, done := make(chan struct{}), make(chan outcome, 2)
	for _, store := range []*Store{s, second} {
		go func(store *Store) {
			<-start
			result, err := store.ConfirmHandheldWeight(g.Token, g.CSRF, c)
			done <- outcome{result, err}
		}(store)
	}
	close(start)
	a, b := <-done, <-done
	if a.err != nil || b.err != nil || a.result.Replayed == b.result.Replayed {
		t.Fatalf("concurrent retry did not yield one mutation and one replay: %+v %+v", a, b)
	}
	a.result.Replayed, b.result.Replayed = false, false
	if !reflect.DeepEqual(a.result, b.result) || a.result.Actual != 527 || a.result.Version != c.Version+1 || a.result.PickVersion != c.PickVersion+1 || testProduct(t, s, p.ID).Stock != 1473 || testCount(t, s, "handheld_pick_commands") != 1 {
		t.Fatalf("concurrent retry changed saved outcome or deducted twice: %+v %+v", a, b)
	}
}
