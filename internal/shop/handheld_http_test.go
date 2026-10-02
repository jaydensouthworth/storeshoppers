package shop

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func handheldHTTPApp(t *testing.T, s *Store) *App {
	t.Helper()
	a, err := New(s, Config{Origin: "https://phone.demo.test", ManagerPassword: "password", DemoMode: true})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func handheldHTTPRequest(t *testing.T, a *App, method, path string, cookies []*http.Cookie, values url.Values, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body := ""
	if values != nil {
		body = values.Encode()
	}
	r := httptest.NewRequest(method, a.config.Origin+path, strings.NewReader(body))
	if values != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == "POST" {
		r.Header.Set("Origin", a.config.Origin)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func handheldHTTPCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name && c.MaxAge > 0 {
			return c
		}
	}
	t.Fatalf("missing %s cookie: %v", name, w.Header())
	return nil
}
func handheldHTTPForm(t *testing.T, w *httptest.ResponseRecorder, path string) url.Values {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("form response %d %s", w.Code, w.Body.String())
	}
	return weightHTTPForm(t, html.UnescapeString(w.Body.String()), path)
}
func handheldHTTPSetup(t *testing.T, s *Store, a *App) (Session, int64, *http.Cookie, HandheldSession) {
	t.Helper()
	owner, id := pickingFixture(t, s)
	testAssign(t, s, id, owner.ID, 1)
	if err := s.Manager(owner.ID, true); err != nil {
		t.Fatal(err)
	}
	owner = testSession(t, s, owner.ID)
	managerCookie := &http.Cookie{Name: "shop_session", Value: owner.ID}
	page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/manager/orders/%d/phone", id), []*http.Cookie{managerCookie}, nil, nil)
	issueURL := fmt.Sprintf("/manager/orders/%d/phone/issue", id)
	form := handheldHTTPForm(t, page, issueURL)
	issue := handheldHTTPRequest(t, a, "POST", issueURL, []*http.Cookie{managerCookie}, form, nil)
	if issue.Code != 200 {
		t.Fatal(issue.Code, issue.Body.String())
	}
	codes := regexp.MustCompile(`[A-Z2-7]{4}(?:-[A-Z2-7]{4}){5}-[A-Z2-7]{2}`).FindString(issue.Body.String())
	if codes == "" || !strings.Contains(issue.Body.String(), "data:image/png;base64,") {
		t.Fatal("missing grouped code/QR", issue.Body.String())
	}
	if strings.Contains(issue.Body.String(), "/handheld/?pair=") {
		t.Fatal("secret in URL query")
	}
	refreshed := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/manager/orders/%d/phone", id), []*http.Cookie{managerCookie}, nil, nil)
	if strings.Contains(refreshed.Body.String(), codes) {
		t.Fatal("raw code re-displayed after response")
	}
	phone := handheldHTTPRequest(t, a, "GET", "/handheld/", nil, nil, nil)
	boot := handheldHTTPCookie(t, phone, handheldPairCookie)
	form = handheldHTTPForm(t, phone, "/handheld/connect")
	form.Set("pairing_code", codes)
	connected := handheldHTTPRequest(t, a, "POST", "/handheld/connect", []*http.Cookie{boot}, form, nil)
	if connected.Code != 303 || connected.Header().Get("Location") != "/handheld/" {
		t.Fatal("connect", connected.Code, connected.Body.String())
	}
	cookie := handheldHTTPCookie(t, connected, handheldCookie)
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/handheld/" || cookie.MaxAge > int(handheldGrantTTL.Seconds()) {
		t.Fatal("unbounded/insecure cookie", cookie)
	}
	for _, c := range connected.Result().Cookies() {
		if c.Name == "shop_session" {
			t.Fatal("phone minted customer session")
		}
	}
	_, session, err := s.HandheldTask(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	session.Token = cookie.Value
	return owner, id, cookie, session
}
func TestHandheldHTTPIndependentDesktopPhoneAndAttackerJourney(t *testing.T) {
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	owner, id, cookie, g := handheldHTTPSetup(t, s, a)
	customerCount := testCount(t, s, "sessions")
	task := handheldTaskTest(t, s, g)
	line := task.Lines[0]
	testExec(t, s, `UPDATE orders SET attention_reason='DO_NOT_EXPOSE_MANAGER_REASON',attention_since=1 WHERE id=?`, id)
	page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d", line.LineID), []*http.Cookie{cookie}, nil, nil)
	if !strings.Contains(page.Body.String(), task.Reference) || strings.Contains(page.Body.String(), "DO_NOT_EXPOSE_MANAGER_REASON") || strings.Contains(page.Body.String(), owner.ID) || strings.Contains(page.Body.String(), owner.CSRF) {
		t.Fatal("unsafe/missing phone projection")
	}
	for _, who := range [][]*http.Cookie{nil, {{Name: "shop_session", Value: owner.ID}}, {{Name: handheldCookie, Value: owner.ID}}, {{Name: handheldCookie, Value: token()}}} {
		stranger := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d&owner=%s&allOrders=true", line.LineID, owner.ID), who, nil, nil)
		if stranger.Code != 200 || strings.Contains(stranger.Body.String(), task.Reference) {
			t.Fatal("unpaired visitor/customer gained worker projection", stranger.Code)
		}
	}
	if testCount(t, s, "sessions") != customerCount {
		t.Fatal("phone created customer sessions")
	}
	scan := handheldHTTPForm(t, page, "/handheld/scan")
	scan.Set("code", "SHOPDEMO-999999")
	wrong := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, scan, nil)
	if wrong.Code != 200 || !strings.Contains(wrong.Body.String(), "Nothing was picked") || strings.Contains(wrong.Body.String(), "action=\"/handheld/pick\"") {
		t.Fatal("wrong code confirmation", wrong.Code, wrong.Body.String())
	}
	c := handheldCommand(t, s, g, 0, 1)
	scan.Set("code", c.Code)
	scan.Set("owner_session_id", owner.ID)
	scan.Set("allOrders", "true")
	scan.Set("source", "photo")
	before := testOrder(t, s, id, owner.ID)
	preview := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, scan, nil)
	pick := handheldHTTPForm(t, preview, "/handheld/pick")
	if pick.Get("picked") != "1" || pick.Get("source") != "photo" {
		t.Fatal("suggestion/source", pick)
	}
	if testOrder(t, s, id, owner.ID).Version != before.Version {
		t.Fatal("preview changed order")
	}
	pick.Set("picked", "1")
	saved := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, pick, nil)
	if saved.Code != 200 || !strings.Contains(saved.Body.String(), "Saved an absolute picked count of 1") {
		t.Fatal(saved.Code, saved.Body.String())
	}
	events := testCount(t, s, "order_events")
	retry := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, pick, nil)
	if retry.Code != 200 || !strings.Contains(retry.Body.String(), "already saved") || testCount(t, s, "order_events") != events {
		t.Fatal("replayed HTTP pick", retry.Code, retry.Body.String())
	}
	desktop := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/orders/%d", id), []*http.Cookie{{Name: "shop_session", Value: owner.ID}}, nil, nil)
	if desktop.Code != 200 || !strings.Contains(desktop.Body.String(), "Picking") {
		t.Fatal("desktop did not show phone progress")
	}
	visitor := testSession(t, s, "")
	foreign := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/orders/%d", id), []*http.Cookie{{Name: "shop_session", Value: visitor.ID}}, nil, nil)
	if foreign.Code != 404 {
		t.Fatal("foreign customer accessed order")
	}
	// A worker capability cannot authorize manager or customer write endpoints.
	for _, route := range []string{"/cart", "/checkout", fmt.Sprintf("/manager/orders/%d/advance", id), fmt.Sprintf("/manager/orders/%d/phone/issue", id)} {
		denied := handheldHTTPRequest(t, a, "POST", route, []*http.Cookie{cookie}, url.Values{"csrf": {g.CSRF}, "owner_session_id": {owner.ID}}, nil)
		if denied.Code != 403 {
			t.Fatal("phone escalated", route, denied.Code)
		}
	}
	// A different owner's line identifier never changes the target grant/order.
	foreignOwner, foreignID := pickingFixture(t, s)
	foreignOrder := testOrder(t, s, foreignID, foreignOwner.ID)
	badLine := foreignOrder.WorkingItems[0].LineID
	scoped := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d", badLine), []*http.Cookie{cookie}, nil, nil)
	if strings.Contains(scoped.Body.String(), foreignOrder.Reference) || !strings.Contains(scoped.Body.String(), "no longer on this task") {
		t.Fatal("foreign line leaked")
	}
}
func TestHandheldHTTPManagerPairingGateScopeAndCSRF(t *testing.T) {
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	owner, id := pickingFixture(t, s)
	testAssign(t, s, id, owner.ID, 1)
	visitor := testSession(t, s, "")
	other := testSession(t, s, "")
	s.Manager(owner.ID, true)
	s.Manager(other.ID, true)
	owner = testSession(t, s, owner.ID)
	other = testSession(t, s, other.ID)
	route := fmt.Sprintf("/manager/orders/%d/phone", id)
	for _, action := range []string{"issue", "revoke"} {
		fields := url.Values{"csrf": {visitor.CSRF}, "version": {"bad"}, "assignment_version": {"bad"}, "command_key": {token()}}
		rejected := handheldHTTPRequest(t, a, "POST", route+"/"+action, []*http.Cookie{{Name: "shop_session", Value: visitor.ID}}, fields, nil)
		if rejected.Code != 303 {
			t.Fatal("manager gate", rejected.Code)
		}
		fields.Set("csrf", other.CSRF)
		rejected = handheldHTTPRequest(t, a, "POST", route+"/"+action, []*http.Cookie{{Name: "shop_session", Value: other.ID}}, fields, nil)
		if rejected.Code != 404 {
			t.Fatal("scope before validation", rejected.Code, rejected.Body.String())
		}
		fields.Set("csrf", "wrong")
		rejected = handheldHTTPRequest(t, a, "POST", route+"/"+action, []*http.Cookie{{Name: "shop_session", Value: owner.ID}}, fields, nil)
		if rejected.Code != 403 {
			t.Fatal("pairing CSRF", rejected.Code)
		}
	}
	if testCount(t, s, "handheld_invitations") != 0 || testCount(t, s, "handheld_grants") != 0 {
		t.Fatal("denied manager request mutated authority")
	}
}
func TestHandheldHTTPFailuresRetainDraftsAndRevocationStopsReplay(t *testing.T) {
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	owner, id, cookie, g := handheldHTTPSetup(t, s, a)
	task := handheldTaskTest(t, s, g)
	line := task.Lines[0]
	page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d", line.LineID), []*http.Cookie{cookie}, nil, nil)
	scan := handheldHTTPForm(t, page, "/handheld/scan")
	c := handheldCommand(t, s, g, 0, 1)
	scan.Set("code", c.Code)
	preview := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, scan, nil)
	pick := handheldHTTPForm(t, preview, "/handheld/pick")
	pick.Set("picked", "999")
	failure := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, pick, nil)
	if failure.Code != 200 || !strings.Contains(failure.Body.String(), c.Code) {
		t.Fatal("lost failed input", failure.Code, failure.Body.String())
	}
	// An unauthorized source or oversized input never creates a saved command.
	for _, field := range []string{"csrf", "source", "line_id", "assignment_version", "version", "pick_version"} {
		bad, _ := url.ParseQuery(pick.Encode())
		bad.Set("picked", "1")
		bad.Set(field, "invalid")
		before := testCount(t, s, "order_events")
		w := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, bad, nil)
		if w.Code != 200 || testCount(t, s, "order_events") != before {
			t.Fatal("invalid phone field", field, w.Code)
		}
	}
	oversized, _ := url.ParseQuery(scan.Encode())
	oversized.Set("code", strings.Repeat("a", 5000))
	if w := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, oversized, nil); w.Code != 400 {
		t.Fatal("unbounded form", w.Code)
	}
	cross := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, scan, map[string]string{"Origin": "https://attacker.test"})
	if cross.Code != 403 {
		t.Fatal("cross origin", cross.Code)
	}
	pick.Set("picked", "1")
	saved := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, pick, nil)
	if saved.Code != 200 {
		t.Fatal(saved.Code)
	}
	o := testOrder(t, s, id, owner.ID)
	if _, err := s.ChangeHandheldPairing(id, owner.ID, false, HandheldPairCommand{Version: o.Version, AssignmentVersion: o.Assignment.Version, Key: token()}, "revoke"); err != nil {
		t.Fatal(err)
	}
	before := testCount(t, s, "order_events")
	denied := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, pick, nil)
	if denied.Code != 200 || denied.Header().Get("X-Handheld-Error") != "revoked" || strings.Contains(denied.Body.String(), o.Reference) || !strings.Contains(denied.Body.String(), "disconnected") || testCount(t, s, "order_events") != before {
		t.Fatal("revoked HTTP replay leaked/saved", denied.Code, denied.Body.String())
	}
}

func TestHandheldHTTPRefusesPublicInsecureCapabilityAccess(t *testing.T) {
	s := newTestStore(t)
	a, err := New(s, Config{Origin: "http://public.demo.test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ method, path string }{{"GET", "/handheld/"}, {"POST", "/handheld/connect"}, {"POST", "/handheld/scan"}, {"POST", "/handheld/pick"}, {"POST", "/handheld/disconnect"}} {
		w := handheldHTTPRequest(t, a, test.method, test.path, nil, url.Values{"pairing_code": {"not-a-real-secret"}}, nil)
		if w.Code != http.StatusUpgradeRequired || len(w.Result().Cookies()) != 0 {
			t.Fatal("insecure public capability accepted", test.path, w.Code)
		}
	}
	if testCount(t, s, "handheld_pair_sessions") != 0 {
		t.Fatal("insecure visit minted pairing session")
	}
}
