package shop

import (
	"errors"
	"net/http"
)

type AttentionDraft struct {
	Action, Reason string
	Truncated      bool
}

func attentionDraft(r *http.Request) *AttentionDraft {
	d := &AttentionDraft{}
	d.Action, _ = boundedManagerDraft(r.PostForm.Get("action"), 20)
	d.Reason, d.Truncated = boundedManagerDraft(r.PostForm.Get("reason"), 500)
	return d
}

// This is display-only recovery, never a second command or trusted version.
func recoveredFinishDraft(r *http.Request) map[string]string {
	if r.PostForm.Get("action") != "release" || r.PostForm.Get("recover_finish") != "1" {
		return nil
	}
	draft := map[string]string{"form_id": "finish"}
	for name, limit := range map[string]int{"reason": 500, "remainder": 32, "disposition": 32} {
		value, _ := boundedManagerDraft(r.PostForm.Get("finish_"+name), limit)
		draft[name] = value
	}
	return draft
}

func (a *App) attentionOrder(w http.ResponseWriter, r *http.Request) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	id, err := num(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err = a.store.ManagerOrder(id, session.ID, !a.config.DemoMode); errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.fail(w, err)
		return
	}
	c := AttentionCommand{Action: r.PostForm.Get("action"), Key: r.PostForm.Get("command_key"), Reason: r.PostForm.Get("reason")}
	c.Version, err = num(r.PostForm.Get("version"))
	if err != nil {
		err = ErrInvalid
	} else {
		err = a.store.AttentionOrder(id, session.ID, !a.config.DemoMode, c)
	}
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil && !orderCommandProblem(err) {
		a.fail(w, err)
		return
	}
	if err == nil && r.Header.Get("HX-Request") != "true" && recoveredFinishDraft(r) == nil {
		redirect(w, r, orderFilters(r).URL(id)+"#order-attention")
		return
	}
	message := ""
	if err == nil {
		message = map[string]string{"hold": "Manager hold opened. Item work and assignment remain available.", "release": "Manager hold released. Stock and picking progress are unchanged.", "note": "Private manager note saved."}[c.Action]
	}
	if err == nil && recoveredFinishDraft(r) != nil {
		message += " Your unsaved partial-completion draft is restored below. Review and submit it separately."
	}
	a.pickingView(w, r, id, message, err)
}
