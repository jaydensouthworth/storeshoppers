package shop

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A clock shared by separate Store connections makes expiry/race tests
// deterministic without time.Sleep or depending on the machine's wall clock.
type reservationClock struct{ seconds atomic.Int64 }

func newReservationClock() *reservationClock {
	c := &reservationClock{}
	c.seconds.Store(time.Now().Unix())
	return c
}
func (c *reservationClock) now() time.Time { return time.Unix(c.seconds.Load(), 0).UTC() }
func (c *reservationClock) at(unix int64)  { c.seconds.Store(unix) }
func reservationStore(t *testing.T, path string, c *reservationClock) *Store {
	t.Helper()
	s, err := OpenWithClock(path, c.now)
	if err != nil {
		t.Fatalf("OpenWithClock: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}
func newReservationStore(t *testing.T) (*Store, *reservationClock, string) {
	t.Helper()
	c := newReservationClock()
	path := filepath.Join(t.TempDir(), "reservations.db")
	return reservationStore(t, path, c), c, path
}
func reservationSnapshot(t *testing.T, s *Store) map[string][][]any {
	t.Helper()
	out := make(map[string][][]any)
	for _, table := range []string{"products", "sessions", "baskets", "cart", "orders", "order_items", "basket_events", "adjustments"} {
		out[table] = migrationQuerySnapshot(t, s.db, `SELECT * FROM `+table+` ORDER BY 1,2`)
	}
	return out
}
func assertReservationUnchanged(t *testing.T, s *Store, before map[string][][]any) {
	t.Helper()
	after := reservationSnapshot(t, s)
	for table, expected := range before {
		if !reflect.DeepEqual(expected, after[table]) {
			t.Errorf("rejected operation changed %s: before %#v, after %#v", table, expected, after[table])
		}
	}
}
func assertReservedStock(t *testing.T, s *Store, pid, available, reserved int64) {
	t.Helper()
	p := testProduct(t, s, pid)
	if p.Stock != available || p.Reserved != reserved {
		t.Fatalf("product %d inventory available=%d reserved=%d, want %d/%d", pid, p.Stock, p.Reserved, available, reserved)
	}
}
func reservationCheckout(t *testing.T, s *Store, sid, instructions string) int64 {
	t.Helper()
	b := testBasket(t, s, sid)
	session := testSession(t, s, sid)
	id, err := s.CheckoutWithInstructions(sid, session.CheckoutKey, b.Revision, b.Quote, instructions)
	if err != nil {
		t.Fatalf("CheckoutWithInstructions: %v", err)
	}
	return id
}

func TestReservationsReserveDeltasReleaseAndCheckoutExactlyOnce(t *testing.T) {
	s, _, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	initial := testProduct(t, s, 1).Stock
	testCart(t, s, owner.ID, 1, 3)
	first := testBasket(t, s, owner.ID)
	if first.ID == "" || first.ID == owner.ID || strings.Contains(first.ID, owner.ID) {
		t.Fatalf("basket identity is missing or leaks a session: %q", first.ID)
	}
	if len(first.Lines) != 1 || first.Lines[0].Reserved != 3 || !first.CanCheckout || first.NeedsReview || first.HoldUntil <= 0 {
		t.Fatalf("new reservation is not held and checkout-ready: %+v", first)
	}
	assertReservedStock(t, s, 1, initial-3, 3)
	if err := s.SetCartVersion(owner.ID, 1, 2, true, first.Revision); err != nil {
		t.Fatal(err)
	}
	increased := testBasket(t, s, owner.ID)
	if increased.Count != 5 || increased.Revision <= first.Revision {
		t.Fatalf("increment did not advance basket: %+v", increased)
	}
	assertReservedStock(t, s, 1, initial-5, 5)
	if err := s.SetCartVersion(owner.ID, 1, 2, false, increased.Revision); err != nil {
		t.Fatal(err)
	}
	assertReservedStock(t, s, 1, initial-2, 2)
	current := testBasket(t, s, owner.ID)
	session := testSession(t, s, owner.ID)
	id, err := s.CheckoutWithInstructions(owner.ID, session.CheckoutKey, current.Revision, current.Quote, "Keep chilled")
	if err != nil || id == 0 {
		t.Fatalf("checkout = %d, %v", id, err)
	}
	assertReservedStock(t, s, 1, initial-2, 0)
	if b := testBasket(t, s, owner.ID); b.Count != 0 || len(b.Lines) != 0 || b.CanCheckout {
		t.Errorf("checkout did not clear basket: %+v", b)
	}
	if o := testOrder(t, s, id, owner.ID); o.Instructions != "Keep chilled" || o.RequiredCount != 2 {
		t.Errorf("wrong receipt snapshot: %+v", o)
	}
	before := reservationSnapshot(t, s)
	replay, err := s.CheckoutWithInstructions(owner.ID, session.CheckoutKey, current.Revision, current.Quote, "Overwrite instructions")
	if err != nil || replay != id {
		t.Fatalf("replay = %d, %v, want %d", replay, err, id)
	}
	assertReservationUnchanged(t, s, before)
	testCart(t, s, owner.ID, 1, 4)
	removal := testBasket(t, s, owner.ID)
	if err := s.SetCartVersion(owner.ID, 1, 0, false, removal.Revision); err != nil {
		t.Fatal(err)
	}
	assertReservedStock(t, s, 1, initial-2, 0)
}

func TestReservationsExpireOnceWithoutReadRenewalAndSurviveRestart(t *testing.T) {
	s, c, path := newReservationStore(t)
	owner := testSession(t, s, "")
	initial := testProduct(t, s, 1).Stock
	testCart(t, s, owner.ID, 1, 2)
	held := testBasket(t, s, owner.ID)
	c.at(held.HoldUntil - 1)
	for i := 0; i < 3; i++ {
		if b := testBasket(t, s, owner.ID); b.HoldUntil != held.HoldUntil || b.Revision != held.Revision {
			t.Fatalf("read renewed or changed reservation: %+v", b)
		}
		if _, err := s.Products("", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Baskets(owner.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	assertReservedStock(t, s, 1, initial-2, 2)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = reservationStore(t, path, c)
	if b := testBasket(t, s, owner.ID); b.ID != held.ID || b.HoldUntil != held.HoldUntil || b.Revision != held.Revision {
		t.Fatalf("restart lost or renewed a live hold: %+v", b)
	}
	c.at(held.HoldUntil)
	if err := s.ExpireHolds(); err != nil {
		t.Fatal(err)
	}
	expired := testBasket(t, s, owner.ID)
	if expired.Count != 2 || len(expired.Lines) != 1 || expired.Lines[0].Reserved != 0 || expired.CanCheckout || !expired.NeedsReview {
		t.Fatalf("expired contents must remain visible and require review: %+v", expired)
	}
	assertReservedStock(t, s, 1, initial, 0)
	before := reservationSnapshot(t, s)
	for i := 0; i < 3; i++ {
		if err := s.ExpireHolds(); err != nil {
			t.Fatal(err)
		}
		_ = testBasket(t, s, owner.ID)
		_ = testProduct(t, s, 1)
	}
	assertReservationUnchanged(t, s, before)
	session := testSession(t, s, owner.ID)
	if id, err := s.Checkout(owner.ID, session.CheckoutKey, expired.Revision, expired.Quote); !errors.Is(err, ErrHold) || id != 0 {
		t.Errorf("expired checkout = %d, %v, want ErrHold", id, err)
	}
	assertReservationUnchanged(t, s, before)
	if err := s.RenewBasket(owner.ID, expired.Revision); err != nil {
		t.Fatal(err)
	}
	renewed := testBasket(t, s, owner.ID)
	if renewed.HoldUntil <= held.HoldUntil || renewed.NeedsReview || !renewed.CanCheckout || renewed.Lines[0].Reserved != 2 {
		t.Errorf("explicit renewal did not restore hold: %+v", renewed)
	}
	assertReservedStock(t, s, 1, initial-2, 2)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	c.at(renewed.HoldUntil + 1)
	s = reservationStore(t, path, c)
	_ = testBasket(t, s, owner.ID)
	assertReservedStock(t, s, 1, initial, 0)
}

func TestReservationsRaceForLastUnitAcrossConnections(t *testing.T) {
	first, c, path := newReservationStore(t)
	second := reservationStore(t, path, c)
	testExec(t, first, `UPDATE products SET stock=1 WHERE id=1`)
	stores := []*Store{first, second}
	const n = 16
	owners := make([]Session, n)
	for i := range owners {
		owners[i] = testSession(t, stores[i%2], "")
	}
	type outcome struct {
		index int
		err   error
	}
	start := make(chan struct{})
	results := make(chan outcome, n)
	var wg sync.WaitGroup
	for i := range owners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results <- outcome{i, stores[i%2].SetCart(owners[i].ID, 1, 1, false)}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	winner := -1
	for got := range results {
		b := testBasket(t, first, owners[got.index].ID)
		if got.err == nil {
			winners++
			winner = got.index
			if b.Count != 1 || b.Lines[0].Reserved != 1 {
				t.Errorf("winner lacks reservation: %+v", b)
			}
		} else {
			if !errors.Is(got.err, ErrStock) {
				t.Errorf("last-unit reservation returned %v, want ErrStock", got.err)
			}
			if b.Count != 0 {
				t.Errorf("failed reservation left cart contents: %+v", b)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("reservation winners=%d, want 1", winners)
	}
	assertReservedStock(t, first, 1, 0, 1)
	reservationCheckout(t, stores[winner%2], owners[winner].ID, "")
	assertReservedStock(t, first, 1, 0, 0)
	if count := testCount(t, first, "orders"); count != 1 {
		t.Errorf("orders=%d, want 1", count)
	}
}

func TestReservationsRenewAllOrNothingAndStaleCommands(t *testing.T) {
	s, c, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	competitor := testSession(t, s, "")
	testExec(t, s, `UPDATE products SET stock=2 WHERE id IN (1,2)`)
	testCart(t, s, owner.ID, 1, 2)
	testCart(t, s, owner.ID, 2, 2)
	held := testBasket(t, s, owner.ID)
	before := reservationSnapshot(t, s)
	for _, command := range []func() error{
		func() error { return s.SetCartVersion(owner.ID, 1, 1, false, held.Revision-1) },
		func() error { return s.RenewBasket(owner.ID, held.Revision-1) },
	} {
		if err := command(); !errors.Is(err, ErrConflict) {
			t.Errorf("stale command = %v", err)
		}
		assertReservationUnchanged(t, s, before)
	}
	c.at(held.HoldUntil + 1)
	if err := s.ExpireHolds(); err != nil {
		t.Fatal(err)
	}
	testCart(t, s, competitor.ID, 2, 1)
	expired := testBasket(t, s, owner.ID)
	before = reservationSnapshot(t, s)
	if err := s.RenewBasket(owner.ID, expired.Revision); !errors.Is(err, ErrStock) {
		t.Fatalf("partial-capacity renew = %v, want ErrStock", err)
	}
	assertReservationUnchanged(t, s, before)
	assertReservedStock(t, s, 1, 2, 0)
	assertReservedStock(t, s, 2, 1, 1)
}

func TestReservationsCheckoutStaleQuoteRevisionAndInsertRollback(t *testing.T) {
	for _, failure := range []string{"revision", "quote", "receipt insert"} {
		t.Run(failure, func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 2)
			testCart(t, s, owner.ID, 2, 1)
			b := testBasket(t, s, owner.ID)
			session := testSession(t, s, owner.ID)
			revision, quote := b.Revision, b.Quote
			var want error
			switch failure {
			case "revision":
				revision--
				want = ErrConflict
			case "quote":
				p := testProduct(t, s, 1)
				p.Price++
				if _, err := s.SaveProduct(p); err != nil {
					t.Fatal(err)
				}
				want = ErrQuote
			case "receipt insert":
				testExec(t, s, `CREATE TRIGGER reject_reserved_receipt BEFORE INSERT ON order_items WHEN NEW.product_id=2 BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END`)
			}
			before := reservationSnapshot(t, s)
			id, err := s.CheckoutWithInstructions(owner.ID, session.CheckoutKey, revision, quote, "A saved note")
			if id != 0 || err == nil || (want != nil && !errors.Is(err, want)) {
				t.Fatalf("checkout %s = %d, %v, want %v", failure, id, err, want)
			}
			assertReservationUnchanged(t, s, before)
		})
	}
}

func TestReservationsInventoryCapIncludesHeldUnits(t *testing.T) {
	s, _, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	testExec(t, s, `UPDATE products SET stock=10000 WHERE id=1`)
	testCart(t, s, owner.ID, 1, 99)
	p := testProduct(t, s, 1)
	assertReservedStock(t, s, 1, 9901, 99)
	before := reservationSnapshot(t, s)
	if err := s.Adjust(1, 1, p.Version, "Must not exceed physical cap"); err == nil {
		t.Fatal("adjustment ignored reserved stock in physical cap")
	}
	assertReservationUnchanged(t, s, before)
	if err := s.Adjust(1, -9902, p.Version, "Cannot remove held units"); err == nil {
		t.Fatal("negative adjustment consumed a held unit")
	}
	assertReservationUnchanged(t, s, before)
	if err := s.Adjust(1, -1, p.Version, "Verified shelf damage"); err != nil {
		t.Fatal(err)
	}
	p = testProduct(t, s, 1)
	if err := s.Adjust(1, 1, p.Version, "Replacement received"); err != nil {
		t.Fatal(err)
	}
	testCart(t, s, owner.ID, 1, 0)
	assertReservedStock(t, s, 1, 10000, 0)
}

func TestReservationsInvalidQuantityNeverPartiallyMutates(t *testing.T) {
	s, _, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 2)
	for _, tc := range []struct {
		pid, qty int64
		add      bool
	}{{1, -1, false}, {1, 100, false}, {1, math.MaxInt64, true}, {1, math.MinInt64, true}, {1, 99, true}, {0, 1, false}, {-1, 1, false}} {
		t.Run(fmt.Sprintf("%d_%d_%t", tc.pid, tc.qty, tc.add), func(t *testing.T) {
			b := testBasket(t, s, owner.ID)
			before := reservationSnapshot(t, s)
			if err := s.SetCartVersion(owner.ID, tc.pid, tc.qty, tc.add, b.Revision); !errors.Is(err, ErrInvalid) {
				t.Errorf("invalid mutation=%v", err)
			}
			assertReservationUnchanged(t, s, before)
		})
	}
}

func TestReservationsManagerAuditFailureRollsBack(t *testing.T) {
	for _, command := range []string{"edit", "renew"} {
		t.Run(command, func(t *testing.T) {
			s, c, _ := newReservationStore(t)
			owner := testSession(t, s, "")
			manager := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 2)
			b := testBasket(t, s, owner.ID)
			if command == "renew" {
				c.at(b.HoldUntil + 1)
				if err := s.ExpireHolds(); err != nil {
					t.Fatal(err)
				}
				b = testBasket(t, s, owner.ID)
			}
			testExec(t, s, `CREATE TRIGGER reject_basket_event BEFORE INSERT ON basket_events BEGIN SELECT RAISE(ABORT,'injected basket audit failure'); END`)
			before := reservationSnapshot(t, s)
			var err error
			if command == "edit" {
				err = s.ManagerSetBasket(b.ID, manager.ID, true, 1, 3, b.Revision, "Customer requested another apple")
			} else {
				err = s.ManagerRenewBasket(b.ID, manager.ID, true, b.Revision, "Customer reviewed their basket")
			}
			if err == nil || !strings.Contains(err.Error(), "injected basket audit failure") {
				t.Fatalf("manager %s did not reach failing audit insert: %v", command, err)
			}
			assertReservationUnchanged(t, s, before)
		})
	}
}

func TestReservationsInstructionsValidationAndImmutableReplay(t *testing.T) {
	for _, input := range []string{strings.Repeat("界", 501), "bad\x00note", "bad\x1bnote", "bad\x7fnote", "bad\vnote", "bad\rnote", "\vnote", "note\f", "\rnote", string([]byte{'a', 0xff, 'b'})} {
		t.Run(fmt.Sprintf("invalid_%x", []byte(input)[:min(len(input), 8)]), func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 1)
			b := testBasket(t, s, owner.ID)
			session := testSession(t, s, owner.ID)
			before := reservationSnapshot(t, s)
			if id, err := s.CheckoutWithInstructions(owner.ID, session.CheckoutKey, b.Revision, b.Quote, input); id != 0 || !errors.Is(err, ErrInvalid) {
				t.Errorf("invalid instructions = %d, %v", id, err)
			}
			assertReservationUnchanged(t, s, before)
		})
	}
	for _, input := range []string{strings.Repeat("界", 500), "Leave by desk\r\nRing once\tplease"} {
		t.Run(fmt.Sprintf("valid_%d", len(input)), func(t *testing.T) {
			s, _, _ := newReservationStore(t)
			owner := testSession(t, s, "")
			testCart(t, s, owner.ID, 1, 1)
			id := reservationCheckout(t, s, owner.ID, input)
			if got := testOrder(t, s, id, owner.ID).Instructions; got != strings.ReplaceAll(input, "\r\n", "\n") {
				t.Errorf("instructions changed: %q", got)
			}
		})
	}
}

func TestReservationsOrderPercentageUsesPickedUnits(t *testing.T) {
	s, _, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, 1, 2)
	testCart(t, s, owner.ID, 2, 1)
	id := reservationCheckout(t, s, owner.ID, "")
	if err := s.AdvanceScoped(id, "Placed", owner.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ pid, qty, version, percent int64 }{{1, 1, 1, 33}, {1, 2, 2, 66}, {2, 1, 1, 100}} {
		if err := s.RecordPicked(id, tc.pid, tc.qty, tc.version, owner.ID, false); err != nil {
			t.Fatal(err)
		}
		order := testOrder(t, s, id, owner.ID)
		if order.Percent != tc.percent {
			t.Errorf("picked=%d/%d percent=%d, want %d", order.PickedCount, order.RequiredCount, order.Percent, tc.percent)
		}
		orders, err := s.Orders(owner.ID, false)
		if err != nil || len(orders) != 1 || orders[0].Percent != tc.percent {
			t.Errorf("list progress disagrees: %+v, %v", orders, err)
		}
	}
}

func TestReservationsManagerScopeReasonsAndPrivateAudit(t *testing.T) {
	s, _, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	other := testSession(t, s, "")
	testCart(t, s, other.ID, 1, 2)
	foreign := testBasket(t, s, other.ID)
	if _, err := s.ManagedBasket(foreign.ID, owner.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("demo scope exposed foreign basket: %v", err)
	}
	before := reservationSnapshot(t, s)
	if err := s.ManagerSetBasket(foreign.ID, owner.ID, false, 1, 1, foreign.Revision, "Unauthorized demo edit"); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign edit=%v", err)
	}
	if err := s.ManagerRenewBasket(foreign.ID, owner.ID, false, foreign.Revision, "Unauthorized demo renewal"); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign renewal=%v", err)
	}
	assertReservationUnchanged(t, s, before)
	for _, reason := range []string{"bad\nreason", strings.Repeat("x", 121)} {
		if err := s.ManagerSetBasket(foreign.ID, owner.ID, true, 1, 1, foreign.Revision, reason); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid reason %q accepted: %v", reason, err)
		}
		assertReservationUnchanged(t, s, before)
	}
	if err := s.ManagerSetBasket(foreign.ID, owner.ID, true, 1, 1, foreign.Revision, "Customer requested one fewer"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`SELECT basket_id,action,reason,details FROM basket_events`)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var id, action, reason, details string
		if err := rows.Scan(&id, &action, &reason, &details); err != nil {
			t.Fatal(err)
		}
		count++
		joined := id + action + reason + details
		if strings.Contains(joined, owner.ID) || strings.Contains(joined, other.ID) || strings.Contains(joined, owner.CSRF) || strings.Contains(joined, other.CheckoutKey) {
			t.Error("basket audit exposed raw session material")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if count == 0 {
		t.Fatal("manager edit lacked audit event")
	}
	if _, err := s.ManagedBasket(other.ID, owner.ID, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("raw session token was accepted as basket route identity: %v", err)
	}
	beforeProducts := migrationQuerySnapshot(t, s.db, `SELECT * FROM products ORDER BY id`)
	if err := s.CreatePracticeBaskets(owner.ID); err != nil {
		t.Fatal(err)
	}
	if after := migrationQuerySnapshot(t, s.db, `SELECT * FROM products ORDER BY id`); !reflect.DeepEqual(beforeProducts, after) {
		t.Error("practice creation reserved real shared inventory without review")
	}
	mine, err := s.Baskets(owner.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	synthetic := 0
	for _, b := range mine {
		if b.ID == foreign.ID {
			t.Error("scoped listing exposed foreign basket")
		}
		var fake bool
		if err := s.db.QueryRow(`SELECT synthetic FROM baskets WHERE id=?`, b.ID).Scan(&fake); err != nil {
			t.Fatal(err)
		}
		if fake {
			synthetic++
			if b.CanCheckout || !b.NeedsReview {
				t.Errorf("practice basket was implicitly reserved: %+v", b)
			}
			if _, err := s.ManagedBasket(b.ID, other.ID, false); !errors.Is(err, ErrNotFound) {
				t.Errorf("synthetic basket leaked across demo sessions: %v", err)
			}
		}
	}
	if synthetic != 2 {
		t.Errorf("practice baskets=%d, want two", synthetic)
	}
}

func TestReservationsRemovingUnavailableLineReleasesHold(t *testing.T) {
	s, _, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	initial1, initial2 := testProduct(t, s, 1).Stock, testProduct(t, s, 2).Stock
	testCart(t, s, owner.ID, 1, 2)
	testCart(t, s, owner.ID, 2, 3)
	held := testBasket(t, s, owner.ID)
	testExec(t, s, `UPDATE products SET archived=1,catalog_version=catalog_version+1 WHERE id IN (1,2)`)
	if err := s.SetCartVersion(owner.ID, 1, 0, false, held.Revision); err != nil {
		t.Fatalf("cannot remove unavailable line: %v", err)
	}
	b := testBasket(t, s, owner.ID)
	if len(b.Lines) != 1 || b.Lines[0].Product.ID != 2 || b.HoldUntil != held.HoldUntil {
		t.Errorf("removal lost remaining contents or extended hold: %+v", b)
	}
	// Archived products are excluded from Products, so inspect the persisted
	// available amount directly without restoring or otherwise changing stock.
	var stock1, stock2 int64
	if err := s.db.QueryRow(`SELECT stock FROM products WHERE id=1`).Scan(&stock1); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT stock FROM products WHERE id=2`).Scan(&stock2); err != nil {
		t.Fatal(err)
	}
	if stock1 != initial1 || stock2 != initial2-3 || b.Lines[0].Reserved != 3 {
		t.Errorf("removal lost inventory conservation: available=%d/%d, basket=%+v", stock1, stock2, b)
	}
}

// Frozen v3 schema additions, independent of migrations/003_catalog.sql. This
// builds a populated prior-version database rather than testing an empty schema.
const reservationLegacyCatalogV3 = `
CREATE TABLE categories (id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,normalized_name TEXT NOT NULL UNIQUE,archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN(0,1)),version INTEGER NOT NULL DEFAULT 1 CHECK(version>0));
CREATE TABLE product_types (id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,normalized_name TEXT NOT NULL UNIQUE,archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN(0,1)),version INTEGER NOT NULL DEFAULT 1 CHECK(version>0));
ALTER TABLE products ADD COLUMN category_id INTEGER REFERENCES categories(id);
ALTER TABLE products ADD COLUMN type_id INTEGER REFERENCES product_types(id);
ALTER TABLE products ADD COLUMN sku TEXT NOT NULL DEFAULT '';
ALTER TABLE products ADD COLUMN archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN(0,1));
ALTER TABLE products ADD COLUMN catalog_version INTEGER NOT NULL DEFAULT 1 CHECK(catalog_version>0);
ALTER TABLE products ADD COLUMN price_version INTEGER NOT NULL DEFAULT 1 CHECK(price_version>0);
ALTER TABLE products ADD COLUMN sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN('each','g'));
ALTER TABLE products ADD COLUMN price_basis INTEGER NOT NULL DEFAULT 1 CHECK((sale_unit='each' AND price_basis=1) OR (sale_unit='g' AND price_basis=1000));
ALTER TABLE products ADD COLUMN quantity_step INTEGER NOT NULL DEFAULT 1 CHECK((sale_unit='each' AND quantity_step=1) OR (sale_unit='g' AND quantity_step BETWEEN 1 AND 1000));
CREATE TABLE product_codes(id INTEGER PRIMARY KEY AUTOINCREMENT,product_id INTEGER NOT NULL REFERENCES products(id),scheme TEXT NOT NULL CHECK(scheme IN('legacy_placeholder','demo_local')),raw_value TEXT NOT NULL,normalized_value TEXT NOT NULL,symbology TEXT NOT NULL,archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN(0,1)),UNIQUE(scheme,normalized_value));
CREATE TABLE catalog_sequence(id INTEGER PRIMARY KEY CHECK(id=1),next_product_id INTEGER NOT NULL CHECK(next_product_id>0));
INSERT INTO catalog_sequence SELECT 1,COALESCE(MAX(id),0)+1 FROM products;
ALTER TABLE order_items ADD COLUMN sku TEXT NOT NULL DEFAULT '';
ALTER TABLE order_items ADD COLUMN sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN('each','g'));
ALTER TABLE order_items ADD COLUMN price_basis INTEGER NOT NULL DEFAULT 1 CHECK(price_basis>0);
ALTER TABLE order_items ADD COLUMN quantity_step INTEGER NOT NULL DEFAULT 1 CHECK(quantity_step>0);
ALTER TABLE order_items ADD COLUMN subtotal INTEGER NOT NULL DEFAULT 0 CHECK(subtotal>=0);
UPDATE order_items SET subtotal=price*quantity;
ALTER TABLE adjustments ADD COLUMN sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN('each','g'));
CREATE TABLE catalog_events(id INTEGER PRIMARY KEY AUTOINCREMENT,kind TEXT NOT NULL CHECK(kind IN('product','category','type')),entity_id INTEGER NOT NULL,action TEXT NOT NULL CHECK(action IN('create','edit','archive','restore')),name TEXT NOT NULL,details TEXT NOT NULL,created TEXT NOT NULL DEFAULT(strftime('%Y-%m-%d %H:%M UTC','now')));
INSERT INTO categories(id,name,normalized_name) VALUES(1,'Produce','produce');
UPDATE products SET category_id=1,sku=printf('SAVED-%06d',id),catalog_version=17,price_version=6;
CREATE UNIQUE INDEX products_sku_unique ON products(sku COLLATE NOCASE);
UPDATE order_items SET sku=printf('RECEIPT-%06d',product_id);
INSERT INTO product_codes(product_id,scheme,raw_value,normalized_value,symbology) SELECT id,'demo_local',sku,sku,'Code128' FROM products;
INSERT INTO schema_version VALUES(3);`

func populatedReservationV3(t *testing.T) (string, Session) {
	t.Helper()
	path, owner := populatedV2(t)
	db := migrationRawDB(t, path)
	migrationExec(t, db, reservationLegacyCatalogV3)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, owner
}
func TestReservationsV4MigrationPreservesPopulatedV3AndReopens(t *testing.T) {
	path, owner := populatedReservationV3(t)
	db := migrationRawDB(t, path)
	queries := map[string]string{"products": `SELECT * FROM products ORDER BY id`, "sessions": `SELECT * FROM sessions ORDER BY id`, "orders": `SELECT id,reference,session_id,checkout_key,total,status,created FROM orders ORDER BY id`, "order_items": `SELECT * FROM order_items ORDER BY order_id,product_id`, "adjustments": `SELECT * FROM adjustments ORDER BY id`, "codes": `SELECT * FROM product_codes ORDER BY id`}
	snapshots := make(map[string][][]any)
	for name, q := range queries {
		snapshots[name] = migrationQuerySnapshot(t, db, q)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var basketID string
	for pass := 0; pass < 2; pass++ {
		s := openTestStore(t, path)
		for name, q := range queries {
			if got := migrationQuerySnapshot(t, s.db, q); !reflect.DeepEqual(got, snapshots[name]) {
				t.Errorf("v4 pass %d changed historical %s", pass, name)
			}
		}
		b := testBasket(t, s, owner.ID)
		if b.ID == "" || b.ID == owner.ID || b.Count != 3 || len(b.Lines) != 1 || b.Lines[0].Product.ID != 2 || b.Lines[0].Reserved != 0 || b.CanCheckout || !b.NeedsReview {
			t.Errorf("unsafe migrated basket: %+v", b)
		}
		if pass == 0 {
			basketID = b.ID
		} else if b.ID != basketID {
			t.Error("reopen assigned a new basket identity")
		}
		if p := testProduct(t, s, 2); p.Stock != 7 || p.Reserved != 0 {
			t.Errorf("migration claimed unreserved legacy stock: %+v", p)
		}
		if o := testOrder(t, s, 2, owner.ID); o.Instructions != "" || o.PickedCount != 1 || o.Items[0].PickVersion != 7 || o.Items[0].SKU != "RECEIPT-000001" {
			t.Errorf("legacy order changed: %+v", o)
		}
		var version int
		if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != latestSchemaVersion {
			t.Errorf("schema version=%d,%v", version, err)
		}
		var legacyColumn int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('cart') WHERE name='session_id'`).Scan(&legacyColumn); err != nil || legacyColumn != 0 {
			t.Errorf("legacy session cart identity remains: %d,%v", legacyColumn, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestReservationsV4MigrationFailureLeavesV3Intact(t *testing.T) {
	path, _ := populatedReservationV3(t)
	db := migrationRawDB(t, path)
	migrationExec(t, db, `CREATE TRIGGER reject_reservations_migration BEFORE INSERT ON schema_version WHEN NEW.version=4 BEGIN SELECT RAISE(ABORT,'injected reservation migration failure'); END`)
	beforeSchema := migrationQuerySnapshot(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`)
	beforeData := migrationLegacyData(t, db)
	if s, err := Open(path); err == nil {
		_ = s.Close()
		t.Fatal("v4 migration ignored injected final-statement failure")
	} else if !strings.Contains(err.Error(), "injected reservation migration failure") {
		t.Fatalf("v4 migration failed before injected failure: %v", err)
	}
	if after := migrationQuerySnapshot(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`); !reflect.DeepEqual(beforeSchema, after) {
		t.Error("failed v4 migration left partial schema")
	}
	if after := migrationLegacyData(t, db); !reflect.DeepEqual(beforeData, after) {
		t.Error("failed v4 migration changed saved v3 data")
	}
	migrationExec(t, db, `DROP TRIGGER reject_reservations_migration`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	recovered := openTestStore(t, path)
	var version int
	if err := recovered.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != latestSchemaVersion {
		t.Errorf("v4 retry did not recover: %d,%v", version, err)
	}
}

func TestReservationsHeldStockPreventsUnitConversion(t *testing.T) {
	s, _, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	testExec(t, s, `UPDATE products SET stock=2 WHERE id=1`)
	testCart(t, s, owner.ID, 1, 2)
	p := testProduct(t, s, 1)
	if p.Stock != 0 || p.Reserved != 2 {
		t.Fatalf("fixture must reserve all available stock: %+v", p)
	}
	p.SaleUnit = "g"
	p.PriceBasis = 1000
	p.QuantityStep = 100
	before := reservationSnapshot(t, s)
	beforeEvents := testCount(t, s, "catalog_events")
	if id, err := s.SaveProduct(p); id != 0 || !errors.Is(err, ErrUnitLocked) {
		t.Errorf("held product unit conversion = %d,%v, want ErrUnitLocked", id, err)
	}
	assertReservationUnchanged(t, s, before)
	if testCount(t, s, "catalog_events") != beforeEvents {
		t.Error("rejected unit conversion wrote catalog audit")
	}
}

func TestReservationsSessionExpiryReleasesBeforeHoldDeadline(t *testing.T) {
	s, c, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	initial := testProduct(t, s, 1).Stock
	testCart(t, s, owner.ID, 1, 2)
	held := testBasket(t, s, owner.ID)
	expires := c.now().Unix() + 30
	if expires >= held.HoldUntil {
		t.Fatal("fixture session must expire before hold")
	}
	testExec(t, s, `UPDATE sessions SET expires=? WHERE id=?`, expires, owner.ID)
	c.at(expires)
	if err := s.ExpireHolds(); err != nil {
		t.Fatal(err)
	}
	assertReservedStock(t, s, 1, initial, 0)
	expired := testBasket(t, s, owner.ID)
	if expired.Count != 2 || expired.Lines[0].Reserved != 0 || expired.CanCheckout || !expired.NeedsReview {
		t.Errorf("expired session retained active allocation: %+v", expired)
	}
	before := reservationSnapshot(t, s)
	if err := s.ExpireHolds(); err != nil {
		t.Fatal(err)
	}
	assertReservationUnchanged(t, s, before)
	replacement := testSession(t, s, owner.ID)
	if replacement.ID == owner.ID {
		t.Fatal("expired session was reused")
	}
	if b := testBasket(t, s, replacement.ID); b.Count != 0 {
		t.Error("replacement session inherited expired private basket")
	}
}

func TestReservationsExpiryRollbackCannotReturnStockTwice(t *testing.T) {
	s, c, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	initial := testProduct(t, s, 1).Stock
	testCart(t, s, owner.ID, 1, 2)
	held := testBasket(t, s, owner.ID)
	testExec(t, s, `CREATE TRIGGER reject_release_after_stock BEFORE UPDATE OF reserved ON cart WHEN OLD.reserved>0 AND NEW.reserved=0 BEGIN SELECT RAISE(ABORT,'injected release failure'); END`)
	c.at(held.HoldUntil)
	before := reservationSnapshot(t, s)
	if err := s.ExpireHolds(); err == nil || !strings.Contains(err.Error(), "injected release failure") {
		t.Fatalf("expiry did not reach injected rollback: %v", err)
	}
	assertReservationUnchanged(t, s, before)
	testExec(t, s, `DROP TRIGGER reject_release_after_stock`)
	if err := s.ExpireHolds(); err != nil {
		t.Fatal(err)
	}
	assertReservedStock(t, s, 1, initial, 0)
	if err := s.ExpireHolds(); err != nil {
		t.Fatal(err)
	}
	assertReservedStock(t, s, 1, initial, 0)
}

func TestReservationsExplicitEditReacquiresWholeExpiredBasketAtomically(t *testing.T) {
	s, c, _ := newReservationStore(t)
	owner := testSession(t, s, "")
	other := testSession(t, s, "")
	testExec(t, s, `UPDATE products SET stock=2 WHERE id IN(1,2)`)
	testCart(t, s, owner.ID, 1, 2)
	testCart(t, s, owner.ID, 2, 2)
	held := testBasket(t, s, owner.ID)
	c.at(held.HoldUntil)
	if err := s.ExpireHolds(); err != nil {
		t.Fatal(err)
	}
	testCart(t, s, other.ID, 2, 2)
	expired := testBasket(t, s, owner.ID)
	before := reservationSnapshot(t, s)
	if err := s.SetCartVersion(owner.ID, 1, 1, false, expired.Revision); !errors.Is(err, ErrStock) {
		t.Errorf("edit with unavailable remainder=%v, want ErrStock", err)
	}
	assertReservationUnchanged(t, s, before)
	testCart(t, s, other.ID, 2, 0)
	if err := s.SetCartVersion(owner.ID, 1, 1, false, expired.Revision); err != nil {
		t.Fatal(err)
	}
	renewed := testBasket(t, s, owner.ID)
	if renewed.Count != 3 || !renewed.CanCheckout || renewed.NeedsReview || renewed.HoldUntil <= held.HoldUntil {
		t.Errorf("explicit edit did not reacquire complete desired basket: %+v", renewed)
	}
	assertReservedStock(t, s, 1, 1, 1)
	assertReservedStock(t, s, 2, 0, 2)
}
