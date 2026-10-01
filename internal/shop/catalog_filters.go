package shop

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const catalogPageSize = 20
const maxCatalogPage = 1000000

type CatalogPage struct {
	Total, Page, Pages, First, Last int
	PreviousURL, NextURL            string
}

// CatalogFilters is the complete, bounded catalog navigation context. It never
// accepts a return URL, SQL expression, or editor identity from submitted data.
type CatalogFilters struct {
	Query, Type, Availability, Status, Sort string
	CategoryID                              int64
	Page                                    int
}

type catalogFilterField struct{ Name, Value string }

func catalogFilters(r *http.Request) CatalogFilters {
	values := r.URL.Query()
	if r.Method == http.MethodPost {
		values = r.PostForm
	}
	f := CatalogFilters{Query: managerSearch(r), Status: "active", Sort: "name", Page: 1}
	if page, err := strconv.Atoi(values.Get("page")); err == nil && page > 0 && page <= maxCatalogPage {
		f.Page = page
	}
	if id, err := strconv.ParseInt(values.Get("category"), 10, 64); err == nil && id > 0 {
		f.CategoryID = id
	}
	if value := values.Get("type"); value == "none" {
		f.Type = value
	} else if id, err := strconv.ParseInt(value, 10, 64); err == nil && id > 0 {
		f.Type = strconv.FormatInt(id, 10)
	}
	switch value := values.Get("availability"); value {
	case "in-stock", "low-stock", "out-of-stock":
		f.Availability = value
	}
	switch value := values.Get("status"); value {
	case "all", "archived":
		f.Status = value
	}
	switch value := values.Get("sort"); value {
	case "name-desc", "sku", "newest", "price-asc", "price-desc":
		f.Sort = value
	}
	return f
}

func (f CatalogFilters) Fields() []catalogFilterField {
	category := ""
	if f.CategoryID > 0 {
		category = strconv.FormatInt(f.CategoryID, 10)
	}
	return []catalogFilterField{{"q", f.Query}, {"category", category}, {"type", f.Type}, {"availability", f.Availability}, {"status", f.Status}, {"sort", f.Sort}, {"page", strconv.Itoa(f.pageNumber())}}
}

func (f CatalogFilters) values() url.Values {
	v := url.Values{}
	for _, field := range f.Fields() {
		if field.Value != "" && !(field.Name == "status" && field.Value == "active") && !(field.Name == "sort" && field.Value == "name") && !(field.Name == "page" && field.Value == "1") {
			v.Set(field.Name, field.Value)
		}
	}
	return v
}

func catalogURL(v url.Values) string {
	if len(v) == 0 {
		return "/manager/catalog"
	}
	return "/manager/catalog?" + v.Encode()
}
func (f CatalogFilters) ListURL() string { return catalogURL(f.values()) }
func (f CatalogFilters) pageNumber() int {
	if f.Page < 1 || f.Page > maxCatalogPage {
		return 1
	}
	return f.Page
}
func (f CatalogFilters) PageURL(page int) string {
	f.Page = page
	return f.ListURL()
}

// Paginate only the already-filtered, stably sorted list. Editors and taxonomy
// options are loaded separately from the complete catalog before this step.
func paginateCatalogProducts(products []Product, f *CatalogFilters) ([]Product, CatalogPage) {
	p := CatalogPage{Total: len(products), Page: f.pageNumber(), Pages: (len(products) + catalogPageSize - 1) / catalogPageSize}
	if p.Pages < 1 {
		p.Pages = 1
	}
	if p.Page > p.Pages {
		p.Page = p.Pages
	}
	f.Page = p.Page
	start := (p.Page - 1) * catalogPageSize
	end := start + catalogPageSize
	if end > p.Total {
		end = p.Total
	}
	if p.Total > 0 {
		p.First, p.Last = start+1, end
	}
	if p.Page > 1 {
		p.PreviousURL = f.PageURL(p.Page - 1)
	}
	if p.Page < p.Pages {
		p.NextURL = f.PageURL(p.Page + 1)
	}
	return products[start:end], p
}

func (f CatalogFilters) NewURL() string {
	v := f.values()
	v.Set("tab", "new")
	return catalogURL(v)
}
func (f CatalogFilters) EditURL(id int64) string {
	v := f.values()
	v.Set("edit", strconv.FormatInt(id, 10))
	return catalogURL(v)
}
func (f CatalogFilters) TypeSelected(id int64) bool { return f.Type == strconv.FormatInt(id, 10) }
func (f CatalogFilters) Refined() bool              { return f.Query != "" || f.SecondaryCount() > 0 }
func (f CatalogFilters) SecondaryCount() int {
	count := 0
	for _, changed := range []bool{f.CategoryID > 0, f.Type != "", f.Availability != "", f.Status != "active", f.Sort != "name"} {
		if changed {
			count++
		}
	}
	return count
}
func (f CatalogFilters) HasCategory(categories []Taxonomy) bool {
	for _, category := range categories {
		if category.ID == f.CategoryID {
			return true
		}
	}
	return f.CategoryID == 0
}
func (f CatalogFilters) HasType(types []Taxonomy) bool {
	for _, kind := range types {
		if f.TypeSelected(kind.ID) {
			return true
		}
	}
	return f.Type == "" || f.Type == "none"
}

// Include both raw and normalized stored identities, even for archived products.
// This is a manager search only; it does not change scanner/checkout eligibility.
func (s *Store) catalogCodeMatches(query string) (map[int64]bool, error) {
	matches := map[int64]bool{}
	if query == "" {
		return matches, nil
	}
	rows, err := s.db.Query(`SELECT DISTINCT product_id FROM product_codes WHERE instr(lower(raw_value),lower(?))>0 OR instr(lower(normalized_value),lower(?))>0`, query, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		matches[id] = true
	}
	return matches, rows.Err()
}

func filterCatalogProducts(products []Product, f CatalogFilters, codeMatches map[int64]bool) []Product {
	filtered := make([]Product, 0, len(products))
	needle := strings.ToLower(f.Query)
	for _, p := range products {
		if (f.Status == "active" && p.Archived) || (f.Status == "archived" && !p.Archived) || (f.CategoryID > 0 && p.CategoryID != f.CategoryID) {
			continue
		}
		if f.Type != "" && ((f.Type == "none" && p.TypeID != 0) || (f.Type != "none" && !f.TypeSelected(p.TypeID))) {
			continue
		}
		if (f.Availability == "in-stock" && p.Stock <= 0) || (f.Availability == "out-of-stock" && p.Stock != 0) || (f.Availability == "low-stock" && (p.SaleUnit != "each" || p.Stock < 1 || p.Stock >= 8)) {
			continue
		}
		if needle != "" && !codeMatches[p.ID] && !strings.Contains(strings.ToLower(p.Name+" "+p.SKU+" "+p.Barcode+" "+p.Category+" "+p.ProductType), needle) {
			continue
		}
		filtered = append(filtered, p)
	}
	sort.Slice(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		switch f.Sort {
		case "newest":
			return a.ID > b.ID
		case "sku":
			if strings.ToLower(a.SKU) != strings.ToLower(b.SKU) {
				return strings.ToLower(a.SKU) < strings.ToLower(b.SKU)
			}
		case "price-asc", "price-desc":
			if a.Price != b.Price {
				if f.Sort == "price-desc" {
					return a.Price > b.Price
				}
				return a.Price < b.Price
			}
		}
		if strings.ToLower(a.Name) != strings.ToLower(b.Name) {
			if f.Sort == "name-desc" {
				return strings.ToLower(a.Name) > strings.ToLower(b.Name)
			}
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return a.ID < b.ID
	})
	return filtered
}
