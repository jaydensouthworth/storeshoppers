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

func TestManagerMarkPickedStartsAtomicallyAndReplays(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	o := testOrder(t, s, id, owner.ID)
	line := o.WorkingItems[0]
	stock := testProduct(t, s, line.ProductID)
	receipt := placedSnapshot(t, s, id)
	key := token()
	if err := s.MarkWorkingLinePicked(id, line.LineID, line.Quantity+1, line.PickVersion, o.Version, key, owner.ID, false); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if testOrder(t, s, id, owner.ID).Status != "Placed" {
		t.Fatal("invalid pick started order")
	}
	for range 2 {
		if err := s.MarkWorkingLinePicked(id, line.LineID, line.Quantity, line.PickVersion, o.Version, key, owner.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	after := testOrder(t, s, id, owner.ID)
	if after.Status != "Picking" || after.PickedCount != line.Quantity || after.Version != o.Version+1 || testCount(t, s, "order_events") != 1 {
		t.Fatal("bad atomic pick/replay", after)
	}
	if !reflect.DeepEqual(stock, testProduct(t, s, line.ProductID)) || !reflect.DeepEqual(receipt, placedSnapshot(t, s, id)) {
		t.Fatal("picking changed stock or receipt")
	}
	if err := s.MarkWorkingLinePicked(id, line.LineID, 0, line.PickVersion, o.Version, key, owner.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatal("changed payload reused key", err)
	}
	other := testSession(t, s, "")
	if err := s.MarkWorkingLinePicked(id, line.LineID, line.Quantity, line.PickVersion, o.Version, key, other.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("replay bypassed ownership", err)
	}
	if err := s.MarkWorkingLinePicked(id, line.LineID, 0, line.PickVersion, o.Version, token(), owner.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale pick accepted", err)
	}
	c := overrideCommand(t, s, id, owner.ID, "cancel", 0, 0)
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	after = testOrder(t, s, id, owner.ID)
	if err := s.MarkWorkingLinePicked(id, line.LineID, 0, after.WorkingItems[0].PickVersion, after.Version, token(), owner.ID, false); !errors.Is(err, ErrTerminal) {
		t.Fatal("reopened terminal", err)
	}
	if err := s.MarkWorkingLinePicked(id, line.LineID, line.Quantity, line.PickVersion, o.Version, key, owner.ID, false); err != nil {
		t.Fatal("exact retry after terminal", err)
	}
}
func TestManagerMarkPickedConcurrentCommands(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "shop.db")
			first, second := openTestStore(t, path), openTestStore(t, path)
			owner, id := pickingFixture(t, first)
			o := testOrder(t, first, id, owner.ID)
			line := o.WorkingItems[0]
			key := token()
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, s := range []*Store{first, second} {
				copy := key
				if !same && i == 1 {
					copy = token()
				}
				go func(s *Store, key string) {
					<-start
					results <- s.MarkWorkingLinePicked(id, line.LineID, line.Quantity, line.PickVersion, o.Version, key, owner.ID, false)
				}(s, copy)
			}
			close(start)
			wins, conflicts := 0, 0
			for range 2 {
				err := <-results
				if err == nil {
					wins++
				} else if errors.Is(err, ErrConflict) {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			if (same && wins != 2) || (!same && (wins != 1 || conflicts != 1)) || testCount(t, first, "order_events") != 1 || testProduct(t, first, 1).Stock != 22 {
				t.Fatal("race duplicated work")
			}
		})
	}
}
func TestRoutineManagerNotesAndContextualDisposition(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	basket := testBasket(t, s, owner.ID)
	if err := s.ManagerSetBasket(basket.ID, owner.ID, false, 3, 1, basket.Revision, ""); err != nil {
		t.Fatal("optional basket note", err)
	}
	basket = testBasket(t, s, owner.ID)
	if err := s.ManagerRenewBasket(basket.ID, owner.ID, false, basket.Revision, ""); err != nil {
		t.Fatal("optional renewal note", err)
	}
	basket = testBasket(t, s, owner.ID)
	if err := s.ManagerSetBasket(basket.ID, owner.ID, false, 3, 2, basket.Revision, "ok"); err != nil {
		t.Fatal("short basket note", err)
	}
	c := overrideCommand(t, s, id, owner.ID, "set", 1, 3)
	c.Reason = ""
	c.Disposition = ""
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal("increase meaningless disposition", err)
	}
	c = overrideCommand(t, s, id, owner.ID, "set", 1, 2)
	c.Reason = ""
	c.Disposition = ""
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal("unpicked release", err)
	}
	o := testOrder(t, s, id, owner.ID)
	line := o.WorkingItems[0]
	if err := s.MarkWorkingLinePicked(id, line.LineID, 2, line.PickVersion, o.Version, token(), owner.ID, false); err != nil {
		t.Fatal(err)
	}
	c = overrideCommand(t, s, id, owner.ID, "set", 1, 1)
	c.Reason = ""
	c.Disposition = ""
	if err := s.OverrideOrder(id, owner.ID, false, c); !errors.Is(err, ErrPickedDisposition) {
		t.Fatal("picked removal guessed stock", err)
	}
	c.Disposition = "restock"
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	events := testOrder(t, s, id, owner.ID).Events
	if events[0].Reason != "Manager changed working-list quantity" || !strings.Contains(events[0].Details, "picked 2 → 1") {
		t.Fatal("routine audit incomplete")
	}
}
func TestHTTPExpiredCSRFExplicitSameSessionRecovery(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	b := testBasket(t, s, owner.ID)
	oldCSRF := owner.CSRF
	if err := s.Manager(owner.ID, true); err != nil {
		t.Fatal(err)
	}
	fresh := testSession(t, s, owner.ID)
	values := url.Values{"csrf": {oldCSRF}, "revision": {fmt.Sprint(b.Revision)}, "product_id": {"1"}, "quantity": {"2"}, "return": {"cart"}}
	w := testRequest(t, a, "POST", "/cart", owner, values, pickingHeaders(true))
	if w.Code != 403 || w.Header().Get("X-Shop-Error") != "csrf-expired" || w.Header().Get("X-Shop-CSRF") != fresh.CSRF || testBasket(t, s, owner.ID).Count != 1 {
		t.Fatal("unsafe stale-CSRF recovery", w.Code, w.Header())
	}
	values.Set("csrf", w.Header().Get("X-Shop-CSRF"))
	w = testRequest(t, a, "POST", "/cart", owner, values, pickingHeaders(true))
	if w.Code != 200 || testBasket(t, s, owner.ID).Count != 2 {
		t.Fatal("explicit reviewed retry failed")
	}
	testExec(t, s, `UPDATE sessions SET expires=? WHERE id=?`, time.Now().Add(-time.Minute).Unix(), owner.ID)
	count := testCount(t, s, "sessions")
	for range 2 {
		w = testRequest(t, a, "POST", "/cart", owner, values, pickingHeaders(true))
		if w.Code != 403 || w.Header().Get("X-Shop-CSRF") != "" || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("expired identity silently replaced")
		}
	}
	if testCount(t, s, "sessions") != count {
		t.Fatal("rejected mutation minted session")
	}
}
func TestHTTPManagerExpiredAndMarkPickedEnvelope(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner, id := pickingFixture(t, s)
	owner = pickingLogin(t, a, owner)
	o := testOrder(t, s, id, owner.ID)
	line := o.WorkingItems[0]
	path := fmt.Sprintf("/manager/orders/%d/lines/%d/pick", id, line.LineID)
	values := url.Values{"csrf": {owner.CSRF}, "picked": {"2"}, "version": {fmt.Sprint(line.PickVersion)}, "order_version": {fmt.Sprint(o.Version)}, "command_key": {token()}}
	for range 2 {
		w := testRequest(t, a, http.MethodPost, path, owner, values, pickingHeaders(true))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if testOrder(t, s, id, owner.ID).Status != "Picking" || testCount(t, s, "order_events") != 1 {
		t.Fatal("HTTP mark failed")
	}
	testExec(t, s, `UPDATE sessions SET manager_until=? WHERE id=?`, time.Now().Add(-time.Minute).Unix(), owner.ID)
	w := testRequest(t, a, http.MethodPost, path, owner, values, pickingHeaders(true))
	if w.Code != 403 || w.Header().Get("X-Shop-Error") != "manager-expired" || w.Header().Get("HX-Redirect") != "" {
		t.Fatal("expired manager lost entered form", w.Code, w.Header())
	}
}
