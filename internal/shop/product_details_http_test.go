package shop

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Read controls by their submitted names so these integration tests survive
// layout changes and still exercise the actual public and manager forms.
func detailHTTPField(t *testing.T, markup, name string) string {
	t.Helper()
	if value, attrs := adminInput(markup, name); attrs != "" {
		return value
	}
	for _, field := range regexp.MustCompile(`(?s)<textarea\b([^>]*)>(.*?)</textarea>`).FindAllStringSubmatch(markup, -1) {
		if adminAttr(field[1], "name") == name {
			return html.UnescapeString(field[2])
		}
	}
	for _, field := range regexp.MustCompile(`(?s)<select\b([^>]*)>(.*?)</select>`).FindAllStringSubmatch(markup, -1) {
		if adminAttr(field[1], "name") != name {
			continue
		}
		for _, option := range regexp.MustCompile(`(?s)<option\b([^>]*)>(.*?)</option>`).FindAllStringSubmatch(field[2], -1) {
			if regexp.MustCompile(`\bselected(?:\s|=|$)`).MatchString(option[1]) {
				return adminAttr(option[1], "value")
			}
		}
	}
	t.Fatalf("missing submitted control %q", name)
	return ""
}

func detailHTTPForm(session Session, p Product) url.Values {
	form := catalogProductForm(session, p)
	for key, value := range map[string]string{
		"details_kind": "food", "details_body": "A crisp baked snack for this HTTP test.",
		"package_label": "One box", "package_details": "Contains six wrapped portions.",
		"nutrition_enabled": "1", "nutrition_serving": "30 g (one portion)",
		"nutrition_energy": "120", "nutrition_fat": "2.5", "nutrition_carbs": "18.0",
		"nutrition_protein": "3.5", "nutrition_sodium": "150",
	} {
		form.Set(key, value)
	}
	return form
}

func detailHTTPProduct(t *testing.T, s *Store, a *App, manager Session) Product {
	t.Helper()
	p := Product{Name: "HTTP detail oat bites", Description: "A short shelf description", CategoryID: testProduct(t, s, 1).CategoryID, Icon: "bread", Price: 625, SaleUnit: "each", QuantityStep: 1}
	w := testRequest(t, a, http.MethodPost, "/manager/catalog/products", manager, detailHTTPForm(manager, p), nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("detail product create=%d: %s", w.Code, w.Body.String())
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Path != "/manager/catalog" {
		t.Fatalf("detail create destination=%q", w.Header().Get("Location"))
	}
	id, err := num(u.Query().Get("edit"))
	if err != nil || id < 1 {
		t.Fatalf("detail create does not open editor: %q", u)
	}
	return testCatalogProduct(t, s, id)
}

func TestProductDetailsHTTPPublicPageEscapesContentAndKeepsContext(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	manager := testManager(t, s)
	p := detailHTTPProduct(t, s, a, manager)
	form := detailHTTPForm(manager, p)
	content := "A <script>alert(\"details\")</script> & crunchy snack.\n\nKeep this second paragraph."
	form.Set("details_body", content)
	assertRedirect(t, testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/catalog/products/%d", p.ID), manager, form, nil), fmt.Sprintf("/manager/catalog?edit=%d", p.ID), false)
	visitor := testSession(t, s, "")
	filters := url.Values{"q": {"oat bites"}, "category": {"Produce"}, "sales": {"1"}, "featured": {"1"}}
	path := fmt.Sprintf("/products/%d?%s", p.ID, filters.Encode())
	for _, htmx := range []bool{false, true} {
		w := testRequest(t, a, http.MethodGet, path, visitor, nil, managerDraftHeaders(htmx))
		body := w.Body.String()
		if w.Code != http.StatusOK || strings.Contains(strings.ToLower(body), "<!doctype html>") == htmx {
			t.Fatalf("detail render HTMX=%t: status=%d", htmx, w.Code)
		}
		for _, want := range []string{p.Name, p.Description, "One box", "Contains six wrapped portions.", "30 g (one portion)", "$6.25", "120", "2.5", "150"} {
			if !strings.Contains(adminVisibleText(body), want) {
				t.Errorf("detail page lacks %q", want)
			}
		}
		if strings.Contains(body, `<script>alert("details")</script>`) || !strings.Contains(html.UnescapeString(body), content) {
			t.Error("long description is missing or interpreted as markup")
		}
		if !adminFindLink(body, func(u *url.URL) bool {
			return u.Path == "/" && u.Query().Get("q") == "oat bites" && u.Query().Get("category") == "Produce" && u.Query().Get("sales") == "1" && u.Query().Get("featured") == "1"
		}) {
			t.Error("detail page lost its filtered storefront back link")
		}
	}
	for _, path := range []string{"/products/999999", "/products/0", "/products/-1", "/products/not-an-id", "/products/9223372036854775808"} {
		if w := testRequest(t, a, http.MethodGet, path, visitor, nil, nil); w.Code != http.StatusNotFound {
			t.Errorf("unknown detail %s=%d", path, w.Code)
		}
	}
	p = testCatalogProduct(t, s, p.ID)
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	for _, unavailable := range []string{path, "/products/999999?" + filters.Encode()} {
		for _, htmx := range []bool{false, true} {
			w := testRequest(t, a, http.MethodGet, unavailable+"&back=https%3A%2F%2Fattacker.invalid%2F", visitor, nil, managerDraftHeaders(htmx))
			body := w.Body.String()
			visible := adminVisibleText(body)
			if w.Code != http.StatusNotFound || !strings.Contains(visible, "This product is unavailable.") {
				t.Errorf("missing/archived detail lacks generic unavailable state: status=%d", w.Code)
			}
			if strings.Contains(strings.ToLower(body), "<!doctype html>") == htmx {
				t.Error("unavailable detail changed full-page/HTMX rendering mode")
			}
			if !htmx && !strings.Contains(strings.ToLower(visible), "neighborhood market") {
				t.Error("unavailable detail lost the store branding")
			}
			for _, private := range []string{p.Name, p.Description, "Keep this second paragraph.", "One box"} {
				if strings.Contains(body, private) {
					t.Errorf("unavailable detail exposes archived product content %q", private)
				}
			}
			if adminForm(body, "/cart") != "" {
				t.Error("unavailable detail exposes a product cart form")
			}
			if !adminFindLink(body, func(u *url.URL) bool {
				return !u.IsAbs() && u.Host == "" && u.Path == "/" && u.Query().Get("q") == "oat bites" && u.Query().Get("category") == "Produce" && u.Query().Get("sales") == "1" && u.Query().Get("featured") == "1" && u.Query().Get("back") == ""
			}) {
				t.Error("unavailable detail lacks a safe filtered back link")
			}
		}
	}
}

func TestProductDetailsHTTPCartReturnAndEffectiveSalePrice(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, clock, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, false)
			shopper := testSession(t, s, "")
			p := testProduct(t, s, 1)
			start := clock.now().Truncate(time.Minute).Unix()
			if _, err := s.SavePromotion(Promotion{ProductID: p.ID, ProductVersion: p.PriceVersion, SalePrice: 249, Starts: start, Ends: start + 3600}); err != nil {
				t.Fatal(err)
			}
			path := "/products/1?q=Honeycrisp&category=Produce&sales=1&featured=1"
			page := testRequest(t, a, http.MethodGet, path, shopper, nil, nil)
			cart := adminForm(page.Body.String(), "/cart")
			if page.Code != http.StatusOK || cart == "" {
				t.Fatalf("counted product does not have a cart form: %d", page.Code)
			}
			for key, want := range map[string]string{"return": "product", "product_id": "1", "csrf": shopper.CSRF, "q": "Honeycrisp", "category": "Produce", "sales": "1", "featured": "1"} {
				if got := detailHTTPField(t, cart, key); got != want {
					t.Errorf("product cart %s=%q, want %q", key, got, want)
				}
			}
			for _, price := range []string{"$2.49", "$3.49"} {
				if !strings.Contains(page.Body.String(), price) {
					t.Errorf("detail page omits sale/regular price %s", price)
				}
			}
			values := url.Values{"csrf": {shopper.CSRF}, "revision": {fmt.Sprint(shopper.Revision)}, "product_id": {"1"}, "quantity": {"2"}, "mode": {"add"}, "return": {"product"}, "q": {"Honeycrisp"}, "category": {"Produce"}, "sales": {"1"}, "featured": {"1"}}
			w := testRequest(t, a, http.MethodPost, "/cart", shopper, values, managerDraftHeaders(htmx))
			if htmx {
				if w.Code != 200 || strings.Contains(strings.ToLower(w.Body.String()), "<!doctype html>") || adminForm(w.Body.String(), "/cart") == "" {
					t.Fatalf("HTMX add did not return product detail fragment: %d", w.Code)
				}
				if got := detailHTTPField(t, adminForm(w.Body.String(), "/cart"), "return"); got != "product" {
					t.Errorf("HTMX add switched page context=%q", got)
				}
			} else {
				u, err := url.Parse(w.Header().Get("Location"))
				if err != nil || w.Code != 303 || u.Path != "/products/1" || u.Query().Get("q") != "Honeycrisp" || u.Query().Get("category") != "Produce" || u.Query().Get("sales") != "1" || u.Query().Get("featured") != "1" {
					t.Fatalf("detail cart redirect=%d %q", w.Code, w.Header().Get("Location"))
				}
			}
			if b := testBasket(t, s, shopper.ID); b.Count != 2 || b.Total != 498 {
				t.Errorf("detail basket sale price/count=%+v", b)
			}
			// Retrying a stale cart form must show the conflict on the same detail
			// page and must not add another pair of units.
			w = testRequest(t, a, http.MethodPost, "/cart", shopper, values, managerDraftHeaders(htmx))
			if w.Code != 200 || !strings.Contains(w.Body.String(), ErrConflict.Error()) || detailHTTPField(t, adminForm(w.Body.String(), "/cart"), "return") != "product" {
				t.Fatalf("stale detail cart response=%d", w.Code)
			}
			if b := testBasket(t, s, shopper.ID); b.Count != 2 || b.Total != 498 {
				t.Error("stale detail add mutated basket")
			}
		})
	}
}

func TestProductDetailsHTTPWeightedItemVisibleButCannotBeOrdered(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	p := testCreateProduct(t, s, func(p *Product) {
		p.Name = "Detail weighed almonds"
		p.SaleUnit = "g"
		p.PriceBasis = 1000
		p.QuantityStep = 100
	})
	if err := s.Adjust(p.ID, 1000, p.Version, "Future weighed item"); err != nil {
		t.Fatal(err)
	}
	shopper := testSession(t, s, "")
	page := testRequest(t, a, http.MethodGet, fmt.Sprintf("/products/%d", p.ID), shopper, nil, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), p.Name) {
		t.Fatalf("weighted detail is unavailable=%d", page.Code)
	}
	if form := adminForm(page.Body.String(), "/cart"); form != "" {
		t.Error("weighted detail exposes a counted add-to-cart form")
	}
	values := url.Values{"csrf": {shopper.CSRF}, "revision": {fmt.Sprint(shopper.Revision)}, "product_id": {fmt.Sprint(p.ID)}, "quantity": {"1"}, "return": {"product"}, "mode": {"add"}}
	w := testRequest(t, a, http.MethodPost, "/cart", shopper, values, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), ErrUnavailable.Error()) || !strings.Contains(w.Body.String(), p.Name) {
		t.Errorf("weighted forged add=%d: %s", w.Code, w.Body.String())
	}
	if testBasket(t, s, shopper.ID).Count != 0 {
		t.Error("weighted detail created a counted reservation")
	}
}

func TestProductDetailsHTTPManagerValidationDraftAndStaleConflict(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s := newTestStore(t)
			a := testApp(t, s, testManagerPassword)
			manager := testManager(t, s)
			p := detailHTTPProduct(t, s, a, manager)
			path := fmt.Sprintf("/manager/catalog/products/%d", p.ID)
			form := detailHTTPForm(manager, p)
			form.Set("details_body", "Unsaved <script>alert(\"draft\")</script> & body\n\nRetain this draft paragraph.")
			form.Set("package_details", "Six wrapped portions.\nKeep the package note on its own line.")
			form.Set("nutrition_fat", "2.55")
			form.Set("nutrition_sodium", "invalid")
			w := testRequest(t, a, http.MethodPost, path, manager, form, managerDraftHeaders(htmx))
			if w.Code != 200 || !strings.Contains(w.Body.String(), ErrInvalid.Error()) {
				t.Fatalf("invalid detail form=%d: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if strings.Contains(strings.ToLower(body), "<!doctype html>") == htmx {
				t.Error("invalid detail form changed rendering mode")
			}
			editor := adminForm(body, path)
			for _, key := range []string{"details_kind", "details_body", "package_label", "package_details", "nutrition_serving", "nutrition_energy", "nutrition_fat", "nutrition_carbs", "nutrition_protein", "nutrition_sodium"} {
				if got := detailHTTPField(t, editor, key); got != form.Get(key) {
					t.Errorf("invalid detail draft %s=%q, want %q", key, got, form.Get(key))
				}
			}
			if strings.Contains(body, `<script>alert("draft")</script>`) {
				t.Error("manager draft rendered executable markup")
			}
			if got := testCatalogProduct(t, s, p.ID); got.CatalogVersion != p.CatalogVersion || got.Name != p.Name {
				t.Error("invalid detail form mutated catalog")
			}
			page := testRequest(t, a, http.MethodGet, fmt.Sprintf("/products/%d", p.ID), manager, nil, nil)
			if strings.Contains(page.Body.String(), "Unsaved") || !strings.Contains(page.Body.String(), "A crisp baked snack") {
				t.Error("invalid detail draft changed the public product")
			}
			current := detailHTTPForm(manager, p)
			current.Set("details_body", "A concurrent manager saved this current description.")
			if got := testRequest(t, a, http.MethodPost, path, manager, current, nil); got.Code != 303 {
				t.Fatalf("current detail save failed=%d", got.Code)
			}
			form.Set("nutrition_fat", "2.5")
			form.Set("nutrition_sodium", "150")
			w = testRequest(t, a, http.MethodPost, path, manager, form, managerDraftHeaders(htmx))
			if w.Code != 200 || !strings.Contains(w.Body.String(), ErrConflict.Error()) {
				t.Fatalf("stale detail form=%d", w.Code)
			}
			editor = adminForm(w.Body.String(), path)
			if got := detailHTTPField(t, editor, "details_body"); got != current.Get("details_body") {
				t.Errorf("stale detail draft was not discarded=%q", got)
			}
			if got := detailHTTPField(t, editor, "catalog_version"); got != fmt.Sprint(p.CatalogVersion+1) {
				t.Errorf("conflict editor lacks fresh version=%q", got)
			}
		})
	}
}

func TestProductDetailsHTTPManagerRejectsInvalidKindsNutritionAndLimits(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	manager := testManager(t, s)
	p := detailHTTPProduct(t, s, a, manager)
	path := fmt.Sprintf("/manager/catalog/products/%d", p.ID)
	cases := []struct{ field, value string }{
		{"details_kind", "unknown"}, {"details_kind", "nonfood"}, {"details_kind", "unspecified"},
		{"nutrition_serving", ""}, {"nutrition_energy", ""}, {"nutrition_energy", "120.5"},
		{"nutrition_sodium", "150.5"}, {"nutrition_fat", "-0.1"}, {"nutrition_carbs", "1e2"},
		{"nutrition_protein", "3.55"}, {"nutrition_sodium", "9223372036854775808"},
		{"details_body", strings.Repeat("界", 1601)}, {"package_label", strings.Repeat("界", 81)},
		{"package_details", strings.Repeat("界", 241)}, {"nutrition_serving", strings.Repeat("界", 81)},
	}
	for _, tc := range cases {
		t.Run(tc.field+fmt.Sprint(utf8.RuneCountInString(tc.value)), func(t *testing.T) {
			form := detailHTTPForm(manager, p)
			form.Set(tc.field, tc.value)
			w := testRequest(t, a, http.MethodPost, path, manager, form, nil)
			if w.Code != 200 || !strings.Contains(w.Body.String(), ErrInvalid.Error()) {
				t.Errorf("invalid %s accepted=%d", tc.field, w.Code)
			}
			if got := testCatalogProduct(t, s, p.ID); got.CatalogVersion != p.CatalogVersion {
				t.Errorf("invalid %s changed catalog version", tc.field)
			}
		})
	}
	form := detailHTTPForm(manager, p)
	form.Set("details_body", strings.Repeat("界", 1600))
	form.Set("package_label", strings.Repeat("箱", 80))
	form.Set("package_details", strings.Repeat("包", 240))
	form.Set("nutrition_serving", strings.Repeat("份", 80))
	assertRedirect(t, testRequest(t, a, http.MethodPost, path, manager, form, nil), fmt.Sprintf("/manager/catalog?edit=%d", p.ID), false)
	page := testRequest(t, a, http.MethodGet, fmt.Sprintf("/manager/catalog?edit=%d", p.ID), manager, nil, nil)
	for _, key := range []string{"details_body", "package_label", "package_details", "nutrition_serving"} {
		if got := detailHTTPField(t, adminForm(page.Body.String(), path), key); got != form.Get(key) {
			t.Errorf("valid Unicode boundary %s was truncated", key)
		}
	}
}

func TestProductDetailsHTTPManagerGuards(t *testing.T) {
	s := newTestStore(t)
	a := reservationHTTPApp(t, s, true)
	visitor := testSession(t, s, "")
	manager := testManager(t, s)
	p := testProduct(t, s, 1)
	before := p
	for _, route := range []string{"/manager/catalog/products", "/manager/catalog/products/1", "/manager/catalog/examples"} {
		form := detailHTTPForm(visitor, p)
		assertRedirect(t, testRequest(t, a, http.MethodPost, route, visitor, form, nil), "/manager/login", false)
		form.Set("csrf", "wrong-token")
		if w := testRequest(t, a, http.MethodPost, route, manager, form, nil); w.Code != http.StatusForbidden {
			t.Errorf("detail route CSRF guard %s=%d", route, w.Code)
		}
	}
	assertRedirect(t, testRequest(t, a, http.MethodGet, "/manager/catalog/examples", visitor, nil, nil), "/manager/login", false)
	if got := testCatalogProduct(t, s, p.ID); got != before {
		t.Error("rejected details request changed product")
	}
}

func TestProductDetailsHTTPReturnFiltersAreBoundedAndLocal(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	shopper := testSession(t, s, "")
	// Exceed the context limits without exceeding the whole POST body limit.
	filters := url.Values{"q": {strings.Repeat("界", 300)}, "category": {strings.Repeat("類", 300)}, "sales": {"true"}, "featured": {"yes"}, "back": {"https://attacker.invalid/"}}
	page := testRequest(t, a, http.MethodGet, "/products/1?"+filters.Encode(), shopper, nil, nil)
	if page.Code != 200 {
		t.Fatalf("bounded detail page=%d", page.Code)
	}
	form := adminForm(page.Body.String(), "/cart")
	for _, key := range []string{"q", "category"} {
		value := detailHTTPField(t, form, key)
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 200 || value == "" {
			t.Errorf("detail %s context is not bounded Unicode: %d bytes", key, len(value))
		}
	}
	values := url.Values{"csrf": {shopper.CSRF}, "revision": {fmt.Sprint(shopper.Revision)}, "product_id": {"1"}, "quantity": {"1"}, "mode": {"add"}, "return": {"product"}}
	for key, value := range filters {
		values[key] = value
	}
	w := testRequest(t, a, http.MethodPost, "/cart", shopper, values, nil)
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != 303 || u.IsAbs() || u.Host != "" || u.Path != "/products/1" {
		t.Fatalf("unsafe detail return=%d %q", w.Code, w.Header().Get("Location"))
	}
	if u.Query().Get("sales") != "" || u.Query().Get("featured") != "" || u.Query().Get("back") != "" {
		t.Errorf("unrecognized return filters propagated: %q", u)
	}
	for _, key := range []string{"q", "category"} {
		if value := u.Query().Get(key); !utf8.ValidString(value) || utf8.RuneCountInString(value) > 200 {
			t.Errorf("POST %s context is unbounded or malformed", key)
		}
	}
}

func detailHTTPUnseededStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "established-catalog.db")
	db := migrationRawDB(t, path)
	// An established empty v6 catalog exercises additive migration without
	// letting a fresh-database seed preinstall the examples under test.
	migrationExec(t, db, legacyPromotionsV6)
	migrationExec(t, db, `INSERT INTO catalog_sequence(id,next_product_id) VALUES(1,1)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t, path)
	if got := testCount(t, s, "products"); got != 0 {
		t.Fatalf("migration silently seeded an established catalog: %d products", got)
	}
	return s
}

func detailHTTPExampleForm(t *testing.T, a *App, manager Session) url.Values {
	t.Helper()
	page := testRequest(t, a, http.MethodGet, "/manager/catalog/examples", manager, nil, nil)
	if page.Code != 200 {
		t.Fatalf("example preview=%d: %s", page.Code, page.Body.String())
	}
	form := adminForm(page.Body.String(), "/manager/catalog/examples")
	values := url.Values{}
	for _, key := range []string{"csrf", "quote", "command_key"} {
		value := detailHTTPField(t, form, key)
		if value == "" {
			t.Fatalf("empty example preview %s", key)
		}
		values.Set(key, value)
	}
	return values
}

func TestProductDetailsHTTPExampleImportRequiresReviewAndIsReplaySafe(t *testing.T) {
	s := detailHTTPUnseededStore(t)
	a := reservationHTTPApp(t, s, true)
	manager := testManager(t, s)
	form := detailHTTPExampleForm(t, a, manager)
	if n := testCount(t, s, "products"); n != 0 {
		t.Fatal("example preview created products")
	}
	if w := testRequest(t, a, http.MethodPost, "/manager/catalog/examples", manager, form, nil); w.Code != 200 || !strings.Contains(w.Body.String(), ErrInvalid.Error()) || testCount(t, s, "products") != 0 {
		t.Fatalf("unconfirmed example import=%d", w.Code)
	}
	form.Set("confirm", "1")
	goodQuote := form.Get("quote")
	form.Set("quote", "unreviewed-quote")
	if w := testRequest(t, a, http.MethodPost, "/manager/catalog/examples", manager, form, nil); w.Code != 200 || testCount(t, s, "products") != 0 {
		t.Fatalf("forged review imported products=%d", w.Code)
	}
	form.Set("quote", goodQuote)
	for range 2 {
		if w := testRequest(t, a, http.MethodPost, "/manager/catalog/examples", manager, form, nil); w.Code != 303 || testCount(t, s, "products") != 2 {
			t.Fatalf("example import/retry=%d, products=%d: %s", w.Code, testCount(t, s, "products"), w.Body.String())
		}
	}
	products, err := s.CatalogProducts()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		name         string
		price, stock int64
	}{
		"DEMO-DEODORANT-75G": {"Everyday deodorant", 499, 24},
		"DEMO-RAZORS-3PK":    {"Three-blade razors · 3 pack", 699, 18},
	}
	for _, p := range products {
		fixture, ok := want[p.SKU]
		if !ok || p.Name != fixture.name || p.Price != fixture.price || p.Stock != fixture.stock || p.SaleUnit != "each" {
			t.Errorf("unexpected imported example=%+v", p)
		}
		page := testRequest(t, a, http.MethodGet, fmt.Sprintf("/products/%d", p.ID), manager, nil, nil)
		if page.Code != 200 || !strings.Contains(page.Body.String(), p.Name) || strings.Contains(page.Body.String(), `id="nutrition-title"`) {
			t.Errorf("nonfood public detail misleading/missing=%d", page.Code)
		}
	}
	// A completed installation is not a reset command: retrying after a manager
	// renames/archives a fixture must preserve those changes and the audit.
	p := products[0]
	p.Name = "Manager customized the deodorant display"
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	p = testCatalogProduct(t, s, p.ID)
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	before := testCatalogProduct(t, s, p.ID)
	if w := testRequest(t, a, http.MethodPost, "/manager/catalog/examples", manager, form, nil); w.Code != 303 {
		t.Fatalf("completed example retry=%d", w.Code)
	}
	if after := testCatalogProduct(t, s, p.ID); after != before {
		t.Error("completed example retry overwrote later catalog work")
	}
}

func TestProductDetailsHTTPExampleImportRefusesConflictAndNonDemo(t *testing.T) {
	t.Run("non-demo", func(t *testing.T) {
		s := newTestStore(t)
		a := testApp(t, s, testManagerPassword)
		manager := testManager(t, s)
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if w := testRequest(t, a, method, "/manager/catalog/examples", manager, url.Values{"csrf": {manager.CSRF}}, nil); w.Code != 404 {
				t.Errorf("non-demo examples %s=%d", method, w.Code)
			}
		}
	})
	for _, conflict := range []string{"reserved SKU", "archived taxonomy", "stale preview"} {
		t.Run(conflict, func(t *testing.T) {
			s := detailHTTPUnseededStore(t)
			a := reservationHTTPApp(t, s, true)
			manager := testManager(t, s)
			form := detailHTTPExampleForm(t, a, manager)
			form.Set("confirm", "1")
			id, err := s.SaveTaxonomy("category", 0, 0, "Personal care")
			if err != nil {
				t.Fatal(err)
			}
			if conflict == "archived taxonomy" {
				if err := s.ArchiveTaxonomy("category", id, 1); err != nil {
					t.Fatal(err)
				}
			} else if conflict == "reserved SKU" {
				_, err := s.SaveProduct(Product{SKU: "DEMO-DEODORANT-75G", Name: "Everyday deodorant", CategoryID: id, Icon: "apple", Price: 499, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := fingerprintTest(t, s.db)
			w := testRequest(t, a, http.MethodPost, "/manager/catalog/examples", manager, form, nil)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `role="alert"`) {
				t.Fatalf("example %s was not visibly refused=%d", conflict, w.Code)
			}
			if after := fingerprintTest(t, s.db); after != before {
				t.Errorf("example %s changed database", conflict)
			}
		})
	}
}

func TestProductDetailsHTTPArchiveAfterViewRendersUsableCartResponse(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s := newTestStore(t)
			a := testApp(t, s, testManagerPassword)
			shopper := testSession(t, s, "")
			p := testProduct(t, s, 1)
			page := testRequest(t, a, http.MethodGet, "/products/1?q=apples&category=Produce", shopper, nil, nil)
			cart := adminForm(page.Body.String(), "/cart")
			if page.Code != http.StatusOK || cart == "" {
				t.Fatal("missing original Add form")
			}
			values := url.Values{"csrf": {shopper.CSRF}, "revision": {detailHTTPField(t, cart, "revision")}, "product_id": {"1"}, "quantity": {"1"}, "mode": {"add"}, "return": {"product"}, "q": {"apples"}, "category": {"Produce"}}
			if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
				t.Fatal(err)
			}
			before := fingerprintTest(t, s.db)
			response := testRequest(t, a, http.MethodPost, "/cart", shopper, values, managerDraftHeaders(htmx))
			want := http.StatusNotFound
			if htmx {
				want = http.StatusOK
			}
			body := response.Body.String()
			if response.Code != want || !strings.Contains(adminVisibleText(body), "This product is unavailable.") {
				t.Fatalf("archived Add response = %d, want useful %d unavailable state", response.Code, want)
			}
			if strings.Contains(body, p.Name) || adminForm(body, "/cart") != "" {
				t.Error("archived Add retained product details or stale cart action")
			}
			if !adminFindLink(body, func(u *url.URL) bool {
				return !u.IsAbs() && u.Path == "/" && u.Query().Get("q") == "apples" && u.Query().Get("category") == "Produce"
			}) {
				t.Error("archived Add lost back-to-shelves context")
			}
			if after := fingerprintTest(t, s.db); after != before {
				t.Error("archive-raced Add changed stock, holds, catalog or audit")
			}
		})
	}
}
