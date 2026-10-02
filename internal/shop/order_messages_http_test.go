package shop

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func messageHTTPFixture(t *testing.T) (*Store, *App, Session, int64, *http.Cookie, *http.Cookie, HandheldSession) {
	t.Helper()
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	owner, id, phone, g := handheldHTTPSetup(t, s, a)
	return s, a, owner, id, &http.Cookie{Name: "shop_session", Value: owner.ID}, phone, g
}
func TestMessagesHTTPSeparateActorsAndPlainText(t *testing.T) {
	s, a, owner, id, customer, phone, _ := messageHTTPFixture(t)
	path := fmt.Sprintf("/orders/%d/messages", id)
	before := fingerprintTest(t, s.db)
	cpage := handheldHTTPRequest(t, a, "GET", path, []*http.Cookie{customer}, nil, nil)
	ppage := handheldHTTPRequest(t, a, "GET", "/handheld/messages", []*http.Cookie{phone}, nil, nil)
	if cpage.Code != 200 || ppage.Code != 200 || fingerprintTest(t, s.db) != before {
		t.Fatal("chat read mutated application state")
	}
	for _, w := range []string{cpage.Body.String(), ppage.Body.String()} {
		if strings.Contains(w, owner.ID) || strings.Contains(w, "handheld.js") || strings.Contains(w, "scanner.js") {
			t.Fatal("chat is not isolated from sender identity/scanner lifecycle")
		}
	}
	form := handheldHTTPForm(t, cpage, path)
	form.Set("body", "  A fake note <script>alert(1)</script>\r\nSecond line  ")
	sent := handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer}, form, map[string]string{"HX-Request": "true"})
	if sent.Code != 200 || sent.Header().Get("X-Messages-Result") != "sent" || sent.Header().Get("X-Messages-Key") != form.Get("command_key") || strings.Contains(sent.Body.String(), "<!doctype") {
		t.Fatal("missing scoped send acknowledgement")
	}
	once := fingerprintTest(t, s.db)
	replay := handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer}, form, map[string]string{"HX-Request": "true"})
	if replay.Header().Get("X-Messages-Result") != "sent" || fingerprintTest(t, s.db) != once {
		t.Fatal("HTTP replay duplicated message")
	}
	feed := handheldHTTPRequest(t, a, "GET", "/handheld/messages?feed=1", []*http.Cookie{phone}, nil, nil)
	if strings.Contains(feed.Body.String(), "<script>alert") || !strings.Contains(feed.Body.String(), "&lt;script&gt;") || strings.Contains(feed.Body.String(), "order-message-form") {
		t.Fatal("feed escaped/plaintext boundary failed")
	}
	pform := handheldHTTPForm(t, ppage, "/handheld/messages")
	pform.Set("body", "Fictional shopper reply")
	psent := handheldHTTPRequest(t, a, "POST", "/handheld/messages", []*http.Cookie{phone}, pform, nil)
	if psent.Code != 303 || psent.Header().Get("Location") != "/handheld/messages" {
		t.Fatal("normal HTML did not use redirect after save")
	}
	count := testCount(t, s, "order_messages")
	if count != 2 {
		t.Fatal(count)
	}
}
func TestMessagesHTTPAccessBeforeBadCursorAndNoSessionCreation(t *testing.T) {
	s, a, owner, id, customer, phone, g := messageHTTPFixture(t)
	path := fmt.Sprintf("/orders/%d/messages", id)
	other := testSession(t, s, "")
	if err := s.Manager(other.ID, true); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	for _, cookies := range [][]*http.Cookie{nil, {{Name: "shop_session", Value: other.ID}}, {phone}} {
		w := handheldHTTPRequest(t, a, "GET", path+"?after=bad&limit=-2", cookies, nil, nil)
		if w.Code != 403 || w.Header().Get("X-Messages-Access") != "ended" || strings.Contains(w.Body.String(), "DEMO-") || len(w.Result().Cookies()) != 0 {
			t.Fatal("foreign cursor leaked thread or minted session")
		}
	}
	own := handheldHTTPRequest(t, a, "GET", path+"?after=bad", []*http.Cookie{customer}, nil, nil)
	if own.Code != 400 {
		t.Fatal("own malformed cursor not rejected")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("chat denial mutated store")
	}
	if err := s.DisconnectHandheld(g.Token, g.CSRF); err != nil {
		t.Fatal(err)
	}
	revoked := handheldHTTPRequest(t, a, "GET", "/handheld/messages?before=bad", []*http.Cookie{phone}, nil, nil)
	if revoked.Code != 403 || strings.Contains(revoked.Body.String(), owner.ID) {
		t.Fatal("revoked phone retained chat")
	}
}
func TestMessagesHTTPAmbiguousCheckAndAssignmentReview(t *testing.T) {
	s, a, owner, id, customer, _, _ := messageHTTPFixture(t)
	path := fmt.Sprintf("/orders/%d/messages", id)
	page := handheldHTTPRequest(t, a, "GET", path, []*http.Cookie{customer}, nil, nil)
	form := handheldHTTPForm(t, page, path)
	form.Set("body", "Pending exact message")
	before := fingerprintTest(t, s.db)
	missing := handheldHTTPRequest(t, a, "POST", path+"/check", []*http.Cookie{customer}, form, map[string]string{"HX-Request": "true"})
	retry := handheldHTTPForm(t, missing, path)
	if missing.Header().Get("X-Messages-Result") != "absent" || retry.Get("command_key") != form.Get("command_key") || !strings.Contains(missing.Body.String(), "Pending exact message") || fingerprintTest(t, s.db) != before {
		t.Fatal("absent result fabricated delivery or a new key")
	}
	_ = handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer}, form, map[string]string{"HX-Request": "true"})
	once := fingerprintTest(t, s.db)
	found := handheldHTTPRequest(t, a, "POST", path+"/check", []*http.Cookie{customer}, form, map[string]string{"HX-Request": "true"})
	if found.Header().Get("X-Messages-Result") != "sent" || found.Header().Get("X-Messages-Key") != form.Get("command_key") || fingerprintTest(t, s.db) != once {
		t.Fatal("lookup mutated or failed to acknowledge saved result")
	}
	// A matching body under a changed assignment payload is not the same intent.
	changedContext, _ := url.ParseQuery(form.Encode())
	changedContext.Set("assignment_version", "999")
	mismatched := handheldHTTPRequest(t, a, "POST", path+"/check", []*http.Cookie{customer}, changedContext, map[string]string{"HX-Request": "true"})
	if mismatched.Header().Get("X-Messages-Result") != "stale" || !strings.Contains(mismatched.Body.String(), "Pending exact message") || fingerprintTest(t, s.db) != once {
		t.Fatal("lookup acknowledged changed assignment context")
	}
	// A changed draft under a saved key is never silently acknowledged/cleared.
	changed, _ := url.ParseQuery(form.Encode())
	changed.Set("body", "New deliberate text")
	conflict := handheldHTTPRequest(t, a, "POST", path+"/check", []*http.Cookie{customer}, changed, nil)
	corrected := handheldHTTPForm(t, conflict, path)
	if conflict.Header().Get("X-Messages-Result") != "stale" || corrected.Get("command_key") == form.Get("command_key") || !strings.Contains(conflict.Body.String(), "New deliberate text") {
		t.Fatal("mismatched saved payload discarded the new draft")
	}
	// A report increments fulfillment version but not assignment context. It must
	// not make an absent message check mint a new key.
	current := testOrder(t, s, id, owner.ID)
	if err := s.AttentionOrder(id, owner.ID, false, AttentionCommand{Action: "hold", Key: token(), Version: current.Version, Reason: "PRIVATE manager context"}); err != nil {
		t.Fatal(err)
	}
	fresh := handheldHTTPRequest(t, a, "GET", path, []*http.Cookie{customer}, nil, nil)
	pending := handheldHTTPForm(t, fresh, path)
	pending.Set("body", "Another exact draft")
	check := handheldHTTPRequest(t, a, "POST", path+"/check", []*http.Cookie{customer}, pending, nil)
	if check.Header().Get("X-Messages-Result") != "absent" || strings.Contains(check.Body.String(), "PRIVATE manager context") {
		t.Fatal("unrelated fulfillment version affected chat or private reason leaked")
	}
}
func TestMessagesHTTPReadDoesNotExpireBasketHolds(t *testing.T) {
	s, clock, _ := newReservationStore(t)
	a := handheldHTTPApp(t, s)
	owner, id := pickingFixture(t, s)
	testAssign(t, s, id, owner.ID, 1)
	testCart(t, s, owner.ID, 3, 1)
	clock.at(clock.now().Add(20 * time.Minute).Unix())
	before := fingerprintTest(t, s.db)
	page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/orders/%d/messages", id), []*http.Cookie{{Name: "shop_session", Value: owner.ID}}, nil, nil)
	if page.Code != 200 || fingerprintTest(t, s.db) != before {
		t.Fatal("chat GET reconciled basket inventory")
	}
}
func TestMessagesHTTPOversizeUnicodeCSRFAndClosedRead(t *testing.T) {
	s, a, owner, id, customer, phone, g := messageHTTPFixture(t)
	path := fmt.Sprintf("/orders/%d/messages", id)
	page := handheldHTTPRequest(t, a, "GET", path, []*http.Cookie{customer}, nil, nil)
	form := handheldHTTPForm(t, page, path)
	form.Set("body", strings.Repeat("🍐", 500))
	good := handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer}, form, nil)
	if good.Code != 303 || testCount(t, s, "order_messages") != 1 {
		t.Fatal("maximum valid Unicode cannot pass encoded form bound")
	}
	bad, _ := url.ParseQuery(form.Encode())
	bad.Set("command_key", token())
	bad.Set("body", strings.Repeat("x", 501))
	invalid := handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer}, bad, nil)
	if invalid.Header().Get("X-Messages-Result") != "invalid" || testCount(t, s, "order_messages") != 1 {
		t.Fatal("oversized body accepted")
	}
	bad.Set("body", "Fake note")
	bad.Set("csrf", "wrong")
	denied := handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer}, bad, nil)
	if denied.Code != 403 || denied.Header().Get("X-Messages-Access") != "ended" {
		t.Fatal("CSRF did not end unsafe composer")
	}
	task := handheldTaskTest(t, s, g)
	for i, l := range task.Lines {
		c := handheldCommand(t, s, g, i, l.Quantity)
		if _, err := s.ConfirmHandheldPick(g.Token, g.CSRF, c); err != nil {
			t.Fatal(err)
		}
	}
	order := testOrder(t, s, id, owner.ID)
	if err := s.AdvanceVersioned(id, "Picking", order.Version, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	closed := handheldHTTPRequest(t, a, "GET", path, []*http.Cookie{customer}, nil, nil)
	if closed.Code != 200 || closed.Header().Get("X-Messages-State") != "closed" || !strings.Contains(closed.Body.String(), "🍐") {
		t.Fatal("owner lost closed history")
	}
	revoked := handheldHTTPRequest(t, a, "GET", "/handheld/messages", []*http.Cookie{phone}, nil, nil)
	if revoked.Code != 403 {
		t.Fatal("Ready did not close phone conversation authority")
	}
}
