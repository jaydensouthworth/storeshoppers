package shop

import (
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
)

const handheldCookie = "shop_handheld"
const handheldPairCookie = "shop_handheld_pair"

type HandheldView struct {
	RecoveringPick, RecoveringWeight                              bool
	CSRF, CommandKey, Message, Error                              string
	ConnectCode, CodeDraft, PickedDraft, FormatDraft, SourceDraft string
	Task                                                          *HandheldTask
	Selected                                                      *HandheldLine
	Recognition                                                   *HandheldRecognition
	WeightDraft                                                   *HandheldWeightDraft
	WeightReview                                                  *HandheldWeightReview
	ReportDraft                                                   *HandheldReportDraft
	ReportKey                                                     string
}
type HandheldPairingView struct {
	View
	*HandheldPairing
	PairingURL               string
	PairingQR                template.URL
	CommandKey, ExpiresLabel string
}

func (a *App) registerHandheldRoutes() {
	a.mux.HandleFunc("GET /handheld/{$}", a.handheldTransport(a.showHandheld))
	a.mux.HandleFunc("POST /handheld/connect", a.handheldTransport(a.connectHandheld))
	a.mux.HandleFunc("POST /handheld/disconnect", a.handheldTransport(a.disconnectHandheld))
	a.mux.HandleFunc("POST /handheld/scan", a.handheldTransport(a.scanHandheld))
	a.mux.HandleFunc("POST /handheld/pick", a.handheldTransport(a.pickHandheld))
	a.mux.HandleFunc("POST /handheld/weight/preview", a.handheldTransport(a.previewHandheldWeight))
	a.mux.HandleFunc("POST /handheld/weight/confirm", a.handheldTransport(a.confirmHandheldWeight))
	a.mux.HandleFunc("POST /handheld/report", a.handheldTransport(a.reportHandheldItem))
	a.mux.HandleFunc("GET /manager/orders/{id}/phone", a.showHandheldPairing)
	a.mux.HandleFunc("POST /manager/orders/{id}/phone/issue", a.issueHandheldPairing)
	a.mux.HandleFunc("POST /manager/orders/{id}/phone/revoke", a.revokeHandheldPairing)
}

// Public worker capabilities require HTTPS even when manager access is later
// disabled in configuration. Loopback development is the only HTTP exception.
func (a *App) handheldTransport(next http.HandlerFunc) http.HandlerFunc {
	origin, _ := url.Parse(a.config.Origin)
	host := origin.Hostname()
	ip := net.ParseIP(host)
	secure := origin.Scheme == "https" || host == "localhost" || (ip != nil && ip.IsLoopback())
	return func(w http.ResponseWriter, r *http.Request) {
		if !secure {
			http.Error(w, "Phone picking requires HTTPS. Use a secure deployment or loopback development server.", http.StatusUpgradeRequired)
			return
		}
		next(w, r)
	}
}

func handheldCookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}
func (a *App) setHandheldCookie(w http.ResponseWriter, name string, s HandheldSession) {
	seconds := int(s.Expires - a.store.now().Unix())
	if seconds < 1 {
		seconds = -1
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: s.Token, Path: "/handheld/", MaxAge: seconds, HttpOnly: true, Secure: a.config.SecureCookies, SameSite: http.SameSiteStrictMode})
}
func (a *App) clearHandheldCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/handheld/", MaxAge: -1, HttpOnly: true, Secure: a.config.SecureCookies, SameSite: http.SameSiteStrictMode})
}
func (a *App) renderHandheldTemplate(w http.ResponseWriter, name string, value any) {
	var body boundedResponseBody
	if err := a.templates.ExecuteTemplate(&body, name, value); err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body.Bytes())
}
func handheldProblem(err error) bool {
	return errors.Is(err, ErrHandheldAccess) || errors.Is(err, ErrHandheldPairing) || errors.Is(err, ErrHandheldCSRF) || errors.Is(err, ErrHandheldAttempts) || errors.Is(err, ErrHandheldIssued) || errors.Is(err, ErrHandheldWeight) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) || errors.Is(err, ErrTerminal) || errors.Is(err, ErrShopperArchived)
}
func (a *App) handheldPage(w http.ResponseWriter, r *http.Request, v HandheldView, lineID int64) {
	if v.CommandKey == "" {
		v.CommandKey = token()
	}
	if v.ReportKey == "" {
		v.ReportKey = token()
	}
	if v.SourceDraft == "" {
		v.SourceDraft = "manual"
	}
	if v.FormatDraft == "" {
		v.FormatDraft = "Code128"
	}
	raw := handheldCookieValue(r, handheldCookie)
	if raw != "" {
		task, session, err := a.store.HandheldTask(raw)
		if err == nil {
			v.Task = task
			v.CSRF = session.CSRF
			for i := range task.Lines {
				if task.Lines[i].LineID == lineID {
					v.Selected = &task.Lines[i]
					break
				}
			}
			if lineID != 0 && v.Selected == nil {
				v.Error = "That item is no longer on this task. Choose an item from the current list."
				v.Recognition = nil
				v.WeightReview = nil
			}
			// Preview and projection are each coherent transactions. A manager change
			// between them must invalidate review, not attach old recognition to new data.
			if (v.Recognition != nil || v.WeightReview != nil) && (v.Selected == nil || r.PostForm.Get("version") != strconv.FormatInt(task.Version, 10) || r.PostForm.Get("pick_version") != strconv.FormatInt(v.Selected.PickVersion, 10) || r.PostForm.Get("assignment_version") != strconv.FormatInt(task.Assignment.Version, 10)) {
				v.Recognition = nil
				v.WeightReview = nil
				if v.WeightDraft != nil {
					v.RecoveringWeight = true
				}
				v.Error = ErrConflict.Error()
			}
			// An explicit successful recovery review establishes a new command.
			// Keep the original key until this step for an exact lost-response
			// retry; a consumed key must not trap an edited count in conflicts.
			if r.URL.Path == "/handheld/scan" && v.RecoveringPick && v.Recognition != nil && v.Recognition.CanPick {
				v.CommandKey = token()
				v.RecoveringPick = false
			}
			if v.Selected != nil && v.Selected.SaleUnit == "g" && v.WeightDraft == nil {
				actual := v.Selected.Allocated
				if v.Selected.Measured {
					actual = v.Selected.Picked
				}
				v.WeightDraft = &HandheldWeightDraft{Actual: strconv.FormatInt(actual, 10)}
			}
			if r.URL.Path == "/handheld/scan" && v.Recognition != nil && v.Recognition.CanMeasure {
				v.RecoveringWeight = false
			}
			if v.PickedDraft == "" && v.Selected != nil {
				suggested := v.Selected.Picked
				if v.Recognition != nil && v.Recognition.CanPick {
					suggested = min(suggested+1, v.Selected.Quantity)
				}
				v.PickedDraft = strconv.FormatInt(suggested, 10)
			}
			a.renderHandheldTemplate(w, "handheld", v)
			return
		}
		if !errors.Is(err, ErrHandheldAccess) {
			a.fail(w, err)
			return
		}
		a.clearHandheldCookie(w, handheldCookie)
		w.Header().Set("X-Handheld-Error", "revoked")
		v.Error = ErrHandheldAccess.Error()
		v.Recognition = nil
		v.WeightReview = nil
		v.WeightDraft = nil
		v.ReportDraft = nil
		v.CodeDraft = ""
		v.PickedDraft = ""
	}
	pair, err := a.store.HandheldPairSession(handheldCookieValue(r, handheldPairCookie))
	if err != nil {
		if handheldProblem(err) {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
		} else {
			a.fail(w, err)
		}
		return
	}
	if pair.Token != "" {
		a.setHandheldCookie(w, handheldPairCookie, pair)
	}
	v.CSRF = pair.CSRF
	a.renderHandheldTemplate(w, "handheld", v)
}
func (a *App) showHandheld(w http.ResponseWriter, r *http.Request) {
	var lineID int64
	v := HandheldView{}
	if raw := r.URL.Query().Get("line"); raw != "" {
		var err error
		lineID, err = num(raw)
		if err != nil || lineID < 1 {
			lineID = -1
			v.Error = "Choose an item from this phone's current task."
		}
	}
	a.handheldPage(w, r, v, lineID)
}
func (a *App) handheldForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid or oversized phone form. Refresh the workspace and try again.", http.StatusBadRequest)
		return false
	}
	return true
}
func (a *App) connectHandheld(w http.ResponseWriter, r *http.Request) {
	if !a.handheldForm(w, r) {
		return
	}
	code := r.PostForm.Get("pairing_code")
	grant, err := a.store.RedeemHandheld(handheldCookieValue(r, handheldPairCookie), r.PostForm.Get("csrf"), code)
	if err != nil {
		if !handheldProblem(err) {
			a.fail(w, err)
			return
		}
		draft, _ := boundedManagerDraft(code, 80)
		a.handheldPage(w, r, HandheldView{Error: err.Error(), ConnectCode: draft}, 0)
		return
	}
	a.setHandheldCookie(w, handheldCookie, grant)
	a.clearHandheldCookie(w, handheldPairCookie)
	// Redirect removes the POST and invitation from browser resubmission state.
	redirect(w, r, "/handheld/")
}
func (a *App) disconnectHandheld(w http.ResponseWriter, r *http.Request) {
	if !a.handheldForm(w, r) {
		return
	}
	err := a.store.DisconnectHandheld(handheldCookieValue(r, handheldCookie), r.PostForm.Get("csrf"))
	if err != nil && !errors.Is(err, ErrHandheldAccess) {
		if handheldProblem(err) {
			a.handheldPage(w, r, HandheldView{Error: err.Error()}, 0)
		} else {
			a.fail(w, err)
		}
		return
	}
	a.clearHandheldCookie(w, handheldCookie)
	redirect(w, r, "/handheld/")
}
func handheldScanForm(r *http.Request) HandheldScan {
	c := HandheldScan{Code: r.PostForm.Get("code"), Source: r.PostForm.Get("source"), Format: r.PostForm.Get("format")}
	valid := true
	for _, field := range []struct {
		name string
		dst  *int64
	}{{"line_id", &c.LineID}, {"assignment_version", &c.AssignmentVersion}, {"version", &c.Version}, {"pick_version", &c.PickVersion}} {
		value, err := num(r.PostForm.Get(field.name))
		if err != nil {
			valid = false
		}
		*field.dst = value
	}
	if !valid {
		c.LineID = 0
	}
	return c
}
func handheldDraft(r *http.Request) HandheldView {
	v := HandheldView{RecoveringPick: r.URL.Path == "/handheld/pick" || r.PostForm.Get("resume_confirmation") == "1", RecoveringWeight: r.PostForm.Get("resume_weight") == "1"}
	if v.RecoveringWeight {
		v.WeightDraft = handheldWeightDraft(r)
	}
	for _, field := range []struct {
		name  string
		dst   *string
		limit int
	}{{"code", &v.CodeDraft, 64}, {"picked", &v.PickedDraft, 20}, {"format", &v.FormatDraft, 20}, {"source", &v.SourceDraft, 20}, {"command_key", &v.CommandKey, 100}} {
		*field.dst, _ = boundedManagerDraft(r.PostForm.Get(field.name), field.limit)
	}
	return v
}
func (a *App) scanHandheld(w http.ResponseWriter, r *http.Request) {
	if !a.handheldForm(w, r) {
		return
	}
	c := handheldScanForm(r)
	v := handheldDraft(r)
	result, err := a.store.PreviewHandheldScan(handheldCookieValue(r, handheldCookie), r.PostForm.Get("csrf"), c)
	if err != nil {
		if !handheldProblem(err) {
			a.fail(w, err)
			return
		}
		v.Error = err.Error()
	} else {
		v.Recognition = &result
		v.CodeDraft = result.Code
		v.FormatDraft = "Code128"
		v.SourceDraft = result.Source
	}
	a.handheldPage(w, r, v, c.LineID)
}
func (a *App) pickHandheld(w http.ResponseWriter, r *http.Request) {
	if !a.handheldForm(w, r) {
		return
	}
	c := HandheldPick{HandheldScan: handheldScanForm(r), Key: r.PostForm.Get("command_key")}
	var err error
	c.Picked, err = num(r.PostForm.Get("picked"))
	if err != nil {
		c.LineID = 0
	}
	v := handheldDraft(r)
	result, err := a.store.ConfirmHandheldPick(handheldCookieValue(r, handheldCookie), r.PostForm.Get("csrf"), c)
	if err != nil {
		if !handheldProblem(err) {
			a.fail(w, err)
			return
		}
		v.Error = err.Error()
		// A quantity validation error can keep the verified review visible, but
		// only after reauthorizing and rechecking the same current code/versions.
		// Conflicts deliberately return to review with the draft/key retained.
		if errors.Is(err, ErrInvalid) {
			if recognition, reviewErr := a.store.PreviewHandheldScan(handheldCookieValue(r, handheldCookie), r.PostForm.Get("csrf"), c.HandheldScan); reviewErr == nil && recognition.CanPick {
				v.Recognition = &recognition
			} else if reviewErr != nil && !handheldProblem(reviewErr) {
				a.fail(w, reviewErr)
				return
			}
		}
	} else {
		// Only persisted success gets this feedback. A retry names its original
		// outcome while the newly fetched task shows any subsequent recorded edits.
		v = HandheldView{Message: fmt.Sprintf("Saved an absolute picked count of %d. Current recorded progress is shown below.", result.Picked)}
		if result.Replayed {
			v.Message = fmt.Sprintf("This confirmation was already saved with a picked count of %d. No duplicate change was made. Current recorded progress is shown below.", result.Picked)
		}
	}
	a.handheldPage(w, r, v, c.LineID)
}

func (a *App) handheldPairingPage(w http.ResponseWriter, r *http.Request, session Session, id int64, pairing *HandheldPairing, message string, problem error) {
	v, ok := a.viewForSession(w, session)
	if !ok {
		return
	}
	var err error
	if pairing == nil {
		pairing, err = a.store.HandheldPairing(id, session.ID, !a.config.DemoMode)
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			a.fail(w, err)
			return
		}
	}
	v.Title = "Connect a shopper phone"
	v.Section = "shoppers"
	v.ManagerTab = "shoppers"
	v.Message = message
	if problem != nil {
		v.Error = problem.Error()
	}
	out := HandheldPairingView{View: v, HandheldPairing: pairing, CommandKey: token(), ExpiresLabel: handheldTime(pairing.Expires)}
	if pairing.PairingCode != "" {
		out.PairingURL = a.config.Origin + "/handheld/#pair=" + url.QueryEscape(pairing.PairingCode)
		out.PairingQR, err = handheldPairingQRDataURL(out.PairingURL)
		if err != nil {
			a.fail(w, err)
			return
		}
	}
	a.renderHandheldTemplate(w, "handheld-pairing", out)
}
func (a *App) showHandheldPairing(w http.ResponseWriter, r *http.Request) {
	v, ok := a.view(w, r)
	if !ok || !a.guard(w, r, v.Session) {
		return
	}
	id, err := num(r.PathValue("id"))
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	a.handheldPairingPage(w, r, v.Session, id, nil, "", nil)
}
func (a *App) issueHandheldPairing(w http.ResponseWriter, r *http.Request) {
	a.changeHandheldPairing(w, r, "issue")
}
func (a *App) revokeHandheldPairing(w http.ResponseWriter, r *http.Request) {
	a.changeHandheldPairing(w, r, "revoke")
}
func (a *App) changeHandheldPairing(w http.ResponseWriter, r *http.Request, action string) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	id, err := num(r.PathValue("id"))
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	c := HandheldPairCommand{Key: r.PostForm.Get("command_key")}
	c.Version, _ = num(r.PostForm.Get("version"))
	c.AssignmentVersion, _ = num(r.PostForm.Get("assignment_version"))
	pairing, err := a.store.ChangeHandheldPairing(id, session.ID, !a.config.DemoMode, c, action)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil && !handheldProblem(err) {
		a.fail(w, err)
		return
	}
	message := ""
	if err == nil {
		message = "The previous phone and unused codes are disconnected. Recorded picking progress is unchanged."
		if action == "issue" {
			message = "A single-use connection code is ready. It expires in ten minutes or when the desktop owner's session ends. Pairing does not start picking."
		}
	}
	a.handheldPairingPage(w, r, session, id, pairing, message, err)
}
