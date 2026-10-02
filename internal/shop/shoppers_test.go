package shop

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func shopperCommand(t *testing.T, s *Store, id int64, sid, action string, shopper int64) ShopperCommand {
	t.Helper()
	o := testOrder(t, s, id, sid)
	c := ShopperCommand{Action: action, Key: token(), Reason: "Manager verified the task handover", Version: o.Version, ShopperID: shopper}
	if o.Assignment != nil {
		c.AssignmentVersion = o.Assignment.Version
	}
	return c
}
func testAssign(t *testing.T, s *Store, id int64, sid string, shopper int64) {
	t.Helper()
	if err := s.AssignShopper(id, sid, false, shopperCommand(t, s, id, sid, "assign", shopper)); err != nil {
		t.Fatal(err)
	}
}
func shopperWorkspace(t *testing.T, s *Store, sid string, all bool, values url.Values, selected int64) *ShoppersWorkspace {
	t.Helper()
	w, err := s.Shoppers(parseShopperFilters(values), sid, all, selected)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func TestShoppersAssignmentLifecycleReplayAndInventoryInvariants(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	original := testOrder(t, s, id, owner.ID)
	stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
	receipt := placedSnapshot(t, s, id)
	c := shopperCommand(t, s, id, owner.ID, "assign", 1)
	for range 2 {
		if err := s.AssignShopper(id, owner.ID, false, c); err != nil {
			t.Fatal("assign/replay", err)
		}
	}
	assigned := testOrder(t, s, id, owner.ID)
	if assigned.Assignment == nil || assigned.Assignment.ShopperID != 1 || assigned.Assignment.Version != 1 || assigned.Version != original.Version+1 || testCount(t, s, "shopper_assignments") != 1 || testCount(t, s, "order_events") != 1 || testCount(t, s, "shopper_event_links") != 1 {
		t.Fatal("assignment/replay changed unexpected state", assigned)
	}
	c.ShopperID = 2
	if err := s.AssignShopper(id, owner.ID, false, c); !errors.Is(err, ErrConflict) {
		t.Fatal("reused command key", err)
	}
	c = shopperCommand(t, s, id, owner.ID, "reassign", 2)
	if err := s.AssignShopper(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	changed := testOrder(t, s, id, owner.ID)
	if changed.Assignment.ID != assigned.Assignment.ID || changed.Assignment.Version != 2 || changed.Assignment.ShopperID != 2 {
		t.Fatal("reassignment identity/version lost")
	}
	if !strings.Contains(changed.Events[0].Details, "Avery Morgan → Jordan Lee") {
		t.Fatal("previous assignee missing")
	}
	if err := s.AssignShopper(id, owner.ID, false, c); err != nil {
		t.Fatal("reassign replay", err)
	}
	// A cancelled task keeps the order fully intact and can be followed by a new task.
	c = shopperCommand(t, s, id, owner.ID, "cancel", 2)
	if err := s.AssignShopper(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	cancelled := testOrder(t, s, id, owner.ID)
	if cancelled.Assignment != nil || cancelled.Status != "Placed" || cancelled.CompletionKind != "" || !reflect.DeepEqual(cancelled.WorkingItems, original.WorkingItems) || !reflect.DeepEqual(receipt, placedSnapshot(t, s, id)) || !reflect.DeepEqual(stock, migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)) {
		t.Fatal("task cancellation touched order or allocation")
	}
	if err := s.AssignShopper(id, owner.ID, false, c); err != nil {
		t.Fatal("cancel replay", err)
	}
	w := shopperWorkspace(t, s, owner.ID, false, nil, id)
	if w.OpenOrders != 1 || w.AssignedOrders != 0 || w.UnassignedOrders != 1 || w.Selected.Assignment != nil || w.Roster[1].ActiveOrders != 0 {
		t.Fatal("cancel did not leave unassigned next step", w)
	}
	testAssign(t, s, id, owner.ID, 3)
	if testOrder(t, s, id, owner.ID).Assignment.ID == assigned.Assignment.ID {
		t.Fatal("new task resurrected cancelled identity")
	}
	// Exact prior replay cannot cancel the replacement task.
	if err := s.AssignShopper(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	if testOrder(t, s, id, owner.ID).Assignment.ShopperID != 3 {
		t.Fatal("old replay cancelled new task")
	}
}
func TestShoppersRosterProgressScopesFiltersPaginationAndHistory(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	other, otherID := pickingFixture(t, s)
	testAssign(t, s, id, owner.ID, 1)
	testAssign(t, s, otherID, other.ID, 1)
	if err := s.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPicked(id, 1, 1, 1, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `UPDATE products SET stock=100 WHERE id=3`)
	for i := 0; i < 22; i++ {
		testCart(t, s, owner.ID, 3, 1)
		oid := testCheckout(t, s, owner.ID)
		if i < 2 {
			testAssign(t, s, oid, owner.ID, 1)
		}
	}
	w := shopperWorkspace(t, s, owner.ID, false, nil, 0)
	if w.Total != 23 || w.OpenOrders != 23 || w.AssignedOrders != 3 || w.UnassignedOrders != 20 || len(w.Tasks) != 20 || w.Pages != 2 || w.First != 1 || w.Last != 20 || w.NextURL == "" {
		t.Fatal("scoped pagination/counts", w)
	}
	if w.Roster[0].ActiveOrders != 3 || w.Roster[0].Picked != 1 || w.Roster[0].Required != 5 || w.Roster[0].Percent != 20 {
		t.Fatal("roster progress is not recorded scoped order progress", w.Roster)
	}
	page := shopperWorkspace(t, s, owner.ID, false, url.Values{"page": {"99"}}, id)
	if page.Page != 2 || len(page.Tasks) != 3 || page.First != 21 || page.Last != 23 || page.Selected.Order.ID != id || page.Selected.Order.PickedCount != 1 {
		t.Fatal("selected outside page failed")
	}
	for _, task := range page.Tasks {
		if task.Order.ID == otherID {
			t.Fatal("foreign row leaked")
		}
	}
	c := shopperCommand(t, s, id, owner.ID, "reassign", 2)
	c.Reason = "Private owner reassignment"
	if err := s.AssignShopper(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	filtered := shopperWorkspace(t, s, owner.ID, false, url.Values{"shopper": {"1"}, "status": {"all"}}, 0)
	found := false
	for _, event := range filtered.Events {
		if event.Reason == c.Reason {
			found = true
		}
	}
	if !found {
		t.Fatal("prior assignee history lost on reassign")
	}
	foreign := shopperWorkspace(t, s, other.ID, false, url.Values{"q": {c.Reason}, "status": {"all"}}, 0)
	if foreign.Total != 0 || len(foreign.Events) != 0 || foreign.OpenOrders != 1 || foreign.Roster[0].ActiveOrders != 1 {
		t.Fatal("private search/count/history leaked", foreign)
	}
	if _, err := s.Shoppers(parseShopperFilters(nil), other.ID, false, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("guessed selected order exposed", err)
	}
	all := shopperWorkspace(t, s, other.ID, true, nil, id)
	if all.Total != 24 || all.OpenOrders != 24 || all.Selected.Order.ID != id {
		t.Fatal("normal manager not store wide")
	}
	for _, status := range []string{"active", "unassigned"} {
		result := shopperWorkspace(t, s, owner.ID, false, url.Values{"status": {status}}, 0)
		want := int64(3)
		if status == "unassigned" {
			want = 20
		}
		if result.Total != want {
			t.Fatal(status, result.Total)
		}
	}
}
func TestShoppersAutoEndAndAuditFailureRollsBackClosure(t *testing.T) {
	for _, outcome := range []string{"Ready", "finish", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			s := newTestStore(t)
			owner, id := pickingFixture(t, s)
			testAssign(t, s, id, owner.ID, 1)
			if err := s.Advance(id, "Placed"); err != nil {
				t.Fatal(err)
			}
			if outcome == "Ready" {
				for _, line := range testOrder(t, s, id, owner.ID).WorkingItems {
					if err := s.RecordPicked(id, line.ProductID, line.Quantity, line.PickVersion, owner.ID, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			command := overrideCommand(t, s, id, owner.ID, outcome, 0, 0)
			perform := func() error {
				if outcome == "Ready" {
					return s.AdvanceVersioned(id, "Picking", command.Version, owner.ID, false)
				}
				return s.OverrideOrder(id, owner.ID, false, command)
			}
			testExec(t, s, `CREATE TRIGGER reject_shopper_end BEFORE INSERT ON shopper_event_links BEGIN SELECT RAISE(ABORT,'assignment audit unavailable'); END`)
			before, _ := databaseFingerprint(s.db)
			if err := perform(); err == nil {
				t.Fatal("audit failure accepted")
			}
			after, _ := databaseFingerprint(s.db)
			if before != after {
				t.Fatal("closure/stock/assignment partially committed")
			}
			testExec(t, s, `DROP TRIGGER reject_shopper_end`)
			if err := perform(); err != nil {
				t.Fatal(err)
			}
			o := testOrder(t, s, id, owner.ID)
			if o.Assignment != nil {
				t.Fatal("terminal order retained task")
			}
			var state string
			var version int64
			if err := s.db.QueryRow(`SELECT state,version FROM shopper_assignments WHERE order_id=?`, id).Scan(&state, &version); err != nil || state != "ended" || version != 2 {
				t.Fatal("autoend state", state, version, err)
			}
			if outcome == "Ready" {
				if err := s.Advance(id, "Ready"); err != nil {
					t.Fatal(err)
				}
				if testCount(t, s, "shopper_event_links") != 2 {
					t.Fatal("collection ended twice")
				}
			}
			c := shopperCommand(t, s, id, owner.ID, "assign", 2)
			if err := s.AssignShopper(id, owner.ID, false, c); !errors.Is(err, ErrTerminal) {
				t.Fatal("closed order assigned", err)
			}
		})
	}
}
func TestShoppersCommandsRollbackAndRejectStaleEdits(t *testing.T) {
	for _, action := range []string{"assign", "reassign", "cancel"} {
		t.Run(action, func(t *testing.T) {
			s := newTestStore(t)
			owner, id := pickingFixture(t, s)
			if action != "assign" {
				testAssign(t, s, id, owner.ID, 1)
			}
			c := shopperCommand(t, s, id, owner.ID, action, 2)
			testExec(t, s, `CREATE TRIGGER reject_assignment_audit BEFORE INSERT ON shopper_event_links BEGIN SELECT RAISE(ABORT,'audit failed'); END`)
			before, _ := databaseFingerprint(s.db)
			if err := s.AssignShopper(id, owner.ID, false, c); err == nil {
				t.Fatal("audit ignored")
			}
			after, _ := databaseFingerprint(s.db)
			if before != after {
				t.Fatal("partial assignment commit")
			}
			testExec(t, s, `DROP TRIGGER reject_assignment_audit`)
			if err := s.Advance(id, "Placed"); err != nil {
				t.Fatal(err)
			}
			if err := s.AssignShopper(id, owner.ID, false, c); !errors.Is(err, ErrConflict) {
				t.Fatal("stale order revision accepted", err)
			}
			c = shopperCommand(t, s, id, owner.ID, action, 2)
			c.AssignmentVersion++
			if err := s.AssignShopper(id, owner.ID, false, c); !errors.Is(err, ErrConflict) {
				t.Fatal("stale task revision accepted", err)
			}
			c = shopperCommand(t, s, id, owner.ID, action, 2)
			if err := s.AssignShopper(id, owner.ID, false, c); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestShoppersTwoConnectionRacesWithClosureAndEachOther(t *testing.T) {
	for _, action := range []string{"assign", "reassign", "cancel"} {
		t.Run(action, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "shop.db")
			first, second := openTestStore(t, path), openTestStore(t, path)
			owner, id := pickingFixture(t, first)
			if action != "assign" {
				testAssign(t, first, id, owner.ID, 1)
			}
			c := shopperCommand(t, first, id, owner.ID, action, 2)
			finish := overrideCommand(t, first, id, owner.ID, "finish", 0, 0)
			start := make(chan struct{})
			results := make(chan error, 2)
			go func() { <-start; results <- first.AssignShopper(id, owner.ID, false, c) }()
			go func() { <-start; results <- second.OverrideOrder(id, owner.ID, false, finish) }()
			close(start)
			wins := 0
			for range 2 {
				err := <-results
				if err == nil {
					wins++
				} else if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrTerminal) {
					t.Fatal(err)
				}
			}
			if wins != 1 {
				t.Fatal("both stale commands won", wins)
			}
			o := testOrder(t, first, id, owner.ID)
			if o.Status != "Completed" {
				finish = overrideCommand(t, second, id, owner.ID, "finish", 0, 0)
				if err := second.OverrideOrder(id, owner.ID, false, finish); err != nil {
					t.Fatal(err)
				}
			}
			if testOrder(t, first, id, owner.ID).Assignment != nil {
				t.Fatal("racing task survived closure")
			}
		})
	}
	t.Run("identical replay", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "shop.db")
		first, second := openTestStore(t, path), openTestStore(t, path)
		owner, id := pickingFixture(t, first)
		c := shopperCommand(t, first, id, owner.ID, "assign", 1)
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, s := range []*Store{first, second} {
			go func(s *Store) { <-start; results <- s.AssignShopper(id, owner.ID, false, c) }(s)
		}
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		if testCount(t, first, "shopper_assignments") != 1 || testCount(t, first, "order_events") != 1 {
			t.Fatal("replay duplicated task")
		}
	})
	t.Run("competing assignees", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "shop.db")
		first, second := openTestStore(t, path), openTestStore(t, path)
		owner, id := pickingFixture(t, first)
		one := shopperCommand(t, first, id, owner.ID, "assign", 1)
		two := shopperCommand(t, first, id, owner.ID, "assign", 2)
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; results <- first.AssignShopper(id, owner.ID, false, one) }()
		go func() { <-start; results <- second.AssignShopper(id, owner.ID, false, two) }()
		close(start)
		wins := 0
		for range 2 {
			err := <-results
			if err == nil {
				wins++
			} else if !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
		}
		if wins != 1 || testCount(t, first, "shopper_assignments") != 1 {
			t.Fatal("multiple task owners")
		}
	})
}
func TestShoppersScopeBeforeValidationAndInvalidCommands(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	other := testSession(t, s, "")
	if err := s.AssignShopper(id, other.ID, false, ShopperCommand{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign malformed command leaked state", err)
	}
	for i, change := range []func(*ShopperCommand){func(c *ShopperCommand) { c.Reason = "x" }, func(c *ShopperCommand) { c.Reason = "bad\nreason" }, func(c *ShopperCommand) { c.Key = "short" }, func(c *ShopperCommand) { c.ShopperID = 999 }, func(c *ShopperCommand) { c.Action = "delete" }, func(c *ShopperCommand) { c.Version = 0 }} {
		c := shopperCommand(t, s, id, owner.ID, "assign", 1)
		change(&c)
		if err := s.AssignShopper(id, owner.ID, false, c); !errors.Is(err, ErrInvalid) {
			t.Fatal(fmt.Sprint(i), err)
		}
	}
	if testCount(t, s, "shopper_assignments") != 0 || testCount(t, s, "order_events") != 0 {
		t.Fatal("invalid commands changed state")
	}
}

func TestShoppersClosedOrderHistoricalFiltersRemainScopedAndDistinct(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	other, foreignID := pickingFixture(t, s)
	testAssign(t, s, id, owner.ID, 1)
	testAssign(t, s, foreignID, other.ID, 1)
	// Several events for the same historical shopper must never duplicate rows or counts.
	for _, shopper := range []int64{2, 1, 2} {
		c := shopperCommand(t, s, id, owner.ID, "reassign", shopper)
		if err := s.AssignShopper(id, owner.ID, false, c); err != nil {
			t.Fatal(err)
		}
	}
	c := shopperCommand(t, s, id, owner.ID, "cancel", 2)
	if err := s.AssignShopper(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	// Open orders still use current assignment semantics even after cancellation.
	for _, status := range []string{"open", "active", "unassigned", "all"} {
		w := shopperWorkspace(t, s, owner.ID, false, url.Values{"status": {status}, "shopper": {"1"}}, 0)
		if w.Total != 0 {
			t.Fatal("open order matched old shopper", status, w.Total)
		}
	}
	for _, oid := range []int64{id, foreignID} {
		sid := owner.ID
		if oid == foreignID {
			sid = other.ID
		}
		finish := overrideCommand(t, s, oid, sid, "finish", 0, 0)
		if err := s.OverrideOrder(oid, sid, false, finish); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []string{"closed", "all"} {
		for _, filter := range []url.Values{{"status": {status}, "shopper": {"1"}}, {"status": {status}, "shopper": {"2"}}, {"status": {status}, "q": {"avery"}}, {"status": {status}, "q": {"Jordan Lee"}}, {"status": {status}, "q": {"avery"}, "shopper": {"2"}}} {
			w := shopperWorkspace(t, s, owner.ID, false, filter, 0)
			if w.Total != 1 || len(w.Tasks) != 1 || w.Tasks[0].Order.ID != id || w.Tasks[0].Assignment != nil || w.First != 1 || w.Last != 1 {
				t.Fatal("historical filter missing/duplicated/leaked rows", filter, w)
			}
			for _, event := range w.Events {
				if event.OrderID != id {
					t.Fatal("historical filter leaked foreign event")
				}
			}
		}
	}
	absent := shopperWorkspace(t, s, owner.ID, false, url.Values{"status": {"closed"}, "shopper": {"3"}}, 0)
	if absent.Total != 0 {
		t.Fatal("unrelated historical shopper matched")
	}
	all := shopperWorkspace(t, s, owner.ID, true, url.Values{"status": {"closed"}, "shopper": {"1"}}, 0)
	if all.Total != 2 || len(all.Tasks) != 2 {
		t.Fatal("normal manager historical filter not store-wide")
	}
}
