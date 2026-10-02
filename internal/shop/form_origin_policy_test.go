package shop

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A browser's navigate-mode form POST under no-referrer has an opaque (null)
// Origin, even to the same server. We must fix the document policy, not allow
// null origins or trust arbitrary reverse-proxy headers at the mutation gate.
func TestNativePhoneAndMessageFormsKeepSameOriginPolicy(t *testing.T) {
	s := newTestStore(t)
	origin := "https://instoreshopperexample-site-3az7di-fdadf8-2-25-70-220.sslip.io"
	a, err := New(s, Config{Origin: origin, ManagerPassword: "password", SecureCookies: true, DemoMode: true})
	if err != nil {
		t.Fatal(err)
	}
	owner, id, phone, _ := handheldHTTPSetup(t, s, a)
	customer := &http.Cookie{Name: "shop_session", Value: owner.ID}
	for _, tc := range []struct {
		path    string
		cookies []*http.Cookie
	}{
		{"/handheld/", nil},
		{"/handheld/", []*http.Cookie{phone}},
		{fmt.Sprintf("/manager/orders/%d/phone", id), []*http.Cookie{customer}},
		{fmt.Sprintf("/orders/%d/messages", id), []*http.Cookie{customer}},
		{"/handheld/messages", []*http.Cookie{phone}},
	} {
		w := handheldHTTPRequest(t, a, "GET", tc.path, tc.cookies, nil, nil)
		if w.Code != 200 || w.Header().Get("Referrer-Policy") != "same-origin" {
			t.Fatalf("%s: status %d policy %q", tc.path, w.Code, w.Header().Get("Referrer-Policy"))
		}
		if strings.Contains(w.Body.String(), `action="http`) || strings.Contains(w.Body.String(), `name="referrer"`) {
			t.Fatalf("%s overrides same-origin form navigation", tc.path)
		}
	}
	// Plain, non-HTMX fallback still works at the public HTTPS host while the
	// backend is a normal HTTP handler behind a proxy. Forwarded headers are not
	// needed and do not determine the trusted origin.
	path := fmt.Sprintf("/orders/%d/messages", id)
	page := handheldHTTPRequest(t, a, "GET", path, []*http.Cookie{customer}, nil, nil)
	form := handheldHTTPForm(t, page, path)
	form.Set("body", "Fake native form note")
	sent := handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer}, form, map[string]string{"Referer": origin + path, "Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate", "X-Forwarded-Proto": "https"})
	if sent.Code != 303 || testCount(t, s, "order_messages") != 1 {
		t.Fatal("same-origin native form failed", sent.Code)
	}
}

func TestPhoneOriginGuardRejectsNullForeignAndSpoofedForwarding(t *testing.T) {
	s, a, _, id, customer, phone, _ := messageHTTPFixture(t)
	paths := []string{"/handheld/connect", "/handheld/pick", "/handheld/messages", fmt.Sprintf("/orders/%d/messages", id), fmt.Sprintf("/manager/orders/%d/phone/issue", id)}
	before := fingerprintTest(t, s.db)
	for _, path := range paths {
		for _, origin := range []string{"null", "https://attacker.test", "http://phone.demo.test", "https://phone.demo.test:444"} {
			headers := map[string]string{"Origin": origin, "Referer": a.config.Origin + "/handheld/", "Sec-Fetch-Site": "same-origin", "X-Forwarded-Host": "phone.demo.test", "X-Forwarded-Proto": "https", "Forwarded": "proto=https;host=phone.demo.test"}
			w := handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer, phone}, url.Values{}, headers)
			if w.Code != 403 || !strings.Contains(w.Body.String(), "Cross-origin request rejected") {
				t.Fatalf("%s origin %q: %d", path, origin, w.Code)
			}
		}
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("rejected origins changed state")
	}
}
