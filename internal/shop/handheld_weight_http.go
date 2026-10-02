package shop

import (
	"errors"
	"fmt"
	"net/http"
)

// Display-only fields survive a rejected request, never its stale approval.
type HandheldWeightDraft struct{ Actual, Disposition, Note string }
type HandheldReportDraft struct{ Kind, Note string }
type HandheldWeightReview struct {
	HandheldWeightPreview
	AmountChange, StockEffect string
}

func handheldWeightDraft(r *http.Request) *HandheldWeightDraft {
	d := &HandheldWeightDraft{}
	for _, f := range []struct {
		name  string
		value *string
		limit int
	}{{"actual", &d.Actual, 64}, {"disposition", &d.Disposition, 32}, {"note", &d.Note, 500}} {
		*f.value, _ = boundedManagerDraft(r.PostForm.Get(f.name), f.limit)
	}
	return d
}
func handheldWeightReview(p HandheldWeightPreview) *HandheldWeightReview {
	wording := weightReview(WeightPreview{Command: WeightCommand{Disposition: p.Command.Disposition}, PriceDelta: p.PriceDelta, StockDelta: p.StockDelta})
	return &HandheldWeightReview{HandheldWeightPreview: p, AmountChange: wording.AmountChange, StockEffect: wording.StockEffect}
}
func (a *App) previewHandheldWeight(w http.ResponseWriter, r *http.Request) {
	a.handheldWeightCommand(w, r, false)
}
func (a *App) confirmHandheldWeight(w http.ResponseWriter, r *http.Request) {
	a.handheldWeightCommand(w, r, true)
}
func (a *App) handheldWeightCommand(w http.ResponseWriter, r *http.Request, confirm bool) {
	if !a.handheldForm(w, r) {
		return
	}
	c := HandheldWeight{HandheldScan: handheldScanForm(r), Key: r.PostForm.Get("command_key"), Disposition: r.PostForm.Get("disposition"), Review: r.PostForm.Get("review"), Note: r.PostForm.Get("note")}
	v := handheldDraft(r)
	v.WeightDraft = handheldWeightDraft(r)
	v.RecoveringWeight = true
	var err error
	c.Actual, err = num(r.PostForm.Get("actual"))
	if err != nil {
		err = ErrInvalid
	}
	if err == nil && !confirm {
		// This explicit preview establishes a new intent. Do not change the key
		// after hashing the review, and never silently replay an ambiguous confirm.
		c.Key = token()
		var p HandheldWeightPreview
		p, err = a.store.PreviewHandheldWeight(handheldCookieValue(r, handheldCookie), r.PostForm.Get("csrf"), c)
		if err == nil {
			v.WeightReview = handheldWeightReview(p)
			v.Recognition = &p.Recognition
			v.CommandKey = p.Command.Key
			v.CodeDraft = p.Command.Code
			v.SourceDraft = p.Command.Source
			v.FormatDraft = p.Command.Format
			v.RecoveringWeight = false
		}
	} else if err == nil {
		var saved HandheldWeightResult
		saved, err = a.store.ConfirmHandheldWeight(handheldCookieValue(r, handheldCookie), r.PostForm.Get("csrf"), c)
		if err == nil {
			v = HandheldView{Message: fmt.Sprintf("Saved %d g as the actual measurement. Allocation, stock and the working amount were updated together. Current saved state is shown below.", saved.Actual)}
			if saved.Replayed {
				v.Message = fmt.Sprintf("This confirmation already saved %d g. No duplicate change was made; current saved state is shown below.", saved.Actual)
			}
		}
	}
	if err != nil {
		if !handheldProblem(err) && !orderCommandProblem(err) && !errors.Is(err, ErrNotFound) {
			a.fail(w, err)
			return
		}
		v.Error = err.Error()
		if errors.Is(err, ErrNotFound) {
			v.Error = "That item is no longer available on this task. Review the current pick list."
		}
	}
	a.handheldPage(w, r, v, c.LineID)
}

func (a *App) reportHandheldItem(w http.ResponseWriter, r *http.Request) {
	if !a.handheldForm(w, r) {
		return
	}
	scan := handheldScanForm(r) // Version parsing only; absent-item reports need no scan.
	c := HandheldReport{LineID: scan.LineID, AssignmentVersion: scan.AssignmentVersion, Version: scan.Version, PickVersion: scan.PickVersion, Kind: r.PostForm.Get("kind"), Note: r.PostForm.Get("note"), Key: r.PostForm.Get("command_key")}
	v := HandheldView{ReportDraft: &HandheldReportDraft{}}
	v.ReportDraft.Kind, _ = boundedManagerDraft(c.Kind, 32)
	v.ReportDraft.Note, _ = boundedManagerDraft(c.Note, 500)
	v.ReportKey, _ = boundedManagerDraft(c.Key, 100)
	result, err := a.store.ReportHandheldItem(handheldCookieValue(r, handheldCookie), r.PostForm.Get("csrf"), c)
	if err != nil {
		if !handheldProblem(err) && !orderCommandProblem(err) && !errors.Is(err, ErrNotFound) {
			a.fail(w, err)
			return
		}
		v.Error = err.Error()
		// A returned error is a definite refusal, not an ambiguous transport
		// outcome. Show the retained report against current state with a fresh
		// key for a new explicit Send. The old unsent/lost-response form still
		// has its original key and can make an exact idempotent retry.
		v.ReportKey = token()
		if errors.Is(err, ErrNotFound) {
			v.Error = "That item is no longer available on this task. Review the current pick list."
		}
	} else {
		v = HandheldView{Message: "Your item report is saved for the manager. Stock, picked quantities and the original receipt are unchanged."}
		if result.Replayed {
			v.Message = "This item report was already saved. No duplicate change was made; the current manager-review status is shown below."
		}
	}
	a.handheldPage(w, r, v, c.LineID)
}
