package shop

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func weightedHTTPFixture(t *testing.T, s *Store) (Session, int64, Product) {
	t.Helper()
	p := testCreateProduct(t, s, func(p *Product) {
		p.Name, p.SaleUnit, p.Price, p.PriceBasis, p.QuantityStep = "Weighed market pears", "g", 349, 1000, 100
	})
	if err := s.Adjust(p.ID, 3000, p.Version, "Fake gram stock"); err != nil {
		t.Fatal(err)
	}
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, 500)
	testCart(t, s, owner.ID, 1, 2)
	return owner, testCheckout(t, s, owner.ID), testProduct(t, s, p.ID)
}
func weightedHTTPLine(t *testing.T, s *Store, id int64, owner string) WorkingOrderItem {
	t.Helper()
	for _, line := range testOrder(t, s, id, owner).WorkingItems {
		if line.SaleUnit == "g" {
			return line
		}
	}
	t.Fatal("weighted working line missing")
	return WorkingOrderItem{}
}
func weightHTTPFields(owner Session, o Order, line WorkingOrderItem) url.Values {
	return url.Values{"csrf": {owner.CSRF}, "pick_version": {fmt.Sprint(line.PickVersion)}, "order_version": {fmt.Sprint(o.Version)}, "command_key": {token()}, "actual": {"527"}, "reason": {"Reviewed fake scale reading"}}
}
func weightHTTPForm(t *testing.T, body, path string) url.Values {
	t.Helper()
	form := adminForm(body, path)
	if form == "" {
		t.Fatalf("missing actual-weight form %s", path)
	}
	values := url.Values{}
	for _, input := range adminInputRE.FindAllStringSubmatch(form, -1) {
		if name := adminAttr(input[1], "name"); name != "" {
			values.Set(name, adminAttr(input[1], "value"))
		}
	}
	return values
}

func TestHTTPWeightedRetainedStepAndPartialFinishSummary(t *testing.T) {
	s := newTestStore(t)
	app := pickingApp(t, s, true)
	owner, id, p := weightedHTTPFixture(t, s)
	owner = pickingLogin(t, app, owner)
	if err := s.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	o := testOrder(t, s, id, owner.ID)
	if err := s.RecordPicked(id, 1, 1, o.WorkingItems[0].PickVersion, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	page := testRequest(t, app, http.MethodGet, fmt.Sprintf("/manager/orders/%d", id), owner, nil, nil)
	text := adminVisibleText(page.Body.String())
	for _, want := range []string{"recorded picked quantities listed below", "Honeycrisp apples: 1 units picked", "unmeasured, no grams fulfilled", "totaling $3.49", "Finish with recorded picked quantities"} {
		if !strings.Contains(text, want) {
			t.Fatalf("partial finish summary missing %q", want)
		}
	}
	if strings.Contains(text, "Finish with 0 picked product lines") {
		t.Fatal("completed-line count misrepresented partial picks")
	}
	c := overrideCommand(t, s, id, owner.ID, "set", p.ID, 0)
	if err := s.OverrideOrder(id, owner.ID, false, c); err != nil {
		t.Fatal(err)
	}
	p = testProduct(t, s, p.ID)
	p.QuantityStep = 25
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	page = testRequest(t, app, http.MethodGet, fmt.Sprintf("/manager/orders/%d/products?context=add&product_q=Weighed", id), owner, nil, pickingHeaders(true))
	wantName := fmt.Sprintf("quantity_%d", p.ID)
	found := false
	for _, input := range adminInputRE.FindAllStringSubmatch(page.Body.String(), -1) {
		if adminAttr(input[1], "name") == wantName {
			found = true
			if adminAttr(input[1], "min") != "100" || adminAttr(input[1], "step") != "100" || adminAttr(input[1], "value") != "100" {
				t.Fatal("retained line used current catalog step", input[1])
			}
		}
	}
	if !found {
		t.Fatal("retained product absent from picker")
	}
}

func TestHTTPWeightPostRecoveryNeverCreatesReplacementIdentity(t *testing.T) {
	s := newTestStore(t)
	app := pickingApp(t, s, true)
	owner, id, _ := weightedHTTPFixture(t, s)
	owner = pickingLogin(t, app, owner)
	// Model a session that expired after the command's initial form check, but
	// before preview/error rendering. POST recovery must require a fresh GET.
	testExec(t, s, `UPDATE sessions SET expires=0 WHERE id=?`, owner.ID)
	before, _ := databaseFingerprint(s.db)
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/manager/orders/%d/lines/2/weight/preview", id), strings.NewReader(url.Values{"csrf": {owner.CSRF}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "shop_session", Value: owner.ID})
	w := httptest.NewRecorder()
	app.pickingWeightView(w, r, id, "", ErrWeightReview, &WeightDraft{LineID: 2, Actual: "527"}, nil)
	if w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
		t.Fatalf("POST minted identity or showed stale view: %d %v", w.Code, w.Result().Cookies())
	}
	after, _ := databaseFingerprint(s.db)
	if before != after {
		t.Fatal("expired POST recovery changed data")
	}
}

func TestHTTPWeightOptionalNotesAndPickerServerValidation(t *testing.T) {
	s := newTestStore(t)
	app := pickingApp(t, s, true)
	owner, id, _ := weightedHTTPFixture(t, s)
	owner = pickingLogin(t, app, owner)
	o := testOrder(t, s, id, owner.ID)
	line := weightedHTTPLine(t, s, id, owner.ID)
	page := testRequest(t, app, http.MethodGet, fmt.Sprintf("/manager/orders/%d", id), owner, nil, nil)
	measurement := adminForm(page.Body.String(), fmt.Sprintf("/manager/orders/%d/lines/%d/weight/preview", id, line.LineID))
	for _, input := range adminInputRE.FindAllStringSubmatch(measurement, -1) {
		if adminAttr(input[1], "name") == "reason" && strings.Contains(input[1], "required") {
			t.Fatal("routine measurement note is mandatory")
		}
	}
	base := fmt.Sprintf("/manager/orders/%d/lines/%d/weight", id, line.LineID)
	fields := weightHTTPFields(owner, o, line)
	fields.Set("reason", "")
	preview := testRequest(t, app, http.MethodPost, base+"/preview", owner, fields, nil)
	confirm := weightHTTPForm(t, preview.Body.String(), base+"/confirm")
	if confirm.Get("reason") != "Manager confirmed scale reading" {
		t.Fatal("missing default audit reason")
	}
	if w := testRequest(t, app, http.MethodPost, base+"/confirm", owner, confirm, nil); w.Code != http.StatusSeeOther {
		t.Fatal("blank routine note blocked", w.Code)
	}
	page = testRequest(t, app, http.MethodGet, fmt.Sprintf("/manager/orders/%d/products?context=add&product_q=milk", id), owner, nil, nil)
	// Each picker form is server-validated so an invalid unselected draft cannot
	// block another radio choice through native browser constraint validation.
	if !strings.Contains(page.Body.String(), "data-picker-form novalidate") {
		t.Fatal("picker can trap unselected invalid quantities")
	}
	c := overrideCommand(t, s, id, owner.ID, "set", 3, 1)
	fields = overrideFields(owner, c)
	fields.Set("form_id", "add")
	fields.Set("quantity_3", "1")
	fields.Set("quantity_2", "invalid unselected draft")
	assertRedirect(t, testRequest(t, app, http.MethodPost, fmt.Sprintf("/manager/orders/%d/override", id), owner, fields, nil), fmt.Sprintf("/manager/orders/%d", id), false)
}
func TestHTTPWeightPreviewConfirmReplayAndPrivacy(t *testing.T) {
	for _, hx := range []bool{false, true} {
		t.Run(fmt.Sprint(hx), func(t *testing.T) {
			s := newTestStore(t)
			a := pickingApp(t, s, true)
			owner, id, p := weightedHTTPFixture(t, s)
			owner = pickingLogin(t, a, owner)
			other := pickingLogin(t, a, testSession(t, s, ""))
			o := testOrder(t, s, id, owner.ID)
			receipt := placedSnapshot(t, s, id)
			line := weightedHTTPLine(t, s, id, owner.ID)
			base := fmt.Sprintf("/manager/orders/%d/lines/%d/weight", id, line.LineID)
			fields := weightHTTPFields(owner, o, line)
			for _, route := range []string{"/preview", "/confirm"} {
				bad := weightHTTPFields(other, o, line)
				bad.Set("actual", "invalid")
				w := testRequest(t, a, http.MethodPost, base+route, other, bad, pickingHeaders(hx))
				if w.Code != 404 || strings.Contains(w.Body.String(), p.Name) {
					t.Fatal("foreign measurement exposed line", w.Code)
				}
				bad = weightHTTPFields(owner, o, line)
				bad.Set("csrf", "bad")
				if w := testRequest(t, a, http.MethodPost, base+route, owner, bad, nil); w.Code != 403 {
					t.Fatal("measurement CSRF accepted")
				}
			}
			page := testRequest(t, a, http.MethodGet, fmt.Sprintf("/manager/orders/%d", id), owner, nil, nil)
			for _, want := range []string{"0 <span>/ 2 product lines picked", "Actual weight needed", "accepted allocation 500 g", "Mark picked", "Enter actual weight"} {
				if !strings.Contains(page.Body.String(), want) {
					t.Fatalf("missing weighted ticket %q", want)
				}
			}
			for n := 0; n < 2; n++ {
				w := testRequest(t, a, http.MethodPost, base+"/preview", owner, fields, pickingHeaders(hx))
				if w.Code != 200 {
					t.Fatal("preview failed", w.Code, w.Body.String())
				}
				for _, want := range []string{"Not saved yet", "500 g", "527 g", "$1.75", "$1.84", "+$0.09", "Reserve 27 g more"} {
					if !strings.Contains(adminVisibleText(w.Body.String()), want) {
						t.Fatalf("preview missing %q", want)
					}
				}
				if !reflect.DeepEqual(o, testOrder(t, s, id, owner.ID)) || !reflect.DeepEqual(p, testProduct(t, s, p.ID)) {
					t.Fatal("preview mutated order or stock")
				}
				if n == 1 {
					fields = weightHTTPForm(t, w.Body.String(), base+"/confirm")
				}
			}
			tampered := weightHTTPForm(t, testRequest(t, a, http.MethodPost, base+"/preview", owner, fields, pickingHeaders(hx)).Body.String(), base+"/confirm")
			tampered.Set("actual", "528")
			w := testRequest(t, a, http.MethodPost, base+"/confirm", owner, tampered, pickingHeaders(hx))
			if w.Code != 200 || !strings.Contains(w.Body.String(), ErrWeightReview.Error()) || !strings.Contains(w.Body.String(), "Submitted actual: 528 g") || adminForm(w.Body.String(), base+"/confirm") != "" {
				t.Fatal("tampered review not rejected with fresh draft", w.Code)
			}
			for n := 0; n < 2; n++ {
				w = testRequest(t, a, http.MethodPost, base+"/confirm", owner, fields, pickingHeaders(hx))
				if hx {
					if w.Code != 200 || !strings.Contains(w.Body.String(), "Actual weight confirmed") {
						t.Fatal("HX confirm", w.Code)
					}
				} else {
					assertRedirect(t, w, fmt.Sprintf("/manager/orders/%d", id), false)
				}
			}
			after := weightedHTTPLine(t, s, id, owner.ID)
			if !after.Measured || after.Picked != 527 || after.Allocated != 527 || after.Quantity != 500 || after.Subtotal != 184 || testProduct(t, s, p.ID).Stock != p.Stock-27 || testCount(t, s, "order_events") != 1 {
				t.Fatal("bad measurement/replay", after)
			}
			fresh := testOrder(t, s, id, owner.ID)
			if !reflect.DeepEqual(receipt, placedSnapshot(t, s, id)) || o.Total != fresh.Total || fresh.RequiredCount != 2 || fresh.PickedCount != 1 {
				t.Fatalf("receipt/progress: before=%+v after=%+v required=%d picked=%d", o.Items, fresh.Items, fresh.RequiredCount, fresh.PickedCount)
			}
			confirmedKey := fields.Get("command_key")
			fields.Set("command_key", token())
			w = testRequest(t, a, http.MethodPost, base+"/confirm", owner, fields, pickingHeaders(hx))
			if w.Header().Get("X-Shop-Error") != "stale-version" || !strings.Contains(w.Body.String(), "Submitted actual: 527 g") || adminForm(w.Body.String(), base+"/confirm") != "" {
				t.Fatal("stale review did not require another preview")
			}
			remove := overrideCommand(t, s, id, owner.ID, "set", p.ID, 0)
			remove.Disposition = "restock"
			if err := s.OverrideOrder(id, owner.ID, false, remove); err != nil {
				t.Fatal(err)
			}
			fields.Set("command_key", confirmedKey)
			beforeReplay := testProduct(t, s, p.ID)
			w = testRequest(t, a, http.MethodPost, base+"/confirm", owner, fields, pickingHeaders(hx))
			if hx {
				if w.Code != 200 {
					t.Fatal("exact replay after removal failed", w.Code)
				}
			} else {
				assertRedirect(t, w, fmt.Sprintf("/manager/orders/%d", id), false)
			}
			if !reflect.DeepEqual(beforeReplay, testProduct(t, s, p.ID)) || testCount(t, s, "order_events") != 2 {
				t.Fatal("measurement replay after removal repeated stock or audit")
			}
		})
	}
}
func TestHTTPWeightReductionReviewAndZero(t *testing.T) {
	for _, disposition := range []string{"restock", "writeoff"} {
		t.Run(disposition, func(t *testing.T) {
			s := newTestStore(t)
			a := pickingApp(t, s, true)
			owner, id, p := weightedHTTPFixture(t, s)
			owner = pickingLogin(t, a, owner)
			o := testOrder(t, s, id, owner.ID)
			line := weightedHTTPLine(t, s, id, owner.ID)
			base := fmt.Sprintf("/manager/orders/%d/lines/%d/weight", id, line.LineID)
			fields := weightHTTPFields(owner, o, line)
			fields.Set("actual", "0")
			w := testRequest(t, a, http.MethodPost, base+"/preview", owner, fields, nil)
			if !strings.Contains(w.Body.String(), ErrPickedDisposition.Error()) || adminForm(w.Body.String(), base+"/confirm") != "" {
				t.Fatal("reduction accepted without disposition")
			}
			fields.Set("disposition", disposition)
			w = testRequest(t, a, http.MethodPost, base+"/preview", owner, fields, nil)
			want := "Release 500 g to available stock"
			if disposition == "writeoff" {
				want = "Write off 500 g"
			}
			if w.Code != 200 || !strings.Contains(adminVisibleText(w.Body.String()), want) || !strings.Contains(w.Body.String(), "−$1.75") {
				t.Fatal("reduction preview inaccurate", w.Code, w.Body.String())
			}
			fields = weightHTTPForm(t, w.Body.String(), base+"/confirm")
			assertRedirect(t, testRequest(t, a, http.MethodPost, base+"/confirm", owner, fields, nil), fmt.Sprintf("/manager/orders/%d", id), false)
			got := weightedHTTPLine(t, s, id, owner.ID)
			stock := p.Stock
			if disposition == "restock" {
				stock += 500
			}
			if !got.Measured || got.Picked != 0 || got.Allocated != 0 || got.Subtotal != 0 || testProduct(t, s, p.ID).Stock != stock {
				t.Fatal("zero allocation incorrect")
			}
		})
	}
}
func TestHTTPGramProductControlsAndPickerQuantities(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner, id, p := weightedHTTPFixture(t, s)
	owner = pickingLogin(t, a, owner)
	line := weightedHTTPLine(t, s, id, owner.ID)
	replacement := testCreateProduct(t, s, func(p *Product) {
		p.Name, p.SaleUnit, p.PriceBasis, p.QuantityStep = "Weighed replacement plums", "g", 1000, 250
	})
	if err := s.Adjust(replacement.ID, 5000, replacement.Version, "Fake replacement stock"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", fmt.Sprintf("/products/%d", p.ID)} {
		w := testRequest(t, a, http.MethodGet, path, owner, nil, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), "ordering coming later") || !strings.Contains(w.Body.String(), `max="100000" step="100"`) {
			t.Fatal("gram storefront bounds missing", path, w.Code)
		}
	}
	path := fmt.Sprintf("/manager/orders/%d/products?context=sub&line_id=%d&product_q=", id, line.LineID)
	w := testRequest(t, a, http.MethodGet, path, owner, nil, pickingHeaders(true))
	if w.Code != 200 || !strings.Contains(w.Body.String(), replacement.Name) || strings.Contains(w.Body.String(), "each</small>") || !strings.Contains(w.Body.String(), `name="quantity_`+fmt.Sprint(replacement.ID)+`" type="number" min="250" max="100000" step="250" value="500"`) {
		t.Fatal("same-unit picker incorrect", w.Code, w.Body.String())
	}
	c := overrideCommand(t, s, id, owner.ID, "substitute", p.ID, 500)
	c.ReplacementID = replacement.ID
	fields := overrideFields(owner, c)
	fields.Set("form_id", fmt.Sprintf("sub-%d", line.LineID))
	fields.Set("quantity", "999999")
	fields.Set(fmt.Sprintf("quantity_%d", replacement.ID), "750")
	fields.Set(fmt.Sprintf("quantity_%d", p.ID), "bad unselected value")
	assertRedirect(t, testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/orders/%d/override", id), owner, fields, nil), fmt.Sprintf("/manager/orders/%d", id), false)
	o := testOrder(t, s, id, owner.ID)
	found := false
	for _, l := range o.WorkingItems {
		if l.ProductID == replacement.ID {
			found = true
			if l.Quantity != 750 || l.Allocated != 750 || l.Measured {
				t.Fatal("selected quantity not applied")
			}
		}
	}
	if !found {
		t.Fatal("replacement missing")
	}
	// A new mixed-unit basket add reads only the quantity belonging to its chosen radio.
	b := testBasket(t, s, owner.ID)
	fields = url.Values{"csrf": {owner.CSRF}, "revision": {fmt.Sprint(b.Revision)}, "product_id": {fmt.Sprint(p.ID)}, fmt.Sprintf("quantity_%d", p.ID): {"600"}, "quantity_1": {"999999"}, "draft_action": {"add"}}
	assertRedirect(t, testRequest(t, a, http.MethodPost, "/manager/baskets/"+b.ID+"/items", owner, fields, nil), "/manager/baskets/"+b.ID, false)
	b = testBasket(t, s, owner.ID)
	if b.Count != 1 || !b.HasWeight || b.Lines[0].Quantity != 600 {
		t.Fatal("gram basket add incorrect", b)
	}
	fields.Set("product_id", "1")
	fields.Set("revision", fmt.Sprint(b.Revision))
	w = testRequest(t, a, http.MethodPost, "/manager/baskets/"+b.ID+"/items", owner, fields, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), ErrInvalid.Error()) || testBasket(t, s, owner.ID).Count != 1 {
		t.Fatal("tampered selection borrowed gram bounds")
	}
	w = testRequest(t, a, http.MethodGet, fmt.Sprintf("/manager/stock?product=%d", p.ID), owner, nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `min="-1000000" max="1000000"`) {
		t.Fatal("gram stock bounds missing")
	}
}

func TestHTTPProductPickerQuantityDraftSurvivesQuoteChange(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner, id, _ := weightedHTTPFixture(t, s)
	owner = pickingLogin(t, a, owner)
	candidate := testCreateProduct(t, s, func(p *Product) {
		p.Name, p.SaleUnit, p.PriceBasis, p.QuantityStep = "New gram candidate", "g", 1000, 100
	})
	if err := s.Adjust(candidate.ID, 2000, candidate.Version, "Fake candidate stock"); err != nil {
		t.Fatal(err)
	}
	c := overrideCommand(t, s, id, owner.ID, "set", candidate.ID, 800)
	fields := overrideFields(owner, c)
	fields.Set("form_id", "add")
	fields.Set("product_q", candidate.Name)
	fields.Set("quantity", "1")
	fields.Set(fmt.Sprintf("quantity_%d", candidate.ID), "800")
	candidate = testProduct(t, s, candidate.ID)
	candidate.Price += 100
	if _, err := s.SaveProduct(candidate); err != nil {
		t.Fatal(err)
	}
	w := testRequest(t, a, http.MethodPost, fmt.Sprintf("/manager/orders/%d/override", id), owner, fields, pickingHeaders(true))
	if w.Code != 200 || !strings.Contains(w.Body.String(), ErrOrderQuote.Error()) || !strings.Contains(w.Body.String(), `name="quantity_`+fmt.Sprint(candidate.ID)+`" type="number" min="100" max="100000" step="100" value="800"`) {
		t.Fatal("stale quote lost selected gram quantity", w.Code)
	}
	if testProduct(t, s, candidate.ID).Stock != 2000 || testCount(t, s, "order_events") != 0 {
		t.Fatal("stale selected quote changed state")
	}
}
