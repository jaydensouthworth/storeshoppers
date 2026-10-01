package shop

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return openTestStore(t, filepath.Join(t.TempDir(), "shop.db"))
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func testSession(t *testing.T, s *Store, id string) Session {
	t.Helper()
	v, err := s.Session(id)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	return v
}

func testExec(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(query, args...); err != nil {
		t.Fatalf("fixture SQL: %v", err)
	}
}

func testProduct(t *testing.T, s *Store, id int64) Product {
	t.Helper()
	products, err := s.Products("", "")
	if err != nil {
		t.Fatalf("Products: %v", err)
	}
	for _, p := range products {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("product %d absent", id)
	return Product{}
}

func testBasket(t *testing.T, s *Store, sid string) Basket {
	t.Helper()
	b, err := s.Basket(sid)
	if err != nil {
		t.Fatalf("Basket: %v", err)
	}
	return b
}

func testCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	// Table is a test-owned constant, never request data.
	if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func testCart(t *testing.T, s *Store, sid string, pid, quantity int64) {
	t.Helper()
	if err := s.SetCart(sid, pid, quantity, false); err != nil {
		t.Fatalf("SetCart: %v", err)
	}
}

func testCheckout(t *testing.T, s *Store, sid string) int64 {
	t.Helper()
	session := testSession(t, s, sid)
	id, err := s.Checkout(sid, session.CheckoutKey, session.Revision)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	return id
}

func testOrder(t *testing.T, s *Store, id int64, sid string) Order {
	t.Helper()
	o, err := s.Order(id, sid, false)
	if err != nil {
		t.Fatalf("Order: %v", err)
	}
	return o
}

func TestCheckoutAllOrNothing(t *testing.T) {
	for _, failure := range []string{"stock changed", "second item insert failed"} {
		t.Run(failure, func(t *testing.T) {
			s := newTestStore(t)
			session := testSession(t, s, "")
			testCart(t, s, session.ID, 1, 2)
			testCart(t, s, session.ID, 2, 3)
			if failure == "stock changed" {
				testExec(t, s, "UPDATE products SET stock=2 WHERE id=2")
			} else {
				// Fail after product 1 has already been deducted and snapshotted.
				testExec(t, s, `CREATE TRIGGER reject_second_order_item BEFORE INSERT ON order_items WHEN NEW.product_id=2 BEGIN SELECT RAISE(ABORT, 'injected item insert failure'); END`)
			}
			beforeSession := testSession(t, s, session.ID)
			beforeBasket := testBasket(t, s, session.ID)
			beforeProducts, err := s.Products("", "")
			if err != nil {
				t.Fatal(err)
			}
			id, err := s.Checkout(session.ID, beforeSession.CheckoutKey, beforeSession.Revision)
			if err == nil || id != 0 {
				t.Fatalf("failed checkout = (%d, %v), want (0, error)", id, err)
			}
			if failure == "stock changed" && !errors.Is(err, ErrStock) {
				t.Fatalf("checkout error = %v, want ErrStock", err)
			}
			afterProducts, err := s.Products("", "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeProducts, afterProducts) {
				t.Error("failed checkout changed stock or product versions")
			}
			if after := testSession(t, s, session.ID); after != beforeSession {
				t.Errorf("failed checkout changed session: got %+v, want %+v", after, beforeSession)
			}
			if after := testBasket(t, s, session.ID); !reflect.DeepEqual(after, beforeBasket) {
				t.Errorf("failed checkout changed basket: got %+v, want %+v", after, beforeBasket)
			}
			if n := testCount(t, s, "orders"); n != 0 {
				t.Errorf("failed checkout left %d orders", n)
			}
			if n := testCount(t, s, "order_items"); n != 0 {
				t.Errorf("failed checkout left %d order items", n)
			}
		})
	}
}

func TestConcurrentLastUnitNeverOversells(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	first := openTestStore(t, path)
	second := openTestStore(t, path)
	stores := []*Store{first, second}
	testExec(t, first, "UPDATE products SET stock=1 WHERE id=1")
	const shoppers = 16
	sessions := make([]Session, shoppers)
	for i := range sessions {
		s := stores[i%len(stores)]
		session := testSession(t, s, "")
		testCart(t, s, session.ID, 1, 1)
		sessions[i] = testSession(t, s, session.ID)
	}
	type outcome struct {
		index int
		id    int64
		err   error
	}
	start := make(chan struct{})
	results := make(chan outcome, shoppers)
	var wg sync.WaitGroup
	for i, session := range sessions {
		wg.Add(1)
		go func(i int, session Session) {
			defer wg.Done()
			<-start
			id, err := stores[i%len(stores)].Checkout(session.ID, session.CheckoutKey, session.Revision)
			results <- outcome{i, id, err}
		}(i, session)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for result := range results {
		if result.err == nil {
			winners++
			if result.id < 1 {
				t.Error("successful checkout returned no order ID")
			}
			if b := testBasket(t, first, sessions[result.index].ID); b.Count != 0 {
				t.Error("winner's basket was not cleared")
			}
		} else {
			if !errors.Is(result.err, ErrStock) {
				t.Errorf("loser error = %v, want ErrStock", result.err)
			}
			if b := testBasket(t, first, sessions[result.index].ID); b.Count != 1 {
				t.Error("loser's basket was modified")
			}
		}
	}
	if winners != 1 {
		t.Errorf("%d successful last-unit orders, want 1", winners)
	}
	if p := testProduct(t, first, 1); p.Stock != 0 || p.Version != 2 {
		t.Errorf("last-unit product = %+v, want stock 0 and version 2", p)
	}
	if n := testCount(t, first, "orders"); n != 1 {
		t.Errorf("%d committed orders, want 1", n)
	}
}

func TestCheckoutIdempotentReplay(t *testing.T) {
	s := newTestStore(t)
	session := testSession(t, s, "")
	testCart(t, s, session.ID, 1, 2)
	session = testSession(t, s, session.ID)
	const retries = 12
	ids := make(chan int64, retries)
	errs := make(chan error, retries)
	var wg sync.WaitGroup
	for range retries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.Checkout(session.ID, session.CheckoutKey, session.Revision)
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	var first int64
	for id := range ids {
		if first == 0 {
			first = id
		}
		if id < 1 || id != first {
			t.Errorf("idempotent replay ID %d differs from first ID %d", id, first)
		}
	}
	for err := range errs {
		if err != nil {
			t.Errorf("idempotent replay: %v", err)
		}
	}
	if n := testCount(t, s, "orders"); n != 1 {
		t.Errorf("%d committed orders, want 1", n)
	}
	if p := testProduct(t, s, 1); p.Stock != 22 || p.Version != 2 {
		t.Errorf("stock/version changed more than once: %+v", p)
	}
	after := testSession(t, s, session.ID)
	if after.Revision != session.Revision+1 || after.CheckoutKey == session.CheckoutKey {
		t.Errorf("checkout did not advance revision and rotate key: before %+v, after %+v", session, after)
	}
	// An old retry must not consume a new basket built after the first order.
	testCart(t, s, session.ID, 2, 1)
	newBasket := testBasket(t, s, session.ID)
	id, err := s.Checkout(session.ID, session.CheckoutKey, session.Revision)
	if err != nil || id != first {
		t.Fatalf("old replay = (%d, %v), want (%d, nil)", id, err, first)
	}
	if got := testBasket(t, s, session.ID); !reflect.DeepEqual(got, newBasket) {
		t.Error("old replay modified the new basket")
	}
}

func TestCheckoutRejectsStaleRevisionAndWrongKey(t *testing.T) {
	s := newTestStore(t)
	session := testSession(t, s, "")
	testCart(t, s, session.ID, 1, 1)
	current := testSession(t, s, session.ID)
	for _, tc := range []struct {
		name string
		key  string
		rev  int64
	}{{"stale revision", current.CheckoutKey, session.Revision}, {"wrong key", "wrong-key", current.Revision}} {
		t.Run(tc.name, func(t *testing.T) {
			if id, err := s.Checkout(session.ID, tc.key, tc.rev); id != 0 || !errors.Is(err, ErrConflict) {
				t.Fatalf("Checkout = (%d, %v), want (0, ErrConflict)", id, err)
			}
		})
	}
	if p := testProduct(t, s, 1); p.Stock != 24 {
		t.Error("rejected checkout deducted stock")
	}
	if n := testCount(t, s, "orders"); n != 0 {
		t.Error("rejected checkout created an order")
	}
	testCheckout(t, s, session.ID)
}

func TestCheckoutSnapshotsPriceAndName(t *testing.T) {
	s := newTestStore(t)
	session := testSession(t, s, "")
	testCart(t, s, session.ID, 1, 2)
	id := testCheckout(t, s, session.ID)
	before := testOrder(t, s, id, session.ID)
	if before.Total != 698 || len(before.Items) != 1 || before.Items[0].Price != 349 || before.Items[0].Quantity != 2 || before.Items[0].Subtotal != 698 {
		t.Fatalf("unexpected checkout snapshot: %+v", before)
	}
	testExec(t, s, "UPDATE products SET price=999,name='Renamed apples' WHERE id=1")
	if after := testOrder(t, s, id, session.ID); !reflect.DeepEqual(before, after) {
		t.Errorf("historical order changed with catalog: before %+v, after %+v", before, after)
	}
}

func TestInventoryOptimisticLockAndAudit(t *testing.T) {
	s := newTestStore(t)
	before := testProduct(t, s, 1)
	if err := s.Adjust(1, 3, before.Version, "  Restock delivery  "); err != nil {
		t.Fatal(err)
	}
	if err := s.Adjust(1, -2, before.Version, "Stale stock count"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale adjustment = %v, want ErrConflict", err)
	}
	p := testProduct(t, s, 1)
	if p.Stock != before.Stock+3 || p.Version != before.Version+1 {
		t.Errorf("unexpected stock/version: %+v", p)
	}
	logs, err := s.Adjustments()
	if err != nil || len(logs) != 1 || logs[0].Delta != 3 || logs[0].Reason != "Restock delivery" {
		t.Errorf("audit = %+v, %v; want only the committed, trimmed adjustment", logs, err)
	}
	// Checkout must also invalidate an already-open manager inventory form.
	session := testSession(t, s, "")
	testCart(t, s, session.ID, 1, 1)
	testCheckout(t, s, session.ID)
	if err := s.Adjust(1, 1, p.Version, "Stale after order"); !errors.Is(err, ErrConflict) {
		t.Errorf("post-checkout stale adjustment = %v, want ErrConflict", err)
	}
	if n := testCount(t, s, "adjustments"); n != 1 {
		t.Errorf("rejected adjustment created an audit entry: count %d", n)
	}
}

func TestInventoryValidationDoesNotMutate(t *testing.T) {
	cases := []struct {
		name   string
		delta  int64
		reason string
		want   error
	}{
		{"zero", 0, "Restock", ErrInvalid}, {"too large", 10001, "Restock", ErrInvalid},
		{"too negative", -10001, "Correction", ErrInvalid}, {"short reason", 1, " x ", ErrInvalid},
		{"stock underflow", -25, "Correction", ErrConflict}, {"stock overflow", 10000, "Restock", ErrConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			before := testProduct(t, s, 1)
			if err := s.Adjust(1, tc.delta, before.Version, tc.reason); !errors.Is(err, tc.want) {
				t.Fatalf("Adjust = %v, want %v", err, tc.want)
			}
			if after := testProduct(t, s, 1); after != before {
				t.Error("invalid inventory adjustment changed the product")
			}
			if n := testCount(t, s, "adjustments"); n != 0 {
				t.Error("invalid inventory adjustment created audit data")
			}
		})
	}
}

func TestOrdersAreSessionOwned(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	other := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 1)
	id := testCheckout(t, s, owner.ID)
	if _, err := s.Order(id, other.ID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("other shopper read order: %v", err)
	}
	if orders, err := s.Orders(other.ID, false); err != nil || len(orders) != 0 {
		t.Errorf("other shopper listed orders: %+v, %v", orders, err)
	}
	if orders, err := s.Orders(owner.ID, false); err != nil || len(orders) != 1 || orders[0].ID != id {
		t.Errorf("owner order list: %+v, %v", orders, err)
	}
	if _, err := s.Order(id, other.ID, true); err != nil {
		t.Errorf("authorized manager could not read order: %v", err)
	}
}

func TestOrderStatusTransitions(t *testing.T) {
	s := newTestStore(t)
	session := testSession(t, s, "")
	testCart(t, s, session.ID, 1, 1)
	id := testCheckout(t, s, session.ID)
	for _, tc := range []struct{ from, to string }{{"Placed", "Picking"}, {"Picking", "Ready"}, {"Ready", "Completed"}} {
		if got := testOrder(t, s, id, session.ID).Status; got != tc.from {
			t.Fatalf("current status %q, want %q", got, tc.from)
		}

		if tc.from == "Picking" {
			if err := s.Advance(id, "Picking"); !errors.Is(err, ErrIncomplete) {
				t.Fatalf("incomplete readiness = %v", err)
			}
			if err := s.RecordPicked(id, 1, 1, 1, session.ID, false); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Advance(id, tc.from); err != nil {
			t.Fatalf("advance %s: %v", tc.from, err)
		}
		if err := s.Advance(id, tc.from); !errors.Is(err, ErrConflict) {
			t.Errorf("repeated %s transition = %v, want ErrConflict", tc.from, err)
		}
		if got := testOrder(t, s, id, session.ID).Status; got != tc.to {
			t.Errorf("status %q, want %q", got, tc.to)
		}
	}
	for _, from := range []string{"Completed", "Cancelled", "", "placed"} {
		if err := s.Advance(id, from); !errors.Is(err, ErrInvalid) {
			t.Errorf("advance from %q = %v, want ErrInvalid", from, err)
		}
	}
	if err := s.Advance(id+100, "Placed"); !errors.Is(err, ErrNotFound) {
		t.Errorf("advance unknown order = %v, want ErrNotFound", err)
	}
	if got := testProduct(t, s, 1).Stock; got != 23 {
		t.Errorf("status transitions changed stock: %d", got)
	}
}

func TestCartValidationAndSessionIsolation(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	other := testSession(t, s, "")
	for _, tc := range []struct {
		pid, qty int64
		want     error
	}{{1, -1, ErrInvalid}, {1, 100, ErrInvalid}, {0, 1, ErrInvalid}, {999, 1, ErrNotFound}, {8, 1, ErrStock}, {1, 25, ErrStock}} {
		t.Run(fmt.Sprintf("product_%d_quantity_%d", tc.pid, tc.qty), func(t *testing.T) {
			if err := s.SetCart(owner.ID, tc.pid, tc.qty, false); !errors.Is(err, tc.want) {
				t.Errorf("SetCart = %v, want %v", err, tc.want)
			}
		})
	}
	if current := testSession(t, s, owner.ID); current.Revision != owner.Revision {
		t.Error("invalid cart update advanced revision")
	}
	testCart(t, s, owner.ID, 1, 2)
	if err := s.SetCart(owner.ID, 1, 1, true); err != nil {
		t.Fatal(err)
	}
	if b := testBasket(t, s, owner.ID); b.Count != 3 || b.Total != 1047 || !b.CanCheckout {
		t.Errorf("unexpected basket %+v", b)
	}
	if b := testBasket(t, s, other.ID); b.Count != 0 || b.CanCheckout {
		t.Error("basket leaked between sessions")
	}
	testCart(t, s, owner.ID, 1, 0)
	if b := testBasket(t, s, owner.ID); b.Count != 0 || b.CanCheckout {
		t.Error("zero quantity did not remove item")
	}
	current := testSession(t, s, owner.ID)
	if _, err := s.Checkout(owner.ID, current.CheckoutKey, current.Revision); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty checkout = %v, want ErrEmpty", err)
	}
}

func TestExpiredSessionCannotRestoreManagerGrant(t *testing.T) {
	s := newTestStore(t)
	old := testSession(t, s, "")
	if err := s.Manager(old.ID, true); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, "UPDATE sessions SET expires=? WHERE id=?", time.Now().Add(-time.Second).Unix(), old.ID)
	fresh := testSession(t, s, old.ID)
	if fresh.ID == old.ID || fresh.ManagerUntil != 0 || fresh.CSRF == old.CSRF {
		t.Errorf("expired session reused sensitive state: %+v", fresh)
	}
}

func TestRelativeDatabasePathPersistsAcrossReopen(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := Open("shop.db")
	if err != nil {
		t.Fatalf("Open relative path: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	session := testSession(t, s, "")
	testCart(t, s, session.ID, 1, 2)
	id := testCheckout(t, s, session.ID)
	testCart(t, s, session.ID, 2, 1)
	if err := s.Adjust(1, 3, 2, "Restock before restart"); err != nil {
		t.Fatal(err)
	}
	before := testOrder(t, s, id, session.ID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, "shop.db")
	if after := testOrder(t, reopened, id, session.ID); !reflect.DeepEqual(after, before) {
		t.Errorf("order not persisted: got %+v, want %+v", after, before)
	}
	if b := testBasket(t, reopened, session.ID); b.Count != 1 || len(b.Lines) != 1 || b.Lines[0].Product.ID != 2 {
		t.Errorf("cart not persisted: %+v", b)
	}
	if p := testProduct(t, reopened, 1); p.Stock != 25 || p.Version != 3 {
		t.Errorf("stock/version reset on restart: %+v", p)
	}
	if testCount(t, reopened, "products") != 8 || testCount(t, reopened, "orders") != 1 || testCount(t, reopened, "adjustments") != 1 {
		t.Error("reopening duplicated or lost persisted data")
	}
}

func TestInventoryAuditFailureRollsBackStock(t *testing.T) {
	s := newTestStore(t)
	before := testProduct(t, s, 1)
	testExec(t, s, `CREATE TRIGGER reject_adjustment BEFORE INSERT ON adjustments BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END`)
	if err := s.Adjust(1, 3, before.Version, "Restock"); err == nil {
		t.Fatal("adjustment unexpectedly succeeded despite audit failure")
	}
	if after := testProduct(t, s, 1); after != before {
		t.Error("audit failure left a stock or version change")
	}
	if testCount(t, s, "adjustments") != 0 {
		t.Error("audit failure left an adjustment row")
	}
}
