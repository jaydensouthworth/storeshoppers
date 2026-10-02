package shop

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogPaginationRangesAndStableBoundaries(t *testing.T) {
	products := make([]Product, 45)
	for i := range products {
		products[i] = Product{ID: int64(45 - i), Name: "Same name", SKU: "Same SKU", Price: 100, SaleUnit: "each"}
	}
	for _, sort := range []string{"name", "name-desc", "sku", "price-asc", "price-desc"} {
		for _, tc := range []struct{ requested, page, first, last, size int }{{1, 1, 1, 20, 20}, {2, 2, 21, 40, 20}, {3, 3, 41, 45, 5}, {999, 3, 41, 45, 5}, {0, 1, 1, 20, 20}} {
			f := CatalogFilters{Status: "active", Sort: sort, Page: tc.requested}
			sorted := filterCatalogProducts(products, f, nil)
			page, meta := paginateCatalogProducts(sorted, &f)
			if meta.Total != 45 || meta.Page != tc.page || meta.Pages != 3 || meta.First != tc.first || meta.Last != tc.last || len(page) != tc.size || f.Page != tc.page {
				t.Fatalf("sort %s page %d: %+v rows=%d", sort, tc.requested, meta, len(page))
			}
			for i, p := range page {
				if p.ID != int64(tc.first+i) {
					t.Errorf("sort %s tie boundary changed at %d: %d", sort, i, p.ID)
				}
			}
			if (meta.PreviousURL != "") != (tc.page > 1) || (meta.NextURL != "") != (tc.page < 3) {
				t.Errorf("wrong boundary links %+v", meta)
			}
		}
	}
	f := CatalogFilters{Query: "missing", Page: 9, Status: "active", Sort: "name"}
	empty, meta := paginateCatalogProducts(nil, &f)
	if len(empty) != 0 || meta.Total != 0 || meta.Page != 1 || meta.Pages != 1 || meta.First != 0 || meta.Last != 0 || meta.PreviousURL != "" || meta.NextURL != "" || f.Page != 1 {
		t.Fatalf("empty metadata %+v filters %+v", meta, f)
	}
}
func TestCatalogPageQueryCanonicalAndBounded(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		page    int
		encoded string
	}{{"", 1, ""}, {"1", 1, ""}, {"0002", 2, "2"}, {"1000000", 1000000, "1000000"}, {"1000001", 1, ""}, {"-1", 1, ""}, {"0", 1, ""}, {"abc", 1, ""}, {"999999999999999999999999", 1, ""}, {"2.5", 1, ""}} {
		f := catalogFilters(httptest.NewRequest(http.MethodGet, "/manager/catalog?page="+tc.raw, nil))
		if f.Page != tc.page || f.values().Get("page") != tc.encoded {
			t.Errorf("page %q => %+v / %s", tc.raw, f, f.ListURL())
		}
	}
	f := CatalogFilters{Query: "fruit & vegetables", CategoryID: 7, Type: "none", Availability: "in-stock", Status: "all", Sort: "newest", Page: 2}
	u, _ := url.Parse(f.PageURL(3))
	if u.Query().Get("page") != "3" || u.Query().Get("q") != f.Query || u.Query().Get("category") != "7" || u.Query().Get("type") != "none" || u.Query().Get("availability") != "in-stock" || u.Query().Get("status") != "all" || u.Query().Get("sort") != "newest" {
		t.Fatalf("pager lost filters: %s", u)
	}
	if f.Page != 2 {
		t.Error("pager URL changed source filters")
	}
}
func TestHTTPCatalogPagesAndContext(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	manager := testManager(t, s)
	all, err := s.CatalogProducts()
	if err != nil {
		t.Fatal(err)
	}
	f := CatalogFilters{Status: "all", Sort: "sku", Page: 2}
	sorted := filterCatalogProducts(all, f, nil)
	for _, htmx := range []bool{false, true} {
		headers := map[string]string{}
		if htmx {
			headers["HX-Request"] = "true"
		}
		for page := 1; page <= 4; page++ {
			f.Page = page
			response := testRequest(t, a, http.MethodGet, f.ListURL(), manager, nil, headers)
			body := response.Body.String()
			end := page * catalogPageSize
			if end > len(sorted) {
				end = len(sorted)
			}
			want := []string{}
			for _, p := range sorted[(page-1)*catalogPageSize : end] {
				want = append(want, fmt.Sprint(p.ID))
			}
			if response.Code != http.StatusOK || !reflect.DeepEqual(catalogHTTPIDs(body), want) {
				t.Fatalf("htmx %t page %d rows=%v want=%v", htmx, page, catalogHTTPIDs(body), want)
			}
			if !strings.Contains(body, "72 results</strong>") || !strings.Contains(body, fmt.Sprintf("Showing %d–%d", (page-1)*20+1, end)) || !strings.Contains(body, fmt.Sprintf("Page %d of 4", page)) {
				t.Errorf("wrong page/range/total: page %d", page)
			}
			if page > 1 && !adminFindLink(body, func(u *url.URL) bool { return u.String() == f.PageURL(page-1) }) {
				t.Error("previous link lost context")
			}
			if page < 4 && !adminFindLink(body, func(u *url.URL) bool { return u.String() == f.PageURL(page+1) }) {
				t.Error("next link lost context")
			}
			if !strings.Contains(body, "aria-label=\"Product catalog pages\"") || !strings.Contains(body, "hx-push-url=\"true\"") {
				t.Error("accessible HTMX pager missing")
			}
			if got, attributes := adminInput(adminForm(body, "/manager/catalog"), "page"); got != "" || attributes != "" {
				t.Error("changing search/filters should start on page 1")
			}
			id := sorted[(page-1)*catalogPageSize].ID
			if !adminFindLink(body, func(u *url.URL) bool { return u.String() == f.EditURL(id) }) {
				t.Error("row editor link lost page")
			}
			editor := testRequest(t, a, http.MethodGet, f.EditURL(id), manager, nil, headers)
			assertCatalogContext(t, editor.Body.String(), f, fmt.Sprintf("/manager/catalog/products/%d", id))
		}
	}
	// Taxonomy controls are complete even though product rows are limited.
	body := testRequest(t, a, http.MethodGet, "/manager/catalog?page=2", manager, nil, nil).Body.String()
	categories, _ := s.Taxonomies("category", true)
	for _, category := range categories {
		if !strings.Contains(body, fmt.Sprintf("value=\"%d\"", category.ID)) || !strings.Contains(body, category.Name) {
			t.Errorf("pagination hid category %s", category.Name)
		}
	}
	// Editor lookup is independent of which list page the selected product occupies.
	f.Page = 2
	response := testRequest(t, a, http.MethodGet, f.EditURL(sorted[0].ID), manager, nil, nil)
	if response.Code != http.StatusOK || adminForm(response.Body.String(), fmt.Sprintf("/manager/catalog/products/%d", sorted[0].ID)) == "" {
		t.Error("pagination hid an off-page editor")
	}
}
func TestHTTPCatalogPageNormalizationAndEmptyResults(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	manager := testManager(t, s)
	for _, tc := range []struct {
		query, target string
		rows          int
	}{
		{"page=999&sort=sku", "/manager/catalog?page=4&sort=sku", 12},
		{"page=0002&sort=sku", "/manager/catalog?page=2&sort=sku", 20},
		{"page=1000001", "/manager/catalog", 20},
		{"page=-7", "/manager/catalog", 20},
		{"page=1", "/manager/catalog", 20},
		{"page=2&page=3", "/manager/catalog?page=2", 20},
		{"page=4&q=no-such-product", "/manager/catalog?q=no-such-product", 0},
	} {
		for _, htmx := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", tc.query, htmx), func(t *testing.T) {
				headers := map[string]string{}
				if htmx {
					headers["HX-Request"] = "true"
				}
				response := testRequest(t, a, http.MethodGet, "/manager/catalog?"+tc.query, manager, nil, headers)
				if htmx {
					if response.Code != http.StatusOK || response.Header().Get("HX-Replace-Url") != tc.target {
						t.Fatalf("HTMX normalization %d %q", response.Code, response.Header().Get("HX-Replace-Url"))
					}
				} else {
					assertRedirect(t, response, tc.target, false)
					response = testRequest(t, a, http.MethodGet, tc.target, manager, nil, nil)
				}
				if got := len(catalogHTTPIDs(response.Body.String())); got != tc.rows {
					t.Errorf("normalized rows=%d want=%d", got, tc.rows)
				}
				if tc.rows == 0 && (!strings.Contains(response.Body.String(), "0 results</strong>") || strings.Contains(response.Body.String(), "Product catalog pages")) {
					t.Error("empty results show incorrect count/pager")
				}
			})
		}
	}
}

func TestHTTPCatalogArchiveLastPageKeepsEditorAndNormalizesReturn(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	manager := testManager(t, s)
	testExec(t, s, "UPDATE products SET name='Pagination candidate' WHERE id<=21")
	f := CatalogFilters{Query: "Pagination candidate", Status: "active", Sort: "newest", Page: 2}
	body := testRequest(t, a, http.MethodGet, f.ListURL(), manager, nil, nil).Body.String()
	if got := catalogHTTPIDs(body); !reflect.DeepEqual(got, []string{"1"}) {
		t.Fatalf("last page rows=%v", got)
	}
	p := testProduct(t, s, 1)
	values := withCatalogContext(catalogProductForm(manager, p), f)
	saved := testRequest(t, a, http.MethodPost, "/manager/catalog/products/1/archive", manager, values, map[string]string{"HX-Request": "true"})
	if saved.Code != http.StatusOK || saved.Header().Get("HX-Push-Url") != f.EditURL(1) {
		t.Fatalf("archive lost current editor/page: %d %s", saved.Code, saved.Header().Get("HX-Push-Url"))
	}
	assertCatalogContext(t, saved.Body.String(), f, "/manager/catalog/products/1/restore")
	back := testRequest(t, a, http.MethodGet, f.ListURL(), manager, nil, nil)
	assertRedirect(t, back, f.PageURL(1), false)
	current := testRequest(t, a, http.MethodGet, back.Header().Get("Location"), manager, nil, nil)
	if len(catalogHTTPIDs(current.Body.String())) != 20 || !strings.Contains(current.Body.String(), "20 results</strong>") || strings.Contains(current.Body.String(), "Product catalog pages") {
		t.Error("return from archived last item did not show remaining page")
	}
}
