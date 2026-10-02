package shop

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func handheldWeightFixture(t *testing.T, s *Store, stock, rate, target int64) (Session, int64, HandheldSession, Product) {
	t.Helper()
	p := weightedProduct(t, s, stock, rate, 1)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, target)
	id := testCheckout(t, s, owner.ID)
	testAssign(t, s, id, owner.ID, 1)
	return owner, id, connectHandheldFixture(t, s, owner, id), p
}

func handheldWeightInput(t *testing.T, s *Store, g HandheldSession, actual int64, disposition string) HandheldWeight {
	t.Helper()
	c := handheldCommand(t, s, g, 0, 0)
	return HandheldWeight{HandheldScan: c.HandheldScan, Actual: actual, Key: token(), Disposition: disposition}
}

func TestHandheldWeightPreviewConfirmBoundsAndReceipt(t *testing.T) {
	for _, tc := range []struct {
		actual, rate int64
		disposition  string
	}{
		{527, 349, ""}, {500, 349, ""}, {480, 349, "restock"}, {480, 349, "writeoff"},
		{0, 349, "restock"}, {0, 349, "writeoff"}, {100000, 349, ""},
		{1, 499, "restock"}, {1, 500, "restock"},
	} {
		t.Run(fmt.Sprintf("%d-%d-%s", tc.actual, tc.rate, tc.disposition), func(t *testing.T) {
			s := newTestStore(t)
			owner, id, g, p := handheldWeightFixture(t, s, 200000, tc.rate, 500)
			testExec(t, s, `UPDATE orders SET attention_reason='PRIVATE_HOLD_KEEP',attention_since=123 WHERE id=?`, id)
			original := placedSnapshot(t, s, id)
			c := handheldWeightInput(t, s, g, tc.actual, tc.disposition)
			c.Source, c.Note = "photo", " Scale reading checked "
			before := fingerprintTest(t, s.db)
			preview, err := s.PreviewHandheldWeight(g.Token, g.CSRF, c)
			if err != nil {
				t.Fatal(err)
			}
			oldAmount, _ := lineAmount(500, tc.rate, 1000, "g")
			amount, _ := lineAmount(tc.actual, tc.rate, 1000, "g")
			if preview.Command.Note != "Scale reading checked" || !preview.Recognition.CanMeasure || preview.Recognition.CanPick || preview.Target != 500 || preview.Allocated != 500 || preview.Picked != 0 || preview.Price != tc.rate || preview.OldSubtotal != oldAmount || preview.NewSubtotal != amount || preview.StockDelta != tc.actual-500 || preview.PriceDelta != amount-oldAmount || preview.WorkingTotal != oldAmount || preview.ProposedWorkingTotal != amount || len(preview.Command.Review) != 64 {
				t.Fatalf("preview %+v", preview)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("preview mutated state")
			}
			if _, err = s.ConfirmHandheldWeight(g.Token, g.CSRF, c); !errors.Is(err, ErrWeightReview) {
				t.Fatal("accepted without review", err)
			}
			result, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, preview.Command)
			if err != nil || result.Actual != tc.actual || result.Version != c.Version+1 || result.PickVersion != c.PickVersion+1 || result.Replayed {
				t.Fatal(result, err)
			}
			o := attentionManagerOrder(t, s, id, owner.ID)
			line := o.WorkingItems[0]
			wantStock := int64(199500)
			if tc.actual >= 500 || tc.disposition == "restock" {
				wantStock = 200000 - tc.actual
			}
			if line.Allocated != tc.actual || line.Picked != tc.actual || !line.Measured || line.Quantity != 500 || line.Price != tc.rate || line.Subtotal != amount || o.WorkingTotal != amount || o.Finalized || o.Status != "Picking" || o.AttentionReason != "PRIVATE_HOLD_KEEP" || testProduct(t, s, p.ID).Stock != wantStock || !reflect.DeepEqual(original, placedSnapshot(t, s, id)) {
				t.Fatalf("saved %+v", o)
			}
			var actor, source, name string
			if err = s.db.QueryRow(`SELECT a.actor,a.source,a.shopper_name FROM order_event_actors a JOIN order_events e ON e.id=a.event_id WHERE e.action='measure'`).Scan(&actor, &source, &name); err != nil || actor != "worker" || source != "photo" || name != "Avery Morgan" {
				t.Fatal("attribution", actor, source, name, err)
			}
			if strings.Contains(fmt.Sprintf("%+v", handheldTaskTest(t, s, g)), "PRIVATE_HOLD_KEEP") {
				t.Fatal("hold leaked")
			}
			before = fingerprintTest(t, s.db)
			replay, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, preview.Command)
			if err != nil || !replay.Replayed || replay.Actual != result.Actual || replay.Version != result.Version || fingerprintTest(t, s.db) != before {
				t.Fatal("not exact retry", replay, err)
			}
		})
	}
}

func TestHandheldWeightMixedWorkingTotalRetainedRateAndReviewBinding(t *testing.T) {
	s := newTestStore(t)
	owner, id, g, p := handheldWeightFixture(t, s, 10000, 349, 500)
	add := overrideCommand(t, s, id, owner.ID, "set", 1, 2)
	if err := s.OverrideOrder(id, owner.ID, false, add); err != nil {
		t.Fatal(err)
	}
	c := handheldWeightInput(t, s, g, 527, "")
	preview, err := s.PreviewHandheldWeight(g.Token, g.CSRF, c)
	if err != nil {
		t.Fatal(err)
	}
	current := testOrder(t, s, id, owner.ID)
	if preview.WorkingTotal != current.WorkingTotal || preview.ProposedWorkingTotal != current.WorkingTotal+9 {
		t.Fatalf("mixed total %+v", preview)
	}
	p = testProduct(t, s, p.ID)
	p.Price = 999
	if _, err = s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*HandheldWeight){
		func(c *HandheldWeight) { c.Actual++ }, func(c *HandheldWeight) { c.Source = "camera" },
		func(c *HandheldWeight) { c.Note = "new note" }, func(c *HandheldWeight) { c.Disposition = "restock" },
		func(c *HandheldWeight) { c.Key = token() },
	} {
		bad := preview.Command
		mutate(&bad)
		before := fingerprintTest(t, s.db)
		if _, err = s.ConfirmHandheldWeight(g.Token, g.CSRF, bad); !errors.Is(err, ErrWeightReview) {
			t.Fatal("changed review", err)
		}
		if fingerprintTest(t, s.db) != before {
			t.Fatal("bad review mutated")
		}
	}
	if _, err = s.ConfirmHandheldWeight(g.Token, g.CSRF, preview.Command); err != nil {
		t.Fatal(err)
	}
	o := testOrder(t, s, id, owner.ID)
	if o.WorkingItems[0].Price != 349 || o.WorkingItems[0].Subtotal != 184 {
		t.Fatal("lost retained rate", o)
	}
	bad := preview.Command
	bad.Actual++
	if _, err = s.ConfirmHandheldWeight(g.Token, g.CSRF, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("consumed key changed", err)
	}
}

func TestHandheldWeightRejectsInvalidInputsAndImplicitDisposition(t *testing.T) {
	s := newTestStore(t)
	_, _, g, _ := handheldWeightFixture(t, s, 1000, 349, 500)
	c := handheldWeightInput(t, s, g, 499, "")
	if _, err := s.PreviewHandheldWeight(g.Token, g.CSRF, c); !errors.Is(err, ErrPickedDisposition) {
		t.Fatal("implicit reduction", err)
	}
	c.Actual = 500
	for _, mutate := range []func(*HandheldWeight){
		func(c *HandheldWeight) { c.Actual = -1 }, func(c *HandheldWeight) { c.Actual = 100001 },
		func(c *HandheldWeight) { c.Disposition = "automatic" }, func(c *HandheldWeight) { c.Note = strings.Repeat("a", 241) },
		func(c *HandheldWeight) { c.Note = "control\nchar" }, func(c *HandheldWeight) { c.Note = "\xff" },
		func(c *HandheldWeight) { c.Key = "short" }, func(c *HandheldWeight) { c.Source = "pretend" },
	} {
		bad := c
		mutate(&bad)
		before := fingerprintTest(t, s.db)
		if _, err := s.PreviewHandheldWeight(g.Token, g.CSRF, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid preview", err)
		}
		if _, err := s.ConfirmHandheldWeight(g.Token, g.CSRF, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid confirm", err)
		}
		if fingerprintTest(t, s.db) != before {
			t.Fatal("invalid input changed state")
		}
	}
}

func TestSharedWeightTransactionKeepsManagerReasonAndAtomicAttribution(t *testing.T) {
	s := newTestStore(t)
	owner, id, _, p := handheldWeightFixture(t, s, 1000, 349, 500)
	c := weightCommand(t, s, id, owner.ID, p.ID, 527, "")
	c.Reason = ""
	preview, err := s.PreviewWeight(id, owner.ID, false, c)
	if err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `CREATE TRIGGER reject_manager_measure_actor BEFORE INSERT ON order_event_actors BEGIN SELECT RAISE(ABORT,'actor failure'); END`)
	before := fingerprintTest(t, s.db)
	if err = s.ConfirmWeight(id, owner.ID, false, preview.Command); err == nil {
		t.Fatal("manager measurement ignored attribution failure")
	}
	if before != fingerprintTest(t, s.db) {
		t.Fatal("manager measurement partly committed")
	}
	testExec(t, s, `DROP TRIGGER reject_manager_measure_actor`)
	if err = s.ConfirmWeight(id, owner.ID, false, preview.Command); err != nil {
		t.Fatal(err)
	}
	var actor, source, reason string
	if err = s.db.QueryRow(`SELECT a.actor,a.source,e.reason FROM order_event_actors a JOIN order_events e ON e.id=a.event_id WHERE e.action='measure'`).Scan(&actor, &source, &reason); err != nil || actor != "manager" || source != "manager" || reason != "Manager confirmed scale reading" {
		t.Fatal("manager reason/actor changed", actor, source, reason, err)
	}
}
