package shop

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func catalogProductForm(session Session, p Product) url.Values {
	return url.Values{"csrf": {session.CSRF}, "sku": {p.SKU}, "name": {p.Name}, "description": {p.Description}, "category_id": {fmt.Sprint(p.CategoryID)}, "type_id": {fmt.Sprint(p.TypeID)}, "icon": {p.Icon}, "price": {fmt.Sprint(p.Price)}, "sale_unit": {p.SaleUnit}, "quantity_step": {fmt.Sprint(p.QuantityStep)}, "catalog_version": {fmt.Sprint(p.CatalogVersion)}}
}

func testManager(t *testing.T, s *Store) Session {
	t.Helper()
	session := testSession(t, s, "")
	if err := s.Manager(session.ID, true); err != nil {
		t.Fatal(err)
	}
	return testSession(t, s, session.ID)
}

func TestHTTPCatalogManagerGuardAndRoundTrip(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	visitor := testSession(t, s, "")
	assertRedirect(t, testRequest(t, a, http.MethodGet, "/manager/catalog", visitor, nil, nil), "/manager/login", false)
	manager := testManager(t, s)
	page := testRequest(t, a, http.MethodGet, "/manager/catalog?q=Honeycrisp", manager, nil, nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Honeycrisp apples") || !strings.Contains(page.Body.String(), `name="csrf"`) {
		t.Fatalf("catalog page = %d: %s", page.Code, page.Body.String())
	}
	category := testProduct(t, s, 1).CategoryID
	p := Product{Name: "HTTP artisan crackers", Description: "Freshly added in manager", CategoryID: category, Icon: "bread", Price: 550, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1}
	count := testCount(t, s, "products")
	createValues := catalogProductForm(manager, p)
	createValues.Set("stock", "9000") // Catalog forms may never invent stock without audit.
	w := testRequest(t, a, http.MethodPost, "/manager/catalog/products", manager, createValues, nil)
	assertRedirect(t, w, "/manager/catalog?edit=73", false)
	all, err := s.CatalogProducts()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range all {
		if candidate.Name == p.Name {
			p = candidate
			break
		}
	}
	if p.ID == 0 || testCount(t, s, "products") != count+1 || p.Stock != 0 || p.CatalogVersion != 1 {
		t.Fatalf("HTTP create did not create zero-stock product: %+v", p)
	}
	p.Name = "HTTP artisan crackers revised"
	p.Price = 625
	w = testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/catalog/products/%d", p.ID), manager, catalogProductForm(manager, p), map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "<!doctype html>") || !strings.Contains(w.Body.String(), p.Name) {
		t.Errorf("HTMX catalog edit = %d: %s", w.Code, w.Body.String())
	}
	saved := testCatalogProduct(t, s, p.ID)
	if saved.Name != p.Name || saved.Price != 625 || saved.CatalogVersion != 2 || saved.PriceVersion != 2 {
		t.Errorf("HTTP edit result: %+v", saved)
	}
	p.Name = "Stale edit must never win"
	w = testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/catalog/products/%d", p.ID), manager, catalogProductForm(manager, p), nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrConflict.Error()) {
		t.Errorf("stale HTTP edit = %d: %s", w.Code, w.Body.String())
	}
	if got := testCatalogProduct(t, s, p.ID); got != saved {
		t.Error("stale HTTP form changed product")
	}
	w = testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/catalog/products/%d/archive", p.ID), manager, url.Values{"csrf": {manager.CSRF}, "catalog_version": {fmt.Sprint(saved.CatalogVersion)}}, nil)
	assertRedirect(t, w, "/manager/catalog?edit=73", false)
	if !testCatalogProduct(t, s, p.ID).Archived {
		t.Error("HTTP archive not persisted")
	}
	home := testRequest(t, a, http.MethodGet, "/", visitor, nil, nil)
	if strings.Contains(home.Body.String(), saved.Name) {
		t.Error("archived item leaked into storefront")
	}
}

func TestHTTPTaxonomyFormsAndProductValidation(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	manager := testManager(t, s)
	for _, tc := range []struct{ route, kind, name string }{{"categories", "category", "HTTP seasonal"}, {"types", "type", "HTTP fruit"}} {
		path := "/manager/catalog/" + tc.route
		w := testRequest(t, a, http.MethodPost, path, manager, url.Values{"csrf": {manager.CSRF}, "name": {tc.name}}, nil)
		assertRedirect(t, w, "/manager/catalog?tab=labels", false)
		all, err := s.Taxonomies(tc.kind, false)
		if err != nil {
			t.Fatal(err)
		}
		var created Taxonomy
		for _, tax := range all {
			if tax.Name == tc.name {
				created = tax
			}
		}
		if created.ID == 0 {
			t.Fatalf("taxonomy create missing: %s", tc.kind)
		}
		path += fmt.Sprintf("/%d", created.ID)
		values := url.Values{"csrf": {manager.CSRF}, "name": {tc.name + " renamed"}, "version": {"1"}}
		assertRedirect(t, testRequest(t, a, http.MethodPost, path, manager, values, nil), "/manager/catalog?tab=labels", false)
		w = testRequest(t, a, http.MethodPost, path, manager, values, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrConflict.Error()) {
			t.Errorf("taxonomy stale form missing conflict: %d", w.Code)
		}
		assertRedirect(t, testRequest(t, a, http.MethodPost, path+"/archive", manager, url.Values{"csrf": {manager.CSRF}, "version": {"2"}}, nil), "/manager/catalog?tab=labels", false)
		if !testTaxonomy(t, s, tc.kind, created.ID).Archived {
			t.Error("taxonomy HTTP archive missing")
		}
	}
	p := testProduct(t, s, 1)
	before := testCount(t, s, "products")
	for _, field := range []string{"category_id", "type_id", "price", "quantity_step"} {
		values := catalogProductForm(manager, p)
		values.Set("sku", "")
		values.Set(field, "9223372036854775808")
		w := testRequest(t, a, http.MethodPost, "/manager/catalog/products", manager, values, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `role="alert"`) {
			t.Errorf("%s malformed field lacks visible error: %d", field, w.Code)
		}
	}
	if testCount(t, s, "products") != before {
		t.Error("malformed forms created products")
	}
}

func TestHTTPQuoteIsRenderedAndReconfirmationIsExplicit(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s := newTestStore(t)
			a := testApp(t, s, testManagerPassword)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 2)
			session := testSession(t, s, owner.ID)
			shown := testBasket(t, s, owner.ID)
			page := testRequest(t, a, http.MethodGet, "/cart", session, nil, nil)
			if !strings.Contains(page.Body.String(), `name="quote"`) || !strings.Contains(page.Body.String(), shown.Quote) {
				t.Fatal("checkout lacks exact basket quote hidden field")
			}
			p := testProduct(t, s, 1)
			p.Price += 100
			if _, err := s.SaveProduct(p); err != nil {
				t.Fatal(err)
			}
			headers := map[string]string{}
			if htmx {
				headers["HX-Request"] = "true"
			}
			values := url.Values{"csrf": {session.CSRF}, "checkout_key": {session.CheckoutKey}, "revision": {fmt.Sprint(session.Revision)}, "quote": {shown.Quote}}
			w := testRequest(t, a, http.MethodPost, "/checkout", session, values, headers)
			current := testBasket(t, s, owner.ID)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrQuote.Error()) || !strings.Contains(w.Body.String(), current.Quote) || testCount(t, s, "orders") != 0 {
				t.Errorf("stale HTTP quote response = %d: %s", w.Code, w.Body.String())
			}
			values.Set("quote", current.Quote)
			w = testRequest(t, a, http.MethodPost, "/checkout", session, values, headers)
			orders, err := s.Orders(owner.ID, false)
			if err != nil || len(orders) != 1 {
				t.Fatalf("reconfirmed order = %+v, %v", orders, err)
			}
			assertRedirect(t, w, fmt.Sprintf("/orders/%d", orders[0].ID), htmx)
		})
	}
}

func TestDemoCatalogChangesCannotExposeOtherSessionOrders(t *testing.T) {
	s := newTestStore(t)
	a, err := New(s, Config{Origin: testOrigin, ManagerPassword: "password", DemoMode: true})
	if err != nil {
		t.Fatal(err)
	}
	other := testSession(t, s, "")
	testCart(t, s, other.ID, 1, 1)
	id := testCheckout(t, s, other.ID)
	order := testOrder(t, s, id, other.ID)
	manager := testManager(t, s)
	p := testProduct(t, s, 1)
	p.Name = "Shared demo apples"
	assertRedirect(t, testRequest(t, a, http.MethodPost, "/manager/catalog/products/1", manager, catalogProductForm(manager, p), nil), "/manager/catalog?edit=1", false)
	for _, path := range []string{"/manager/catalog", "/manager", "/orders"} {
		w := testRequest(t, a, http.MethodGet, path, manager, nil, nil)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), order.Reference) || strings.Contains(w.Body.String(), other.ID) {
			t.Errorf("demo page %s leaked another shopper: %d", path, w.Code)
		}
	}
	for _, path := range []string{fmt.Sprintf("/orders/%d", id), fmt.Sprintf("/orders/%d/status", id), fmt.Sprintf("/manager/orders/%d", id)} {
		if w := testRequest(t, a, http.MethodGet, path, manager, nil, nil); w.Code != http.StatusNotFound {
			t.Errorf("catalog manager crossed demo ownership at %s: %d", path, w.Code)
		}
	}
	if got := testOrder(t, s, id, other.ID); !reflect.DeepEqual(got, order) {
		t.Error("shared catalog edit changed another shopper's receipt")
	}
}

func TestHTTPWeightedProductUsesGramStepAndPreservesBasis(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	manager := testManager(t, s)
	p := Product{Name: "HTTP future weighed nuts", CategoryID: testProduct(t, s, 1).CategoryID, Icon: "leaf", Price: 1599, SaleUnit: "g", QuantityStep: 100}
	values := catalogProductForm(manager, p)
	values.Set("price_basis", "1")
	assertRedirect(t, testRequest(t, a, http.MethodPost, "/manager/catalog/products", manager, values, nil), "/manager/catalog?edit=73", false)
	all, err := s.CatalogProducts()
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range all {
		if got.Name == p.Name {
			p = got
			break
		}
	}
	if p.ID == 0 || p.PriceBasis != 1000 || p.SaleUnit != "g" {
		t.Fatalf("unit basis trusted forged request: %+v", p)
	}
	if err := s.Adjust(p.ID, 500, p.Version, "Future weighted stock"); err != nil {
		t.Fatal(err)
	}
	shopper := testSession(t, s, "")
	w := testRequest(t, a, http.MethodPost, "/cart", shopper, url.Values{"revision": {fmt.Sprint(shopper.Revision)}, "csrf": {shopper.CSRF}, "product_id": {fmt.Sprint(p.ID)}, "quantity": {"1"}}, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrInvalid.Error()) {
		t.Errorf("weighted HTTP cart = %d: %s", w.Code, w.Body.String())
	}
	if b := testBasket(t, s, shopper.ID); b.Count != 0 || b.CanCheckout {
		t.Errorf("weighted HTTP request created counted basket: %+v", b)
	}
	valid := url.Values{"revision": {fmt.Sprint(shopper.Revision)}, "csrf": {shopper.CSRF}, "product_id": {fmt.Sprint(p.ID)}, "quantity": {"200"}}
	assertRedirect(t, testRequest(t, a, http.MethodPost, "/cart", shopper, valid, nil), "/", false)
	if b := testBasket(t, s, shopper.ID); b.Count != 1 || !b.HasWeight || !b.CanCheckout || b.Total != 320 || b.Lines[0].Quantity != 200 {
		t.Errorf("gram HTTP reservation or price incorrect: %+v", b)
	}
}

func TestHTTPRestoreTaxonomyPreservesVersionGuard(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	manager := testManager(t, s)
	for _, tc := range []struct{ kind, route string }{{"category", "categories"}, {"type", "types"}} {
		id, err := s.SaveTaxonomy(tc.kind, 0, 0, "HTTP restored "+tc.kind)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ArchiveTaxonomy(tc.kind, id, 1); err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/manager/catalog/%s/%d/restore", tc.route, id)
		values := url.Values{"csrf": {manager.CSRF}, "version": {"1"}}
		w := testRequest(t, a, http.MethodPost, path, manager, values, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrConflict.Error()) {
			t.Errorf("stale taxonomy restore = %d: %s", w.Code, w.Body.String())
		}
		if !testTaxonomy(t, s, tc.kind, id).Archived {
			t.Error("stale taxonomy restore succeeded")
		}
		values.Set("version", "2")
		assertRedirect(t, testRequest(t, a, http.MethodPost, path, manager, values, nil), "/manager/catalog?tab=labels", false)
		if got := testTaxonomy(t, s, tc.kind, id); got.Archived || got.Version != 3 {
			t.Errorf("restored taxonomy = %+v", got)
		}
	}
}

func TestHTTPProductRestoreGuardsAndExplicitRecovery(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	manager := testManager(t, s)
	category, err := s.SaveTaxonomy("category", 0, 0, "HTTP recovery department")
	if err != nil {
		t.Fatal(err)
	}
	p := testCreateProduct(t, s, func(p *Product) { p.CategoryID = category })
	if err := s.ArchiveProduct(p.ID, p.CatalogVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveTaxonomy("category", category, 1); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/manager/catalog/products/%d/restore", p.ID)
	values := url.Values{"csrf": {manager.CSRF}, "catalog_version": {"2"}}
	w := testRequest(t, a, http.MethodPost, path, manager, values, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrTaxonomyInactive.Error()) {
		t.Errorf("inactive-label product restore = %d: %s", w.Code, w.Body.String())
	}
	if !testCatalogProduct(t, s, p.ID).Archived {
		t.Error("inactive-label HTTP restore succeeded")
	}
	if err := s.RestoreTaxonomy("category", category, 2); err != nil {
		t.Fatal(err)
	}
	values.Set("catalog_version", "1")
	w = testRequest(t, a, http.MethodPost, path, manager, values, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrConflict.Error()) {
		t.Errorf("stale product restore = %d: %s", w.Code, w.Body.String())
	}
	values.Set("catalog_version", "2")
	assertRedirect(t, testRequest(t, a, http.MethodPost, path, manager, values, nil), fmt.Sprintf("/manager/catalog?edit=%d", p.ID), false)
	if got := testProduct(t, s, p.ID); got.Archived || got.CatalogVersion != 3 || got.SKU != p.SKU {
		t.Errorf("HTTP product restore changed identity: %+v", got)
	}
}
