package shop

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type PromotionDraft struct {
	ProductRef, SalePrice, Starts, Ends string
	Version                             int64
}
type PromotionWorkspace struct {
	HasDraft, HasFeatureDraft, FeatureChecked          bool
	Mode, Search, State, ListURL, PreviousURL, NextURL string
	Page, Pages, Total, First, Last                    int
	Rows                                               []Promotion
	Products, FeatureProducts                          []Product
	Selected                                           *Promotion
	Feature                                            *Product
	Draft                                              PromotionDraft
	Events                                             []PromotionEvent
	Examples                                           []Promotion
	ExampleQuote, CommandKey, ExampleError             string
}

func (p Product) PromotionRef() string { return fmt.Sprintf("%d:%d", p.ID, p.PriceVersion) }
func promotionContext(r *http.Request) (string, string, int) {
	v := r.URL.Query()
	if r.Method == http.MethodPost {
		v = r.PostForm
	}
	search, _ := boundedManagerDraft(strings.TrimSpace(v.Get("q")), 100)
	state := stockChoice(v.Get("state"), "active", "upcoming", "expired", "cancelled")
	if strings.HasPrefix(r.URL.Path, "/manager/featured") {
		state = stockChoice(v.Get("state"), "featured", "unfeatured")
	}
	return search, state, stockPageNumber(v.Get("page"))
}
func promotionURL(path, search, state string, page int) string {
	v := url.Values{}
	if search != "" {
		v.Set("q", search)
	}
	if state != "" {
		v.Set("state", state)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) > 0 {
		return path + "?" + v.Encode()
	}
	return path
}
func promotionProblem(err error) bool {
	return errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) || errors.Is(err, ErrPromotionOverlap) || errors.Is(err, ErrPromotionUnit) || errors.Is(err, ErrPromotionPrice) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrExampleSales)
}
func (a *App) showPromotions(w http.ResponseWriter, r *http.Request) {
	a.promotionsView(w, r, "", nil, nil)
}
func (a *App) promotionsView(w http.ResponseWriter, r *http.Request, message string, problem error, draft *PromotionDraft) {
	v, ok := a.view(w, r)
	if !ok || !a.guard(w, r, v.Session) {
		return
	}
	feature := strings.HasPrefix(r.URL.Path, "/manager/featured")
	examples := r.URL.Path == "/manager/promotions/examples"
	if examples && !a.config.DemoMode {
		http.NotFound(w, r)
		return
	}
	base := "/manager/promotions"
	v.Section = "promotions"
	v.Title = "Promotions"
	if feature {
		base = "/manager/featured"
		v.Section = "featured"
		v.Title = "Featured products"
	}
	ws := &PromotionWorkspace{Mode: "list", CommandKey: token()}
	ws.Search, ws.State, ws.Page = promotionContext(r)
	ws.ListURL = promotionURL(base, ws.Search, ws.State, ws.Page)
	products, err := a.store.CatalogProducts()
	if err != nil {
		a.fail(w, err)
		return
	}
	for _, p := range products {
		if !p.Archived {
			ws.Products = append(ws.Products, p)
		}
	}
	ws.Events, err = a.store.PromotionEvents()
	if err != nil {
		a.fail(w, err)
		return
	}
	if feature {
		ws.Mode = "featured"
		edit := r.URL.Query().Get("edit")
		if r.Method == http.MethodPost {
			edit = r.PathValue("id")
		}
		if edit != "" {
			id, e := num(edit)
			if e != nil || id < 1 {
				http.NotFound(w, r)
				return
			}
			for _, p := range products {
				if p.ID == id {
					copy := p
					ws.Feature = &copy
					break
				}
			}
			if ws.Feature == nil {
				http.NotFound(w, r)
				return
			}
			ws.FeatureChecked = ws.Feature.Featured
			if r.Method == http.MethodPost && problem != nil {
				ws.HasFeatureDraft = true
				ws.FeatureChecked = r.PostForm.Get("featured") == "1"
			}
		}
		for _, p := range products {
			if ws.State == "featured" && !p.Featured || ws.State == "unfeatured" && p.Featured {
				continue
			}
			if ws.Search != "" && !strings.Contains(strings.ToLower(p.Name+" "+p.SKU), strings.ToLower(ws.Search)) {
				continue
			}
			ws.FeatureProducts = append(ws.FeatureProducts, p)
		}
		ws.Total = len(ws.FeatureProducts)
		ws.Page, ws.Pages, ws.First, ws.Last = stockBounds(ws.Total, ws.Page, 20)
		if ws.Total > 0 {
			ws.FeatureProducts = ws.FeatureProducts[ws.First-1 : ws.Last]
		}
	} else if examples {
		ws.Mode = "examples"
		v.Title = "Example weekly sales"
		ws.Examples, ws.ExampleQuote, err = a.store.ExampleSales()
		if err != nil {
			if !promotionProblem(err) {
				a.fail(w, err)
				return
			}
			ws.ExampleError = err.Error()
		}
		if r.Method == http.MethodPost && problem != nil {
			ws.CommandKey = r.PostForm.Get("command_key")
		}
	} else {
		all, e := a.store.Promotions()
		if e != nil {
			a.fail(w, e)
			return
		}
		edit := r.PathValue("id")
		if strings.HasSuffix(r.URL.Path, "/new") || (r.Method == http.MethodPost && r.URL.Path == "/manager/promotions" && edit == "") {
			ws.Mode = "new"
			v.Title = "Create sale"
			now := a.store.now().UTC().Truncate(time.Minute)
			ws.Draft.Starts = now.Format("2006-01-02T15:04")
			ws.Draft.Ends = now.AddDate(0, 0, 7).Format("2006-01-02T15:04")
			if len(ws.Products) > 0 {
				ws.Draft.ProductRef = ws.Products[0].PromotionRef()
			}
		} else if edit != "" {
			id, e := num(edit)
			if e != nil || id < 1 {
				http.NotFound(w, r)
				return
			}
			for _, p := range all {
				if p.ID == id {
					copy := p
					ws.Selected = &copy
					break
				}
			}
			if ws.Selected == nil {
				http.NotFound(w, r)
				return
			}
			ws.Mode = "edit"
			v.Title = "Edit sale"
			p := ws.Selected
			ws.Draft = PromotionDraft{ProductRef: fmt.Sprintf("%d:%d", p.ProductID, p.ProductVersion), SalePrice: fmt.Sprintf("%d.%02d", p.SalePrice/100, p.SalePrice%100), Starts: p.StartInput(), Ends: p.EndInput(), Version: p.Version}
		}
		if draft != nil {
			ws.Draft = *draft
			ws.HasDraft = true
			if ws.Selected != nil {
				ws.Draft.Version = ws.Selected.Version
				ws.Draft.ProductRef = fmt.Sprintf("%d:%d", ws.Selected.ProductID, ws.Selected.ProductVersion)
			}
		}
		for _, p := range all {
			if ws.State != "" && !strings.EqualFold(ws.State, p.Status) {
				continue
			}
			if ws.Search != "" && !strings.Contains(strings.ToLower(p.ProductName+" "+p.SKU), strings.ToLower(ws.Search)) {
				continue
			}
			ws.Rows = append(ws.Rows, p)
		}
		ws.Total = len(ws.Rows)
		ws.Page, ws.Pages, ws.First, ws.Last = stockBounds(ws.Total, ws.Page, 20)
		if ws.Total > 0 {
			ws.Rows = ws.Rows[ws.First-1 : ws.Last]
		}
	}
	ws.ListURL = promotionURL(base, ws.Search, ws.State, ws.Page)
	if ws.Page > 1 {
		ws.PreviousURL = promotionURL(base, ws.Search, ws.State, ws.Page-1)
	}
	if ws.Page < ws.Pages {
		ws.NextURL = promotionURL(base, ws.Search, ws.State, ws.Page+1)
	}
	v.Promotions = ws
	v.Message = message
	if problem != nil {
		v.Error = problem.Error()
	}
	a.render(w, r, v, http.StatusOK)
}
func (a *App) promotionResult(w http.ResponseWriter, r *http.Request, message string, err error, draft *PromotionDraft) {
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil && !promotionProblem(err) {
		a.fail(w, err)
		return
	}
	if err == nil {
		q, state, page := promotionContext(r)
		path := "/manager/promotions"
		if strings.HasPrefix(r.URL.Path, "/manager/featured") {
			path = "/manager/featured"
		} else if id := r.PathValue("id"); id != "" {
			path += "/" + id
		}
		destination := promotionURL(path, q, state, page)
		if strings.HasPrefix(r.URL.Path, "/manager/featured") && r.PathValue("id") != "" {
			sep := "?"
			if strings.Contains(destination, "?") {
				sep = "&"
			}
			destination += sep + "edit=" + url.QueryEscape(r.PathValue("id"))
		}
		redirect(w, r, destination)
		return
	}

	a.promotionsView(w, r, "", err, draft)
}
func parsePromotionDraft(r *http.Request) (Promotion, PromotionDraft, error) {
	p := Promotion{}
	d := PromotionDraft{}
	for key, dst := range map[string]*string{"product_ref": &d.ProductRef, "sale_price_usd": &d.SalePrice, "starts": &d.Starts, "ends": &d.Ends} {
		*dst, _ = boundedManagerDraft(r.PostForm.Get(key), 80)
	}
	invalid := false
	parts := strings.Split(d.ProductRef, ":")
	if len(parts) != 2 {
		invalid = true
	} else {
		var e error
		p.ProductID, e = num(parts[0])
		invalid = invalid || e != nil
		p.ProductVersion, e = num(parts[1])
		invalid = invalid || e != nil
	}
	var err error
	p.SalePrice, err = parseSaleUSD(d.SalePrice)
	invalid = invalid || err != nil
	if raw := r.PostForm.Get("version"); raw != "" {
		p.Version, err = num(raw)
		invalid = invalid || err != nil
	}
	d.Version = p.Version
	if raw := r.PathValue("id"); raw != "" {
		p.ID, err = num(raw)
		if err != nil || p.ID < 1 {
			return p, d, ErrNotFound
		}
	}
	for _, f := range []struct {
		raw string
		dst *int64
	}{{d.Starts, &p.Starts}, {d.Ends, &p.Ends}} {
		t, e := time.Parse("2006-01-02T15:04", f.raw)
		if e != nil {
			invalid = true
		} else {
			*f.dst = t.Unix()
		}
	}
	if invalid {
		return p, d, ErrInvalid
	}
	return p, d, nil
}
func (a *App) savePromotion(w http.ResponseWriter, r *http.Request) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	p, d, err := parsePromotionDraft(r)
	if err == nil {
		var id int64
		id, err = a.store.SavePromotion(p)
		if err == nil {
			r.SetPathValue("id", fmt.Sprint(id))
		}
	}
	a.promotionResult(w, r, "Sale saved.", err, &d)
}
func (a *App) cancelPromotion(w http.ResponseWriter, r *http.Request) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	id, e1 := num(r.PathValue("id"))
	version, e2 := num(r.PostForm.Get("version"))
	err := error(nil)
	if e1 != nil || e2 != nil {
		err = ErrInvalid
	} else {
		err = a.store.CancelPromotion(id, version)
	}
	a.promotionResult(w, r, "Sale cancelled.", err, nil)
}
func (a *App) setFeatured(w http.ResponseWriter, r *http.Request) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	id, e1 := num(r.PathValue("id"))
	version, e2 := num(r.PostForm.Get("feature_version"))
	err := error(nil)
	if e1 != nil || e2 != nil || (r.PostForm.Get("featured") != "" && r.PostForm.Get("featured") != "1") {
		err = ErrInvalid
	} else {
		err = a.store.SetFeatured(id, version, r.PostForm.Get("featured") == "1")
	}
	a.promotionResult(w, r, "Featured choice saved.", err, nil)
}
func (a *App) createExampleSales(w http.ResponseWriter, r *http.Request) {
	if !a.config.DemoMode {
		http.NotFound(w, r)
		return
	}
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	err := error(nil)
	if r.PostForm.Get("confirm") != "1" {
		err = ErrInvalid
	} else {
		err = a.store.CreateExampleSales(r.PostForm.Get("command_key"), r.PostForm.Get("quote"))
	}
	a.promotionResult(w, r, "Example weekly sales created.", err, nil)
}

// parseSaleUSD uses decimal digits only; floats, signs, exponent notation and
// extra precision are never rounded into a different reviewed sale price.
func parseSaleUSD(raw string) (int64, error) {
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 5 {
		return 0, ErrInvalid
	}
	for _, part := range parts {
		if part == "" {
			return 0, ErrInvalid
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, ErrInvalid
			}
		}
	}
	whole, err := num(parts[0])
	if err != nil {
		return 0, ErrInvalid
	}
	cents := whole * 100
	if len(parts) == 2 {
		if len(parts[1]) > 2 {
			return 0, ErrInvalid
		}
		fraction, _ := num(parts[1])
		if len(parts[1]) == 1 {
			fraction *= 10
		}
		cents += fraction
	}
	if cents < 1 || cents > 1000000 {
		return 0, ErrInvalid
	}
	return cents, nil
}
