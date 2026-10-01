package shop

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func pickingFixture(t *testing.T, s *Store) (Session, int64) {
	t.Helper()
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 2)
	testCart(t, s, owner.ID, 2, 1)
	return owner, testCheckout(t, s, owner.ID)
}

func receiptSnapshot(o Order) Order {
	o.Percent = 0
	o.Status, o.PickedCount, o.AllPicked = "", 0, false
	o.Items = append([]OrderItem(nil), o.Items...)
	for i := range o.Items {
		o.Items[i].Picked, o.Items[i].PickVersion = 0, 0
	}
	return o
}

func TestPickingLifecycleMaintainsReceiptAndStock(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	before := testOrder(t, s, id, owner.ID)
	products, err := s.Products("", "")
	if err != nil {
		t.Fatal(err)
	}
	if before.PickedCount != 0 || before.RequiredCount != 3 || before.AllPicked || len(before.Items) != 2 || before.Items[0].ProductID != 1 || before.Items[0].PickVersion != 1 {
		t.Fatalf("new order picking state = %+v", before)
	}
	if err := s.RecordPicked(id, 1, 1, 1, owner.ID, false); !errors.Is(err, ErrConflict) {
		t.Errorf("pick before Picking = %v, want ErrConflict", err)
	}
	if err := s.AdvanceScoped(id, "Placed", owner.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, advance := range []func() error{
		func() error { return s.AdvanceScoped(id, "Picking", owner.ID, false) },
		func() error { return s.Advance(id, "Picking") },
	} {
		if err := advance(); !errors.Is(err, ErrIncomplete) {
			t.Errorf("unpicked order advanced to Ready: %v", err)
		}
	}
	for _, pick := range []struct{ pid, quantity, version int64 }{
		{1, 1, 1}, // Partial progress.
		{1, 1, 2}, // A fresh form sets the absolute count, not an increment.
		{1, 0, 3}, // Corrections may reduce the count while still Picking.
		{1, 2, 4},
	} {
		if err := s.RecordPicked(id, pick.pid, pick.quantity, pick.version, owner.ID, false); err != nil {
			t.Fatal(err)
		}
		o := testOrder(t, s, id, owner.ID)
		if o.PickedCount != pick.quantity || o.Items[0].Picked != pick.quantity || o.Items[0].PickVersion != pick.version+1 || o.AllPicked {
			t.Errorf("absolute pick update produced wrong progress: %+v", o)
		}
		if err := s.AdvanceScoped(id, "Picking", owner.ID, false); !errors.Is(err, ErrIncomplete) {
			t.Errorf("partially picked order advanced to Ready: %v", err)
		}
	}
	if err := s.RecordPicked(id, 2, 1, 1, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	if o := testOrder(t, s, id, owner.ID); !o.AllPicked || o.PickedCount != 3 || o.RequiredCount != 3 {
		t.Errorf("fully picked order has wrong totals: %+v", o)
	}
	listed, err := s.Orders(owner.ID, false)
	if err != nil || len(listed) != 1 || listed[0].PickedCount != 3 || listed[0].RequiredCount != 3 || !listed[0].AllPicked {
		t.Errorf("queue progress differs from order: %+v, %v", listed, err)
	}
	for _, stage := range []struct{ from, to string }{{"Picking", "Ready"}, {"Ready", "Completed"}} {
		if err := s.AdvanceScoped(id, stage.from, owner.ID, false); err != nil {
			t.Fatalf("advance %s: %v", stage.from, err)
		}
		if got := testOrder(t, s, id, owner.ID).Status; got != stage.to {
			t.Errorf("status = %s, want %s", got, stage.to)
		}
		if err := s.AdvanceScoped(id, stage.from, owner.ID, false); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate %s transition = %v, want ErrConflict", stage.from, err)
		}
		if err := s.RecordPicked(id, 1, 0, 5, owner.ID, false); !errors.Is(err, ErrConflict) {
			t.Errorf("pick changed after %s: %v", stage.to, err)
		}
	}
	for _, from := range []string{"Completed", "Cancelled", "", "picking"} {
		if err := s.AdvanceScoped(id, from, owner.ID, false); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid transition %q = %v, want ErrInvalid", from, err)
		}
	}
	if after := receiptSnapshot(testOrder(t, s, id, owner.ID)); !reflect.DeepEqual(receiptSnapshot(before), after) {
		t.Error("picking changed immutable order identity, timestamps, quantities, or prices")
	}
	afterProducts, err := s.Products("", "")
	if err != nil || !reflect.DeepEqual(products, afterProducts) {
		t.Errorf("picking changed inventory or product versions: %v", err)
	}
	if got := testCount(t, s, "adjustments"); got != 0 {
		t.Error("picking created an inventory adjustment")
	}
}

func TestPickingRejectsInvalidAndStaleWrites(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	if err := s.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPicked(id, 1, 1, 1, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	before := testOrder(t, s, id, owner.ID)
	for _, tc := range []struct {
		name                 string
		pid, picked, version int64
		want                 error
	}{
		{"negative", 1, -1, 2, ErrInvalid},
		{"over ordered quantity", 1, 3, 2, ErrInvalid},
		{"over absolute limit", 1, 100, 2, ErrInvalid},
		{"zero version", 1, 1, 0, ErrInvalid},
		{"negative version", 1, 1, -1, ErrInvalid},
		{"duplicate form", 1, 1, 1, ErrConflict},
		{"stale correction", 1, 0, 1, ErrConflict},
		{"future version", 1, 2, 3, ErrConflict},
		{"product not on order", 3, 1, 1, ErrNotFound},
		{"unknown product", 9999, 1, 1, ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.RecordPicked(id, tc.pid, tc.picked, tc.version, owner.ID, false); !errors.Is(err, tc.want) {
				t.Errorf("RecordPicked = %v, want %v", err, tc.want)
			}
			if after := testOrder(t, s, id, owner.ID); !reflect.DeepEqual(before, after) {
				t.Error("rejected pick changed order or progress")
			}
		})
	}
}

func TestPickingScopedOwnershipAndEmptyOrderGuard(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	other := testSession(t, s, "")
	for _, sid := range []string{other.ID, "", strings.Repeat("f", 64)} {
		if err := s.AdvanceScoped(id, "Placed", sid, false); !errors.Is(err, ErrNotFound) {
			t.Errorf("unowned advance = %v, want ErrNotFound", err)
		}
		if err := s.RecordPicked(id, 1, 1, 1, sid, false); !errors.Is(err, ErrNotFound) {
			t.Errorf("unowned pick = %v, want ErrNotFound", err)
		}
	}
	if err := s.AdvanceScoped(id+100, "Placed", owner.ID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown order advance = %v, want ErrNotFound", err)
	}
	if err := s.RecordPicked(id+100, 1, 1, 1, owner.ID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown order pick = %v, want ErrNotFound", err)
	}
	if err := s.AdvanceScoped(id, "Placed", other.ID, true); err != nil {
		t.Fatalf("authorized store-wide manager advance: %v", err)
	}
	if err := s.RecordPicked(id, 1, 2, 1, other.ID, true); err != nil {
		t.Fatalf("authorized store-wide manager pick: %v", err)
	}
	// A malformed empty historical order must not be treated as fully picked.
	testExec(t, s, `INSERT INTO orders(reference,session_id,checkout_key,total,status) VALUES('EMPTY-LEGACY',?,'empty-key',100,'Picking')`, owner.ID)
	var emptyID int64
	if err := s.db.QueryRow(`SELECT id FROM orders WHERE reference='EMPTY-LEGACY'`).Scan(&emptyID); err != nil {
		t.Fatal(err)
	}
	if err := s.Advance(emptyID, "Picking"); !errors.Is(err, ErrIncomplete) {
		t.Errorf("empty order became Ready: %v", err)
	}
	if o := testOrder(t, s, emptyID, owner.ID); o.AllPicked || o.Status != "Picking" {
		t.Errorf("empty order claims completion: %+v", o)
	}
}

func TestPickingConcurrentSameVersionHasOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	first, second := openTestStore(t, path), openTestStore(t, path)
	owner, id := pickingFixture(t, first)
	if err := first.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, store := range []*Store{first, second} {
		go func(s *Store, picked int64) {
			<-start
			results <- s.RecordPicked(id, 1, picked, 1, owner.ID, false)
		}(store, int64(i+1))
	}
	close(start)
	winners, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Errorf("concurrent pick: %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Errorf("concurrent updates: %d successes, %d conflicts; want one each", winners, conflicts)
	}
	o := testOrder(t, first, id, owner.ID)
	if o.Items[0].PickVersion != 2 || (o.Items[0].Picked != 1 && o.Items[0].Picked != 2) {
		t.Errorf("concurrent picks produced invalid state: %+v", o.Items[0])
	}
	if p := testProduct(t, first, 1); p.Stock != 22 || p.Version != 2 {
		t.Errorf("concurrent picks changed inventory: %+v", p)
	}
}

func pickingApp(t *testing.T, s *Store, demo bool) *App {
	t.Helper()
	a, err := New(s, Config{Origin: testOrigin, ManagerPassword: testManagerPassword, DemoMode: demo})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func pickingLogin(t *testing.T, a *App, owner Session) Session {
	t.Helper()
	w := testRequest(t, a, http.MethodPost, "/manager/login", owner, url.Values{"csrf": {owner.CSRF}, "password": {testManagerPassword}}, nil)
	assertRedirect(t, w, "/manager", false)
	manager := testSession(t, a.store, owner.ID)
	if manager.ID != owner.ID || manager.ManagerUntil <= time.Now().Unix() || manager.CSRF == owner.CSRF {
		t.Fatal("manager login lost the customer session or did not grant access")
	}
	return manager
}

func pickingHeaders(htmx bool) map[string]string {
	if htmx {
		return map[string]string{"HX-Request": "true"}
	}
	return nil
}

func TestHTTPDemoManagersOnlyAccessOwnOrders(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	first, firstID := pickingFixture(t, s)
	second, secondID := pickingFixture(t, s)
	first, second = pickingLogin(t, a, first), pickingLogin(t, a, second)
	firstOrder, secondOrder := testOrder(t, s, firstID, first.ID), testOrder(t, s, secondID, second.ID)
	for _, visitor := range []struct {
		session    Session
		own, other Order
	}{{first, firstOrder, secondOrder}, {second, secondOrder, firstOrder}} {
		for _, htmx := range []bool{false, true} {
			headers := pickingHeaders(htmx)
			for _, path := range []string{"/manager", "/orders"} {
				w := testRequest(t, a, http.MethodGet, path, visitor.session, nil, headers)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), visitor.own.Reference) || strings.Contains(w.Body.String(), visitor.other.Reference) {
					t.Errorf("demo list %s (HX=%t) exposed wrong queue: status %d", path, htmx, w.Code)
				}
			}
			for _, pattern := range []string{"/orders/%d", "/orders/%d/status", "/manager/orders/%d"} {
				w := testRequest(t, a, http.MethodGet, fmt.Sprintf(pattern, visitor.other.ID), visitor.session, nil, headers)
				if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), visitor.other.Reference) {
					t.Errorf("demo cross-session read %s (HX=%t) = %d, want 404", pattern, htmx, w.Code)
				}
				w = testRequest(t, a, http.MethodGet, fmt.Sprintf(pattern, visitor.own.ID), visitor.session, nil, headers)
				if w.Code != http.StatusOK {
					t.Errorf("demo owner read %s (HX=%t) = %d", pattern, htmx, w.Code)
				}
			}
			for _, targetID := range []int64{visitor.other.ID, 99999} {
				for _, suffix := range []string{"advance", "items/1"} {
					w := testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/orders/%d/%s", targetID, suffix), visitor.session,
						url.Values{"csrf": {visitor.session.CSRF}, "status": {"Placed"}, "picked": {"1"}, "version": {"1"}}, headers)
					if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), visitor.other.Reference) {
						t.Errorf("demo cross-session mutation %d/%s (HX=%t) = %d, want 404", targetID, suffix, htmx, w.Code)
					}
				}
			}
		}
	}
	if got := testOrder(t, s, firstID, first.ID); !reflect.DeepEqual(got, firstOrder) {
		t.Error("second demo manager changed first visitor's order")
	}
	if got := testOrder(t, s, secondID, second.ID); !reflect.DeepEqual(got, secondOrder) {
		t.Error("first demo manager changed second visitor's order")
	}
	// Inventory deliberately remains the shared store inventory in demo mode.
	p := testProduct(t, s, 1)
	w := testRequest(t, a, http.MethodPost, "/manager/inventory", second,
		url.Values{"csrf": {second.CSRF}, "product_id": {"1"}, "delta": {"1"}, "version": {fmt.Sprint(p.Version)}, "reason": {"Shared demo restock"}}, nil)
	assertRedirect(t, w, "/manager/stock", false)
	if got := testProduct(t, s, 1); got.Stock != p.Stock+1 {
		t.Error("demo privacy isolation incorrectly isolated global inventory")
	}
}

func TestHTTPAbsentSessionCannotUseGuessedOrders(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner, id := pickingFixture(t, s)
	owner = pickingLogin(t, a, owner)
	before := testOrder(t, s, id, owner.ID)
	for _, session := range []Session{{}, {ID: strings.Repeat("f", 64)}} {
		for _, htmx := range []bool{false, true} {
			headers := pickingHeaders(htmx)
			for _, suffix := range []string{"", "/status"} {
				w := testRequest(t, a, http.MethodGet, fmt.Sprintf("/orders/%d%s", id, suffix), session, nil, headers)
				if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), before.Reference) {
					t.Errorf("absent session read receipt/status: %d", w.Code)
				}
			}
			w := testRequest(t, a, http.MethodGet, fmt.Sprintf("/manager/orders/%d", id), session, nil, headers)
			assertRedirect(t, w, "/manager/login", htmx)
			for _, suffix := range []string{"advance", "items/1"} {
				w = testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/orders/%d/%s", id, suffix), session,
					url.Values{"csrf": {owner.CSRF}, "status": {"Placed"}, "picked": {"1"}, "version": {"1"}}, headers)
				if w.Code != http.StatusForbidden {
					t.Errorf("absent session action %s = %d, want 403", suffix, w.Code)
				}
			}
		}
	}
	// An expired browser cookie must also lose its old ownership and manager grant.
	testExec(t, s, `UPDATE sessions SET expires=? WHERE id=?`, time.Now().Add(-time.Second).Unix(), owner.ID)
	w := testRequest(t, a, http.MethodGet, fmt.Sprintf("/orders/%d/status", id), owner, nil, pickingHeaders(true))
	if w.Code != http.StatusNotFound {
		t.Errorf("expired session retained order access: %d", w.Code)
	}
	w = testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/orders/%d/advance", id), owner, url.Values{"csrf": {owner.CSRF}, "status": {"Placed"}}, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("expired session retained mutation access: %d", w.Code)
	}
	if after := testOrder(t, s, id, owner.ID); !reflect.DeepEqual(before, after) {
		t.Error("absent or expired session changed order")
	}
}

func TestHTTPNormalManagerCanFulfillOtherSessionOrder(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, false)
	owner, id := pickingFixture(t, s)
	manager := pickingLogin(t, a, testSession(t, s, ""))
	order := testOrder(t, s, id, owner.ID)
	for _, path := range []string{"/manager", fmt.Sprintf("/orders/%d", id), fmt.Sprintf("/manager/orders/%d", id), fmt.Sprintf("/orders/%d/status", id)} {
		w := testRequest(t, a, http.MethodGet, path, manager, nil, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), order.Reference) {
			t.Errorf("normal manager lost authorized order access at %s: %d", path, w.Code)
		}
	}
	detailPath := fmt.Sprintf("/manager/orders/%d", id)
	w := testRequest(t, a, http.MethodPost, detailPath+"/advance", manager, url.Values{"csrf": {manager.CSRF}, "status": {"Placed"}}, nil)
	assertRedirect(t, w, detailPath, false)
	for _, item := range order.Items {
		w = testRequest(t, a, http.MethodPost, fmt.Sprintf("%s/items/%d", detailPath, item.ProductID), manager,
			url.Values{"csrf": {manager.CSRF}, "picked": {fmt.Sprint(item.Quantity)}, "version": {fmt.Sprint(item.PickVersion)}}, nil)
		assertRedirect(t, w, detailPath, false)
	}
	for _, stage := range []string{"Picking", "Ready"} {
		w = testRequest(t, a, http.MethodPost, detailPath+"/advance", manager, url.Values{"csrf": {manager.CSRF}, "status": {stage}}, nil)
		assertRedirect(t, w, detailPath, false)
	}
	if o := testOrder(t, s, id, owner.ID); o.Status != "Completed" || !o.AllPicked {
		t.Errorf("normal manager could not fulfill another session order: %+v", o)
	}
}

func TestHTTPPickingFormValidationAndGuards(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner, id := pickingFixture(t, s)
	owner = pickingLogin(t, a, owner)
	if err := s.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/manager/orders/%d/items/1", id)
	before := testOrder(t, s, id, owner.ID)
	for _, token := range []string{"", "wrong", testSession(t, s, "").CSRF} {
		w := testRequest(t, a, http.MethodPost, path, owner, url.Values{"csrf": {token}, "picked": {"1"}, "version": {"1"}}, nil)
		if w.Code != http.StatusForbidden {
			t.Errorf("pick accepted invalid CSRF: %d", w.Code)
		}
	}
	w := testRequest(t, a, http.MethodPost, path+"?csrf="+owner.CSRF, owner, url.Values{"picked": {"1"}, "version": {"1"}}, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("pick accepted query-only CSRF: %d", w.Code)
	}
	for _, htmx := range []bool{false, true} {
		for _, fields := range []struct{ picked, version string }{
			{"-1", "1"}, {"3", "1"}, {"100", "1"}, {"1.5", "1"}, {"nope", "1"}, {"", "1"}, {"9223372036854775808", "1"},
			{"1", "0"}, {"1", "2"}, {"1", "nope"}, {"1", ""}, {"1", "9223372036854775808"},
		} {
			w = testRequest(t, a, http.MethodPost, path, owner, url.Values{"csrf": {owner.CSRF}, "picked": {fields.picked}, "version": {fields.version}}, pickingHeaders(htmx))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `role="alert"`) {
				t.Errorf("invalid picking form (%q,%q) HX=%t did not render error: %d", fields.picked, fields.version, htmx, w.Code)
			}
		}
		w = testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/orders/%d/advance", id), owner, url.Values{"csrf": {owner.CSRF}, "status": {"Picking"}}, pickingHeaders(htmx))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrIncomplete.Error()) {
			t.Errorf("incomplete Ready did not render actionable error: %d", w.Code)
		}
	}
	if after := testOrder(t, s, id, owner.ID); !reflect.DeepEqual(before, after) {
		t.Error("invalid form or incomplete transition changed order")
	}
	// A valid CSRF token never substitutes for a manager grant.
	if err := s.Manager(owner.ID, false); err != nil {
		t.Fatal(err)
	}
	owner = testSession(t, s, owner.ID)
	for _, htmx := range []bool{false, true} {
		w = testRequest(t, a, http.MethodPost, path, owner, url.Values{"csrf": {owner.CSRF}, "picked": {"1"}, "version": {"1"}}, pickingHeaders(htmx))
		assertRedirect(t, w, "/manager/login", htmx)
	}
	if after := testOrder(t, s, id, owner.ID); !reflect.DeepEqual(before, after) {
		t.Error("customer session without manager grant changed picking progress")
	}
}

func TestHTTPStatusFragmentProgressAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s := openTestStore(t, path)
	a := pickingApp(t, s, true)
	owner, id := pickingFixture(t, s)
	owner = pickingLogin(t, a, owner)
	detailPath, statusPath := fmt.Sprintf("/manager/orders/%d", id), fmt.Sprintf("/orders/%d/status", id)
	w := testRequest(t, a, http.MethodPost, detailPath+"/advance", owner, url.Values{"csrf": {owner.CSRF}, "status": {"Placed"}}, pickingHeaders(true))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="workspace"`) {
		t.Fatalf("HTMX start-picking failed: %d", w.Code)
	}
	w = testRequest(t, a, http.MethodPost, detailPath+"/items/1", owner, url.Values{"csrf": {owner.CSRF}, "picked": {"1"}, "version": {"1"}}, pickingHeaders(true))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="workspace"`) {
		t.Fatalf("HTMX pick save failed: %d", w.Code)
	}
	// Replayed HTMX forms cannot increment twice or overwrite newer progress.
	w = testRequest(t, a, http.MethodPost, detailPath+"/items/1", owner, url.Values{"csrf": {owner.CSRF}, "picked": {"2"}, "version": {"1"}}, pickingHeaders(true))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ErrConflict.Error()) {
		t.Errorf("stale HTMX pick did not show conflict: %d", w.Code)
	}
	before := testOrder(t, s, id, owner.ID)
	if before.Status != "Picking" || before.PickedCount != 1 || before.Items[0].PickVersion != 2 {
		t.Fatalf("HTTP progress = %+v", before)
	}
	testCart(t, s, owner.ID, 3, 1)
	beforeSession := testSession(t, s, owner.ID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, path)
	a = pickingApp(t, s, true)
	if after := testOrder(t, s, id, owner.ID); !reflect.DeepEqual(before, after) {
		t.Error("restart lost picked quantity, version, or receipt")
	}
	if after := testSession(t, s, owner.ID); after != beforeSession {
		t.Error("restart lost the shared customer/manager session")
	}
	if b := testBasket(t, s, owner.ID); b.Count != 1 || len(b.Lines) != 1 || b.Lines[0].Product.ID != 3 {
		t.Error("restart lost the customer's newer basket")
	}
	for _, htmx := range []bool{false, true} {
		w = testRequest(t, a, http.MethodGet, statusPath, owner, nil, pickingHeaders(htmx))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `id="order-status"`) || !strings.Contains(body, "1 of 3") || !strings.Contains(body, "Picking") {
			t.Errorf("persisted status progress missing (HX=%t): %d", htmx, w.Code)
		}
		if htmx && (strings.Contains(body, `id="workspace"`) || strings.Contains(strings.ToLower(body), "<!doctype")) {
			t.Error("status fragment incorrectly returned a full workspace or document")
		}
		if !htmx && (!strings.Contains(strings.ToLower(body), "<!doctype") || !strings.Contains(body, before.Reference) || !strings.Contains(body, "$9.97")) {
			t.Error("non-JavaScript status fallback did not return a complete receipt page")
		}
		if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Vary"), "HX-Request") {
			t.Error("private status representation lacks cache isolation headers")
		}
	}
	w = testRequest(t, a, http.MethodGet, detailPath, owner, nil, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), before.Reference) {
		t.Error("persisted browser manager session cannot open picking ticket after restart")
	}
}
