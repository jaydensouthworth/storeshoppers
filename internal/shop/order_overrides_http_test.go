package shop

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func overrideFields(session Session, c OrderCommand) url.Values {
	return url.Values{"catalog_quote": {c.CatalogQuote}, "csrf": {session.CSRF}, "version": {fmt.Sprint(c.Version)}, "command_key": {c.Key}, "action": {c.Action}, "product_id": {fmt.Sprint(c.ProductID)}, "replacement_id": {fmt.Sprint(c.ReplacementID)}, "quantity": {fmt.Sprint(c.Quantity)}, "reason": {c.Reason}, "disposition": {c.Disposition}, "remainder": {c.Remainder}}
}
func TestHTTPOrderOverridesScopedFormsReplayAndCSRF(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s := newTestStore(t)
			a := pickingApp(t, s, true)
			owner, id := pickingFixture(t, s)
			owner = pickingLogin(t, a, owner)
			other := pickingLogin(t, a, testSession(t, s, ""))
			path := fmt.Sprintf("/manager/orders/%d/override", id)
			c := overrideCommand(t, s, id, owner.ID, "set", 1, 3)
			for _, visitor := range []Session{other, {}} {
				w := testRequest(t, a, http.MethodPost, path, visitor, overrideFields(visitor, c), pickingHeaders(htmx))
				want := 404
				if visitor.ID == "" {
					want = 403
				}
				if w.Code != want {
					t.Fatalf("scoped override=%d want %d", w.Code, want)
				}
			}
			bad := overrideFields(owner, c)
			bad.Set("csrf", "wrong")
			w := testRequest(t, a, http.MethodPost, path, owner, bad, nil)
			if w.Code != 403 {
				t.Fatal("CSRF accepted")
			}
			bad = overrideFields(other, c)
			bad.Set("version", "broken")
			w = testRequest(t, a, http.MethodPost, path, other, bad, nil)
			if w.Code != 404 || strings.Contains(w.Body.String(), "Manager verified") {
				t.Fatal("invalid foreign request leaked data")
			}
			w = testRequest(t, a, http.MethodGet, strings.TrimSuffix(path, "/override"), owner, nil, pickingHeaders(htmx))
			for _, required := range []string{"WORKING PICK LIST", "Mark picked", "Add an item", "Substitute item", "Finish with picked items", "Cancel the entire order", "ORIGINAL PLACED RECEIPT", "command_key", "order_version"} {
				if !strings.Contains(w.Body.String(), required) {
					t.Fatalf("working control missing %q", required)
				}
			}
			for range 2 {
				w = testRequest(t, a, http.MethodPost, path, owner, overrideFields(owner, c), pickingHeaders(htmx))
				if htmx {
					if w.Code != 200 || !strings.Contains(w.Body.String(), "Manager change saved") {
						t.Fatal("htmx override failed")
					}
				} else {
					assertRedirect(t, w, strings.TrimSuffix(path, "/override"), false)
				}
			}
			if testCount(t, s, "order_events") != 1 || testProduct(t, s, 1).Stock != 21 {
				t.Fatal("HTTP replay mutated twice")
			}
			c.Key = token()
			w = testRequest(t, a, http.MethodPost, path, owner, overrideFields(owner, c), nil)
			if !strings.Contains(w.Body.String(), ErrConflict.Error()) {
				t.Fatal("stale override not surfaced")
			}
			// Stale and missing normal advance forms cannot freeze a revised ticket.
			for _, version := range []string{"", fmt.Sprint(c.Version)} {
				w = testRequest(t, a, http.MethodPost, strings.TrimSuffix(path, "/override")+"/advance", owner, url.Values{"csrf": {owner.CSRF}, "status": {"Placed"}, "order_version": {version}}, nil)
				if w.Code != 200 || !strings.Contains(w.Body.String(), `role="alert"`) {
					t.Fatal("missing/stale transition accepted")
				}
			}
			// Scope private reasons even through the aggregate stock activity view.
			for _, visitor := range []Session{owner, other} {
				w = testRequest(t, a, http.MethodGet, "/manager/stock?view=activity&action=order-change", visitor, nil, nil)
				if w.Code != 200 {
					t.Fatal(w.Code)
				}
				found := strings.Contains(w.Body.String(), c.Reason)
				if found != (visitor.ID == owner.ID) {
					t.Fatal("activity ownership leaked or hid event")
				}
			}
		})
	}
}
func TestRealHTTPOrderOverridesFinishAndStableLine(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	a := pickingApp(t, s, true)
	owner = pickingLogin(t, a, owner)
	server := httptest.NewServer(a)
	defer server.Close()
	a.config.Origin = server.URL
	client := server.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	call := func(method, path string, values url.Values, hx bool) (int, string) {
		t.Helper()
		var body io.Reader
		if values != nil {
			body = strings.NewReader(values.Encode())
		}
		req, err := http.NewRequest(method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "shop_session", Value: owner.ID})
		if values != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", server.URL)
		}
		if hx {
			req.Header.Set("HX-Request", "true")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data)
	}
	path := fmt.Sprintf("/manager/orders/%d", id)
	status, body := call("GET", path, nil, false)
	if status != 200 || !strings.Contains(body, "ORIGINAL PLACED RECEIPT") {
		t.Fatal("network ticket failed", status)
	}
	c := overrideCommand(t, s, id, owner.ID, "set", 3, 1)
	if status, _ = call("POST", path+"/override", overrideFields(owner, c), false); status != 303 {
		t.Fatal("network add", status)
	}
	o := testOrder(t, s, id, owner.ID)
	if status, _ = call("POST", path+"/advance", url.Values{"csrf": {owner.CSRF}, "status": {"Placed"}, "order_version": {fmt.Sprint(o.Version)}}, false); status != 303 {
		t.Fatal("network start", status)
	}
	o = testOrder(t, s, id, owner.ID)
	line := o.WorkingItems[0]
	status, body = call("POST", fmt.Sprintf("%s/lines/%d/pick", path, line.LineID), url.Values{"csrf": {owner.CSRF}, "version": {fmt.Sprint(line.PickVersion)}, "picked": {"1"}}, true)
	if status != 200 || !strings.Contains(body, "Picked quantity saved") {
		t.Fatal("stable line network pick", status)
	}
	c = overrideCommand(t, s, id, owner.ID, "finish", 0, 0)
	c.Disposition = "writeoff"
	status, body = call("POST", path+"/override", overrideFields(owner, c), true)
	if status != 200 || !strings.Contains(body, "Completed · partial") || !strings.Contains(body, "1 of 4") {
		t.Fatal("partial network finish", status)
	}
	status, body = call("GET", fmt.Sprintf("/orders/%d/status", id), nil, true)
	if status != 200 || strings.Contains(body, "100% shopped") || !strings.Contains(body, "unavailable") || !strings.Contains(body, "FINAL TOTAL") || strings.Contains(body, "<!doctype") {
		t.Fatal("polled partial summary untruthful")
	}
	status, _ = call("POST", path+"/override", overrideFields(owner, c), false)
	if status != 303 {
		t.Fatal("network finish replay", status)
	}
}

func TestHTTPOrderOverridesNormalManagerAndPriceReview(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	a := pickingApp(t, s, false)
	manager := pickingLogin(t, a, testSession(t, s, ""))
	path := fmt.Sprintf("/manager/orders/%d/override", id)
	c := overrideCommand(t, s, id, owner.ID, "set", 3, 1)
	fields := overrideFields(manager, c)
	fields.Set("form_id", "add")
	fields.Set("reason", "Reviewed price & quantity")
	p := testProduct(t, s, 3)
	p.Price += 10
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	w := testRequest(t, a, http.MethodPost, path, manager, fields, pickingHeaders(true))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, ErrOrderQuote.Error()) || !strings.Contains(body, `value="Reviewed price &amp; quantity"`) {
		t.Fatal("price review did not preserve bounded draft", w.Code)
	}
	c = overrideCommand(t, s, id, owner.ID, "set", 3, 1)
	w = testRequest(t, a, http.MethodPost, path, manager, overrideFields(manager, c), nil)
	assertRedirect(t, w, strings.TrimSuffix(path, "/override"), false)
	if len(testOrder(t, s, id, owner.ID).WorkingItems) != 3 {
		t.Fatal("normal manager could not change authorized visitor order")
	}
	// A valid customer CSRF never supplies a missing manager grant.
	c = overrideCommand(t, s, id, owner.ID, "set", 1, 3)
	before := testOrder(t, s, id, owner.ID).Version
	w = testRequest(t, a, http.MethodPost, path, owner, overrideFields(owner, c), nil)
	assertRedirect(t, w, "/manager/login", false)
	if testOrder(t, s, id, owner.ID).Version != before {
		t.Fatal("customer changed manager order")
	}
}
