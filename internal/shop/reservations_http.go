package shop

import (
	"errors"
	"net/http"
)

func basketProblem(err error) bool {
	return errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) || errors.Is(err, ErrStock) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrEmpty) || errors.Is(err, ErrHold) || errors.Is(err, ErrZeroEstimate)
}
func (a *App) renewCart(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok {
		return
	}
	revision, err := num(r.PostForm.Get("revision"))
	if err != nil {
		err = ErrInvalid
	} else {
		err = a.store.RenewBasket(s.ID, revision)
	}
	if err == nil {
		redirect(w, r, "/cart")
		return
	}
	if !basketProblem(err) {
		a.fail(w, err)
		return
	}
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	v.Title = "Review your basket"
	v.Section = "cart"
	v.Error = err.Error()
	a.render(w, r, v, 200)
}
func (a *App) showBaskets(w http.ResponseWriter, r *http.Request) { a.basketsView(w, r, "", nil) }
func (a *App) showBasket(w http.ResponseWriter, r *http.Request)  { a.basketsView(w, r, "", nil) }
func (a *App) basketsView(w http.ResponseWriter, r *http.Request, message string, problem error) {
	v, ok := a.view(w, r)
	if !ok || !a.guard(w, r, v.Session) {
		return
	}
	id := r.PathValue("id")
	var err error
	v.ManagerTab = "baskets"
	v.Title = "Current baskets"
	v.Section = "baskets"
	v.Message = message
	if problem != nil {
		v.Error = problem.Error()
		if errors.Is(problem, ErrConflict) {
			w.Header().Set("X-Shop-Error", "stale-version")
		}
	}
	if id != "" {
		v.Section = "basket"
		v.ManagedBasket, err = a.store.ManagedBasket(id, v.Session.ID, !a.config.DemoMode)
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			a.fail(w, err)
			return
		}
		if problem != nil && r.Method == http.MethodPost {
			v.BasketDraft = basketFormDraft(r, v.ManagedBasket)
		}
		v.Title = v.ManagedBasket.Label
		if v.Products, err = a.store.Products("", ""); err != nil {
			a.fail(w, err)
			return
		}
	} else {
		if v.Baskets, err = a.store.Baskets(v.Session.ID, !a.config.DemoMode); err != nil {
			a.fail(w, err)
			return
		}
	}
	if v.BasketEvents, err = a.store.BasketEvents(id, v.Session.ID, !a.config.DemoMode); err != nil {
		a.fail(w, err)
		return
	}
	if id != "" {
		if a.handleProductPickerError(w, r, a.populateProductPicker(r, &v, true)) {
			return
		}
		if a.renderProductPicker(w, r, v) {
			return
		}
	}
	a.render(w, r, v, 200)
}
func (a *App) createPracticeBaskets(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	if err := a.store.CreatePracticeBaskets(s.ID); err != nil {
		a.fail(w, err)
		return
	}
	redirect(w, r, "/manager/baskets")
}
func (a *App) managerChangeBasket(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	pid, e1 := num(r.PostForm.Get("product_id"))
	selectedPickerQuantity(r, pid)
	qty, e2 := num(r.PostForm.Get("quantity"))
	revision, e3 := num(r.PostForm.Get("revision"))
	var err error
	if e1 != nil || e2 != nil || e3 != nil {
		err = ErrInvalid
	} else {
		err = a.store.ManagerSetBasket(r.PathValue("id"), s.ID, !a.config.DemoMode, pid, qty, revision, r.PostForm.Get("reason"))
	}
	a.basketCommandResult(w, r, err)
}
func (a *App) managerRenewBasket(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	revision, err := num(r.PostForm.Get("revision"))
	if err != nil {
		err = ErrInvalid
	} else {
		err = a.store.ManagerRenewBasket(r.PathValue("id"), s.ID, !a.config.DemoMode, revision, r.PostForm.Get("reason"))
	}
	a.basketCommandResult(w, r, err)
}
func (a *App) basketCommandResult(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil && !basketProblem(err) {
		a.fail(w, err)
		return
	}
	if err == nil && r.Header.Get("HX-Request") != "true" {
		redirect(w, r, "/manager/baskets/"+r.PathValue("id"))
		return
	}
	message := ""
	if err == nil {
		message = "Basket updated. The manager change is recorded below."
	}
	a.basketsView(w, r, message, err)
}
