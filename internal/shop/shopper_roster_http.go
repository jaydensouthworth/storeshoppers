package shop

import (
	"errors"
	"net/http"
	"strconv"
)

func shopperRosterDraft(r *http.Request) *ShopperRosterDraft {
	d := &ShopperRosterDraft{}
	for _, field := range []struct {
		key   string
		dst   *string
		limit int
	}{{"action", &d.Action, 20}, {"name", &d.Name, 80}, {"initials", &d.Initials, 4}, {"availability", &d.Availability, 40}, {"capacity", &d.Capacity, 64}} {
		value, truncated := boundedManagerDraft(r.PostForm.Get(field.key), field.limit)
		*field.dst = value
		d.Truncated = d.Truncated || truncated
	}
	return d
}
func (a *App) createShopperRoster(w http.ResponseWriter, r *http.Request) {
	a.saveShopperRoster(w, r, true)
}
func (a *App) changeShopperRoster(w http.ResponseWriter, r *http.Request) {
	a.saveShopperRoster(w, r, false)
}
func (a *App) saveShopperRoster(w http.ResponseWriter, r *http.Request, create bool) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	var id int64
	var err error
	if !create {
		id, err = num(r.PathValue("id"))
		if err != nil || id < 1 {
			http.NotFound(w, r)
			return
		}
		if _, err = a.store.ShopperRosterProfile(id, session.ID, !a.config.DemoMode); errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			a.fail(w, err)
			return
		}
	}
	// Editor identity is derived from this authorized route, never a draft field.
	r.PostForm.Del("person")
	r.PostForm.Del("new")
	if create {
		r.PostForm.Set("new", "1")
	} else {
		r.PostForm.Set("person", strconv.FormatInt(id, 10))
	}
	c := ShopperRosterCommand{Action: r.PostForm.Get("action"), Key: r.PostForm.Get("command_key"), Name: r.PostForm.Get("name"), Initials: r.PostForm.Get("initials"), Availability: r.PostForm.Get("availability")}
	if create {
		if c.Action != "create" {
			err = ErrInvalid
		} else if raw := r.PostForm.Get("version"); raw != "" {
			c.Version, err = num(raw)
		}
	} else {
		c.Version, err = num(r.PostForm.Get("version"))
	}
	if err == nil && (c.Action == "create" || c.Action == "edit") {
		if create && r.PostForm.Get("capacity") == "" {
			c.Capacity = 3
		} else {
			c.Capacity, err = num(r.PostForm.Get("capacity"))
		}
	}
	if err != nil {
		err = ErrInvalid
	} else {
		id, err = a.store.SaveShopperRoster(id, session.ID, !a.config.DemoMode, c)
	}
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil && !shopperRosterProblem(err) {
		a.fail(w, err)
		return
	}
	if err == nil {
		r.PostForm.Del("new")
		if c.Action == "archive" {
			r.PostForm.Del("person")
		} else {
			r.PostForm.Set("person", strconv.FormatInt(id, 10))
		}
		if r.Header.Get("HX-Request") != "true" {
			redirect(w, r, shopperRequestFilters(r).EditorURL(0))
			return
		}
	}
	message := ""
	if err == nil {
		message = map[string]string{"create": "Simulated shopper created.", "edit": "Shopper profile saved. Existing assignments and recorded picking progress remain unchanged.", "archive": "Shopper archived. Assignment history is preserved; restore this profile to use it again.", "restore": "Shopper restored. Review availability and capacity before assigning new work."}[c.Action]
	}
	a.shoppersView(w, r, 0, message, err)
}
