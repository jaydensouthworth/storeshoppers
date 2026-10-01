package shop

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

const demoResetPath = "/manager/demo/reset"

// A reset waits at most two seconds for in-flight HTTP handlers. New requests
// stop joining while a reset waits, so checkout can finish before the snapshot,
// but no checkout can straddle baseline installation. Context cancellation also
// cancels the wait. The gate is in-process; SQLite ownership excludes old apps.
func (a *App) acquireRequestGate(ctx context.Context, reset bool) (func(), error) {
	if reset && !a.resetPending.CompareAndSwap(false, true) {
		return nil, errors.New("another demo reset is pending")
	}
	success := false
	defer func() {
		if reset && !success {
			a.resetPending.Store(false)
		}
	}()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	retry := time.NewTicker(10 * time.Millisecond)
	defer retry.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if reset {
			if a.requestGate.TryLock() {
				success = true
				return func() { a.requestGate.Unlock(); a.resetPending.Store(false) }, nil
			}
		} else if !a.resetPending.Load() && a.requestGate.TryRLock() {
			// Recheck after locking so a request racing the pending flag does not join
			// the drain after the reset has announced ownership intent.
			if !a.resetPending.Load() {
				return a.requestGate.RUnlock, nil
			}
			a.requestGate.RUnlock()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("demo reset request drain timed out")
		case <-retry.C:
		}
	}
}

func (a *App) showDemoReset(w http.ResponseWriter, r *http.Request) {
	session, err := a.session(w, r)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !a.guard(w, r, session) {
		return
	}
	a.renderNamed(w, r, "demo-reset-page", View{Title: "Reset shared demo", Session: session, DemoMode: true}, http.StatusOK)
}

func (a *App) performDemoReset(w http.ResponseWriter, r *http.Request) {
	if a.resetPending.Load() {
		demoResetBusy(w)
		return
	}
	readRelease, err := a.acquireRequestGate(r.Context(), false)
	if err != nil {
		demoResetBusy(w)
		return
	}
	// Authentication, body parsing, CSRF and confirmation run under an ordinary
	// read lease. Slow or invalid submissions cannot request maintenance or stop
	// unrelated requests. Release this lease before waiting for the write gate.
	session, ok := func() (Session, bool) {
		defer readRelease()
		return a.preflightDemoReset(w, r)
	}()
	if !ok {
		return
	}
	release, err := a.acquireRequestGate(r.Context(), true)
	if err != nil {
		demoResetBusy(w)
		return
	}
	defer release()
	// A logout, CSRF rotation or previous reset may have happened after the
	// preflight read lease ended. Recheck existing state without parsing the
	// request body or creating a replacement session under exclusive ownership.
	session, ok = a.revalidateDemoReset(w, r, session)
	if !ok {
		return
	}
	a.applyDemoReset(w, r, session)
}

func demoResetBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "3")
	http.Error(w, "The demo is busy or being reset. Wait a moment and try again.", http.StatusServiceUnavailable)
}

func (a *App) preflightDemoReset(w http.ResponseWriter, r *http.Request) (Session, bool) {
	// Reject visitors before reading a potentially slow body. The second guard
	// below also catches a manager grant which expires while the body is read.
	session, err := a.session(w, r)
	if err != nil {
		a.fail(w, err)
		return Session{}, false
	}
	if !a.guard(w, r, session) {
		return session, false
	}
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return session, false
	}
	if r.PostForm.Get("confirm") != "reset-shared-demo" {
		a.renderNamed(w, r, "demo-reset-page", View{Title: "Reset shared demo", Session: session, DemoMode: true, Error: "Confirm that you want to reset the shared demo for every visitor."}, http.StatusBadRequest)
		return session, false
	}
	return a.revalidateDemoReset(w, r, session)
}

func (a *App) revalidateDemoReset(w http.ResponseWriter, r *http.Request, session Session) (Session, bool) {
	err := a.store.db.QueryRowContext(r.Context(), `SELECT csrf,manager_until FROM sessions WHERE id=? AND expires>?`, session.ID, a.store.now().Unix()).Scan(&session.CSRF, &session.ManagerUntil)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (a.config.ManagerPassword == "" || session.ManagerUntil <= time.Now().Unix() || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(session.CSRF)) != 1)) {
		http.Error(w, "Your reset form expired. Sign in again and review the confirmation page.", http.StatusForbidden)
		return session, false
	}
	if err != nil {
		a.fail(w, err)
		return session, false
	}
	return session, true
}

func (a *App) applyDemoReset(w http.ResponseWriter, r *http.Request, session Session) {
	result, err := a.store.ResetDemo(DemoResetOptions{BackupMaxBytes: a.config.DemoBackupMaxBytes})
	if err != nil {
		log.Printf("demo reset failed: %v", err)
		message := "Reset could not be completed. The previous demo remains recoverable. Wait a moment and try again, or ask the operator to check the server log."
		if strings.Contains(err.Error(), "budget") {
			message = "Reset was refused because the private backup storage budget is full. The current demo is unchanged. Ask the operator to retain the backups elsewhere or raise the storage budget."
		}
		if strings.Contains(err.Error(), "ownership unavailable") {
			message = "Reset was refused because another database user is connected. The current demo is unchanged. Ask the operator to disconnect that user, then try again."
		}
		if result.Reset {
			message = "The demo was reset and the previous state was backed up, but a database maintenance step failed. Ask the operator to check the server log before trying again."
		}
		a.renderNamed(w, r, "demo-reset-page", View{Title: "Reset shared demo", Session: session, DemoMode: true, Error: message}, http.StatusServiceUnavailable)
		return
	}
	log.Printf("Shared demo reset completed; pre-reset database preserved privately at %s", result.BackupPath)
	http.SetCookie(w, &http.Cookie{Name: "shop_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.config.SecureCookies, SameSite: http.SameSiteLaxMode})
	a.renderNamed(w, r, "demo-reset-success", View{Title: "Shared demo reset", DemoMode: true}, http.StatusOK)
}
