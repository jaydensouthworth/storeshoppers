package shop

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSchemaGuardRejectsEveryMutationWithoutChangingData(t *testing.T) {
	s := newTestStore(t)
	session := testSession(t, s, "")
	testCart(t, s, session.ID, 1, 2)
	orderID := testCheckout(t, s, session.ID)
	order := testOrder(t, s, orderID, session.ID)
	line := order.WorkingItems[0]
	testCart(t, s, session.ID, 2, 3)
	basket := testBasket(t, s, session.ID)
	product := testProduct(t, s, 1)
	// Make expiry a real attempted stock release rather than an empty command.
	testExec(t, s, `UPDATE baskets SET hold_until=1 WHERE id=?`, basket.ID)
	testExec(t, s, `INSERT INTO schema_version VALUES(?)`, latestSchemaVersion+1)
	before := fingerprintTest(t, s.db)
	for _, command := range []struct {
		name string
		run  func() error
	}{
		{"session creation", func() error { _, err := s.Session(""); return err }},
		{"manager login", func() error { return s.Manager(session.ID, true) }},
		{"manager logout", func() error { return s.Manager(session.ID, false) }},
		{"expiry", s.ExpireHolds},
		{"stock", func() error { return s.Adjust(1, 1, product.Version, "Restock") }},
		{"product", func() error { _, err := s.SaveProduct(product); return err }},
		{"product details", func() error {
			_, err := s.SaveProductWithDetails(product, ProductDetails{Body: "New details"})
			return err
		}},
		{"archive product", func() error { return s.ArchiveProduct(1, 1) }},
		{"restore product", func() error { return s.RestoreProduct(1, 1) }},
		{"create category", func() error { _, err := s.SaveTaxonomy("category", 0, 0, "New category"); return err }},
		{"edit type", func() error { _, err := s.SaveTaxonomy("type", 1, 1, "New type"); return err }},
		{"archive category", func() error { return s.ArchiveTaxonomy("category", 1, 1) }},
		{"restore type", func() error { return s.RestoreTaxonomy("type", 1, 1) }},
		{"product examples", func() error { return s.CreateProductExamples(token(), strings.Repeat("a", 64)) }},
		{"promotion", func() error { _, err := s.SavePromotion(Promotion{ProductID: 1}); return err }},
		{"cancel promotion", func() error { return s.CancelPromotion(1, 1) }},
		{"featured", func() error { return s.SetFeatured(1, 0, true) }},
		{"promotion examples", func() error { return s.CreateExampleSales(token(), strings.Repeat("a", 64)) }},
		{"cart", func() error { return s.SetCartVersion(session.ID, 1, 1, false, basket.Revision) }},
		{"renew cart", func() error { return s.RenewBasket(session.ID, basket.Revision) }},
		{"manager cart", func() error {
			return s.ManagerSetBasket(basket.ID, session.ID, true, 1, 1, basket.Revision, "Reviewed")
		}},
		{"manager renewal", func() error { return s.ManagerRenewBasket(basket.ID, session.ID, true, basket.Revision, "Reviewed") }},
		{"practice baskets", func() error { return s.CreatePracticeBaskets(session.ID) }},
		{"checkout", func() error {
			_, err := s.Checkout(session.ID, session.CheckoutKey, session.Revision, basket.Quote)
			return err
		}},
		{"pick", func() error { return s.RecordPicked(orderID, 1, 1, 1, session.ID, true) }},
		{"mark working line picked", func() error {
			return s.MarkWorkingLinePicked(orderID, line.LineID, line.Quantity, line.PickVersion, order.Version, token(), session.ID, true)
		}},
		{"transition", func() error { return s.AdvanceVersioned(orderID, "Placed", 1, session.ID, true) }},
		{"override", func() error {
			return s.OverrideOrder(orderID, session.ID, true, OrderCommand{Action: "set", Version: 1, Key: token(), Reason: "Reviewed quantity", Quantity: 1, ProductID: 1})
		}},
		{"assignment", func() error {
			return s.AssignShopper(orderID, session.ID, true, ShopperCommand{Action: "assign", Version: 1, Key: token(), Reason: "Reviewed assignment", ShopperID: 1})
		}},
		{"manager hold", func() error {
			return s.AttentionOrder(orderID, session.ID, true, AttentionCommand{Action: "hold", Version: 1, Key: token(), Reason: "Needs review"})
		}},
		{"manager release", func() error {
			return s.AttentionOrder(orderID, session.ID, true, AttentionCommand{Action: "release", Version: 1, Key: token()})
		}},
		{"internal note", func() error {
			return s.AttentionOrder(orderID, session.ID, true, AttentionCommand{Action: "note", Version: 1, Key: token(), Reason: "Private review"})
		}},
		{"reset", func() error { _, err := s.ResetDemo(DemoResetOptions{}); return err }},
	} {
		t.Run(command.name, func(t *testing.T) {
			if err := command.run(); !errors.Is(err, ErrSchemaIncompatible) {
				t.Fatalf("command error = %v, want schema refusal", err)
			}
			if after := fingerprintTest(t, s.db); after != before {
				t.Fatal("refused command changed schema, data, stock, holds or audit")
			}
		})
	}
}

func TestSchemaGuardFailsClosedOnInvalidMarker(t *testing.T) {
	for _, query := range []string{
		fmt.Sprintf(`DELETE FROM schema_version WHERE version=%d`, latestSchemaVersion),
		`DELETE FROM schema_version`,
		`DROP TABLE schema_version`,
	} {
		t.Run(query, func(t *testing.T) {
			s := newTestStore(t)
			session := testSession(t, s, "")
			testExec(t, s, query)
			before := fingerprintTest(t, s.db)
			if err := s.CheckCompatibility(context.Background()); !errors.Is(err, ErrSchemaIncompatible) {
				t.Fatalf("readiness = %v", err)
			}
			if err := s.Manager(session.ID, true); !errors.Is(err, ErrSchemaIncompatible) {
				t.Fatalf("mutation = %v", err)
			}
			if _, err := s.ResetDemo(DemoResetOptions{}); !errors.Is(err, ErrSchemaIncompatible) {
				t.Fatalf("reset = %v", err)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("invalid marker refusal changed database")
			}
		})
	}
}

func TestSchemaGuardMigrationWinsWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	old := openTestStore(t, path)
	future := openTestStore(t, path)
	session := testSession(t, old, "")
	// Migration deliberately bypasses the application guard and owns the writer
	// lock while its version and data change are still uncommitted.
	migration, err := future.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer migration.Rollback()
	if _, err = migration.Exec(fmt.Sprintf(`UPDATE products SET stock=1200,sale_unit='g',price_basis=1000,quantity_step=50 WHERE id=1; INSERT INTO schema_version VALUES(%d)`, latestSchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	started, result := make(chan struct{}), make(chan error, 1)
	go func() { close(started); result <- old.Manager(session.ID, true) }()
	<-started
	select {
	case err := <-result:
		t.Fatalf("command passed the migration writer lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err = migration.Commit(); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, future.db)
	select {
	case err := <-result:
		if !errors.Is(err, ErrSchemaIncompatible) {
			t.Fatalf("waiting command = %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("waiting command never completed")
	}
	if fingerprintTest(t, future.db) != before {
		t.Fatal("old command changed future database")
	}
	if reopened, err := Open(path); !errors.Is(err, ErrSchemaIncompatible) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("old binary startup = %v", err)
	}
	if fingerprintTest(t, future.db) != before {
		t.Fatal("old startup changed future database")
	}
}

func TestSchemaGuardCommandWinsWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	old := openTestStore(t, path)
	future := openTestStore(t, path)
	tx, err := old.beginWrite()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE products SET stock=stock-1,version=version+1 WHERE id=1; INSERT INTO adjustments(product_id,delta,reason,sale_unit) VALUES(1,-1,'Before migration','each')`); err != nil {
		t.Fatal(err)
	}
	started, result := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		migration, err := future.db.Begin()
		if err != nil {
			result <- err
			return
		}
		defer migration.Rollback()
		var stock, audit int
		if err = migration.QueryRow(`SELECT stock,(SELECT COUNT(*) FROM adjustments) FROM products WHERE id=1`).Scan(&stock, &audit); err == nil && (stock != 23 || audit != 1) {
			err = errors.New("migration did not see complete earlier command")
		}
		if err == nil {
			_, err = migration.Exec(fmt.Sprintf(`INSERT INTO schema_version VALUES(%d)`, latestSchemaVersion+1))
		}
		if err == nil {
			err = migration.Commit()
		}
		result <- err
	}()
	<-started
	select {
	case err := <-result:
		t.Fatalf("migration passed the command writer lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("migration never completed")
	}
	before := fingerprintTest(t, future.db)
	if err = old.Adjust(1, -1, 2, "After migration"); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatalf("later command = %v", err)
	}
	if fingerprintTest(t, future.db) != before {
		t.Fatal("later command changed future database")
	}
}

func TestSchemaGuardHTTPRefusesDynamicRequestsButServesAssets(t *testing.T) {
	s := newTestStore(t)
	a, err := New(s, Config{Origin: testOrigin, ManagerPassword: testManagerPassword, DemoMode: true})
	if err != nil {
		t.Fatal(err)
	}
	session := testSession(t, s, "")
	testExec(t, s, fmt.Sprintf(`INSERT INTO schema_version VALUES(%d)`, latestSchemaVersion+1))
	before := fingerprintTest(t, s.db)
	for _, path := range []string{"/", "/products/1", "/cart", "/orders/1/status", "/manager", "/healthz", demoResetPath} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, htmx := range []string{"false", "true"} {
				w := testRequest(t, a, method, path, session, url.Values{"csrf": {session.CSRF}}, map[string]string{"HX-Request": htmx})
				assertSchemaUnavailable(t, w)
			}
		}
	}
	w := testRequest(t, a, http.MethodGet, "/static/app.css", Session{}, nil, nil)
	if w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Fatalf("static asset = %d", w.Code)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("incompatible HTTP request changed data")
	}
}

func TestSchemaGuardProtectsScopedProductPickers(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner, orderID := pickingFixture(t, s)
	owner = pickingLogin(t, a, owner)
	order := testOrder(t, s, orderID, owner.ID)
	basket := testBasket(t, s, owner.ID)
	paths := []string{
		fmt.Sprintf("/manager/orders/%d/products?context=sub&line_id=%d&product_q=spinach", orderID, order.WorkingItems[0].LineID),
		"/manager/baskets/" + basket.ID + "/products?context=add&product_q=sourdough",
	}
	for _, path := range paths {
		for _, htmx := range []bool{false, true} {
			w := testRequest(t, a, http.MethodGet, path, owner, nil, pickingHeaders(htmx))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `type="radio"`) {
				t.Fatalf("compatible picker %s: %d, %s", path, w.Code, w.Body.String())
			}
		}
	}
	testExec(t, s, `INSERT INTO schema_version VALUES(?)`, latestSchemaVersion+1)
	before := fingerprintTest(t, s.db)
	for _, path := range paths {
		for _, htmx := range []bool{false, true} {
			w := testRequest(t, a, http.MethodGet, path, owner, nil, pickingHeaders(htmx))
			assertSchemaUnavailable(t, w)
			if strings.Contains(w.Body.String(), `type="radio"`) {
				t.Fatal("incompatible picker released product results")
			}
		}
	}
	// A cold browser must not get a replacement identity on either route type.
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := testRequest(t, a, method, "/", Session{}, nil, nil)
		assertSchemaUnavailable(t, w)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("refused picker or cold-browser request changed state")
	}
}

func assertSchemaUnavailable(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "3" || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "temporarily unavailable") {
		t.Fatalf("incompatibility response = %d, %v, %q", w.Code, w.Header(), w.Body.String())
	}
	for _, name := range []string{"Set-Cookie", "Location", "HX-Redirect", "HX-Push-Url", "X-Shop-CSRF", "X-Shop-Error"} {
		if value := w.Header().Get(name); value != "" {
			t.Errorf("discarded response leaked %s: %q", name, value)
		}
	}
}

func TestSchemaGuardDiscardsResponseIfMigrationCommitsDuringHandler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s := openTestStore(t, path)
	future := openTestStore(t, path)
	a := testApp(t, s, testManagerPassword)
	a.mux.HandleFunc("GET /compatibility-race", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "must-not-leak", Value: "session"})
		w.Header().Set("Location", "/manager")
		w.Header().Set("HX-Redirect", "/manager")
		w.Header().Set("HX-Push-Url", "/manager")
		w.Header().Set("X-Shop-CSRF", "must-not-leak")
		w.Header().Set("X-Shop-Error", "csrf-expired")
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Length", "12345")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("future grams rendered as units must not leak"))
		testExec(t, future, fmt.Sprintf(`INSERT INTO schema_version VALUES(%d)`, latestSchemaVersion+1))
	})
	w := testRequest(t, a, http.MethodGet, "/compatibility-race", Session{}, nil, nil)
	assertSchemaUnavailable(t, w)
	if strings.Contains(w.Body.String(), "grams") || w.Header().Get("Content-Type") == "text/html" || w.Header().Get("Content-Length") != "" {
		t.Fatal("discarded response leaked body or content headers")
	}
}

func TestSchemaGuardResponseBufferIsBounded(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, "")
	a.mux.HandleFunc("GET /oversized-response", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write(make([]byte, maxDynamicResponseBytes+1))
		if !errors.Is(err, errResponseTooLarge) {
			t.Errorf("oversized write = %v", err)
		}
	})
	w := testRequest(t, a, http.MethodGet, "/oversized-response", Session{}, nil, nil)
	if w.Code != http.StatusInternalServerError || w.Body.Len() > 1000 {
		t.Fatalf("oversized response = %d, %d bytes", w.Code, w.Body.Len())
	}
}

func TestSchemaGuardCommittedCheckoutStillReplaysAfterResponseRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s := openTestStore(t, path)
	future := openTestStore(t, path)
	session := testSession(t, s, "")
	testCart(t, s, session.ID, 1, 2)
	session = testSession(t, s, session.ID)
	basket := testBasket(t, s, session.ID)
	a := testApp(t, s, "")
	var orderID int64
	a.mux.HandleFunc("POST /checkout-before-migration", func(w http.ResponseWriter, r *http.Request) {
		var err error
		orderID, err = s.Checkout(session.ID, session.CheckoutKey, session.Revision, basket.Quote)
		if err != nil {
			t.Fatal(err)
		}
		testExec(t, future, fmt.Sprintf(`INSERT INTO schema_version VALUES(%d)`, latestSchemaVersion+1))
		redirect(w, r, "/orders")
	})
	w := testRequest(t, a, http.MethodPost, "/checkout-before-migration", session, nil, nil)
	assertSchemaUnavailable(t, w)
	if orderID == 0 || testCount(t, s, "orders") != 1 {
		t.Fatal("compatible checkout should have committed before migration")
	}
	before := fingerprintTest(t, s.db)
	if _, err := s.Checkout(session.ID, session.CheckoutKey, session.Revision, basket.Quote); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatalf("old replay after migration = %v", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("refused replay changed data")
	}
	// This simulation changes only the version marker. Remove it to represent a
	// compatible client resuming; the existing receipt/key still supplies replay.
	testExec(t, future, fmt.Sprintf(`DELETE FROM schema_version WHERE version=%d`, latestSchemaVersion+1))
	replayed, err := s.Checkout(session.ID, session.CheckoutKey, session.Revision, basket.Quote)
	if err != nil || replayed != orderID || testCount(t, s, "orders") != 1 {
		t.Fatalf("replay lost: %d, %v", replayed, err)
	}
}

func TestSchemaGuardAcrossRunningProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s := openTestStore(t, path)
	session := testSession(t, s, "")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestSchemaGuardProcessHelper$")
	child.Env = append(os.Environ(), "SHOP_SCHEMA_GUARD_TEST_PATH="+path, "SHOP_SCHEMA_GUARD_TEST_SESSION="+session.ID)
	var stderr bytes.Buffer
	child.Stderr = &stderr
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	reader := bufio.NewReader(stdout)
	if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child readiness = %q, %v, %s", line, err, stderr.String())
	}
	testExec(t, s, fmt.Sprintf(`INSERT INTO schema_version VALUES(%d)`, latestSchemaVersion+1))
	before := fingerprintTest(t, s.db)
	if _, err = stdin.Write([]byte("check\n")); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if line, err := reader.ReadString('\n'); err != nil || line != "refused\n" {
		t.Fatalf("child refusal = %q, %v, %s", line, err, stderr.String())
	}
	if err = child.Wait(); err != nil {
		t.Fatalf("child process: %v, %s", err, stderr.String())
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("already-running process wrote after migration")
	}
}

func TestSchemaGuardProcessHelper(t *testing.T) {
	path := os.Getenv("SHOP_SCHEMA_GUARD_TEST_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, _ = os.Stdout.WriteString("ready\n")
	if _, err = bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err = s.Manager(os.Getenv("SHOP_SCHEMA_GUARD_TEST_SESSION"), true); !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatalf("already-running process command = %v", err)
	}
	_, _ = os.Stdout.WriteString("refused\n")
}

func TestSchemaGuardResetRefusesFutureWithoutArchiveOrInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s := openTestStore(t, path)
	testExec(t, s, fmt.Sprintf(`INSERT INTO schema_version VALUES(%d)`, latestSchemaVersion+1))
	before := fingerprintTest(t, s.db)
	result, err := s.resetDemo(DemoResetOptions{}, func(_, _ *sql.DB) error { t.Fatal("incompatible reset reached installation"); return nil })
	if !errors.Is(err, ErrSchemaIncompatible) || result.Reset || result.BackupPath != "" {
		t.Fatalf("reset = %+v, %v", result, err)
	}
	if _, err = os.Stat(path + ".demo-backups"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reset created archive directory: %v", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("reset changed future database")
	}
}

func TestSchemaGuardResetRefusesIncompatibleSourceBeforeCopy(t *testing.T) {
	for _, query := range []string{
		fmt.Sprintf(`DELETE FROM schema_version WHERE version=%d`, latestSchemaVersion),
		fmt.Sprintf(`INSERT INTO schema_version VALUES(%d)`, latestSchemaVersion+1),
		`DELETE FROM schema_version`,
		`DROP TABLE schema_version`,
	} {
		t.Run(query, func(t *testing.T) {
			destination := newTestStore(t)
			source, err := freshDemoDatabase(4096)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			testExec(t, &Store{db: source}, query)
			before := fingerprintTest(t, destination.db)
			sourceBefore := fingerprintTest(t, source)
			err = installDemoBaseline(destination.db, source, func(_, _ *sql.DB) error {
				t.Fatal("incompatible baseline reached live copy")
				return nil
			})
			if !errors.Is(err, ErrSchemaIncompatible) {
				t.Fatalf("baseline error = %v", err)
			}
			if fingerprintTest(t, destination.db) != before || fingerprintTest(t, source) != sourceBefore {
				t.Fatal("refused baseline copy changed source or destination")
			}
		})
	}
}
