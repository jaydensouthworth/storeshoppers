package shop

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func rosterCommandFields(session Session, c ShopperRosterCommand) url.Values {
	return url.Values{"csrf": {session.CSRF}, "action": {c.Action}, "command_key": {c.Key}, "version": {fmt.Sprint(c.Version)}, "name": {c.Name}, "initials": {c.Initials}, "availability": {c.Availability}, "capacity": {fmt.Sprint(c.Capacity)}}
}
func TestHTTPShopperRosterCommandsAuthCSRFAndOwnership(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner := pickingLogin(t, a, testSession(t, s, ""))
	foreign := pickingLogin(t, a, testSession(t, s, ""))
	visitor := testSession(t, s, "")
	custom := saveRoster(t, s, owner.ID, 0, rosterCommand(t, s, owner.ID, 0, "create"))
	for _, hx := range []bool{false, true} {
		for _, route := range []string{"/manager/shoppers/roster", "/manager/shoppers/roster/1"} {
			c := rosterCommand(t, s, owner.ID, 0, "create")
			if strings.HasSuffix(route, "/1") {
				c = rosterCommand(t, s, owner.ID, 1, "edit")
			}
			fields := rosterCommandFields(visitor, c)
			w := testRequest(t, a, "POST", route, visitor, fields, pickingHeaders(hx))
			if (hx && (w.Code != 403 || w.Header().Get("X-Shop-Error") != "manager-expired")) || (!hx && (w.Code != 303 || w.Header().Get("Location") != "/manager/login")) {
				t.Fatal("unauthorized roster write", hx, w.Code, w.Body.String())
			}
			fields = rosterCommandFields(owner, c)
			fields.Set("csrf", "wrong")
			w = testRequest(t, a, "POST", route, owner, fields, pickingHeaders(hx))
			if w.Code != 403 {
				t.Fatal("csrf write", w.Code)
			}
		}
		for _, route := range []string{fmt.Sprintf("/manager/shoppers?person=%d", custom), "/manager/shoppers?person=invalid", "/manager/shoppers?person=0"} {
			w := testRequest(t, a, "GET", route, foreign, nil, pickingHeaders(hx))
			if w.Code != 404 || strings.Contains(w.Body.String(), "Taylor Demo") {
				t.Fatal("foreign/invalid selected identity", route, w.Code)
			}
		}
		fields := rosterCommandFields(foreign, ShopperRosterCommand{Name: "Private foreign attempted value", Key: token(), Action: "edit"})
		fields.Set("version", "invalid")
		w := testRequest(t, a, "POST", fmt.Sprintf("/manager/shoppers/roster/%d", custom), foreign, fields, pickingHeaders(hx))
		if w.Code != 404 || strings.Contains(w.Body.String(), "Private foreign") {
			t.Fatal("scope-before-validation", w.Code, w.Body.String())
		}
	}
	if testCount(t, s, "shopper_roster_events") != 1 || len(shopperWorkspace(t, s, owner.ID, false, nil, 0).Roster) != 4 {
		t.Fatal("rejected command mutated roster")
	}
}

func TestHTTPShopperRosterCreateReplayDraftAndEditorNavigation(t *testing.T) {
	for _, hx := range []bool{false, true} {
		t.Run(fmt.Sprint(hx), func(t *testing.T) {
			s := newTestStore(t)
			a := pickingApp(t, s, true)
			owner := pickingLogin(t, a, testSession(t, s, ""))
			c := rosterCommand(t, s, owner.ID, 0, "create")
			fields := rosterCommandFields(owner, c)
			fields.Set("q", "Avery & market")
			fields.Set("status", "all")
			fields.Set("roster_q", "Taylor")
			fields.Set("roster_status", "all")
			fields.Set("queue", "status=open&q=Market")
			for range 2 {
				w := testRequest(t, a, "POST", "/manager/shoppers/roster", owner, fields, pickingHeaders(hx))
				location := w.Header().Get("Location")
				if hx {
					location = w.Header().Get("HX-Push-Url")
				}
				if (hx && w.Code != 200) || (!hx && w.Code != 303) || !strings.Contains(location, "person=") || !strings.Contains(location, "roster_q=Taylor") || !strings.Contains(location, "q=Avery+%26+market") || strings.Contains(location, "new=1") {
					t.Fatal("create context/replay", w.Code, location, w.Body.String())
				}
			}
			if testCount(t, s, "shopper_roster_events") != 1 || testCount(t, s, "shoppers") != 4 {
				t.Fatal("create replay duplicated state")
			}
			profile := shopperWorkspace(t, s, owner.ID, false, nil, 0).Roster[3]
			path := fmt.Sprintf("/manager/shoppers/roster/%d", profile.ID)
			stale := rosterCommand(t, s, owner.ID, profile.ID, "edit")
			fresh := stale
			fresh.Key = token()
			fresh.Availability = "off_shift"
			saveRoster(t, s, owner.ID, profile.ID, fresh)
			stale.Name = `Draft <script> & "name"`
			stale.Capacity = 4
			stale.Availability = "break"
			fields = rosterCommandFields(owner, stale)
			fields.Set("roster_q", "Not a match")
			fields.Set("status", "active")
			w := testRequest(t, a, "POST", path, owner, fields, pickingHeaders(hx))
			body := w.Body.String()
			for _, text := range []string{"This change was not saved", ErrConflict.Error(), `Draft &lt;script&gt; &amp; &#34;name&#34;`, `name="version" value="2"`, `value="break" selected`, `name="roster_q" value="Not a match"`} {
				if !strings.Contains(body, text) {
					t.Fatal("draft/current version/context missing", text, w.Code, body)
				}
			}
			if strings.Contains(body, "<script>") || strings.Contains(body, `value="`+stale.Key+`"`) {
				t.Fatal("unsafe draft or stale command key repeated")
			}
			saved := rosterProfile(t, s, owner.ID, profile.ID)
			w = testRequest(t, a, "GET", "/manager/shoppers?roster_q=Not+a+match&status=active", owner, nil, pickingHeaders(hx))
			if w.Code != 200 || strings.Contains(w.Body.String(), `id="roster-editor"`) || rosterProfile(t, s, owner.ID, profile.ID).Version != saved.Version {
				t.Fatal("close mutated or retained editor")
			}
			archive := rosterCommand(t, s, owner.ID, profile.ID, "archive")
			w = testRequest(t, a, "POST", path, owner, rosterCommandFields(owner, archive), pickingHeaders(hx))
			location := w.Header().Get("Location")
			if hx {
				location = w.Header().Get("HX-Push-Url")
			}
			if strings.Contains(location, "person=") || !rosterProfile(t, s, owner.ID, profile.ID).Archived {
				t.Fatal("archive did not close editor", location)
			}
			restore := rosterCommand(t, s, owner.ID, profile.ID, "restore")
			w = testRequest(t, a, "POST", path, owner, rosterCommandFields(owner, restore), pickingHeaders(hx))
			if rosterProfile(t, s, owner.ID, profile.ID).Archived || (w.Code != 200 && w.Code != 303) {
				t.Fatal("restore", w.Code)
			}
		})
	}
}

func TestHTTPShopperRosterEnforcesEligibilityWithoutLosingCurrentWork(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner, id := pickingFixture(t, s)
	next := secondRosterOrder(t, s, owner.ID)
	owner = pickingLogin(t, a, owner)
	edit := rosterCommand(t, s, owner.ID, 1, "edit")
	edit.Capacity = 1
	saveRoster(t, s, owner.ID, 1, edit)
	testAssign(t, s, id, owner.ID, 1)
	for _, expected := range []error{ErrShopperCapacity, ErrShopperUnavailable} {
		if expected == ErrShopperUnavailable {
			edit = rosterCommand(t, s, owner.ID, 1, "edit")
			edit.Availability = "break"
			saveRoster(t, s, owner.ID, 1, edit)
		}
		command := shopperCommand(t, s, next, owner.ID, "assign", 1)
		w := testRequest(t, a, "POST", fmt.Sprintf("/manager/shoppers/orders/%d", next), owner, shopperFields(owner, command), pickingHeaders(true))
		if w.Code != 200 || !strings.Contains(w.Body.String(), expected.Error()) || testOrder(t, s, next, owner.ID).Assignment != nil || testOrder(t, s, id, owner.ID).Assignment == nil {
			t.Fatal("eligibility failure", w.Code, w.Body.String())
		}
	}
	archive := rosterCommand(t, s, owner.ID, 1, "archive")
	w := testRequest(t, a, "POST", "/manager/shoppers/roster/1", owner, rosterCommandFields(owner, archive), pickingHeaders(true))
	if w.Code != 200 || !strings.Contains(w.Body.String(), ErrShopperActive.Error()) || rosterProfile(t, s, owner.ID, 1).Archived {
		t.Fatal("active archive", w.Code, w.Body.String())
	}
}
