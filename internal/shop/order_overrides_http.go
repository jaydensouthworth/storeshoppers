package shop

import (
	"errors"
	"fmt"
	"net/http"
)

func orderCommandProblem(err error) bool {
	return basketProblem(err) || errors.Is(err, ErrTerminal) || errors.Is(err, ErrStockCapacity) || errors.Is(err, ErrOrderQuote) || errors.Is(err, ErrUseReady) || errors.Is(err, ErrPickedDisposition)
}
func (a *App) overrideOrder(w http.ResponseWriter, r *http.Request) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	id, err := num(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Check scope before rendering any validation error or repopulating a form.
	if _, err = a.store.Order(id, session.ID, !a.config.DemoMode); errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.fail(w, err)
		return
	}
	c := OrderCommand{CatalogQuote: r.PostForm.Get("catalog_quote"), Action: r.PostForm.Get("action"), Key: r.PostForm.Get("command_key"), Reason: r.PostForm.Get("reason"), Disposition: r.PostForm.Get("disposition"), Remainder: r.PostForm.Get("remainder")}
	c.Version, err = num(r.PostForm.Get("version"))
	for name, target := range map[string]*int64{"product_id": &c.ProductID, "replacement_id": &c.ReplacementID, "quantity": &c.Quantity} {
		if raw := r.PostForm.Get(name); raw != "" {
			v, e := num(raw)
			if e != nil {
				err = ErrInvalid
			}
			*target = v
		}
	}
	if err != nil {
		err = ErrInvalid
	} else {
		err = a.store.OverrideOrder(id, session.ID, !a.config.DemoMode, c)
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
		redirect(w, r, fmt.Sprintf("/manager/orders/%d", id)+searchQuery(managerSearch(r)))
		return
	}
	message := ""
	if err == nil {
		message = "Manager change saved. The original placed receipt is unchanged."
	}
	a.pickingView(w, r, id, message, err)
}

func (a *App) recordWorkingLinePicked(w http.ResponseWriter, r *http.Request) {
	// The original product-keyed route remains product-keyed; a separate stable-ID
	// route prevents old open forms from accidentally targeting a different item.
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	id, e1 := num(r.PathValue("id"))
	lineID, e2 := num(r.PathValue("line_id"))
	picked, e3 := num(r.PostForm.Get("picked"))
	version, e4 := num(r.PostForm.Get("version"))
	if e1 != nil {
		http.NotFound(w, r)
		return
	}
	order, err := a.store.Order(id, session.ID, !a.config.DemoMode)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	var pid int64
	for _, line := range order.WorkingItems {
		if line.LineID == lineID {
			pid = line.ProductID
		}
	}
	newEnvelope := r.PostForm.Get("command_key") != "" || r.PostForm.Get("order_version") != ""
	if e2 != nil || (pid == 0 && !newEnvelope) {
		http.NotFound(w, r)
		return
	}
	if e3 != nil || e4 != nil {
		err = ErrInvalid
	} else if newEnvelope {
		orderVersion, parseErr := num(r.PostForm.Get("order_version"))
		if parseErr != nil {
			err = ErrInvalid
		} else {
			err = a.store.MarkWorkingLinePicked(id, lineID, picked, version, orderVersion, r.PostForm.Get("command_key"), session.ID, !a.config.DemoMode)
		}
	} else {
		err = a.store.RecordPicked(id, pid, picked, version, session.ID, !a.config.DemoMode)
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
		redirect(w, r, fmt.Sprintf("/manager/orders/%d", id)+searchQuery(managerSearch(r)))
		return
	}
	message := ""
	if err == nil {
		message = "Picked quantity saved."
	}
	a.pickingView(w, r, id, message, err)
}
