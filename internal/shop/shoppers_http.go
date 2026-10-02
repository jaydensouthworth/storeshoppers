package shop

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type ShopperFilters struct {
	Query, Status, ShopperFilter string
	ShopperID                    int64
	Page                         int
}

func parseShopperFilters(values url.Values) ShopperFilters {
	f := ShopperFilters{Status: "open", Page: 1}
	f.Query, _ = boundedManagerDraft(strings.TrimSpace(values.Get("q")), 100)
	switch values.Get("status") {
	case "open", "active", "unassigned", "closed", "all":
		f.Status = values.Get("status")
	}
	if id, err := num(values.Get("shopper")); err == nil && id > 0 {
		f.ShopperID = id
		f.ShopperFilter = strconv.FormatInt(id, 10)
	}
	if page, err := strconv.Atoi(values.Get("page")); err == nil && page > 0 && page <= 1000000 {
		f.Page = page
	}
	return f
}
func (f ShopperFilters) Values() url.Values {
	v := url.Values{"status": {f.Status}}
	if f.Query != "" {
		v.Set("q", f.Query)
	}
	if f.ShopperFilter != "" {
		v.Set("shopper", f.ShopperFilter)
	}
	if f.Page > 1 {
		v.Set("page", strconv.Itoa(f.Page))
	}
	return v
}
func (f ShopperFilters) URL(selected int64) string {
	v := f.Values()
	if selected > 0 {
		v.Set("order", strconv.FormatInt(selected, 10))
	}
	return "/manager/shoppers?" + v.Encode()
}
func shopperRequestFilters(r *http.Request) ShopperFilters {
	if r.Method == http.MethodPost {
		return parseShopperFilters(r.PostForm)
	}
	return parseShopperFilters(r.URL.Query())
}
func (a *App) showShoppers(w http.ResponseWriter, r *http.Request) {
	var selected int64
	if raw := r.URL.Query().Get("order"); raw != "" {
		var err error
		selected, err = num(raw)
		if err != nil || selected < 1 {
			http.NotFound(w, r)
			return
		}
	}
	a.shoppersView(w, r, selected, "", nil)
}
func (a *App) shoppersView(w http.ResponseWriter, r *http.Request, selected int64, message string, problem error) {
	v, ok := a.view(w, r)
	if !ok || !a.guard(w, r, v.Session) {
		return
	}
	f := shopperRequestFilters(r)
	workspace, err := a.store.Shoppers(f, v.Session.ID, !a.config.DemoMode, selected)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if problem != nil {
		v.Error = problem.Error()
		d := &ShopperDraft{}
		for _, field := range []struct {
			key   string
			dst   *string
			limit int
		}{{"action", &d.Action, 20}, {"shopper_id", &d.ShopperID, 64}, {"reason", &d.Reason, 240}} {
			value, truncated := boundedManagerDraft(r.PostForm.Get(field.key), field.limit)
			*field.dst = value
			d.Truncated = d.Truncated || truncated
		}
		workspace.Draft = d
	}
	v.Title, v.Section, v.ManagerTab = "Shoppers", "shoppers", "shoppers"
	v.Shoppers = workspace
	v.Search = f.Query
	v.Message = message
	if r.Method == http.MethodPost && r.Header.Get("HX-Request") == "true" {
		f.Page = workspace.Page
		w.Header().Set("HX-Push-Url", f.URL(selected))
	}
	a.render(w, r, v, http.StatusOK)
}
func (a *App) changeShopper(w http.ResponseWriter, r *http.Request) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	id, err := num(r.PathValue("id"))
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	// Scope precedes parsing, stale errors and draft recovery.
	if _, err = a.store.Order(id, session.ID, !a.config.DemoMode); errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.fail(w, err)
		return
	}
	c := ShopperCommand{Action: r.PostForm.Get("action"), Key: r.PostForm.Get("command_key"), Reason: r.PostForm.Get("reason")}
	c.Version, err = num(r.PostForm.Get("version"))
	var e2, e3 error
	c.AssignmentVersion, e2 = num(r.PostForm.Get("assignment_version"))
	if c.Action != "cancel" {
		c.ShopperID, e3 = num(r.PostForm.Get("shopper_id"))
	}
	if err != nil || e2 != nil || e3 != nil {
		err = ErrInvalid
	} else {
		err = a.store.AssignShopper(id, session.ID, !a.config.DemoMode, c)
	}
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil && !orderCommandProblem(err) {
		a.fail(w, err)
		return
	}
	if err == nil && r.Header.Get("HX-Request") != "true" {
		redirect(w, r, shopperRequestFilters(r).URL(id))
		return
	}
	message := ""
	if err == nil {
		message = "Shopper assignment saved. Recorded picking progress is unchanged."
		if c.Action == "cancel" {
			message = "Task cancellation recorded. Review the current order and assignment below. Cancelling a task leaves stock and picked quantities unchanged."
		}
	}
	a.shoppersView(w, r, id, message, err)
}
