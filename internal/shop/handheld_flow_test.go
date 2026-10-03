package shop

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func employeeFlowFixture(t *testing.T, s *Store, a *App) (Session, int64, EmployeeSession, HandheldSession, []*http.Cookie) {
	t.Helper()
	owner, id := pickingFixture(t, s)
	enrollEmployeeOrderTest(t, s, id)
	e := employeeFixture(t, s)
	g := employeeClaimTest(t, s, e, id)
	return owner, id, e, g, []*http.Cookie{{Name: handheldCookie, Value: g.Token}, {Name: employeeCookie, Value: e.Token}}
}
func flowReview(t *testing.T, s *Store, a *App, g HandheldSession, cookies []*http.Cookie, index int) (*httptest.ResponseRecorder, url.Values) {
	t.Helper()
	c := handheldCommand(t, s, g, index, 0)
	page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d", c.LineID), cookies, nil, nil)
	form := handheldHTTPForm(t, page, "/handheld/scan")
	form.Set("code", c.Code)
	review := handheldHTTPRequest(t, a, "POST", "/handheld/scan", cookies, form, nil)
	return review, handheldHTTPForm(t, review, "/handheld/pick")
}
func TestEmployeeFlowPartialCompletionAutoNextUndoReplayAndReload(t *testing.T) {
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	owner, id, _, g, cookies := employeeFlowFixture(t, s, a)
	task := handheldTaskTest(t, s, g)
	index := 0
	for i, l := range task.Lines {
		if l.Quantity > 1 {
			index = i
		}
	}
	line := task.Lines[index]
	stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock FROM products ORDER BY id`)
	review, pick := flowReview(t, s, a, g, cookies, index)
	if !strings.Contains(review.Body.String(), `data-scan-auto-review`) && !strings.Contains(review.Body.String(), `Save picked quantity`) {
		t.Fatal("missing explicit review")
	}
	pick.Set("picked", "1")
	partial := handheldHTTPRequest(t, a, "POST", "/handheld/pick", cookies, pick, nil)
	if partial.Header().Get("HX-Replace-Url") != "" || !strings.Contains(partial.Body.String(), `data-pick-line-delta="0"`) {
		t.Fatal("partial advanced or credited line")
	}
	nextPick := handheldHTTPForm(t, partial, "/handheld/pick")
	if nextPick.Get("line_id") != fmt.Sprint(line.LineID) || nextPick.Get("command_key") == pick.Get("command_key") {
		t.Fatal("partial lost selected item or command")
	}
	nextPick.Set("picked", fmt.Sprint(line.Quantity))
	saved := handheldHTTPRequest(t, a, "POST", "/handheld/pick", cookies, nextPick, nil)
	other := task.Lines[1-index]
	if saved.Header().Get("HX-Replace-Url") != fmt.Sprintf("/handheld/?line=%d", other.LineID) || !strings.Contains(saved.Body.String(), `data-pick-line-delta="1"`) {
		t.Fatal("complete did not advance to next department/item", saved.Header())
	}
	scan := handheldHTTPForm(t, saved, "/handheld/scan")
	if scan.Get("line_id") != fmt.Sprint(other.LineID) {
		t.Fatal("wrong next item")
	}
	undo := handheldHTTPForm(t, saved, "/handheld/pick/undo")
	if undo.Get("picked") != "1" || undo.Get("line_id") != fmt.Sprint(line.LineID) {
		t.Fatal("wrong undo target")
	}
	fresh := handheldHTTPRequest(t, a, "GET", saved.Header().Get("HX-Replace-Url"), cookies, nil, nil)
	if handheldHTTPForm(t, fresh, "/handheld/scan").Get("line_id") != fmt.Sprint(other.LineID) || strings.Contains(fresh.Body.String(), `data-pick-event=`) {
		t.Fatal("refresh changed selection or recounted completion")
	}
	before := fingerprintTest(t, s.db)
	for n := 0; n < 3; n++ {
		replay := handheldHTTPRequest(t, a, "POST", "/handheld/pick", cookies, nextPick, nil)
		if replay.Header().Get("HX-Replace-Url") != "" || strings.Contains(replay.Body.String(), `data-pick-event=`) || fingerprintTest(t, s.db) != before {
			t.Fatal("repeat altered pick or auto-advanced")
		}
	}
	undone := handheldHTTPRequest(t, a, "POST", "/handheld/pick/undo", cookies, undo, nil)
	if undone.Header().Get("HX-Replace-Url") != "" || !strings.Contains(undone.Body.String(), `data-pick-line-delta="-1"`) || strings.Contains(undone.Body.String(), `action="/handheld/pick/undo"`) {
		t.Fatal("undo failed to reopen safely")
	}
	if form := handheldHTTPForm(t, undone, "/handheld/pick"); form.Get("line_id") != fmt.Sprint(line.LineID) {
		t.Fatal("undo did not select restored line")
	}
	once := fingerprintTest(t, s.db)
	_ = handheldHTTPRequest(t, a, "POST", "/handheld/pick/undo", cookies, undo, nil)
	if once != fingerprintTest(t, s.db) {
		t.Fatal("undo replay mutated")
	}
	if !reflect.DeepEqual(stock, migrationQuerySnapshot(t, s.db, `SELECT id,stock FROM products ORDER BY id`)) || testOrder(t, s, id, owner.ID).Status != "Picking" {
		t.Fatal("flow changed stock or finished order")
	}
}
func TestEmployeeFlowOverpickAndConcurrentDifferentKeysDoNotAdvanceTwice(t *testing.T) {
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	_, _, _, g, cookies := employeeFlowFixture(t, s, a)
	_, form := flowReview(t, s, a, g, cookies, 0)
	task := handheldTaskTest(t, s, g)
	line := task.Lines[0]
	form.Set("picked", fmt.Sprint(line.Quantity+1))
	before := fingerprintTest(t, s.db)
	bad := handheldHTTPRequest(t, a, "POST", "/handheld/pick", cookies, form, nil)
	if bad.Header().Get("HX-Replace-Url") != "" || strings.Contains(bad.Body.String(), `data-pick-event=`) || before != fingerprintTest(t, s.db) {
		t.Fatal("overpick accepted")
	}
	c := handheldCommand(t, s, g, 0, line.Quantity)
	second := c
	second.Key = token()
	var wg sync.WaitGroup
	results := make(chan HandheldPickResult, 2)
	errs := make(chan error, 2)
	for _, command := range []HandheldPick{c, second} {
		wg.Add(1)
		go func(cmd HandheldPick) {
			defer wg.Done()
			r, e := s.ConfirmHandheldPick(g.Token, g.CSRF, cmd)
			results <- r
			errs <- e
		}(command)
	}
	wg.Wait()
	close(results)
	close(errs)
	success, conflict := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if e == ErrConflict {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent duplicate changed count", success, conflict)
	}
	for r := range results {
		if r.LineID != 0 && r.Picked != line.Quantity {
			t.Fatal("incorrect completion credit", r)
		}
	}
	stale := HandheldView{Task: handheldTaskTest(t, s, g), Selected: &line, Saved: &HandheldSaved{LineID: line.LineID, Version: task.Version, PickVersion: line.PickVersion, Scan: c.HandheldScan, Key: token(), Counted: true}}
	applyHandheldFlow(&stale)
	if stale.Advanced || stale.Undo != nil || stale.PickEvent != "" || stale.Selected.LineID != line.LineID {
		t.Fatal("stale projection moved selection")
	}
}
func TestEmployeeFlowLastLineRequiresExplicitReadyAndRetainsUndo(t *testing.T) {
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	owner, id, e, g, cookies := employeeFlowFixture(t, s, a)
	task := handheldTaskTest(t, s, g)
	for i, l := range task.Lines {
		_, form := flowReview(t, s, a, g, cookies, i)
		form.Set("picked", fmt.Sprint(l.Quantity))
		page := handheldHTTPRequest(t, a, "POST", "/handheld/pick", cookies, form, nil)
		if i == len(task.Lines)-1 {
			if page.Header().Get("HX-Replace-Url") != "/handheld/" || !strings.Contains(page.Body.String(), "All product lines confirmed") || !strings.Contains(page.Body.String(), `action="/handheld/pick/undo"`) {
				t.Fatal("final completion summary absent")
			}
		}
	}
	current := testOrder(t, s, id, owner.ID)
	if current.Status != "Picking" {
		t.Fatal("auto marked ready")
	}
	if err := s.EmployeeReady(e.Token, e.CSRF, token(), id, current.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.HandheldTask(g.Token); err != ErrHandheldAccess {
		t.Fatal("finished capability survives")
	}
}
func TestEmployeeFlowSubstitutionAndReportedExceptionsStayProminent(t *testing.T) {
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	owner, id, _, g, cookies := employeeFlowFixture(t, s, a)
	p, decision := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
	for _, state := range []string{"awaiting customer", "declined"} {
		if state == "declined" {
			decision.Action = "reject"
			if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, decision); err != nil {
				t.Fatal(err)
			}
		}
		task := handheldTaskTest(t, s, g)
		if len(task.Exceptions) != 1 || task.Exceptions[0].LineID != p.LineID || !strings.Contains(task.Exceptions[0].Exception, state) {
			t.Fatal("missing exception", state, task.Exceptions)
		}
		next := nextHandheldLine(task, 0)
		if next == nil || next.LineID == p.LineID {
			t.Fatal("auto selected exception")
		}
		page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d", p.LineID), cookies, nil, nil)
		body := page.Body.String()
		if !strings.Contains(body, `class="handheld-item-actions"`) || !strings.Contains(body, `id="handheld-report" open`) || strings.Index(body, "Unavailable / issue") > strings.Index(body, `id="handheld-scan-form"`) {
			t.Fatal("exception controls hidden")
		}
	}
	task := handheldTaskTest(t, s, g)
	next := nextHandheldLine(task, 0)
	c := HandheldReport{LineID: next.LineID, AssignmentVersion: task.Assignment.Version, Version: task.Version, PickVersion: next.PickVersion, Kind: "unavailable", Key: token()}
	if _, err := s.ReportHandheldItem(g.Token, g.CSRF, c); err != nil {
		t.Fatal(err)
	}
	task = handheldTaskTest(t, s, g)
	if len(task.Exceptions) != 2 || nextHandheldLine(task, 0) != nil {
		t.Fatal("report is not outstanding")
	}
}
func TestEmployeeFlowGroupingUnknownStableAndReleaseReclaim(t *testing.T) {
	task := &HandheldTask{Lines: []HandheldLine{{WorkingOrderItem: WorkingOrderItem{LineID: 1}, Department: "Produce"}, {WorkingOrderItem: WorkingOrderItem{LineID: 2}, Department: "Produce"}, {WorkingOrderItem: WorkingOrderItem{LineID: 3}}}}
	groupHandheldTask(task)
	first := fmt.Sprintf("%+v", task.Departments)
	groupHandheldTask(task)
	if len(task.Departments) != 2 || task.Departments[1].Name != "Other department" || len(task.Departments[0].Lines) != 2 || fmt.Sprintf("%+v", task.Departments) != first {
		t.Fatal("unstable grouping")
	}
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	owner, id, e, g, cookies := employeeFlowFixture(t, s, a)
	_, form := flowReview(t, s, a, g, cookies, 0)
	line := handheldTaskTest(t, s, g).Lines[0]
	form.Set("picked", fmt.Sprint(line.Quantity))
	saved := handheldHTTPRequest(t, a, "POST", "/handheld/pick", cookies, form, nil)
	undo := handheldHTTPForm(t, saved, "/handheld/pick/undo")
	current := testOrder(t, s, id, owner.ID)
	if err := s.EmployeeRelease(e.Token, e.CSRF, token(), id, current.Version); err != nil {
		t.Fatal(err)
	}
	nextEmployee := employeeFixture(t, s)
	nextGrant := employeeClaimTest(t, s, nextEmployee, id)
	before := migrationQuerySnapshot(t, s.db, `SELECT id,picked_quantity,pick_version FROM working_order_items ORDER BY id`)
	denied := handheldHTTPRequest(t, a, "POST", "/handheld/pick/undo", cookies, undo, nil)
	if denied.Header().Get("X-Handheld-Error") != "revoked" || !reflect.DeepEqual(before, migrationQuerySnapshot(t, s.db, `SELECT id,picked_quantity,pick_version FROM working_order_items ORDER BY id`)) {
		t.Fatal("old undo crossed ownership")
	}
	if task := handheldTaskTest(t, s, nextGrant); task.PickedLines != 1 || task.Assignment.ID == current.Assignment.ID {
		t.Fatal("reclaim lost progress or scope")
	}
}
func TestEmployeeFlowWeightNeedsReviewThenAdvancesAndCreditsOneLine(t *testing.T) {
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	_, id, _ := weightedHTTPFixture(t, s)
	enrollEmployeeOrderTest(t, s, id)
	e := employeeFixture(t, s)
	g := employeeClaimTest(t, s, e, id)
	cookies := []*http.Cookie{{Name: handheldCookie, Value: g.Token}}
	task := handheldTaskTest(t, s, g)
	index := 0
	for i, l := range task.Lines {
		if l.SaleUnit == "g" {
			index = i
		}
	}
	command := handheldCommand(t, s, g, index, 0)
	page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d", command.LineID), cookies, nil, nil)
	scan := handheldHTTPForm(t, page, "/handheld/scan")
	scan.Set("code", command.Code)
	matched := handheldHTTPRequest(t, a, "POST", "/handheld/scan", cookies, scan, nil)
	if matched.Header().Get("HX-Replace-Url") != "" || handheldTaskTest(t, s, g).PickedLines != 0 {
		t.Fatal("scan saved weight")
	}
	preview := handheldHTTPForm(t, matched, "/handheld/weight/preview")
	preview.Set("actual", "527")
	reviewed := handheldHTTPRequest(t, a, "POST", "/handheld/weight/preview", cookies, preview, nil)
	confirm := handheldHTTPForm(t, reviewed, "/handheld/weight/confirm")
	if reviewed.Header().Get("HX-Replace-Url") != "" || handheldTaskTest(t, s, g).PickedLines != 0 {
		t.Fatal("weight preview saved or advanced")
	}
	saved := handheldHTTPRequest(t, a, "POST", "/handheld/weight/confirm", cookies, confirm, nil)
	if saved.Header().Get("HX-Replace-Url") == "" || !strings.Contains(saved.Body.String(), `data-pick-line-delta="1"`) || strings.Contains(saved.Body.String(), `action="/handheld/pick/undo"`) {
		t.Fatal("weight did not advance or exposed unsafe stock undo")
	}
	replay := handheldHTTPRequest(t, a, "POST", "/handheld/weight/confirm", cookies, confirm, nil)
	if replay.Header().Get("HX-Replace-Url") != "" || strings.Contains(replay.Body.String(), `data-pick-event=`) {
		t.Fatal("weight replay advances/counts")
	}
}
