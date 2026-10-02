package shop

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestProductPickerBoundedCodesAvailabilityAndScope(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner, id := pickingFixture(t, s)
	owner = pickingLogin(t, a, owner)
	other := pickingLogin(t, a, testSession(t, s, ""))
	o := testOrder(t, s, id, owner.ID)
	line := o.WorkingItems[0]
	results, more, err := s.ProductPickerResults("", 0, 0, nil)
	if err != nil || len(results) != 6 || !more {
		t.Fatal("picker not bounded", len(results), more, err)
	}
	for _, query := range []string{"sourdough", "SHOPDEMO-000003", "000000000103"} {
		rows, _, err := s.ProductPickerResults(query, 0, 0, nil)
		if err != nil || len(rows) != 1 || rows[0].ID != 3 {
			t.Fatal("name/SKU/code search", query, rows, err)
		}
	}
	rows, _, err := s.ProductPickerResults("strawberry jam", 0, 0, nil)
	if err != nil || len(rows) != 0 {
		t.Fatal("out of stock eligible", err)
	}
	path := fmt.Sprintf("/manager/orders/%d/products?context=sub&line_id=%d&product_q=spinach", id, line.LineID)
	w := testRequest(t, a, http.MethodGet, path, owner, nil, pickingHeaders(true))
	body := w.Body.String()
	if w.Code != 200 || strings.Count(body, `type="radio"`) != 1 || !strings.Contains(body, `name="replacement_id"`) || !strings.Contains(body, "recorded rate") || strings.Contains(body, "<!doctype") || strings.Contains(body, `name="order_version"`) {
		t.Fatal("scoped fragment invalid", w.Code, body)
	}
	w = testRequest(t, a, http.MethodGet, path, owner, nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<!doctype") {
		t.Fatal("normal fallback failed")
	}
	for _, route := range []string{path, fmt.Sprintf("/manager/orders/%d/products?context=sub&line_id=999999", id)} {
		visitor := other
		if strings.Contains(route, "999999") {
			visitor = owner
		}
		w = testRequest(t, a, http.MethodGet, route, visitor, nil, pickingHeaders(true))
		if w.Code != 404 || strings.Contains(w.Body.String(), "spinach") {
			t.Fatal("foreign picker context leaked", w.Code)
		}
	}
	w = testRequest(t, a, http.MethodGet, fmt.Sprintf("/manager/orders/%d/products?context=add&product_q=apples", id), owner, nil, pickingHeaders(true))
	if strings.Contains(w.Body.String(), `type="radio"`) {
		t.Fatal("add picker includes active existing line")
	}
	// Same catalog search must not reveal a foreign basket or its selected state.
	b := testBasket(t, s, owner.ID)
	testCart(t, s, owner.ID, 3, 1)
	bp := "/manager/baskets/" + b.ID + "/products?context=add&product_q=sourdough"
	w = testRequest(t, a, http.MethodGet, bp, owner, nil, pickingHeaders(true))
	if w.Code != 200 || strings.Contains(w.Body.String(), `type="radio"`) {
		t.Fatal("basket add includes existing item")
	}
	w = testRequest(t, a, http.MethodGet, bp, other, nil, pickingHeaders(true))
	if w.Code != 404 {
		t.Fatal("foreign basket picker accessible")
	}
}
func TestProductPickerStalePriceAndSelectedDraft(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner, id := pickingFixture(t, s)
	owner = pickingLogin(t, a, owner)
	c := overrideCommand(t, s, id, owner.ID, "set", 3, 1)
	c.Reason = ""
	p := testProduct(t, s, 3)
	p.Price += 100
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	values := overrideFields(owner, c)
	values.Set("form_id", "add")
	values.Set("product_q", "sourdough")
	values.Set("picker_context", "add")
	before := testProduct(t, s, 3).Stock
	w := testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/orders/%d/override", id), owner, values, pickingHeaders(true))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, ErrOrderQuote.Error()) || !strings.Contains(body, `name="product_q" value="sourdough"`) || !strings.Contains(body, `value="3" checked`) {
		t.Fatal("selected stale-price draft missing", w.Code, body)
	}
	if testProduct(t, s, 3).Stock != before || testCount(t, s, "order_events") != 0 {
		t.Fatal("stale search price mutated")
	}
	c = overrideCommand(t, s, id, owner.ID, "set", 3, 1)
	c.Reason = ""
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	c = overrideCommand(t, s, id, owner.ID, "set", 1, 0)
	c.Disposition = ""
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.ProductPickerResults("apples", 0, 0, map[int64]bool{2: true, 3: true})
	if err != nil || len(rows) == 0 || rows[0].ID != 1 {
		t.Fatal("removed product cannot return", err)
	}
	c = overrideCommand(t, s, id, owner.ID, "set", 1, 1)
	c.CatalogQuote = ""
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal("removed snapshot rate should persist", err)
	}
}
func TestCustomerStaleRevisionKeepsExplicitReviewDraft(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	b := testBasket(t, s, owner.ID)
	testCart(t, s, owner.ID, 2, 1)
	values := url.Values{"csrf": {owner.CSRF}, "revision": {fmt.Sprint(b.Revision)}, "product_id": {"1"}, "quantity": {"3"}, "return": {"cart"}}
	w := testRequest(t, a, http.MethodPost, "/cart", owner, values, pickingHeaders(true))
	if w.Code != 200 || w.Header().Get("X-Shop-Error") != "stale-version" || !strings.Contains(w.Body.String(), `value="3"`) {
		t.Fatal("stale draft not kept", w.Code)
	}
	if b := testBasket(t, s, owner.ID); b.Count != 2 {
		t.Fatal("stale mutation replayed")
	}
	if err := s.SetCartVersion(owner.ID, 1, 3, false, b.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal("stale version bypass", err)
	}
}
