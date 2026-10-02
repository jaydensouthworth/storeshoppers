package shop

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func catalogFilterFixture() []Product {
	return []Product{
		{ID: 1, Name: "Apple", SKU: "B-01", Barcode: "001234567890", CategoryID: 1, Category: "Produce", TypeID: 1, ProductType: "Organic", Stock: 9, Price: 300, SaleUnit: "each"},
		{ID: 2, Name: "Pear", SKU: "A-02", CategoryID: 2, Category: "Pantry", TypeID: 2, ProductType: "Seasonal", Stock: 0, Price: 100, SaleUnit: "each"},
		{ID: 3, Name: "Banana", SKU: "C-03", CategoryID: 1, Category: "Produce", Stock: 4, Price: 200, SaleUnit: "each"},
		{ID: 4, Name: "Cherry", SKU: "D-04", CategoryID: 1, Category: "Produce", TypeID: 1, ProductType: "Organic", Stock: 1, Price: 500, SaleUnit: "each", Archived: true},
		{ID: 5, Name: "Dates", SKU: "F-05", CategoryID: 2, Category: "Pantry", TypeID: 2, ProductType: "Seasonal", Stock: 7, Price: 150, SaleUnit: "g"},
		{ID: 6, Name: "apple", SKU: "E-06", CategoryID: 2, Category: "Pantry", TypeID: 1, ProductType: "Organic", Stock: 8, Price: 300, SaleUnit: "each"},
	}
}
func catalogProductIDs(products []Product) []int64 {
	ids := []int64{}
	for _, p := range products {
		ids = append(ids, p.ID)
	}
	return ids
}
func TestCatalogFiltersIndependentlyAndCombined(t *testing.T) {
	products := catalogFilterFixture()
	before := append([]Product(nil), products...)
	for _, tc := range []struct {
		query string
		want  []int64
	}{
		{"", []int64{1, 6, 3, 5, 2}},
		{"q=%20aPpLe%20", []int64{1, 6}},
		{"q=b-01", []int64{1}},
		{"q=001234567890", []int64{1}},
		{"q=organic", []int64{1, 6}},
		{"q=produce", []int64{1, 3}},
		{"q=stored-code", []int64{3}},
		{"category=1", []int64{1, 3}},
		{"type=1", []int64{1, 6}},
		{"type=none", []int64{3}},
		{"availability=in-stock", []int64{1, 6, 3, 5}},
		{"availability=out-of-stock", []int64{2}},
		{"availability=low-stock", []int64{3}},
		{"status=archived", []int64{4}},
		{"status=all", []int64{1, 6, 3, 4, 5, 2}},
		{"status=all&availability=low-stock", []int64{3, 4}},
		{"category=1&type=1&availability=in-stock&status=all&q=organic&sort=newest", []int64{4, 1}},
		{"category=1&type=2", []int64{}},
		{"category=999999", []int64{}},
		{"q=%25", []int64{}},
		{"q=%27%20OR%201%3D1--", []int64{}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			f := catalogFilters(httptest.NewRequest(http.MethodGet, "/manager/catalog?"+tc.query, nil))
			codes := map[int64]bool{}
			if f.Query == "stored-code" {
				codes[3] = true
			}
			if got := catalogProductIDs(filterCatalogProducts(products, f, codes)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ids=%v want %v", got, tc.want)
			}
		})
	}
	if !reflect.DeepEqual(products, before) {
		t.Error("filter changed source products")
	}
}
func TestCatalogSortWhitelistAndStableTies(t *testing.T) {
	for _, tc := range []struct {
		sort string
		want []int64
	}{
		{"name", []int64{1, 6, 3, 5, 2}},
		{"name-desc", []int64{2, 5, 3, 1, 6}},
		{"sku", []int64{2, 1, 3, 6, 5}},
		{"newest", []int64{6, 5, 3, 2, 1}},
		{"price-asc", []int64{2, 5, 3, 1, 6}},
		{"price-desc", []int64{1, 6, 3, 5, 2}},
		{"price DESC; DROP TABLE products", []int64{1, 6, 3, 5, 2}},
	} {
		t.Run(tc.sort, func(t *testing.T) {
			f := catalogFilters(httptest.NewRequest(http.MethodGet, "/manager/catalog?sort="+url.QueryEscape(tc.sort), nil))
			if got := catalogProductIDs(filterCatalogProducts(catalogFilterFixture(), f, nil)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ids=%v want %v", got, tc.want)
			}
		})
	}
}
func TestCatalogContextCanonicalAndSafe(t *testing.T) {
	values := url.Values{"q": {"  A & B / <fruit>  "}, "category": {"0007"}, "type": {"0003"}, "availability": {"low-stock"}, "status": {"all"}, "sort": {"price-desc"}, "return": {"https://evil.invalid"}, "edit": {"https://evil.invalid"}, "tab": {"labels"}}
	f := catalogFilters(httptest.NewRequest(http.MethodGet, "/manager/catalog?"+values.Encode(), nil))
	want := url.Values{"q": {"A & B / <fruit>"}, "category": {"7"}, "type": {"3"}, "availability": {"low-stock"}, "status": {"all"}, "sort": {"price-desc"}}
	if !reflect.DeepEqual(f.values(), want) {
		t.Fatalf("context=%v want %v", f.values(), want)
	}
	for _, path := range []string{f.ListURL(), f.NewURL(), f.EditURL(4)} {
		u, err := url.Parse(path)
		if err != nil || u.Host != "" || u.Path != "/manager/catalog" || u.Query().Get("return") != "" {
			t.Errorf("unsafe destination %q", path)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/manager/catalog/products/4?q=ignored&sort=newest", nil)
	r.PostForm = values
	r.SetPathValue("id", "4")
	if got := catalogDestination(r); got != f.EditURL(4) {
		t.Errorf("POST lost context: %s", got)
	}
	bad := catalogFilters(httptest.NewRequest(http.MethodGet, "/manager/catalog?category=-1&type=999999999999999999999&availability=unknown&status=deleted&sort=hack&return=//evil.invalid", nil))
	if bad.ListURL() != "/manager/catalog" || bad.Status != "active" || bad.Sort != "name" {
		t.Errorf("invalid context not defaulted: %+v", bad)
	}
	long := catalogFilters(httptest.NewRequest(http.MethodGet, "/manager/catalog?q="+strings.Repeat("a", 300), nil))
	if len(long.Query) != 100 {
		t.Errorf("query not bounded: %d", len(long.Query))
	}
}

var catalogRowRE = regexp.MustCompile("<article class=\"compact-product[^\"]*\" id=\"catalog-product-(\\d+)\"")

func catalogHTTPIDs(body string) []string {
	ids := []string{}
	for _, match := range catalogRowRE.FindAllStringSubmatch(body, -1) {
		ids = append(ids, match[1])
	}
	return ids
}
func TestHTTPCatalogStoredIdentifiersAndArchiveStates(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	manager := testManager(t, s)
	p := testProduct(t, s, 1)
	testExec(t, s, "INSERT INTO product_codes(product_id,scheme,raw_value,normalized_value,symbology) VALUES(1,'legacy_placeholder','Additional raw code','NORMALIZED-ALIAS-ONE','Code128')")
	var local string
	if err := s.db.QueryRow("SELECT normalized_value FROM product_codes WHERE product_id=1 AND scheme='demo_local'").Scan(&local); err != nil {
		t.Fatal(err)
	}
	for _, htmx := range []bool{false, true} {
		for _, query := range []string{p.SKU, p.Barcode, strings.ToLower(local), "Additional raw code", "normalized-alias-one"} {
			t.Run(fmt.Sprintf("%t_%s", htmx, query), func(t *testing.T) {
				headers := map[string]string{}
				if htmx {
					headers["HX-Request"] = "true"
				}
				page := testRequest(t, a, http.MethodGet, "/manager/catalog?q="+url.QueryEscape(query), manager, nil, headers)
				if page.Code != http.StatusOK || !reflect.DeepEqual(catalogHTTPIDs(page.Body.String()), []string{"1"}) {
					t.Fatalf("query %q code %d rows %v", query, page.Code, catalogHTTPIDs(page.Body.String()))
				}
				if !strings.Contains(page.Body.String(), "1 result</strong>") || htmx == strings.Contains(page.Body.String(), "<!doctype html>") {
					t.Error("wrong result count or fragment/document")
				}
			})
		}
	}
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"active", "archived", "all"} {
		page := testRequest(t, a, http.MethodGet, "/manager/catalog?q="+url.QueryEscape(local)+"&status="+status, manager, nil, nil)
		want := []string{"1"}
		if status == "active" {
			want = []string{}
		}
		if got := catalogHTTPIDs(page.Body.String()); !reflect.DeepEqual(got, want) {
			t.Errorf("status %s rows %v want %v", status, got, want)
		}
	}
}
func assertCatalogContext(t *testing.T, body string, f CatalogFilters, actions ...string) {
	t.Helper()
	if !adminFindLink(body, func(u *url.URL) bool { return u.String() == f.ListURL() }) {
		t.Errorf("back/cancel lost context %s", f.ListURL())
	}
	for _, action := range actions {
		form := adminForm(body, action)
		if form == "" {
			t.Fatalf("missing form %s", action)
		}
		for _, field := range f.Fields() {
			if got, _ := adminInput(form, field.Name); got != field.Value {
				t.Errorf("%s lost %s: %q want %q", action, field.Name, got, field.Value)
			}
		}
	}
}
func withCatalogContext(values url.Values, f CatalogFilters) url.Values {
	for _, field := range f.Fields() {
		values.Set(field.Name, field.Value)
	}
	values.Set("return", "https://evil.invalid")
	return values
}
func TestHTTPCatalogFullContextAcrossWritesAndErrors(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s := newTestStore(t)
			a := testApp(t, s, testManagerPassword)
			manager := testManager(t, s)
			p := testProduct(t, s, 1)
			f := CatalogFilters{Query: "Honeycrisp", CategoryID: p.CategoryID, Type: "none", Availability: "in-stock", Status: "all", Sort: "price-desc"}
			headers := map[string]string{}
			if htmx {
				headers["HX-Request"] = "true"
			}
			list := testRequest(t, a, http.MethodGet, f.ListURL(), manager, nil, headers).Body.String()
			for _, target := range []string{f.EditURL(1), f.NewURL()} {
				if !adminFindLink(list, func(u *url.URL) bool { return u.String() == target }) {
					t.Errorf("list lost link %s", target)
				}
			}
			f.Page = 3 // Editors preserve return-page context even if the product no longer matches.
			page := testRequest(t, a, http.MethodGet, f.EditURL(1), manager, nil, headers)
			assertCatalogContext(t, page.Body.String(), f, "/manager/catalog/products/1", "/manager/catalog/products/1/archive")
			page = testRequest(t, a, http.MethodGet, f.NewURL(), manager, nil, headers)
			assertCatalogContext(t, page.Body.String(), f, "/manager/catalog/products")
			for _, invalid := range []string{"price", "catalog_version"} {
				values := withCatalogContext(catalogProductForm(manager, p), f)
				expected := ErrInvalid.Error()
				values.Set("price", "0")
				if invalid == "catalog_version" {
					values.Set("price", fmt.Sprint(p.Price))
					values.Set("catalog_version", "9999")
					expected = ErrConflict.Error()
				}
				response := testRequest(t, a, http.MethodPost, "/manager/catalog/products/1", manager, values, headers)
				assertCatalogContext(t, response.Body.String(), f, "/manager/catalog/products/1")
				if !strings.Contains(response.Body.String(), expected) || testProduct(t, s, 1) != p {
					t.Error("invalid/stale writes changed data or hid error")
				}
			}
			for _, action := range []string{"save", "archive", "restore"} {
				current := testCatalogProduct(t, s, 1)
				path := "/manager/catalog/products/1"
				if action != "save" {
					path += "/" + action
				}
				response := testRequest(t, a, http.MethodPost, path, manager, withCatalogContext(catalogProductForm(manager, current), f), headers)
				location := response.Header().Get("Location")
				if htmx {
					location = response.Header().Get("HX-Push-Url")
					if response.Code != http.StatusOK {
						t.Fatalf("HTMX %s code %d", action, response.Code)
					}
				} else {
					if response.Code != http.StatusSeeOther {
						t.Fatalf("HTML %s code %d", action, response.Code)
					}
					response = testRequest(t, a, http.MethodGet, location, manager, nil, nil)
				}
				if location != f.EditURL(1) {
					t.Errorf("%s lost context: %s", action, location)
				}
				form := "/manager/catalog/products/1"
				if action == "archive" {
					form += "/restore"
				}
				assertCatalogContext(t, response.Body.String(), f, form)
			}
			fresh := newCatalogProduct()
			fresh.Name = "Context-created product"
			fresh.CategoryID = p.CategoryID
			response := testRequest(t, a, http.MethodPost, "/manager/catalog/products", manager, withCatalogContext(catalogProductForm(manager, fresh), f), headers)
			location := response.Header().Get("Location")
			if htmx {
				location = response.Header().Get("HX-Push-Url")
			}
			if location != f.EditURL(73) {
				t.Errorf("create lost context: %s", location)
			}
			f.Query = "no longer matches <anything>"
			page = testRequest(t, a, http.MethodGet, f.EditURL(1), manager, nil, headers)
			assertCatalogContext(t, page.Body.String(), f, "/manager/catalog/products/1")
		})
	}
}
func TestHTTPCatalogRefinementMarkupAndGuards(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	manager := testManager(t, s)
	visitor := testSession(t, s, "")
	for _, htmx := range []bool{false, true} {
		headers := map[string]string{}
		if htmx {
			headers["HX-Request"] = "true"
		}
		for _, suffix := range []string{"?status=archived&availability=low-stock", "?tab=new&status=all", "?edit=1&type=none"} {
			assertRedirect(t, testRequest(t, a, http.MethodGet, "/manager/catalog"+suffix, visitor, nil, headers), "/manager/login", htmx)
		}
		form := catalogProductForm(manager, testProduct(t, s, 1))
		form.Set("csrf", "wrong")
		if got := testRequest(t, a, http.MethodPost, "/manager/catalog/products/1", manager, form, headers); got.Code != http.StatusForbidden {
			t.Errorf("CSRF guard weakened: %d", got.Code)
		}
	}
	body := testRequest(t, a, http.MethodGet, "/manager/catalog?category=1&type=none&availability=in-stock&status=all&sort=newest", manager, nil, nil).Body.String()
	assertAdminNavigation(t, body, "products", manager.CSRF)
	search := adminForm(body, "/manager/catalog")
	for _, expected := range []string{"method=\"get\"", "hx-get=\"/manager/catalog\"", "hx-push-url=\"true\"", "aria-label=\"Search and refine products\"", "name=\"q\"", "<details class=\"catalog-filter-disclosure\">", "Filters &amp; sort", "5 applied", "name=\"category\"", "name=\"type\"", "name=\"availability\"", "name=\"status\"", "name=\"sort\"", "Apply filters"} {
		if !strings.Contains(search, expected) {
			t.Errorf("missing progressive filter markup %s", expected)
		}
	}
	if strings.Index(search, "name=\"q\"") > strings.Index(search, "<details") {
		t.Error("text search hidden inside mobile disclosure")
	}
	if strings.Contains(search, "<details class=\"catalog-filter-disclosure\" open") {
		t.Error("mobile filters start expanded")
	}
	if strings.Contains(body, "manager-add-product") || strings.Contains(body, "manager-mobile-actions") {
		t.Error("global Add product shortcut remains")
	}
	count := 0
	for _, link := range adminAnchorRE.FindAllStringSubmatch(body, -1) {
		u, _ := url.Parse(adminAttr(link[1], "href"))
		if u != nil && u.Query().Get("tab") == "new" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected one Add product link in catalog header, got %d", count)
	}
	if !strings.Contains(body, "href=\"/manager/catalog\" hx-get=\"/manager/catalog\"") || !strings.Contains(body, "role=\"status\" aria-live=\"polite\"") {
		t.Error("clear filters or live result count missing")
	}
}
