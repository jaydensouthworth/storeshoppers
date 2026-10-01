package shop

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// CatalogProductForm keeps the shared create/edit field template scoped to a
// product and the manager's current CSRF token. Posted drafts are never HTML.
type CatalogProductForm struct {
	Product           Product
	Categories, Types []Taxonomy
	Session           Session
	FieldID           string
}

type CatalogTaxonomyPanel struct {
	Kind, Title string
	Items       []Taxonomy
	Session     Session
}

func catalogTaxonomyPanel(kind string, v View) CatalogTaxonomyPanel {
	if kind == "categories" {
		return CatalogTaxonomyPanel{Kind: kind, Title: "Departments", Items: v.Categories, Session: v.Session}
	}
	return CatalogTaxonomyPanel{Kind: "types", Title: "Product types", Items: v.Types, Session: v.Session}
}

func newCatalogProduct() Product {
	return Product{Icon: "apple", Price: 100, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1}
}

func catalogIllustrations() []string {
	return []string{"apple", "leaf", "bread", "milk", "egg", "pasta", "oil", "jam"}
}

func catalogForm(p Product, v View) CatalogProductForm {
	if v.CatalogDraft != nil && v.CatalogDraft.ID == p.ID {
		sku := p.SKU
		p = *v.CatalogDraft
		if p.ID != 0 {
			p.SKU = sku
		}
	}
	id := "new"
	if p.ID != 0 {
		id = strconv.FormatInt(p.ID, 10)
	}
	return CatalogProductForm{Product: p, Categories: v.Categories, Types: v.Types, Session: v.Session, FieldID: id}
}

func (a *App) showCatalog(w http.ResponseWriter, r *http.Request) {
	a.catalogView(w, r, "", nil, nil)
}

func (a *App) catalogView(w http.ResponseWriter, r *http.Request, message string, problem error, draft *Product) {
	v, ok := a.view(w, r)
	if !ok || !a.guard(w, r, v.Session) {
		return
	}
	var err error
	if v.CatalogProducts, err = a.store.CatalogProducts(); err != nil {
		a.fail(w, err)
		return
	}
	if v.Categories, err = a.store.Taxonomies("category", true); err != nil {
		a.fail(w, err)
		return
	}
	if v.Types, err = a.store.Taxonomies("type", true); err != nil {
		a.fail(w, err)
		return
	}
	if v.CatalogEvents, err = a.store.CatalogEvents(); err != nil {
		a.fail(w, err)
		return
	}
	for _, p := range v.CatalogProducts {
		if !p.Archived {
			v.ProductCount++
		}
	}
	for _, c := range v.Categories {
		if !c.Archived {
			v.CategoryCount++
		}
	}
	if r.Method == http.MethodPost && r.Header.Get("HX-Request") == "true" {
		current, _ := url.Parse(r.Header.Get("HX-Current-URL"))
		if current == nil || current.Path != "/manager/catalog" {
			w.Header().Set("HX-Push-Url", "/manager/catalog")
		}
	}
	v.Search = strings.TrimSpace(r.URL.Query().Get("q"))
	if len(v.Search) > 100 {
		v.Search = v.Search[:100]
	}
	if v.Search != "" {
		filtered := make([]Product, 0, len(v.CatalogProducts))
		needle := strings.ToLower(v.Search)
		for _, p := range v.CatalogProducts {
			if strings.Contains(strings.ToLower(p.Name+" "+p.SKU+" "+p.Category+" "+p.ProductType), needle) {
				filtered = append(filtered, p)
			}
		}
		v.CatalogProducts = filtered
	}
	v.Title, v.Section = "Manage the catalog", "catalog"
	v.Message, v.CatalogDraft = message, draft
	if problem != nil {
		v.Error = problem.Error()
	}
	a.render(w, r, v, http.StatusOK)
}

func (a *App) catalogResult(w http.ResponseWriter, r *http.Request, message string, err error, draft *Product) {
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrDuplicate) && !errors.Is(err, ErrReferenced) && !errors.Is(err, ErrUnitLocked) && !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrTaxonomyInactive) {
		a.fail(w, err)
		return
	}
	if err == nil {
		if r.Header.Get("HX-Request") != "true" {
			redirect(w, r, "/manager/catalog")
			return
		}
		draft = nil
	} else {
		message = ""
		// Never turn a stale version into a fresh edit. Discard that draft and
		// display current data so the manager can review before submitting again.
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrUnavailable) {
			draft = nil
		}
	}
	a.catalogView(w, r, message, err, draft)
}

func catalogPathID(r *http.Request) (int64, error) {
	if r.PathValue("id") == "" {
		return 0, nil
	}
	id, err := num(r.PathValue("id"))
	if err != nil || id < 1 {
		return 0, ErrNotFound
	}
	return id, nil
}

func (a *App) saveCatalogProduct(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	id, err := catalogPathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p := Product{ID: id, SKU: strings.TrimSpace(r.PostForm.Get("sku")), Name: strings.TrimSpace(r.PostForm.Get("name")), Description: strings.TrimSpace(r.PostForm.Get("description")), Icon: r.PostForm.Get("icon"), SaleUnit: r.PostForm.Get("sale_unit")}
	invalid := false
	for key, dest := range map[string]*int64{"category_id": &p.CategoryID, "price": &p.Price, "quantity_step": &p.QuantityStep} {
		value, e := num(r.PostForm.Get(key))
		if e != nil {
			invalid = true
		}
		*dest = value
	}
	if value := r.PostForm.Get("type_id"); value != "" {
		p.TypeID, err = num(value)
		invalid = invalid || err != nil
	}
	if id != 0 {
		p.CatalogVersion, err = num(r.PostForm.Get("catalog_version"))
		invalid = invalid || err != nil || p.CatalogVersion < 1
	}
	if p.SaleUnit == "each" {
		p.PriceBasis = 1
	} else if p.SaleUnit == "g" {
		p.PriceBasis = 1000
	}
	if invalid {
		err = ErrInvalid
	} else {
		_, err = a.store.SaveProduct(p)
	}
	a.catalogResult(w, r, "Product saved. Use inventory adjustments to change its stock.", err, &p)
}

func (a *App) archiveCatalogProduct(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	id, err := catalogPathID(r)
	if err != nil || id == 0 {
		http.NotFound(w, r)
		return
	}
	version, err := num(r.PostForm.Get("catalog_version"))
	if err != nil || version < 1 {
		err = ErrInvalid
	} else {
		err = a.store.ArchiveProduct(id, version)
	}
	a.catalogResult(w, r, "Product archived. Existing receipts and picking tickets are unchanged.", err, nil)
}

func catalogTaxonomyKind(r *http.Request) string {
	switch r.PathValue("kind") {
	case "categories":
		return "category"
	case "types":
		return "type"
	}
	return ""
}

func (a *App) saveCatalogTaxonomy(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	kind := catalogTaxonomyKind(r)
	id, err := catalogPathID(r)
	if kind == "" || err != nil {
		http.NotFound(w, r)
		return
	}
	var version int64
	if id != 0 {
		version, err = num(r.PostForm.Get("version"))
		if err != nil || version < 1 {
			err = ErrInvalid
		}
	}
	if err == nil {
		_, err = a.store.SaveTaxonomy(kind, id, version, r.PostForm.Get("name"))
	}
	a.catalogResult(w, r, "Catalog label saved.", err, nil)
}

func (a *App) archiveCatalogTaxonomy(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	kind := catalogTaxonomyKind(r)
	id, err := catalogPathID(r)
	if kind == "" || err != nil || id == 0 {
		http.NotFound(w, r)
		return
	}
	version, err := num(r.PostForm.Get("version"))
	if err != nil || version < 1 {
		err = ErrInvalid
	} else {
		err = a.store.ArchiveTaxonomy(kind, id, version)
	}
	a.catalogResult(w, r, "Catalog label archived. Its name stays reserved.", err, nil)
}

func (a *App) restoreCatalogTaxonomy(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	kind := catalogTaxonomyKind(r)
	id, err := catalogPathID(r)
	if kind == "" || err != nil || id == 0 {
		http.NotFound(w, r)
		return
	}
	version, err := num(r.PostForm.Get("version"))
	if err != nil || version < 1 {
		err = ErrInvalid
	} else {
		err = a.store.RestoreTaxonomy(kind, id, version)
	}
	a.catalogResult(w, r, "Catalog label restored with its original identity. Archived products remain archived.", err, nil)
}

func (a *App) restoreCatalogProduct(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	id, err := catalogPathID(r)
	if err != nil || id == 0 {
		http.NotFound(w, r)
		return
	}
	version, err := num(r.PostForm.Get("catalog_version"))
	if err != nil || version < 1 {
		err = ErrInvalid
	} else {
		err = a.store.RestoreProduct(id, version)
	}
	a.catalogResult(w, r, "Product restored with its original SKU, stock and identity. Check its shelf details before ordering.", err, nil)
}
