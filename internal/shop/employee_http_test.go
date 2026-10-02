package shop

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func employeeHTTPStart(t *testing.T, s *Store, a *App) (*http.Cookie, EmployeeWorkspace) {
	t.Helper()
	w := handheldHTTPRequest(t, a, "GET", "/handheld/employee", nil, nil, nil)
	if w.Code != 200 {
		t.Fatalf("employee start: %d", w.Code)
	}
	c := handheldHTTPCookie(t, w, employeeCookie)
	if !c.HttpOnly || !c.Secure || c.Path != "/handheld/" || c.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe employee cookie")
	}
	for _, x := range w.Result().Cookies() {
		if x.Name == "shop_session" {
			t.Fatal("employee inherited customer identity")
		}
	}
	q, err := s.EmployeeQueue(c.Value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), "Your shift starts here.") || strings.Contains(w.Body.String(), `action="/manager`) {
		t.Fatal("dashboard missing or manager controls leaked")
	}
	if strings.Contains(w.Body.String(), c.Value) || strings.Contains(w.Body.String(), q.Employee.Hash) {
		t.Fatal("employee bearer leaked")
	}
	again := handheldHTTPRequest(t, a, "GET", "/handheld/employee", []*http.Cookie{c}, nil, nil)
	if !strings.Contains(again.Body.String(), q.Employee.Name) || testCount(t, s, "employee_sessions") != 1 {
		t.Fatal("refresh regenerated employee")
	}
	return c, q
}
func TestEmployeeHTTPSharedDashboardAndNativePicking(t *testing.T) {
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	home := handheldHTTPRequest(t, a, "GET", "/", nil, nil, nil)
	if !strings.Contains(home.Body.String(), "New fake orders enter the shared employee queue") || strings.Contains(home.Body.String(), "Your orders stay in this browser session") {
		t.Fatal("public demo boundary copy does not describe shared employee work")
	}
	owner, privateID := pickingFixture(t, s)
	private := testOrder(t, s, privateID, owner.ID)
	c, q := employeeHTTPStart(t, s, a)
	before := testCount(t, s, "sessions")
	page := handheldHTTPRequest(t, a, "GET", "/handheld/employee", []*http.Cookie{c}, nil, nil)
	if strings.Contains(page.Body.String(), private.Reference) {
		t.Fatal("private historical order enrolled")
	}
	form := handheldHTTPForm(t, page, "/handheld/employee/practice")
	loaded := handheldHTTPRequest(t, a, "POST", "/handheld/employee/practice", []*http.Cookie{c}, form, nil)
	if loaded.Code != 303 {
		t.Fatal("practice HTTP", loaded.Code)
	}
	q, err := s.EmployeeQueue(c.Value)
	if err != nil || len(q.Queue) != 3 {
		t.Fatal("practice queue", len(q.Queue), err)
	}
	q2page := handheldHTTPRequest(t, a, "GET", "/handheld/employee", nil, nil, nil)
	c2 := handheldHTTPCookie(t, q2page, employeeCookie)
	q2, err := s.EmployeeQueue(c2.Value)
	if err != nil || len(q2.Queue) != 3 || q2.Employee.ShopperID == q.Employee.ShopperID {
		t.Fatal("not shared queue with independent employees", err)
	}
	if testCount(t, s, "sessions") <= before {
		t.Fatal("missing synthetic practice owner")
	}
	item := q.Queue[0]
	page = handheldHTTPRequest(t, a, "GET", "/handheld/employee", []*http.Cookie{c}, nil, nil)
	route := fmt.Sprintf("/handheld/employee/%d/claim", item.ID)
	claim := handheldHTTPForm(t, page, route)
	claimed := handheldHTTPRequest(t, a, "POST", route, []*http.Cookie{c}, claim, map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "same-origin"})
	if claimed.Code != 303 || claimed.Header().Get("Location") != "/handheld/" {
		t.Fatal("native claim", claimed.Code)
	}
	phone := handheldHTTPCookie(t, claimed, handheldCookie)
	claimedAgain := handheldHTTPRequest(t, a, "POST", route, []*http.Cookie{c}, claim, nil)
	if claimedAgain.Code != 303 || handheldHTTPCookie(t, claimedAgain, handheldCookie).Value != phone.Value {
		t.Fatal("replayed claim rotated active grant")
	}
	losing := url.Values{"csrf": {q2.CSRF}, "command_key": {token()}, "version": {fmt.Sprint(item.Version)}}
	denied := handheldHTTPRequest(t, a, "POST", route, []*http.Cookie{c2}, losing, nil)
	if denied.Code != 200 || !strings.Contains(denied.Body.String(), ErrEmployeeClaim.Error()) {
		t.Fatal("claim conflict not recoverable", denied.Code)
	}
	task, g, err := s.HandheldTask(phone.Value)
	if err != nil {
		t.Fatal(err)
	}
	g.Token = phone.Value
	line := task.Lines[0]
	itemPage := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d", line.LineID), []*http.Cookie{phone, c}, nil, nil)
	scan := handheldHTTPForm(t, itemPage, "/handheld/scan")
	scan.Set("code", line.SKU)
	preview := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{phone, c}, scan, nil)
	pick := handheldHTTPForm(t, preview, "/handheld/pick")
	pick.Set("picked", "1")
	saved := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{phone, c}, pick, nil)
	if saved.Code != 200 || !strings.Contains(saved.Body.String(), "Saved an absolute picked count of 1") {
		t.Fatal("employee pick", saved.Code)
	}
	dashboard := handheldHTTPRequest(t, a, "GET", "/handheld/employee", []*http.Cookie{c}, nil, nil)
	readyRoute := fmt.Sprintf("/handheld/employee/%d/ready", item.ID)
	ready := handheldHTTPForm(t, dashboard, readyRoute)
	finished := handheldHTTPRequest(t, a, "POST", readyRoute, []*http.Cookie{c}, ready, nil)
	if finished.Code != 303 {
		t.Fatal("employee ready", finished.Code)
	}
	if _, _, err = s.HandheldTask(phone.Value); err != ErrHandheldAccess {
		t.Fatal("finished grant remains live", err)
	}
}
func TestEmployeeHTTPStrictOriginCSRFAndNoManagerAuthority(t *testing.T) {
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	c, q := employeeHTTPStart(t, s, a)
	if err := s.SeedEmployeePractice(c.Value, q.CSRF); err != nil {
		t.Fatal(err)
	}
	q, _ = s.EmployeeQueue(c.Value)
	item := q.Queue[0]
	routes := []string{"/handheld/employee/practice", fmt.Sprintf("/handheld/employee/%d/claim", item.ID), fmt.Sprintf("/handheld/employee/%d/release", item.ID), fmt.Sprintf("/handheld/employee/%d/ready", item.ID)}
	for _, route := range routes {
		form := url.Values{"csrf": {q.CSRF}, "version": {fmt.Sprint(item.Version)}, "command_key": {token()}}
		before := fingerprintTest(t, s.db)
		for _, origin := range []string{"null", "https://elsewhere.test", "http://phone.demo.test"} {
			w := handheldHTTPRequest(t, a, "POST", route, []*http.Cookie{c}, form, map[string]string{"Origin": origin, "X-Forwarded-Host": "phone.demo.test", "X-Forwarded-Proto": "https"})
			if w.Code != 403 {
				t.Fatal("foreign origin accepted", route, w.Code)
			}
		}
		if before != fingerprintTest(t, s.db) {
			t.Fatal("foreign origin changed state")
		}
		form.Set("csrf", "invalid")
		w := handheldHTTPRequest(t, a, "POST", route, []*http.Cookie{c}, form, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), ErrHandheldCSRF.Error()) {
			t.Fatal("CSRF feedback", route, w.Code)
		}
	}
	for _, route := range []string{"/manager/catalog/products", "/cart", "/checkout"} {
		w := handheldHTTPRequest(t, a, "POST", route, []*http.Cookie{c}, url.Values{"csrf": {q.CSRF}}, nil)
		if w.Code != 403 {
			t.Fatal("employee manager/customer authority", route, w.Code)
		}
	}
	privateApp, err := New(s, Config{Origin: "https://private.test"})
	if err != nil {
		t.Fatal(err)
	}
	w := handheldHTTPRequest(t, privateApp, "GET", "/handheld/employee", nil, nil, nil)
	if w.Code != 404 {
		t.Fatal("public employee flow exposed in non-demo mode", w.Code)
	}
}
func TestDemoHTTPCheckoutEnrolmentIsExplicitAndPrivateCheckoutStaysPrivate(t *testing.T) {
	s := newTestStore(t)
	privateOwner, privateID := pickingFixture(t, s)
	var exists int
	s.db.QueryRow(`SELECT COUNT(*) FROM employee_store_orders WHERE order_id=?`, privateID).Scan(&exists)
	if exists != 0 {
		t.Fatal("private store API enrolled order")
	}
	// Rendered demo HTTP form uses the shared checkout variant atomically.
	owner := testSession(t, s, "")
	if err := s.SetCart(owner.ID, 1, 1, true); err != nil {
		t.Fatal(err)
	}
	owner = testSession(t, s, owner.ID)
	basket, err := s.Basket(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.checkoutWithInstructions(owner.ID, owner.CheckoutKey, owner.Revision, basket.Quote, "Fake shared instruction", true)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.EmployeeSession("")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.EmployeeQueue(e.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Queue) != 1 || q.Queue[0].ID != id {
		t.Fatal("shared enrollment missing or private leaked")
	}
	again, err := s.checkoutWithInstructions(owner.ID, owner.CheckoutKey, owner.Revision, basket.Quote, "Fake shared instruction", true)
	if err != nil || again != id {
		t.Fatal("checkout retry duplicated")
	}
	if _, err = s.Order(id, privateOwner.ID, false); err != ErrNotFound {
		t.Fatal("shared employee order leaked customer receipt")
	}
}
