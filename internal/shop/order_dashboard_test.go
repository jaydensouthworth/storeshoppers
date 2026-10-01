package shop

import (
	"net/http"
	"strings"
	"testing"
)

func TestManagerDashboardShowsPickingSeparateFromReady(t *testing.T) {
	s := newTestStore(t)
	a := reservationHTTPApp(t, s, true)
	manager := testManager(t, s)
	foreign := testSession(t, s, "")
	var refs []string
	for _, status := range []string{"Placed", "Picking", "Ready", "Completed"} {
		testCart(t, s, manager.ID, 1, 2)
		id := reservationCheckout(t, s, manager.ID, "")
		if status != "Placed" {
			if err := s.AdvanceScoped(id, "Placed", manager.ID, false); err != nil {
				t.Fatal(err)
			}
			qty := int64(2)
			if status == "Picking" {
				qty = 1
			}
			if err := s.RecordPicked(id, 1, qty, 1, manager.ID, false); err != nil {
				t.Fatal(err)
			}
		}
		if status == "Ready" || status == "Completed" {
			if err := s.AdvanceScoped(id, "Picking", manager.ID, false); err != nil {
				t.Fatal(err)
			}
		}
		if status == "Completed" {
			if err := s.AdvanceScoped(id, "Ready", manager.ID, false); err != nil {
				t.Fatal(err)
			}
		}
		order, err := s.Order(id, manager.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, order.Reference)
	}
	testCart(t, s, foreign.ID, 2, 1)
	foreignID := reservationCheckout(t, s, foreign.ID, "")
	foreignOrder, err := s.Order(foreignID, foreign.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	// An unsubmitted shopping basket must not become a confirmed order row.
	testCart(t, s, manager.ID, 2, 1)
	for _, path := range []string{"/manager", "/manager/orders"} {
		for _, hx := range []bool{false, true} {
			headers := map[string]string{}
			if hx {
				headers["HX-Request"] = "true"
			}
			page := testRequest(t, a, http.MethodGet, path, manager, nil, headers)
			body := page.Body.String()
			if page.Code != http.StatusOK {
				t.Fatalf("%s status %d", path, page.Code)
			}
			for _, ref := range refs {
				if !strings.Contains(body, ref) {
					t.Errorf("%s lost an order in its default view: %s", path, ref)
				}
			}
			for _, state := range []string{`status-Placed`, `status-Picking`, `status-Ready`, `status-Completed`, `50% shopped`, `1 / 2 picked`, `100% shopped`} {
				if !strings.Contains(body, state) {
					t.Errorf("%s missing visible status/progress %q", path, state)
				}
			}
			if strings.Contains(body, foreignOrder.Reference) {
				t.Error("demo dashboard exposed a foreign order")
			}
			if got := strings.Count(body, `class="order-row"`); got != 4 {
				t.Errorf("confirmed order rows = %d, want4", got)
			}
		}
	}
}
