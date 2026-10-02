package shop

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func substitutionHTTPFixture(t *testing.T) (*Store, *App, Session, int64, *http.Cookie, *http.Cookie, HandheldSession, int64) {
	t.Helper()
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	owner, id := pickingFixture(t, s)
	enrollEmployeeOrderTest(t, s, id)
	employee := employeeFixture(t, s)
	g := employeeClaimTest(t, s, employee, id)
	task, _, err := s.HandheldTask(g.Token)
	if err != nil {
		t.Fatal(err)
	}
	return s, a, owner, id, &http.Cookie{Name: "shop_session", Value: owner.ID}, &http.Cookie{Name: handheldCookie, Value: g.Token}, g, task.Lines[0].LineID
}
func TestSubstitutionHTTPNativePreviewSendApproveAndPick(t *testing.T) {
	s, a, owner, id, customer, phone, _, line := substitutionHTTPFixture(t)
	page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/substitutions?line=%d", line), []*http.Cookie{phone}, nil, nil)
	if page.Code != 200 || page.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal(page.Code)
	}
	for _, secret := range []string{owner.ID, phone.Value, "handheld.js", "scanner.js", `action="/manager`} {
		if strings.Contains(page.Body.String(), secret) {
			t.Fatal("authority/controller leaked", secret)
		}
	}
	form := handheldHTTPForm(t, page, "/handheld/substitutions/preview")
	form.Set("replacement_id", "3")
	form.Set("quantity", "2")
	form.Set("disposition", "writeoff")
	form.Set("note", "Missing fake original <script>unsafe</script>")
	before := fingerprintTest(t, s.db)
	preview := handheldHTTPRequest(t, a, "POST", "/handheld/substitutions/preview", []*http.Cookie{phone}, form, nil)
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "PREVIEW ONLY") || strings.Contains(preview.Body.String(), "<script>unsafe") {
		t.Fatal("preview unavailable/plaintext failed", preview.Code)
	}
	if before != fingerprintTest(t, s.db) {
		t.Fatal("preview wrote")
	}
	send := weightHTTPForm(t, preview.Body.String(), "/handheld/substitutions/send")
	for k := range send {
		send.Set(k, html.UnescapeString(send.Get(k)))
	}
	for n := 0; n < 2; n++ {
		posted := handheldHTTPRequest(t, a, "POST", "/handheld/substitutions/send", []*http.Cookie{phone}, send, nil)
		if posted.Code != 303 {
			t.Fatal("native proposal", posted.Code, posted.Body.String())
		}
	}
	if testCount(t, s, "substitution_proposals") != 1 {
		t.Fatal("duplicate proposal")
	}
	path := fmt.Sprintf("/orders/%d/substitutions", id)
	pending := handheldHTTPRequest(t, a, "GET", path, []*http.Cookie{customer}, nil, nil)
	if !strings.Contains(pending.Body.String(), "Waiting for customer decision") || !strings.Contains(pending.Body.String(), "Approve this exact replacement") {
		t.Fatal("missing customer decision")
	}
	status := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/orders/%d/status", id), []*http.Cookie{customer}, nil, map[string]string{"HX-Request": "true"})
	if !strings.Contains(status.Body.String(), "replacement request(s) to review") {
		t.Fatal("no pending customer alert")
	}
	decision := handheldHTTPForm(t, pending, path)
	decision.Set("decision", "approve")
	decision.Set("replacement_id", "99999")
	decision.Set("quantity", "99")
	decision.Set("price", "1")
	for n := 0; n < 2; n++ {
		posted := handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer}, decision, nil)
		if posted.Code != 303 {
			t.Fatal("approve", posted.Code, posted.Body.String())
		}
	}
	order := testOrder(t, s, id, owner.ID)
	for _, i := range order.WorkingItems {
		if i.ProductID == 3 && i.Quantity != 2 {
			t.Fatal("tampered approval changed quantity")
		}
	}
	task, _, err := s.HandheldTask(phone.Value)
	if err != nil {
		t.Fatal(err)
	}
	var replacement HandheldLine
	for _, l := range task.Lines {
		if l.ProductID == 3 {
			replacement = l
		}
	}
	item := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d", replacement.LineID), []*http.Cookie{phone}, nil, nil)
	scan := handheldHTTPForm(t, item, "/handheld/scan")
	scan.Set("code", replacement.SKU)
	scanned := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{phone}, scan, nil)
	pick := handheldHTTPForm(t, scanned, "/handheld/pick")
	pick.Set("picked", "2")
	saved := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{phone}, pick, nil)
	if saved.Code != 200 || !strings.Contains(saved.Body.String(), "Saved an absolute picked count of 2") {
		t.Fatal("replacement not pickable")
	}
}
func TestSubstitutionHTTPAuthorityCSRFOriginAndPractice(t *testing.T) {
	s, a, _, id, customer, phone, g, line := substitutionHTTPFixture(t)
	path := fmt.Sprintf("/orders/%d/substitutions", id)
	stranger := testSession(t, s, "")
	if err := s.Manager(stranger.ID, true); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	for _, cookies := range [][]*http.Cookie{nil, {phone}, {{Name: "shop_session", Value: stranger.ID}}} {
		w := handheldHTTPRequest(t, a, "GET", path, cookies, nil, nil)
		if w.Code != 403 || len(w.Result().Cookies()) > 0 {
			t.Fatal("foreign customer access", w.Code)
		}
	}
	if before != fingerprintTest(t, s.db) {
		t.Fatal("foreign read mutated")
	}
	page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/substitutions?line=%d", line), []*http.Cookie{phone}, nil, nil)
	f := handheldHTTPForm(t, page, "/handheld/substitutions/preview")
	f.Set("replacement_id", "3")
	f.Set("quantity", "1")
	f.Set("disposition", "restock")
	f.Set("note", "fake note")
	for _, origin := range []string{"null", "https://foreign.example"} {
		w := handheldHTTPRequest(t, a, "POST", "/handheld/substitutions/preview", []*http.Cookie{phone}, f, map[string]string{"Origin": origin})
		if w.Code != 403 {
			t.Fatal("origin accepted")
		}
	}
	f.Set("csrf", "bad")
	if w := handheldHTTPRequest(t, a, "POST", "/handheld/substitutions/preview", []*http.Cookie{phone}, f, nil); w.Code != 403 {
		t.Fatal("phone csrf", w.Code)
	}
	if w := handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer}, url.Values{"csrf": {"bad"}}, nil); w.Code != 403 {
		t.Fatal("customer csrf", w.Code)
	}
	if w := handheldHTTPRequest(t, a, "POST", "/handheld/substitutions/preview", []*http.Cookie{phone}, url.Values{"csrf": {g.CSRF}, "note": {strings.Repeat("x", 9000)}}, nil); w.Code != 400 {
		t.Fatal("unbounded body", w.Code)
	}
	if before != fingerprintTest(t, s.db) {
		t.Fatal("refused requests changed state")
	}
	employee := employeeFixture(t, s)
	if err := s.SeedEmployeePractice(employee.Token, employee.CSRF); err != nil {
		t.Fatal(err)
	}
	q := employeeQueueTest(t, s, employee)
	var practice int64
	for _, i := range q.Queue {
		if i.ID != id {
			practice = i.ID
			break
		}
	}
	pg := employeeClaimTest(t, s, employee, practice)
	w := handheldHTTPRequest(t, a, "GET", "/handheld/substitutions", []*http.Cookie{{Name: handheldCookie, Value: pg.Token}}, nil, nil)
	if w.Code != 403 {
		t.Fatal("practice offered dead end", w.Code)
	}
}
func TestSubstitutionHTTPBackEditUsesFreshPreviewIdentity(t *testing.T) {
	s, a, _, _, _, phone, _, line := substitutionHTTPFixture(t)
	page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/substitutions?line=%d", line), []*http.Cookie{phone}, nil, nil)
	draft := handheldHTTPForm(t, page, "/handheld/substitutions/preview")
	draft.Set("replacement_id", "3")
	draft.Set("quantity", "1")
	draft.Set("disposition", "restock")
	draft.Set("note", "First fake replacement")
	first := handheldHTTPRequest(t, a, "POST", "/handheld/substitutions/preview", []*http.Cookie{phone}, draft, nil)
	send := handheldHTTPForm(t, first, "/handheld/substitutions/send")
	posted := handheldHTTPRequest(t, a, "POST", "/handheld/substitutions/send", []*http.Cookie{phone}, send, nil)
	if posted.Code != 303 {
		t.Fatal(posted.Code)
	}
	history := handheldHTTPRequest(t, a, "GET", "/handheld/substitutions", []*http.Cookie{phone}, nil, nil)
	withdraw := handheldHTTPForm(t, history, "/handheld/substitutions/withdraw")
	if w := handheldHTTPRequest(t, a, "POST", "/handheld/substitutions/withdraw", []*http.Cookie{phone}, withdraw, nil); w.Code != 303 {
		t.Fatal(w.Code)
	}
	// Restore an older preview/edit form with a consumed key, as browser Back can.
	draft.Set("command_key", send.Get("command_key"))
	draft.Set("note", "A reviewed different fake reason")
	second := handheldHTTPRequest(t, a, "POST", "/handheld/substitutions/preview", []*http.Cookie{phone}, draft, nil)
	fresh := handheldHTTPForm(t, second, "/handheld/substitutions/send")
	if fresh.Get("command_key") == send.Get("command_key") {
		t.Fatal("explicit new preview reused consumed identity")
	}
	if w := handheldHTTPRequest(t, a, "POST", "/handheld/substitutions/send", []*http.Cookie{phone}, fresh, nil); w.Code != 303 {
		t.Fatal(w.Code)
	}
	if testCount(t, s, "substitution_proposals") != 2 {
		t.Fatal("reviewed request did not persist")
	}
}

func TestSubstitutionChatIsNeverApprovalAndManagerNotesStayPrivate(t *testing.T) {
	s, a, owner, id, customer, phone, g, _ := substitutionHTTPFixture(t)
	proposal, _ := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "writeoff")
	testExec(t, s, `INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details,visibility) VALUES(?,?,?,'note','PRIVATE manager reason','PRIVATE manager note','internal')`, id, token(), strings.Repeat("a", 64))
	working := migrationQuerySnapshot(t, s.db, `SELECT * FROM working_order_items ORDER BY id`)
	stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
	convo, err := s.CustomerMessages(id, owner.ID, MessageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SendCustomerMessage(id, owner.ID, owner.CSRF, MessageCommand{ConversationKey: convo.ConversationKey, Key: token(), Body: "Yes, approve this replacement please", AssignmentID: convo.AssignmentID, AssignmentVersion: convo.AssignmentVersion})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err = s.db.QueryRow(`SELECT status FROM substitution_proposals WHERE id=?`, proposal.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatal("chat became approval", status, err)
	}
	if !reflect.DeepEqual(working, migrationQuerySnapshot(t, s.db, `SELECT * FROM working_order_items ORDER BY id`)) || !reflect.DeepEqual(stock, migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)) {
		t.Fatal("chat modified fulfillment")
	}
	for path, cookies := range map[string][]*http.Cookie{fmt.Sprintf("/orders/%d/substitutions", id): {customer}, "/handheld/substitutions": {phone}} {
		page := handheldHTTPRequest(t, a, "GET", path, cookies, nil, nil)
		if page.Code != 200 || strings.Contains(page.Body.String(), "PRIVATE manager") {
			t.Fatal("manager notes leaked", page.Code)
		}
	}
}
