package shop

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestStockInventoryFiltersSortPagesAndExpiry(t *testing.T) {
	s, clock, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 3)
	held := testBasket(t, s, owner.ID)
	testExec(t, s, "UPDATE products SET stock=0 WHERE id=2")
	testExec(t, s, "UPDATE products SET stock=7 WHERE id=3")
	testExec(t, s, "UPDATE products SET archived=1 WHERE id=4")
	p := testProduct(t, s, 1)
	for _, tc := range []struct {
		name    string
		filters StockFilters
		assert  func(StockPage)
	}{
		{"pagination", StockFilters{Page: 1}, func(got StockPage) {
			if got.Total != 71 || len(got.Products) != 12 || got.Pages != 6 {
				t.Errorf("pagination=%+v", got)
			}
			for i := 1; i < len(got.Products); i++ {
				if strings.ToLower(got.Products[i-1].Name) > strings.ToLower(got.Products[i].Name) {
					t.Error("default sort is not alphabetical")
				}
			}
		}},
		{"large page clamped", StockFilters{Page: 1000000}, func(got StockPage) {
			if got.Page != 6 || got.First != 61 || got.Last != 71 || len(got.Products) != 11 {
				t.Errorf("clamp=%+v", got)
			}
		}},
		{"sku search", StockFilters{Search: strings.ToLower(p.SKU)}, func(got StockPage) {
			if got.Total != 1 || got.Products[0].ID != 1 {
				t.Errorf("sku query=%+v", got)
			}
		}},
		{"reserved", StockFilters{State: "reserved"}, func(got StockPage) {
			if got.Total != 1 || got.Products[0].ID != 1 || got.Products[0].Reserved != 3 {
				t.Errorf("reserved query=%+v", got)
			}
		}},
		{"archived", StockFilters{Lifecycle: "archived"}, func(got StockPage) {
			if got.Total != 1 || got.Products[0].ID != 4 || !got.Products[0].Archived {
				t.Errorf("archive query=%+v", got)
			}
		}},
		{"all", StockFilters{Lifecycle: "all"}, func(got StockPage) {
			if got.Total != 72 {
				t.Errorf("all count=%d", got.Total)
			}
		}},
		{"zero", StockFilters{State: "out"}, func(got StockPage) {
			for _, p := range got.Products {
				if p.Stock != 0 {
					t.Error("nonzero in out-of-stock")
				}
			}
			if got.Total < 1 {
				t.Error("no out-of-stock result")
			}
		}},
		{"low", StockFilters{State: "low"}, func(got StockPage) {
			for _, p := range got.Products {
				if p.Stock >= 8 || p.SaleUnit != "each" {
					t.Error("nonlow in low stock")
				}
			}
		}},
		{"department", StockFilters{Department: p.CategoryID}, func(got StockPage) {
			for _, got := range got.Products {
				if got.CategoryID != p.CategoryID {
					t.Error("foreign department")
				}
			}
		}},
		{"ascending quantity", StockFilters{Sort: "available"}, func(got StockPage) {
			for i := 1; i < len(got.Products); i++ {
				if got.Products[i-1].Stock > got.Products[i].Stock {
					t.Error("quantity sort")
				}
			}
		}},
		{"empty", StockFilters{Search: "%not-a-product_'"}, func(got StockPage) {
			if got.Total != 0 || got.First != 0 || got.Last != 0 || got.Page != 1 || got.Pages != 1 {
				t.Errorf("empty=%+v", got)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.StockInventory(tc.filters)
			if err != nil {
				t.Fatal(err)
			}
			tc.assert(got)
		})
	}
	clock.at(held.HoldUntil)
	got, err := s.StockInventory(StockFilters{State: "reserved"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 0 {
		t.Error("inventory query retained expired reservations")
	}
	if testProduct(t, s, 1).Stock != p.Stock+3 {
		t.Error("expiry did not restore available stock")
	}
}

func TestStockFiltersBoundAndPreserveIndependentContexts(t *testing.T) {
	raw := url.Values{"q": {strings.Repeat("界", 200)}, "page": {"9223372036854775807"}, "sort": {"stock; DROP TABLE products"}, "department": {"1 OR 1=1"}, "product": {"2"}, "activity_product": {"3"}, "stock": {"reserved"}, "view": {"activity"}, "action": {"stock"}, "from": {"2026-02-30"}, "to": {"2026-03-01"}}
	f := parseStockFilters(raw)
	if len([]rune(f.Search)) != 100 || f.Page != 1 || f.Sort != "" || f.Department != 0 || f.DateError == "" || f.From != "" {
		t.Errorf("unbounded filters=%+v", f)
	}
	f = parseStockFilters(url.Values{"from": {"2026-10-02"}, "to": {"2026-10-01"}})
	if f.DateError == "" {
		t.Error("reversed dates accepted")
	}
	f = parseStockFilters(url.Values{"q": {"apples"}, "department": {"1"}, "stock": {"low"}, "lifecycle": {"all"}, "unit": {"each"}, "sort": {"reserved"}, "page": {"3"}, "product": {"1"}, "view": {"activity"}, "activity_page": {"2"}, "action": {"stock"}, "activity_product": {"1"}, "from": {"2026-10-01"}, "to": {"2026-10-01"}})
	roundtrip := parseStockFilters(f.Values())
	if !reflect.DeepEqual(f, roundtrip) {
		t.Errorf("roundtrip=%+v want %+v", roundtrip, f)
	}
	selected, _ := url.Parse(f.SelectURL(2))
	if selected.Query().Get("product") != "2" || selected.Query().Get("q") != "apples" || selected.Query().Get("action") != "stock" || selected.Query().Get("view") != "" {
		t.Errorf("selection lost context: %s", selected)
	}
	page, _ := url.Parse(f.InventoryPageURL(4))
	if page.Query().Get("product") != "1" {
		t.Error("pagination lost selection")
	}
	clear, _ := url.Parse(f.ClearActivityURL())
	if clear.Query().Get("q") != "apples" || clear.Query().Get("product") != "1" || clear.Query().Get("activity_product") != "" {
		t.Errorf("clear crossed contexts: %s", clear)
	}
}

func TestStockActivityUsesSavedEventsWithScopedCountsAndLinks(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	foreign := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	mine := reservationCheckout(t, s, owner.ID, "")
	testCart(t, s, foreign.ID, 2, 1)
	other := reservationCheckout(t, s, foreign.ID, "")
	testExec(t, s, "UPDATE orders SET reference='MY-PLACED-ORDER',created='2026-10-01 12:00 UTC' WHERE id=?", mine)
	testExec(t, s, "UPDATE orders SET reference='FOREIGN-PLACED-ORDER',created='2026-10-01 13:00 UTC' WHERE id=?", other)
	ownBasket := testBasket(t, s, owner.ID)
	foreignBasket := testBasket(t, s, foreign.ID)
	testExec(t, s, "INSERT INTO basket_events(basket_id,action,reason,details,created) VALUES(?,'set quantity','Own basket reason','Product 1: quantity 0 → 1','2026-10-01 12:00 UTC')", ownBasket.ID)
	for i := 0; i < 20; i++ {
		testExec(t, s, "INSERT INTO basket_events(basket_id,action,reason,details,created) VALUES(?,'set quantity','FOREIGN-BASKET-REASON','Product 1: quantity 0 → 1','2026-10-01 14:00 UTC')", foreignBasket.ID)
	}
	p := testProduct(t, s, 1)
	if err := s.Adjust(1, 2, p.Version, "Shared stock reason"); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, "UPDATE adjustments SET created='2026-10-01 10:00 UTC'")
	p = testProduct(t, s, 1)
	p.Name = "Shared catalog product"
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, "UPDATE catalog_events SET created='2026-09-30 10:00 UTC'")
	got, err := s.StockActivity(StockFilters{}, owner.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 4 || len(got.Events) != 4 || got.Pages != 1 {
		t.Fatalf("scoped counts=%+v", got)
	}
	joined := fmt.Sprint(got.Events)
	for _, secret := range []string{"FOREIGN-PLACED-ORDER", "FOREIGN-BASKET-REASON", foreignBasket.ID, foreign.ID, owner.ID} {
		if strings.Contains(joined, secret) {
			t.Errorf("activity leaked %q", secret)
		}
	}
	for _, e := range got.Events {
		switch e.Source {
		case "order":
			if e.URL != fmt.Sprintf("/manager/orders/%d", mine) || e.Action != "Order placed" {
				t.Errorf("untruthful order event=%+v", e)
			}
		case "basket":
			if e.URL != "/manager/baskets/"+ownBasket.ID {
				t.Errorf("unscoped basket link=%+v", e)
			}
		case "stock":
			if e.Delta != 2 || e.Reason != "Shared stock reason" {
				t.Errorf("stock event=%+v", e)
			}
		}
	}
	all, err := s.StockActivity(StockFilters{}, owner.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 25 || all.Pages != 3 {
		t.Errorf("normal-mode count=%+v", all)
	}
	filtered, err := s.StockActivity(StockFilters{ActivityProduct: 1}, owner.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 3 {
		t.Errorf("product history should exclude unstructured basket event: %+v", filtered)
	}
	for _, e := range filtered.Events {
		if e.Source == "basket" {
			t.Error("product history guessed from basket text")
		}
	}
	filtered, err = s.StockActivity(StockFilters{From: "2026-10-01", To: "2026-10-01", Action: "stock", ActivityProduct: 1}, owner.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || filtered.Events[0].Reason != "Shared stock reason" {
		t.Errorf("action/date/product intersection=%+v", filtered)
	}
	filtered, err = s.StockActivity(StockFilters{ActivityProduct: 2}, owner.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 0 {
		t.Error("foreign order visible by product filter")
	}
	invalid, err := s.StockActivity(StockFilters{DateError: "invalid date"}, owner.ID, true)
	if err != nil || invalid.Total != 0 || len(invalid.Events) != 0 {
		t.Error("invalid dates broadened activity query")
	}
}

func TestStockActivityPaginationStableAndInvalidWritesNeverAudit(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 30; i++ {
		p := testProduct(t, s, 1)
		if err := s.Adjust(1, 1, p.Version, fmt.Sprintf("Recorded adjustment %02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	before := testCount(t, s, "adjustments")
	p := testProduct(t, s, 1)
	if err := s.Adjust(1, 1, p.Version-1, "Rejected stale correction"); err == nil {
		t.Fatal("stale write succeeded")
	}
	if testCount(t, s, "adjustments") != before {
		t.Error("failed write produced event")
	}
	page, err := s.StockActivity(StockFilters{Action: "stock", ActivityPage: 2}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.StockActivity(StockFilters{Action: "stock", ActivityPage: 2}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 30 || page.Page != 2 || page.Pages != 3 || page.First != 13 || page.Last != 24 || !reflect.DeepEqual(page, again) {
		t.Errorf("unstable page=%+v", page)
	}
	last, err := s.StockActivity(StockFilters{Action: "stock", ActivityPage: 1000000}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if last.Page != 3 || len(last.Events) != 6 {
		t.Errorf("unbounded last page=%+v", last)
	}
}
