package shop

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func employeeFixture(t *testing.T, s *Store) EmployeeSession {
	t.Helper()
	e, err := s.EmployeeSession("")
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func employeeQueueTest(t *testing.T, s *Store, e EmployeeSession) EmployeeWorkspace {
	t.Helper()
	q, err := s.EmployeeQueue(e.Token)
	if err != nil {
		t.Fatal(err)
	}
	return q
}
func enrollEmployeeOrderTest(t *testing.T, s *Store, id int64) {
	t.Helper()
	testExec(t, s, `INSERT INTO employee_store_orders(order_id,epoch) SELECT ?,epoch FROM handheld_state WHERE id=1`, id)
}
func employeeClaimTest(t *testing.T, s *Store, e EmployeeSession, id int64) HandheldSession {
	t.Helper()
	var version int64
	if err := s.db.QueryRow(`SELECT order_version FROM orders WHERE id=?`, id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	g, err := s.EmployeeClaim(e.Token, e.CSRF, token(), id, version)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func TestEmployeePracticeIsExplicitAtomicOnceAndCustomerPrivate(t *testing.T) {
	s := newTestStore(t)
	e := employeeFixture(t, s)
	other := employeeFixture(t, s)
	if e.Name == other.Name || e.ShopperID == other.ShopperID || e.Token == other.Token {
		t.Fatal("employee identities reused")
	}
	q := employeeQueueTest(t, s, e)
	if q.Seeded || len(q.Queue) != 0 {
		t.Fatal("GET seeded", q)
	}
	before := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
	if err := s.SeedEmployeePractice(e.Token, "bad"); !errors.Is(err, ErrHandheldCSRF) {
		t.Fatal(err)
	}
	if after := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`); !reflect.DeepEqual(before, after) {
		t.Fatal("invalid csrf allocated stock")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, v := range []EmployeeSession{e, other} {
		wg.Add(1)
		go func(v EmployeeSession) { defer wg.Done(); errs <- s.SeedEmployeePractice(v.Token, v.CSRF) }(v)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	q = employeeQueueTest(t, s, e)
	if !q.Seeded || len(q.Queue) != 3 || q.AvailableCount != 3 || testCount(t, s, "orders") != 3 {
		t.Fatal(q)
	}
	after := fingerprintTest(t, s.db)
	if err := s.SeedEmployeePractice(other.Token, other.CSRF); err != nil {
		t.Fatal(err)
	}
	if fingerprintTest(t, s.db) != after {
		t.Fatal("reseed changed database")
	}
	var allocated int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM employee_store_orders so JOIN orders o ON o.id=so.order_id JOIN sessions owner ON owner.id=o.session_id JOIN working_order_items w ON w.order_id=o.id JOIN products p ON p.id=w.product_id WHERE so.practice=1 AND w.quantity=1 AND w.allocated_quantity=1 AND owner.expires<=?`, s.now().Unix()).Scan(&allocated); err != nil || allocated != 3 {
		t.Fatal(allocated, err)
	}
	customer := testSession(t, s, "")
	orders, err := s.Orders(customer.ID, false)
	if err != nil || len(orders) != 0 {
		t.Fatal("practice leaked into visitor manager", orders, err)
	}
	for _, item := range q.Queue {
		if _, err = s.CustomerMessages(item.ID, customer.ID, MessageQuery{}); !errors.Is(err, ErrNotFound) {
			t.Fatal("practice customer access", err)
		}
	}
}
func TestEmployeePracticeUnavailableStillRecordsOneAttempt(t *testing.T) {
	s := newTestStore(t)
	e := employeeFixture(t, s)
	testExec(t, s, `UPDATE products SET stock=0`)
	if err := s.SeedEmployeePractice(e.Token, e.CSRF); err != nil {
		t.Fatal(err)
	}
	q := employeeQueueTest(t, s, e)
	if !q.Seeded || len(q.Queue) != 0 {
		t.Fatal(q)
	}
	testExec(t, s, `UPDATE products SET stock=1`)
	if err := s.SeedEmployeePractice(e.Token, e.CSRF); err != nil {
		t.Fatal(err)
	}
	if testCount(t, s, "orders") != 0 {
		t.Fatal("zero-result attempt reseeded")
	}
}
func TestEmployeeClaimRaceReplayAndReconnectPreservePicks(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	enrollEmployeeOrderTest(t, s, id)
	e := employeeFixture(t, s)
	other := employeeFixture(t, s)
	q := employeeQueueTest(t, s, e)
	key := token()
	type result struct {
		e   EmployeeSession
		g   HandheldSession
		err error
	}
	out := make(chan result, 2)
	var wg sync.WaitGroup
	for _, v := range []EmployeeSession{e, other} {
		wg.Add(1)
		go func(v EmployeeSession) {
			defer wg.Done()
			g, err := s.EmployeeClaim(v.Token, v.CSRF, key, id, q.Queue[0].Version)
			out <- result{v, g, err}
		}(v)
	}
	wg.Wait()
	close(out)
	var winner result
	var wins, losses int
	for r := range out {
		if r.err == nil {
			winner = r
			wins++
		} else if errors.Is(r.err, ErrEmployeeClaim) {
			losses++
		} else {
			t.Fatal(r.err)
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatal(wins, losses)
	}
	c := handheldCommand(t, s, winner.g, 0, 1)
	if _, err := s.ConfirmHandheldPick(winner.g.Token, winner.g.CSRF, c); err != nil {
		t.Fatal(err)
	}
	saved := testOrder(t, s, id, owner.ID)
	stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
	before := fingerprintTest(t, s.db)
	again, err := s.EmployeeClaim(winner.e.Token, winner.e.CSRF, key, id, q.Queue[0].Version)
	if err != nil || again != winner.g {
		t.Fatal("replay churned capability", again, err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("exact replay mutated")
	}
	if _, err = s.EmployeeClaim(winner.e.Token, winner.e.CSRF, key, id, saved.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("edited replay accepted", err)
	}
	next, err := s.EmployeeClaim(winner.e.Token, winner.e.CSRF, token(), id, saved.Version)
	if err != nil {
		t.Fatal(err)
	}
	assertHandheldDenied(t, s, winner.g, c)
	if _, err = s.EmployeeClaim(winner.e.Token, winner.e.CSRF, key, id, q.Queue[0].Version); !errors.Is(err, ErrEmployeeClaim) {
		t.Fatal("old replay revived old grant", err)
	}
	task := handheldTaskTest(t, s, next)
	if task.Lines[0].Picked != 1 {
		t.Fatal("saved pick lost")
	}
	if got := testOrder(t, s, id, owner.ID); !reflect.DeepEqual(saved, got) {
		t.Fatal("reconnect modified order")
	}
	if after := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`); !reflect.DeepEqual(stock, after) {
		t.Fatal("claim/reconnect changed stock")
	}
}
func TestEmployeeScopeRevocationAndExpiredOwner(t *testing.T) {
	s := newTestStore(t)
	owner, privateID := pickingFixture(t, s)
	e := employeeFixture(t, s)
	if _, err := s.EmployeeClaim(e.Token, e.CSRF, token(), privateID, 1); !errors.Is(err, ErrEmployeeClaim) {
		t.Fatal("private claim", err)
	}
	enrollEmployeeOrderTest(t, s, privateID)
	g := employeeClaimTest(t, s, e, privateID)
	c := handheldCommand(t, s, g, 0, 1)
	testExec(t, s, `UPDATE sessions SET expires=0 WHERE id=?`, owner.ID)
	if _, _, err := s.HandheldTask(g.Token); err != nil {
		t.Fatal("shared enrolled order expired with customer", err)
	}
	if _, err := s.ConfirmHandheldPick(g.Token, g.CSRF, c); err != nil {
		t.Fatal(err)
	}
	testExec(t, s, `UPDATE employee_sessions SET revoked=1 WHERE token_hash=?`, e.Hash)
	assertHandheldDenied(t, s, g, c)
	if _, err := s.HandheldMessages(g.Token, MessageQuery{}); !errors.Is(err, ErrHandheldAccess) {
		t.Fatal("revoked messages", err)
	}
	other := employeeFixture(t, s)
	q := employeeQueueTest(t, s, other)
	if q.AvailableCount != 1 || q.Queue[0].Assigned {
		t.Fatal("revoked assignment stranded", q)
	}
	next := employeeClaimTest(t, s, other, privateID)
	if handheldTaskTest(t, s, next).Lines[0].Picked != 1 {
		t.Fatal("reclaim lost pick")
	}
	testExec(t, s, `DELETE FROM employee_store_orders WHERE order_id=?`, privateID)
	assertHandheldDenied(t, s, next, c)
}
func TestEmployeeExpiryReleaseAndReclaimKeepsState(t *testing.T) {
	s, clock, _ := newReservationStore(t)
	owner, id := pickingFixture(t, s)
	enrollEmployeeOrderTest(t, s, id)
	e := employeeFixture(t, s)
	g := employeeClaimTest(t, s, e, id)
	c := handheldCommand(t, s, g, 0, 1)
	if _, err := s.ConfirmHandheldPick(g.Token, g.CSRF, c); err != nil {
		t.Fatal(err)
	}
	items := migrationQuerySnapshot(t, s.db, `SELECT * FROM working_order_items`)
	stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
	before := testOrder(t, s, id, owner.ID)
	clock.at(clock.now().Add(employeeTTL + time.Second).Unix())
	other := employeeFixture(t, s)
	q := employeeQueueTest(t, s, other)
	if q.AvailableCount != 1 || q.Queue[0].Version != before.Version+1 {
		t.Fatal(q)
	}
	assertHandheldDenied(t, s, g, c)
	if after := migrationQuerySnapshot(t, s.db, `SELECT * FROM working_order_items`); !reflect.DeepEqual(items, after) {
		t.Fatal("expiry changed working items")
	}
	if after := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`); !reflect.DeepEqual(stock, after) {
		t.Fatal("expiry changed stock")
	}
	next := employeeClaimTest(t, s, other, id)
	if handheldTaskTest(t, s, next).Lines[0].Picked != 1 {
		t.Fatal("saved pick lost")
	}
}
func TestEmployeeVoluntaryReleaseAndReadyAreScopedReplaySafe(t *testing.T) {
	s := newTestStore(t)
	e := employeeFixture(t, s)
	other := employeeFixture(t, s)
	if err := s.SeedEmployeePractice(e.Token, e.CSRF); err != nil {
		t.Fatal(err)
	}
	q := employeeQueueTest(t, s, e)
	id := q.Queue[0].ID
	g := employeeClaimTest(t, s, e, id)
	task := handheldTaskTest(t, s, g)
	if err := s.EmployeeReady(other.Token, other.CSRF, token(), id, task.Version); !errors.Is(err, ErrEmployeeClaim) {
		t.Fatal(err)
	}
	if err := s.EmployeeReady(e.Token, e.CSRF, token(), id, task.Version); !errors.Is(err, ErrIncomplete) {
		t.Fatal(err)
	}
	pick := handheldCommand(t, s, g, 0, 1)
	if _, err := s.ConfirmHandheldPick(g.Token, g.CSRF, pick); err != nil {
		t.Fatal(err)
	}
	task = handheldTaskTest(t, s, g)
	key := token()
	if err := s.EmployeeRelease(e.Token, e.CSRF, key, id, task.Version); err != nil {
		t.Fatal(err)
	}
	assertHandheldDenied(t, s, g, pick)
	next := employeeClaimTest(t, s, other, id)
	after := fingerprintTest(t, s.db)
	if err := s.EmployeeRelease(e.Token, e.CSRF, key, id, task.Version); err != nil {
		t.Fatal(err)
	}
	if fingerprintTest(t, s.db) != after {
		t.Fatal("release replay touched new owner")
	}
	task = handheldTaskTest(t, s, next)
	if task.Lines[0].Picked != 1 {
		t.Fatal("release lost pick")
	}
	testExec(t, s, `UPDATE orders SET attention_reason='PRIVATE HOLD',order_version=order_version+1 WHERE id=?`, id)
	task = handheldTaskTest(t, s, next)
	if err := s.EmployeeReady(other.Token, other.CSRF, key, id, task.Version); !errors.Is(err, ErrAttention) {
		t.Fatal(err)
	}
	testExec(t, s, `UPDATE orders SET attention_reason='',order_version=order_version+1 WHERE id=?`, id)
	task = handheldTaskTest(t, s, next)
	stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
	receipt := migrationQuerySnapshot(t, s.db, `SELECT * FROM order_items`)
	if err := s.EmployeeReady(other.Token, other.CSRF, key, id, task.Version); err != nil {
		t.Fatal(err)
	}
	done := fingerprintTest(t, s.db)
	if err := s.EmployeeReady(other.Token, other.CSRF, key, id, task.Version); err != nil {
		t.Fatal(err)
	}
	if fingerprintTest(t, s.db) != done {
		t.Fatal("ready replay mutated")
	}
	assertHandheldDenied(t, s, next, pick)
	var status, kind string
	var total, final int64
	if err := s.db.QueryRow(`SELECT status,completion_kind,total,final_total FROM orders WHERE id=?`, id).Scan(&status, &kind, &total, &final); err != nil || status != "Ready" || kind != "full" || total != final {
		t.Fatal(status, kind, total, final, err)
	}
	if after := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`); !reflect.DeepEqual(stock, after) {
		t.Fatal("ready changed stock")
	}
	if after := migrationQuerySnapshot(t, s.db, `SELECT * FROM order_items`); !reflect.DeepEqual(receipt, after) {
		t.Fatal("ready changed receipt")
	}
}
func TestEmployeeClaimReplayCannotCrossAssignmentLifetimes(t *testing.T) {
	s := newTestStore(t)
	e := employeeFixture(t, s)
	if err := s.SeedEmployeePractice(e.Token, e.CSRF); err != nil {
		t.Fatal(err)
	}
	q := employeeQueueTest(t, s, e)
	item := q.Queue[0]
	key := token()
	g, err := s.EmployeeClaim(e.Token, e.CSRF, key, item.ID, item.Version)
	if err != nil {
		t.Fatal(err)
	}
	task := handheldTaskTest(t, s, g)
	if err = s.EmployeeRelease(e.Token, e.CSRF, key, item.ID, task.Version); err != nil {
		t.Fatal("cross-action key collision", err)
	}
	next := employeeClaimTest(t, s, e, item.ID)
	before := fingerprintTest(t, s.db)
	if _, err = s.EmployeeClaim(e.Token, e.CSRF, key, item.ID, item.Version); !errors.Is(err, ErrEmployeeClaim) {
		t.Fatal("old assignment replay accepted", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("stale replay revoked new grant")
	}
	handheldTaskTest(t, s, next)
}
func TestEmployeePracticeRollbackAndLegacyPairingExpiry(t *testing.T) {
	s := newTestStore(t)
	e := employeeFixture(t, s)
	testExec(t, s, `CREATE TRIGGER refuse_employee_seed BEFORE INSERT ON employee_store_orders BEGIN SELECT RAISE(ABORT,'test rollback'); END`)
	before := fingerprintTest(t, s.db)
	if err := s.SeedEmployeePractice(e.Token, e.CSRF); err == nil {
		t.Fatal("injected failure not reported")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("failed seed left partial state")
	}
	testExec(t, s, `DROP TRIGGER refuse_employee_seed`)
	owner, _, g := handheldFixture(t, s)
	c := handheldCommand(t, s, g, 0, 1)
	testExec(t, s, `UPDATE sessions SET expires=0 WHERE id=?`, owner.ID)
	assertHandheldDenied(t, s, g, c)
}
func TestEmployeeSessionAndProjectionKeepManagerIdentitySeparate(t *testing.T) {
	s := newTestStore(t)
	owner := testSession(t, s, "")
	if err := s.Manager(owner.ID, true); err != nil {
		t.Fatal(err)
	}
	e := employeeFixture(t, s)
	resumed, err := s.EmployeeSession(e.Token)
	if err != nil || resumed.Token != "" || resumed.ShopperID != e.ShopperID || resumed.CSRF != e.CSRF {
		t.Fatal(resumed, err)
	}
	roster, err := s.Shoppers(ShopperFilters{}, owner.ID, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, person := range roster.Roster {
		if person.ID == e.ShopperID {
			t.Fatal("employee in private manager roster")
		}
	}
	q := employeeQueueTest(t, s, e)
	if projection := fmt.Sprintf("%+v", q.Queue); strings.Contains(projection, owner.ID) || strings.Contains(projection, e.Token) {
		t.Fatal("secret projected")
	}
}

func TestEmployeePracticeCannotCreateUnresolvableHoldsOrMessages(t *testing.T) {
	s := newTestStore(t)
	e := employeeFixture(t, s)
	if err := s.SeedEmployeePractice(e.Token, e.CSRF); err != nil {
		t.Fatal(err)
	}
	id := employeeQueueTest(t, s, e).Queue[0].ID
	g := employeeClaimTest(t, s, e, id)
	task := handheldTaskTest(t, s, g)
	if !task.Employee || !task.Practice {
		t.Fatal("missing employee practice identity", task)
	}
	before := fingerprintTest(t, s.db)
	c := HandheldReport{LineID: task.Lines[0].LineID, AssignmentVersion: task.Assignment.Version, Version: task.Version, PickVersion: task.Lines[0].PickVersion, Kind: "unavailable", Key: token()}
	if _, err := s.ReportHandheldItem(g.Token, g.CSRF, c); !errors.Is(err, ErrEmployeePractice) {
		t.Fatal(err)
	}
	if _, err := s.HandheldMessages(g.Token, MessageQuery{}); !errors.Is(err, ErrEmployeePractice) {
		t.Fatal(err)
	}
	if _, err := s.SendHandheldMessage(g.Token, g.CSRF, MessageCommand{Key: token(), Body: "No synthetic customer exists"}); !errors.Is(err, ErrEmployeePractice) {
		t.Fatal(err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("practice report/message request mutated data")
	}
}

func TestEmployeeWeightRequiresMeasurementAndKeepsRequestedReceipt(t *testing.T) {
	s := newTestStore(t)
	p := weightedProduct(t, s, 10000, 349, 1)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, 500)
	id := testCheckout(t, s, owner.ID)
	enrollEmployeeOrderTest(t, s, id)
	e := employeeFixture(t, s)
	g := employeeClaimTest(t, s, e, id)
	task := handheldTaskTest(t, s, g)
	if !task.Employee || task.Practice {
		t.Fatal("incorrect task kind")
	}
	if err := s.EmployeeReady(e.Token, e.CSRF, token(), id, task.Version); !errors.Is(err, ErrIncomplete) {
		t.Fatal(err)
	}
	original := placedSnapshot(t, s, id)
	c := handheldWeightInput(t, s, g, 527, "")
	preview, err := s.PreviewHandheldWeight(g.Token, g.CSRF, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmHandheldWeight(g.Token, g.CSRF, preview.Command); err != nil {
		t.Fatal(err)
	}
	task = handheldTaskTest(t, s, g)
	if task.PickedLines != 1 {
		t.Fatal(task)
	}
	stock := testProduct(t, s, p.ID).Stock
	if err = s.EmployeeReady(e.Token, e.CSRF, token(), id, task.Version); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, placedSnapshot(t, s, id)) || testProduct(t, s, p.ID).Stock != stock {
		t.Fatal("Ready changed receipt or stock")
	}
	o := testOrder(t, s, id, owner.ID)
	expected, _ := lineAmount(527, 349, 1000, "g")
	if o.FinalTotal != expected {
		t.Fatal("actual-weight final amount", o)
	}
}

func TestEmployeeClaimsSerializeAcrossStoreConnections(t *testing.T) {
	s, clock, path := newReservationStore(t)
	otherStore := reservationStore(t, path, clock)
	e := employeeFixture(t, s)
	other := employeeFixture(t, otherStore)
	if err := s.SeedEmployeePractice(e.Token, e.CSRF); err != nil {
		t.Fatal(err)
	}
	item := employeeQueueTest(t, s, e).Queue[0]
	start := make(chan struct{})
	out := make(chan error, 2)
	var wg sync.WaitGroup
	for _, v := range []struct {
		s *Store
		e EmployeeSession
	}{{s, e}, {otherStore, other}} {
		wg.Add(1)
		go func(s *Store, e EmployeeSession) {
			defer wg.Done()
			<-start
			_, err := s.EmployeeClaim(e.Token, e.CSRF, token(), item.ID, item.Version)
			out <- err
		}(v.s, v.e)
	}
	close(start)
	wg.Wait()
	close(out)
	wins, losses := 0, 0
	for err := range out {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrEmployeeClaim) {
			losses++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatal(wins, losses)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM shopper_assignments WHERE order_id=? AND state='active'`, item.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
