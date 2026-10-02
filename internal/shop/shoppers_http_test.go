package shop

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func shopperFields(session Session, c ShopperCommand) url.Values {
	return url.Values{"csrf": {session.CSRF}, "action": {c.Action}, "command_key": {c.Key}, "reason": {c.Reason}, "version": {fmt.Sprint(c.Version)}, "assignment_version": {fmt.Sprint(c.AssignmentVersion)}, "shopper_id": {fmt.Sprint(c.ShopperID)}}
}
func TestHTTPShoppersScopedSelectionCommandsDraftAndContext(t *testing.T) {
	for _, hx := range []bool{false, true} {
		t.Run(fmt.Sprint(hx), func(t *testing.T) {
			s := newTestStore(t)
			a := pickingApp(t, s, true)
			owner, id := pickingFixture(t, s)
			owner = pickingLogin(t, a, owner)
			other := pickingLogin(t, a, testSession(t, s, ""))
			path := fmt.Sprintf("/manager/shoppers/orders/%d", id)
			w := testRequest(t, a, "GET", fmt.Sprintf("/manager/shoppers?order=%d&q=no-match&status=unassigned", id), owner, nil, pickingHeaders(hx))
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			for _, text := range []string{"save-shopper-task", "Simulated shopper", "Scans per minute", "Unrecorded", `name="q" value="no-match"`, `name="assignment_version" value="0"`, "0 results", "Close without saving"} {
				if !strings.Contains(w.Body.String(), text) {
					t.Fatal("missing selected workspace", text)
				}
			}
			if strings.Contains(w.Body.String(), "<!doctype") == hx {
				t.Fatal("full/fragment mismatch")
			}
			c := shopperCommand(t, s, id, owner.ID, "assign", 1)
			for _, route := range []string{fmt.Sprintf("/manager/shoppers?order=%d", id), fmt.Sprintf("/manager/shoppers?order=%d&q=secret&status=all", id)} {
				w = testRequest(t, a, "GET", route, other, nil, pickingHeaders(hx))
				if w.Code != 404 {
					t.Fatal("foreign selection", w.Code)
				}
			}
			bad := shopperFields(other, c)
			bad.Set("version", "broken")
			bad.Set("reason", "private foreign attempted text")
			w = testRequest(t, a, "POST", path, other, bad, pickingHeaders(hx))
			if w.Code != 404 || strings.Contains(w.Body.String(), "private foreign") {
				t.Fatal("foreign invalid form leaked")
			}
			bad = shopperFields(owner, c)
			bad.Set("csrf", "wrong")
			w = testRequest(t, a, "POST", path, owner, bad, nil)
			if w.Code != 403 {
				t.Fatal("CSRF accepted")
			}
			fields := shopperFields(owner, c)
			fields.Set("q", "Avery & market")
			fields.Set("status", "active")
			fields.Set("shopper", "1")
			fields.Set("page", "2")
			w = testRequest(t, a, "POST", path, owner, fields, pickingHeaders(hx))
			if hx {
				if w.Code != 200 || !strings.Contains(w.Body.String(), "Shopper assignment saved") || w.Header().Get("HX-Push-Url") == "" {
					t.Fatal("HTMX assign", w.Code, w.Body.String())
				}
			} else {
				if w.Code != 303 {
					t.Fatal("normal assign", w.Code)
				}
				if !strings.Contains(w.Header().Get("Location"), "q=Avery+%26+market") {
					t.Fatal("redirect lost context")
				}
			}
			// Failed reassign keeps attempted text, but freshly read concurrency tokens.
			stale := shopperCommand(t, s, id, owner.ID, "reassign", 2)
			stale.Version--
			fields = shopperFields(owner, stale)
			fields.Set("reason", `Keep <script> & "quoted" notes`)
			fields.Set("q", "Avery & market")
			fields.Set("status", "all")
			w = testRequest(t, a, "POST", path, owner, fields, pickingHeaders(hx))
			body := w.Body.String()
			for _, text := range []string{"Your change was not saved", `Keep &lt;script&gt; &amp; &#34;quoted&#34; notes`, ErrConflict.Error(), `name="version" value="2"`, `value="2" selected`, `value="Avery &amp; market"`} {
				if !strings.Contains(body, text) {
					t.Fatal("draft/current context missing", text, body)
				}
			}
			if strings.Contains(body, "<script>") {
				t.Fatal("draft injection")
			}
			unknown := shopperFields(owner, shopperCommand(t, s, id, owner.ID, "reassign", 999))
			w = testRequest(t, a, "POST", path, owner, unknown, pickingHeaders(hx))
			if !strings.Contains(w.Body.String(), "Previous shopper unavailable; choose again") {
				t.Fatal("invalid shopper draft silently selected valid value")
			}
			// Close is an ordinary GET, with no submitted command and no task mutation.
			before := testOrder(t, s, id, owner.ID)
			w = testRequest(t, a, "GET", "/manager/shoppers?q=Avery+%26+market&status=all", owner, nil, pickingHeaders(hx))
			if w.Code != 200 || strings.Contains(w.Body.String(), `id="assignment-editor"`) || testOrder(t, s, id, owner.ID).Version != before.Version {
				t.Fatal("close mutated or left editor")
			}
			// Cancellation never presents the order as cancelled, and same-field replay is safe.
			cancel := shopperCommand(t, s, id, owner.ID, "cancel", 1)
			for range 2 {
				w = testRequest(t, a, "POST", path, owner, shopperFields(owner, cancel), pickingHeaders(hx))
				if w.Code != 200 && w.Code != 303 {
					t.Fatal("cancel", w.Code)
				}
			}
			o := testOrder(t, s, id, owner.ID)
			if o.Assignment != nil || o.Status != "Placed" || o.CompletionKind != "" {
				t.Fatal("task cancellation cancelled order")
			}
			w = testRequest(t, a, "GET", fmt.Sprintf("/manager/orders/%d", id), owner, nil, nil)
			if !strings.Contains(w.Body.String(), "SHOPPER ASSIGNMENT") || !strings.Contains(w.Body.String(), fmt.Sprintf(`/manager/shoppers?order=%d`, id)) {
				t.Fatal("ticket lacks linked assignment")
			}
			w = testRequest(t, a, "GET", "/manager/shoppers?status=all", other, nil, nil)
			if strings.Contains(w.Body.String(), c.Reason) || strings.Contains(w.Body.String(), o.Reference) {
				t.Fatal("other visitor history leaked")
			}
		})
	}
}
func TestHTTPShoppersNormalManagerAcrossOwners(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, false)
	owner, id := pickingFixture(t, s)
	manager := pickingLogin(t, a, testSession(t, s, ""))
	c := shopperCommand(t, s, id, owner.ID, "assign", 3)
	w := testRequest(t, a, "POST", fmt.Sprintf("/manager/shoppers/orders/%d", id), manager, shopperFields(manager, c), nil)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = testRequest(t, a, "GET", fmt.Sprintf("/manager/shoppers?order=%d", id), manager, nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Casey Rivera") || !strings.Contains(w.Body.String(), "Currently assigned to") {
		t.Fatal("normal manager denied store-wide task")
	}
}
func TestRealHTTPShoppersTaskLifecycleAndReadyEndsTask(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	a := pickingApp(t, s, true)
	owner = pickingLogin(t, a, owner)
	server := httptest.NewServer(a)
	defer server.Close()
	a.config.Origin = server.URL
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	call := func(method, path string, values url.Values, hx bool) (int, string, http.Header) {
		t.Helper()
		var body io.Reader
		if values != nil {
			body = strings.NewReader(values.Encode())
		}
		req, err := http.NewRequest(method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "shop_session", Value: owner.ID})
		if values != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", server.URL)
		}
		if hx {
			req.Header.Set("HX-Request", "true")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data), resp.Header
	}
	path := fmt.Sprintf("/manager/shoppers/orders/%d", id)
	for _, step := range []struct {
		action  string
		shopper int64
		hx      bool
	}{{"assign", 1, false}, {"reassign", 2, true}, {"cancel", 2, true}, {"assign", 3, false}} {
		c := shopperCommand(t, s, id, owner.ID, step.action, step.shopper)
		status, body, _ := call("POST", path, shopperFields(owner, c), step.hx)
		if (step.hx && status != 200) || (!step.hx && status != 303) {
			t.Fatal(step.action, status, body)
		}
	}
	if err := s.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	for _, line := range testOrder(t, s, id, owner.ID).WorkingItems {
		if err := s.RecordPicked(id, line.ProductID, line.Quantity, line.PickVersion, owner.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	o := testOrder(t, s, id, owner.ID)
	status, body, _ := call("POST", fmt.Sprintf("/manager/orders/%d/advance", id), url.Values{"csrf": {owner.CSRF}, "status": {"Picking"}, "order_version": {fmt.Sprint(o.Version)}}, true)
	if status != 200 || !strings.Contains(body, "No active picking task") {
		t.Fatal("ready failed autoend", status, body)
	}
	status, body, _ = call("GET", fmt.Sprintf("/manager/shoppers?status=closed&order=%d", id), nil, true)
	if status != 200 || !strings.Contains(body, "ended automatically: Ready") || strings.Contains(body, `id="save-shopper-task"`) {
		t.Fatal("terminal task controls/history", status, body)
	}
}
