package shop

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func stockPostFormCount(body string) int {
	count := 0
	for _, form := range adminFormRE.FindAllStringSubmatch(body, -1) {
		if adminAttr(form[1], "action") == "/manager/inventory" {
			count++
		}
	}
	return count
}
func TestStockHTTPSelectionIsOneReasonedFormAndCloseWorks(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s := newTestStore(t)
			a := reservationHTTPApp(t, s, false)
			manager := testManager(t, s)
			page := testRequest(t, a, http.MethodGet, "/manager/stock", manager, nil, managerDraftHeaders(htmx))
			body := page.Body.String()
			if page.Code != 200 || stockPostFormCount(body) != 0 {
				t.Fatalf("unselected page=%d forms=%d", page.Code, stockPostFormCount(body))
			}
			if got := len(regexp.MustCompile(`id="stock-product-\d+"`).FindAllString(body, -1)); got != 12 {
				t.Errorf("inventory rendered %d rows, want 12", got)
			}
			if !adminFindLink(body, func(u *url.URL) bool { return u.Path == "/manager/stock" && u.Query().Get("page") == "2" }) {
				t.Error("missing next page")
			}
			path := "/manager/stock?q=Honeycrisp&product=1&stock=available&sort=reserved"
			page = testRequest(t, a, http.MethodGet, path, manager, nil, managerDraftHeaders(htmx))
			body = page.Body.String()
			if page.Code != 200 || stockPostFormCount(body) != 1 {
				t.Fatalf("selected page=%d forms=%d: %s", page.Code, stockPostFormCount(body), body)
			}
			form := adminForm(body, "/manager/inventory")
			for key, want := range map[string]string{"product_id": "1", "product": "1", "q": "Honeycrisp", "stock": "available", "sort": "reserved", "csrf": manager.CSRF} {
				if got, _ := adminInput(form, key); got != want {
					t.Errorf("form %s=%q want %q", key, got, want)
				}
			}
			if !strings.Contains(body, "each available") || !strings.Contains(body, "each reserved") {
				t.Error("selection missing explicit quantity labels")
			}
			if !adminFindLink(body, func(u *url.URL) bool {
				return u.Path == "/manager/stock" && u.Query().Get("product") == "" && u.Fragment == "inventory"
			}) {
				t.Error("missing close link")
			}
			page = testRequest(t, a, http.MethodGet, "/manager/stock?q=Honeycrisp&stock=available&sort=reserved", manager, nil, nil)
			if stockPostFormCount(page.Body.String()) != 0 {
				t.Error("close immediately reopened the only product")
			}
			if htmx && strings.Contains(body, "<!doctype html>") {
				t.Error("selection HTMX contains full document")
			}
		})
	}
}

func TestStockHTTPFiltersStayThroughDraftSuccessAndPagination(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s := newTestStore(t)
			a := reservationHTTPApp(t, s, false)
			manager := testManager(t, s)
			p := testProduct(t, s, 1)
			values := url.Values{"csrf": {manager.CSRF}, "product_id": {"1"}, "product": {"1"}, "version": {fmt.Sprint(p.Version)}, "delta": {"bad"}, "reason": {`Review <script>unsafe()</script> & correction`}, "q": {"No matching product"}, "department": {fmt.Sprint(p.CategoryID)}, "stock": {"low"}, "lifecycle": {"all"}, "unit": {"each"}, "sort": {"available"}, "page": {"2"}, "action": {"stock"}, "activity_product": {"1"}, "from": {"2026-10-01"}, "to": {"2026-10-01"}, "activity_page": {"2"}}
			before := reservationSnapshot(t, s)
			page := testRequest(t, a, http.MethodPost, "/manager/inventory", manager, values, managerDraftHeaders(htmx))
			body := page.Body.String()
			if page.Code != 200 || stockPostFormCount(body) != 1 {
				t.Fatalf("draft page=%d %s", page.Code, body)
			}
			assertManagerDraftResponse(t, body, htmx, ErrInvalid)
			form := adminForm(body, "/manager/inventory")
			for _, key := range []string{"product", "q", "department", "stock", "lifecycle", "unit", "sort", "action", "activity_product", "from", "to", "activity_page"} {
				if got, _ := adminInput(form, key); got != values.Get(key) {
					t.Errorf("draft lost %s=%q", key, got)
				}
			}
			if strings.Contains(body, "<script>unsafe()</script>") || !strings.Contains(body, html.EscapeString(values.Get("reason"))) {
				t.Error("draft HTML unsafe or lost")
			}
			if !strings.Contains(body, "No products match these filters.") {
				t.Error("filter no-match state missing")
			}
			assertReservationUnchanged(t, s, before)
			values.Set("delta", "1")
			page = testRequest(t, a, http.MethodPost, "/manager/inventory", manager, values, managerDraftHeaders(htmx))
			location := page.Header().Get("Location")
			if htmx {
				location = page.Header().Get("HX-Push-Url")
			}
			dest, err := url.Parse(location)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"product", "q", "department", "stock", "lifecycle", "unit", "sort", "action", "activity_product", "from", "to", "activity_page"} {
				if got := dest.Query().Get(key); got != values.Get(key) {
					t.Errorf("save lost %s=%q at %s", key, got, dest)
				}
			}
			if got := testProduct(t, s, 1); got.Stock != p.Stock+1 || got.Version != p.Version+1 {
				t.Error("save not applied exactly once")
			}
		})
	}
}

func TestStockHTTPActivityScopesDataCountsAndLinks(t *testing.T) {
	for _, demo := range []bool{false, true} {
		t.Run(fmt.Sprint(demo), func(t *testing.T) {
			s := newTestStore(t)
			a := reservationHTTPApp(t, s, demo)
			manager := testManager(t, s)
			other := testSession(t, s, "")
			testCart(t, s, manager.ID, 1, 1)
			own := reservationCheckout(t, s, manager.ID, "")
			testCart(t, s, other.ID, 1, 1)
			foreign := reservationCheckout(t, s, other.ID, "")
			testExec(t, s, "UPDATE orders SET reference='VISIBLE-OWNER-ORDER' WHERE id=?", own)
			testExec(t, s, "UPDATE orders SET reference='SECRET-FOREIGN-ORDER' WHERE id=?", foreign)
			foreignBasket := testBasket(t, s, other.ID)
			testExec(t, s, "INSERT INTO basket_events(basket_id,action,reason,details) VALUES(?,'set quantity','SECRET-FOREIGN-REASON','Changed basket')", foreignBasket.ID)
			for _, path := range []string{"/manager/stock?view=activity", "/manager/stock?view=activity&action=order-placed&activity_product=1", "/manager/stock?view=activity&activity_page=999999"} {
				page := testRequest(t, a, http.MethodGet, path, manager, nil, nil)
				body := page.Body.String()
				if page.Code != 200 {
					t.Fatalf("activity=%d %s", page.Code, body)
				}
				if !strings.Contains(body, "VISIBLE-OWNER-ORDER") {
					t.Error("own order event missing")
				}
				if strings.Contains(body, "SECRET-FOREIGN-ORDER") == demo {
					t.Errorf("wrong order visibility demo=%t path=%s", demo, path)
				}
				if demo && (strings.Contains(body, foreignBasket.ID) || strings.Contains(body, "SECRET-FOREIGN-REASON") || adminFindLink(body, func(u *url.URL) bool { return u.Path == fmt.Sprintf("/manager/orders/%d", foreign) })) {
					t.Error("foreign basket/order link leaked")
				}
				if strings.Contains(body, manager.ID) || strings.Contains(body, other.ID) {
					t.Error("session identifier displayed as actor")
				}
				if stockPostFormCount(body) != 0 {
					t.Error("activity screen embeds adjustment forms")
				}
			}
			page := testRequest(t, a, http.MethodGet, "/manager/stock?view=activity&from=2026-10-02&to=2026-10-01", manager, nil, nil)
			if !strings.Contains(page.Body.String(), "The start date must be on or before") || strings.Contains(page.Body.String(), "VISIBLE-OWNER-ORDER") {
				t.Error("invalid dates broadened history")
			}
		})
	}
}

func TestStockHTTPGateBadIdentityArchiveAndImmutableReceipt(t *testing.T) {
	s := newTestStore(t)
	a := reservationHTTPApp(t, s, true)
	manager := testManager(t, s)
	visitor := testSession(t, s, "")
	for _, path := range []string{"/manager/stock", "/manager/stock?view=activity"} {
		assertRedirect(t, testRequest(t, a, http.MethodGet, path, visitor, nil, nil), "/manager/login", false)
	}
	for _, id := range []string{"bogus", "0", "-1", "999999", "9223372036854775808"} {
		if page := testRequest(t, a, http.MethodGet, "/manager/stock?product="+id, manager, nil, nil); page.Code != 404 {
			t.Errorf("bad product %s status=%d", id, page.Code)
		}
	}
	testCart(t, s, manager.ID, 1, 1)
	orderID := reservationCheckout(t, s, manager.ID, "")
	receipt, err := s.Order(orderID, manager.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	p := testProduct(t, s, 1)
	testExec(t, s, "UPDATE products SET archived=1 WHERE id=1")
	page := testRequest(t, a, http.MethodGet, "/manager/stock?product=1&lifecycle=archived", manager, nil, nil)
	if page.Code != 200 || stockPostFormCount(page.Body.String()) != 0 || !strings.Contains(page.Body.String(), "Restore this product") {
		t.Error("archived product has editable stock form")
	}
	values := url.Values{"csrf": {manager.CSRF}, "product_id": {"1"}, "version": {fmt.Sprint(p.Version)}, "delta": {"1"}, "reason": {"Rejected archived change"}}
	before := reservationSnapshot(t, s)
	page = testRequest(t, a, http.MethodPost, "/manager/inventory", manager, values, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Not saved.") {
		t.Error("archive rejection draft missing")
	}
	assertReservationUnchanged(t, s, before)
	testExec(t, s, "UPDATE products SET archived=0 WHERE id=1")
	if err := s.Adjust(1, 2, p.Version, "After-order stock correction"); err != nil {
		t.Fatal(err)
	}
	after, err := s.Order(orderID, manager.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(receipt) != fmt.Sprint(after) {
		t.Error("adjustment changed immutable receipt")
	}
}
