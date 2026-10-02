package shop

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func overrideCommand(t *testing.T, s *Store, id int64, sid, action string, pid, qty int64) OrderCommand {
	t.Helper()
	products, err := s.Products("", "")
	if err != nil {
		t.Fatal(err)
	}
	return OrderCommand{CatalogQuote: orderCatalogQuote(products), Action: action, Version: testOrder(t, s, id, sid).Version, Key: token(), Reason: "Manager verified requested change", ProductID: pid, Quantity: qty, Disposition: "restock", Remainder: "unavailable"}
}
func placedSnapshot(t *testing.T, s *Store, id int64) [][]any {
	t.Helper()
	return migrationQuerySnapshot(t, s.db, fmt.Sprintf(`SELECT product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,subtotal FROM order_items WHERE order_id=%d ORDER BY product_id`, id))
}
func TestOrderOverridesWorkingReceiptStockAndReplay(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	before := placedSnapshot(t, s, id)
	placedTotal := testOrder(t, s, id, owner.ID).Total
	originalStock := testProduct(t, s, 1).Stock
	addedStock := testProduct(t, s, 3).Stock
	c := overrideCommand(t, s, id, owner.ID, "set", 1, 4)
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	after := testOrder(t, s, id, owner.ID)
	if after.Items[0].Quantity != 2 || after.WorkingItems[0].Quantity != 4 || testProduct(t, s, 1).Stock != originalStock-2 {
		t.Fatal("quantity failed or receipt changed")
	}
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal("exact replay", err)
	}
	if testCount(t, s, "order_events") != 1 || testProduct(t, s, 1).Stock != originalStock-2 {
		t.Fatal("duplicate audit or deduction")
	}
	c.Quantity = 5
	if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay", err)
	}
	c = overrideCommand(t, s, id, owner.ID, "set", 3, 2)
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	if testProduct(t, s, 3).Stock != addedStock-2 {
		t.Fatal("new product did not reserve")
	}
	c = overrideCommand(t, s, id, owner.ID, "substitute", 1, 1)
	c.ReplacementID = 3
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	after = testOrder(t, s, id, owner.ID)
	if len(after.WorkingItems) != 2 || testProduct(t, s, 1).Stock != originalStock+2 || testProduct(t, s, 3).Stock != addedStock-3 {
		t.Fatalf("substitution allocations %+v", after.WorkingItems)
	}
	c = overrideCommand(t, s, id, owner.ID, "set", 3, 0)
	c.Disposition = "writeoff"
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	if testProduct(t, s, 3).Stock != addedStock-3 {
		t.Fatal("writeoff invented stock")
	}
	if !reflect.DeepEqual(before, placedSnapshot(t, s, id)) || testOrder(t, s, id, owner.ID).Total != placedTotal {
		t.Fatal("placed receipt mutated")
	}
	if testCount(t, s, "adjustments") != 0 {
		t.Fatal("private order reason leaked into shared stock history")
	}
}
func TestOrderOverridesPartialCompletionAndCancellation(t *testing.T) {
	for _, action := range []string{"finish", "cancel"} {
		for _, disposition := range []string{"restock", "writeoff"} {
			for _, picked := range []int64{0, 1} {
				t.Run(fmt.Sprintf("%s/%s/%d", action, disposition, picked), func(t *testing.T) {
					s := newTestStore(t)
					owner, id := pickingFixture(t, s)
					stock := testProduct(t, s, 1).Stock
					before := placedSnapshot(t, s, id)
					if err := s.Advance(id, "Placed"); err != nil {
						t.Fatal(err)
					}
					if err := s.RecordPicked(id, 1, picked, 1, owner.ID, false); err != nil {
						t.Fatal(err)
					}
					c := overrideCommand(t, s, id, owner.ID, action, 0, 0)
					c.Disposition = disposition
					if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
						t.Fatal(err)
					}
					o := testOrder(t, s, id, owner.ID)
					wantTotal := picked * o.Items[0].Price
					if action == "cancel" {
						wantTotal = 0
					}
					if o.Status != "Completed" || !o.Finalized || o.FinalTotal != wantTotal || o.PickedCount != picked || o.RequiredCount != 3 || o.AllPicked || o.Percent == 100 {
						t.Fatalf("untruthful completion %+v", o)
					}
					wantStock := stock
					if disposition == "restock" {
						if action == "cancel" {
							wantStock += 2
						} else {
							wantStock += 2 - picked
						}
					}
					if testProduct(t, s, 1).Stock != wantStock {
						t.Fatal("incorrect stock disposition")
					}
					if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
						t.Fatal("retry finalization", err)
					}
					if testProduct(t, s, 1).Stock != wantStock {
						t.Fatal("double restock")
					}
					c = overrideCommand(t, s, id, owner.ID, "set", 1, 3)
					if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrTerminal) {
						t.Fatal("terminal reopen", err)
					}
					if !reflect.DeepEqual(before, placedSnapshot(t, s, id)) {
						t.Fatal("receipt changed")
					}
				})
			}
		}
	}
}
func TestOrderOverridesAtomicFailureAndCapacity(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	for _, tc := range []struct {
		name   string
		mutate func(*OrderCommand)
		want   error
	}{
		{"stale", func(c *OrderCommand) { c.Version++ }, ErrConflict},
		{"reason", func(c *OrderCommand) { c.Reason = "bad\nreason" }, ErrInvalid},
		{"invalid disposition", func(c *OrderCommand) { c.Quantity = 0; c.Disposition = "unknown" }, ErrInvalid},
		{"out of stock substitute", func(c *OrderCommand) { c.Action = "substitute"; c.ReplacementID = 3; c.Quantity = 99 }, ErrStock},
		{"same substitute", func(c *OrderCommand) { c.Action = "substitute"; c.ReplacementID = 1 }, ErrInvalid},
		{"unknown substitute", func(c *OrderCommand) { c.Action = "substitute"; c.ReplacementID = 999 }, ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := databaseFingerprint(s.db)
			c := overrideCommand(t, s, id, owner.ID, "set", 1, 3)
			tc.mutate(&c)
			if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, tc.want) {
				t.Fatalf("%v want %v", err, tc.want)
			}
			after, _ := databaseFingerprint(s.db)
			if before != after {
				t.Fatal("failed command had side effects")
			}
		})
	}
	// Manager restocks can consume room previously occupied by an order allocation.
	testExec(t, s, `UPDATE products SET stock=9999 WHERE id=1`)
	c := overrideCommand(t, s, id, owner.ID, "set", 1, 0)
	if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrStockCapacity) {
		t.Fatal("overflow", err)
	}
	if testProduct(t, s, 1).Stock != 9999 || testOrder(t, s, id, owner.ID).WorkingItems[0].Quantity != 2 {
		t.Fatal("overflow was clamped/partially committed")
	}
	c.Disposition = "writeoff"
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
}
func TestOrderOverridesConcurrentConnectionsAndPickInvalidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	first, second := openTestStore(t, path), openTestStore(t, path)
	owner, id := pickingFixture(t, first)
	c := overrideCommand(t, first, id, owner.ID, "set", 1, 3)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, s := range []*Store{first, second} {
		go func(i int, s *Store) {
			<-start
			copy := c
			copy.Key = fmt.Sprintf("different-request-key-%d", i)
			results <- s.OverrideOrder(id, owner.ID, false, copy)
		}(i, s)
	}
	close(start)
	wins, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 || testProduct(t, first, 1).Stock != 21 {
		t.Fatal("race oversold or duplicated")
	}
	if err := first.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	c = overrideCommand(t, first, id, owner.ID, "finish", 0, 0)
	line := testOrder(t, first, id, owner.ID).WorkingItems[0]
	if err := second.RecordPicked(id, 1, 1, line.PickVersion, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := first.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrConflict) {
		t.Fatal("old finish froze newer pick", err)
	}
}
func TestOrderOverridesResurrectionStableIDAndUnitLock(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	line := testOrder(t, s, id, owner.ID).WorkingItems[0]
	c := overrideCommand(t, s, id, owner.ID, "set", 1, 0)
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	c = overrideCommand(t, s, id, owner.ID, "set", 1, 1)
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	now := testOrder(t, s, id, owner.ID).WorkingItems[0]
	if now.LineID != line.LineID || now.PickVersion <= line.PickVersion {
		t.Fatal("unstable resurrection identity")
	}
	if err := s.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPicked(id, 1, 1, line.PickVersion, owner.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatal("old pick form accepted after removal", err)
	}
}

func TestOrderOverridesAuditRollbackReadyFreezeAndAddedUnitLock(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	c := overrideCommand(t, s, id, owner.ID, "set", 3, 1)
	testExec(t, s, `CREATE TRIGGER reject_order_audit BEFORE INSERT ON order_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	before, _ := databaseFingerprint(s.db)
	if err := s.OverrideOrder(id, owner.ID, false, c); err == nil {
		t.Fatal("audit failure should abort")
	}
	after, _ := databaseFingerprint(s.db)
	if before != after {
		t.Fatal("audit failure partially changed allocation")
	}
	testExec(t, s, `DROP TRIGGER reject_order_audit`)
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal("retry", err)
	}
	// Working-only historical use locks units, even after removing the line and stock.
	remove := overrideCommand(t, s, id, owner.ID, "set", 3, 0)
	if err := s.OverrideOrder(id, owner.ID, false, remove); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `UPDATE products SET stock=0 WHERE id=3`)
	p := testProduct(t, s, 3)
	p.SaleUnit = "g"
	p.PriceBasis = 1000
	p.QuantityStep = 10
	if _, err := s.SaveProduct(p); !errors.Is(err, ErrUnitLocked) {
		t.Fatal("working-only product unit changed", err)
	}
	if err := s.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	for _, line := range testOrder(t, s, id, owner.ID).WorkingItems {
		if err := s.RecordPicked(id, line.ProductID, line.Quantity, line.PickVersion, owner.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Advance(id, "Picking"); err != nil {
		t.Fatal(err)
	}
	ready := testOrder(t, s, id, owner.ID)
	if !ready.Finalized || ready.FinalTotal != ready.WorkingTotal {
		t.Fatal("Ready did not freeze amount")
	}
	for _, action := range []string{"set", "substitute", "finish", "cancel"} {
		c = overrideCommand(t, s, id, owner.ID, action, 1, 1)
		c.ReplacementID = 2
		if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrTerminal) {
			t.Fatalf("Ready %s %v", action, err)
		}
	}
	if err := s.AdvanceVersioned(id, "Ready", ready.Version-1, owner.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale collect accepted")
	}
	if err := s.AdvanceVersioned(id, "Ready", ready.Version, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	if testOrder(t, s, id, owner.ID).FinalTotal != ready.FinalTotal {
		t.Fatal("collection recalculated frozen final")
	}
}
func TestOrderOverridesReturnCapacityIncludesHeldBaskets(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	testExec(t, s, `UPDATE products SET stock=9999 WHERE id=1`)
	other := testSession(t, s, "")
	testCart(t, s, other.ID, 1, 1)
	if testProduct(t, s, 1).Stock != 9998 {
		t.Fatal("fixture hold")
	}
	c := overrideCommand(t, s, id, owner.ID, "set", 1, 0)
	if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrStockCapacity) {
		t.Fatal("return ignored held basket", err)
	}
}
func TestOrderOverridesConcurrentIdenticalFinishReturnsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	first, second := openTestStore(t, path), openTestStore(t, path)
	owner, id := pickingFixture(t, first)
	c := overrideCommand(t, first, id, owner.ID, "finish", 0, 0)
	c.Remainder = "cancelled"
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, s := range []*Store{first, second} {
		go func(s *Store) { <-start; results <- s.OverrideOrder(id, owner.ID, false, c) }(s)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if testProduct(t, first, 1).Stock != 24 || testCount(t, first, "order_events") != 1 {
		t.Fatal("idempotent finish returned stock twice")
	}
	o := testOrder(t, first, id, owner.ID)
	if o.FinalTotal != 0 || o.WorkingItems[0].Cancelled != 2 {
		t.Fatal("cancelled remainder incorrect")
	}
}

func TestOrderOverridesTwoOrdersCompeteForLastUnit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	first, second := openTestStore(t, path), openTestStore(t, path)
	owner, id := pickingFixture(t, first)
	other, otherID := pickingFixture(t, first)
	testExec(t, first, `UPDATE products SET stock=1 WHERE id=3`)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, store := range []*Store{first, second} {
		oid, sid := id, owner.ID
		if i == 1 {
			oid, sid = otherID, other.ID
		}
		command := overrideCommand(t, first, oid, sid, "set", 3, 1)
		go func(s *Store, oid int64, sid string, c OrderCommand) {
			<-start
			results <- s.OverrideOrder(oid, sid, false, c)
		}(store, oid, sid, command)
	}
	close(start)
	wins, stockFailures := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			wins++
		} else if errors.Is(err, ErrStock) {
			stockFailures++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || stockFailures != 1 || testProduct(t, first, 3).Stock != 0 {
		t.Fatal("last unit oversold")
	}
}

func TestOrderOverridesCatalogQuoteAndRetainedMergedRate(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	stale := overrideCommand(t, s, id, owner.ID, "set", 3, 1)
	p := testProduct(t, s, 3)
	p.Price += 100
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	before, _ := databaseFingerprint(s.db)
	if err := s.OverrideOrder(id, owner.ID, false, stale); !errors.Is(err, ErrOrderQuote) {
		t.Fatal("stale displayed price accepted", err)
	}
	after, _ := databaseFingerprint(s.db)
	if before != after {
		t.Fatal("quote rejection changed stock or audit")
	}
	c := overrideCommand(t, s, id, owner.ID, "substitute", 1, 2)
	c.ReplacementID = 3
	// An unrelated stock-only version bump must not require another price review.
	testExec(t, s, `UPDATE products SET stock=stock+1,version=version+1 WHERE id=3`)
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	o := testOrder(t, s, id, owner.ID)
	var replacement WorkingOrderItem
	for _, line := range o.WorkingItems {
		if line.ProductID == 3 {
			replacement = line
		}
	}
	if err := s.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPicked(id, 3, 1, replacement.PickVersion, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	p = testProduct(t, s, 3)
	p.Price += 300
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	c = overrideCommand(t, s, id, owner.ID, "substitute", 2, 1)
	c.ReplacementID = 3
	c.CatalogQuote = "ignored for existing snapshot"
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	o = testOrder(t, s, id, owner.ID)
	if len(o.WorkingItems) != 1 {
		t.Fatal("same replacement not merged")
	}
	line := o.WorkingItems[0]
	if line.LineID != replacement.LineID || line.Price != replacement.Price || line.Quantity != 3 || line.Picked != 1 {
		t.Fatal("merge lost rate, identity or picks", line)
	}
	if o.Items[0].Quantity != 2 || o.Items[1].Quantity != 1 || len(o.Events) < 4 {
		t.Fatal("source history lost")
	}
}
func TestOrderOverridesReclaimsExpiredHoldAndValidatesReason(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	other := testSession(t, s, "")
	stock := testProduct(t, s, 3).Stock
	testCart(t, s, other.ID, 3, stock)
	c := overrideCommand(t, s, id, owner.ID, "set", 3, 1)
	now := s.now()
	s.now = func() time.Time { return now.Add(16 * time.Minute) }
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal("expired hold still blocked allocation", err)
	}
	if testProduct(t, s, 3).Stock != stock-1 || !testBasket(t, s, other.ID).NeedsReview {
		t.Fatal("expiry reconciliation inaccurate")
	}
	for _, reason := range []string{"bad\x00reason", "bad\treason", "bad\nreason", string([]byte{'b', 'a', 'd', 0xff})} {
		c = overrideCommand(t, s, id, owner.ID, "set", 1, 3)
		c.Reason = reason
		if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsafe reason accepted", err)
		}
	}
}
func TestOrderOverridesAllPickedFinishRequiresCollection(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	if err := s.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	for _, line := range testOrder(t, s, id, owner.ID).WorkingItems {
		if err := s.RecordPicked(id, line.ProductID, line.Quantity, line.PickVersion, owner.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	c := overrideCommand(t, s, id, owner.ID, "finish", 0, 0)
	if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrUseReady) {
		t.Fatal("finish falsely claimed collection", err)
	}
	if testOrder(t, s, id, owner.ID).Status != "Picking" {
		t.Fatal("all-picked finish changed state")
	}
}
