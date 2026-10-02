package shop

import (
	"errors"
	"fmt"
	"net/http"
)

// WeightDraft is display-only submitted data. A failed confirmation never keeps
// an approval token: the manager must preview again against current versions.
type WeightDraft struct {
	LineID                      int64
	Actual, Reason, Disposition string
	Truncated                   bool
}

type WeightReview struct {
	WeightPreview
	AmountChange, StockEffect string
}

func weightDraft(r *http.Request, lineID int64) *WeightDraft {
	d := &WeightDraft{LineID: lineID}
	for _, field := range []struct {
		name  string
		value *string
		limit int
	}{{"actual", &d.Actual, 64}, {"reason", &d.Reason, 500}, {"disposition", &d.Disposition, 32}} {
		value, changed := boundedManagerDraft(r.PostForm.Get(field.name), field.limit)
		*field.value = value
		d.Truncated = d.Truncated || changed
	}
	return d
}

func weightReview(p WeightPreview) *WeightReview {
	r := &WeightReview{WeightPreview: p, AmountChange: Money(p.PriceDelta)}
	if p.PriceDelta > 0 {
		r.AmountChange = "+" + Money(p.PriceDelta)
	} else if p.PriceDelta < 0 {
		r.AmountChange = "−" + Money(-p.PriceDelta)
	}
	switch {
	case p.StockDelta > 0:
		r.StockEffect = fmt.Sprintf("Reserve %d g more from available stock.", p.StockDelta)
	case p.StockDelta < 0 && p.Command.Disposition == "restock":
		r.StockEffect = fmt.Sprintf("Release %d g to available stock: physically returned and resalable.", -p.StockDelta)
	case p.StockDelta < 0:
		r.StockEffect = fmt.Sprintf("Write off %d g: unavailable or damaged; none returns to available stock.", -p.StockDelta)
	default:
		r.StockEffect = "No stock change. The accepted allocation stays the same."
	}
	return r
}

func (a *App) previewWeight(w http.ResponseWriter, r *http.Request) {
	a.weightCommand(w, r, false)
}

func (a *App) confirmWeight(w http.ResponseWriter, r *http.Request) {
	a.weightCommand(w, r, true)
}

func (a *App) weightCommand(w http.ResponseWriter, r *http.Request, confirm bool) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	id, idErr := num(r.PathValue("id"))
	if idErr != nil {
		http.NotFound(w, r)
		return
	}
	// Resolve ownership and the stable line before rendering any submitted data.
	order, err := a.store.ManagerOrder(id, session.ID, !a.config.DemoMode)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	lineID, lineErr := num(r.PathValue("line_id"))
	found := false
	for _, line := range order.WorkingItems {
		if line.LineID == lineID && line.SaleUnit == "g" {
			found = true
		}
	}
	// A committed command can be retried after the line was removed. Confirm's
	// scoped replay check precedes its current-line lookup; previews need a live
	// weighed line because they render a new proposed change.
	if lineErr != nil || lineID < 1 || (!confirm && !found) {
		http.NotFound(w, r)
		return
	}
	c := WeightCommand{LineID: lineID, Key: r.PostForm.Get("command_key"), Reason: r.PostForm.Get("reason"), Disposition: r.PostForm.Get("disposition"), Review: r.PostForm.Get("review")}
	for name, target := range map[string]*int64{"actual": &c.Actual, "pick_version": &c.PickVersion, "order_version": &c.OrderVersion} {
		value, parseErr := num(r.PostForm.Get(name))
		*target = value
		if parseErr != nil {
			err = ErrInvalid
		}
	}
	var review *WeightReview
	if err == nil {
		if confirm {
			err = a.store.ConfirmWeight(id, session.ID, !a.config.DemoMode, c)
		} else {
			var preview WeightPreview
			preview, err = a.store.PreviewWeight(id, session.ID, !a.config.DemoMode, c)
			if err == nil {
				review = weightReview(preview)
			}
		}
	}
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil && !orderCommandProblem(err) {
		a.fail(w, err)
		return
	}
	if err == nil && confirm && r.Header.Get("HX-Request") != "true" {
		redirect(w, r, orderFilters(r).URL(id))
		return
	}
	message := ""
	var draft *WeightDraft
	if err != nil || !confirm {
		draft = weightDraft(r, lineID)
	} else {
		message = "Actual weight confirmed. Allocation, working amount and stock are saved together."
	}
	a.pickingWeightView(w, r, id, message, err, draft, review)
}
