package shop

import (
	"errors"
	"net/http"
)

func stockRequestFilters(r *http.Request) StockFilters {
	if r.Method == http.MethodPost {
		f := parseStockFilters(r.PostForm)
		f.View = ""
		return f
	}
	return parseStockFilters(r.URL.Query())
}
func (a *App) showStock(w http.ResponseWriter, r *http.Request) { a.stockView(w, r, "", nil) }
func (a *App) stockView(w http.ResponseWriter, r *http.Request, message string, problem error) {
	v, ok := a.view(w, r)
	if !ok || !a.guard(w, r, v.Session) {
		return
	}
	v.Title, v.Section, v.ManagerTab = "Stock & Activity", "manager", "stock"
	v.Message = message
	f := stockRequestFilters(r)
	if problem != nil {
		v.Error = problem.Error()
		v.StockDraft = managerDraft(r, "stock")
	}
	var err error
	workspace := &StockWorkspace{Filters: f}
	if workspace.Inventory, err = a.store.StockInventory(f); err != nil {
		a.fail(w, err)
		return
	}
	workspace.Filters.Page = workspace.Inventory.Page
	if workspace.Summary, err = a.store.StockSummary(); err != nil {
		a.fail(w, err)
		return
	}
	if v.Categories, err = a.store.Taxonomies("category", true); err != nil {
		a.fail(w, err)
		return
	}
	selected := f.Product
	if v.StockDraft != nil {
		selected = v.StockDraft.ProductID
	}
	if selected == 0 && r.Method == http.MethodPost {
		selected, _ = num(r.PostForm.Get("product_id"))
	}
	if selected > 0 {
		workspace.Selected, err = a.store.stockProduct(selected)
		if errors.Is(err, ErrNotFound) && problem != nil {
			workspace.Selected = nil
		} else if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			a.fail(w, err)
			return
		}
	} else if r.Method == http.MethodGet && r.URL.Query().Get("product") != "" {
		http.NotFound(w, r)
		return
	}
	if f.View == "activity" {
		if workspace.Choices, err = a.store.stockChoices(); err != nil {
			a.fail(w, err)
			return
		}
		if workspace.Activity, err = a.store.StockActivity(f, v.Session.ID, !a.config.DemoMode); err != nil {
			a.fail(w, err)
			return
		}
		workspace.Filters.ActivityPage = workspace.Activity.Page
		if f.DateError != "" {
			v.Error = f.DateError
		}
	} else {
		recent, err := a.store.StockActivity(StockFilters{Action: "stock", ActivityPage: 1}, v.Session.ID, !a.config.DemoMode)
		if err != nil {
			a.fail(w, err)
			return
		}
		workspace.Recent = recent.Events
		if len(workspace.Recent) > 3 {
			workspace.Recent = workspace.Recent[:3]
		}
	}
	v.StockWorkspace = workspace
	v.Search = f.Search
	a.render(w, r, v, http.StatusOK)
}
func (a *App) inventory(w http.ResponseWriter, r *http.Request) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	pid, e1 := num(r.PostForm.Get("product_id"))
	delta, e2 := num(r.PostForm.Get("delta"))
	version, e3 := num(r.PostForm.Get("version"))
	var err error
	if e1 != nil || e2 != nil || e3 != nil {
		err = ErrInvalid
	} else {
		err = a.store.Adjust(pid, delta, version, r.PostForm.Get("reason"))
	}
	if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrNotFound) {
		a.fail(w, err)
		return
	}
	f := stockRequestFilters(r)
	f.View = ""
	// The return location is assembled from validated parameters, never a submitted URL.
	if err == nil && r.Header.Get("HX-Request") != "true" {
		redirect(w, r, f.URL())
		return
	}
	message := ""
	if err == nil {
		message = "Inventory adjusted. The change is recorded below."
		w.Header().Set("HX-Push-Url", f.URL())
	}
	a.stockView(w, r, message, err)
}
