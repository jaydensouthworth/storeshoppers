package shop

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetValues(session Session) url.Values {
	return url.Values{"csrf": {session.CSRF}, "confirm": {"reset-shared-demo"}}
}

func TestDemoResetHTTPAuthorizationAndConfirmation(t *testing.T) {
	for _, demo := range []bool{false, true} {
		s := newTestStore(t)
		a := reservationHTTPApp(t, s, demo)
		manager := testManager(t, s)
		testCart(t, s, manager.ID, 1, 2)
		before := fingerprintTest(t, s.db)
		get := testRequest(t, a, http.MethodGet, demoResetPath, manager, nil, nil)
		if !demo {
			if get.Code != 404 {
				t.Fatalf("normal GET=%d", get.Code)
			}
			if post := testRequest(t, a, http.MethodPost, demoResetPath, manager, resetValues(manager), nil); post.Code != 404 {
				t.Fatalf("normal POST=%d", post.Code)
			}
			if strings.Contains(testRequest(t, a, http.MethodGet, "/manager", manager, nil, nil).Body.String(), demoResetPath) {
				t.Fatal("normal mode shows reset link")
			}
		} else {
			if get.Code != 200 || !strings.Contains(get.Body.String(), "This affects every visitor") || !strings.Contains(get.Body.String(), "name=\"confirm\"") {
				t.Fatalf("confirmation page missing: %d %s", get.Code, get.Body.String())
			}
			if !strings.Contains(testRequest(t, a, http.MethodGet, "/manager", manager, nil, nil).Body.String(), demoResetPath) {
				t.Fatal("demo manager has no reset link")
			}
			for _, bad := range []string{"missing csrf", "wrong csrf", "query csrf", "missing confirmation", "wrong confirmation"} {
				values := resetValues(manager)
				path := demoResetPath
				want := 403
				switch bad {
				case "missing csrf":
					values.Del("csrf")
				case "wrong csrf":
					values.Set("csrf", "wrong")
				case "query csrf":
					values.Del("csrf")
					path += "?csrf=" + manager.CSRF
				case "missing confirmation":
					values.Del("confirm")
					want = 400
				case "wrong confirmation":
					values.Set("confirm", "yes")
					want = 400
				}
				response := testRequest(t, a, http.MethodPost, path, manager, values, nil)
				if response.Code != want {
					t.Fatalf("%s=%d want%d", bad, response.Code, want)
				}
			}
		}
		if fingerprintTest(t, s.db) != before {
			t.Fatal("GET/rejected reset changed data")
		}
	}
	s := newTestStore(t)
	a := reservationHTTPApp(t, s, true)
	visitor := testSession(t, s, "")
	before := fingerprintTest(t, s.db)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		response := testRequest(t, a, method, demoResetPath, visitor, resetValues(visitor), nil)
		assertRedirect(t, response, "/manager/login", false)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("non-manager reset changed data")
	}
}

func TestDemoResetHTTPRestoresSeedAndInvalidatesEverySession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s := openTestStore(t, path)
	a := reservationHTTPApp(t, s, true)
	manager := testManager(t, s)
	other := testManager(t, s)
	testCart(t, s, other.ID, 1, 2)
	order := testCheckout(t, s, other.ID)
	testCart(t, s, manager.ID, 2, 3)
	testExec(t, s, `UPDATE products SET name='Temporary shared edit' WHERE id=1`)
	response := testRequest(t, a, http.MethodPost, demoResetPath, manager, resetValues(manager), nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "The shared demo is back to its defaults.") {
		t.Fatalf("reset %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), ".demo-backups") || strings.Contains(response.Body.String(), path) {
		t.Fatal("backup path exposed in page")
	}
	assertDemoSeed(t, s)
	deleted := false
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "shop_session" && cookie.MaxAge < 0 {
			deleted = true
		}
	}
	if !deleted {
		t.Fatal("reset did not clear browser session cookie")
	}
	backups := savedBackups(t, path)
	if len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
	snapshot, err := openBackupReadOnly(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var n int
	snapshot.QueryRow("SELECT COUNT(*) FROM orders WHERE id=?", order).Scan(&n)
	if n != 1 {
		t.Fatal("other visitor order not preserved")
	}
	// Replaying a stale reset POST is rejected; it cannot reset new activity.
	response = testRequest(t, a, http.MethodPost, demoResetPath, manager, resetValues(manager), nil)
	assertRedirect(t, response, "/manager/login", false)
	if len(savedBackups(t, path)) != 1 {
		t.Fatal("stale replay created archive")
	}
	response = testRequest(t, a, http.MethodGet, "/manager", other, nil, nil)
	assertRedirect(t, response, "/manager/login", false)
}

func TestDemoResetHTTPBackupFailurePreservesStateAndHidesPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s := openTestStore(t, path)
	manager := testManager(t, s)
	testCart(t, s, manager.ID, 1, 2)
	a, err := New(s, Config{Origin: testOrigin, ManagerPassword: testManagerPassword, DemoMode: true, DemoBackupMaxBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	response := testRequest(t, a, http.MethodPost, demoResetPath, manager, resetValues(manager), nil)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "budget is full") {
		t.Fatalf("cap response %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), path) || strings.Contains(response.Body.String(), ".demo-backups") {
		t.Fatal("private diagnostics exposed")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("failed reset changed state")
	}
	if response = testRequest(t, a, http.MethodGet, "/manager", manager, nil, nil); response.Code != 200 {
		t.Fatal("failed reset blocked ordinary traffic")
	}
}

func TestDemoResetDrainCancellationPreservesDataAndReopensTraffic(t *testing.T) {
	s := newTestStore(t)
	a := reservationHTTPApp(t, s, true)
	manager := testManager(t, s)
	before := fingerprintTest(t, s.db)
	a.requestGate.RLock()
	request := httptest.NewRequest(http.MethodPost, testOrigin+demoResetPath, strings.NewReader(resetValues(manager).Encode()))
	request.AddCookie(&http.Cookie{Name: "shop_session", Value: manager.ID})
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Millisecond)
	defer cancel()
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	a.ServeHTTP(response, request)
	a.requestGate.RUnlock()
	if response.Code != 503 || a.resetPending.Load() {
		t.Fatalf("drain did not release after cancel: %d", response.Code)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("cancelled drain changed state")
	}
	if response = testRequest(t, a, http.MethodGet, "/manager", manager, nil, nil); response.Code != 200 {
		t.Fatal("traffic remains blocked")
	}
}

func TestDemoResetDrainsCheckoutAndBlocksNewCheckout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s := openTestStore(t, path)
	a := reservationHTTPApp(t, s, true)
	manager := testManager(t, s)
	shopper := testSession(t, s, "")
	testCart(t, s, shopper.ID, 1, 2)
	basket := testBasket(t, s, shopper.ID)
	nextShopper := testSession(t, s, "")
	testCart(t, s, nextShopper.ID, 1, 3)
	nextBasket := testBasket(t, s, nextShopper.ID)
	started, finish := make(chan struct{}), make(chan struct{})
	a.mux.HandleFunc("POST /test/inflight-checkout", func(w http.ResponseWriter, r *http.Request) {
		a.checkout(w, r)
		close(started)
		<-finish // Simulate an in-flight response after checkout commits.
	})
	slowDone := make(chan struct{})
	go func() {
		testRequest(t, a, http.MethodPost, "/test/inflight-checkout", shopper, reservationCheckoutValues(shopper, basket, ""), nil)
		close(slowDone)
	}()
	<-started
	resetDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		resetDone <- testRequest(t, a, http.MethodPost, demoResetPath, manager, resetValues(manager), nil)
	}()
	deadline := time.After(time.Second)
	for !a.resetPending.Load() {
		select {
		case <-deadline:
			t.Fatal("reset never began drain")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	checkoutDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		checkoutDone <- testRequest(t, a, http.MethodPost, "/checkout", nextShopper, reservationCheckoutValues(nextShopper, nextBasket, ""), nil)
	}()
	select {
	case <-checkoutDone:
		t.Fatal("checkout bypassed pending reset")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-resetDone:
		t.Fatal("reset bypassed in-flight handler")
	default:
	}
	close(finish)
	<-slowDone
	reset := <-resetDone
	if reset.Code != 200 {
		t.Fatalf("reset=%d %s", reset.Code, reset.Body.String())
	}
	checkout := <-checkoutDone
	if checkout.Code != 403 {
		t.Fatalf("stale checkout was not rejected after reset: %d %s", checkout.Code, checkout.Body.String())
	}
	if testCount(t, s, "orders") != 0 || testProduct(t, s, 1).Stock != 24 {
		t.Fatal("checkout straddled reset")
	}
	snapshot, err := openBackupReadOnly(savedBackups(t, path)[0])
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var reserved int
	if err = snapshot.QueryRow("SELECT SUM(reserved) FROM cart").Scan(&reserved); err != nil || reserved != 3 {
		t.Fatalf("pre-reset basket not archived %d %v", reserved, err)
	}
	var orders int
	if err = snapshot.QueryRow("SELECT count(*) FROM orders").Scan(&orders); err != nil || orders != 1 {
		t.Fatalf("completed checkout missing from archive: %d %v", orders, err)
	}
}

func TestDemoResetBoundsWaitAndRejectsConcurrentReset(t *testing.T) {
	s := newTestStore(t)
	a := reservationHTTPApp(t, s, true)
	manager := testManager(t, s)
	before := fingerprintTest(t, s.db)
	a.requestGate.RLock()
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- testRequest(t, a, http.MethodPost, demoResetPath, manager, resetValues(manager), nil) }()
	deadline := time.After(time.Second)
	for !a.resetPending.Load() {
		select {
		case <-deadline:
			t.Fatal("reset not pending")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	second := testRequest(t, a, http.MethodPost, demoResetPath, manager, resetValues(manager), nil)
	if second.Code != 503 {
		t.Fatalf("second reset=%d", second.Code)
	}
	response := <-first
	a.requestGate.RUnlock()
	if response.Code != 503 || a.resetPending.Load() {
		t.Fatal("bounded drain did not time out cleanly")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("drain timeout changed state")
	}
	if response = testRequest(t, a, http.MethodGet, "/manager", manager, nil, nil); response.Code != 200 {
		t.Fatal("timeout did not reopen traffic")
	}
}

func TestRejectedDemoResetNeverRequestsMaintenance(t *testing.T) {
	for _, kind := range []string{"anonymous", "visitor", "bad csrf", "unconfirmed"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestStore(t)
			a := reservationHTTPApp(t, s, true)
			manager := testManager(t, s)
			session := manager
			values := resetValues(manager)
			want := http.StatusForbidden
			switch kind {
			case "anonymous":
				session = Session{}
				want = http.StatusSeeOther
			case "visitor":
				session = testSession(t, s, "")
				values = resetValues(session)
				want = http.StatusSeeOther
			case "bad csrf":
				values.Set("csrf", "wrong")
			case "unconfirmed":
				values.Del("confirm")
				want = http.StatusBadRequest
			}
			// An ordinary in-flight request must not make invalid reset requests wait
			// for exclusive ownership, or stop another ordinary request from joining.
			a.requestGate.RLock()
			defer a.requestGate.RUnlock()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- testRequest(t, a, http.MethodPost, demoResetPath, session, values, nil) }()
			select {
			case response := <-done:
				if response.Code != want {
					t.Fatalf("rejection=%d want%d", response.Code, want)
				}
			case <-time.After(300 * time.Millisecond):
				t.Fatalf("%s tried to drain ordinary traffic (pending=%t)", kind, a.resetPending.Load())
			}
			if a.resetPending.Load() {
				t.Fatal("rejected POST requested maintenance")
			}
			if response := testRequest(t, a, http.MethodGet, "/healthz", Session{}, nil, nil); response.Code != http.StatusOK {
				t.Fatal("ordinary traffic blocked")
			}
		})
	}
}

type demoResetSlowBody struct {
	reader                io.Reader
	app                   *App
	started               chan struct{}
	resume                chan struct{}
	once                  sync.Once
	readDuringMaintenance atomic.Bool
}

func (body *demoResetSlowBody) Read(bytes []byte) (int, error) {
	if body.app.resetPending.Load() {
		body.readDuringMaintenance.Store(true)
	}
	body.once.Do(func() { close(body.started); <-body.resume })
	if body.app.resetPending.Load() {
		body.readDuringMaintenance.Store(true)
	}
	return body.reader.Read(bytes)
}
func (body *demoResetSlowBody) Close() error { return nil }

func TestSlowDemoResetBodiesDoNotHoldMaintenance(t *testing.T) {
	for _, kind := range []string{"anonymous", "bad csrf", "unconfirmed", "valid"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestStore(t)
			a := reservationHTTPApp(t, s, true)
			manager := testManager(t, s)
			session := manager
			values := resetValues(manager)
			want := http.StatusOK
			switch kind {
			case "anonymous":
				session = Session{}
				want = http.StatusSeeOther
			case "bad csrf":
				values.Set("csrf", "wrong")
				want = http.StatusForbidden
			case "unconfirmed":
				values.Del("confirm")
				want = http.StatusBadRequest
			}
			body := &demoResetSlowBody{reader: strings.NewReader(values.Encode()), app: a, started: make(chan struct{}), resume: make(chan struct{})}
			var resume sync.Once
			unblock := func() { resume.Do(func() { close(body.resume) }) }
			defer unblock()
			request := httptest.NewRequest(http.MethodPost, testOrigin+demoResetPath, body)
			request.Header.Set("Origin", testOrigin)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if session.ID != "" {
				request.AddCookie(&http.Cookie{Name: "shop_session", Value: session.ID})
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { response := httptest.NewRecorder(); a.ServeHTTP(response, request); done <- response }()
			if kind == "anonymous" {
				select {
				case response := <-done:
					if response.Code != want {
						t.Fatalf("anonymous response=%d", response.Code)
					}
				case <-body.started:
					t.Fatal("anonymous reset parsed an untrusted body before manager authentication")
				case <-time.After(time.Second):
					t.Fatal("anonymous reset blocked")
				}
				return
			}
			select {
			case <-body.started:
			case <-time.After(time.Second):
				t.Fatal("body parsing did not start")
			}
			if a.resetPending.Load() {
				t.Fatal("slow body requested maintenance")
			}
			ordinary := make(chan *httptest.ResponseRecorder, 1)
			go func() { ordinary <- testRequest(t, a, http.MethodGet, "/healthz", Session{}, nil, nil) }()
			select {
			case response := <-ordinary:
				if response.Code != 200 {
					t.Fatalf("ordinary response=%d", response.Code)
				}
			case <-time.After(300 * time.Millisecond):
				t.Fatal("slow reset body blocked ordinary traffic")
			}
			unblock()
			select {
			case response := <-done:
				if response.Code != want {
					t.Fatalf("parsed reset=%d want%d: %s", response.Code, want, response.Body.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("reset did not finish")
			}
			if body.readDuringMaintenance.Load() {
				t.Fatal("request body was read under maintenance")
			}
		})
	}
}

func TestDemoResetRevalidatesAfterPreflightSessionChanges(t *testing.T) {
	for _, change := range []string{"csrf rotated", "manager revoked", "session cleared by reset"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "shop.db")
			s := openTestStore(t, path)
			a := reservationHTTPApp(t, s, true)
			manager := testManager(t, s)
			testExec(t, s, `UPDATE products SET name='Keep this later state' WHERE id=1`)
			a.requestGate.RLock()
			held := true
			defer func() {
				if held {
					a.requestGate.RUnlock()
				}
			}()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- testRequest(t, a, http.MethodPost, demoResetPath, manager, resetValues(manager), nil) }()
			deadline := time.After(time.Second)
			for !a.resetPending.Load() {
				select {
				case <-deadline:
					t.Fatal("valid reset did not finish preflight")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			// Model a previously admitted request changing the session while the
			// validated reset drains it, or the old session disappearing after reset.
			switch change {
			case "csrf rotated":
				testExec(t, s, `UPDATE sessions SET csrf='rotated' WHERE id=?`, manager.ID)
			case "manager revoked":
				testExec(t, s, `UPDATE sessions SET manager_until=0 WHERE id=?`, manager.ID)
			case "session cleared by reset":
				testExec(t, s, `DELETE FROM baskets WHERE owner_session_id=?`, manager.ID)
				testExec(t, s, `DELETE FROM sessions WHERE id=?`, manager.ID)
			}
			before := fingerprintTest(t, s.db)
			a.requestGate.RUnlock()
			held = false
			response := <-done
			if response.Code != http.StatusForbidden {
				t.Fatalf("stale authorized reset accepted: %d %s", response.Code, response.Body.String())
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("revalidation failure changed data or created a replacement session")
			}
			if len(savedBackups(t, path)) != 0 {
				t.Fatal("revalidation failure archived or reset the database")
			}
			if a.resetPending.Load() {
				t.Fatal("revalidation failure retained maintenance")
			}
		})
	}
}
