package shop

import (
	"net/http"
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
