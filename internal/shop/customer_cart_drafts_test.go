package shop

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func customerCartValues(session Session, basket Basket, note string) url.Values {
	values := reservationCheckoutValues(session, basket, note)
	values.Set("return", "cart")
	for _, line := range basket.Lines {
		values.Set(fmt.Sprintf("quantity-%d", line.Product.ID), fmt.Sprint(line.Quantity))
	}
	return values
}

func customerNote(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`(?s)<textarea\b[^>]*id="instructions"[^>]*>(.*?)</textarea>`).FindStringSubmatch(body)
	if match == nil {
		t.Fatal("missing customer instructions textarea")
	}
	return html.UnescapeString(match[1])
}

func TestCustomerBasketNativeFormCarriesAllDrafts(t *testing.T) {
	s, _, _ := newReservationStore(t)
	a := reservationHTTPApp(t, s, false)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	testCart(t, s, owner.ID, 2, 2)
	body := testRequest(t, a, http.MethodGet, "/cart", owner, nil, nil).Body.String()
	forms := adminFormRE.FindAllStringSubmatch(body, -1)
	if len(forms) != 1 || adminAttr(forms[0][1], "id") != "customer-basket" {
		t.Fatalf("cart drafts must belong to one native form, got %d forms", len(forms))
	}
	form := forms[0][0]
	for _, field := range []string{`name="quantity-1"`, `name="quantity-2"`, `name="instructions"`, `name="checkout_key"`, `name="revision"`, `name="quote"`, `hx-sync="this:drop"`, `formaction="/cart/renew" formnovalidate`, `formaction="/cart" name="product_id" value="1" formnovalidate`} {
		if !strings.Contains(form, field) {
			t.Errorf("native basket form missing %s", field)
		}
	}
	if strings.Contains(body, owner.ID) {
		t.Fatal("form exposed session credential")
	}
}

func TestCustomerBasketDraftQuantityThenRenewHTMLAndHTMX(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, clock, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, false)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 1)
			testCart(t, s, owner.ID, 2, 2)
			basket := testBasket(t, s, owner.ID)
			owner = testSession(t, s, owner.ID)
			note := "Fake demo note <script>kept as text</script>\nBread on top & dry"
			values := customerCartValues(owner, basket, note)
			values.Set("product_id", "1")
			values.Set("quantity-1", "3")
			values.Set("quantity-2", "4")
			response := testRequest(t, a, http.MethodPost, "/cart", owner, values, managerDraftHeaders(htmx))
			if response.Code != http.StatusOK || response.Header().Get("Location") != "" || response.Header().Get("HX-Redirect") != "" {
				t.Fatalf("quantity redirected away drafts: %d %v", response.Code, response.Header())
			}
			body := response.Body.String()
			if got := customerNote(t, body); got != note {
				t.Errorf("quantity update lost note: %q", got)
			}
			if strings.Contains(body, "<script>kept") || !strings.Contains(body, html.EscapeString(note)) {
				t.Error("note was not rendered as escaped plaintext")
			}
			if got := managerDraftInput(t, body, "quantity-2"); got != "4" {
				t.Errorf("unsubmitted quantity lost: %q", got)
			}
			basket = testBasket(t, s, owner.ID)
			if basket.Lines[0].Quantity != 3 || basket.Lines[1].Quantity != 2 {
				t.Fatalf("quantity action saved wrong fields: %+v", basket.Lines)
			}
			clock.at(basket.HoldUntil)
			basket = testBasket(t, s, owner.ID)
			values = customerCartValues(owner, basket, note)
			values.Set("quantity-2", "4")
			before := reservationSnapshot(t, s)
			failed := testRequest(t, a, http.MethodPost, "/checkout", owner, values, managerDraftHeaders(htmx))
			if failed.Code != http.StatusOK || !strings.Contains(failed.Body.String(), errCartQuantityDraft.Error()) || customerNote(t, failed.Body.String()) != note {
				t.Fatalf("checkout silently ignored unsaved quantity or lost draft: %d", failed.Code)
			}
			assertReservationUnchanged(t, s, before)
			values.Set("quantity-2", "2")
			failed = testRequest(t, a, http.MethodPost, "/checkout", owner, values, managerDraftHeaders(htmx))
			if !strings.Contains(failed.Body.String(), ErrHold.Error()) || customerNote(t, failed.Body.String()) != note {
				t.Fatal("expired checkout did not retain review note")
			}
			values.Set("quantity-2", "4")
			renewed := testRequest(t, a, http.MethodPost, "/cart/renew", owner, values, managerDraftHeaders(htmx))
			if renewed.Code != http.StatusOK || renewed.Header().Get("HX-Redirect") != "" || renewed.Header().Get("Location") != "" || customerNote(t, renewed.Body.String()) != note {
				t.Fatal("renewal discarded notes instead of rendering explicit review")
			}
			if got := managerDraftInput(t, renewed.Body.String(), "quantity-2"); got != "4" {
				t.Errorf("renewal lost unsaved quantity: %q", got)
			}
			basket = testBasket(t, s, owner.ID)
			if !basket.CanCheckout || basket.Lines[1].Quantity != 2 || testCount(t, s, "orders") != 0 {
				t.Fatal("renewal applied an unsaved quantity or checked out automatically")
			}
			// A stale/rapid repeat must retain drafts while rejecting the write.
			before = reservationSnapshot(t, s)
			stale := testRequest(t, a, http.MethodPost, "/cart/renew", owner, values, managerDraftHeaders(htmx))
			if !strings.Contains(stale.Body.String(), ErrConflict.Error()) || customerNote(t, stale.Body.String()) != note {
				t.Fatal("stale renewal lost instructions or conflict")
			}
			assertReservationUnchanged(t, s, before)
		})
	}
}

func TestCustomerBasketCheckoutClearsDraftAndPreservesReplay(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprint(htmx), func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			a := reservationHTTPApp(t, s, false)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 1)
			basket := testBasket(t, s, owner.ID)
			owner = testSession(t, s, owner.ID)
			values := customerCartValues(owner, basket, "Placed fake note")
			placed := testRequest(t, a, http.MethodPost, "/checkout", owner, values, managerDraftHeaders(htmx))
			orders, err := s.Orders(owner.ID, false)
			if err != nil || len(orders) != 1 {
				t.Fatalf("checkout failed: %v / %d", err, placed.Code)
			}
			path := fmt.Sprintf("/orders/%d", orders[0].ID)
			assertRedirect(t, placed, path, htmx)
			assertRedirect(t, testRequest(t, a, http.MethodPost, "/checkout", owner, values, managerDraftHeaders(htmx)), path, htmx)
			testCart(t, s, owner.ID, 1, 2)
			page := testRequest(t, a, http.MethodGet, "/cart", owner, nil, nil).Body.String()
			if customerNote(t, page) != "" || strings.Contains(page, `data-customer-basket="`+owner.CheckoutKey+`"`) {
				t.Fatal("placed note or generation leaked into next basket")
			}
			// An in-flight old request cannot repopulate the new generation.
			values.Set("product_id", "1")
			values.Set("quantity-1", "9")
			stale := testRequest(t, a, http.MethodPost, "/cart", owner, values, managerDraftHeaders(htmx))
			if customerNote(t, stale.Body.String()) != "" || managerDraftInput(t, stale.Body.String(), "quantity-1") != "2" {
				t.Fatal("old-generation drafts contaminated new basket")
			}
			other := testSession(t, s, "")
			testCart(t, s, other.ID, 1, 1)
			if customerNote(t, testRequest(t, a, http.MethodGet, "/cart", other, nil, nil).Body.String()) != "" {
				t.Fatal("note leaked to another session")
			}
			if got := testOrder(t, s, orders[0].ID, owner.ID).Instructions; got != "Placed fake note" {
				t.Fatalf("placed instruction snapshot changed: %q", got)
			}
		})
	}
}

func TestCustomerBasketDraftValidationIsBoundedAndNonpersistent(t *testing.T) {
	s, _, _ := newReservationStore(t)
	a := reservationHTTPApp(t, s, false)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	basket := testBasket(t, s, owner.ID)
	owner = testSession(t, s, owner.ID)
	values := customerCartValues(owner, basket, strings.Repeat("x", 501)+"\x00")
	values.Set("product_id", "1")
	values.Set("quantity-1", "bad")
	before := reservationSnapshot(t, s)
	response := testRequest(t, a, http.MethodPost, "/cart", owner, values, nil)
	if response.Code != http.StatusOK || utf8.RuneCountInString(customerNote(t, response.Body.String())) != 500 || !strings.Contains(response.Body.String(), "shortened or replaced") {
		t.Fatal("invalid draft was not safely bounded for review")
	}
	assertReservationUnchanged(t, s, before)
	if customerNote(t, testRequest(t, a, http.MethodGet, "/cart", owner, nil, nil).Body.String()) != "" {
		t.Fatal("draft persisted beyond its submitted response")
	}
	// POST authorization remains mandatory; no recovery based on raw session IDs.
	values.Set("csrf", "wrong")
	if denied := testRequest(t, a, http.MethodPost, "/cart", owner, values, managerDraftHeaders(true)); denied.Code != http.StatusForbidden {
		t.Fatal("draft path bypassed CSRF")
	}
	assertReservationUnchanged(t, s, before)
	testExec(t, s, "UPDATE sessions SET expires=0 WHERE id=?", owner.ID)
	values.Set("csrf", owner.CSRF)
	if denied := testRequest(t, a, http.MethodPost, "/cart/renew", owner, values, managerDraftHeaders(true)); denied.Code != http.StatusForbidden || denied.Header().Get("X-Shop-CSRF") != "" {
		t.Fatal("reset/unknown session acquired draft or replacement credentials")
	}
}

func TestCustomerBasketRejectsOldGenerationEvenWithCurrentRevision(t *testing.T) {
	s, _, _ := newReservationStore(t)
	a := reservationHTTPApp(t, s, false)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 2)
	basket := testBasket(t, s, owner.ID)
	values := customerCartValues(owner, basket, "Foreign old note")
	values.Set("checkout_key", testSession(t, s, "").CheckoutKey)
	values.Set("product_id", "1")
	values.Set("quantity-1", "5")
	before := reservationSnapshot(t, s)
	for _, path := range []string{"/cart", "/cart/renew"} {
		response := testRequest(t, a, http.MethodPost, path, owner, values, managerDraftHeaders(true))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), ErrConflict.Error()) || customerNote(t, response.Body.String()) != "" {
			t.Errorf("%s accepted an old basket generation", path)
		}
		if managerDraftInput(t, response.Body.String(), "quantity-1") != "2" {
			t.Errorf("%s restored another generation's quantity", path)
		}
		assertReservationUnchanged(t, s, before)
	}
}

func TestCustomerBasketSharedFormPayloadBudget(t *testing.T) {
	for _, extra := range []int{0, 100} {
		t.Run(fmt.Sprintf("extra_products_%d", extra), func(t *testing.T) {
			s := newTestStore(t)
			a := testApp(t, s, testManagerPassword)
			owner := testSession(t, s, "")
			category := testProduct(t, s, 1).CategoryID
			for i := 0; i < extra; i++ {
				id, err := s.SaveProduct(Product{Name: fmt.Sprintf("Large basket demo %d", i), CategoryID: category, Icon: "apple", Price: 199, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1})
				if err != nil {
					t.Fatal(err)
				}
				testExec(t, s, "UPDATE products SET stock=20 WHERE id=?", id)
			}
			// Restock seeded sold-out examples so the full catalog fits in the fixture basket.
			testExec(t, s, "UPDATE products SET stock=20 WHERE stock=0")
			products, err := s.Products("", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range products {
				testCart(t, s, owner.ID, p.ID, 1)
			}
			basket := testBasket(t, s, owner.ID)
			owner = testSession(t, s, owner.ID)
			note := strings.Repeat("\U0001F34E", 500)
			values := customerCartValues(owner, basket, note)
			values.Set("product_id", "1")
			payloadBytes := len(values.Encode())
			t.Logf("%d basket lines and 500 four-byte instruction characters encode to %d bytes", len(basket.Lines), payloadBytes)
			if payloadBytes > customerCartFormLimit || (extra > 0 && payloadBytes <= 8192) {
				t.Fatalf("fixture does not exercise the supported shared-form budget: %d bytes", payloadBytes)
			}
			for _, path := range []string{"/cart", "/cart/renew"} {
				response := testRequest(t, a, http.MethodPost, path, owner, values, nil)
				if response.Code != http.StatusOK || customerNote(t, response.Body.String()) != note {
					t.Fatalf("%s rejected or damaged a %d-byte valid basket form: %d", path, payloadBytes, response.Code)
				}
				basket = testBasket(t, s, owner.ID)
				values = customerCartValues(owner, basket, note)
				values.Set("product_id", "1")
			}
			response := testRequest(t, a, http.MethodPost, "/checkout", owner, values, nil)
			if response.Code != http.StatusSeeOther {
				t.Fatalf("checkout rejected the shared form: %d %s", response.Code, response.Body.String())
			}
			orders, err := s.Orders(owner.ID, false)
			if err != nil || len(orders) != 1 || testOrder(t, s, orders[0].ID, owner.ID).Instructions != note {
				t.Fatal("large basket checkout did not retain its exact instruction snapshot")
			}
		})
	}
}

func TestCustomerBasketRejectsOversizedSharedFormBeforeMutation(t *testing.T) {
	s := newTestStore(t)
	a := testApp(t, s, testManagerPassword)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	values := customerCartValues(owner, testBasket(t, s, owner.ID), strings.Repeat("x", customerCartFormLimit))
	values.Set("product_id", "1")
	values.Set("quantity-1", "2")
	before := reservationSnapshot(t, s)
	for _, path := range []string{"/cart", "/cart/renew", "/checkout"} {
		response := testRequest(t, a, http.MethodPost, path, owner, values, managerDraftHeaders(true))
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s accepted an oversized shared form: %d", path, response.Code)
		}
		assertReservationUnchanged(t, s, before)
	}
}
