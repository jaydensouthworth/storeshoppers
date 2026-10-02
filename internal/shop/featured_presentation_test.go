package shop

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestFeaturedShelfSkipsCircularWithoutChangingSelection(t *testing.T) {
	s, clock, _ := promotionTestStore(t)
	a := reservationHTTPApp(t, s, false)
	ends := clock.now().Unix() + 3600
	for id := int64(1); id <= 7; id++ {
		if err := s.SetFeatured(id, 0, true); err != nil {
			t.Fatal(err)
		}
		if id <= 3 {
			p := testProduct(t, s, id)
			saveTestPromotion(t, s, id, p.Price-100, clock.now().Unix()-60, ends)
		}
	}
	v := View{FeaturedOnly: true}
	if err := a.populateStore(&v); err != nil {
		t.Fatal(err)
	}
	if v.FeaturedCount != 7 || len(v.Products) != 7 || len(v.WeeklySales) != 3 || len(v.FeaturedProducts) != 4 {
		t.Fatalf("presentation changed catalog selection: total=%d filtered=%d circular=%d shelf=%d", v.FeaturedCount, len(v.Products), len(v.WeeklySales), len(v.FeaturedProducts))
	}
	for i, p := range v.FeaturedProducts {
		if p.ID != int64(i+4) {
			t.Fatalf("shelf did not refill with distinct featured picks: %+v", v.FeaturedProducts)
		}
	}
	clock.at(ends)
	v = View{FeaturedOnly: true}
	if err := a.populateStore(&v); err != nil {
		t.Fatal(err)
	}
	if v.FeaturedCount != 7 || len(v.Products) != 7 || len(v.WeeklySales) != 0 || len(v.FeaturedProducts) != 4 || v.FeaturedProducts[0].ID != 1 {
		t.Fatal("expired circular offers did not return to featured presentation")
	}
}

func TestCircularOnlyFeaturedPicksKeepDiscoveryWithoutDuplicateShelf(t *testing.T) {
	s, clock, _ := promotionTestStore(t)
	a := reservationHTTPApp(t, s, false)
	visitor := testSession(t, s, "")
	for id := int64(1); id <= 3; id++ {
		if err := s.SetFeatured(id, 0, true); err != nil {
			t.Fatal(err)
		}
		p := testProduct(t, s, id)
		saveTestPromotion(t, s, id, p.Price-100, clock.now().Unix()-60, clock.now().Unix()+3600)
	}
	for _, htmx := range []bool{false, true} {
		page := testRequest(t, a, http.MethodGet, "/?featured=1", visitor, nil, managerDraftHeaders(htmx))
		if page.Code != http.StatusOK {
			t.Fatalf("featured storefront status %d", page.Code)
		}
		body := page.Body.String()
		if strings.Contains(body, `class="featured-shelf"`) {
			t.Error("circular products were repeated in a second merchandising shelf")
		}
		if !strings.Contains(body, `href="/?featured=1#catalog-title"`) || !strings.Contains(adminVisibleText(body), "3 featured picks") {
			t.Error("featured filter discovery disappeared with the duplicate shelf")
		}
	}
}

func TestFeaturedShelfMixedPricesLongTitlesAndCartContext(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprintf("htmx=%t", htmx), func(t *testing.T) {
			s, clock, _ := promotionTestStore(t)
			a := reservationHTTPApp(t, s, false)
			visitor := testSession(t, s, "")
			names := map[int64]string{
				4: "Family pantry whole grain penne pasta for quick suppers & weekend meals",
				5: "Market strawberries grown nearby and picked for sunny weekend picnics",
			}
			for id, name := range names {
				p := testCatalogProduct(t, s, id)
				p.Name, p.CategoryID = name, testProduct(t, s, 1).CategoryID
				if _, err := s.SaveProduct(p); err != nil {
					t.Fatal(err)
				}
				if err := s.SetFeatured(id, 0, true); err != nil {
					t.Fatal(err)
				}
			}
			// Three earlier offers occupy the circular, leaving the fourth sale
			// beside a regular-price pick in the independent featured shelf.
			for _, id := range []int64{1, 2, 3, 5} {
				p := testProduct(t, s, id)
				saveTestPromotion(t, s, id, p.Price-100, clock.now().Unix()-60, clock.now().Unix()+3600)
			}
			filters := url.Values{"q": {"weekend"}, "category": {"Produce"}, "sales": {"1"}, "featured": {"1"}}
			shelfRE := regexp.MustCompile(`(?s)<section\b[^>]*aria-labelledby="featured-title"[^>]*>.*?</section>`)
			articleRE := regexp.MustCompile(`(?s)<article\b[^>]*>.*?</article>`)
			strongRE := regexp.MustCompile(`(?s)<strong\b[^>]*>(.*?)</strong>`)
			delRE := regexp.MustCompile(`(?s)<del\b[^>]*>(.*?)</del>`)
			buttonRE := regexp.MustCompile(`<button\b([^>]*)>`)
			var wantTotal int64
			for i, addID := range []int64{4, 5} {
				page := testRequest(t, a, http.MethodGet, "/?"+filters.Encode(), visitor, nil, managerDraftHeaders(htmx))
				if page.Code != http.StatusOK {
					t.Fatalf("storefront status %d", page.Code)
				}
				body := page.Body.String()
				ids := map[string]bool{}
				for _, tag := range adminTagRE.FindAllString(body, -1) {
					if id := adminAttr(tag, "id"); id != "" {
						if ids[id] {
							t.Errorf("duplicate page ID %q", id)
						}
						ids[id] = true
					}
				}
				articles := articleRE.FindAllString(shelfRE.FindString(body), -1)
				if len(articles) != 2 {
					t.Fatalf("mixed featured shelf has %d products", len(articles))
				}
				var addValues url.Values
				for j, article := range articles {
					p := testProduct(t, s, int64(j+4))
					strong := strongRE.FindStringSubmatch(article)
					wantPrice := Money(p.EffectivePrice())
					if p.OnSale() {
						wantPrice = "Sale price " + wantPrice
					}
					if len(strong) != 2 || adminVisibleText(strong[1]) != wantPrice {
						t.Errorf("product %d lacks its current price semantics", p.ID)
					}
					regular := delRE.FindStringSubmatch(article)
					if p.OnSale() {
						if len(regular) != 2 || adminVisibleText(regular[1]) != "Regular price "+Money(p.Price) || !strings.Contains(adminVisibleText(article), "Ends "+p.SaleEndLabel()) {
							t.Error("sale pick lost its former price or end date")
						}
					} else if len(regular) != 0 || strings.Contains(adminVisibleText(article), "Ends ") || strings.Contains(article, "Sale price") {
						t.Error("regular-price pick acquired sale-only content")
					}
					if !strings.Contains(adminVisibleText(article), p.Name) || !adminFindLink(article, func(u *url.URL) bool {
						return u.Path == fmt.Sprintf("/products/%d", p.ID) && u.Query().Encode() == filters.Encode()
					}) {
						t.Errorf("product %d lost its complete long title or contextual detail link", p.ID)
					}
					form := adminForm(article, "/cart")
					formParts := adminFormRE.FindStringSubmatch(form)
					if len(formParts) != 3 || adminAttr(formParts[1], "method") != "post" || adminAttr(formParts[1], "hx-post") != "/cart" || adminAttr(formParts[1], "hx-target") != "#workspace" || adminAttr(formParts[1], "hx-swap") != "outerHTML" || adminAttr(formParts[1], "hx-disabled-elt") != "find button" {
						t.Fatal("featured form lost its ordinary or enhanced submission contract")
					}
					button := buttonRE.FindStringSubmatch(form)
					if len(button) != 2 || adminAttr(button[1], "id") != fmt.Sprintf("featured-add-%d", p.ID) || adminAttr(button[1], "aria-label") != "Add featured "+p.Name+" to basket" {
						t.Fatalf("product %d lacks its unique, fully named add control", p.ID)
					}
					values := url.Values{}
					for _, input := range adminInputRE.FindAllStringSubmatch(form, -1) {
						values.Set(adminAttr(input[1], "name"), adminAttr(input[1], "value"))
					}
					for key := range filters {
						if values.Get(key) != filters.Get(key) {
							t.Errorf("featured form lost %s", key)
						}
					}
					if values.Get("product_id") != fmt.Sprint(p.ID) || values.Get("csrf") != visitor.CSRF || values.Get("revision") != fmt.Sprint(testBasket(t, s, visitor.ID).Revision) || values.Get("quantity") != "1" || values.Get("mode") != "add" || values.Get("return") != "store" {
						t.Fatal("featured form lost its protected cart fields")
					}
					if p.ID == addID {
						addValues = values
						wantTotal += p.EffectivePrice()
					}
				}
				result := testRequest(t, a, http.MethodPost, "/cart", visitor, addValues, managerDraftHeaders(htmx))
				if htmx {
					if result.Code != http.StatusOK || !shelfRE.MatchString(result.Body.String()) {
						t.Fatalf("HTMX add did not return the storefront: %d", result.Code)
					}
					returnedForm := adminForm(shelfRE.FindString(result.Body.String()), "/cart")
					for key := range filters {
						if value, _ := adminInput(returnedForm, key); value != filters.Get(key) {
							t.Errorf("HTMX add lost %s in its refreshed storefront", key)
						}
					}
				} else {
					location, err := url.Parse(result.Header().Get("Location"))
					if err != nil || result.Code != http.StatusSeeOther || location.Path != "/" || location.Query().Encode() != filters.Encode() {
						t.Fatalf("no-JS add lost storefront context: %d %q", result.Code, result.Header().Get("Location"))
					}
				}
				if basket := testBasket(t, s, visitor.ID); basket.Count != int64(i+1) || basket.Total != wantTotal {
					t.Fatalf("rendered featured form added incorrect count/price: %d/%d", basket.Count, basket.Total)
				}
			}
		})
	}
}
