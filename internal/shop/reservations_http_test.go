package shop

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func reservationHTTPApp(t *testing.T, s *Store, demo bool) *App {
	t.Helper()
	a, err := New(s, Config{Origin: testOrigin, ManagerPassword: testManagerPassword, DemoMode: demo})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func reservationManagerValues(manager Session, b Basket, qty string) url.Values {
	return url.Values{"csrf": {manager.CSRF}, "revision": {fmt.Sprint(b.Revision)}, "product_id": {"1"}, "quantity": {qty}, "reason": {"Customer reviewed their basket"}}
}
func reservationCheckoutValues(session Session, b Basket, instructions string) url.Values {
	return url.Values{"csrf": {session.CSRF}, "checkout_key": {session.CheckoutKey}, "revision": {fmt.Sprint(b.Revision)}, "quote": {b.Quote}, "instructions": {instructions}}
}

func TestReservationHTTPRejectsCSRFOnNewMutations(t *testing.T) {
	for _, route := range []string{"/cart/renew", "/manager/baskets/%s/items", "/manager/baskets/%s/renew", "/manager/baskets/practice"} {
		for _, kind := range []string{"missing", "incorrect", "other session", "query only"} {
			t.Run(route+"/"+kind, func(t *testing.T) {
				s, _, _ := newReservationStore(t)
				a := reservationHTTPApp(t, s, true)
				manager := testManager(t, s)
				testCart(t, s, manager.ID, 1, 2)
				b := testBasket(t, s, manager.ID)
				path := route
				if strings.Contains(path, "%s") {
					path = fmt.Sprintf(path, b.ID)
				}
				values := reservationManagerValues(manager, b, "3")
				switch kind {
				case "missing":
					values.Del("csrf")
				case "incorrect":
					values.Set("csrf", "wrong-token")
				case "other session":
					values.Set("csrf", testSession(t, s, "").CSRF)
				case "query only":
					values.Del("csrf")
					path += "?csrf=" + manager.CSRF
				}
				before := reservationSnapshot(t, s)
				w := testRequest(t, a, http.MethodPost, path, manager, values, nil)
				if w.Code != http.StatusForbidden {
					t.Fatalf("new mutation accepted %s CSRF: %d %s", kind, w.Code, w.Body.String())
				}
				assertReservationUnchanged(t, s, before)
			})
		}
	}
}

func TestReservationHTTPManagerGateOnAllBasketRoutes(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, false)
			visitor := testSession(t, s, "")
			testCart(t, s, visitor.ID, 1, 1)
			b := testBasket(t, s, visitor.ID)
			headers := map[string]string{}
			if htmx {
				headers["HX-Request"] = "true"
			}
			before := reservationSnapshot(t, s)
			for _, route := range []struct{ method, path string }{{http.MethodGet, "/manager/baskets"}, {http.MethodGet, "/manager/baskets/" + b.ID}, {http.MethodPost, "/manager/baskets/" + b.ID + "/items"}, {http.MethodPost, "/manager/baskets/" + b.ID + "/renew"}, {http.MethodPost, "/manager/baskets/practice"}} {
				var values url.Values
				if route.method == http.MethodPost {
					values = reservationManagerValues(visitor, b, "2")
				}
				assertRedirect(t, testRequest(t, a, route.method, route.path, visitor, values, headers), "/manager/login", htmx)
			}
			assertReservationUnchanged(t, s, before)
		})
	}
}

func TestReservationHTTPDemoScopeOpaqueIDsAndPracticeBaskets(t *testing.T) {
	s, _, _ := newReservationStore(t)
	a := reservationHTTPApp(t, s, true)
	manager := testManager(t, s)
	other := testSession(t, s, "")
	testCart(t, s, manager.ID, 1, 1)
	testCart(t, s, other.ID, 2, 2)
	mine := testBasket(t, s, manager.ID)
	foreign := testBasket(t, s, other.ID)
	before := reservationSnapshot(t, s)
	for _, route := range []struct{ method, path string }{{http.MethodGet, "/manager/baskets/" + foreign.ID}, {http.MethodPost, "/manager/baskets/" + foreign.ID + "/items"}, {http.MethodPost, "/manager/baskets/" + foreign.ID + "/renew"}} {
		var values url.Values
		if route.method == http.MethodPost {
			values = reservationManagerValues(manager, foreign, "1")
		}
		w := testRequest(t, a, route.method, route.path, manager, values, nil)
		if w.Code != http.StatusNotFound {
			t.Errorf("foreign demo basket route %s returned %d", route.path, w.Code)
		}
	}
	assertReservationUnchanged(t, s, before)
	for _, path := range []string{"/manager/baskets", "/manager/baskets/" + mine.ID, "/cart"} {
		w := testRequest(t, a, http.MethodGet, path, manager, nil, nil)
		if w.Code != http.StatusOK {
			t.Errorf("own route %s = %d", path, w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, other.ID) || strings.Contains(body, manager.ID) || strings.Contains(body, foreign.ID) {
			t.Errorf("basket HTML leaked raw session or foreign opaque identity at %s", path)
		}
	}
	if w := testRequest(t, a, http.MethodGet, "/manager/baskets/"+other.ID, manager, nil, nil); w.Code != http.StatusNotFound {
		t.Errorf("route accepted session credential as basket identity: %d", w.Code)
	}
	if count := testCount(t, s, "baskets"); count != len(before["baskets"]) {
		t.Error("viewing basket pages implicitly created practice shoppers")
	}
	productsBefore := migrationQuerySnapshot(t, s.db, `SELECT * FROM products ORDER BY id`)
	w := testRequest(t, a, http.MethodPost, "/manager/baskets/practice", manager, url.Values{"csrf": {manager.CSRF}}, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/manager/baskets" {
		t.Fatalf("practice creation response = %d, location %q: %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if after := migrationQuerySnapshot(t, s.db, `SELECT * FROM products ORDER BY id`); !reflect.DeepEqual(productsBefore, after) {
		t.Error("practice creation silently claimed shared stock")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM baskets WHERE synthetic=1 AND owner_session_id=?`, manager.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("created practice baskets = %d, %v", count, err)
	}
	page := testRequest(t, a, http.MethodGet, "/manager/baskets", manager, nil, nil)
	if page.Code != http.StatusOK || strings.Contains(page.Body.String(), other.ID) || strings.Contains(page.Body.String(), manager.ID) {
		t.Errorf("practice listing leaked session identity: %d", page.Code)
	}
	anotherManager := testManager(t, s)
	rows, err := s.db.Query(`SELECT id FROM baskets WHERE synthetic=1 AND owner_session_id=?`, manager.ID)
	if err != nil {
		t.Fatal(err)
	}
	var syntheticIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		syntheticIDs = append(syntheticIDs, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, id := range syntheticIDs {
		if w := testRequest(t, a, http.MethodGet, "/manager/baskets/"+id, anotherManager, nil, nil); w.Code != http.StatusNotFound {
			t.Errorf("practice shopper leaked to another demo visitor: %d", w.Code)
		}
	}
}

func TestReservationHTTPManagerEditVersionGuardAndEscapedReason(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, false)
			manager := testManager(t, s)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 2)
			b := testBasket(t, s, owner.ID)
			initial := testProduct(t, s, 1).Stock
			headers := map[string]string{}
			if htmx {
				headers["HX-Request"] = "true"
			}
			path := "/manager/baskets/" + b.ID
			values := reservationManagerValues(manager, b, "3")
			reason := `Customer said <img src=x onerror=alert(1)>`
			values.Set("reason", reason)
			w := testRequest(t, a, http.MethodPost, path+"/items", manager, values, headers)
			if htmx {
				if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "<!doctype html>") {
					t.Fatalf("HTMX basket edit failed: %d %s", w.Code, w.Body.String())
				}
			} else {
				assertRedirect(t, w, path, false)
			}
			saved := testBasket(t, s, owner.ID)
			if saved.Count != 3 || saved.Revision <= b.Revision {
				t.Fatalf("manager basket edit did not persist: %+v", saved)
			}
			if p := testProduct(t, s, 1); p.Stock != initial-1 || p.Reserved != 3 {
				t.Errorf("manager edit inventory: %+v", p)
			}
			detail := testRequest(t, a, http.MethodGet, path, manager, nil, nil)
			body := detail.Body.String()
			if detail.Code != http.StatusOK || strings.Contains(body, reason) || !strings.Contains(body, html.EscapeString(reason)) {
				t.Errorf("audit reason not rendered as escaped plain text: %d %s", detail.Code, body)
			}
			if strings.Contains(body, owner.ID) || strings.Contains(body, manager.ID) {
				t.Error("manager detail leaked raw session tokens")
			}
			before := reservationSnapshot(t, s)
			values.Set("quantity", "1")
			w = testRequest(t, a, http.MethodPost, path+"/items", manager, values, headers)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrConflict.Error()) {
				t.Errorf("stale basket form missing visible conflict: %d %s", w.Code, w.Body.String())
			}
			assertReservationUnchanged(t, s, before)
		})
	}
}

func TestReservationHTTPInvalidQuantitiesAndRevisionsHaveVisibleErrors(t *testing.T) {
	s, _, _ := newReservationStore(t)
	a := reservationHTTPApp(t, s, false)
	manager := testManager(t, s)
	testCart(t, s, manager.ID, 1, 2)
	b := testBasket(t, s, manager.ID)
	for _, route := range []string{"/cart", "/manager/baskets/" + b.ID + "/items"} {
		for _, field := range []string{"product_id", "quantity", "revision"} {
			for _, invalid := range []string{"", "nope", "9223372036854775808", "-9223372036854775809"} {
				t.Run(route+"/"+field+"/"+invalid, func(t *testing.T) {
					values := reservationManagerValues(manager, b, "1")
					values.Set("return", "cart")
					values.Set(field, invalid)
					before := reservationSnapshot(t, s)
					w := testRequest(t, a, http.MethodPost, route, manager, values, map[string]string{"HX-Request": "true"})
					if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrInvalid.Error()) {
						t.Errorf("invalid %s lacked visible validation: %d %s", field, w.Code, w.Body.String())
					}
					assertReservationUnchanged(t, s, before)
				})
			}
		}
	}
}

func TestReservationHTTPExpiryRequiresExplicitRenewal(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, c, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, false)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 2)
			held := testBasket(t, s, owner.ID)
			c.at(held.HoldUntil)
			headers := map[string]string{}
			if htmx {
				headers["HX-Request"] = "true"
			}
			page := testRequest(t, a, http.MethodGet, "/cart", owner, nil, headers)
			expired := testBasket(t, s, owner.ID)
			if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `action="/cart/renew"`) || expired.CanCheckout || expired.Lines[0].Reserved != 0 {
				t.Fatalf("expired cart did not require renewal: %d %s", page.Code, page.Body.String())
			}
			before := reservationSnapshot(t, s)
			_ = testRequest(t, a, http.MethodGet, "/cart", owner, nil, headers)
			assertReservationUnchanged(t, s, before)
			session := testSession(t, s, owner.ID)
			w := testRequest(t, a, http.MethodPost, "/checkout", session, reservationCheckoutValues(session, expired, "Keep upright"), headers)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrHold.Error()) {
				t.Errorf("expired checkout lacks safe visible hold error: %d %s", w.Code, w.Body.String())
			}
			assertReservationUnchanged(t, s, before)
			w = testRequest(t, a, http.MethodPost, "/cart/renew", session, url.Values{"csrf": {session.CSRF}, "revision": {fmt.Sprint(expired.Revision)}}, headers)
			if htmx {
				if w.Code != http.StatusOK {
					t.Errorf("HTMX renewal=%d", w.Code)
				}
			} else {
				assertRedirect(t, w, "/cart", false)
			}
			renewed := testBasket(t, s, owner.ID)
			if !renewed.CanCheckout || renewed.NeedsReview || renewed.HoldUntil <= held.HoldUntil || renewed.Lines[0].Reserved != 2 {
				t.Errorf("explicit renewal failed: %+v", renewed)
			}
			before = reservationSnapshot(t, s)
			w = testRequest(t, a, http.MethodPost, "/cart/renew", session, url.Values{"csrf": {session.CSRF}, "revision": {fmt.Sprint(expired.Revision)}}, headers)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrConflict.Error()) {
				t.Errorf("stale renewal missing visible conflict: %d %s", w.Code, w.Body.String())
			}
			assertReservationUnchanged(t, s, before)
		})
	}
}

func TestReservationHTTPInstructionsArePlainTextAndImmutable(t *testing.T) {
	s, _, _ := newReservationStore(t)
	a := reservationHTTPApp(t, s, false)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	b := testBasket(t, s, owner.ID)
	session := testSession(t, s, owner.ID)
	instructions := "<script>alert(\"x\")</script>\r\nA & B\tplease"
	values := reservationCheckoutValues(session, b, instructions)
	w := testRequest(t, a, http.MethodPost, "/checkout", session, values, nil)
	orders, err := s.Orders(owner.ID, false)
	if err != nil || len(orders) != 1 {
		t.Fatalf("checkout instructions order missing: %+v, %v; response %d %s", orders, err, w.Code, w.Body.String())
	}
	id := orders[0].ID
	assertRedirect(t, w, fmt.Sprintf("/orders/%d", id), false)
	normalized := strings.ReplaceAll(instructions, "\r\n", "\n")
	if got := testOrder(t, s, id, owner.ID).Instructions; got != normalized {
		t.Errorf("plain instruction snapshot=%q", got)
	}
	manager := testManager(t, s)
	for _, tc := range []struct {
		path    string
		session Session
		headers map[string]string
	}{{fmt.Sprintf("/orders/%d", id), owner, nil}, {fmt.Sprintf("/manager/orders/%d", id), manager, nil}} {
		page := testRequest(t, a, http.MethodGet, tc.path, tc.session, nil, tc.headers)
		body := page.Body.String()
		if page.Code != http.StatusOK || strings.Contains(body, `<script>alert`) || !strings.Contains(body, html.EscapeString(normalized)) {
			t.Errorf("instructions missing or unsafe on %s: %d %s", tc.path, page.Code, body)
		}
	}
	// The polling response replaces only the status panel. Instructions stay
	// outside that panel in the full receipt and must not become executable.
	status := testRequest(t, a, http.MethodGet, fmt.Sprintf("/orders/%d/status", id), owner, nil, map[string]string{"HX-Request": "true"})
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), `<script>alert`) || !strings.Contains(status.Body.String(), "0% shopped") {
		t.Errorf("unsafe or incorrect status fragment: %d %s", status.Code, status.Body.String())
	}
	before := reservationSnapshot(t, s)
	values.Set("instructions", "Replace historical instruction")
	assertRedirect(t, testRequest(t, a, http.MethodPost, "/checkout", session, values, nil), fmt.Sprintf("/orders/%d", id), false)
	assertReservationUnchanged(t, s, before)
}

func TestReservationHTTPInstructionsInvalidDoesNotConsumeStock(t *testing.T) {
	s, _, _ := newReservationStore(t)
	a := reservationHTTPApp(t, s, false)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	b := testBasket(t, s, owner.ID)
	session := testSession(t, s, owner.ID)
	for _, bad := range []string{strings.Repeat("界", 501), "bad\x00note", string([]byte{'x', 0xff})} {
		before := reservationSnapshot(t, s)
		w := testRequest(t, a, http.MethodPost, "/checkout", session, reservationCheckoutValues(session, b, bad), map[string]string{"HX-Request": "true"})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrInvalid.Error()) {
			t.Errorf("invalid instructions not visibly rejected: %d %s", w.Code, w.Body.String())
		}
		assertReservationUnchanged(t, s, before)
	}
}
