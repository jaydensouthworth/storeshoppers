package shop

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func managerDraftHeaders(htmx bool) map[string]string {
	if htmx {
		return map[string]string{"HX-Request": "true"}
	}
	return nil
}

func managerDraftInput(t *testing.T, body, id string) string {
	t.Helper()
	for _, input := range adminInputRE.FindAllStringSubmatch(body, -1) {
		if adminAttr(input[1], "id") == id {
			return adminAttr(input[1], "value")
		}
	}
	t.Fatalf("missing draft input %q", id)
	return ""
}

func managerDraftForm(t *testing.T, body, inputID string) string {
	t.Helper()
	for _, form := range adminFormRE.FindAllStringSubmatch(body, -1) {
		for _, input := range adminInputRE.FindAllStringSubmatch(form[2], -1) {
			if adminAttr(input[1], "id") == inputID {
				return form[0]
			}
		}
	}
	t.Fatalf("missing form containing %q", inputID)
	return ""
}

func assertManagerDraftResponse(t *testing.T, body string, htmx bool, problem error) {
	t.Helper()
	if strings.Contains(strings.ToLower(body), "<!doctype html>") == htmx {
		t.Errorf("draft response did not preserve HTML/HTMX rendering mode (HTMX=%t)", htmx)
	}
	visible := strings.ToLower(adminVisibleText(body))
	if !strings.Contains(visible, strings.ToLower(problem.Error())) {
		t.Errorf("draft response lacks visible error %q", problem)
	}
	if !strings.Contains(visible, "not saved") {
		t.Error("draft response does not visibly distinguish submitted values from saved state")
	}
}

func TestManagerBasketDraftRetainsValidationInputHTMLAndHTMX(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		for _, kind := range []string{"line", "inferred line", "add", "add existing", "renew"} {
			t.Run(fmt.Sprintf("%s/htmx_%t", kind, htmx), func(t *testing.T) {
				s, _, _ := newReservationStore(t)
				a := reservationHTTPApp(t, s, false)
				manager := testManager(t, s)
				owner := testSession(t, s, "")
				testCart(t, s, owner.ID, 1, 2)
				b := testBasket(t, s, owner.ID)
				path := "/manager/baskets/" + b.ID
				reason := `Review "quoted" & <script>alert('draft')</script> groceries`
				values := reservationManagerValues(manager, b, "not-a-number")
				values.Set("reason", reason)
				quantityID, reasonID := "basket-qty-1", "basket-reason-1"
				switch kind {
				case "line":
					values.Set("draft_action", "line")
				case "add", "add existing":
					values.Set("draft_action", "add")
					if kind == "add" {
						values.Set("product_id", "2")
					}
					quantityID, reasonID = "basket-new-quantity", "basket-add-reason"
				case "renew":
					values.Set("revision", "not-a-revision")
					reasonID = "basket-renew-reason"
				}
				route := path + "/items"
				if kind == "renew" {
					route = path + "/renew"
				}
				before := reservationSnapshot(t, s)
				w := testRequest(t, a, http.MethodPost, route, manager, values, managerDraftHeaders(htmx))
				if w.Code != http.StatusOK {
					t.Fatalf("validation response=%d: %s", w.Code, w.Body.String())
				}
				body := w.Body.String()
				assertManagerDraftResponse(t, body, htmx, ErrInvalid)
				if got := managerDraftInput(t, body, reasonID); got != reason {
					t.Errorf("reason draft=%q, want %q", got, reason)
				}
				if strings.Contains(body, "<script>alert('draft')</script>") || !strings.Contains(body, html.EscapeString(reason)) {
					t.Error("submitted reason is not safely HTML escaped")
				}
				if kind != "renew" {
					if got := managerDraftInput(t, body, quantityID); got != values.Get("quantity") {
						t.Errorf("quantity draft=%q, want %q", got, values.Get("quantity"))
					}
					if !strings.Contains(adminVisibleText(body), "not-a-number") {
						t.Error("invalid number is lost to a number input without a visible draft summary")
					}
				}
				if kind == "add" || kind == "add existing" {
					selected := regexp.MustCompile(`<option\b[^>]*value="` + values.Get("product_id") + `"[^>]*\bselected\b`)
					if !selected.MatchString(body) {
						t.Error("add form lost its submitted product selection")
					}
					if got := managerDraftInput(t, body, "basket-qty-1"); got != "2" {
						t.Errorf("add-form draft contaminated persisted line input: %q", got)
					}
				} else if got := managerDraftInput(t, body, "basket-add-reason"); got != "" {
					t.Errorf("line/renewal draft contaminated the add form: %q", got)
				}
				form := managerDraftForm(t, body, reasonID)
				if got, _ := adminInput(form, "revision"); got != fmt.Sprint(b.Revision) {
					t.Errorf("recovery revision=%q, want current %d", got, b.Revision)
				}
				assertReservationUnchanged(t, s, before)
			})
		}
	}
}

func TestManagerBasketDraftConflictRequiresExplicitResubmit(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, false)
			manager := testManager(t, s)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 2)
			stale := testBasket(t, s, owner.ID)
			testCart(t, s, owner.ID, 1, 3)
			current := testBasket(t, s, owner.ID)
			values := reservationManagerValues(manager, stale, "4")
			values.Set("draft_action", "line")
			values.Set("reason", "Manager draft awaiting explicit review")
			path := "/manager/baskets/" + stale.ID
			before := reservationSnapshot(t, s)
			w := testRequest(t, a, http.MethodPost, path+"/items", manager, values, managerDraftHeaders(htmx))
			if w.Code != http.StatusOK {
				t.Fatalf("stale response=%d: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			assertManagerDraftResponse(t, body, htmx, ErrConflict)
			if got := managerDraftInput(t, body, "basket-qty-1"); got != "4" {
				t.Errorf("stale quantity draft=%q", got)
			}
			if got := managerDraftInput(t, body, "basket-reason-1"); got != values.Get("reason") {
				t.Errorf("stale reason draft=%q", got)
			}
			// The submitted number belongs in the editable field. The saved
			// quantity must also be independently visible outside every form.
			visibleSaved := strings.ToLower(adminVisibleText(adminFormRE.ReplaceAllString(body, " ")))
			if !regexp.MustCompile(`(?:saved|current)[^.!]{0,60}\b3\b`).MatchString(visibleSaved) {
				t.Errorf("current persisted quantity is not separately visible: %s", visibleSaved)
			}
			form := managerDraftForm(t, body, "basket-qty-1")
			revision, _ := adminInput(form, "revision")
			if revision != fmt.Sprint(current.Revision) {
				t.Fatalf("recovery revision=%q, want current %d", revision, current.Revision)
			}
			assertReservationUnchanged(t, s, before)
			values.Set("revision", revision)
			w = testRequest(t, a, http.MethodPost, path+"/items", manager, values, managerDraftHeaders(htmx))
			if htmx {
				if w.Code != http.StatusOK || strings.Contains(strings.ToLower(adminVisibleText(w.Body.String())), "not saved") {
					t.Fatalf("explicit HTMX resubmission failed: %d %s", w.Code, w.Body.String())
				}
			} else {
				assertRedirect(t, w, path, false)
			}
			if saved := testBasket(t, s, owner.ID); saved.Count != 4 || saved.Revision <= current.Revision {
				t.Errorf("explicit retry did not persist: %+v", saved)
			}
			if got := testCount(t, s, "basket_events"); got != len(before["basket_events"])+1 {
				t.Errorf("explicit retry audit count=%d", got)
			}
		})
	}
}

func TestManagerRenewalDraftConflictRetainsReasonAndFreshRevision(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, false)
			manager := testManager(t, s)
			testCart(t, s, manager.ID, 1, 2)
			stale := testBasket(t, s, manager.ID)
			testCart(t, s, manager.ID, 1, 3)
			current := testBasket(t, s, manager.ID)
			values := reservationManagerValues(manager, stale, "")
			values.Set("reason", `Reviewed "fresh" & <script>renew()</script>`)
			before := reservationSnapshot(t, s)
			w := testRequest(t, a, http.MethodPost, "/manager/baskets/"+stale.ID+"/renew", manager, values, managerDraftHeaders(htmx))
			if w.Code != http.StatusOK {
				t.Fatalf("renewal conflict=%d: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			assertManagerDraftResponse(t, body, htmx, ErrConflict)
			if got := managerDraftInput(t, body, "basket-renew-reason"); got != values.Get("reason") {
				t.Errorf("renewal reason=%q", got)
			}
			if strings.Contains(body, "<script>renew()</script>") {
				t.Error("renewal draft was not escaped")
			}
			form := managerDraftForm(t, body, "basket-renew-reason")
			if got, _ := adminInput(form, "revision"); got != fmt.Sprint(current.Revision) {
				t.Errorf("renewal recovery revision=%q, want %d", got, current.Revision)
			}
			assertReservationUnchanged(t, s, before)
		})
	}
}

func TestManagerDraftNeverReflectsUnauthorizedOrForeignBasketInput(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		for _, gate := range []string{"foreign demo", "visitor", "csrf"} {
			t.Run(fmt.Sprintf("%s/htmx_%t", gate, htmx), func(t *testing.T) {
				s, _, _ := newReservationStore(t)
				a := reservationHTTPApp(t, s, true)
				manager := testManager(t, s)
				owner := testSession(t, s, "")
				testCart(t, s, owner.ID, 1, 2)
				foreign := testBasket(t, s, owner.ID)
				session := manager
				if gate == "visitor" {
					session = owner
				}
				for _, endpoint := range []string{"items", "renew"} {
					values := reservationManagerValues(session, foreign, "bad-secret-quantity")
					values.Set("revision", "invalid")
					values.Set("reason", "foreign-draft-secret <script>bad()</script>")
					if gate == "csrf" {
						values.Set("csrf", "wrong")
					}
					before := reservationSnapshot(t, s)
					w := testRequest(t, a, http.MethodPost, "/manager/baskets/"+foreign.ID+"/"+endpoint, session, values, managerDraftHeaders(htmx))
					switch gate {
					case "foreign demo":
						if w.Code != http.StatusNotFound {
							t.Errorf("foreign validation draft returned %d, want 404", w.Code)
						}
					case "visitor":
						assertRedirect(t, w, "/manager/login", htmx)
					case "csrf":
						if w.Code != http.StatusForbidden {
							t.Errorf("CSRF draft returned %d, want 403", w.Code)
						}
					}
					for _, secret := range []string{"foreign-draft-secret", "bad-secret-quantity", foreign.ID, foreign.Label, owner.ID} {
						if strings.Contains(w.Body.String(), secret) {
							t.Errorf("rejected request reflected private/draft data %q", secret)
						}
					}
					assertReservationUnchanged(t, s, before)
				}
			})
		}
	}
}

func TestManagerBasketDraftBoundsUntrustedValuesAndMarksNormalization(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		for _, kind := range []string{"long reason", "control reason", "invalid UTF-8", "long quantity", "invalid product"} {
			t.Run(fmt.Sprintf("%s/htmx_%t", kind, htmx), func(t *testing.T) {
				s, _, _ := newReservationStore(t)
				a := reservationHTTPApp(t, s, false)
				manager := testManager(t, s)
				testCart(t, s, manager.ID, 1, 2)
				b := testBasket(t, s, manager.ID)
				values := reservationManagerValues(manager, b, "bad")
				values.Set("draft_action", "line")
				inputID, field, limit := "basket-reason-1", "reason", 500
				switch kind {
				case "long reason":
					values.Set("reason", strings.Repeat("界", 550))
				case "control reason":
					values.Set("reason", "Reason\x00with\x1bcontrols")
				case "invalid UTF-8":
					values.Set("reason", "Reason "+string([]byte{0xff})+" invalid")
				case "long quantity":
					values.Set("quantity", strings.Repeat("9", 100))
					inputID, field, limit = "basket-qty-1", "quantity", 64
				case "invalid product":
					values.Set("product_id", strings.Repeat("無", 100))
					values.Set("draft_action", "add")
					inputID, field, limit = "", "product_id", 64
				}
				before := reservationSnapshot(t, s)
				w := testRequest(t, a, http.MethodPost, "/manager/baskets/"+b.ID+"/items", manager, values, managerDraftHeaders(htmx))
				if w.Code != http.StatusOK {
					t.Fatalf("bounded draft=%d: %s", w.Code, w.Body.String())
				}
				body := w.Body.String()
				assertManagerDraftResponse(t, body, htmx, ErrInvalid)
				visible := strings.ToLower(adminVisibleText(body))
				if !regexp.MustCompile(`truncat|shorten|normaliz|replac|control|limit`).MatchString(visible) {
					t.Error("bounded/normalized draft lacks a visible explanation")
				}
				if inputID != "" {
					got := managerDraftInput(t, body, inputID)
					if !utf8.ValidString(got) || utf8.RuneCountInString(got) > limit || strings.IndexFunc(got, unicode.IsControl) >= 0 {
						t.Errorf("unsafe or unbounded draft field %s (%d runes): %q", field, utf8.RuneCountInString(got), got)
					}
					if got == "" {
						t.Errorf("recovery discarded the entire %s value", field)
					}
				}
				if kind == "long reason" || kind == "long quantity" || kind == "invalid product" {
					runes := []rune(values.Get(field))
					if !strings.Contains(body, string(runes[:limit])) || strings.Contains(body, string(runes[:limit+1])) {
						t.Errorf("%s draft not bounded to %d Unicode characters", field, limit)
					}
				}
				assertReservationUnchanged(t, s, before)
			})
		}
	}
}

func TestManagerStockDraftSearchAndFreshVersionRequireExplicitResubmit(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		for _, kind := range []string{"invalid delta", "stale version"} {
			t.Run(fmt.Sprintf("%s/htmx_%t", kind, htmx), func(t *testing.T) {
				s, _, _ := newReservationStore(t)
				a := reservationHTTPApp(t, s, false)
				manager := testManager(t, s)
				stale := testProduct(t, s, 1)
				if err := s.Adjust(1, 1, stale.Version, "Existing stock correction"); err != nil {
					t.Fatal(err)
				}
				current := testProduct(t, s, 1)
				reason := `Restock "review" & <script>draft()</script>`
				values := url.Values{"csrf": {manager.CSRF}, "product_id": {"1"}, "delta": {"2"}, "version": {fmt.Sprint(stale.Version)}, "reason": {reason}, "q": {stale.SKU}}
				problem := ErrConflict
				if kind == "invalid delta" {
					values.Set("delta", "not-a-number")
					problem = ErrInvalid
				}
				before := reservationSnapshot(t, s)
				w := testRequest(t, a, http.MethodPost, "/manager/inventory", manager, values, managerDraftHeaders(htmx))
				if w.Code != http.StatusOK {
					t.Fatalf("stock draft=%d: %s", w.Code, w.Body.String())
				}
				body := w.Body.String()
				assertManagerDraftResponse(t, body, htmx, problem)
				assertAdminNavigation(t, body, "stock", manager.CSRF)
				if got := managerDraftInput(t, body, "stock-search"); got != stale.SKU {
					t.Errorf("stock search lost: %q", got)
				}
				if got := managerDraftInput(t, body, "delta-1"); got != values.Get("delta") {
					t.Errorf("stock delta draft=%q", got)
				}
				if got := managerDraftInput(t, body, "reason-1"); got != reason {
					t.Errorf("stock reason draft=%q", got)
				}
				if strings.Contains(body, "<script>draft()</script>") || !strings.Contains(body, html.EscapeString(reason)) {
					t.Error("stock draft was not HTML escaped")
				}
				if strings.Contains(body, `id="delta-2"`) {
					t.Error("stock recovery discarded search filtering")
				}
				form := managerDraftForm(t, body, "delta-1")
				version, _ := adminInput(form, "version")
				if version != fmt.Sprint(current.Version) {
					t.Fatalf("stock recovery version=%q, want %d", version, current.Version)
				}
				if got, _ := adminInput(form, "q"); got != stale.SKU {
					t.Errorf("stock recovery form lost search: %q", got)
				}
				assertReservationUnchanged(t, s, before)
				values.Set("version", version)
				values.Set("delta", "2")
				w = testRequest(t, a, http.MethodPost, "/manager/inventory", manager, values, managerDraftHeaders(htmx))
				if htmx {
					if w.Code != http.StatusOK || strings.Contains(strings.ToLower(adminVisibleText(w.Body.String())), "not saved") {
						t.Fatalf("explicit stock retry failed: %d %s", w.Code, w.Body.String())
					}
				} else {
					assertRedirect(t, w, "/manager/stock?q="+url.QueryEscape(stale.SKU), false)
				}
				if got := testProduct(t, s, 1); got.Stock != current.Stock+2 || got.Version != current.Version+1 {
					t.Errorf("explicit stock retry did not persist exactly once: %+v", got)
				}
				if got := testCount(t, s, "adjustments"); got != len(before["adjustments"])+1 {
					t.Errorf("explicit stock retry audit count=%d", got)
				}
			})
		}
	}
}

func TestManagerBasketDraftRemovedLineRecoversInAddForm(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, false)
			manager := testManager(t, s)
			testCart(t, s, manager.ID, 1, 2)
			stale := testBasket(t, s, manager.ID)
			testCart(t, s, manager.ID, 1, 0)
			current := testBasket(t, s, manager.ID)
			values := reservationManagerValues(manager, stale, "4")
			values.Set("draft_action", "line")
			values.Set("reason", "Review this removed item before adding it again")
			before := reservationSnapshot(t, s)
			w := testRequest(t, a, http.MethodPost, "/manager/baskets/"+stale.ID+"/items", manager, values, managerDraftHeaders(htmx))
			if w.Code != http.StatusOK {
				t.Fatalf("removed-line response=%d: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			assertManagerDraftResponse(t, body, htmx, ErrConflict)
			if got := managerDraftInput(t, body, "basket-new-quantity"); got != "4" {
				t.Errorf("removed-line quantity lost: %q", got)
			}
			if got := managerDraftInput(t, body, "basket-add-reason"); got != values.Get("reason") {
				t.Errorf("removed-line reason lost: %q", got)
			}
			if !regexp.MustCompile(`<option\b[^>]*value="1"[^>]*\bselected\b`).MatchString(body) {
				t.Error("removed-line product not selected in recovery form")
			}
			form := managerDraftForm(t, body, "basket-new-quantity")
			if got, _ := adminInput(form, "revision"); got != fmt.Sprint(current.Revision) {
				t.Errorf("removed-line revision=%q, want %d", got, current.Revision)
			}
			if strings.Contains(body, `id="basket-qty-1"`) {
				t.Error("removed line appeared to remain saved")
			}
			assertReservationUnchanged(t, s, before)
		})
	}
}

func TestManagerStockDraftKeepsRenamedProductVisibleInSearch(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, false)
			manager := testManager(t, s)
			stale := testProduct(t, s, 1)
			if err := s.Adjust(1, 1, stale.Version, "Current inventory correction"); err != nil {
				t.Fatal(err)
			}
			current := testProduct(t, s, 1)
			current.Name = "Renamed orchard fruit"
			if _, err := s.SaveProduct(current); err != nil {
				t.Fatal(err)
			}
			current = testProduct(t, s, 1)
			values := url.Values{"csrf": {manager.CSRF}, "product_id": {"1"}, "delta": {"3"}, "version": {fmt.Sprint(stale.Version)}, "reason": {"Retain this correction after catalog rename"}, "q": {stale.Name}}
			before := reservationSnapshot(t, s)
			w := testRequest(t, a, http.MethodPost, "/manager/inventory", manager, values, managerDraftHeaders(htmx))
			if w.Code != http.StatusOK {
				t.Fatalf("renamed stock response=%d: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			assertManagerDraftResponse(t, body, htmx, ErrConflict)
			if got := managerDraftInput(t, body, "stock-search"); got != stale.Name {
				t.Errorf("rename recovery lost search=%q", got)
			}
			if !strings.Contains(adminVisibleText(body), current.Name) {
				t.Error("current product identity disappeared from searched stock recovery")
			}
			if got := managerDraftInput(t, body, "delta-1"); got != "3" {
				t.Errorf("rename recovery lost delta=%q", got)
			}
			if got := managerDraftInput(t, body, "reason-1"); got != values.Get("reason") {
				t.Errorf("rename recovery lost reason=%q", got)
			}
			form := managerDraftForm(t, body, "delta-1")
			if got, _ := adminInput(form, "version"); got != fmt.Sprint(current.Version) {
				t.Errorf("rename recovery version=%q, want %d", got, current.Version)
			}
			assertReservationUnchanged(t, s, before)
		})
	}
}

func TestManagerStockDraftBoundsAndNormalizesFields(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, false)
			manager := testManager(t, s)
			p := testProduct(t, s, 1)
			for _, reason := range []string{strings.Repeat("界", 550), "Review\x00with\x1bcontrol", "Invalid " + string([]byte{0xff}) + " text"} {
				values := url.Values{"csrf": {manager.CSRF}, "product_id": {"1"}, "delta": {strings.Repeat("9", 100)}, "version": {fmt.Sprint(p.Version)}, "reason": {reason}, "q": {p.SKU}}
				before := reservationSnapshot(t, s)
				w := testRequest(t, a, http.MethodPost, "/manager/inventory", manager, values, managerDraftHeaders(htmx))
				if w.Code != http.StatusOK {
					t.Fatalf("stock bound response=%d: %s", w.Code, w.Body.String())
				}
				body := w.Body.String()
				assertManagerDraftResponse(t, body, htmx, ErrInvalid)
				if !regexp.MustCompile(`truncat|shorten|normaliz|replac`).MatchString(strings.ToLower(adminVisibleText(body))) {
					t.Error("stock draft lacks visible truncation/normalization notice")
				}
				if got := managerDraftInput(t, body, "delta-1"); got != strings.Repeat("9", 64) {
					t.Errorf("stock numeric draft not bounded to 64 characters: %q", got)
				}
				got := managerDraftInput(t, body, "reason-1")
				if got == "" || !utf8.ValidString(got) || utf8.RuneCountInString(got) > 500 || strings.IndexFunc(got, unicode.IsControl) >= 0 {
					t.Errorf("unsafe/bounded stock reason=%q", got)
				}
				if strings.HasPrefix(reason, "界") && got != strings.Repeat("界", 500) {
					t.Error("stock reason was not retained to the Unicode character limit")
				}
				assertReservationUnchanged(t, s, before)
			}
		})
	}
}
