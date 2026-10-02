package shop

import (
	"errors"
	"fmt"
	"net/http"
)

type SubstitutionView struct {
	SubstitutionWorkspace
	Path, BackURL, Key, Error string
	Selected                  *WorkingOrderItem
	Draft                     SubstitutionCommand
	Preview                   *SubstitutionPreview
}

func (a *App) registerSubstitutionRoutes() {
	a.mux.HandleFunc("GET /handheld/substitutions", a.handheldTransport(a.showPhoneSubstitutions))
	a.mux.HandleFunc("POST /handheld/substitutions/preview", a.handheldTransport(a.previewPhoneSubstitution))
	a.mux.HandleFunc("POST /handheld/substitutions/send", a.handheldTransport(a.sendPhoneSubstitution))
	a.mux.HandleFunc("POST /handheld/substitutions/withdraw", a.handheldTransport(a.withdrawPhoneSubstitution))
	a.mux.HandleFunc("GET /orders/{id}/substitutions", a.showCustomerSubstitutions)
	a.mux.HandleFunc("POST /orders/{id}/substitutions", a.decideCustomerSubstitution)
}
func (a *App) substitutionAccessError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, ErrHandheldAccess) || errors.Is(err, ErrHandheldCSRF) || errors.Is(err, ErrMessageCSRF) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrEmployeePractice) {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "This replacement workspace is unavailable or your access ended. Return to your order or employee dashboard.", http.StatusForbidden)
		return true
	}
	return false
}
func (a *App) substitutionPage(w http.ResponseWriter, r *http.Request, phone bool, v SubstitutionView) {
	id, _ := num(r.PathValue("id"))
	workspace, err := a.store.Substitutions(id, messageCustomerCookie(r), handheldCookieValue(r, handheldCookie), phone)
	if err != nil {
		if !a.substitutionAccessError(w, err) {
			a.fail(w, err)
		}
		return
	}
	v.SubstitutionWorkspace = workspace
	v.Path = fmt.Sprintf("/orders/%d/substitutions", workspace.OrderID)
	v.BackURL = fmt.Sprintf("/orders/%d", workspace.OrderID)
	if phone {
		v.Path = "/handheld/substitutions"
		v.BackURL = "/handheld/"
	}
	if v.Key == "" {
		v.Key = token()
	}
	if phone {
		line := v.Draft.LineID
		if line == 0 {
			line, _ = num(r.URL.Query().Get("line"))
		}
		for i := range workspace.Lines {
			if workspace.Lines[i].LineID == line && workspace.Lines[i].SaleUnit == "each" {
				v.Selected = &workspace.Lines[i]
				break
			}
		}
		if v.Preview != nil && (workspace.Version != v.Preview.Command.Version || workspace.AssignmentVersion != v.Preview.Command.AssignmentVersion) {
			v.Preview = nil
			v.Error = ErrSubstitutionStale.Error()
		}
		if v.Selected != nil {
			v.Draft.LineID = v.Selected.LineID
			v.Draft.Version = workspace.Version
			v.Draft.PickVersion = v.Selected.PickVersion
			v.Draft.AssignmentVersion = workspace.AssignmentVersion
			if v.Draft.Quantity == 0 {
				v.Draft.Quantity = v.Selected.Quantity
			}
		}
	}
	a.renderHandheldTemplate(w, "substitutions", v)
}
func (a *App) showPhoneSubstitutions(w http.ResponseWriter, r *http.Request) {
	a.substitutionPage(w, r, true, SubstitutionView{})
}
func (a *App) showCustomerSubstitutions(w http.ResponseWriter, r *http.Request) {
	a.substitutionPage(w, r, false, SubstitutionView{})
}
func (a *App) substitutionForm(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Origin") != a.config.Origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "Cross-origin replacement request rejected.", http.StatusForbidden)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid or oversized replacement form. Refresh and review before trying again.", http.StatusBadRequest)
		return false
	}
	return true
}
func substitutionCommandForm(r *http.Request) SubstitutionCommand {
	c := SubstitutionCommand{Key: r.PostForm.Get("command_key"), Note: r.PostForm.Get("note"), Disposition: r.PostForm.Get("disposition"), Review: r.PostForm.Get("review")}
	for key, p := range map[string]*int64{"line_id": &c.LineID, "version": &c.Version, "pick_version": &c.PickVersion, "assignment_version": &c.AssignmentVersion, "replacement_id": &c.ReplacementID, "quantity": &c.Quantity} {
		n, e := num(r.PostForm.Get(key))
		if e != nil {
			n = -1
		}
		*p = n
	}
	return c
}
func (a *App) previewPhoneSubstitution(w http.ResponseWriter, r *http.Request) {
	if !a.substitutionForm(w, r) {
		return
	}
	c := substitutionCommandForm(r)
	// Explicit Preview is a fresh intent. The separate Send form retains this
	// exact key for retries; browser Back/edit cannot trap a consumed identity.
	c.Key = token()
	preview, err := a.store.PreviewSubstitution(handheldCookieValue(r, handheldCookie), r.PostForm.Get("csrf"), c)
	v := SubstitutionView{Draft: c, Key: c.Key}
	if err != nil {
		if a.substitutionAccessError(w, err) {
			return
		}
		if !substitutionProblem(err) {
			a.fail(w, err)
			return
		}
		v.Error = err.Error()
	} else {
		v.Preview = &preview
	}
	a.substitutionPage(w, r, true, v)
}
func (a *App) sendPhoneSubstitution(w http.ResponseWriter, r *http.Request) {
	if !a.substitutionForm(w, r) {
		return
	}
	c := substitutionCommandForm(r)
	_, err := a.store.SendSubstitution(handheldCookieValue(r, handheldCookie), r.PostForm.Get("csrf"), c)
	if err != nil {
		if a.substitutionAccessError(w, err) {
			return
		}
		if !substitutionProblem(err) {
			a.fail(w, err)
			return
		}
		a.substitutionPage(w, r, true, SubstitutionView{Draft: c, Key: c.Key, Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/handheld/substitutions", http.StatusSeeOther)
}
func (a *App) withdrawPhoneSubstitution(w http.ResponseWriter, r *http.Request) {
	if !a.substitutionForm(w, r) {
		return
	}
	id, _ := num(r.PostForm.Get("proposal_id"))
	err := a.store.WithdrawSubstitution(handheldCookieValue(r, handheldCookie), r.PostForm.Get("csrf"), id, r.PostForm.Get("review"))
	if err != nil {
		if a.substitutionAccessError(w, err) {
			return
		}
		if !substitutionProblem(err) {
			a.fail(w, err)
			return
		}
		a.substitutionPage(w, r, true, SubstitutionView{Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/handheld/substitutions", http.StatusSeeOther)
}
func (a *App) decideCustomerSubstitution(w http.ResponseWriter, r *http.Request) {
	if !a.substitutionForm(w, r) {
		return
	}
	id, _ := num(r.PathValue("id"))
	pid, _ := num(r.PostForm.Get("proposal_id"))
	c := SubstitutionDecision{ProposalID: pid, Key: r.PostForm.Get("command_key"), Binding: r.PostForm.Get("binding"), Review: r.PostForm.Get("review"), Action: r.PostForm.Get("decision")}
	err := a.store.DecideSubstitution(id, messageCustomerCookie(r), r.PostForm.Get("csrf"), c)
	if err != nil {
		if a.substitutionAccessError(w, err) {
			return
		}
		if !substitutionProblem(err) {
			a.fail(w, err)
			return
		}
		a.substitutionPage(w, r, false, SubstitutionView{Error: err.Error()})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/orders/%d/substitutions", id), http.StatusSeeOther)
}
