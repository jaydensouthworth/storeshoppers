package shop

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Keep these checks on the rendered circular and its actual forms, independent
// of how the offers are laid out at each viewport size.
func weeklyHero(t *testing.T, body string) string {
	t.Helper()
	for _, tag := range regexp.MustCompile(`<section\b[^>]*>`).FindAllStringIndex(body, -1) {
		if adminAttr(body[tag[0]:tag[1]], "aria-labelledby") != "store-title" {
			continue
		}
		end := strings.Index(body[tag[1]:], "</section>")
		if end < 0 {
			t.Fatal("weekly circular has no closing section")
		}
		return body[tag[0] : tag[1]+end+len("</section>")]
	}
	t.Fatal("storefront has no named weekly circular")
	return ""
}

func TestWeeklyCircularShowsOnlyCurrentOffersAtEveryCount(t *testing.T) {
	for _, count := range []int{0, 1, 2, 3, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, clock, _ := promotionTestStore(t)
			a := reservationHTTPApp(t, s, false)
			visitor := testSession(t, s, "")
			now := clock.now().Unix()
			for id := int64(1); id <= int64(count); id++ {
				p := testProduct(t, s, id)
				saveTestPromotion(t, s, id, p.Price-100, now-60, now+3600)
			}
			// Upcoming and expired offers must never leak into the circular count,
			// prices or Add controls.
			saveTestPromotion(t, s, 6, 199, now+3600, now+7200)
			saveTestPromotion(t, s, 7, 199, now-7200, now-3600)
			for _, htmx := range []bool{false, true} {
				page := testRequest(t, a, http.MethodGet, "/", visitor, nil, managerDraftHeaders(htmx))
				if page.Code != http.StatusOK {
					t.Fatalf("HTMX=%t: storefront status=%d", htmx, page.Code)
				}
				hero := weeklyHero(t, page.Body.String())
				forms := adminFormRE.FindAllStringSubmatch(hero, -1)
				if len(forms) != min(count, 3) {
					t.Fatalf("%d active offers produce %d circular forms", count, len(forms))
				}
				if count == 0 {
					if !strings.Contains(adminVisibleText(hero), "No active sales right now") || strings.Contains(hero, "Sale price") {
						t.Error("empty circular invents an offer or lacks an honest explanation")
					}
					continue
				}
				if !strings.Contains(adminVisibleText(hero), fmt.Sprintf("%d LIVE OFFER", count)) {
					t.Error("circular count does not include every active offer")
				}
				if !adminFindLink(hero, func(u *url.URL) bool {
					return u.Path == "/" && u.Query().Get("sales") == "1" && u.Fragment == "catalog-title"
				}) {
					t.Error("circular has no path to every sale")
				}
				seen := map[int64]bool{}
				for _, form := range forms {
					idText, _ := adminInput(form[0], "product_id")
					id, err := num(idText)
					if err != nil || seen[id] || id < 1 || id > int64(min(count, 3)) {
						t.Fatalf("duplicated or ineligible circular product %q", idText)
					}
					seen[id] = true
					p := testProduct(t, s, id)
					for _, want := range []string{p.Name, Money(p.Price), Money(p.EffectivePrice()), p.SaleEndLabel(), fmt.Sprintf(`aria-labelledby="weekly-title-%d"`, id), fmt.Sprintf(`id="weekly-title-%d"`, id)} {
						if !strings.Contains(hero, want) {
							t.Errorf("current offer lacks %q", want)
						}
					}
				}
				if count > 3 && !strings.Contains(adminVisibleText(hero), "3 picks from 4 live offers") {
					t.Error("circular does not explain that additional sales are available")
				}
			}
		})
	}
}

func TestWeeklyCircularRenderedFormsAddAtSalePriceAndKeepFilters(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, clock, _ := promotionTestStore(t)
			a := reservationHTTPApp(t, s, false)
			visitor := testSession(t, s, "")
			saveTestPromotion(t, s, 1, 249, clock.now().Unix()-60, clock.now().Unix()+3600)
			if err := s.SetFeatured(1, 0, true); err != nil {
				t.Fatal(err)
			}
			filters := url.Values{"q": {"Honeycrisp"}, "category": {"Produce"}, "sales": {"1"}, "featured": {"1"}}
			page := testRequest(t, a, http.MethodGet, "/?"+filters.Encode(), visitor, nil, managerDraftHeaders(htmx))
			hero := weeklyHero(t, page.Body.String())
			form := adminForm(hero, "/cart")
			if form == "" {
				t.Fatal("sale has no real cart form")
			}
			values := url.Values{}
			for _, input := range adminInputRE.FindAllStringSubmatch(form, -1) {
				values.Set(adminAttr(input[1], "name"), adminAttr(input[1], "value"))
			}
			for key := range filters {
				if values.Get(key) != filters.Get(key) {
					t.Errorf("circular form lost %s", key)
				}
			}
			if values.Get("csrf") != visitor.CSRF || values.Get("quantity") != "1" || values.Get("mode") != "add" || values.Get("return") != "store" {
				t.Fatal("circular form lost its protected add-to-basket contract")
			}
			if !adminFindLink(hero, func(u *url.URL) bool {
				return u.Path == "/products/1" && u.Query().Get("q") == "Honeycrisp" && u.Query().Get("category") == "Produce" && u.Query().Get("sales") == "1" && u.Query().Get("featured") == "1"
			}) {
				t.Error("sale detail link lost storefront context")
			}
			result := testRequest(t, a, http.MethodPost, "/cart", visitor, values, managerDraftHeaders(htmx))
			if htmx {
				if result.Code != http.StatusOK || !strings.Contains(weeklyHero(t, result.Body.String()), "1 item on the list") {
					t.Fatalf("HTMX circular did not refresh basket: %d", result.Code)
				}
			} else {
				location, err := url.Parse(result.Header().Get("Location"))
				if err != nil || result.Code != http.StatusSeeOther {
					t.Fatalf("no-JS add did not redirect: %d", result.Code)
				}
				for key := range filters {
					if location.Query().Get(key) != filters.Get(key) {
						t.Errorf("no-JS add lost %s", key)
					}
				}
			}
			if basket := testBasket(t, s, visitor.ID); basket.Count != 1 || basket.Total != 249 {
				t.Fatalf("circular add count/price=%d/%d", basket.Count, basket.Total)
			}
		})
	}
}

func TestWeeklyCircularTracksPriceStockAndExpiry(t *testing.T) {
	s, clock, _ := promotionTestStore(t)
	a := reservationHTTPApp(t, s, false)
	visitor := testSession(t, s, "")
	sale := saveTestPromotion(t, s, 1, 249, clock.now().Unix()-60, clock.now().Unix()+3600)
	sale.SalePrice = 199
	if _, err := s.SavePromotion(sale); err != nil {
		t.Fatal(err)
	}
	p := testProduct(t, s, 1)
	if err := s.Adjust(1, -p.Stock, p.Version, "Empty test shelf"); err != nil {
		t.Fatal(err)
	}
	hero := weeklyHero(t, testRequest(t, a, http.MethodGet, "/", visitor, nil, nil).Body.String())
	if !strings.Contains(hero, "$1.99") || !strings.Contains(hero, "$3.49") || strings.Contains(hero, "$2.49") || !strings.Contains(hero, "Sold out") {
		t.Error("circular did not reflect the current sale, regular price and stock")
	}
	form := adminForm(hero, "/cart")
	button := regexp.MustCompile(`<button\b([^>]*)>`).FindStringSubmatch(form)
	if len(button) != 2 || !regexp.MustCompile(`\bdisabled(?:\s|$)`).MatchString(button[1]) || adminAttr(button[1], "aria-describedby") != "weekly-stock-1 weekly-end-1" {
		t.Error("sold-out offer remains enabled or lacks stock/date context")
	}
	clock.at(sale.Ends)
	hero = weeklyHero(t, testRequest(t, a, http.MethodGet, "/", visitor, nil, nil).Body.String())
	if !strings.Contains(hero, "No active sales right now") || strings.Contains(hero, "$1.99") || adminForm(hero, "/cart") != "" {
		t.Error("expired offer or price remains on the weekly circular")
	}
}
