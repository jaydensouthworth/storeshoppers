package shop

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func attentionManagerOrder(t *testing.T, s *Store, id int64, sid string) Order {
	t.Helper()
	o, err := s.ManagerOrder(id, sid, false)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func attentionCommand(t *testing.T, s *Store, id int64, sid, action, reason string) AttentionCommand {
	t.Helper()
	return AttentionCommand{Action: action, Key: token(), Reason: reason, Version: attentionManagerOrder(t, s, id, sid).Version}
}

func attentionApply(t *testing.T, s *Store, id int64, sid, action, reason string) AttentionCommand {
	t.Helper()
	c := attentionCommand(t, s, id, sid, action, reason)
	if err := s.AttentionOrder(id, sid, false, c); err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return c
}

func TestAttentionLifecycleReplayAndCustomerPrivacy(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	other := testSession(t, s, "")
	now := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	before := attentionManagerOrder(t, s, id, owner.ID)
	receipt := placedSnapshot(t, s, id)
	stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
	// A customer-visible event must survive filtering of newer internal events.
	if err := s.AdvanceVersioned(id, "Placed", before.Version, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	before = attentionManagerOrder(t, s, id, owner.ID)
	hold := attentionApply(t, s, id, owner.ID, "hold", "Private stock investigation")
	held := attentionManagerOrder(t, s, id, owner.ID)
	if !held.Held || held.AttentionReason != hold.Reason || held.AttentionSince != now.Unix() || held.Version != before.Version+1 || held.Status != "Picking" {
		t.Fatalf("hold state=%+v", held)
	}
	note := attentionApply(t, s, id, owner.ID, "note", "Private call from receiving")
	withNote := attentionManagerOrder(t, s, id, owner.ID)
	if withNote.AttentionReason != held.AttentionReason || withNote.AttentionSince != held.AttentionSince || !withNote.Held || withNote.Version != held.Version+1 {
		t.Fatal("note replaced the active hold or failed to revise the order")
	}
	for _, scope := range []struct {
		sid string
		all bool
	}{{owner.ID, false}, {owner.ID, true}, {other.ID, true}} {
		public, err := s.Order(id, scope.sid, scope.all)
		if err != nil {
			t.Fatal(err)
		}
		if !public.Held || public.AttentionReason != "" || public.AttentionSince != 0 || len(public.Events) != 1 {
			t.Fatalf("customer projection exposed private fields or lost public history: %+v", public)
		}
		if text := fmt.Sprintf("%+v", public); strings.Contains(text, hold.Reason) || strings.Contains(text, note.Reason) {
			t.Fatal("internal text leaked through customer order projection")
		}
	}
	if len(withNote.Events) != 3 {
		t.Fatalf("manager lost internal history: %+v", withNote.Events)
	}
	listed, err := s.Orders(owner.ID, true)
	if err != nil || len(listed) != 1 || !listed[0].Held || listed[0].AttentionReason != "" || listed[0].AttentionSince != 0 {
		t.Fatal("customer list exposed private attention despite all-orders scope", listed, err)
	}
	if _, err := s.ManagerOrder(id, other.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign manager scope", err)
	}
	if all, err := s.ManagerOrder(id, other.ID, true); err != nil || all.AttentionReason != hold.Reason || len(all.Events) != 3 {
		t.Fatal("store-wide manager projection", all, err)
	}
	beforeReplay := fingerprintTest(t, s.db)
	for _, c := range []AttentionCommand{hold, note} {
		if err := s.AttentionOrder(id, owner.ID, false, c); err != nil {
			t.Fatal("exact stale retry", err)
		}
	}
	if fingerprintTest(t, s.db) != beforeReplay {
		t.Fatal("replay changed hold, revision or audit")
	}
	release := attentionApply(t, s, id, owner.ID, "release", "")
	released := attentionManagerOrder(t, s, id, owner.ID)
	if released.Held || released.AttentionReason != "" || released.AttentionSince != 0 || released.Version != withNote.Version+1 {
		t.Fatal("release did not clear active hold", released)
	}
	var releaseReason, visibility string
	if err := s.db.QueryRow(`SELECT reason,visibility FROM order_events WHERE order_id=? AND command_key=?`, id, release.Key).Scan(&releaseReason, &visibility); err != nil || strings.TrimSpace(releaseReason) == "" || visibility != "internal" {
		t.Fatalf("release needs a default internal audit: %q %q %v", releaseReason, visibility, err)
	}
	now = now.Add(time.Minute)
	attentionApply(t, s, id, owner.ID, "hold", "Private second investigation")
	reheld := attentionManagerOrder(t, s, id, owner.ID)
	if reheld.AttentionSince != now.Unix() || !reheld.Held {
		t.Fatal("rehold retained old timestamp", reheld)
	}
	beforeReplay = fingerprintTest(t, s.db)
	for _, old := range []AttentionCommand{release, hold} {
		if err := s.AttentionOrder(id, owner.ID, false, old); err != nil {
			t.Fatal("old exact command should remain replayable", err)
		}
	}
	if fingerprintTest(t, s.db) != beforeReplay {
		t.Fatal("old replay undid the replacement hold")
	}
	if !reflect.DeepEqual(receipt, placedSnapshot(t, s, id)) || !reflect.DeepEqual(stock, migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)) {
		t.Fatal("attention changed placed receipt or stock")
	}
	var internal int
	if err := s.db.QueryRow(`SELECT count(*) FROM order_events WHERE order_id=? AND visibility='internal'`, id).Scan(&internal); err != nil || internal != 4 {
		t.Fatal("attention audit visibility/count", internal, err)
	}
}

func TestAttentionRejectsInvalidStaleForeignAndChangedReplayWithoutWrites(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	other := testSession(t, s, "")
	for _, tc := range []struct {
		name string
		edit func(*AttentionCommand)
		want error
	}{
		{"unknown action", func(c *AttentionCommand) { c.Action = "unknown" }, ErrInvalid},
		{"missing key", func(c *AttentionCommand) { c.Key = "" }, ErrInvalid},
		{"zero version", func(c *AttentionCommand) { c.Version = 0 }, ErrInvalid},
		{"stale version", func(c *AttentionCommand) { c.Version++ }, ErrConflict},
		{"empty reason", func(c *AttentionCommand) { c.Reason = "" }, ErrInvalid},
		{"short reason", func(c *AttentionCommand) { c.Reason = "ab" }, ErrInvalid},
		{"too many runes", func(c *AttentionCommand) { c.Reason = strings.Repeat("界", 241) }, ErrInvalid},
		{"control", func(c *AttentionCommand) { c.Reason = "bad\nreason" }, ErrInvalid},
		{"NUL", func(c *AttentionCommand) { c.Reason = "bad\x00reason" }, ErrInvalid},
		{"invalid UTF8", func(c *AttentionCommand) { c.Reason = string([]byte{'b', 'a', 'd', 255}) }, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := attentionCommand(t, s, id, owner.ID, "hold", "Review stock discrepancy")
			tc.edit(&c)
			before := fingerprintTest(t, s.db)
			if err := s.AttentionOrder(id, owner.ID, false, c); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("rejected command changed database")
			}
		})
	}
	c := attentionCommand(t, s, id, owner.ID, "hold", "Review stock discrepancy")
	before := fingerprintTest(t, s.db)
	if err := s.AttentionOrder(id, other.ID, false, c); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign attention command", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("foreign command changed database")
	}
	if err := s.AttentionOrder(id, other.ID, true, c); err != nil {
		t.Fatal("store-wide manager command", err)
	}
	for _, change := range []func(*AttentionCommand){
		func(c *AttentionCommand) { c.Reason = "Changed private reason" },
		func(c *AttentionCommand) { c.Action = "note" },
		func(c *AttentionCommand) { c.Version++ },
	} {
		altered := c
		change(&altered)
		before = fingerprintTest(t, s.db)
		if err := s.AttentionOrder(id, owner.ID, false, altered); !errors.Is(err, ErrConflict) {
			t.Fatal("reused key with changed payload", err)
		}
		if fingerprintTest(t, s.db) != before {
			t.Fatal("changed replay had side effects")
		}
	}
	attentionApply(t, s, id, owner.ID, "note", strings.Repeat("界", 240))
	// Command keys share the existing order audit namespace across command types.
	edit := overrideCommand(t, s, id, owner.ID, "set", 1, 3)
	edit.Key = c.Key
	before = fingerprintTest(t, s.db)
	if err := s.OverrideOrder(id, owner.ID, false, edit); !errors.Is(err, ErrConflict) {
		t.Fatal("attention key reused for allocation command", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("cross-command collision changed stock")
	}
}

func TestAttentionAuditFailureRollsBackEachAction(t *testing.T) {
	for _, action := range []string{"hold", "note", "release"} {
		t.Run(action, func(t *testing.T) {
			s := newTestStore(t)
			owner, id := pickingFixture(t, s)
			if action == "release" {
				attentionApply(t, s, id, owner.ID, "hold", "Investigate before release")
			}
			c := attentionCommand(t, s, id, owner.ID, action, "Manager verified attention change")
			testExec(t, s, `CREATE TRIGGER reject_attention_audit BEFORE INSERT ON order_events WHEN NEW.visibility='internal' BEGIN SELECT RAISE(ABORT,'internal audit unavailable'); END`)
			before := fingerprintTest(t, s.db)
			if err := s.AttentionOrder(id, owner.ID, false, c); err == nil {
				t.Fatal("audit failure should abort")
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("failed audit committed hold state or order version")
			}
			testExec(t, s, `DROP TRIGGER reject_attention_audit`)
			if err := s.AttentionOrder(id, owner.ID, false, c); err != nil {
				t.Fatal("retry after audit recovery", err)
			}
		})
	}
}

func TestAttentionConcurrentConnectionsSerializeCommands(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_key=%t", sameKey), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "shop.db")
			first, second := openTestStore(t, path), openTestStore(t, path)
			owner, id := pickingFixture(t, first)
			c := attentionCommand(t, first, id, owner.ID, "hold", "One serialized hold")
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, store := range []*Store{first, second} {
				command := c
				if !sameKey {
					command.Key = fmt.Sprintf("independent-attention-key-%d", i)
				}
				go func(s *Store, command AttentionCommand) {
					<-start
					results <- s.AttentionOrder(id, owner.ID, false, command)
				}(store, command)
			}
			close(start)
			wins, conflicts := 0, 0
			for range 2 {
				err := <-results
				if err == nil {
					wins++
				} else if errors.Is(err, ErrConflict) {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			wantWins, wantConflicts := 1, 1
			if sameKey {
				wantWins, wantConflicts = 2, 0
			}
			o := attentionManagerOrder(t, first, id, owner.ID)
			if wins != wantWins || conflicts != wantConflicts || o.Version != c.Version+1 || !o.Held || testCount(t, first, "order_events") != 1 {
				t.Fatalf("race wins=%d conflicts=%d order=%+v", wins, conflicts, o)
			}
		})
	}
}

func TestAttentionHoldRacesReadyWithoutHeldFinalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	first, second := openTestStore(t, path), openTestStore(t, path)
	owner, id := pickingFixture(t, first)
	testAssign(t, first, id, owner.ID, 1)
	if err := first.Advance(id, "Placed"); err != nil {
		t.Fatal(err)
	}
	for _, item := range testOrder(t, first, id, owner.ID).WorkingItems {
		if err := first.RecordPicked(id, item.ProductID, item.Quantity, item.PickVersion, owner.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	hold := attentionCommand(t, first, id, owner.ID, "hold", "Review before collection")
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- first.AttentionOrder(id, owner.ID, false, hold) }()
	go func() { <-start; results <- second.AdvanceVersioned(id, "Picking", hold.Version, owner.ID, false) }()
	close(start)
	wins := 0
	for range 2 {
		err := <-results
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrTerminal) && !errors.Is(err, ErrAttention) {
			t.Fatal(err)
		}
	}
	o := attentionManagerOrder(t, first, id, owner.ID)
	if wins != 1 || o.Version != hold.Version+1 {
		t.Fatal("hold and Ready both committed or lost revision", wins, o)
	}
	if o.Held {
		if o.Status != "Picking" || o.Finalized || o.Assignment == nil {
			t.Fatal("winning hold allowed finalization or assignment closure", o)
		}
	} else if o.Status != "Ready" || !o.Finalized || o.Assignment != nil {
		t.Fatal("winning Ready left a hold or active assignment", o)
	}
}

func TestAttentionAllowsWorkButBlocksReadyWithoutSideEffects(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	attentionApply(t, s, id, owner.ID, "hold", "Resolve supply exception before handoff")
	testAssign(t, s, id, owner.ID, 1)
	edit := overrideCommand(t, s, id, owner.ID, "set", 1, 3)
	if err := s.OverrideOrder(id, owner.ID, false, edit); err != nil {
		t.Fatal("held allocation edit", err)
	}
	o := attentionManagerOrder(t, s, id, owner.ID)
	if err := s.AdvanceVersioned(id, "Placed", o.Version, owner.ID, false); err != nil {
		t.Fatal("hold prevented beginning work", err)
	}
	stale := attentionCommand(t, s, id, owner.ID, "release", "")
	for _, item := range testOrder(t, s, id, owner.ID).WorkingItems {
		if err := s.RecordPicked(id, item.ProductID, item.Quantity, item.PickVersion, owner.ID, false); err != nil {
			t.Fatal("held picking", err)
		}
	}
	before := fingerprintTest(t, s.db)
	if err := s.AttentionOrder(id, owner.ID, false, stale); !errors.Is(err, ErrConflict) {
		t.Fatal("pick did not invalidate stale release", err)
	}
	o = attentionManagerOrder(t, s, id, owner.ID)
	if !o.AllPicked || !o.Held || o.Assignment == nil {
		t.Fatal("fixture did not retain held complete work and assignment", o)
	}
	for _, advance := range []func() error{
		func() error { return s.AdvanceVersioned(id, "Picking", o.Version, owner.ID, false) },
		func() error { return s.AdvanceScoped(id, "Picking", owner.ID, false) },
	} {
		if err := advance(); !errors.Is(err, ErrAttention) {
			t.Fatal("held Ready should return ErrAttention", err)
		}
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("held Ready or stale release changed stock, audit, finalization or assignment")
	}
	attentionApply(t, s, id, owner.ID, "release", "Exception resolved")
	o = attentionManagerOrder(t, s, id, owner.ID)
	if err := s.AdvanceVersioned(id, "Picking", o.Version, owner.ID, false); err != nil {
		t.Fatal("released Ready", err)
	}
	for _, state := range []string{"Ready", "Completed"} {
		for _, action := range []string{"hold", "release", "note"} {
			c := attentionCommand(t, s, id, owner.ID, action, "Closed order cannot change attention")
			before = fingerprintTest(t, s.db)
			if err := s.AttentionOrder(id, owner.ID, false, c); !errors.Is(err, ErrTerminal) {
				t.Fatalf("%s %s: %v", state, action, err)
			}
			if fingerprintTest(t, s.db) != before {
				t.Fatal("closed attention command changed data")
			}
		}
		if state == "Ready" {
			if err := s.Advance(id, "Ready"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestAttentionFinishRejectionAndCancellationAreAtomic(t *testing.T) {
	s := newTestStore(t)
	owner, id := pickingFixture(t, s)
	attentionApply(t, s, id, owner.ID, "hold", "Cancellation requires manager review")
	testAssign(t, s, id, owner.ID, 2)
	stock1, stock2 := testProduct(t, s, 1).Stock, testProduct(t, s, 2).Stock
	other := testSession(t, s, "")
	testCart(t, s, other.ID, 3, 1)
	finish := overrideCommand(t, s, id, owner.ID, "finish", 0, 0)
	// A rejected finish must not expire an unrelated basket as a preflight side effect.
	testExec(t, s, `UPDATE baskets SET hold_until=? WHERE owner_session_id=?`, s.now().Add(-time.Minute).Unix(), other.ID)
	before := fingerprintTest(t, s.db)
	if err := s.OverrideOrder(id, owner.ID, false, finish); !errors.Is(err, ErrAttention) {
		t.Fatal("held partial finish", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("held finish rejection mutated unrelated reservations, stock or order state")
	}
	// Renew only the fixture's expiry so cancellation rollback isolates its own writes.
	testExec(t, s, `UPDATE baskets SET hold_until=? WHERE owner_session_id=?`, s.now().Add(time.Minute).Unix(), other.ID)
	cancel := overrideCommand(t, s, id, owner.ID, "cancel", 0, 0)
	testExec(t, s, `CREATE TRIGGER reject_attention_cancel BEFORE INSERT ON shopper_event_links BEGIN SELECT RAISE(ABORT,'assignment closure unavailable'); END`)
	before = fingerprintTest(t, s.db)
	if err := s.OverrideOrder(id, owner.ID, false, cancel); err == nil {
		t.Fatal("cancel should fail with its assignment audit")
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("failed cancellation cleared hold, restocked or ended assignment")
	}
	testExec(t, s, `DROP TRIGGER reject_attention_cancel`)
	if err := s.OverrideOrder(id, owner.ID, false, cancel); err != nil {
		t.Fatal("cancel held order", err)
	}
	o := attentionManagerOrder(t, s, id, owner.ID)
	if o.Status != "Completed" || o.CompletionKind != "cancelled" || !o.Finalized || o.FinalTotal != 0 || o.Held || o.AttentionReason != "" || o.AttentionSince != 0 || o.Assignment != nil {
		t.Fatal("cancellation did not close held work", o)
	}
	if testProduct(t, s, 1).Stock != stock1+2 || testProduct(t, s, 2).Stock != stock2+1 {
		t.Fatal("held cancellation returned incorrect allocation")
	}
	before = fingerprintTest(t, s.db)
	if err := s.OverrideOrder(id, owner.ID, false, cancel); err != nil {
		t.Fatal("cancel retry", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("cancel retry returned stock or ended assignment twice")
	}
}

func TestAttentionAllowsMeasuredWeightWhileHeld(t *testing.T) {
	s := newTestStore(t)
	p := weightedProduct(t, s, 1000, 349, 50)
	owner := testSession(t, s, "")
	testCart(t, s, owner.ID, p.ID, 500)
	id := testCheckout(t, s, owner.ID)
	attentionApply(t, s, id, owner.ID, "hold", "Review packaging after weighing")
	confirmWeight(t, s, id, owner.ID, p.ID, 527, "")
	o := attentionManagerOrder(t, s, id, owner.ID)
	if !o.Held || !o.AllPicked || o.WorkingItems[0].Allocated != 527 || !o.WorkingItems[0].Measured || testProduct(t, s, p.ID).Stock != 473 {
		t.Fatal("held measurement did not preserve fulfillment and hold", o)
	}
	before := fingerprintTest(t, s.db)
	if err := s.AdvanceVersioned(id, "Picking", o.Version, owner.ID, false); !errors.Is(err, ErrAttention) {
		t.Fatal("measured held order reached Ready", err)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("held weighted Ready changed database")
	}
}

func attentionQueue(t *testing.T, s *Store, sid string, all bool, filters OrderFilters) OrderQueue {
	t.Helper()
	q, err := s.OrderQueue(sid, all, filters)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func attentionQueueFixture(t *testing.T, s *Store) (Session, Session, []int64, []int64) {
	t.Helper()
	owner, other := testSession(t, s, ""), testSession(t, s, "")
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var owned, foreign []int64
	states := []string{"Placed", "Picking", "Ready", "Completed"}
	for i := 0; i < 132; i++ {
		sid, prefix := owner.ID, "OWN"
		if i >= 125 {
			sid, prefix = other.ID, "FOREIGN"
		}
		status := states[i%4]
		reason, since := "", int64(0)
		if i%8 == 0 {
			reason, since = "Private queue reason", int64(1790942400)
		}
		created := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC).Add(time.Duration(i/5) * time.Minute).Format("2006-01-02 15:04 UTC")
		result, err := tx.Exec(`INSERT INTO orders(reference,session_id,checkout_key,total,status,created,attention_reason,attention_since) VALUES(?,?,?,?,?,?,?,?)`, fmt.Sprintf("%s-%04d", prefix, 132-i), sid, fmt.Sprintf("queue-fixture-key-%04d", i), 349, status, created, reason, since)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		name, sku := "Snapshot apple", "SNAPSHOT-APPLE"
		if i == 0 {
			name, sku = "Rare pineapple match", "ARCHIVE-SKU-000"
		}
		if i >= 125 {
			name, sku = "Foreign private fruit", "FOREIGN-SKU"
		}
		if _, err = tx.Exec(`INSERT INTO order_items(order_id,product_id,name,price,quantity,sku,subtotal) VALUES(?,1,?,349,1,?,349)`, id, name, sku); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			name, sku = "Replacement kumquat", "REPLACEMENT-SKU"
		}
		if _, err = tx.Exec(`INSERT INTO working_order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,allocated_quantity) VALUES(?,1,?,349,1,?,'each',1,1,1)`, id, name, sku); err != nil {
			t.Fatal(err)
		}
		if i%8 == 1 {
			if _, err = tx.Exec(`INSERT INTO shopper_assignments(order_id,shopper_id,state) VALUES(?,1,'active')`, id); err != nil {
				t.Fatal(err)
			}
		}
		if i < 125 {
			owned = append(owned, id)
		} else {
			foreign = append(foreign, id)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return owner, other, owned, foreign
}

func TestAttentionQueueFullPaginationStableOrderingAndScope(t *testing.T) {
	s := newTestStore(t)
	owner, _, owned, foreign := attentionQueueFixture(t, s)
	before := fingerprintTest(t, s.db)
	q := attentionQueue(t, s, owner.ID, false, OrderFilters{})
	if q.Total != 125 || q.Open != 94 || q.HeldCount != 16 || q.Page != 1 || q.Pages != 7 || q.Start != 1 || q.End != 20 || len(q.Orders) != 20 {
		t.Fatalf("default queue excludes rows, states or scope: %+v", q)
	}
	seen := make(map[int64]bool)
	for page := 1; page <= 7; page++ {
		q = attentionQueue(t, s, owner.ID, false, OrderFilters{Page: page})
		for j, order := range q.Orders {
			position := (page-1)*20 + j
			if order.ID != owned[len(owned)-1-position] || seen[order.ID] {
				t.Fatalf("unstable newest page %d row %d: id=%d", page, j, order.ID)
			}
			seen[order.ID] = true
		}
	}
	if len(seen) != 125 || q.Start != 121 || q.End != 125 || len(q.Orders) != 5 {
		t.Fatal("pagination capped or dropped old rows", len(seen), q)
	}
	for _, id := range foreign {
		if seen[id] {
			t.Fatal("foreign queue row leaked")
		}
	}
	for _, page := range []int{-10, 0, 999} {
		q = attentionQueue(t, s, owner.ID, false, OrderFilters{Page: page})
		want := 1
		if page == 999 {
			want = 7
		}
		if q.Page != want {
			t.Fatalf("page %d clamped to %d, want %d", page, q.Page, want)
		}
	}
	oldest := attentionQueue(t, s, owner.ID, false, OrderFilters{Sort: "oldest"})
	reference := attentionQueue(t, s, owner.ID, false, OrderFilters{Sort: "reference"})
	for i := range 20 {
		if oldest.Orders[i].ID != owned[i] || reference.Orders[i].ID != owned[len(owned)-1-i] {
			t.Fatal("oldest/reference ordering ignored deterministic secondary key")
		}
	}
	all := attentionQueue(t, s, owner.ID, true, OrderFilters{})
	if all.Total != 132 || all.Open != 99 || all.HeldCount != 17 {
		t.Fatal("store-wide stats are capped or incorrectly scoped", all)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("queue reads changed data")
	}
}

func TestAttentionQueueSearchAndCombinedFilters(t *testing.T) {
	s := newTestStore(t)
	owner, other, owned, _ := attentionQueueFixture(t, s)
	for _, tc := range []struct {
		name string
		f    OrderFilters
		want int64
	}{
		{"placed", OrderFilters{State: "Placed"}, 32},
		{"picking", OrderFilters{State: "Picking"}, 31},
		{"ready", OrderFilters{State: "Ready"}, 31},
		{"completed", OrderFilters{State: "Completed"}, 31},
		{"held", OrderFilters{Held: "held"}, 16},
		{"clear", OrderFilters{Held: "clear"}, 109},
		{"assigned", OrderFilters{Assigned: "assigned"}, 16},
		{"unassigned", OrderFilters{Assigned: "unassigned"}, 109},
		{"held placed unassigned", OrderFilters{State: "Placed", Held: "held", Assigned: "unassigned"}, 16},
		{"incompatible combination", OrderFilters{State: "Ready", Held: "held", Assigned: "assigned"}, 0},
		{"reference", OrderFilters{Query: "own-0132"}, 1},
		{"old product name", OrderFilters{Query: "PINEAPPLE"}, 1},
		{"old SKU", OrderFilters{Query: "archive-sku"}, 1},
		{"working product name", OrderFilters{Query: "kumquat"}, 1},
		{"working SKU", OrderFilters{Query: "replacement-sku"}, 1},
		{"private reason excluded", OrderFilters{Query: "Private queue reason"}, 0},
		{"foreign reference", OrderFilters{Query: "FOREIGN"}, 0},
		{"literal percent", OrderFilters{Query: "%"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := attentionQueue(t, s, owner.ID, false, tc.f)
			if q.Total != tc.want || q.Open != 94 || q.HeldCount != 16 {
				t.Fatalf("filter count or scope-wide summary: total=%d open=%d held=%d, want total=%d", q.Total, q.Open, q.HeldCount, tc.want)
			}
			if tc.want == 0 && (len(q.Orders) != 0 || q.Start != 0 || q.End != 0) {
				t.Fatal("empty queue retained rows/range", q)
			}
			if tc.want == 1 && q.Orders[0].ID != owned[0] {
				t.Fatal("search lost order beyond former hundred-row cutoff")
			}
		})
	}
	foreign := attentionQueue(t, s, other.ID, false, OrderFilters{Query: "pineapple"})
	if foreign.Total != 0 || foreign.Open != 5 || foreign.HeldCount != 1 {
		t.Fatal("foreign scope leaked product search or summary counts", foreign)
	}
	all := attentionQueue(t, s, owner.ID, true, OrderFilters{Query: "foreign-sku"})
	if all.Total != 7 {
		t.Fatal("store-wide SKU search", all)
	}
}
