package shop

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
)

func weightedProduct(t *testing.T, s *Store, stock, price, step int64) Product {
	t.Helper()
	p := testCreateProduct(t, s, func(p *Product) {
		p.Name = "Loose apples"
		p.SaleUnit = "g"
		p.PriceBasis = 1000
		p.QuantityStep = step
		p.Price = price
	})
	if stock > 0 {
		if err := s.Adjust(p.ID, stock, p.Version, "Measured fake stock"); err != nil {
			t.Fatal(err)
		}
	}
	return testProduct(t, s, p.ID)
}
func weightCommand(t *testing.T, s *Store, id int64, sid string, pid, actual int64, disposition string) WeightCommand {
	t.Helper()
	o := testOrder(t, s, id, sid)
	for _, l := range o.WorkingItems {
		if l.ProductID == pid {
			return WeightCommand{LineID: l.LineID, PickVersion: l.PickVersion, OrderVersion: o.Version, Actual: actual, Key: token(), Reason: "Measured this bag on the scale", Disposition: disposition}
		}
	}
	t.Fatal("working product absent")
	return WeightCommand{}
}
func confirmWeight(t *testing.T, s *Store, id int64, sid string, pid, actual int64, disposition string) WeightCommand {
	t.Helper()
	c := weightCommand(t, s, id, sid, pid, actual, disposition)
	p, err := s.PreviewWeight(id, sid, false, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmWeight(id, sid, false, p.Command); err != nil {
		t.Fatal(err)
	}
	return p.Command
}
func TestWeightedIntegerRoundingAndBounds(t *testing.T) {
	for _, tc := range []struct{ qty, rate, want int64 }{{527, 349, 184}, {1250, 349, 436}, {1, 500, 1}, {1, 499, 0}, {100000, 1000000, 100000000}, {0, 349, 0}} {
		got, err := lineAmount(tc.qty, tc.rate, 1000, "g")
		if err != nil || got != tc.want {
			t.Fatalf("%+v got%d %v", tc, got, err)
		}
	}
	for _, tc := range []struct {
		qty, rate, basis int64
		unit             string
	}{{math.MaxInt64, math.MaxInt64, 1000, "g"}, {100001, 1, 1000, "g"}, {100, 1, 1, "each"}, {1, 1, 1, "g"}, {1, 1, 1000, "each"}, {-1, 100, 1000, "g"}, {1, 0, 1000, "g"}} {
		if _, err := lineAmount(tc.qty, tc.rate, tc.basis, tc.unit); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid %+v =%v", tc, err)
		}
	}
	if _, err := addAmount(math.MaxInt64, 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("total overflow", err)
	}
}

func TestWeightedConfirmationReconcilesHoldsExpiredAfterPreview(t *testing.T) {
	for _, actual := range []int64{527, 1001} {
		t.Run(fmt.Sprint(actual), func(t *testing.T) {
			s, clock, _ := newReservationStore(t)
			p := weightedProduct(t, s, 1000, 349, 1)
			owner := testSession(t, s, "")
			other := testSession(t, s, "")
			testCart(t, s, owner.ID, p.ID, 500)
			id := testCheckout(t, s, owner.ID)
			testCart(t, s, other.ID, p.ID, 500)
			c := weightCommand(t, s, id, owner.ID, p.ID, actual, "")
			preview, err := s.PreviewWeight(id, owner.ID, false, c)
			if err != nil {
				t.Fatal(err)
			}
			clock.at(clock.now().Add(basketHold).Unix())
			before := fingerprintTest(t, s.db)
			err = s.ConfirmWeight(id, owner.ID, false, preview.Command)
			if actual == 1001 {
				if !errors.Is(err, ErrStock) {
					t.Fatalf("over-allocation = %v", err)
				}
				if fingerprintTest(t, s.db) != before {
					t.Fatal("failed measurement committed expiry or allocation changes")
				}
				return
			}
			if err != nil {
				t.Fatalf("expired hold was treated as a shortage: %v", err)
			}
			assertReservedStock(t, s, p.ID, 473, 0)
			order := testOrder(t, s, id, owner.ID)
			if order.Items[0].Quantity != 500 || order.WorkingItems[0].Allocated != 527 || order.WorkingItems[0].Subtotal != 184 {
				t.Fatalf("measurement snapshot = %+v", order)
			}
			committed := fingerprintTest(t, s.db)
			if err = s.ConfirmWeight(id, owner.ID, false, preview.Command); err != nil {
				t.Fatal(err)
			}
			if fingerprintTest(t, s.db) != committed {
				t.Fatal("measurement replay repeated expiry/deduction")
			}
		})
	}
}
func TestWeightedRequestedActualAllocationAndFrozenReceipt(t *testing.T) {
	s := newTestStore(t)
	p := weightedProduct(t, s, 10000, 349, 50)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, 500)
	b := testBasket(t, s, owner.ID)
	if b.Total != 175 || b.Count != 1 || !b.HasWeight {
		t.Fatalf("estimate %+v", b)
	}
	id := testCheckout(t, s, owner.ID)
	original := placedSnapshot(t, s, id)
	if testProduct(t, s, p.ID).Stock != 9500 {
		t.Fatal("checkout double deduction")
	}
	c := weightCommand(t, s, id, owner.ID, p.ID, 527, "")
	before, _ := databaseFingerprint(s.db)
	preview, err := s.PreviewWeight(id, owner.ID, false, c)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := databaseFingerprint(s.db)
	if before != after {
		t.Fatal("preview mutated state")
	}
	if preview.StockDelta != 27 || preview.PriceDelta != 9 || preview.OldSubtotal != 175 || preview.NewSubtotal != 184 {
		t.Fatalf("preview %+v", preview)
	}
	if err = s.ConfirmWeight(id, owner.ID, false, c); !errors.Is(err, ErrWeightReview) {
		t.Fatal("unreviewed measurement", err)
	}
	if err = s.ConfirmWeight(id, owner.ID, false, preview.Command); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmWeight(id, owner.ID, false, preview.Command); err != nil {
		t.Fatal("replay", err)
	}
	o := testOrder(t, s, id, owner.ID)
	l := o.WorkingItems[0]
	if l.Quantity != 500 || l.Allocated != 527 || l.Picked != 527 || !l.Measured || l.Subtotal != 184 || o.Total != 175 || o.WorkingTotal != 184 || !o.AllPicked || testProduct(t, s, p.ID).Stock != 9473 {
		t.Fatalf("measurement %+v", o)
	}
	current := testProduct(t, s, p.ID)
	current.Price = 999
	if _, err = s.SaveProduct(current); err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceVersioned(id, "Picking", o.Version, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	o = testOrder(t, s, id, owner.ID)
	if o.FinalTotal != 184 || !o.Finalized || o.Items[0].Price != 349 || !reflect.DeepEqual(original, placedSnapshot(t, s, id)) {
		t.Fatal("frozen receipt or rate changed")
	}
	if err = s.ConfirmWeight(id, owner.ID, false, preview.Command); err != nil {
		t.Fatal("terminal exact replay", err)
	}
	if err = s.ConfirmWeight(id, owner.ID, false, weightCommand(t, s, id, owner.ID, p.ID, 530, "")); !errors.Is(err, ErrWeightReview) {
		t.Fatal("terminal unreviewed command", err)
	}
}
func TestWeightedReductionDispositionZeroAndTargetInvalidation(t *testing.T) {
	for _, disposition := range []string{"restock", "writeoff"} {
		t.Run(disposition, func(t *testing.T) {
			s := newTestStore(t)
			p := weightedProduct(t, s, 1000, 349, 50)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, p.ID, 500)
			id := testCheckout(t, s, owner.ID)
			c := weightCommand(t, s, id, owner.ID, p.ID, 480, "")
			if _, err := s.PreviewWeight(id, owner.ID, false, c); !errors.Is(err, ErrPickedDisposition) {
				t.Fatal("implicit return", err)
			}
			confirmWeight(t, s, id, owner.ID, p.ID, 480, disposition)
			want := int64(500)
			if disposition == "restock" {
				want = 520
			}
			if testProduct(t, s, p.ID).Stock != want {
				t.Fatal("invented/lost stock")
			}
			edit := overrideCommand(t, s, id, owner.ID, "set", p.ID, 550)
			if err := s.OverrideOrder(id, owner.ID, false, edit); err != nil {
				t.Fatal(err)
			}
			o := testOrder(t, s, id, owner.ID)
			l := o.WorkingItems[0]
			if l.Quantity != 550 || l.Allocated != 550 || l.Picked != 0 || l.Measured || o.AllPicked || testProduct(t, s, p.ID).Stock != want-70 {
				t.Fatalf("target did not invalidate %+v", o)
			}
			if err := s.Advance(id, "Picking"); !errors.Is(err, ErrIncomplete) {
				t.Fatal("unmeasured ready", err)
			}
			confirmWeight(t, s, id, owner.ID, p.ID, 0, disposition)
			o = testOrder(t, s, id, owner.ID)
			if !o.AllPicked || o.WorkingTotal != 0 || o.WorkingItems[0].Quantity != 550 {
				t.Fatalf("explicit zero %+v", o)
			}
			if err := s.Advance(id, "Picking"); err != nil {
				t.Fatal(err)
			}
			if o = testOrder(t, s, id, owner.ID); !o.Finalized || o.FinalTotal != 0 || o.Total != 175 {
				t.Fatalf("final zero %+v", o)
			}
		})
	}
}
func TestWeightedMeasurementConflictsPrivacyAndAuditRollback(t *testing.T) {
	s := newTestStore(t)
	p := weightedProduct(t, s, 1000, 349, 1)
	owner := testSession(t, s, "")
	other := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, 500)
	id := testCheckout(t, s, owner.ID)
	c := weightCommand(t, s, id, owner.ID, p.ID, 527, "")
	preview, err := s.PreviewWeight(id, owner.ID, false, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PreviewWeight(id, other.ID, false, c); !errors.Is(err, ErrNotFound) {
		t.Fatal("privacy", err)
	}
	if err = s.ConfirmWeight(id, other.ID, false, preview.Command); !errors.Is(err, ErrNotFound) {
		t.Fatal("confirm privacy", err)
	}
	for _, change := range []func(*WeightCommand){func(c *WeightCommand) { c.Actual++ }, func(c *WeightCommand) { c.OrderVersion++ }, func(c *WeightCommand) { c.PickVersion++ }, func(c *WeightCommand) { c.Actual = maxGrams + 1 }, func(c *WeightCommand) { c.Reason = "bad\nreason" }} {
		bad := preview.Command
		change(&bad)
		before, _ := databaseFingerprint(s.db)
		if err = s.ConfirmWeight(id, owner.ID, false, bad); err == nil {
			t.Fatal("bad command accepted")
		}
		after, _ := databaseFingerprint(s.db)
		if before != after {
			t.Fatal("failed measurement changed state")
		}
	}
	testExec(t, s, `CREATE TRIGGER fail_measure_audit BEFORE INSERT ON order_events WHEN NEW.action='measure' BEGIN SELECT RAISE(ABORT,'audit failed'); END`)
	before, _ := databaseFingerprint(s.db)
	if err = s.ConfirmWeight(id, owner.ID, false, preview.Command); err == nil {
		t.Fatal("audit failure accepted")
	}
	after, _ := databaseFingerprint(s.db)
	if before != after {
		t.Fatal("audit failure partially committed")
	}
	testExec(t, s, `DROP TRIGGER fail_measure_audit`)
	if err = s.ConfirmWeight(id, owner.ID, false, preview.Command); err != nil {
		t.Fatal(err)
	}
	bad := preview.Command
	bad.Actual++
	if err = s.ConfirmWeight(id, owner.ID, false, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay", err)
	}
	if err = s.RecordPicked(id, p.ID, 1, 2, owner.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("legacy count route picked grams", err)
	}
}
func TestWeightedSubstitutionsUnitsRatesAndAtomicQuote(t *testing.T) {
	s := newTestStore(t)
	p := weightedProduct(t, s, 2000, 349, 50)
	replacement := weightedProduct(t, s, 2000, 599, 25)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, 500)
	id := testCheckout(t, s, owner.ID)
	c := overrideCommand(t, s, id, owner.ID, "substitute", p.ID, 525)
	c.ReplacementID = 1
	before, _ := databaseFingerprint(s.db)
	if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrInvalid) {
		t.Fatal("mixed substitution", err)
	}
	after, _ := databaseFingerprint(s.db)
	if before != after {
		t.Fatal("mixed partial mutation")
	}
	c.ReplacementID = replacement.ID
	c.CatalogQuote = "stale"
	if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrOrderQuote) {
		t.Fatal("stale replacement", err)
	}
	c = overrideCommand(t, s, id, owner.ID, "substitute", p.ID, 525)
	c.ReplacementID = replacement.ID
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	o := testOrder(t, s, id, owner.ID)
	if len(o.WorkingItems) != 1 || o.WorkingItems[0].Price != 599 || o.WorkingItems[0].Allocated != 525 || o.WorkingItems[0].Measured {
		t.Fatalf("replacement %+v", o)
	}
	rp := testProduct(t, s, replacement.ID)
	rp.Price = 899
	if _, err := s.SaveProduct(rp); err != nil {
		t.Fatal(err)
	}
	c = overrideCommand(t, s, id, owner.ID, "set", replacement.ID, 550)
	c.CatalogQuote = "stale"
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal("working rate retained", err)
	}
	confirmWeight(t, s, id, owner.ID, replacement.ID, 547, "restock")
	o = testOrder(t, s, id, owner.ID)
	if o.WorkingItems[0].Price != 599 || o.WorkingItems[0].Subtotal != 328 || o.Items[0].ProductID != p.ID || o.Items[0].Price != 349 {
		t.Fatalf("snapshot rates %+v", o)
	}
}
func TestWeightedReservationsExpiryCompetitionAndZeroEstimate(t *testing.T) {
	s, clock, path := newReservationStore(t)
	p := weightedProduct(t, s, 1000, 349, 50)
	a, b := testSession(t, s, ""), testSession(t, s, "")
	second := reservationStore(t, path, clock)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, owner := range []Session{a, b} {
		store := []*Store{s, second}[i]
		wg.Add(1)
		go func() { defer wg.Done(); <-start; results <- store.SetCart(owner.ID, p.ID, 750, false) }()
	}
	close(start)
	wg.Wait()
	close(results)
	success, short := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrStock) {
			short++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || short != 1 {
		t.Fatal("oversell", success, short)
	}
	assertReservedStock(t, s, p.ID, 250, 750)
	clock.at(clock.now().Add(basketHold).Unix())
	if err := s.ExpireHolds(); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireHolds(); err != nil {
		t.Fatal(err)
	}
	assertReservedStock(t, s, p.ID, 1000, 0)
	tiny := weightedProduct(t, s, 1000, 1, 1)
	before, _ := databaseFingerprint(s.db)
	if err := s.SetCart(testSession(t, s, "").ID, tiny.ID, 1, false); !errors.Is(err, ErrZeroEstimate) {
		t.Fatal("zero estimate", err)
	}
	_ = before // A new visitor was deliberately created; inventory must be untouched.
	assertReservedStock(t, s, tiny.ID, 1000, 0)
	for _, qty := range []int64{49, 100001, math.MaxInt64} {
		if err := s.SetCart(a.ID, p.ID, qty, false); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid grams", qty, err)
		}
	}
}

func TestWeightedMixedFinishCancelAndShopperMetrics(t *testing.T) {
	for _, action := range []string{"finish", "cancel"} {
		t.Run(action, func(t *testing.T) {
			s := newTestStore(t)
			p := weightedProduct(t, s, 2000, 349, 50)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 3)
			testCart(t, s, owner.ID, p.ID, 500)
			id := testCheckout(t, s, owner.ID)
			confirmWeight(t, s, id, owner.ID, p.ID, 480, "writeoff")
			o := testOrder(t, s, id, owner.ID)
			if err := s.RecordPicked(id, 1, 1, o.WorkingItems[0].PickVersion, owner.ID, false); err != nil {
				t.Fatal(err)
			}
			o = testOrder(t, s, id, owner.ID)
			if o.RequiredCount != 2 || o.PickedCount != 1 || o.Percent != 50 || !o.HasWeight {
				t.Fatalf("mixed progress %+v", o)
			}
			if err := s.AssignShopper(id, owner.ID, false, ShopperCommand{Action: "assign", Version: o.Version, ShopperID: 1, Key: token(), Reason: "Assign fake task"}); err != nil {
				t.Fatal(err)
			}
			work, err := s.Shoppers(ShopperFilters{Status: "all", Page: 1}, owner.ID, false, id)
			if err != nil {
				t.Fatal(err)
			}
			if !work.Roster[0].HasWeight || work.Roster[0].Picked != 1 || work.Roster[0].Required != 2 || work.Tasks[0].Order.Percent != 50 {
				t.Fatalf("mixed roster %+v", work)
			}
			c := overrideCommand(t, s, id, owner.ID, action, 0, 0)
			c.Disposition = "writeoff"
			if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
				t.Fatal(err)
			}
			o = testOrder(t, s, id, owner.ID)
			want := int64(168) + o.Items[0].Price
			if action == "cancel" {
				want = 0
			}
			if o.FinalTotal != want || !o.Finalized || o.Assignment != nil || testProduct(t, s, p.ID).Stock != 1500 {
				t.Fatalf("terminal weighted resolution %+v", o)
			}
			before, _ := databaseFingerprint(s.db)
			if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
				t.Fatal("terminal replay", err)
			}
			after, _ := databaseFingerprint(s.db)
			if before != after {
				t.Fatal("repeated resolution changed state")
			}
		})
	}
}

func TestWeightedMeasurementRaceCapacityAndRestart(t *testing.T) {
	s, clock, path := newReservationStore(t)
	p := weightedProduct(t, s, 1000, 349, 1)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, 500)
	id := testCheckout(t, s, owner.ID)
	second := reservationStore(t, path, clock)
	p1, err := s.PreviewWeight(id, owner.ID, false, weightCommand(t, s, id, owner.ID, p.ID, 750, ""))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.PreviewWeight(id, owner.ID, false, weightCommand(t, s, id, owner.ID, p.ID, 800, ""))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	result := make(chan error, 2)
	for i, c := range []WeightCommand{p1.Command, p2.Command} {
		store := []*Store{s, second}[i]
		go func() { <-start; result <- store.ConfirmWeight(id, owner.ID, false, c) }()
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		err := <-result
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("measurement race", success, conflict)
	}
	o := testOrder(t, s, id, owner.ID)
	actual := o.WorkingItems[0].Picked
	if testProduct(t, s, p.ID).Stock != 1000-actual {
		t.Fatal("measurement double deduction")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = reservationStore(t, path, clock)
	if got := testOrder(t, s, id, owner.ID); !got.WorkingItems[0].Measured || got.WorkingItems[0].Allocated != actual || got.Total != 175 {
		t.Fatal("measurement lost on restart")
	}
	testExec(t, s, `UPDATE products SET stock=1000000 WHERE id=?`, p.ID)
	c := weightCommand(t, s, id, owner.ID, p.ID, actual-1, "restock")
	pv, err := s.PreviewWeight(id, owner.ID, false, c)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := databaseFingerprint(s.db)
	if err = s.ConfirmWeight(id, owner.ID, false, pv.Command); !errors.Is(err, ErrStockCapacity) {
		t.Fatal("return over capacity", err)
	}
	after, _ := databaseFingerprint(s.db)
	if before != after {
		t.Fatal("capacity failure mutated")
	}
	c.Disposition = "writeoff"
	pv, err = s.PreviewWeight(id, owner.ID, false, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmWeight(id, owner.ID, false, pv.Command); err != nil {
		t.Fatal(err)
	}
}

func TestWeightedUnreservedCartLocksUnitAndStep(t *testing.T) {
	s, clock, _ := newReservationStore(t)
	p := weightedProduct(t, s, 1000, 349, 25)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, 525)
	clock.at(clock.now().Add(basketHold).Unix())
	if err := s.ExpireHolds(); err != nil {
		t.Fatal(err)
	}
	p = testProduct(t, s, p.ID)
	p.QuantityStep = 50
	if _, err := s.SaveProduct(p); !errors.Is(err, ErrUnitLocked) {
		t.Fatal("step invalidated saved grams", err)
	}
	testExec(t, s, `UPDATE products SET stock=0 WHERE id=?`, p.ID)
	p = testProduct(t, s, p.ID)
	p.SaleUnit = "each"
	p.PriceBasis = 1
	p.QuantityStep = 1
	if _, err := s.SaveProduct(p); !errors.Is(err, ErrUnitLocked) {
		t.Fatal("unit reinterpreted unreserved cart", err)
	}
}

func TestWeightedCompetingActualAllocationsDoNotOversell(t *testing.T) {
	s, clock, path := newReservationStore(t)
	p := weightedProduct(t, s, 1300, 349, 1)
	a, b := testSession(t, s, ""), testSession(t, s, "")
	testCart(t, s, a.ID, p.ID, 500)
	idA := testCheckout(t, s, a.ID)
	testCart(t, s, b.ID, p.ID, 500)
	idB := testCheckout(t, s, b.ID)
	second := reservationStore(t, path, clock)
	pa, err := s.PreviewWeight(idA, a.ID, false, weightCommand(t, s, idA, a.ID, p.ID, 750, ""))
	if err != nil {
		t.Fatal(err)
	}
	pb, err := second.PreviewWeight(idB, b.ID, false, weightCommand(t, s, idB, b.ID, p.ID, 750, ""))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- s.ConfirmWeight(idA, a.ID, false, pa.Command) }()
	go func() { <-start; results <- second.ConfirmWeight(idB, b.ID, false, pb.Command) }()
	close(start)
	success, short := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrStock) {
			short++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || short != 1 || testProduct(t, s, p.ID).Stock != 50 {
		t.Fatal("actual allocation oversold", success, short)
	}
	allocated := testOrder(t, s, idA, a.ID).WorkingItems[0].Allocated + testOrder(t, s, idB, b.ID).WorkingItems[0].Allocated
	if allocated != 1250 {
		t.Fatal("failed allocation partially changed", allocated)
	}
}

func TestWeightedReadyRejectsOutOfBoundsSnapshotBeforeArithmetic(t *testing.T) {
	s := newTestStore(t)
	p := weightedProduct(t, s, 1000, 349, 1)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, 500)
	id := testCheckout(t, s, owner.ID)
	confirmWeight(t, s, id, owner.ID, p.ID, 527, "")
	// Older SQL schemas allowed a wider price range than the application did.
	// A malformed imported snapshot must fail closed before SQLite promotes an
	// overflowing integer multiplication into a floating-point final amount.
	testExec(t, s, `UPDATE working_order_items SET price=? WHERE order_id=?`, int64(math.MaxInt64), id)
	before, _ := databaseFingerprint(s.db)
	if err := s.Advance(id, "Picking"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsafe snapshot finalized", err)
	}
	after, _ := databaseFingerprint(s.db)
	if before != after {
		t.Fatal("invalid finalization mutated order")
	}
}

func TestWeightedSubstitutionRetainsAcceptedDestinationBag(t *testing.T) {
	s := newTestStore(t)
	source := weightedProduct(t, s, 2000, 349, 50)
	dest := weightedProduct(t, s, 2000, 499, 50)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, source.ID, 500)
	testCart(t, s, owner.ID, dest.ID, 500)
	id := testCheckout(t, s, owner.ID)
	confirmWeight(t, s, id, owner.ID, dest.ID, 800, "")
	c := overrideCommand(t, s, id, owner.ID, "substitute", source.ID, 100)
	c.ReplacementID = dest.ID
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	o := testOrder(t, s, id, owner.ID)
	l := o.WorkingItems[0]
	if l.ProductID != dest.ID || l.Quantity != 600 || l.Allocated != 900 || l.Picked != 0 || l.Measured || testProduct(t, s, dest.ID).Stock != 1100 || testProduct(t, s, source.ID).Stock != 2000 {
		t.Fatalf("destination allocation lost %+v", o)
	}
	confirmWeight(t, s, id, owner.ID, dest.ID, 890, "restock")
	if testProduct(t, s, dest.ID).Stock != 1110 {
		t.Fatal("remeasurement did not use combined allocation")
	}
}

func TestWeightedRemovingPositiveLineKeepsZeroRemainderRemovable(t *testing.T) {
	s, clock, _ := newReservationStore(t)
	tiny := weightedProduct(t, s, 1000, 1, 1)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	testCart(t, s, owner.ID, tiny.ID, 1)
	before := testBasket(t, s, owner.ID)
	clock.at(clock.now().Unix() + 30)
	if err := s.SetCart(owner.ID, 1, 0, false); err != nil {
		t.Fatal("positive line could not be removed", err)
	}
	after := testBasket(t, s, owner.ID)
	if len(after.Lines) != 1 || after.Lines[0].Product.ID != tiny.ID || after.Total != 0 || after.CanCheckout || after.HoldUntil != before.HoldUntil || after.Lines[0].Reserved != 1 {
		t.Fatalf("zero remainder %+v", after)
	}
	assertReservedStock(t, s, tiny.ID, 999, 1)
	if err := s.SetCart(owner.ID, tiny.ID, 0, false); err != nil {
		t.Fatal(err)
	}
	assertReservedStock(t, s, tiny.ID, 1000, 0)
}

func TestWeightedUnchangedTargetSurvivesArchive(t *testing.T) {
	s := newTestStore(t)
	p := weightedProduct(t, s, 1000, 349, 50)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, 500)
	id := testCheckout(t, s, owner.ID)
	confirmWeight(t, s, id, owner.ID, p.ID, 480, "restock")
	p = testProduct(t, s, p.ID)
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	c := overrideCommand(t, s, id, owner.ID, "set", p.ID, 500)
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	l := testOrder(t, s, id, owner.ID).WorkingItems[0]
	if l.Quantity != 500 || l.Allocated != 480 || l.Picked != 480 || !l.Measured {
		t.Fatal("unchanged target lost archived measurement", l)
	}
}
