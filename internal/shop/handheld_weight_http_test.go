package shop

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func phoneWeightHTTPFixture(t *testing.T) (*Store, *App, Session, int64, Product, *http.Cookie, HandheldSession, WorkingOrderItem) {
	t.Helper()
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	owner, id, p := weightedHTTPFixture(t, s)
	testAssign(t, s, id, owner.ID, 1)
	g := connectHandheldFixture(t, s, owner, id)
	return s, a, owner, id, p, &http.Cookie{Name: handheldCookie, Value: g.Token}, g, weightedHTTPLine(t, s, id, owner.ID)
}
func phoneWeightReviewForm(t *testing.T, s *Store, a *App, cookie *http.Cookie, g HandheldSession, line WorkingOrderItem, actual, disposition string, headers map[string]string) (*httptest.ResponseRecorder, url.Values) {
	t.Helper()
	page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d", line.LineID), []*http.Cookie{cookie}, nil, nil)
	scan := handheldHTTPForm(t, page, "/handheld/scan")
	var code string
	if err := s.db.QueryRow(`SELECT normalized_value FROM product_codes WHERE product_id=? AND scheme='demo_local' AND archived=0`, line.ProductID).Scan(&code); err != nil {
		t.Fatal(err)
	}
	scan.Set("code", code)
	recognized := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, scan, headers)
	fields := handheldHTTPForm(t, recognized, "/handheld/weight/preview")
	if fields.Get("actual") == "" {
		t.Fatal("measurement form has no visible initial grams")
	}
	fields.Set("actual", actual)
	fields.Set("disposition", disposition)
	fields.Set("note", "QA scale reading")
	review := handheldHTTPRequest(t, a, "POST", "/handheld/weight/preview", []*http.Cookie{cookie}, fields, headers)
	return review, fields
}
func TestHandheldHTTPWeightPreviewConfirmReplayAndScope(t *testing.T) {
	for _, hx := range []bool{false, true} {
		t.Run(fmt.Sprint(hx), func(t *testing.T) {
			s, a, owner, id, p, cookie, g, line := phoneWeightHTTPFixture(t)
			headers := pickingHeaders(hx)
			before := fingerprintTest(t, s.db)
			preview, _ := phoneWeightReviewForm(t, s, a, cookie, g, line, "527", "", headers)
			if fingerprintTest(t, s.db) != before {
				t.Fatal("recognition/preview mutated data")
			}
			for _, want := range []string{"527", "$1.75", "$1.84", "27 g", "Proposed working order total"} {
				if !strings.Contains(preview.Body.String(), want) {
					t.Fatalf("review missing %q", want)
				}
			}
			confirm := handheldHTTPForm(t, preview, "/handheld/weight/confirm")
			if len(confirm.Get("review")) != 64 || len(confirm.Get("command_key")) < 16 {
				t.Fatal("missing bounded explicit review")
			}
			// Customer identity cannot act as the phone, including with known form values.
			foreign := handheldHTTPRequest(t, a, "POST", "/handheld/weight/confirm", []*http.Cookie{{Name: "shop_session", Value: owner.ID}}, confirm, headers)
			if strings.Contains(foreign.Body.String(), "QA scale reading") || fingerprintTest(t, s.db) == "" {
				t.Fatal("phone draft leaked through foreign identity")
			}
			// A rejected actor may receive a bootstrap, so assert fulfillment separately.
			if testOrder(t, s, id, owner.ID).WorkingItems[1].Measured {
				t.Fatal("customer cookie measured phone item")
			}
			saved := handheldHTTPRequest(t, a, "POST", "/handheld/weight/confirm", []*http.Cookie{cookie}, confirm, headers)
			if !strings.Contains(saved.Body.String(), "Saved 527 g") {
				t.Fatal("missing saved outcome")
			}
			o := testOrder(t, s, id, owner.ID)
			l := weightedHTTPLine(t, s, id, owner.ID)
			if l.Picked != 527 || l.Allocated != 527 || !l.Measured || l.Subtotal != 184 || o.Total != 873 || o.Finalized || testProduct(t, s, p.ID).Stock != 2473 {
				t.Fatal("weight invariants", l, o.Total)
			}
			once := fingerprintTest(t, s.db)
			replay := handheldHTTPRequest(t, a, "POST", "/handheld/weight/confirm", []*http.Cookie{cookie}, confirm, headers)
			if !strings.Contains(replay.Body.String(), "already saved 527 g") || fingerprintTest(t, s.db) != once {
				t.Fatal("exact replay changed outcome")
			}
			if err := s.DisconnectHandheld(g.Token, g.CSRF); err != nil {
				t.Fatal(err)
			}
			revoked := handheldHTTPRequest(t, a, "POST", "/handheld/weight/confirm", []*http.Cookie{cookie}, confirm, headers)
			if revoked.Header().Get("X-Handheld-Error") != "revoked" || strings.Contains(revoked.Body.String(), "QA scale reading") || strings.Contains(revoked.Body.String(), "already saved 527") {
				t.Fatal("revocation did not precede replay")
			}
		})
	}
}
func TestHandheldHTTPWeightInvalidAndStaleDraftRecovery(t *testing.T) {
	s, a, owner, id, p, cookie, g, line := phoneWeightHTTPFixture(t)
	_, fields := phoneWeightReviewForm(t, s, a, cookie, g, line, "527", "", nil)
	fields.Set("actual", "not grams")
	invalid := handheldHTTPRequest(t, a, "POST", "/handheld/weight/preview", []*http.Cookie{cookie}, fields, nil)
	scan := handheldHTTPForm(t, invalid, "/handheld/scan")
	if scan.Get("actual") != "not grams" || scan.Get("resume_weight") != "1" || !strings.Contains(invalid.Body.String(), "not grams") {
		t.Fatal("invalid actual grams were hidden or sanitized")
	}
	again := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, scan, nil)
	fields = handheldHTTPForm(t, again, "/handheld/weight/preview")
	if fields.Get("actual") != "not grams" {
		t.Fatal("code re-review lost weight draft")
	}
	fields.Set("actual", "0")
	fields.Set("disposition", "writeoff")
	review := handheldHTTPRequest(t, a, "POST", "/handheld/weight/preview", []*http.Cookie{cookie}, fields, nil)
	confirm := handheldHTTPForm(t, review, "/handheld/weight/confirm")
	oldKey := confirm.Get("command_key")
	confirmWeight(t, s, id, owner.ID, p.ID, 500, "")
	stale := handheldHTTPRequest(t, a, "POST", "/handheld/weight/confirm", []*http.Cookie{cookie}, confirm, nil)
	if strings.Contains(stale.Body.String(), `action="/handheld/weight/confirm"`) {
		t.Fatal("stale approval survived")
	}
	scan = handheldHTTPForm(t, stale, "/handheld/scan")
	if scan.Get("actual") != "0" || scan.Get("disposition") != "writeoff" {
		t.Fatal("zero/disposition lost after stale confirm")
	}
	again = handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, scan, nil)
	fields = handheldHTTPForm(t, again, "/handheld/weight/preview")
	fields.Set("disposition", "writeoff")
	review = handheldHTTPRequest(t, a, "POST", "/handheld/weight/preview", []*http.Cookie{cookie}, fields, nil)
	confirm = handheldHTTPForm(t, review, "/handheld/weight/confirm")
	if confirm.Get("command_key") == oldKey {
		t.Fatal("explicit preview reused previous intent")
	}
	saved := handheldHTTPRequest(t, a, "POST", "/handheld/weight/confirm", []*http.Cookie{cookie}, confirm, nil)
	if !strings.Contains(saved.Body.String(), "Saved 0 g") || weightedHTTPLine(t, s, id, owner.ID).Picked != 0 || !weightedHTTPLine(t, s, id, owner.ID).Measured {
		t.Fatal("zero not explicitly saved")
	}
}

func TestHandheldHTTPWeightSecondReadDiscardsStaleApproval(t *testing.T) {
	s, a, owner, id, _, cookie, g, line := phoneWeightHTTPFixture(t)
	task := handheldTaskTest(t, s, g)
	var code string
	s.db.QueryRow(`SELECT normalized_value FROM product_codes WHERE product_id=? AND scheme='demo_local'`, line.ProductID).Scan(&code)
	c := HandheldWeight{HandheldScan: HandheldScan{LineID: line.LineID, AssignmentVersion: task.Assignment.Version, Version: task.Version, PickVersion: line.PickVersion, Code: code, Format: "Code128", Source: "manual"}, Actual: 527, Key: token()}
	p, err := s.PreviewHandheldWeight(g.Token, g.CSRF, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceVersioned(id, "Placed", task.Version, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", a.config.Origin+"/handheld/weight/preview", nil)
	r.AddCookie(cookie)
	r.PostForm = url.Values{"version": {fmt.Sprint(c.Version)}, "pick_version": {fmt.Sprint(c.PickVersion)}, "assignment_version": {fmt.Sprint(c.AssignmentVersion)}}
	w := httptest.NewRecorder()
	a.handheldPage(w, r, HandheldView{WeightDraft: &HandheldWeightDraft{Actual: "527"}, WeightReview: handheldWeightReview(p), Recognition: &p.Recognition, CodeDraft: code}, line.LineID)
	if strings.Contains(w.Body.String(), `action="/handheld/weight/confirm"`) || !strings.Contains(w.Body.String(), "Weight draft kept for review") {
		t.Fatal("second read attached stale approval to fresh state")
	}
}
func TestHandheldHTTPReportPrivacyReplayAndEditedRecovery(t *testing.T) {
	s, a, owner, id, p, cookie, g, line := phoneWeightHTTPFixture(t)
	page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d", line.LineID), []*http.Cookie{cookie}, nil, nil)
	report := handheldHTTPForm(t, page, "/handheld/report")
	report.Set("kind", "unavailable")
	report.Set("note", "QA cannot locate this demo item")
	original := testOrder(t, s, id, owner.ID)
	stock := testProduct(t, s, p.ID).Stock
	saved := handheldHTTPRequest(t, a, "POST", "/handheld/report", []*http.Cookie{cookie}, report, nil)
	if !strings.Contains(saved.Body.String(), "Your item report is saved") || !strings.Contains(saved.Body.String(), "Under manager review") {
		t.Fatal("missing report acknowledgment")
	}
	o := testOrder(t, s, id, owner.ID)
	if !o.Held || o.WorkingTotal != original.WorkingTotal || o.Total != original.Total || testProduct(t, s, p.ID).Stock != stock || weightedHTTPLine(t, s, id, owner.ID).Measured {
		t.Fatal("report mutated fulfillment")
	}
	var privateReason string
	if err := s.db.QueryRow(`SELECT attention_reason FROM orders WHERE id=?`, id).Scan(&privateReason); err != nil {
		t.Fatal(err)
	}
	if privateReason == "" || strings.Contains(saved.Body.String(), privateReason) {
		t.Fatal("private hold reason leaked")
	}
	if err := s.AttentionOrder(id, owner.ID, false, AttentionCommand{Action: "release", Key: token(), Version: o.Version}); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	replay := handheldHTTPRequest(t, a, "POST", "/handheld/report", []*http.Cookie{cookie}, report, nil)
	if !strings.Contains(replay.Body.String(), "already saved") || fingerprintTest(t, s.db) != before || testOrder(t, s, id, owner.ID).Held {
		t.Fatal("old report replay reopened hold")
	}
	// Back or lost response plus edited report cannot silently reuse a consumed key.
	changed, _ := url.ParseQuery(report.Encode())
	changed.Set("kind", "damaged")
	conflict := handheldHTTPRequest(t, a, "POST", "/handheld/report", []*http.Cookie{cookie}, changed, nil)
	retry := handheldHTTPForm(t, conflict, "/handheld/report")
	if retry.Get("command_key") == report.Get("command_key") || !strings.Contains(conflict.Body.String(), "QA cannot locate this demo item") {
		t.Fatal("edited report recovery stuck or lost note")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("report conflict mutated state")
	}
	retry.Set("kind", "damaged")
	retry.Set("note", "QA revised item report")
	corrected := handheldHTTPRequest(t, a, "POST", "/handheld/report", []*http.Cookie{cookie}, retry, nil)
	if !strings.Contains(corrected.Body.String(), "Your item report is saved") || !testOrder(t, s, id, owner.ID).Held {
		t.Fatal("explicit corrected report did not save")
	}
	if err := s.DisconnectHandheld(g.Token, g.CSRF); err != nil {
		t.Fatal(err)
	}
	revoked := handheldHTTPRequest(t, a, "POST", "/handheld/report", []*http.Cookie{cookie}, retry, nil)
	if revoked.Header().Get("X-Handheld-Error") != "revoked" || strings.Contains(revoked.Body.String(), "QA revised item report") {
		t.Fatal("revoked report exposed draft or replayed")
	}
}
