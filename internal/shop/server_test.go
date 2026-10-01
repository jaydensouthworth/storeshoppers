package shop

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testOrigin = "http://127.0.0.1"
const testManagerPassword = "a-local-demo-password"

func testApp(t *testing.T, s *Store, password string) *App {
	t.Helper()
	a, err := New(s, Config{Origin: testOrigin, ManagerPassword: password})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func testRequest(t *testing.T, a *App, method, path string, session Session, values url.Values, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body := ""
	if values != nil {
		body = values.Encode()
	}
	r := httptest.NewRequest(method, testOrigin+path, strings.NewReader(body))
	if values != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == http.MethodPost {
		r.Header.Set("Origin", testOrigin)
	}
	if session.ID != "" {
		r.AddCookie(&http.Cookie{Name: "shop_session", Value: session.ID})
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func assertRedirect(t *testing.T, w *httptest.ResponseRecorder, path string, htmx bool) {
	t.Helper()
	if htmx {
		if w.Code != http.StatusOK || w.Header().Get("HX-Redirect") != path {
			t.Errorf("HTMX response = %d, HX-Redirect %q, want 200 and %q", w.Code, w.Header().Get("HX-Redirect"), path)
		}
	} else if w.Code != http.StatusSeeOther || w.Header().Get("Location") != path {
		t.Errorf("response = %d, Location %q, want 303 and %q", w.Code, w.Header().Get("Location"), path)
	}
}

func TestHTTPRejectsCSRFOnEveryMutation(t *testing.T) {
	for _, path := range []string{"/cart", "/cart/renew", "/manager/baskets/practice", "/manager/baskets/missing/items", "/manager/baskets/missing/renew", "/checkout", "/manager/login", "/manager/logout", "/manager/inventory", "/manager/orders/1/advance", "/manager/orders/1/items/1", "/manager/catalog/products", "/manager/catalog/products/1", "/manager/catalog/products/1/archive", "/manager/catalog/products/1/restore", "/manager/catalog/categories", "/manager/catalog/categories/1", "/manager/catalog/categories/1/archive", "/manager/catalog/categories/1/restore", "/manager/catalog/types", "/manager/catalog/types/1", "/manager/catalog/types/1/archive", "/manager/catalog/types/1/restore"} {
		for _, tokenKind := range []string{"missing", "incorrect", "another session", "query string only"} {
			t.Run(path+"/"+tokenKind, func(t *testing.T) {
				s := newTestStore(t)
				a := testApp(t, s, testManagerPassword)
				session := testSession(t, s, "")
				if err := s.Manager(session.ID, true); err != nil {
					t.Fatal(err)
				}
				testCart(t, s, session.ID, 1, 1)
				session = testSession(t, s, session.ID)
				csrf := ""
				requestPath := path
				switch tokenKind {
				case "incorrect":
					csrf = "wrong-token"
				case "another session":
					csrf = testSession(t, s, "").CSRF
				case "query string only":
					requestPath += "?csrf=" + session.CSRF
				}
				values := url.Values{"csrf": {csrf}, "product_id": {"1"}, "quantity": {"2"}, "picked": {"1"}, "delta": {"1"}, "version": {"1"}, "reason": {"Restock"}, "status": {"Placed"}, "password": {testManagerPassword}, "checkout_key": {session.CheckoutKey}, "revision": {fmt.Sprint(session.Revision)}, "quote": {testBasket(t, s, session.ID).Quote}}
				w := testRequest(t, a, http.MethodPost, requestPath, session, values, nil)
				if w.Code != http.StatusForbidden {
					t.Fatalf("response = %d, want 403; body %s", w.Code, w.Body.String())
				}
				if after := testSession(t, s, session.ID); after != session {
					t.Error("CSRF-rejected request mutated session")
				}
				if b := testBasket(t, s, session.ID); b.Count != 1 {
					t.Error("CSRF-rejected request mutated basket")
				}
				if p := testProduct(t, s, 1); p.Stock != 23 || p.Version != 2 {
					t.Error("CSRF-rejected request mutated inventory")
				}
				if testCount(t, s, "orders") != 0 || testCount(t, s, "adjustments") != 0 {
					t.Error("CSRF-rejected request created data")
				}
			})
		}
	}
}

func TestHTTPCrossOriginAndHostRejected(t *testing.T) {
	for _, tc := range []struct {
		name, host, origin, fetch string
		status                    int
	}{
		{"unrecognized host", "attacker.test", testOrigin, "same-origin", 400},
		{"cross-origin", "127.0.0.1", "https://attacker.test", "same-origin", 403},
		{"null origin", "127.0.0.1", "null", "same-origin", 403},
		{"cross-site without origin", "127.0.0.1", "", "cross-site", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			a := testApp(t, s, testManagerPassword)
			session := testSession(t, s, "")
			values := url.Values{"csrf": {session.CSRF}, "product_id": {"1"}, "quantity": {"1"}}
			r := httptest.NewRequest(http.MethodPost, testOrigin+"/cart", strings.NewReader(values.Encode()))
			r.Host = tc.host
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.fetch)
			r.AddCookie(&http.Cookie{Name: "shop_session", Value: session.ID})
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Errorf("response %d, want %d", w.Code, tc.status)
			}
			if b := testBasket(t, s, session.ID); b.Count != 0 {
				t.Error("rejected request changed basket")
			}
		})
	}
}

func TestHTTPUnauthorizedManagerMutations(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("htmx_%t_expired_%t", htmx, expired), func(t *testing.T) {
				s := newTestStore(t)
				a := testApp(t, s, testManagerPassword)
				owner := testSession(t, s, "")
				testCart(t, s, owner.ID, 1, 1)
				id := testCheckout(t, s, owner.ID)
				unauthorized := testSession(t, s, "")
				if expired {
					testExec(t, s, "UPDATE sessions SET manager_until=? WHERE id=?", time.Now().Add(-time.Second).Unix(), unauthorized.ID)
					unauthorized = testSession(t, s, unauthorized.ID)
				}
				headers := map[string]string{}
				if htmx {
					headers["HX-Request"] = "true"
				}
				for _, path := range []string{"/manager/inventory", fmt.Sprintf("/manager/orders/%d/advance", id), fmt.Sprintf("/manager/orders/%d/items/1", id), "/manager/catalog/products", "/manager/catalog/products/1", "/manager/catalog/products/1/archive", "/manager/catalog/products/1/restore", "/manager/catalog/categories", "/manager/catalog/categories/1", "/manager/catalog/categories/1/archive", "/manager/catalog/categories/1/restore", "/manager/catalog/types", "/manager/catalog/types/1", "/manager/catalog/types/1/archive", "/manager/catalog/types/1/restore"} {
					w := testRequest(t, a, http.MethodPost, path, unauthorized, url.Values{"csrf": {unauthorized.CSRF}, "product_id": {"1"}, "version": {"2"}, "delta": {"1"}, "reason": {"Restock"}, "status": {"Placed"}}, headers)
					assertRedirect(t, w, "/manager/login", htmx)
				}
				if p := testProduct(t, s, 1); p.Stock != 23 || p.Version != 2 {
					t.Error("unauthorized manager request changed stock")
				}
				if order := testOrder(t, s, id, owner.ID); order.Status != "Placed" {
					t.Error("unauthorized manager request advanced order")
				}
				if testCount(t, s, "adjustments") != 0 {
					t.Error("unauthorized manager request created audit entry")
				}
			})
		}
	}
}

func TestHTTPOrderOwnership(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	owner := testSession(t, s, "")
	other := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	id := testCheckout(t, s, owner.ID)
	order := testOrder(t, s, id, owner.ID)
	path := fmt.Sprintf("/orders/%d", id)
	for _, htmx := range []bool{false, true} {
		headers := map[string]string{}
		if htmx {
			headers["HX-Request"] = "true"
		}
		w := testRequest(t, a, http.MethodGet, path, other, nil, headers)
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), order.Reference) {
			t.Errorf("other session received order details: status %d", w.Code)
		}
		w = testRequest(t, a, http.MethodGet, "/orders", other, nil, headers)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), order.Reference) {
			t.Errorf("other session's order list leaked order: status %d", w.Code)
		}
		w = testRequest(t, a, http.MethodGet, path, owner, nil, headers)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), order.Reference) {
			t.Errorf("owner could not read order: status %d", w.Code)
		}
	}
	if err := s.Manager(other.ID, true); err != nil {
		t.Fatal(err)
	}
	other = testSession(t, s, other.ID)
	if w := testRequest(t, a, http.MethodGet, path, other, nil, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), order.Reference) {
		t.Errorf("authorized manager could not read order: status %d", w.Code)
	}
}

func TestHTTPDisabledManagerAccess(t *testing.T) {
	t.Run("cannot sign in without configured password", func(t *testing.T) {
		s := newTestStore(t)
		a := testApp(t, s, "")
		session := testSession(t, s, "")
		for _, password := range []string{"", testManagerPassword} {
			w := testRequest(t, a, http.MethodPost, "/manager/login", session, url.Values{"csrf": {session.CSRF}, "password": {password}}, nil)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Manager access is disabled") {
				t.Errorf("disabled login response: status %d, body %s", w.Code, w.Body.String())
			}
			if current := testSession(t, s, session.ID); current.ManagerUntil != 0 {
				t.Error("disabled password granted manager access")
			}
		}
	})
	for _, action := range []string{"workspace", "inventory", "advance", "other shopper's order"} {
		t.Run("existing grant/"+action, func(t *testing.T) {
			s := newTestStore(t)
			enabled := testApp(t, s, testManagerPassword)
			manager := testSession(t, s, "")
			w := testRequest(t, enabled, http.MethodPost, "/manager/login", manager, url.Values{"csrf": {manager.CSRF}, "password": {testManagerPassword}}, nil)
			assertRedirect(t, w, "/manager", false)
			manager = testSession(t, s, manager.ID)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 1)
			id := testCheckout(t, s, owner.ID)
			// Simulate a restart against the same database with MANAGER_PASSWORD unset.
			disabled := testApp(t, s, "")
			switch action {
			case "workspace":
				w = testRequest(t, disabled, http.MethodGet, "/manager", manager, nil, nil)
				assertRedirect(t, w, "/manager/login", false)
			case "inventory":
				w = testRequest(t, disabled, http.MethodPost, "/manager/inventory", manager, url.Values{"csrf": {manager.CSRF}, "product_id": {"1"}, "delta": {"1"}, "version": {"2"}, "reason": {"Restock"}}, nil)
				assertRedirect(t, w, "/manager/login", false)
				if p := testProduct(t, s, 1); p.Stock != 23 || p.Version != 2 {
					t.Errorf("disabled manager access changed inventory: stock=%d, version=%d", p.Stock, p.Version)
				}
			case "advance":
				w = testRequest(t, disabled, http.MethodPost, fmt.Sprintf("/manager/orders/%d/advance", id), manager, url.Values{"csrf": {manager.CSRF}, "status": {"Placed"}}, nil)
				assertRedirect(t, w, "/manager/login", false)
				if status := testOrder(t, s, id, owner.ID).Status; status != "Placed" {
					t.Errorf("disabled manager access advanced order to %s", status)
				}
			case "other shopper's order":
				w = testRequest(t, disabled, http.MethodGet, fmt.Sprintf("/orders/%d", id), manager, nil, nil)
				if w.Code != http.StatusNotFound {
					t.Errorf("disabled manager could read another shopper's order: status %d, want 404", w.Code)
				}
			}
		})
	}
}

func TestHTTPManagerLoginLogoutRotatesCSRF(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	session := testSession(t, s, "")
	w := testRequest(t, a, http.MethodPost, "/manager/login", session, url.Values{"csrf": {session.CSRF}, "password": {"wrong-password"}}, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Could not sign in") {
		t.Errorf("wrong password response: status %d", w.Code)
	}
	if current := testSession(t, s, session.ID); current.ManagerUntil != 0 {
		t.Error("wrong password granted manager access")
	}
	w = testRequest(t, a, http.MethodPost, "/manager/login", session, url.Values{"csrf": {session.CSRF}, "password": {testManagerPassword}}, nil)
	assertRedirect(t, w, "/manager", false)
	manager := testSession(t, s, session.ID)
	if manager.ManagerUntil <= time.Now().Unix() || manager.CSRF == session.CSRF {
		t.Fatal("login did not establish manager access and rotate CSRF")
	}
	w = testRequest(t, a, http.MethodPost, "/manager/inventory", manager, url.Values{"csrf": {session.CSRF}, "product_id": {"1"}, "delta": {"1"}, "version": {"1"}, "reason": {"Restock"}}, nil)
	if w.Code != http.StatusForbidden {
		t.Error("pre-login CSRF token remained valid")
	}
	w = testRequest(t, a, http.MethodPost, "/manager/logout", manager, url.Values{"csrf": {manager.CSRF}}, nil)
	assertRedirect(t, w, "/", false)
	loggedOut := testSession(t, s, session.ID)
	if loggedOut.ManagerUntil != 0 || loggedOut.CSRF == manager.CSRF {
		t.Error("logout did not revoke manager access and rotate CSRF")
	}
	w = testRequest(t, a, http.MethodGet, "/manager", loggedOut, nil, nil)
	assertRedirect(t, w, "/manager/login", false)
}

func TestHTTPFormAndNumericValidation(t *testing.T) {
	for _, tc := range []struct{ name, pid, quantity string }{
		{"non-numeric ID", "nope", "1"}, {"overflow ID", "9223372036854775808", "1"},
		{"non-numeric quantity", "1", "nope"}, {"fraction", "1", "1.5"},
		{"negative quantity", "1", "-1"}, {"excess quantity", "1", "100"}, {"missing product", "9999", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			a := testApp(t, s, testManagerPassword)
			session := testSession(t, s, "")
			w := testRequest(t, a, http.MethodPost, "/cart", session, url.Values{"revision": {fmt.Sprint(session.Revision)}, "csrf": {session.CSRF}, "product_id": {tc.pid}, "quantity": {tc.quantity}}, map[string]string{"HX-Request": "true"})
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `role="alert"`) {
				t.Errorf("validation must render an HTMX error state: status %d", w.Code)
			}
			if b := testBasket(t, s, session.ID); b.Count != 0 {
				t.Error("invalid request changed basket")
			}
		})
	}
	for _, body := range []string{"csrf=%zz", "csrf=" + strings.Repeat("x", 8193)} {
		s := newTestStore(t)
		a := testApp(t, s, testManagerPassword)
		r := httptest.NewRequest(http.MethodPost, testOrigin+"/cart", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("malformed or oversized form: status %d, want 400", w.Code)
		}
	}
}

func TestHTTPSessionCookieHeadersAndHTMXRendering(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	a.config.SecureCookies = true
	w := testRequest(t, a, http.MethodGet, "/", Session{}, nil, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<!doctype html>") {
		t.Fatalf("full storefront: status %d", w.Code)
	}
	for header, want := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "same-origin", "X-Frame-Options": "DENY", "Vary": "HX-Request"} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "form-action 'self'") {
		t.Errorf("missing restrictive CSP: %q", csp)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "shop_session" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge != 86400 {
		t.Errorf("unsafe session cookie attributes: %+v", cookie)
	}
	session := testSession(t, s, cookie.Value)
	w = testRequest(t, a, http.MethodGet, "/", session, nil, map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "<!doctype html>") || !strings.Contains(w.Body.String(), `id="workspace"`) {
		t.Error("HTMX request did not return only workspace fragment")
	}
	if len(w.Result().Cookies()) != 0 {
		t.Error("existing session was needlessly replaced")
	}
}

func TestHTTPCheckoutStaleRevisionAndReplay(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprintf("htmx_%t", htmx), func(t *testing.T) {
			s := newTestStore(t)
			a := testApp(t, s, testManagerPassword)
			session := testSession(t, s, "")
			testCart(t, s, session.ID, 1, 1)
			headers := map[string]string{}
			if htmx {
				headers["HX-Request"] = "true"
			}
			values := url.Values{"csrf": {session.CSRF}, "checkout_key": {session.CheckoutKey}, "revision": {fmt.Sprint(session.Revision)}, "quote": {testBasket(t, s, session.ID).Quote}}
			w := testRequest(t, a, http.MethodPost, "/checkout", session, values, headers)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrConflict.Error()) {
				t.Fatalf("stale checkout did not render conflict: status %d", w.Code)
			}
			if testCount(t, s, "orders") != 0 || testProduct(t, s, 1).Stock != 23 {
				t.Error("stale HTTP checkout created an order or changed stock")
			}
			current := testSession(t, s, session.ID)
			values.Set("revision", fmt.Sprint(current.Revision))
			w = testRequest(t, a, http.MethodPost, "/checkout", current, values, headers)
			orders, err := s.Orders(session.ID, false)
			if err != nil || len(orders) != 1 {
				t.Fatalf("orders = %+v, %v, want one order", orders, err)
			}
			path := fmt.Sprintf("/orders/%d", orders[0].ID)
			assertRedirect(t, w, path, htmx)
			w = testRequest(t, a, http.MethodPost, "/checkout", current, values, headers)
			assertRedirect(t, w, path, htmx)
			if testCount(t, s, "orders") != 1 || testProduct(t, s, 1).Stock != 23 {
				t.Error("replayed HTTP checkout duplicated order or stock deduction")
			}
		})
	}
}

func TestPublicManagerRequiresTLSAndStrongPassword(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name, origin, password string
		wantError              bool
	}{
		{"HTTP storefront without manager", "http://market.example", "", false},
		{"HTTP manager refused", "http://market.example", "this-is-a-test-only-long-password", true},
		{"HTTPS manager weak password refused", "https://market.example", "local-demo-only", true},
		{"HTTPS manager strong password", "https://market.example", "this-is-a-test-only-long-password", false},
		{"loopback development allowed", "http://127.0.0.1:8090", "local-demo-only", false},
		{"localhost development allowed", "http://localhost:8090", "local-demo-only", false},
		{"IPv6 loopback development allowed", "http://[::1]:8090", "local-demo-only", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := New(s, Config{Origin: tc.origin, ManagerPassword: tc.password})
			if (err != nil) != tc.wantError {
				t.Fatalf("New error=%v, wantError=%v", err, tc.wantError)
			}
			if err == nil && strings.HasPrefix(tc.origin, "https:") && !a.config.SecureCookies {
				t.Error("HTTPS origin must force Secure cookies")
			}
		})
	}
}

func TestWeakPasswordRequiresExplicitDemoMode(t *testing.T) {
	s := newTestStore(t)
	for _, tc := range []struct {
		name, origin, password string
		demo, wantError        bool
	}{
		{"normal HTTPS rejects password", "https://market.example", "password", false, true},
		{"demo HTTPS permits password", "https://market.example", "password", true, false},
		{"demo public HTTP still rejected", "http://market.example", "password", true, true},
		{"normal local rejects password", "http://127.0.0.1", "password", false, true},
		{"demo local permits password", "http://127.0.0.1", "password", true, false},
		{"demo without password remains disabled", "https://market.example", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := New(s, Config{Origin: tc.origin, ManagerPassword: tc.password, DemoMode: tc.demo})
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%v", err, tc.wantError)
			}
			if err != nil {
				return
			}
			session := testSession(t, s, "")
			r := httptest.NewRequest(http.MethodGet, tc.origin+"/", nil)
			r.AddCookie(&http.Cookie{Name: "shop_session", Value: session.ID})
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			if tc.demo && !strings.Contains(w.Body.String(), "Fake catalog and inventory are shared by all visitors.") {
				t.Error("shared demo warning missing")
			}
			if strings.HasPrefix(tc.origin, "https:") && !a.config.SecureCookies {
				t.Error("demo mode weakened Secure cookies")
			}
			if tc.password == "" && a.config.ManagerPassword != "" {
				t.Error("empty demo password unexpectedly enabled management")
			}
		})
	}
}

func TestDemoLoginStillRequiresPasswordAndCSRF(t *testing.T) {
	s := newTestStore(t)
	a, err := New(s, Config{Origin: testOrigin, ManagerPassword: "password", DemoMode: true})
	if err != nil {
		t.Fatal(err)
	}
	session := testSession(t, s, "")
	w := testRequest(t, a, http.MethodPost, "/manager/login", session, url.Values{"password": {"password"}}, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", w.Code)
	}
	w = testRequest(t, a, http.MethodPost, "/manager/login", session, url.Values{"csrf": {session.CSRF}, "password": {"wrong"}}, nil)
	if testSession(t, s, session.ID).ManagerUntil != 0 {
		t.Fatal("wrong password granted manager access")
	}
	w = testRequest(t, a, http.MethodPost, "/manager/login", session, url.Values{"csrf": {session.CSRF}, "password": {"password"}}, nil)
	assertRedirect(t, w, "/manager", false)
	if testSession(t, s, session.ID).ManagerUntil <= time.Now().Unix() {
		t.Fatal("explicit demo password did not authenticate")
	}
}

func TestCatalogSingularResult(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, "")
	w := testRequest(t, a, http.MethodGet, "/?q=Honeycrisp", Session{}, nil, nil)
	if !strings.Contains(w.Body.String(), "1 product /") || strings.Contains(w.Body.String(), "1 products") {
		t.Fatal("single result must use singular product")
	}
}
