package shop

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func attentionHTTPFields(owner Session, c AttentionCommand, f OrderFilters) url.Values {
	v := f.Values()
	v.Set("csrf", owner.CSRF)
	v.Set("action", c.Action)
	v.Set("version", fmt.Sprint(c.Version))
	v.Set("command_key", c.Key)
	v.Set("reason", c.Reason)
	return v
}
func assertOrderContext(t *testing.T, markup string, f OrderFilters) {
	t.Helper()
	count := 0
	for _, form := range adminFormRE.FindAllStringSubmatch(markup, -1) {
		action := adminAttr(form[1], "action")
		if !strings.HasPrefix(action, "/manager/orders/") {
			continue
		}
		count++
		for _, field := range f.Fields() {
			if actual, _ := adminInput(form[2], field.Name); actual != field.Value {
				t.Errorf("%s lost %s: %q != %q", action, field.Name, actual, field.Value)
			}
		}
	}
	if count == 0 {
		t.Fatal("no order forms to check")
	}
}
func TestAttentionHTTPPrivacyScopeAndStaleRecovery(t *testing.T) {
	for _, hx := range []bool{false, true} {
		t.Run(fmt.Sprint(hx), func(t *testing.T) {
			s := newTestStore(t)
			a := pickingApp(t, s, true)
			owner, id := pickingFixture(t, s)
			owner = pickingLogin(t, a, owner)
			other := pickingLogin(t, a, testSession(t, s, ""))
			f := OrderFilters{Query: `apples & <bread> "demo"`, State: "Picking", Held: "held", Assigned: "unassigned", Sort: "oldest", Page: 3}
			path := fmt.Sprintf("/manager/orders/%d/attention", id)
			c := attentionCommand(t, s, id, owner.ID, "hold", `Private <script>alert("hold")</script> & receiving`)
			fields := attentionHTTPFields(owner, c, f)
			bad := attentionHTTPFields(other, c, f)
			bad.Set("version", "broken")
			w := testRequest(t, a, http.MethodPost, path, other, bad, pickingHeaders(hx))
			if w.Code != 404 || strings.Contains(w.Body.String(), "receiving") {
				t.Fatal("foreign error leaked", w.Code)
			}
			bad = attentionHTTPFields(owner, c, f)
			bad.Set("csrf", "bad")
			if w = testRequest(t, a, http.MethodPost, path, owner, bad, pickingHeaders(hx)); w.Code != 403 {
				t.Fatal("bad csrf")
			}
			for i := 0; i < 2; i++ {
				w = testRequest(t, a, http.MethodPost, path, owner, fields, pickingHeaders(hx))
				if hx {
					if w.Code != 200 || !strings.Contains(w.Body.String(), "Manager hold opened") {
						t.Fatal("hold not saved", w.Code, w.Body.String())
					}
					assertOrderContext(t, w.Body.String(), f)
					if w.Header().Get("HX-Push-Url") != f.URL(id) {
						t.Fatal("HX context", w.Header())
					}
				} else {
					assertRedirect(t, w, f.URL(id)+"#order-attention", false)
				}
			}
			// The same signed-in browser must still get a customer-safe tracker.
			for _, demo := range []bool{true, false} {
				a.config.DemoMode = demo
				for _, p := range []string{fmt.Sprintf("/orders/%d", id), fmt.Sprintf("/orders/%d/status", id), "/orders"} {
					w = testRequest(t, a, http.MethodGet, p, owner, nil, pickingHeaders(hx))
					body := w.Body.String()
					if w.Code != 200 || strings.Contains(body, "receiving") || strings.Contains(body, "alert(") || strings.Contains(body, "Manager only") {
						t.Fatal("customer private text", p, w.Code)
					}
					if p != "/orders" && !strings.Contains(body, "Under manager review") {
						t.Fatal("missing generic review", p)
					}
				}
			}
			a.config.DemoMode = true
			page := testRequest(t, a, http.MethodGet, f.URL(id), owner, nil, pickingHeaders(hx))
			assertOrderContext(t, page.Body.String(), f)
			if !strings.Contains(page.Body.String(), html.EscapeString(c.Reason)) || strings.Contains(page.Body.String(), `<script>alert`) {
				t.Fatal("hold not escaped")
			}
			if !adminFindLink(page.Body.String(), func(u *url.URL) bool { return u.String() == f.URL(0) }) {
				t.Fatal("missing queue context back link")
			}
			// Release has no required/minlength trap, and blank notes get useful audit.
			releaseForm := adminForm(page.Body.String(), path)
			_, attrs := adminInput(releaseForm, "reason")
			if strings.Contains(attrs, "required") || strings.Contains(attrs, "minlength") {
				t.Fatal("release note trapped")
			}
			release := attentionCommand(t, s, id, owner.ID, "release", "")
			w = testRequest(t, a, http.MethodPost, path, owner, attentionHTTPFields(owner, release, f), pickingHeaders(hx))
			if w.Code != 200 && w.Code != 303 {
				t.Fatal("release", w.Code)
			}
			// A stale release remains visible as an unsaved draft after someone released it.
			stale := attentionHTTPFields(owner, release, f)
			stale.Set("command_key", token())
			stale.Set("reason", `Unsaved <private> & note`)
			w = testRequest(t, a, http.MethodPost, path, owner, stale, pickingHeaders(hx))
			if w.Code != 200 || w.Header().Get("X-Shop-Error") != "stale-version" || !strings.Contains(w.Body.String(), "Unsaved &lt;private&gt; &amp; note") {
				t.Fatal("stale draft lost", w.Code, w.Body.String())
			}
			assertOrderContext(t, w.Body.String(), f)
			for _, p := range []string{fmt.Sprintf("/manager/orders/%d", id), fmt.Sprintf("/manager/orders/%d/products?context=add", id), "/manager/shoppers?order=" + fmt.Sprint(id)} {
				w = testRequest(t, a, http.MethodGet, p, other, nil, pickingHeaders(hx))
				if w.Code != 404 || strings.Contains(w.Body.String(), "receiving") {
					t.Fatal("foreign page leaked", p, w.Code)
				}
			}
			// Queue contents and counts remain scoped, including empty/out-of-range pages.
			for _, p := range []string{"/manager/orders?held=held&page=999999", "/manager/orders?q=receiving"} {
				w = testRequest(t, a, http.MethodGet, p, other, nil, pickingHeaders(hx))
				if w.Code != 200 || strings.Contains(w.Body.String(), "receiving</p>") || !strings.Contains(w.Body.String(), "Needs attention · 0") {
					t.Fatal("queue leak", p)
				}
			}
		})
	}
}
func TestAttentionHTTPCompleteContextThroughWorkAndWeight(t *testing.T) {
	for _, hx := range []bool{false, true} {
		t.Run(fmt.Sprint(hx), func(t *testing.T) {
			s := newTestStore(t)
			a := pickingApp(t, s, true)
			owner, id, _ := weightedHTTPFixture(t, s)
			owner = pickingLogin(t, a, owner)
			f := OrderFilters{Query: `pear & "bread"`, State: "Picking", Held: "held", Assigned: "unassigned", Sort: "reference", Page: 2}
			attentionApply(t, s, id, owner.ID, "hold", "Review weighed goods")
			o := testOrder(t, s, id, owner.ID)
			line := weightedHTTPLine(t, s, id, owner.ID)
			page := testRequest(t, a, http.MethodGet, f.URL(id), owner, nil, pickingHeaders(hx))
			assertOrderContext(t, page.Body.String(), f)
			picker := f.Values()
			picker.Set("context", "sub")
			picker.Set("line_id", fmt.Sprint(line.LineID))
			picker.Set("product_q", "pear")
			page = testRequest(t, a, http.MethodGet, fmt.Sprintf("/manager/orders/%d/products?", id)+picker.Encode(), owner, nil, pickingHeaders(hx))
			if page.Code != 200 {
				t.Fatal("picker", page.Code)
			}
			if !hx {
				assertOrderContext(t, page.Body.String(), f)
			}
			fields := weightHTTPFields(owner, o, line)
			for k, v := range f.Values() {
				fields[k] = v
			}
			base := fmt.Sprintf("/manager/orders/%d/lines/%d/weight", id, line.LineID)
			page = testRequest(t, a, http.MethodPost, base+"/preview", owner, fields, pickingHeaders(hx))
			if page.Code != 200 || !strings.Contains(page.Body.String(), "Not saved yet") {
				t.Fatal("preview", page.Code)
			}
			assertOrderContext(t, page.Body.String(), f)
			confirm := weightHTTPForm(t, page.Body.String(), base+"/confirm")
			page = testRequest(t, a, http.MethodPost, base+"/confirm", owner, confirm, pickingHeaders(hx))
			if hx {
				if page.Code != 200 || page.Header().Get("HX-Push-Url") != f.URL(id) {
					t.Fatal("confirm context", page.Code)
				}
				assertOrderContext(t, page.Body.String(), f)
			} else {
				assertRedirect(t, page, f.URL(id), false)
			}
			// Assignment round-trip carries a bounded nested queue context, never a return URL.
			ticket := testRequest(t, a, http.MethodGet, f.URL(id), owner, nil, nil).Body.String()
			var assignment string
			for _, link := range adminAnchorRE.FindAllStringSubmatch(ticket, -1) {
				u, _ := url.Parse(adminAttr(link[1], "href"))
				if u.Path == "/manager/shoppers" && u.Query().Get("order") != "" {
					assignment = u.String()
				}
			}
			if assignment == "" {
				t.Fatal("assignment link missing")
			}
			assignPage := testRequest(t, a, http.MethodGet, assignment, owner, nil, pickingHeaders(hx))
			if assignPage.Code != 200 || !adminFindLink(assignPage.Body.String(), func(u *url.URL) bool { return u.String() == f.URL(id) }) {
				t.Fatal("assignment back context", assignPage.Code)
			}
			assignForm := weightHTTPForm(t, assignPage.Body.String(), fmt.Sprintf("/manager/shoppers/orders/%d", id))
			if assignForm.Get("queue") != f.Values().Encode() {
				t.Fatal("assignment form context")
			}
			assignForm.Set("action", "assign")
			assignForm.Set("shopper_id", "1")
			assignForm.Set("reason", "Review task allocation")
			assigned := testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/shoppers/orders/%d", id), owner, assignForm, pickingHeaders(hx))
			if hx {
				if assigned.Code != 200 || !adminFindLink(assigned.Body.String(), func(u *url.URL) bool { return u.String() == f.URL(id) }) {
					t.Fatal("assignment result context")
				}
			} else {
				u, _ := url.Parse(assigned.Header().Get("Location"))
				if assigned.Code != 303 || u.Query().Get("queue") != f.Values().Encode() {
					t.Fatal("assignment redirect")
				}
			}
			// Ready/partial finish stays explicitly linked to the hold while working row controls remain.
			ticket = testRequest(t, a, http.MethodGet, f.URL(id), owner, nil, nil).Body.String()
			for _, want := range []string{"Review and release", "Mark picked", "Preview weight", "Cancel the entire order"} {
				if !strings.Contains(ticket, want) {
					t.Fatal("missing held control", want)
				}
			}
		})
	}
}
func TestAttentionHTTPQueueCountsPagingAndNormalization(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner := testManager(t, s)
	for i := 0; i < 105; i++ {
		testExec(t, s, `INSERT INTO orders(reference,session_id,checkout_key,total,status) VALUES(?,?,?,100,'Placed')`, fmt.Sprintf("QUEUE-%03d", i), owner.ID, token())
	}
	for _, hx := range []bool{false, true} {
		w := testRequest(t, a, http.MethodGet, "/manager/orders?page=6&sort=oldest", owner, nil, pickingHeaders(hx))
		if w.Code != 200 || strings.Count(w.Body.String(), `class="order-row"`) != 5 || !strings.Contains(w.Body.String(), "101–105 of 105 orders") {
			t.Fatal("page count", w.Code)
		}
		w = testRequest(t, a, http.MethodGet, "/manager/orders?state=wrong&held=wrong&sort=drop&page=-10", owner, nil, pickingHeaders(hx))
		if w.Code != 200 || strings.Count(w.Body.String(), `class="order-row"`) != 20 || !strings.Contains(w.Body.String(), "1–20 of 105 orders") {
			t.Fatal("normalization", w.Code)
		}
	}
	w := testRequest(t, a, http.MethodGet, "/orders?page=6", owner, nil, nil)
	if w.Code != 200 || strings.Count(w.Body.String(), `class="order-row"`) != 5 || strings.Contains(w.Body.String(), "Manager only") {
		t.Fatal("customer pagination")
	}
}

func TestAttentionHTTPHeldFinishDraftSurvivesRelease(t *testing.T) {
	for _, hx := range []bool{false, true} {
		t.Run(fmt.Sprint(hx), func(t *testing.T) {
			s := newTestStore(t)
			a := pickingApp(t, s, true)
			owner, id := pickingFixture(t, s)
			owner = pickingLogin(t, a, owner)
			f := OrderFilters{Query: "apples", State: "Picking", Held: "held", Assigned: "unassigned", Sort: "oldest", Page: 2}
			finish := overrideCommand(t, s, id, owner.ID, "finish", 0, 0)
			finish.Reason = `Draft <shortfall> & stock review`
			finish.Disposition = "writeoff"
			finish.Remainder = "unavailable"
			fields := overrideFields(owner, finish)
			fields.Set("form_id", "finish")
			for k, v := range f.Values() {
				fields[k] = v
			}
			attentionApply(t, s, id, owner.ID, "hold", "Concurrent hold for review")
			before := attentionManagerOrder(t, s, id, owner.ID)
			stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
			w := testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/orders/%d/override", id), owner, fields, pickingHeaders(hx))
			if w.Code != 200 || !strings.Contains(w.Body.String(), "Your partial-completion draft has not been saved.") || !strings.Contains(w.Body.String(), html.EscapeString(finish.Reason)) {
				t.Fatal("blocked finish draft hidden", w.Code)
			}
			releasePath := fmt.Sprintf("/manager/orders/%d/attention", id)
			release := weightHTTPForm(t, w.Body.String(), releasePath)
			if release.Get("recover_finish") != "1" || release.Get("finish_reason") != finish.Reason {
				t.Fatal("release lost draft")
			}
			w = testRequest(t, a, http.MethodPost, releasePath, owner, release, pickingHeaders(hx))
			if w.Code != 200 || !strings.Contains(w.Body.String(), "submit it separately") {
				t.Fatal("release did not restore draft", w.Code)
			}
			assertOrderContext(t, w.Body.String(), f)
			var restored string
			for _, form := range adminFormRE.FindAllStringSubmatch(w.Body.String(), -1) {
				if action, _ := adminInput(form[2], "action"); action == "finish" {
					restored = form[2]
				}
			}
			if reason, _ := adminInput(restored, "reason"); reason != finish.Reason {
				t.Fatal("restored finish reason", reason)
			}
			if !strings.Contains(restored, `value="writeoff" selected`) || !strings.Contains(restored, `value="unavailable" selected`) {
				t.Fatal("restored choices missing")
			}
			after := attentionManagerOrder(t, s, id, owner.ID)
			if after.Held || after.Finalized || after.Status != before.Status || after.Version != before.Version+1 {
				t.Fatal("release unexpectedly completed", after)
			}
			if got := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`); fmt.Sprint(got) != fmt.Sprint(stock) {
				t.Fatal("release changed stock")
			}
		})
	}
}

func TestAttentionHTTPAssignmentShortcutsKeepQueue(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner, id := pickingFixture(t, s)
	owner = pickingLogin(t, a, owner)
	if err := s.AssignShopper(id, owner.ID, false, ShopperCommand{Action: "assign", Key: token(), Version: 1, ShopperID: 1, Reason: "Simulated shopper assigned"}); err != nil {
		t.Fatal(err)
	}
	f := OrderFilters{Query: `apples & pears`, State: "Picking", Held: "held", Assigned: "assigned", Sort: "oldest", Page: 3}
	query := url.Values{"order": {fmt.Sprint(id)}, "queue": {f.Values().Encode()}}
	for _, hx := range []bool{false, true} {
		w := testRequest(t, a, http.MethodGet, "/manager/shoppers?"+query.Encode(), owner, nil, pickingHeaders(hx))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		count := 0
		for _, link := range adminAnchorRE.FindAllStringSubmatch(w.Body.String(), -1) {
			u, _ := url.Parse(adminAttr(link[1], "href"))
			text := adminVisibleText(link[2])
			if u.Path != "/manager/shoppers" || (u.RawQuery == "" && !strings.Contains(text, "Clear filters")) {
				continue
			}
			count++
			if u.Query().Get("queue") != f.Values().Encode() {
				t.Fatalf("shortcut %q lost queue: %s", text, u)
			}
		}
		if count < 9 {
			t.Fatal("insufficient shortcut coverage", count)
		}
	}
}
