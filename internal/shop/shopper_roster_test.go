package shop

import (
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func rosterCommand(t *testing.T, s *Store, sid string, id int64, action string) ShopperRosterCommand {
	t.Helper()
	c := ShopperRosterCommand{Action: action, Key: token(), Name: "Taylor Demo", Availability: "available", Capacity: 3}
	if id > 0 {
		p, err := s.ShopperRosterProfile(id, sid, false)
		if err != nil {
			t.Fatal(err)
		}
		c.Version = p.Version
		c.Name = p.Name
		c.Initials = p.Initials
		c.Availability = p.Availability
		c.Capacity = p.Capacity
	}
	return c
}
func saveRoster(t *testing.T, s *Store, sid string, id int64, c ShopperRosterCommand) int64 {
	t.Helper()
	saved, err := s.SaveShopperRoster(id, sid, false, c)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}
func rosterProfile(t *testing.T, s *Store, sid string, id int64) *Shopper {
	t.Helper()
	p, err := s.ShopperRosterProfile(id, sid, false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func secondRosterOrder(t *testing.T, s *Store, sid string) int64 {
	t.Helper()
	testCart(t, s, sid, 3, 1)
	return testCheckout(t, s, sid)
}

func TestShopperRosterScopedIdentityReplayAndHistorySnapshots(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	other, otherOrder := pickingFixture(t, s)
	c := rosterCommand(t, s, owner.ID, 0, "create")
	person := saveRoster(t, s, owner.ID, 0, c)
	if again := saveRoster(t, s, owner.ID, 0, c); again != person {
		t.Fatal("replay duplicated identity")
	}
	if p := rosterProfile(t, s, owner.ID, person); p.Initials != "TD" || p.Capacity != 3 || !p.Assignable {
		t.Fatal(p)
	}
	c.Name = "Reused different name"
	if _, err := s.SaveShopperRoster(0, owner.ID, false, c); !errors.Is(err, ErrConflict) {
		t.Fatal("key reuse", err)
	}
	same := rosterCommand(t, s, other.ID, 0, "create")
	same.Key = c.Key
	otherPerson := saveRoster(t, s, other.ID, 0, same)
	if otherPerson == person {
		t.Fatal("cross-scope identity collision")
	}
	if _, err := s.SaveShopperRoster(person, other.ID, false, ShopperRosterCommand{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("scope before validation", err)
	}
	if err := s.AssignShopper(otherOrder, other.ID, false, shopperCommand(t, s, otherOrder, other.ID, "assign", person)); !errors.Is(err, ErrInvalid) {
		t.Fatal("foreign identity assignment", err)
	}
	testAssign(t, s, id, owner.ID, person)
	stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
	receipt := placedSnapshot(t, s, id)
	before := testOrder(t, s, id, owner.ID)
	edit := rosterCommand(t, s, owner.ID, person, "edit")
	edit.Name = "Updated Taylor"
	edit.Initials = "UT"
	edit.Availability = "break"
	edit.Capacity = 1
	saveRoster(t, s, owner.ID, person, edit)
	after := testOrder(t, s, id, owner.ID)
	if after.Assignment.ShopperName != "Taylor Demo" || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(stock, migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)) || !reflect.DeepEqual(receipt, placedSnapshot(t, s, id)) {
		t.Fatal("profile edit changed existing work/snapshot")
	}
	if _, err := s.SaveShopperRoster(person, owner.ID, false, edit); err != nil {
		t.Fatal("edit replay", err)
	}
	edit.Key = token()
	if _, err := s.SaveShopperRoster(person, owner.ID, false, edit); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit", err)
	}
	activeByCurrentName := shopperWorkspace(t, s, owner.ID, false, url.Values{"q": {"Updated Taylor"}, "status": {"active"}}, 0)
	if activeByCurrentName.Total != 1 || activeByCurrentName.Tasks[0].Assignment.ShopperName != "Taylor Demo" {
		t.Fatal("current-name search lost work or changed snapshot")
	}
	foreign := shopperWorkspace(t, s, other.ID, false, url.Values{"roster_q": {"Updated"}, "roster_status": {"all"}}, 0)
	if len(foreign.VisibleRoster) != 0 || len(foreign.Roster) != 4 || len(foreign.RosterEvents) != 1 {
		t.Fatal("foreign profile/event leak", foreign)
	}
	finish := overrideCommand(t, s, id, owner.ID, "finish", 0, 0)
	if err := s.OverrideOrder(id, owner.ID, false, finish); err != nil {
		t.Fatal(err)
	}
	history := shopperWorkspace(t, s, owner.ID, false, url.Values{"status": {"closed"}, "q": {"Taylor Demo"}}, 0)
	if history.Total != 1 || len(history.Events) != 2 || !strings.Contains(history.Events[0].Details, "Taylor Demo") {
		t.Fatal("historical name mutated", history)
	}
	renamed := shopperWorkspace(t, s, owner.ID, false, url.Values{"status": {"closed"}, "q": {"Updated Taylor"}}, 0)
	if renamed.Total != 0 {
		t.Fatal("new name rewrote historic assignment search")
	}
}

func TestShopperRosterSeedProfilesDoNotLeakGlobalOrVisitorChanges(t *testing.T) {
	s := newTestStore(t)
	one := testSession(t, s, "")
	two := testSession(t, s, "")
	edit := rosterCommand(t, s, one.ID, 1, "edit")
	edit.Name = "My private seed"
	edit.Availability = "off_shift"
	edit.Capacity = 1
	saveRoster(t, s, one.ID, 1, edit)
	other := rosterProfile(t, s, two.ID, 1)
	if other.Name != "Avery Morgan" || other.Availability != "available" || other.Capacity != 0 || other.Version != 1 {
		t.Fatal("visitor override leaked", other)
	}
	global, err := s.ShopperRosterProfile(1, "", true)
	if err != nil {
		t.Fatal(err)
	}
	edit.Key = token()
	edit.Version = global.Version
	edit.Name = "Global seed name"
	if _, err = s.SaveShopperRoster(1, "", true, edit); err != nil {
		t.Fatal(err)
	}
	if rosterProfile(t, s, two.ID, 1).Name != "Avery Morgan" {
		t.Fatal("global edits became visitor defaults")
	}
	custom := ShopperRosterCommand{Action: "create", Key: token(), Name: "Global custom", Availability: "available", Capacity: 3}
	if _, err = s.SaveShopperRoster(0, "", true, custom); err != nil {
		t.Fatal(err)
	}
	if len(shopperWorkspace(t, s, two.ID, false, nil, 0).Roster) != 3 {
		t.Fatal("global custom exposed in demo")
	}
}

func TestShopperRosterUnavailableCapacityArchiveAndRecovery(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	next := secondRosterOrder(t, s, owner.ID)
	edit := rosterCommand(t, s, owner.ID, 1, "edit")
	edit.Capacity = 1
	saveRoster(t, s, owner.ID, 1, edit)
	testAssign(t, s, id, owner.ID, 1)
	if err := s.AssignShopper(next, owner.ID, false, shopperCommand(t, s, next, owner.ID, "assign", 1)); !errors.Is(err, ErrShopperCapacity) {
		t.Fatal("capacity", err)
	}
	if p := rosterProfile(t, s, owner.ID, 1); p.Assignable || p.RemainingCapacity != 0 {
		t.Fatal(p)
	}
	for _, state := range []string{"break", "off_shift"} {
		edit = rosterCommand(t, s, owner.ID, 1, "edit")
		edit.Availability = state
		saveRoster(t, s, owner.ID, 1, edit)
		if err := s.AssignShopper(next, owner.ID, false, shopperCommand(t, s, next, owner.ID, "assign", 1)); !errors.Is(err, ErrShopperUnavailable) {
			t.Fatal("unavailable", err)
		}
		if testOrder(t, s, id, owner.ID).Assignment == nil {
			t.Fatal("availability cancelled work")
		}
	}
	archive := rosterCommand(t, s, owner.ID, 1, "archive")
	if _, err := s.SaveShopperRoster(1, owner.ID, false, archive); !errors.Is(err, ErrShopperActive) {
		t.Fatal("archive active", err)
	}
	if err := s.AssignShopper(id, owner.ID, false, shopperCommand(t, s, id, owner.ID, "reassign", 2)); err != nil {
		t.Fatal("reassign away from unavailable", err)
	}
	saveRoster(t, s, owner.ID, 1, archive)
	if p := rosterProfile(t, s, owner.ID, 1); !p.Archived || p.Assignable {
		t.Fatal(p)
	}
	if err := s.AssignShopper(next, owner.ID, false, shopperCommand(t, s, next, owner.ID, "assign", 1)); !errors.Is(err, ErrShopperArchived) {
		t.Fatal(err)
	}
	if len(shopperWorkspace(t, s, owner.ID, false, nil, 0).VisibleRoster) != 2 || len(shopperWorkspace(t, s, owner.ID, false, url.Values{"roster_status": {"archived"}}, 0).VisibleRoster) != 1 {
		t.Fatal("archive visibility")
	}
	restore := rosterCommand(t, s, owner.ID, 1, "restore")
	saveRoster(t, s, owner.ID, 1, restore)
	if p := rosterProfile(t, s, owner.ID, 1); p.Archived || p.Availability != "off_shift" || p.Assignable {
		t.Fatal("restore changed availability", p)
	}
	edit = rosterCommand(t, s, owner.ID, 1, "edit")
	edit.Availability = "available"
	saveRoster(t, s, owner.ID, 1, edit)
	testAssign(t, s, next, owner.ID, 1)
	// The exact old archive retry must never archive the restored active person.
	saveRoster(t, s, owner.ID, 1, archive)
	if rosterProfile(t, s, owner.ID, 1).Archived {
		t.Fatal("archive replay undid restore")
	}
}

func TestShopperRosterInvalidCommandsAuditRollbackAndLoweredCapacity(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	for _, bad := range []func(*ShopperRosterCommand){func(c *ShopperRosterCommand) { c.Name = "\nBad" }, func(c *ShopperRosterCommand) { c.Name = strings.Repeat("界", 81) }, func(c *ShopperRosterCommand) { c.Initials = "TOOLONG" }, func(c *ShopperRosterCommand) { c.Capacity = -1 }, func(c *ShopperRosterCommand) { c.Capacity = 21 }, func(c *ShopperRosterCommand) { c.Availability = "online" }, func(c *ShopperRosterCommand) { c.Key = "short" }, func(c *ShopperRosterCommand) { c.Version = 1 }} {
		c := rosterCommand(t, s, owner.ID, 0, "create")
		bad(&c)
		if _, err := s.SaveShopperRoster(0, owner.ID, false, c); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid accepted", c, err)
		}
	}
	duplicate := rosterCommand(t, s, owner.ID, 0, "create")
	duplicate.Name = " avery   MORGAN "
	if _, err := s.SaveShopperRoster(0, owner.ID, false, duplicate); !errors.Is(err, ErrShopperDuplicate) {
		t.Fatal("duplicate", err)
	}
	testExec(t, s, `CREATE TRIGGER reject_roster_audit BEFORE INSERT ON shopper_roster_events BEGIN SELECT RAISE(ABORT,'roster audit failed'); END`)
	before := fingerprintTest(t, s.db)
	c := rosterCommand(t, s, owner.ID, 0, "create")
	if _, err := s.SaveShopperRoster(0, owner.ID, false, c); err == nil {
		t.Fatal("audit accepted")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("partial roster create")
	}
	testExec(t, s, `DROP TRIGGER reject_roster_audit`)
	saveRoster(t, s, owner.ID, 0, c)
	testAssign(t, s, id, owner.ID, 1)
	second := secondRosterOrder(t, s, owner.ID)
	testAssign(t, s, second, owner.ID, 1)
	edit := rosterCommand(t, s, owner.ID, 1, "edit")
	edit.Capacity = 1
	saveRoster(t, s, owner.ID, 1, edit)
	if p := rosterProfile(t, s, owner.ID, 1); p.ActiveOrders != 2 || p.RemainingCapacity != 0 || p.Assignable {
		t.Fatal("lower capacity lost work", p)
	}
}

func TestShopperRosterTwoConnectionsEnforceCapacityAndArchive(t *testing.T) {
	for _, race := range []string{"capacity", "archive", "unavailable"} {
		t.Run(race, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "roster.db")
			a, b := openTestStore(t, path), openTestStore(t, path)
			owner, id := pickingFixture(t, a)
			next := secondRosterOrder(t, a, owner.ID)
			edit := rosterCommand(t, a, owner.ID, 1, "edit")
			edit.Capacity = 1
			saveRoster(t, a, owner.ID, 1, edit)
			first := shopperCommand(t, a, id, owner.ID, "assign", 1)
			second := shopperCommand(t, a, next, owner.ID, "assign", 1)
			change := rosterCommand(t, a, owner.ID, 1, "archive")
			if race == "unavailable" {
				change.Action = "edit"
				change.Availability = "break"
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			go func() { <-start; results <- a.AssignShopper(id, owner.ID, false, first) }()
			go func() {
				<-start
				if race == "capacity" {
					results <- b.AssignShopper(next, owner.ID, false, second)
				} else {
					_, err := b.SaveShopperRoster(1, owner.ID, false, change)
					results <- err
				}
			}()
			close(start)
			wins := 0
			for range 2 {
				err := <-results
				if err == nil {
					wins++
				} else if !errors.Is(err, ErrShopperCapacity) && !errors.Is(err, ErrShopperActive) && !errors.Is(err, ErrShopperArchived) && !errors.Is(err, ErrShopperUnavailable) {
					t.Fatal(err)
				}
			}
			profile := rosterProfile(t, a, owner.ID, 1)
			if race == "capacity" && (wins != 1 || profile.ActiveOrders != 1) {
				t.Fatal("oversubscribed", wins, profile)
			}
			if race == "archive" && (wins != 1 || (profile.Archived && profile.ActiveOrders > 0)) {
				t.Fatal("archived active work", wins, profile)
			}
			if race == "unavailable" && (profile.Availability != "break" || profile.ActiveOrders > 1) {
				t.Fatal("availability race", profile)
			}
		})
	}
}

func TestShopperRosterOtherVisitorWorkNeverChangesEligibility(t *testing.T) {
	s := newTestStore(t)
	one, first := pickingFixture(t, s)
	two, second := pickingFixture(t, s)
	for _, sid := range []string{one.ID, two.ID} {
		edit := rosterCommand(t, s, sid, 1, "edit")
		edit.Capacity = 1
		saveRoster(t, s, sid, 1, edit)
	}
	testAssign(t, s, second, two.ID, 1)
	// One visitor can archive their idle seed profile while the other is busy.
	saveRoster(t, s, one.ID, 1, rosterCommand(t, s, one.ID, 1, "archive"))
	if p := rosterProfile(t, s, two.ID, 1); p.Archived || p.ActiveOrders != 1 || p.Assignable {
		t.Fatal("other visitor state leaked or changed", p)
	}
	saveRoster(t, s, one.ID, 1, rosterCommand(t, s, one.ID, 1, "restore"))
	testAssign(t, s, first, one.ID, 1)
	if p := rosterProfile(t, s, one.ID, 1); p.ActiveOrders != 1 || p.Capacity != 1 {
		t.Fatal("capacity counted another visitor", p)
	}
	global, err := s.ShopperRosterProfile(1, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if global.ActiveOrders != 2 || global.Capacity != 0 {
		t.Fatal("normal scope failed to aggregate", global)
	}
	edit := ShopperRosterCommand{Action: "edit", Key: token(), Version: global.Version, Name: global.Name, Initials: global.Initials, Availability: "available", Capacity: 1}
	if _, err = s.SaveShopperRoster(1, "", true, edit); err != nil {
		t.Fatal(err)
	}
	third := secondRosterOrder(t, s, one.ID)
	if err = s.AssignShopper(third, one.ID, true, shopperCommand(t, s, third, one.ID, "assign", 1)); !errors.Is(err, ErrShopperCapacity) {
		t.Fatal("normal mode ignored global workload", err)
	}
}
