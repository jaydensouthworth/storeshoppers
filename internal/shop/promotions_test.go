package shop

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func promotionTestStore(t *testing.T) (*Store, *reservationClock, string) {
	t.Helper()
	clock := newReservationClock()
	clock.at(time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC).Unix())
	path := t.TempDir() + "/promotions.db"
	return reservationStore(t, path, clock), clock, path
}

func testPromotion(t *testing.T, s *Store, id int64) Promotion {
	t.Helper()
	all, err := s.Promotions()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("promotion %d absent", id)
	return Promotion{}
}

func saveTestPromotion(t *testing.T, s *Store, pid, price, starts, ends int64) Promotion {
	t.Helper()
	id, err := s.SavePromotion(Promotion{ProductID: pid, ProductVersion: testCatalogProduct(t, s, pid).PriceVersion, SalePrice: price, Starts: starts, Ends: ends})
	if err != nil {
		t.Fatal(err)
	}
	return testPromotion(t, s, id)
}

func assertPromotionUnchanged(t *testing.T, s *Store, before string) {
	t.Helper()
	if got := fingerprintTest(t, s.db); got != before {
		t.Fatal("rejected promotion operation changed persistent rows or sequences")
	}
}

func promotionWorkingLine(t *testing.T, s *Store, id int64, sid string, pid int64) WorkingOrderItem {
	t.Helper()
	for _, line := range testOrder(t, s, id, sid).WorkingItems {
		if line.ProductID == pid {
			return line
		}
	}
	t.Fatalf("working product %d absent from order %d", pid, id)
	return WorkingOrderItem{}
}

func TestPromotionsClockBoundariesAndRegularPrice(t *testing.T) {
	s, clock, _ := promotionTestStore(t)
	now := clock.now().Unix()
	regular := testProduct(t, s, 1)
	sale := saveTestPromotion(t, s, 1, 249, now+10, now+70)
	if current := testProduct(t, s, 1); current.Price != regular.Price || current.PriceVersion != regular.PriceVersion+1 || current.Version != regular.Version || current.CatalogVersion != regular.CatalogVersion {
		t.Fatalf("sale creation altered regular price, stock version, or catalog version: %+v", current)
	}
	regular = testProduct(t, s, 1)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 2)
	var beforeStartQuote, duringQuote string
	for _, tc := range []struct {
		name      string
		at, price int64
		status    string
		onSale    bool
	}{
		{"before start", now + 9, 349, "Upcoming", false},
		{"inclusive start", now + 10, 249, "Active", true},
		{"before end", now + 69, 249, "Active", true},
		{"exclusive end", now + 70, 349, "Expired", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock.at(tc.at)
			basket := testBasket(t, s, owner.ID)
			for source, p := range map[string]Product{"storefront": testProduct(t, s, 1), "manager": testCatalogProduct(t, s, 1), "basket": basket.Lines[0].Product} {
				if p.Price != regular.Price || p.PriceVersion != regular.PriceVersion || p.EffectivePrice() != tc.price || p.OnSale() != tc.onSale {
					t.Errorf("%s has inconsistent regular/sale pricing: %+v", source, p)
				}
				if tc.onSale && (p.PromotionID != sale.ID || p.PromotionVersion != sale.Version || p.SaleStarts != sale.Starts || p.SaleEnds != sale.Ends) {
					t.Errorf("%s lost the active sale identity: %+v", source, p)
				}
			}
			if basket.Total != 2*tc.price || basket.Lines[0].Subtotal != 2*tc.price || !basket.CanCheckout {
				t.Fatalf("incorrect basket at boundary: %+v", basket)
			}
			if p := testPromotion(t, s, sale.ID); p.Status != tc.status {
				t.Errorf("status=%s, want %s", p.Status, tc.status)
			}
			switch tc.name {
			case "before start":
				beforeStartQuote = basket.Quote
			case "inclusive start":
				duringQuote = basket.Quote
			case "before end":
				if basket.Quote != duringQuote {
					t.Error("time alone changed an unchanged active quote")
				}
			case "exclusive end":
				if basket.Quote == duringQuote {
					t.Error("expiry did not invalidate sale quote")
				}
			}
		})
	}
	if beforeStartQuote == duringQuote {
		t.Fatal("activation did not invalidate regular quote")
	}
}

func TestPromotionsBasketQuoteChangesPreserveLiveHolds(t *testing.T) {
	for _, change := range []string{"start", "end", "full interval", "same-rate edit", "cancel"} {
		t.Run(change, func(t *testing.T) {
			s, clock, _ := promotionTestStore(t)
			now := clock.now().Unix()
			starts := now - 10
			if change == "start" || change == "full interval" {
				starts = now + 10
			}
			sale := saveTestPromotion(t, s, 1, 249, starts, now+60)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 2)
			old := testBasket(t, s, owner.ID)
			switch change {
			case "start":
				clock.at(sale.Starts)
			case "end", "full interval":
				clock.at(sale.Ends)
			case "same-rate edit":
				if _, err := s.SavePromotion(sale); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if err := s.CancelPromotion(sale.ID, sale.Version); err != nil {
					t.Fatal(err)
				}
			}
			fresh := testBasket(t, s, owner.ID)
			if fresh.Quote == old.Quote {
				t.Fatal("sale change retained old checkout quote")
			}
			if fresh.HoldUntil != old.HoldUntil || fresh.Revision != old.Revision || fresh.Lines[0].Reserved != 2 {
				t.Fatal("sale change altered the live hold")
			}
			if (change == "same-rate edit" || change == "full interval") && fresh.Total != old.Total {
				t.Fatal("unchanged effective rate changed total")
			}
			before := fingerprintTest(t, s.db)
			if id, err := s.Checkout(owner.ID, owner.CheckoutKey, old.Revision, old.Quote); id != 0 || !errors.Is(err, ErrQuote) {
				t.Fatalf("old checkout quote=%d, %v; want ErrQuote", id, err)
			}
			assertPromotionUnchanged(t, s, before)
			assertReservedStock(t, s, 1, 22, 2)
			id, err := s.Checkout(owner.ID, owner.CheckoutKey, fresh.Revision, fresh.Quote)
			if err != nil {
				t.Fatal("fresh checkout", err)
			}
			o := testOrder(t, s, id, owner.ID)
			if o.Total != fresh.Total || o.Items[0].Price != fresh.Lines[0].Product.EffectivePrice() || o.WorkingItems[0].Price != o.Items[0].Price {
				t.Fatalf("checkout did not snapshot the reviewed effective price: %+v", o)
			}
			assertReservedStock(t, s, 1, 22, 0)
		})
	}
}

func TestPromotionsNewWorkingQuoteChangesAreAtomic(t *testing.T) {
	for _, change := range []string{"start", "end", "full interval", "same-rate edit", "cancel"} {
		t.Run(change, func(t *testing.T) {
			s, clock, _ := promotionTestStore(t)
			owner, id := pickingFixture(t, s)
			receipt := placedSnapshot(t, s, id)
			now := clock.now().Unix()
			starts := now - 10
			if change == "start" || change == "full interval" {
				starts = now + 10
			}
			sale := saveTestPromotion(t, s, 3, 399, starts, now+60)
			command := overrideCommand(t, s, id, owner.ID, "set", 3, 2)
			switch change {
			case "start":
				clock.at(sale.Starts)
			case "end", "full interval":
				clock.at(sale.Ends)
			case "same-rate edit":
				if _, err := s.SavePromotion(sale); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if err := s.CancelPromotion(sale.ID, sale.Version); err != nil {
					t.Fatal(err)
				}
			}
			before := fingerprintTest(t, s.db)
			if err := s.OverrideOrder(id, owner.ID, false, command); !errors.Is(err, ErrOrderQuote) {
				t.Fatalf("old catalog quote=%v, want ErrOrderQuote", err)
			}
			assertPromotionUnchanged(t, s, before)
			command = overrideCommand(t, s, id, owner.ID, "set", 3, 2)
			price, stock := testProduct(t, s, 3).EffectivePrice(), testProduct(t, s, 3).Stock
			if err := s.OverrideOrder(id, owner.ID, false, command); err != nil {
				t.Fatal(err)
			}
			line := promotionWorkingLine(t, s, id, owner.ID, 3)
			if line.Price != price || line.Subtotal != 2*price || testProduct(t, s, 3).Stock != stock-2 {
				t.Fatalf("new working line failed to snapshot sale: %+v", line)
			}
			if !reflect.DeepEqual(receipt, placedSnapshot(t, s, id)) {
				t.Fatal("new sale line rewrote the placed receipt")
			}
		})
	}
}

func TestPromotionsExistingAndTombstoneSnapshotsStayImmutable(t *testing.T) {
	s, clock, _ := promotionTestStore(t)
	now := clock.now().Unix()
	appleSale := saveTestPromotion(t, s, 1, 249, now-1, now+600)
	owner, id := pickingFixture(t, s)
	receipt, total := placedSnapshot(t, s, id), testOrder(t, s, id, owner.ID).Total
	apple := promotionWorkingLine(t, s, id, owner.ID, 1)
	if apple.Price != 249 {
		t.Fatal("initial working line lost checkout sale")
	}
	for _, quantity := range []int64{0, 3} {
		if quantity == 3 {
			if err := s.CancelPromotion(appleSale.ID, appleSale.Version); err != nil {
				t.Fatal(err)
			}
			saveTestPromotion(t, s, 1, 199, now-1, now+600)
		}
		command := overrideCommand(t, s, id, owner.ID, "set", 1, quantity)
		command.CatalogQuote = "stale quote is irrelevant to an existing snapshot"
		if err := s.OverrideOrder(id, owner.ID, false, command); err != nil {
			t.Fatal(err)
		}
	}
	resurrected := promotionWorkingLine(t, s, id, owner.ID, 1)
	if resurrected.LineID != apple.LineID || resurrected.Price != apple.Price || resurrected.Subtotal != 3*apple.Price {
		t.Fatalf("receipt tombstone was repriced: %+v", resurrected)
	}
	breadSale := saveTestPromotion(t, s, 3, 399, now-1, now+600)
	command := overrideCommand(t, s, id, owner.ID, "set", 3, 1)
	if err := s.OverrideOrder(id, owner.ID, false, command); err != nil {
		t.Fatal(err)
	}
	bread := promotionWorkingLine(t, s, id, owner.ID, 3)
	breadSale.SalePrice = 299
	if _, err := s.SavePromotion(breadSale); err != nil {
		t.Fatal(err)
	}
	for _, quantity := range []int64{2, 0, 1} {
		if quantity == 1 {
			breadSale = testPromotion(t, s, breadSale.ID)
			if err := s.CancelPromotion(breadSale.ID, breadSale.Version); err != nil {
				t.Fatal(err)
			}
		}
		command = overrideCommand(t, s, id, owner.ID, "set", 3, quantity)
		command.CatalogQuote = "stale"
		if err := s.OverrideOrder(id, owner.ID, false, command); err != nil {
			t.Fatal(err)
		}
		if quantity > 0 {
			line := promotionWorkingLine(t, s, id, owner.ID, 3)
			if line.LineID != bread.LineID || line.Price != 399 || line.Subtotal != quantity*399 {
				t.Fatalf("manager-added snapshot was repriced: %+v", line)
			}
		}
	}
	if !reflect.DeepEqual(receipt, placedSnapshot(t, s, id)) || testOrder(t, s, id, owner.ID).Total != total {
		t.Fatal("sale mutation rewrote immutable receipt")
	}
}

func TestPromotionsFeaturedChangesDoNotInvalidatePriceQuotes(t *testing.T) {
	s, _, _ := promotionTestStore(t)
	orderOwner, id := pickingFixture(t, s)
	command := overrideCommand(t, s, id, orderOwner.ID, "set", 3, 1)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 3, 1)
	basket := testBasket(t, s, owner.ID)
	product := testProduct(t, s, 3)
	for _, featured := range []bool{true, false, true} {
		current := testProduct(t, s, 3)
		if err := s.SetFeatured(3, current.FeatureVersion, featured); err != nil {
			t.Fatal(err)
		}
		after := testProduct(t, s, 3)
		if after.Featured != featured || after.FeatureVersion != current.FeatureVersion+1 || after.Price != product.Price || after.PriceVersion != product.PriceVersion {
			t.Fatalf("feature update changed pricing or lost version: %+v", after)
		}
		if quote := testBasket(t, s, owner.ID).Quote; quote != basket.Quote {
			t.Fatal("merchandising change invalidated basket rate")
		}
		fresh := overrideCommand(t, s, id, orderOwner.ID, "set", 3, 1)
		if fresh.CatalogQuote != command.CatalogQuote {
			t.Fatal("merchandising change invalidated working rate")
		}
	}
	before := fingerprintTest(t, s.db)
	if err := s.SetFeatured(3, 1, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale feature=%v", err)
	}
	assertPromotionUnchanged(t, s, before)
	if err := s.OverrideOrder(id, orderOwner.ID, false, command); err != nil {
		t.Fatal("featured old working quote", err)
	}
	if _, err := s.Checkout(owner.ID, owner.CheckoutKey, basket.Revision, basket.Quote); err != nil {
		t.Fatal("featured old basket quote", err)
	}
}

func TestPromotionsRegularPriceEditsInvalidateQuotesAndSaleEditors(t *testing.T) {
	s, clock, _ := promotionTestStore(t)
	orderOwner, id := pickingFixture(t, s)
	sale := saveTestPromotion(t, s, 3, 399, clock.now().Unix()-1, clock.now().Unix()+600)
	command := overrideCommand(t, s, id, orderOwner.ID, "set", 3, 1)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 3, 1)
	basket := testBasket(t, s, owner.ID)
	product := testCatalogProduct(t, s, 3)
	product.Price = 699
	if _, err := s.SaveProduct(product); err != nil {
		t.Fatal(err)
	}
	current := testProduct(t, s, 3)
	if current.Price != 699 || current.EffectivePrice() != 399 || current.PriceVersion != product.PriceVersion+1 {
		t.Fatalf("regular price edit lost sale: %+v", current)
	}
	before := fingerprintTest(t, s.db)
	if _, err := s.Checkout(owner.ID, owner.CheckoutKey, basket.Revision, basket.Quote); !errors.Is(err, ErrQuote) {
		t.Fatalf("changed regular price accepted old basket quote: %v", err)
	}
	if err := s.OverrideOrder(id, orderOwner.ID, false, command); !errors.Is(err, ErrOrderQuote) {
		t.Fatalf("changed regular price accepted old working quote: %v", err)
	}
	if _, err := s.SavePromotion(sale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale regular price in sale editor=%v", err)
	}
	assertPromotionUnchanged(t, s, before)
	if testBasket(t, s, owner.ID).Total != basket.Total {
		t.Fatal("unchanged sale rate changed basket total")
	}
}

func TestPromotionsGuardRegularPriceAndSellingUnit(t *testing.T) {
	for _, state := range []string{"active", "upcoming", "expired", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			s, clock, _ := promotionTestStore(t)
			product := testCreateProduct(t, s, nil)
			now, starts, ends := clock.now().Unix(), clock.now().Unix()-1, clock.now().Unix()+600
			if state == "upcoming" {
				starts, ends = now+600, now+1200
			}
			if state == "expired" {
				starts, ends = now-600, now
			}
			sale := saveTestPromotion(t, s, product.ID, 399, starts, ends)
			if state == "cancelled" {
				if err := s.CancelPromotion(sale.ID, sale.Version); err != nil {
					t.Fatal(err)
				}
			}
			for _, change := range []string{"equal", "below", "unit"} {
				p := testCatalogProduct(t, s, product.ID)
				switch change {
				case "equal":
					p.Price = 399
				case "below":
					p.Price = 299
				case "unit":
					p.SaleUnit, p.PriceBasis, p.QuantityStep = "g", 1000, 100
				}
				before := fingerprintTest(t, s.db)
				_, err := s.SaveProduct(p)
				if state == "active" || state == "upcoming" {
					if !errors.Is(err, ErrPromotionPrice) {
						t.Fatalf("%s change accepted during %s sale: %v", change, state, err)
					}
					assertPromotionUnchanged(t, s, before)
				} else if err != nil {
					t.Fatalf("%s historical sale blocked %s change: %v", state, change, err)
				}
			}
		})
	}
}

func TestPromotionsValidationAndStaleMutations(t *testing.T) {
	s, clock, _ := promotionTestStore(t)
	now := clock.now().Unix()
	valid := Promotion{ProductID: 1, ProductVersion: 1, SalePrice: 249, Starts: now - 1, Ends: now + 600}
	for _, tc := range []struct {
		name   string
		change func(*Promotion)
		want   error
	}{
		{"zero price", func(p *Promotion) { p.SalePrice = 0 }, ErrInvalid},
		{"too high", func(p *Promotion) { p.SalePrice = 1000001 }, ErrInvalid},
		{"equal regular", func(p *Promotion) { p.SalePrice = 349 }, ErrPromotionPrice},
		{"above regular", func(p *Promotion) { p.SalePrice = 350 }, ErrPromotionPrice},
		{"empty window", func(p *Promotion) { p.Ends = p.Starts }, ErrInvalid},
		{"reverse window", func(p *Promotion) { p.Ends = p.Starts - 1 }, ErrInvalid},
		{"negative start", func(p *Promotion) { p.Starts = -1 }, ErrInvalid},
		{"unsupported end", func(p *Promotion) { p.Ends = 253402300800 }, ErrInvalid},
		{"missing product version", func(p *Promotion) { p.ProductVersion = 0 }, ErrConflict},
		{"stale product", func(p *Promotion) { p.ProductVersion++ }, ErrConflict},
		{"unknown product", func(p *Promotion) { p.ProductID = 99999 }, ErrNotFound},
		{"cancelled create", func(p *Promotion) { p.Cancelled = true }, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.change(&p)
			before := fingerprintTest(t, s.db)
			if _, err := s.SavePromotion(p); !errors.Is(err, tc.want) {
				t.Fatalf("SavePromotion=%v, want %v", err, tc.want)
			}
			assertPromotionUnchanged(t, s, before)
		})
	}
	sale := saveTestPromotion(t, s, 1, 249, valid.Starts, valid.Ends)
	updated := sale
	if _, err := s.SavePromotion(updated); err != nil {
		t.Fatal(err)
	}
	if got := testPromotion(t, s, sale.ID); got.Version != sale.Version+1 || got.SalePrice != sale.SalePrice {
		t.Fatalf("same-rate edit failed to advance sale version: %+v", got)
	}
	sale.ProductVersion = testProduct(t, s, sale.ProductID).PriceVersion
	before := fingerprintTest(t, s.db)
	if _, err := s.SavePromotion(sale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit=%v", err)
	}
	if err := s.CancelPromotion(sale.ID, sale.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cancel=%v", err)
	}
	assertPromotionUnchanged(t, s, before)
	updated = testPromotion(t, s, sale.ID)
	if err := s.CancelPromotion(updated.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	before = fingerprintTest(t, s.db)
	if err := s.CancelPromotion(updated.ID, updated.Version+1); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate cancellation=%v", err)
	}
	updated.ProductVersion = testProduct(t, s, updated.ProductID).PriceVersion
	if _, err := s.SavePromotion(updated); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled sale reopened by editor=%v", err)
	}
	assertPromotionUnchanged(t, s, before)
}

func TestPromotionsOverlapAdjacencyAndConcurrentConnections(t *testing.T) {
	first, clock, path := promotionTestStore(t)
	second := reservationStore(t, path, clock)
	now := clock.now().Unix()
	p := Promotion{ProductID: 1, ProductVersion: 1, SalePrice: 249, Starts: now, Ends: now + 60}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, s := range []*Store{first, second} {
		go func(s *Store) { <-start; _, err := s.SavePromotion(p); results <- err }(s)
	}
	close(start)
	wins, overlaps := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			wins++
		} else if errors.Is(err, ErrPromotionOverlap) || errors.Is(err, ErrConflict) {
			overlaps++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || overlaps != 1 || testCount(t, first, "promotions") != 1 || testCount(t, first, "promotion_events") != 1 {
		t.Fatalf("overlap race wins=%d overlaps=%d", wins, overlaps)
	}
	left := saveTestPromotion(t, first, 1, 229, now-60, now)
	right := saveTestPromotion(t, first, 1, 239, now+60, now+120)
	saveTestPromotion(t, first, 3, 399, now, now+60)
	for _, window := range [][2]int64{{now - 1, now + 1}, {now, now + 1}, {now + 1, now + 59}, {now - 100, now + 200}} {
		copy := p
		copy.ProductVersion = testProduct(t, first, 1).PriceVersion
		copy.Starts, copy.Ends = window[0], window[1]
		before := fingerprintTest(t, first.db)
		if _, err := first.SavePromotion(copy); !errors.Is(err, ErrPromotionOverlap) {
			t.Fatalf("overlap %v=%v", window, err)
		}
		assertPromotionUnchanged(t, first, before)
	}
	right.Starts--
	before := fingerprintTest(t, first.db)
	if _, err := first.SavePromotion(right); !errors.Is(err, ErrPromotionOverlap) {
		t.Fatalf("overlapping edit=%v", err)
	}
	assertPromotionUnchanged(t, first, before)
	if err := first.CancelPromotion(left.ID, left.Version); err != nil {
		t.Fatal(err)
	}
	saveTestPromotion(t, first, 1, 219, now-60, now)
	// The database guards cover code paths outside the service, including updates.
	before = fingerprintTest(t, first.db)
	if _, err := first.db.Exec(`INSERT INTO promotions(product_id,sale_price,starts,ends) VALUES(1,199,?,?)`, now, now+1); err == nil {
		t.Fatal("database accepted direct overlap")
	}
	if _, err := first.db.Exec(`UPDATE promotions SET starts=? WHERE id=?`, now, right.ID); err == nil {
		t.Fatal("database accepted overlapping direct edit")
	}
	assertPromotionUnchanged(t, first, before)
}

func TestPromotionsAuditFailureRollsBackEveryMutation(t *testing.T) {
	for _, action := range []string{"create", "edit", "cancel", "feature", "unfeature", "examples"} {
		t.Run(action, func(t *testing.T) {
			s, clock, _ := promotionTestStore(t)
			now := clock.now().Unix()
			var sale Promotion
			if action == "edit" || action == "cancel" {
				sale = saveTestPromotion(t, s, 1, 249, now-1, now+600)
			}
			if action == "unfeature" {
				if err := s.SetFeatured(1, 0, true); err != nil {
					t.Fatal(err)
				}
			}
			var quote string
			if action == "examples" {
				var err error
				_, quote, err = s.ExampleSales()
				if err != nil {
					t.Fatal(err)
				}
			}
			// Examples fail after the first sale and feature were inserted, not at the first statement.
			condition := ""
			if action == "examples" {
				condition = " WHEN NEW.product_id=3"
			}
			testExec(t, s, `CREATE TRIGGER reject_promotion_audit BEFORE INSERT ON promotion_events`+condition+` BEGIN SELECT RAISE(ABORT,'promotion audit unavailable'); END`)
			before := fingerprintTest(t, s.db)
			var err error
			switch action {
			case "create":
				_, err = s.SavePromotion(Promotion{ProductID: 1, ProductVersion: 1, SalePrice: 249, Starts: now, Ends: now + 600})
			case "edit":
				sale.SalePrice = 239
				_, err = s.SavePromotion(sale)
			case "cancel":
				err = s.CancelPromotion(sale.ID, sale.Version)
			case "feature":
				err = s.SetFeatured(1, 0, true)
			case "unfeature":
				err = s.SetFeatured(1, 1, false)
			case "examples":
				err = s.CreateExampleSales("example-audit-rollback", quote)
			}
			if err == nil || !strings.Contains(err.Error(), "promotion audit unavailable") {
				t.Fatalf("expected injected audit failure, got %v", err)
			}
			assertPromotionUnchanged(t, s, before)
			testExec(t, s, `DROP TRIGGER reject_promotion_audit`)
			if action == "examples" {
				if err := s.CreateExampleSales("example-audit-rollback", quote); err != nil {
					t.Fatal("exact retry after rollback", err)
				}
			}
		})
	}
}

func TestPromotionsExampleSalesPreviewReplayAndNoOverwrite(t *testing.T) {
	s, clock, _ := promotionTestStore(t)
	before := fingerprintTest(t, s.db)
	examples, quote, err := s.ExampleSales()
	if err != nil || len(examples) != 3 || len(quote) != 64 {
		t.Fatalf("example preview=%+v %q %v", examples, quote, err)
	}
	assertPromotionUnchanged(t, s, before)
	starts, ends := resetPromotionWeek(clock.now())
	for i, want := range []struct{ id, price int64 }{{1, 249}, {3, 399}, {4, 329}} {
		if p := examples[i]; p.ProductID != want.id || p.SalePrice != want.price || p.Starts != starts || p.Ends != ends {
			t.Errorf("wrong demo example: %+v", p)
		}
	}
	if err := s.CreateExampleSales("reviewed-demo-examples", quote); err != nil {
		t.Fatal(err)
	}
	if testCount(t, s, "promotions") != 3 || testCount(t, s, "product_features") != 3 || testCount(t, s, "promotion_events") != 6 || testCount(t, s, "promotion_commands") != 1 {
		t.Fatal("example application incomplete")
	}
	before = fingerprintTest(t, s.db)
	if err := s.CreateExampleSales("reviewed-demo-examples", quote); err != nil {
		t.Fatal("exact replay", err)
	}
	if err := s.CreateExampleSales("reviewed-demo-examples", strings.Repeat("0", 64)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay=%v", err)
	}
	if _, _, err := s.ExampleSales(); !errors.Is(err, ErrExampleSales) {
		t.Fatalf("existing choices accepted for overwrite: %v", err)
	}
	if err := s.CreateExampleSales("second-demo-examples", quote); !errors.Is(err, ErrExampleSales) {
		t.Fatalf("different command overwrote examples: %v", err)
	}
	assertPromotionUnchanged(t, s, before)
	for _, example := range examples {
		if p := testProduct(t, s, example.ProductID); !p.Featured || p.EffectivePrice() != example.SalePrice || p.Price != example.RegularPrice {
			t.Fatalf("examples damaged regular price or missed feature: %+v", p)
		}
	}
	for _, conflict := range []string{"product edit", "feature choice", "sale", "next UTC week"} {
		t.Run(conflict, func(t *testing.T) {
			s, clock, _ := promotionTestStore(t)
			_, quote, err := s.ExampleSales()
			if err != nil {
				t.Fatal(err)
			}
			switch conflict {
			case "product edit":
				p := testCatalogProduct(t, s, 1)
				p.Description += " Custom"
				if _, err := s.SaveProduct(p); err != nil {
					t.Fatal(err)
				}
			case "feature choice":
				if err := s.SetFeatured(1, 0, false); err != nil {
					t.Fatal(err)
				}
			case "sale":
				saveTestPromotion(t, s, 1, 239, clock.now().Unix()-1, clock.now().Unix()+600)
			case "next UTC week":
				_, end := resetPromotionWeek(clock.now())
				clock.at(end)
			}
			before := fingerprintTest(t, s.db)
			want := ErrExampleSales
			if conflict == "next UTC week" {
				want = ErrConflict
			}
			if err := s.CreateExampleSales(fmt.Sprintf("reviewed-examples-%s", conflict), quote); !errors.Is(err, want) {
				t.Fatalf("changed preview=%v, want %v", err, want)
			}
			assertPromotionUnchanged(t, s, before)
		})
	}
}

func TestPromotionsUnavailableProductsRejectSalesAndFeatures(t *testing.T) {
	for _, state := range []string{"archived", "weighted"} {
		t.Run(state, func(t *testing.T) {
			s, clock, _ := promotionTestStore(t)
			product := testCreateProduct(t, s, func(p *Product) {
				if state == "weighted" {
					p.SaleUnit, p.PriceBasis, p.QuantityStep = "g", 1000, 100
				}
			})
			if state == "archived" {
				if err := s.ArchiveProduct(product.ID, product.CatalogVersion); err != nil {
					t.Fatal(err)
				}
				product = testCatalogProduct(t, s, product.ID)
			}
			before := fingerprintTest(t, s.db)
			_, err := s.SavePromotion(Promotion{ProductID: product.ID, ProductVersion: product.PriceVersion, SalePrice: 399, Starts: clock.now().Unix(), Ends: clock.now().Unix() + 60})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unavailable product sale=%v", err)
			}
			if err := s.SetFeatured(product.ID, product.FeatureVersion, true); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unavailable product feature=%v", err)
			}
			assertPromotionUnchanged(t, s, before)
		})
	}
}

func TestPromotionsMutationVersionsLeaveInventoryAndCatalogUntouched(t *testing.T) {
	s, clock, _ := promotionTestStore(t)
	before := testProduct(t, s, 1)
	sale := saveTestPromotion(t, s, 1, 249, clock.now().Unix()-1, clock.now().Unix()+60)
	for step, action := range []string{"create", "edit", "cancel"} {
		if action == "edit" {
			if _, err := s.SavePromotion(sale); err != nil {
				t.Fatal(err)
			}
			sale = testPromotion(t, s, sale.ID)
		}
		if action == "cancel" {
			if err := s.CancelPromotion(sale.ID, sale.Version); err != nil {
				t.Fatal(err)
			}
		}
		p := testProduct(t, s, 1)
		if p.PriceVersion != before.PriceVersion+int64(step)+1 || p.CatalogVersion != before.CatalogVersion || p.Version != before.Version || p.Stock != before.Stock || p.Price != before.Price {
			t.Fatalf("%s touched an unrelated product field or missed rate invalidation: %+v", action, p)
		}
	}
	if testCount(t, s, "catalog_events") != 0 || testCount(t, s, "adjustments") != 0 {
		t.Fatal("sales created unrelated catalog/stock audit events")
	}
	events, err := s.PromotionEvents()
	if err != nil || len(events) != 3 {
		t.Fatalf("promotion audit events=%+v, %v", events, err)
	}
	for i, action := range []string{"cancel", "edit", "create"} {
		event := events[i]
		if event.Action != action || event.PromotionID != sale.ID || event.ProductID != 1 || event.Name != before.Name || !strings.Contains(event.Details, "$2.49") || !strings.Contains(event.Details, "starts "+sale.StartLabel()) || !strings.Contains(event.Details, "ends "+sale.EndLabel()) || !strings.Contains(event.Details, "SHOPDEMO-000001") || event.Created != "2026-10-02 12:00:00 UTC" {
			t.Errorf("incomplete %s audit: %+v", action, event)
		}
	}
}
