package shop

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func assertAppearanceControls(t *testing.T, body string) {
	t.Helper()
	header := regexp.MustCompile(`(?s)<header\b[^>]*>(.*?)</header>`).FindStringSubmatch(body)
	footer := regexp.MustCompile(`(?s)<footer\b[^>]*>(.*?)</footer>`).FindStringSubmatch(body)
	if len(header) != 2 || len(footer) != 2 {
		t.Fatal("page needs a header and footer")
	}
	buttons := regexp.MustCompile(`(?s)<button\b([^>]*)>(.*?)</button>`)
	controls := buttons.FindAllStringSubmatch(header[1], -1)
	if len(controls) != 1 {
		t.Fatalf("header has %d buttons, want one compact theme toggle", len(controls))
	}
	attrs := controls[0][1]
	if !strings.Contains(attrs, "data-theme-toggle") || adminAttr(attrs, "type") != "button" || adminAttr(attrs, "aria-label") != "Dark mode" || adminAttr(attrs, "aria-pressed") != "false" {
		t.Errorf("theme toggle is not a named native non-submit toggle: %s", attrs)
	}
	if adminAttr(attrs, "tabindex") != "" || strings.Contains(header[1], "<select") {
		t.Error("theme control must use native keyboard interaction and no header select")
	}
	if strings.Count(controls[0][2], `aria-hidden="true"`) != 2 || adminVisibleText(controls[0][2]) != "" {
		t.Error("header button needs decorative sun/moon icons without extra visible text")
	}
	for _, id := range strings.Fields(adminAttr(attrs, "aria-describedby")) {
		if strings.Count(body, `id="`+id+`"`) != 1 {
			t.Errorf("theme description %q must resolve uniquely", id)
		}
	}
	foundSystem := false
	for _, button := range buttons.FindAllStringSubmatch(footer[1], -1) {
		if !strings.Contains(button[1], "data-theme-system") {
			continue
		}
		foundSystem = true
		if adminAttr(button[1], "type") != "button" || adminVisibleText(button[2]) != "Use system appearance" {
			t.Error("footer system action must be a clear native non-submit button")
		}
	}
	if !foundSystem || !strings.Contains(footer[1], "Appearance: System") {
		t.Error("footer must expose current appearance and a discoverable system reset")
	}
}

func TestHTTPAppearanceControlsAcrossFullPagesAndFragments(t *testing.T) {
	s := newTestStore(t)
	a := reservationHTTPApp(t, s, true)
	manager := testManager(t, s)
	visitor := testSession(t, s, "")
	for _, htmx := range []bool{false, true} {
		for _, path := range []string{"/", "/products/1", "/cart", "/orders", "/manager/login", "/manager", "/manager/catalog", "/manager/stock", demoResetPath} {
			t.Run(fmt.Sprintf("%s/htmx_%t", path, htmx), func(t *testing.T) {
				headers := map[string]string{}
				if htmx {
					headers["HX-Request"] = "true"
				}
				session := manager
				if path == "/manager/login" {
					session = visitor
				}
				page := testRequest(t, a, http.MethodGet, path, session, nil, headers)
				if page.Code != http.StatusOK {
					t.Fatalf("GET %s returned %d", path, page.Code)
				}
				assertAppearanceControls(t, page.Body.String())
			})
		}
	}
}

func TestHTTPDemoGuideOnlyOnDemoDiscoverySurfaces(t *testing.T) {
	for _, demo := range []bool{false, true} {
		s := newTestStore(t)
		a := reservationHTTPApp(t, s, demo)
		manager := testManager(t, s)
		for _, htmx := range []bool{false, true} {
			for _, tc := range []struct {
				path      string
				discovery bool
			}{
				{"/", true}, {"/?q=apples&category=Produce", true}, {"/products/1", true},
				{"/products/999999", true}, {"/cart", false}, {"/orders", false},
				{"/manager", false}, {"/manager/catalog", false}, {"/manager/stock", false},
			} {
				t.Run(fmt.Sprintf("demo_%t/htmx_%t/%s", demo, htmx, tc.path), func(t *testing.T) {
					headers := map[string]string{}
					if htmx {
						headers["HX-Request"] = "true"
					}
					page := testRequest(t, a, http.MethodGet, tc.path, manager, nil, headers)
					if page.Code != http.StatusOK && page.Code != http.StatusNotFound {
						t.Fatalf("GET %s returned %d", tc.path, page.Code)
					}
					body := page.Body.String()
					if !htmx && strings.Contains(body, `src="/static/demo_guide.js"`) != demo {
						t.Error("demo guide enhancement must load only for full demo pages")
					}
					guide := regexp.MustCompile(`(?s)<aside\b([^>]*data-demo-guide[^>]*)>(.*?)</aside>`).FindStringSubmatch(body)
					wantGuide := demo && tc.discovery
					if (len(guide) != 0) != wantGuide || strings.Contains(body, "data-demo-guide-reopen") != wantGuide {
						t.Fatalf("guide visibility does not match demo discovery scope: want %t", wantGuide)
					}
					if !wantGuide {
						return
					}
					if !strings.Contains(guide[1], " hidden") || adminAttr(guide[1], "aria-label") != "Store management demo" || adminAttr(guide[1], "aria-modal") != "" {
						t.Error("guide must progressively enhance a labelled nonmodal region")
					}
					if !strings.Contains(guide[2], "Click here to try store management") || !strings.Contains(guide[2], `aria-label="Dismiss store management guide"`) || !strings.Contains(guide[2], `type="button"`) {
						t.Error("guide lacks requested guidance or native close action")
					}
					if !strings.Contains(guide[2], "Use the Employees navigation link") {
						t.Error("guide must name its destination independently of the visual arrow")
					}
					foundTarget := false
					for _, link := range adminAnchorRE.FindAllStringSubmatch(body, -1) {
						if strings.Contains(link[1], "data-demo-guide-target") {
							foundTarget = adminAttr(link[1], "href") == "/manager" && strings.Contains(adminVisibleText(link[2]), "Employees")
						}
					}
					if !foundTarget {
						t.Error("guide is not anchored to the real Employees navigation link")
					}
					if strings.Index(body, `id="workspace"`) > strings.Index(body, guide[0]) {
						t.Error("guide must be removed with the replaceable workspace")
					}
				})
			}
		}
	}
}
