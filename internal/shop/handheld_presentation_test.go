package shop

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestHandheldConfirmationDraftSurvivesInvalidAndStaleRereview(t *testing.T) {
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
	originalKey := pick.Get("command_key")
	if pick.Get("picked") != "1" {
		t.Fatal("fresh recognition did not suggest exactly one additional unit")
	}
	pick.Set("picked", "999")
	invalid := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, pick, nil)
	retry := handheldHTTPForm(t, invalid, "/handheld/pick")
	if retry.Get("picked") != "999" || retry.Get("command_key") != originalKey {
		t.Fatal("invalid quantity is not visibly recoverable")
	}
	pick.Set("picked", "1")
	o := testOrder(t, s, id, owner.ID)
	if err := s.AdvanceVersioned(id, "Placed", o.Version, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	before := testCount(t, s, "order_events")
	stale := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, pick, nil)
	if !strings.Contains(stale.Body.String(), "Unsaved picked count: 1") {
		t.Fatal("stale response hid the prior confirmation count")
	}
	continuation := handheldHTTPForm(t, stale, "/handheld/scan")
	if continuation.Get("resume_confirmation") != "1" || continuation.Get("picked") != "1" || continuation.Get("command_key") != originalKey || continuation.Get("code") != c.Code {
		t.Fatal("rereview lost a bounded draft or command identity")
	}
	if testCount(t, s, "order_events") != before {
		t.Fatal("stale review changed the order")
	}
	reviewed := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, continuation, nil)
	corrected := handheldHTTPForm(t, reviewed, "/handheld/pick")
	if corrected.Get("picked") != "1" || corrected.Get("command_key") == originalKey || len(corrected.Get("command_key")) < 16 {
		t.Fatal("explicit rereview must retain quantity and issue a new command identity")
	}
	saved := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, corrected, nil)
	if saved.Code != 200 || testCount(t, s, "order_events") != before+1 {
		t.Fatal("corrected explicit confirmation did not save once")
	}
}

func TestHandheldCSPAndPublicNavigationBoundaries(t *testing.T) {
	s := newTestStore(t)
	a := handheldHTTPApp(t, s)
	phone := handheldHTTPRequest(t, a, "GET", "/handheld/", nil, nil, nil)
	csp := phone.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data: blob:;") || !strings.Contains(csp, "media-src 'self' blob:;") || strings.Contains(csp, "unsafe-") {
		t.Fatal("phone local media policy incorrect")
	}
	home := handheldHTTPRequest(t, a, "GET", "/", nil, nil, nil)
	if strings.Contains(home.Header().Get("Content-Security-Policy"), "blob:") {
		t.Fatal("phone-only media permission widened storefront policy")
	}
	if !strings.Contains(home.Body.String(), `href="/handheld/"`) || !strings.Contains(home.Body.String(), "In-store shopping demo") {
		t.Fatal("storefront lacks clearly marked phone entry")
	}
	if !strings.Contains(phone.Body.String(), `action="/handheld/connect"`) || strings.Contains(phone.Body.String(), `class="manager-shell"`) {
		t.Fatal("phone surface is not independent and usable")
	}
	// Ordinary no-JavaScript POST remains available, with no implicit activation.
	fields := handheldHTTPForm(t, phone, "/handheld/connect")
	fields.Set("pairing_code", "not-a-connection")
	bad := handheldHTTPRequest(t, a, "POST", "/handheld/connect", phone.Result().Cookies(), url.Values(fields), nil)
	if bad.Code != 200 || !strings.Contains(bad.Body.String(), "not-a-connection") {
		t.Fatal("manual connect fallback lost its draft")
	}
}

func TestHandheldLostResponseEditedPayloadRecoveryCreatesNewIntent(t *testing.T) {
	for _, correctedCount := range []string{"0", "2"} {
		t.Run(correctedCount, func(t *testing.T) {
			s := newTestStore(t)
			a := handheldHTTPApp(t, s)
			_, _, cookie, g := handheldHTTPSetup(t, s, a)
			task := handheldTaskTest(t, s, g)
			line := task.Lines[0]
			page := handheldHTTPRequest(t, a, "GET", fmt.Sprintf("/handheld/?line=%d", line.LineID), []*http.Cookie{cookie}, nil, nil)
			scan := handheldHTTPForm(t, page, "/handheld/scan")
			c := handheldCommand(t, s, g, 0, 1)
			scan.Set("code", c.Code)
			review := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, scan, nil)
			original := handheldHTTPForm(t, review, "/handheld/pick")
			original.Set("picked", "1")
			originalKey := original.Get("command_key")
			before := testCount(t, s, "order_events")
			// The server saves, but the client keeps the original form as on a lost
			// response or Back navigation. It has not obtained a new command identity.
			_ = handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, original, nil)
			if testCount(t, s, "order_events") != before+1 {
				t.Fatal("first intent was not saved once")
			}
			once := fingerprintTest(t, s.db)
			exact := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, original, nil)
			if !strings.Contains(exact.Body.String(), "already saved with a picked count of 1") || fingerprintTest(t, s.db) != once {
				t.Fatal("exact lost-response retry changed original outcome")
			}
			changed, _ := url.ParseQuery(original.Encode())
			changed.Set("picked", correctedCount)
			conflict := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, changed, nil)
			continuation := handheldHTTPForm(t, conflict, "/handheld/scan")
			if continuation.Get("command_key") != originalKey || continuation.Get("picked") != correctedCount || continuation.Get("resume_confirmation") != "1" {
				t.Fatal("edited stale confirmation lost quantity or prematurely replaced key")
			}
			if fingerprintTest(t, s.db) != once {
				t.Fatal("changed payload wrote before explicit review")
			}
			wrong, _ := url.ParseQuery(continuation.Encode())
			wrong.Set("code", "SHOPDEMO-999999")
			wrongReview := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, wrong, nil)
			stillPending := handheldHTTPForm(t, wrongReview, "/handheld/scan")
			if stillPending.Get("command_key") != originalKey || stillPending.Get("picked") != correctedCount {
				t.Fatal("failed recognition lost pending retry identity")
			}
			rereview := handheldHTTPRequest(t, a, "POST", "/handheld/scan", []*http.Cookie{cookie}, continuation, nil)
			fresh := handheldHTTPForm(t, rereview, "/handheld/pick")
			if fresh.Get("command_key") == originalKey || len(fresh.Get("command_key")) < 16 || fresh.Get("picked") != correctedCount {
				t.Fatal("successful explicit review did not renew intent while preserving edited quantity")
			}
			if fingerprintTest(t, s.db) != once {
				t.Fatal("rereview automatically wrote or replayed a pick")
			}
			saved := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, fresh, nil)
			if !strings.Contains(saved.Body.String(), "Saved an absolute picked count of "+correctedCount) || testCount(t, s, "order_events") != before+2 {
				t.Fatal("new explicit intent failed or created duplicate events")
			}
			twice := fingerprintTest(t, s.db)
			// Both command identities retain their own deterministic original outcomes,
			// while replay never replaces the latest saved physical count.
			for _, retry := range []struct {
				form  url.Values
				count string
			}{{original, "1"}, {fresh, correctedCount}} {
				result := handheldHTTPRequest(t, a, "POST", "/handheld/pick", []*http.Cookie{cookie}, retry.form, nil)
				if !strings.Contains(result.Body.String(), "already saved with a picked count of "+retry.count) || fingerprintTest(t, s.db) != twice {
					t.Fatal("recovery changed deterministic replay or rewrote current count")
				}
			}
			var current string
			if err := s.db.QueryRow(`SELECT CAST(picked_quantity AS TEXT) FROM working_order_items WHERE id=?`, line.LineID).Scan(&current); err != nil || current != correctedCount {
				t.Fatal("replay overwrote the correction", current, err)
			}
		})
	}
}
