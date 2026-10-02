package shop

import (
	"fmt"
	"strings"
	"testing"
)

func TestHTTPRosterFocusedEditorAndAccessibleFields(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner := pickingLogin(t, a, testSession(t, s, ""))
	for _, hx := range []bool{false, true} {
		w := testRequest(t, a, "GET", "/manager/shoppers?new=1&status=unassigned&roster_q=Demo&roster_status=all", owner, nil, pickingHeaders(hx))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		body := w.Body.String()
		for _, want := range []string{`id="roster-editor"`, `id="shopper-roster-form"`, `action="/manager/shoppers/roster"`, `for="roster-name"`, `for="roster-initials"`, `for="roster-availability"`, `for="roster-capacity"`, `name="capacity" type="number" min="0" max="20" step="1" required value="3"`, `name="roster_q" value="Demo"`, `name="roster_status" value="all"`, `name="status" value="unassigned"`, "Use made-up names", "Create simulated shopper", "Existing tasks stay assigned"} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %q", want)
			}
		}
		if strings.Contains(body, `id="archive-shopper"`) || strings.Contains(body, `id="restore-shopper"`) {
			t.Fatal("create editor exposed actions for existing identity")
		}
		w = testRequest(t, a, "GET", "/manager/shoppers?status=unassigned&roster_q=Demo&roster_status=all", owner, nil, pickingHeaders(hx))
		if w.Code != 200 || strings.Contains(w.Body.String(), `id="roster-editor"`) {
			t.Fatal("close did not dismiss roster editor")
		}
	}
}

func TestHTTPRosterEligibilityArchiveAndHistoryPresentation(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner, id := pickingFixture(t, s)
	owner = pickingLogin(t, a, owner)
	_, err := s.SaveShopperRoster(1, owner.ID, false, ShopperRosterCommand{Action: "edit", Key: token(), Version: 1, Name: "Avery Example", Initials: "AE", Availability: "available", Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AssignShopper(id, owner.ID, false, shopperCommand(t, s, id, owner.ID, "assign", 1)); err != nil {
		t.Fatal(err)
	}
	w := testRequest(t, a, "GET", fmt.Sprintf("/manager/shoppers?person=1&order=%d", id), owner, nil, nil)
	body := w.Body.String()
	for _, want := range []string{`id="roster-editor"`, `id="assignment-editor"`, `id="archive-shopper" type="submit" aria-describedby="roster-archive-help" disabled`, "Review active assignments", "At capacity", "Maximum active orders", "Roster activity"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q: %s", want, body)
		}
	}
	optionStart := strings.Index(body, `<option value="1"`)
	if optionStart < 0 {
		t.Fatal("missing assignment shopper")
	}
	option := body[optionStart : optionStart+strings.Index(body[optionStart:], "</option>")]
	if !strings.Contains(option, "disabled") || !strings.Contains(option, "At capacity") {
		t.Fatal("capacity option is not visibly unavailable", option)
	}
	_, err = s.SaveShopperRoster(2, owner.ID, false, ShopperRosterCommand{Action: "edit", Key: token(), Version: 1, Name: "Break Example", Initials: "BE", Availability: "break", Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveShopperRoster(2, owner.ID, false, ShopperRosterCommand{Action: "archive", Key: token(), Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	w = testRequest(t, a, "GET", "/manager/shoppers?person=2&roster_status=archived", owner, nil, nil)
	for _, want := range []string{`id="restore-shopper"`, "Break Example", "Archived shopper", "Review availability and capacity"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatal("missing archive/restore presentation", want)
		}
	}
	if strings.Contains(w.Body.String(), `id="shopper-roster-form"`) {
		t.Fatal("archived person has editable active form")
	}
}
