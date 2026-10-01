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

// Small semantic HTML helpers keep these assertions independent of CSS classes
// and attribute ordering. They inspect only generated, well-formed test output.
var adminTagRE = regexp.MustCompile(`(?s)<[^>]*>`)
var adminAnchorRE = regexp.MustCompile(`(?s)<a\b([^>]*)>(.*?)</a>`)
var adminFormRE = regexp.MustCompile(`(?s)<form\b([^>]*)>(.*?)</form>`)
var adminInputRE = regexp.MustCompile(`(?s)<input\b([^>]*)>`)

func adminAttr(attributes, key string) string {
	re := regexp.MustCompile(`(?:^|\s)` + regexp.QuoteMeta(key) + `="([^"]*)"`)
	m := re.FindStringSubmatch(attributes)
	if len(m) == 0 {
		return ""
	}
	return html.UnescapeString(m[1])
}
func adminVisibleText(markup string) string {
	return strings.Join(strings.Fields(html.UnescapeString(adminTagRE.ReplaceAllString(markup, " "))), " ")
}
func adminForm(markup, action string) string {
	for _, m := range adminFormRE.FindAllStringSubmatch(markup, -1) {
		target, err := url.Parse(adminAttr(m[1], "action"))
		if err == nil && target.Path == action {
			return m[0]
		}
	}
	return ""
}
func adminInput(markup, name string) (string, string) {
	for _, m := range adminInputRE.FindAllStringSubmatch(markup, -1) {
		if adminAttr(m[1], "name") == name {
			return adminAttr(m[1], "value"), m[1]
		}
	}
	return "", ""
}
func adminFindLink(markup string, want func(*url.URL) bool) bool {
	for _, m := range adminAnchorRE.FindAllStringSubmatch(markup, -1) {
		u, err := url.Parse(adminAttr(m[1], "href"))
		if err == nil && want(u) {
			return true
		}
	}
	return false
}
func assertAdminNavigation(t *testing.T, body, current string, csrf string) {
	t.Helper()
	navRE := regexp.MustCompile(`(?s)<nav\b([^>]*)>(.*?)</nav>`)
	var navigation string
	for _, n := range navRE.FindAllStringSubmatch(body, -1) {
		if adminFindLink(n[2], func(u *url.URL) bool { return u.Path == "/manager/stock" }) {
			if adminAttr(n[1], "aria-label") == "" {
				t.Error("manager navigation lacks an accessible name")
			}
			navigation = n[2]
			break
		}
	}
	if navigation == "" {
		t.Fatal("manager page lacks persistent semantic navigation with Stock link")
	}
	want := map[string]string{"orders": "Orders", "baskets": "Baskets", "products": "Products", "labels": "Categories & types", "stock": "Stock"}
	found := map[string]bool{}
	active := 0
	for _, m := range adminAnchorRE.FindAllStringSubmatch(navigation, -1) {
		u, err := url.Parse(adminAttr(m[1], "href"))
		if err != nil {
			continue
		}
		section := ""
		switch u.Path {
		case "/manager", "/manager/orders":
			section = "orders"
		case "/manager/baskets":
			section = "baskets"
		case "/manager/stock":
			section = "stock"
		case "/manager/catalog":
			if u.Query().Get("tab") == "labels" {
				section = "labels"
			} else if u.Query().Get("tab") == "" && u.Query().Get("edit") == "" {
				section = "products"
			}
		}
		if label, ok := want[section]; ok {
			if !strings.Contains(strings.ToLower(adminVisibleText(m[2])), strings.ToLower(label)) {
				t.Errorf("manager %s link is not clearly labeled %q: %q", section, label, adminVisibleText(m[2]))
			}
			found[section] = true
			if adminAttr(m[1], "aria-current") == "page" {
				active++
				if section != current {
					t.Errorf("%s is marked current, want %s", section, current)
				}
			}
		}
	}
	for section := range want {
		if !found[section] {
			t.Errorf("manager nav missing %s", section)
		}
	}
	if active != 1 {
		t.Errorf("manager navigation has %d current sections, want one", active)
	}
	logout := adminForm(body, "/manager/logout")
	if logout == "" {
		t.Error("manager logout form missing")
	} else if got, _ := adminInput(logout, "csrf"); got != csrf {
		t.Error("manager logout form lacks this session's CSRF")
	}
}

func TestAdminNavigationPersistsAcrossSectionsAndDemo(t *testing.T) {
	for _, demo := range []bool{false, true} {
		for _, htmx := range []bool{false, true} {
			t.Run(fmt.Sprintf("demo_%t_htmx_%t", demo, htmx), func(t *testing.T) {
				s := newTestStore(t)
				a := reservationHTTPApp(t, s, demo)
				manager := testManager(t, s)
				testCart(t, s, manager.ID, 1, 1)
				basket := testBasket(t, s, manager.ID)
				order := reservationCheckout(t, s, manager.ID, "")
				testCart(t, s, manager.ID, 1, 1)
				headers := map[string]string{}
				if htmx {
					headers["HX-Request"] = "true"
				}
				for _, tc := range []struct{ path, current string }{{"/manager", "orders"}, {"/manager/orders", "orders"}, {fmt.Sprintf("/manager/orders/%d", order), "orders"}, {"/manager/baskets", "baskets"}, {"/manager/baskets/" + basket.ID, "baskets"}, {"/manager/catalog", "products"}, {"/manager/catalog?tab=new", "products"}, {"/manager/catalog?edit=1", "products"}, {"/manager/catalog?tab=labels", "labels"}, {"/manager/stock", "stock"}} {
					t.Run(tc.path, func(t *testing.T) {
						page := testRequest(t, a, http.MethodGet, tc.path, manager, nil, headers)
						if page.Code != http.StatusOK {
							t.Fatalf("manager page=%d: %s", page.Code, page.Body.String())
						}
						assertAdminNavigation(t, page.Body.String(), tc.current, manager.CSRF)
						if htmx && strings.Contains(page.Body.String(), "<!doctype html>") {
							t.Error("HTMX navigation returned an entire document")
						}
					})
				}
			})
		}
	}
}

func TestAdminOrdersDefaultAndSearchKeepDemoScope(t *testing.T) {
	s := newTestStore(t)
	a := reservationHTTPApp(t, s, true)
	manager := testManager(t, s)
	other := testSession(t, s, "")
	testCart(t, s, manager.ID, 1, 1)
	first := reservationCheckout(t, s, manager.ID, "")
	testCart(t, s, manager.ID, 2, 1)
	second := reservationCheckout(t, s, manager.ID, "")
	testCart(t, s, other.ID, 1, 1)
	foreign := reservationCheckout(t, s, other.ID, "")
	testExec(t, s, `UPDATE orders SET reference='DEMO-ALPHA-ONLY' WHERE id=?`, first)
	testExec(t, s, `UPDATE orders SET reference='DEMO-BRAVO-ONLY' WHERE id=?`, second)
	testExec(t, s, `UPDATE orders SET reference='DEMO-ALPHA-PRIVATE' WHERE id=?`, foreign)
	page := testRequest(t, a, http.MethodGet, "/manager", manager, nil, nil)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, "DEMO-ALPHA-ONLY") || !strings.Contains(body, "DEMO-BRAVO-ONLY") {
		t.Errorf("default manager page is not the order queue: %d", page.Code)
	}
	if adminForm(body, "/manager/inventory") != "" {
		t.Error("default order page still embeds the stock workspace")
	}
	if strings.Contains(body, "DEMO-ALPHA-PRIVATE") {
		t.Error("default demo queue exposed another customer's order")
	}
	page = testRequest(t, a, http.MethodGet, "/manager/orders?q=ALPHA", manager, nil, nil)
	body = page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, "DEMO-ALPHA-ONLY") || strings.Contains(body, "DEMO-BRAVO-ONLY") || strings.Contains(body, "DEMO-ALPHA-PRIVATE") {
		t.Errorf("order search is wrong or bypasses ownership: %d %s", page.Code, body)
	}
	if got, _ := adminInput(body, "q"); got != "ALPHA" {
		t.Errorf("order search term not retained: %q", got)
	}
	search := `<script>alert(1)</script>`
	page = testRequest(t, a, http.MethodGet, "/manager/orders?q="+url.QueryEscape(search), manager, nil, nil)
	if strings.Contains(page.Body.String(), search) {
		t.Error("order query reflected executable markup")
	}
}

func TestAdminStockIsSeparateSearchableAndKeepsAdjustmentContext(t *testing.T) {
	s := newTestStore(t)
	a := reservationHTTPApp(t, s, false)
	manager := testManager(t, s)
	query := "Honeycrisp"
	path := "/manager/stock?q=" + url.QueryEscape(query)
	page := testRequest(t, a, http.MethodGet, path, manager, nil, nil)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, "Honeycrisp apples") || strings.Contains(body, "Baby spinach") {
		t.Errorf("stock search did not filter products: %d %s", page.Code, body)
	}
	if adminForm(body, "/manager/inventory") == "" {
		t.Fatal("stock screen lacks a stock adjustment form")
	}
	if got, _ := adminInput(body, "q"); got != query {
		t.Errorf("stock query=%q", got)
	}
	p := testProduct(t, s, 1)
	values := url.Values{"csrf": {manager.CSRF}, "product_id": {"1"}, "version": {fmt.Sprint(p.Version)}, "delta": {"1"}, "reason": {"Counted fresh delivery"}, "q": {query}}
	assertRedirect(t, testRequest(t, a, http.MethodPost, "/manager/inventory", manager, values, nil), path, false)
	if got := testProduct(t, s, 1); got.Stock != p.Stock+1 {
		t.Error("stock action failed to persist")
	}
}

func TestAdminCatalogUsesDedicatedProductAndTaxonomyScreens(t *testing.T) {
	s := newTestStore(t)
	a := reservationHTTPApp(t, s, false)
	manager := testManager(t, s)
	for _, tc := range []struct{ path, expected string }{{"/manager/catalog", "list"}, {"/manager/catalog?tab=new", "new"}, {"/manager/catalog?edit=1", "edit"}, {"/manager/catalog?tab=labels", "labels"}} {
		t.Run(tc.expected, func(t *testing.T) {
			page := testRequest(t, a, http.MethodGet, tc.path, manager, nil, nil)
			if page.Code != http.StatusOK {
				t.Fatalf("catalog screen=%d", page.Code)
			}
			body := page.Body.String()
			create := adminForm(body, "/manager/catalog/products")
			edit := adminForm(body, "/manager/catalog/products/1")
			categories := adminForm(body, "/manager/catalog/categories")
			types := adminForm(body, "/manager/catalog/types")
			switch tc.expected {
			case "list":
				if create != "" || edit != "" || categories != "" || types != "" {
					t.Error("product list embeds create/edit/taxonomy forms")
				}
				if !strings.Contains(body, "Honeycrisp apples") {
					t.Error("product list missing products")
				}
				if !adminFindLink(body, func(u *url.URL) bool { return u.Path == "/manager/catalog" && u.Query().Get("edit") == "1" }) {
					t.Error("product list lacks link to dedicated editor")
				}
			case "new":
				if create == "" || edit != "" || categories != "" || types != "" {
					t.Error("add product is not a dedicated create screen")
				}
			case "edit":
				if edit == "" || create != "" || categories != "" || types != "" || adminForm(body, "/manager/catalog/products/2") != "" {
					t.Error("edit product is not a dedicated single-product screen")
				}
			case "labels":
				if categories == "" || types == "" || create != "" || edit != "" {
					t.Error("taxonomy page missing labels or includes product forms")
				}
			}
		})
	}
	for _, bad := range []string{"0", "-1", "not-a-product", "9223372036854775808", "999999"} {
		if page := testRequest(t, a, http.MethodGet, "/manager/catalog?edit="+bad, manager, nil, nil); page.Code != http.StatusNotFound {
			t.Errorf("invalid editor identity %q returned %d", bad, page.Code)
		}
	}
}

func TestAdminCatalogEditPreservesSearchAcrossBackAndSave(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s := newTestStore(t)
			a := reservationHTTPApp(t, s, false)
			manager := testManager(t, s)
			query := "Honeycrisp"
			headers := map[string]string{}
			if htmx {
				headers["HX-Request"] = "true"
			}
			page := testRequest(t, a, http.MethodGet, "/manager/catalog?q="+url.QueryEscape(query), manager, nil, headers)
			body := page.Body.String()
			if !adminFindLink(body, func(u *url.URL) bool {
				return u.Path == "/manager/catalog" && u.Query().Get("edit") == "1" && u.Query().Get("q") == query
			}) {
				t.Error("product edit link discarded current search")
			}
			page = testRequest(t, a, http.MethodGet, "/manager/catalog?edit=1&q="+url.QueryEscape(query), manager, nil, headers)
			body = page.Body.String()
			if !adminFindLink(body, func(u *url.URL) bool {
				return u.Path == "/manager/catalog" && u.Query().Get("edit") == "" && u.Query().Get("tab") == "" && u.Query().Get("q") == query
			}) {
				t.Error("editor back link discarded current search")
			}
			form := adminForm(body, "/manager/catalog/products/1")
			if got, _ := adminInput(form, "q"); got != query {
				t.Errorf("editor POST query=%q, want %q", got, query)
			}
			p := testProduct(t, s, 1)
			p.Name = "Honeycrisp apples updated"
			values := catalogProductForm(manager, p)
			values.Set("q", query)
			w := testRequest(t, a, http.MethodPost, "/manager/catalog/products/1", manager, values, headers)
			var location string
			if htmx {
				if w.Code != http.StatusOK {
					t.Fatalf("HTMX save=%d", w.Code)
				}
				location = w.Header().Get("HX-Push-Url")
			} else {
				if w.Code != http.StatusSeeOther {
					t.Fatalf("save=%d", w.Code)
				}
				location = w.Header().Get("Location")
			}
			destination, err := url.Parse(location)
			if err != nil || destination.Path != "/manager/catalog" || destination.Query().Get("edit") != "1" || destination.Query().Get("q") != query {
				t.Errorf("save lost editor/search context: %q", location)
			}
			if got := testProduct(t, s, 1); got.Name != p.Name {
				t.Error("editor save did not persist")
			}
		})
	}
}

func TestAdminCatalogInvalidDraftRetainsEditableValuesAndRealIdentity(t *testing.T) {
	s := newTestStore(t)
	a := reservationHTTPApp(t, s, false)
	manager := testManager(t, s)
	p := testProduct(t, s, 1)
	values := catalogProductForm(manager, p)
	draftName := `Draft <strong>apples</strong>`
	draftDescription := `Keep this description & "details"`
	values.Set("name", draftName)
	values.Set("description", draftDescription)
	values.Set("price", "0")
	values.Set("sku", "FORGED-IMMUTABLE-SKU")
	values.Set("q", "Honeycrisp")
	before := reservationSnapshot(t, s)
	w := testRequest(t, a, http.MethodPost, "/manager/catalog/products/1", manager, values, map[string]string{"HX-Request": "true"})
	body := w.Body.String()
	form := adminForm(body, "/manager/catalog/products/1")
	if w.Code != http.StatusOK || !strings.Contains(body, ErrInvalid.Error()) || form == "" {
		t.Fatalf("invalid edit did not return visible dedicated draft: %d %s", w.Code, body)
	}
	if got, _ := adminInput(form, "name"); got != draftName {
		t.Errorf("invalid draft lost name: %q", got)
	}
	if !strings.Contains(form, html.EscapeString(draftDescription)) || strings.Contains(form, draftName) {
		t.Error("description/name draft lost or rendered as markup")
	}
	if got, attributes := adminInput(form, "sku"); got != p.SKU || !strings.Contains(attributes, "readonly") {
		t.Errorf("invalid draft replaced immutable SKU: %q %s", got, attributes)
	}
	if strings.Contains(body, "FORGED-IMMUTABLE-SKU") {
		t.Error("forged SKU shown as authoritative identity")
	}
	if got, _ := adminInput(form, "catalog_version"); got != fmt.Sprint(p.CatalogVersion) {
		t.Errorf("invalid draft changed optimistic revision: %q", got)
	}
	if got, _ := adminInput(form, "q"); got != "Honeycrisp" {
		t.Errorf("invalid draft lost search: %q", got)
	}
	assertReservationUnchanged(t, s, before)
}

func TestAdminDemoPasswordHintIsExplicitlyScoped(t *testing.T) {
	const hint = "Demo manager password: password"
	for _, tc := range []struct {
		name, password string
		demo, show     bool
	}{{"public-demo-default", "password", true, true}, {"public-demo-custom", "a-private-demo-secret", true, false}, {"public-demo-disabled", "", true, false}, {"local-gate", testManagerPassword, false, false}, {"local-gate-disabled", "", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			a, err := New(s, Config{Origin: testOrigin, ManagerPassword: tc.password, DemoMode: tc.demo})
			if err != nil {
				t.Fatal(err)
			}
			visitor := testSession(t, s, "")
			page := testRequest(t, a, http.MethodGet, "/manager/login", visitor, nil, nil)
			if page.Code != http.StatusOK {
				t.Fatalf("login page=%d", page.Code)
			}
			body := page.Body.String()
			text := adminVisibleText(body)
			if strings.Contains(text, hint) != tc.show {
				t.Errorf("password hint visibility=%t, want %t: %s", strings.Contains(text, hint), tc.show, text)
			}
			if !tc.show && tc.password != "" && strings.Contains(body, tc.password) {
				t.Error("login page echoed a custom or non-demo credential")
			}
		})
	}
}
