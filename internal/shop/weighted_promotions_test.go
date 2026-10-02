package shop

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestWeightedPromotionQuoteBoundariesKeepReservationAndRateSnapshots(t *testing.T) {
	for _, boundary := range []string{"start", "expiry", "cancel"} {
		t.Run(boundary, func(t *testing.T) {
			s, clock, _ := promotionTestStore(t)
			now := clock.now().Unix()
			p := weightedProduct(t, s, 3000, 499, 1)
			starts := now - 60
			if boundary == "start" {
				starts = now + 60
			}
			sale := saveTestPromotion(t, s, p.ID, 349, starts, now+120)
			if sale.SaleUnit != "g" || sale.PriceBasis != 1000 || sale.RateUnit() != "per kg" {
				t.Fatalf("sale basis lost: %+v", sale)
			}
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, p.ID, 500)
			old := testBasket(t, s, owner.ID)
			switch boundary {
			case "start":
				clock.at(sale.Starts)
			case "expiry":
				clock.at(sale.Ends)
			case "cancel":
				if err := s.CancelPromotion(sale.ID, sale.Version); err != nil {
					t.Fatal(err)
				}
			}
			fresh := testBasket(t, s, owner.ID)
			if fresh.Quote == old.Quote || fresh.HoldUntil != old.HoldUntil || fresh.Revision != old.Revision || fresh.Lines[0].Reserved != 500 {
				t.Fatal("sale boundary changed reservation or retained quote", fresh)
			}
			before := fingerprintTest(t, s.db)
			if _, err := s.Checkout(owner.ID, owner.CheckoutKey, old.Revision, old.Quote); !errors.Is(err, ErrQuote) {
				t.Fatal("stale weighed quote accepted", err)
			}
			assertPromotionUnchanged(t, s, before)
			id, err := s.Checkout(owner.ID, owner.CheckoutKey, fresh.Revision, fresh.Quote)
			if err != nil {
				t.Fatal(err)
			}
			order := testOrder(t, s, id, owner.ID)
			rate := fresh.Lines[0].Product.EffectivePrice()
			placed := placedSnapshot(t, s, id)
			if order.Items[0].Price != rate || order.Items[0].PriceBasis != 1000 || order.Items[0].Quantity != 500 {
				t.Fatal("wrong placed rate/basis", order)
			}
			// Expiry after placement must never reprice the original bag when measured.
			clock.at(now + 180)
			command := confirmWeight(t, s, id, owner.ID, p.ID, 527, "")
			order = testOrder(t, s, id, owner.ID)
			want, _ := lineAmount(527, rate, 1000, "g")
			if order.WorkingItems[0].Price != rate || order.WorkingTotal != want || order.WorkingItems[0].Allocated != 527 || !reflect.DeepEqual(placed, placedSnapshot(t, s, id)) {
				t.Fatal("measurement changed snapped rate/receipt", order)
			}
			before = fingerprintTest(t, s.db)
			if err = s.ConfirmWeight(id, owner.ID, false, command); err != nil {
				t.Fatal(err)
			}
			assertPromotionUnchanged(t, s, before)
			assertReservedStock(t, s, p.ID, 2473, 0)
		})
	}
}

func TestWeightedPromotionSubstitutionChecksQuoteAndKeepsTargetRate(t *testing.T) {
	s, clock, _ := promotionTestStore(t)
	now := clock.now().Unix()
	original := weightedProduct(t, s, 2000, 699, 1)
	replacement := weightedProduct(t, s, 2000, 499, 1)
	sale := saveTestPromotion(t, s, replacement.ID, 349, now-60, now+60)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, original.ID, 500)
	id := testCheckout(t, s, owner.ID)
	receipt := placedSnapshot(t, s, id)
	c := overrideCommand(t, s, id, owner.ID, "substitute", original.ID, 500)
	c.ReplacementID = replacement.ID
	clock.at(sale.Ends)
	before := fingerprintTest(t, s.db)
	if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrOrderQuote) {
		t.Fatal("expired replacement quote accepted", err)
	}
	assertPromotionUnchanged(t, s, before)
	saveTestPromotion(t, s, replacement.ID, 299, sale.Ends, sale.Ends+60)
	c = overrideCommand(t, s, id, owner.ID, "substitute", original.ID, 500)
	c.ReplacementID = replacement.ID
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	clock.at(sale.Ends + 61)
	confirmWeight(t, s, id, owner.ID, replacement.ID, 527, "")
	l := promotionWorkingLine(t, s, id, owner.ID, replacement.ID)
	if l.Price != 299 || l.Subtotal != 158 || l.Allocated != 527 || !reflect.DeepEqual(receipt, placedSnapshot(t, s, id)) {
		t.Fatal("replacement rate/snapshot changed", l)
	}
	assertReservedStock(t, s, original.ID, 2000, 0)
	assertReservedStock(t, s, replacement.ID, 1473, 0)
}

func TestPromotionHistoricalSellingUnitNeverReinterpreted(t *testing.T) {
	s, clock, path := promotionTestStore(t)
	now := clock.now().Unix()
	product := testCreateProduct(t, s, nil)
	sale := saveTestPromotion(t, s, product.ID, 399, now-120, now-60)
	p := testCatalogProduct(t, s, product.ID)
	p.SaleUnit, p.PriceBasis, p.QuantityStep = "g", 1000, 50
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	saved := testPromotion(t, s, sale.ID)
	if saved.SaleUnit != "each" || saved.PriceBasis != 1 || saved.CurrentSaleUnit != "g" || !saved.Unavailable {
		t.Fatal("historical basis lost", saved)
	}
	saved.Starts, saved.Ends = now, now+60
	before := fingerprintTest(t, s.db)
	if _, err := s.SavePromotion(saved); !errors.Is(err, ErrPromotionUnit) {
		t.Fatal("history reinterpreted on edit", err)
	}
	assertPromotionUnchanged(t, s, before)
	// Defense in depth: a stored offer with a mismatched basis never prices grams.
	testExec(t, s, `UPDATE promotions SET starts=?,ends=? WHERE id=?`, now, now+60, sale.ID)
	if current := testProduct(t, s, product.ID); current.OnSale() || current.EffectivePrice() != 499 {
		t.Fatal("mismatched sale applied", current)
	}
	if err := s.CancelPromotion(sale.ID, sale.Version); err != nil {
		t.Fatal(err)
	}
	events, err := s.PromotionEvents()
	if err != nil || !strings.Contains(events[0].Details, "$3.99 each") {
		t.Fatal("cancel audit relabelled history", events, err)
	}
	newSale := saveTestPromotion(t, s, product.ID, 349, now, now+60)
	if newSale.RateUnit() != "per kg" || !testProduct(t, s, product.ID).OnSale() {
		t.Fatal("new weighed offer unavailable")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = reservationStore(t, path, clock)
	if testPromotion(t, s, sale.ID).RateUnit() != "each" || testPromotion(t, s, newSale.ID).RateUnit() != "per kg" {
		t.Fatal("restart changed price bases")
	}
}

func TestWeightedPromotionHTTPFormsFeaturedLinksAndGramBasket(t *testing.T) {
	for _, hx := range []bool{false, true} {
		t.Run(fmt.Sprint(hx), func(t *testing.T) {
			s, clock, _ := promotionTestStore(t)
			a := reservationHTTPApp(t, s, true)
			manager := testManager(t, s)
			p := weightedProduct(t, s, 3000, 499, 50)
			now := clock.now().Unix()
			page := testRequest(t, a, http.MethodGet, "/manager/promotions/new", manager, nil, managerDraftHeaders(hx))
			if page.Code != 200 || !strings.Contains(adminVisibleText(page.Body.String()), p.Name+" · $4.99 per kg regular") {
				t.Fatal("weighed product absent or wrongly labelled in sale form")
			}
			form := promotionHTTPForm(manager, p, "3.49", now-60, now+3600)
			w := testRequest(t, a, http.MethodPost, "/manager/promotions", manager, form, managerDraftHeaders(hx))
			if (!hx && w.Code != 303) || (hx && w.Header().Get("HX-Redirect") == "") {
				t.Fatalf("weighed sale creation failed: %d %s", w.Code, w.Body.String())
			}
			if err := s.SetFeatured(p.ID, 0, true); err != nil {
				t.Fatal(err)
			}
			featured := weightedProduct(t, s, 1000, 799, 100)
			if err := s.SetFeatured(featured.ID, 0, true); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/", "/?sales=1", "/?featured=1"} {
				w = testRequest(t, a, http.MethodGet, path, manager, nil, managerDraftHeaders(hx))
				body := w.Body.String()
				hero := weeklyHero(t, body)
				if w.Code != 200 || !strings.Contains(hero, "$3.49") || !strings.Contains(hero, "per kg") || !strings.Contains(hero, "3000 g available") || !strings.Contains(hero, fmt.Sprintf(`id="weekly-weight-%d"`, p.ID)) {
					t.Fatalf("weighted offer omitted/mislabelled: %s", hero)
				}
				if adminForm(hero, "/cart") != "" {
					t.Fatal("hero silently adds one gram")
				}
				if !strings.Contains(body, fmt.Sprintf(`id="featured-weight-%d"`, featured.ID)) || strings.Contains(body, fmt.Sprintf(`id="featured-add-%d"`, featured.ID)) {
					t.Fatal("featured item lost explicit gram choice")
				}
			}
			w = testRequest(t, a, http.MethodGet, fmt.Sprintf("/products/%d?sales=1", p.ID), manager, nil, nil)
			formBody := adminForm(w.Body.String(), "/cart")
			values := url.Values{}
			for _, input := range adminInputRE.FindAllStringSubmatch(formBody, -1) {
				values.Set(adminAttr(input[1], "name"), adminAttr(input[1], "value"))
			}
			values.Set("quantity", "500")
			w = testRequest(t, a, http.MethodPost, "/cart", manager, values, managerDraftHeaders(hx))
			if w.Code != 200 && w.Code != 303 {
				t.Fatal("gram basket post failed", w.Code)
			}
			if basket := testBasket(t, s, manager.ID); basket.Total != 175 || basket.Lines[0].Quantity != 500 {
				t.Fatal("wrong weighted sale estimate", basket)
			}
			editor := testRequest(t, a, http.MethodGet, "/manager/promotions/1", manager, nil, nil)
			if !strings.Contains(adminVisibleText(editor.Body.String()), "Regular $4.99 per kg · Sale $3.49 per kg") {
				t.Fatal("sale editor lost unit labels")
			}
		})
	}
}
