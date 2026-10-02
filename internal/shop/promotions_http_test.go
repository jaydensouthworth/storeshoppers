package shop

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func promotionHTTPForm(session Session, p Product, price string, start, end int64) url.Values {
	return url.Values{"csrf": {session.CSRF}, "product_ref": {p.PromotionRef()}, "sale_price_usd": {price}, "starts": {time.Unix(start, 0).UTC().Format("2006-01-02T15:04")}, "ends": {time.Unix(end, 0).UTC().Format("2006-01-02T15:04")}, "version": {"0"}}
}
func TestPromotionDollarInputIsExact(t *testing.T) {
	for raw, want := range map[string]int64{"2": 200, "2.4": 240, "2.49": 249, "0.01": 1, "10000.00": 1000000, "0002.49": 249} {
		got, err := parseSaleUSD(raw)
		if err != nil || got != want {
			t.Errorf("%q = %d,%v want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "0", "0.00", ".99", "1.", "1.000", "1e2", "+2", "-2", "2,49", "2.49 ", " 2.49", "10000.01", "999999999999999", "NaN", "١.٢"} {
		if _, err := parseSaleUSD(raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid %q accepted: %v", raw, err)
		}
	}
}
func TestPromotionsHTTPManagerCSRFAndNoJSCRUD(t *testing.T) {
	s, clock, _ := newReservationStore(t)
	a := reservationHTTPApp(t, s, false)
	manager := testManager(t, s)
	visitor := testSession(t, s, "")
	start := clock.now().Truncate(time.Minute).Unix()
	end := start + 3600
	form := promotionHTTPForm(manager, testProduct(t, s, 1), "2.49", start, end)
	for _, path := range []string{"/manager/promotions", "/manager/promotions/new", "/manager/promotions/1", "/manager/featured"} {
		w := testRequest(t, a, http.MethodGet, path, visitor, nil, nil)
		if w.Code != http.StatusSeeOther {
			t.Errorf("unguarded GET %s: %d", path, w.Code)
		}
	}
	bad := promotionHTTPForm(visitor, testProduct(t, s, 1), "2.49", start, end)
	for _, path := range []string{"/manager/promotions", "/manager/promotions/1", "/manager/promotions/1/cancel", "/manager/featured/1"} {
		w := testRequest(t, a, http.MethodPost, path, visitor, bad, nil)
		if w.Code != http.StatusSeeOther {
			t.Errorf("unguarded POST %s: %d", path, w.Code)
		}
	}
	bad.Set("csrf", "wrong")
	if w := testRequest(t, a, http.MethodPost, "/manager/promotions", manager, bad, nil); w.Code != http.StatusForbidden {
		t.Fatalf("CSRF=%d", w.Code)
	}
	form.Set("q", "apples")
	form.Set("state", "active")
	w := testRequest(t, a, http.MethodPost, "/manager/promotions", manager, form, nil)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/manager/promotions/1?") {
		t.Fatalf("create redirect=%d %s", w.Code, w.Body.String())
	}
	sales, err := s.Promotions()
	if err != nil || len(sales) != 1 || sales[0].SalePrice != 249 {
		t.Fatalf("sale=%+v %v", sales, err)
	}
	page := testRequest(t, a, http.MethodGet, w.Header().Get("Location"), manager, nil, nil)
	body := page.Body.String()
	if page.Code != 200 || !strings.Contains(body, "sale_price_usd") || !strings.Contains(body, "UTC") || !strings.Contains(body, "2.49") {
		t.Fatalf("editor unavailable: %d %s", page.Code, body)
	}
	form = promotionHTTPForm(manager, testProduct(t, s, 1), "2.19", start, end)
	form.Set("version", "1")
	if w = testRequest(t, a, http.MethodPost, "/manager/promotions/1", manager, form, map[string]string{"HX-Request": "true"}); w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("HTMX save failed=%d %s", w.Code, w.Body.String())
	}
	if p := testProduct(t, s, 1); p.Price != 349 || p.EffectivePrice() != 219 {
		t.Errorf("regular/effective=%d/%d", p.Price, p.EffectivePrice())
	}
	stale := testRequest(t, a, http.MethodPost, "/manager/promotions/1", manager, form, nil)
	if !strings.Contains(stale.Body.String(), ErrConflict.Error()) {
		t.Errorf("stale form did not explain review: %s", stale.Body.String())
	}
	cancel := url.Values{"csrf": {manager.CSRF}, "version": {"2"}}
	if w = testRequest(t, a, http.MethodPost, "/manager/promotions/1/cancel", manager, cancel, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("cancel=%d", w.Code)
	}
	if p := testProduct(t, s, 1); p.OnSale() {
		t.Error("cancelled sale remains active")
	}
	if w = testRequest(t, a, http.MethodPost, "/manager/featured/1", manager, url.Values{"csrf": {manager.CSRF}, "feature_version": {"0"}, "featured": {"1"}}, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("feature=%d", w.Code)
	}
	if p := testProduct(t, s, 1); !p.Featured {
		t.Error("featured control not persisted")
	}
}
func TestPromotionsHTTPInvalidDraftEmptySalesAndPreservedFilters(t *testing.T) {
	s, c, _ := newReservationStore(t)
	a := reservationHTTPApp(t, s, false)
	manager := testManager(t, s)
	page := testRequest(t, a, http.MethodGet, "/?sales=1", manager, nil, nil)
	if page.Code != 200 || !strings.Contains(strings.ToLower(adminVisibleText(page.Body.String())), "no active") {
		t.Fatalf("missing honest fallback: %s", page.Body.String())
	}
	start := c.now().Truncate(time.Minute).Unix()
	form := promotionHTTPForm(manager, testProduct(t, s, 1), "2.499", start, start+3600)
	form.Set("q", "Honeycrisp")
	w := testRequest(t, a, http.MethodPost, "/manager/promotions", manager, form, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "2.499") || !strings.Contains(w.Body.String(), ErrInvalid.Error()) {
		t.Fatalf("invalid draft lost: %d %s", w.Code, w.Body.String())
	}
	form.Set("sale_price_usd", "2.49")
	if w = testRequest(t, a, http.MethodPost, "/manager/promotions", manager, form, nil); w.Code != http.StatusSeeOther {
		t.Fatal("valid sale failed")
	}
	page = testRequest(t, a, http.MethodGet, "/?sales=1&q=Honeycrisp&category=Produce", manager, nil, nil)
	for _, text := range []string{"$2.49", "$3.49", "Shop all sales"} {
		if !strings.Contains(page.Body.String(), text) {
			t.Errorf("store missing %q", text)
		}
	}
	b := testBasket(t, s, manager.ID)
	cart := url.Values{"csrf": {manager.CSRF}, "product_id": {"1"}, "quantity": {"1"}, "mode": {"add"}, "revision": {fmt.Sprint(b.Revision)}, "return": {"store"}, "q": {"Honeycrisp"}, "category": {"Produce"}, "sales": {"1"}}
	w = testRequest(t, a, http.MethodPost, "/cart", manager, cart, nil)
	u, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != 303 || u.Query().Get("sales") != "1" || u.Query().Get("q") != "Honeycrisp" || u.Query().Get("category") != "Produce" {
		t.Fatalf("lost sales filters: %d %s", w.Code, w.Header().Get("Location"))
	}
	if b := testBasket(t, s, manager.ID); b.Total != 249 {
		t.Errorf("basket price %d", b.Total)
	}
}
func TestExampleSalesHTTPRequiresDemoPreviewAndConfirmation(t *testing.T) {
	for _, demo := range []bool{false, true} {
		t.Run(fmt.Sprint(demo), func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, demo)
			manager := testManager(t, s)
			page := testRequest(t, a, http.MethodGet, "/manager/promotions/examples", manager, nil, nil)
			if !demo {
				if page.Code != 404 {
					t.Fatalf("non-demo GET=%d", page.Code)
				}
				if w := testRequest(t, a, http.MethodPost, "/manager/promotions/examples", manager, url.Values{"csrf": {manager.CSRF}}, nil); w.Code != 404 {
					t.Fatalf("non-demo POST=%d", w.Code)
				}
				return
			}
			if page.Code != 200 || !strings.Contains(page.Body.String(), "confirm") || !strings.Contains(page.Body.String(), "UTC") {
				t.Fatalf("preview=%d %s", page.Code, page.Body.String())
			}
			if n := testCount(t, s, "promotions"); n != 0 {
				t.Fatal("GET mutated offers")
			}
			_, quote, err := s.ExampleSales()
			if err != nil {
				t.Fatal(err)
			}
			form := url.Values{"csrf": {manager.CSRF}, "quote": {quote}, "command_key": {token()}}
			if w := testRequest(t, a, http.MethodPost, "/manager/promotions/examples", manager, form, nil); w.Code != 200 || testCount(t, s, "promotions") != 0 {
				t.Fatal("missing confirmation mutated offers")
			}
			form.Set("confirm", "1")
			w := testRequest(t, a, http.MethodPost, "/manager/promotions/examples", manager, form, nil)
			if w.Code != 303 || testCount(t, s, "promotions") != 3 {
				t.Fatalf("confirmed create=%d %s", w.Code, w.Body.String())
			}
			if w = testRequest(t, a, http.MethodPost, "/manager/promotions/examples", manager, form, nil); w.Code != 303 || testCount(t, s, "promotions") != 3 {
				t.Fatal("exact retry duplicated examples")
			}
			events, err := s.StockActivity(StockFilters{View: "activity", ActivityPage: 1}, manager.ID, !demo)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range events.Events {
				if e.Source == "promotion" && strings.HasPrefix(e.URL, "/manager/") {
					found = true
				}
			}
			if !found {
				t.Error("promotion audit missing from activity")
			}
		})
	}
}

func TestFeaturedStoreFilterIncludesEveryEligibleFlag(t *testing.T) {
	s, _, _ := newReservationStore(t)
	a := reservationHTTPApp(t, s, false)
	owner := testSession(t, s, "")
	for _, id := range []int64{1, 2, 3, 4, 6} {
		if err := s.SetFeatured(id, 0, true); err != nil {
			t.Fatal(err)
		}
	}
	page := testRequest(t, a, http.MethodGet, "/?featured=1", owner, nil, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `id="add-6"`) || strings.Contains(page.Body.String(), `id="add-5"`) {
		t.Fatalf("featured filter drops extra choice or includes unfeatured product: %d", page.Code)
	}
	p := testProduct(t, s, 1)
	if err := s.ArchiveProduct(1, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	page = testRequest(t, a, http.MethodGet, "/?featured=1", owner, nil, nil)
	if strings.Contains(page.Body.String(), `id="add-1"`) || strings.Contains(page.Body.String(), `id="featured-add-1"`) {
		t.Fatal("archive remained on featured storefront")
	}
	b := testBasket(t, s, owner.ID)
	values := url.Values{"csrf": {owner.CSRF}, "revision": {fmt.Sprint(b.Revision)}, "product_id": {"6"}, "quantity": {"1"}, "mode": {"add"}, "return": {"store"}, "featured": {"1"}, "q": {"pasta"}}
	w := testRequest(t, a, http.MethodPost, "/cart", owner, values, nil)
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Query().Get("featured") != "1" || u.Query().Get("q") != "pasta" {
		t.Fatalf("featured return lost filter: %s", w.Header().Get("Location"))
	}
}
func TestPromotionHTTPRetainsDraftWithFreshAuthoritativeVersions(t *testing.T) {
	s, c, _ := newReservationStore(t)
	a := reservationHTTPApp(t, s, false)
	manager := testManager(t, s)
	now := c.now().Truncate(time.Minute).Unix()
	sale := saveTestPromotion(t, s, 1, 249, now, now+3600)
	old := promotionHTTPForm(manager, testProduct(t, s, 1), "1.99", now+60, now+3600)
	old.Set("version", fmt.Sprint(sale.Version))
	sale.SalePrice = 229
	if _, err := s.SavePromotion(sale); err != nil {
		t.Fatal(err)
	}
	page := testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/promotions/%d", sale.ID), manager, old, map[string]string{"HX-Request": "true"})
	form := adminForm(page.Body.String(), fmt.Sprintf("/manager/promotions/%d", sale.ID))
	if got, _ := adminInput(form, "sale_price_usd"); got != "1.99" {
		t.Fatalf("stale draft amount discarded: %q", got)
	}
	if got, _ := adminInput(form, "version"); got != "2" {
		t.Fatalf("stale draft retained authoritative old version: %q", got)
	}
	p := testProduct(t, s, 1)
	if got, _ := adminInput(form, "product_ref"); got != p.PromotionRef() {
		t.Fatalf("route-bound product identity/version not current: %q want %s", got, p.PromotionRef())
	}
	if err := s.SetFeatured(1, 0, true); err != nil {
		t.Fatal(err)
	}
	page = testRequest(t, a, http.MethodPost, "/manager/featured/1", manager, url.Values{"csrf": {manager.CSRF}, "feature_version": {"0"}}, nil)
	form = adminForm(page.Body.String(), "/manager/featured/1")
	if got, _ := adminInput(form, "feature_version"); got != "1" {
		t.Fatalf("feature authoritative version=%q", got)
	}
	_, attrs := adminInput(form, "featured")
	if strings.Contains(attrs, "checked") {
		t.Fatal("stale attempted unchecked choice discarded")
	}
	if p := testProduct(t, s, 1); !p.Featured {
		t.Fatal("stale feature request changed saved state")
	}
}
