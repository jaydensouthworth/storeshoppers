package shop

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func substitutionInputTest(t *testing.T, s *Store, g HandheldSession, source, replacement, quantity int64, disposition string) SubstitutionCommand {
	t.Helper()
	task := handheldTaskTest(t, s, g)
	for _, line := range task.Lines {
		if line.ProductID == source {
			return SubstitutionCommand{LineID: line.LineID, Version: task.Version, PickVersion: line.PickVersion, AssignmentVersion: task.Assignment.Version, ReplacementID: replacement, Quantity: quantity, Key: token(), Note: "Customer requested an alternative", Disposition: disposition}
		}
	}
	t.Fatalf("source product %d absent", source)
	return SubstitutionCommand{}
}
func substitutionPreviewTest(t *testing.T, s *Store, g HandheldSession, source, replacement, quantity int64, disposition string) SubstitutionPreview {
	t.Helper()
	p, err := s.PreviewSubstitution(g.Token, g.CSRF, substitutionInputTest(t, s, g, source, replacement, quantity, disposition))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func substitutionProposalTest(t *testing.T, s *Store, owner Session, id int64, g HandheldSession, source, replacement, quantity int64, disposition string) (SubstitutionProposal, SubstitutionDecision) {
	t.Helper()
	preview := substitutionPreviewTest(t, s, g, source, replacement, quantity, disposition)
	p, err := s.SendSubstitution(g.Token, g.CSRF, preview.Command)
	if err != nil {
		t.Fatal(err)
	}
	return p, substitutionDecisionTest(t, s, owner, id, p, "approve")
}
func substitutionDecisionTest(t *testing.T, s *Store, owner Session, id int64, p SubstitutionProposal, action string) SubstitutionDecision {
	t.Helper()
	w, err := s.Substitutions(id, owner.ID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	return SubstitutionDecision{ProposalID: p.ID, Key: token(), Binding: w.Binding, Review: p.Review, Action: action}
}
func substitutionPickProductTest(t *testing.T, s *Store, g HandheldSession, pid, picked int64) {
	t.Helper()
	task := handheldTaskTest(t, s, g)
	for i, l := range task.Lines {
		if l.ProductID == pid {
			if _, err := s.ConfirmHandheldPick(g.Token, g.CSRF, handheldCommand(t, s, g, i, picked)); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("pick product absent", pid)
}
func substitutionAssertUnchanged(t *testing.T, s *Store, before string) {
	t.Helper()
	if fingerprintTest(t, s.db) != before {
		t.Fatal("rejected/read/replayed substitution changed persistent state")
	}
}

func TestSubstitutionPreviewSendDecisionReceiptStockAndReplay(t *testing.T) {
	for _, disposition := range []string{"restock", "writeoff"} {
		t.Run(disposition, func(t *testing.T) {
			s := newTestStore(t)
			owner, id, g := handheldFixture(t, s)
			substitutionPickProductTest(t, s, g, 1, 1)
			receipt := placedSnapshot(t, s, id)
			before := fingerprintTest(t, s.db)
			preview := substitutionPreviewTest(t, s, g, 1, 3, 2, disposition)
			substitutionAssertUnchanged(t, s, before)
			if preview.Source.ProductID != 1 || preview.Source.Allocated != 2 || preview.Source.Picked != 1 || preview.OldAmount != 698 || preview.NewAmount != 1198 || preview.WorkingTotal != 997 || preview.ProposedTotal != 1497 || preview.AmountChange() != "+$5.00" || len(preview.Command.Review) != 64 {
				t.Fatalf("bad review: %+v", preview)
			}
			originalOrder := testOrder(t, s, id, owner.ID)
			stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
			p, err := s.SendSubstitution(g.Token, g.CSRF, preview.Command)
			if err != nil {
				t.Fatal(err)
			}
			if p.Status != "pending" || p.Preview.Command.Note != preview.Command.Note || p.DecidedAt != 0 {
				t.Fatal(p)
			}
			if !reflect.DeepEqual(originalOrder, testOrder(t, s, id, owner.ID)) || !reflect.DeepEqual(stock, migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)) {
				t.Fatal("proposal changed fulfillment")
			}
			before = fingerprintTest(t, s.db)
			replay, err := s.SendSubstitution(g.Token, g.CSRF, preview.Command)
			if err != nil || !reflect.DeepEqual(p, replay) {
				t.Fatal("proposal replay", replay, err)
			}
			substitutionAssertUnchanged(t, s, before)
			for _, edit := range []func(*SubstitutionCommand){func(c *SubstitutionCommand) { c.Note = "Changed shopper note" }, func(c *SubstitutionCommand) { c.Quantity++ }, func(c *SubstitutionCommand) { c.ReplacementID = 4 }, func(c *SubstitutionCommand) {
				c.Disposition = "restock"
				if disposition == "restock" {
					c.Disposition = "writeoff"
				}
			}, func(c *SubstitutionCommand) { c.Review = strings.Repeat("f", 64) }} {
				changed := preview.Command
				edit(&changed)
				if _, err = s.SendSubstitution(g.Token, g.CSRF, changed); !errors.Is(err, ErrConflict) {
					t.Fatal("changed proposal replay", err)
				}
			}
			substitutionAssertUnchanged(t, s, before)
			phone, err := s.Substitutions(9999, "", g.Token, true)
			if err != nil || phone.OrderID != id || !phone.CanPropose || len(phone.Proposals) != 1 || phone.Proposals[0].CanDecide || !phone.Proposals[0].CanWithdraw {
				t.Fatal(phone, err)
			}
			customer, err := s.Substitutions(id, owner.ID, "", false)
			if err != nil || len(customer.Proposals) != 1 || !customer.Proposals[0].CanDecide || customer.Proposals[0].CanWithdraw || customer.CanPropose {
				t.Fatal(customer, err)
			}
			d := substitutionDecisionTest(t, s, owner, id, p, "approve")
			if err = s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
				t.Fatal(err)
			}
			order := testOrder(t, s, id, owner.ID)
			var source WorkingOrderItem
			if err = s.db.QueryRow(`SELECT quantity,allocated_quantity,picked_quantity,pick_version FROM working_order_items WHERE order_id=? AND product_id=1`, id).Scan(&source.Quantity, &source.Allocated, &source.Picked, &source.PickVersion); err != nil {
				t.Fatal(err)
			}
			replacement := promotionWorkingLine(t, s, id, owner.ID, 3)
			expectedSourceStock := int64(22)
			if disposition == "restock" {
				expectedSourceStock = 24
			}
			if source.Quantity != 0 || source.Allocated != 0 || source.Picked != 0 || replacement.Quantity != 2 || replacement.Allocated != 2 || replacement.Picked != 0 || order.WorkingTotal != 1497 || order.Version != originalOrder.Version+1 || order.Finalized || testProduct(t, s, 1).Stock != expectedSourceStock || testProduct(t, s, 3).Stock != 8 {
				t.Fatalf("wrong atomic substitution: order=%+v source=%+v destination=%+v", order, source, replacement)
			}
			if !reflect.DeepEqual(receipt, placedSnapshot(t, s, id)) {
				t.Fatal("approval rewrote placed receipt")
			}
			var events int
			if err = s.db.QueryRow(`SELECT count(*) FROM order_events WHERE order_id=? AND action='substitute'`, id).Scan(&events); err != nil || events != 1 {
				t.Fatal(events, err)
			}
			before = fingerprintTest(t, s.db)
			if err = s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
				t.Fatal("approval replay", err)
			}
			changed := d
			changed.Action = "reject"
			if err = s.DecideSubstitution(id, owner.ID, owner.CSRF, changed); !errors.Is(err, ErrConflict) {
				t.Fatal("decision payload conflict", err)
			}
			changed = d
			changed.Key = token()
			if err = s.DecideSubstitution(id, owner.ID, owner.CSRF, changed); !errors.Is(err, ErrConflict) {
				t.Fatal("decision new-key conflict", err)
			}
			replay, err = s.SendSubstitution(g.Token, g.CSRF, preview.Command)
			if err != nil || replay.ID != p.ID || replay.Status != "approved" || !reflect.DeepEqual(replay.Preview, p.Preview) {
				t.Fatal("post-approval proposal recovery", replay, err)
			}
			substitutionAssertUnchanged(t, s, before)
			// Removed rows remain for history but cannot be a source of new goods.
			removed := preview.Command
			removed.Key = token()
			removed.Version = order.Version
			removed.PickVersion = source.PickVersion
			removed.Review = ""
			if _, err = s.PreviewSubstitution(g.Token, g.CSRF, removed); !errors.Is(err, ErrSubstitutionStale) {
				t.Fatal("removed source permitted", err)
			}
		})
	}
}
func TestSubstitutionRejectWithdrawPendingAndReviewBindings(t *testing.T) {
	for _, action := range []string{"reject", "withdraw"} {
		t.Run(action, func(t *testing.T) {
			s := newTestStore(t)
			owner, id, g := handheldFixture(t, s)
			p, d := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "writeoff")
			stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
			order := testOrder(t, s, id, owner.ID)
			next := substitutionPreviewTest(t, s, g, 1, 4, 1, "restock")
			if _, err := s.SendSubstitution(g.Token, g.CSRF, next.Command); !errors.Is(err, ErrSubstitutionPending) {
				t.Fatal("parallel pending source", err)
			}
			before := fingerprintTest(t, s.db)
			bad := d
			bad.Review = strings.Repeat("f", 64)
			if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, bad); !errors.Is(err, ErrSubstitutionStale) {
				t.Fatal(err)
			}
			bad = d
			bad.Binding = strings.Repeat("f", 64)
			if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, bad); !errors.Is(err, ErrSubstitutionStale) {
				t.Fatal(err)
			}
			if err := s.WithdrawSubstitution(g.Token, g.CSRF, p.ID, "bad"); !errors.Is(err, ErrSubstitutionStale) {
				t.Fatal(err)
			}
			substitutionAssertUnchanged(t, s, before)
			if action == "reject" {
				d.Action = "reject"
				if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
					t.Fatal(err)
				}
				before = fingerprintTest(t, s.db)
				if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.WithdrawSubstitution(g.Token, g.CSRF, p.ID, p.Review); err != nil {
					t.Fatal(err)
				}
				before = fingerprintTest(t, s.db)
				if err := s.WithdrawSubstitution(g.Token, g.CSRF, p.ID, p.Review); err != nil {
					t.Fatal(err)
				}
			}
			substitutionAssertUnchanged(t, s, before)
			if !reflect.DeepEqual(stock, migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)) || !reflect.DeepEqual(order, testOrder(t, s, id, owner.ID)) {
				t.Fatal("negative decision changed fulfillment")
			}
			if _, err := s.SendSubstitution(g.Token, g.CSRF, next.Command); err != nil {
				t.Fatal("terminal proposal blocked next proposal", err)
			}
			if action == "reject" {
				w, err := s.Substitutions(id, owner.ID, "", false)
				if err != nil {
					t.Fatal(err)
				}
				reuse := substitutionDecisionTest(t, s, owner, id, w.Proposals[0], "reject")
				reuse.Key = d.Key
				if err = s.DecideSubstitution(id, owner.ID, owner.CSRF, reuse); !errors.Is(err, ErrConflict) {
					t.Fatal("decision key reused across proposals", err)
				}
			}
		})
	}
}
func TestSubstitutionAuthBeforeValidationAndPracticeProhibition(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	p, d := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
	stranger := testSession(t, s, "")
	testExec(t, s, `UPDATE sessions SET manager_until=? WHERE id=?`, s.now().Add(time.Hour).Unix(), stranger.ID)
	before := fingerprintTest(t, s.db)
	for _, sid := range []string{"", stranger.ID, g.Token} {
		if _, err := s.Substitutions(id, sid, "", false); !errors.Is(err, ErrNotFound) {
			t.Fatal("foreign customer read", err)
		}
		if err := s.DecideSubstitution(id, sid, "", SubstitutionDecision{}); !errors.Is(err, ErrNotFound) {
			t.Fatal("foreign customer decision", err)
		}
	}
	for _, raw := range []string{"", owner.ID, stranger.ID} {
		if _, err := s.Substitutions(id, "", raw, true); !errors.Is(err, ErrHandheldAccess) {
			t.Fatal(err)
		}
		if _, err := s.PreviewSubstitution(raw, "", SubstitutionCommand{}); !errors.Is(err, ErrHandheldAccess) {
			t.Fatal(err)
		}
		if _, err := s.SendSubstitution(raw, "", p.Preview.Command); !errors.Is(err, ErrHandheldAccess) {
			t.Fatal(err)
		}
		if err := s.WithdrawSubstitution(raw, "", p.ID, p.Review); !errors.Is(err, ErrHandheldAccess) {
			t.Fatal(err)
		}
	}
	if err := s.DecideSubstitution(id, owner.ID, g.CSRF, d); !errors.Is(err, ErrMessageCSRF) {
		t.Fatal(err)
	}
	if _, err := s.PreviewSubstitution(g.Token, owner.CSRF, p.Preview.Command); !errors.Is(err, ErrHandheldCSRF) {
		t.Fatal(err)
	}
	if _, err := s.SendSubstitution(g.Token, owner.CSRF, p.Preview.Command); !errors.Is(err, ErrHandheldCSRF) {
		t.Fatal(err)
	}
	if err := s.WithdrawSubstitution(g.Token, owner.CSRF, p.ID, p.Review); !errors.Is(err, ErrHandheldCSRF) {
		t.Fatal(err)
	}
	substitutionAssertUnchanged(t, s, before)
	employee := employeeFixture(t, s)
	if err := s.SeedEmployeePractice(employee.Token, employee.CSRF); err != nil {
		t.Fatal(err)
	}
	q := employeeQueueTest(t, s, employee)
	practice := employeeClaimTest(t, s, employee, q.Queue[0].ID)
	practiceTask := handheldTaskTest(t, s, practice)
	practiceInput := substitutionInputTest(t, s, practice, practiceTask.Lines[0].ProductID, 4, 1, "restock")
	before = fingerprintTest(t, s.db)
	if _, err := s.Substitutions(0, "", practice.Token, true); !errors.Is(err, ErrEmployeePractice) {
		t.Fatal("practice workspace", err)
	}
	if _, err := s.PreviewSubstitution(practice.Token, practice.CSRF, practiceInput); !errors.Is(err, ErrEmployeePractice) {
		t.Fatal("practice preview", err)
	}
	if _, err := s.SendSubstitution(practice.Token, practice.CSRF, SubstitutionCommand{}); !errors.Is(err, ErrEmployeePractice) {
		t.Fatal("practice send", err)
	}
	substitutionAssertUnchanged(t, s, before)
}
func TestSubstitutionGrantLifetimePrecedesReplay(t *testing.T) {
	for _, state := range []string{"revoked", "expired", "reassigned", "released", "Ready", "owner-expired", "epoch"} {
		for _, decided := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/decided=%t", state, decided), func(t *testing.T) {
				s, clock, _ := newReservationStore(t)
				owner, id, g := handheldFixture(t, s)
				p, d := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
				if decided {
					if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
						t.Fatal(err)
					}
				}
				switch state {
				case "revoked":
					testExec(t, s, `UPDATE handheld_grants SET revoked=1`)
				case "expired":
					clock.at(g.Expires)
				case "reassigned":
					o := testOrder(t, s, id, owner.ID)
					if err := s.AssignShopper(id, owner.ID, false, ShopperCommand{Action: "reassign", Key: token(), Reason: "Reassign task for test", Version: o.Version, AssignmentVersion: o.Assignment.Version, ShopperID: 2}); err != nil {
						t.Fatal(err)
					}
				case "released":
					testExec(t, s, `UPDATE shopper_assignments SET state='cancelled',ended='2026-10-02 19:00 UTC',version=version+1 WHERE order_id=?`, id)
				case "Ready":
					testExec(t, s, `UPDATE orders SET status='Ready' WHERE id=?`, id)
				case "owner-expired":
					testExec(t, s, `UPDATE sessions SET expires=? WHERE id=?`, clock.now().Unix(), owner.ID)
				case "epoch":
					testExec(t, s, `UPDATE handheld_state SET epoch=?`, token())
				}
				before := fingerprintTest(t, s.db)
				if _, err := s.SendSubstitution(g.Token, g.CSRF, p.Preview.Command); !errors.Is(err, ErrHandheldAccess) {
					t.Fatal("dead grant replay", err)
				}
				if err := s.WithdrawSubstitution(g.Token, g.CSRF, p.ID, p.Review); !errors.Is(err, ErrHandheldAccess) {
					t.Fatal("dead grant withdrawal", err)
				}
				want := ErrSubstitutionStale
				if state == "owner-expired" {
					want = ErrNotFound
				}
				if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, d); !errors.Is(err, want) {
					t.Fatal("dead authority decision/replay", err)
				}
				if state != "owner-expired" {
					w, err := s.Substitutions(id, owner.ID, "", false)
					if err != nil || len(w.Proposals) != 1 || w.Proposals[0].CanDecide {
						t.Fatal(w, err)
					}
				}
				substitutionAssertUnchanged(t, s, before)
			})
		}
	}
}
func TestSubstitutionApprovalBindsEconomicAndRelevantPickState(t *testing.T) {
	for _, change := range []string{"source-pick", "destination-pick", "unrelated-pick", "source-quantity", "unrelated-allocation", "catalog-price", "catalog-name", "catalog-unit", "catalog-archive", "insufficient-stock", "sufficient-stock", "restock-capacity", "sale-boundary"} {
		t.Run(change, func(t *testing.T) {
			s, clock, _ := newReservationStore(t)
			owner, id, g := handheldFixture(t, s)
			replacement := int64(3)
			if change == "destination-pick" {
				replacement = 2
			}
			if change == "sale-boundary" {
				saveTestPromotion(t, s, 3, 399, clock.now().Unix(), clock.now().Unix()+60)
			}
			p, d := substitutionProposalTest(t, s, owner, id, g, 1, replacement, 1, "restock")
			want := ErrSubstitutionStale
			switch change {
			case "source-pick":
				substitutionPickProductTest(t, s, g, 1, 1)
			case "destination-pick":
				substitutionPickProductTest(t, s, g, 2, 1)
			case "unrelated-pick":
				substitutionPickProductTest(t, s, g, 2, 1)
				want = nil
			case "source-quantity":
				testExec(t, s, `UPDATE working_order_items SET quantity=1,allocated_quantity=1 WHERE order_id=? AND product_id=1`, id)
			case "unrelated-allocation":
				testExec(t, s, `UPDATE working_order_items SET quantity=2,allocated_quantity=2 WHERE order_id=? AND product_id=2`, id)
			case "catalog-price":
				testExec(t, s, `UPDATE products SET price=price+1,price_version=price_version+1 WHERE id=3`)
			case "catalog-name":
				testExec(t, s, `UPDATE products SET name='Changed replacement label' WHERE id=3`)
			case "catalog-unit":
				testExec(t, s, `UPDATE products SET sale_unit='g',price_basis=1000,quantity_step=1 WHERE id=3`)
				want = ErrUnavailable
			case "catalog-archive":
				testExec(t, s, `UPDATE products SET archived=1 WHERE id=3`)
				want = ErrUnavailable
			case "insufficient-stock":
				testExec(t, s, `UPDATE products SET stock=0,version=version+1 WHERE id=3`)
				want = ErrStock
			case "sufficient-stock":
				testExec(t, s, `UPDATE products SET stock=stock-1,version=version+1 WHERE id=3`)
				want = nil
			case "restock-capacity":
				testExec(t, s, `UPDATE products SET stock=10000 WHERE id=1`)
				want = ErrStockCapacity
			case "sale-boundary":
				clock.at(clock.now().Unix() + 60)
			}
			before := fingerprintTest(t, s.db)
			if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, d); !errors.Is(err, want) {
				t.Fatalf("%s got %v want %v", change, err, want)
			}
			if want != nil {
				substitutionAssertUnchanged(t, s, before)
				w, err := s.Substitutions(id, owner.ID, "", false)
				if err != nil || len(w.Proposals) != 1 || w.Proposals[0].State != "stale" || w.Proposals[0].CanDecide || w.Proposals[0].Problem == "" {
					t.Fatal("stale projection", w, err)
				}
			} else {
				if testOrder(t, s, id, owner.ID).WorkingTotal != p.Preview.ProposedTotal {
					t.Fatal("review total mismatch")
				}
				if change == "unrelated-pick" && promotionWorkingLine(t, s, id, owner.ID, 2).Picked != 1 {
					t.Fatal("unrelated pick lost")
				}
			}
		})
	}
}
func TestSubstitutionApprovalFailureRollsBackEveryWrite(t *testing.T) {
	for _, point := range []string{"stock", "source", "event", "decision"} {
		t.Run(point, func(t *testing.T) {
			s := newTestStore(t)
			owner, id, g := handheldFixture(t, s)
			_, d := substitutionProposalTest(t, s, owner, id, g, 1, 3, 2, "restock")
			triggers := map[string]string{
				"stock":    `CREATE TRIGGER fail_substitution BEFORE UPDATE ON products WHEN OLD.id=1 BEGIN SELECT RAISE(ABORT,'test stock return failure'); END`,
				"source":   `CREATE TRIGGER fail_substitution BEFORE UPDATE ON working_order_items WHEN OLD.product_id=1 BEGIN SELECT RAISE(ABORT,'test source failure'); END`,
				"event":    `CREATE TRIGGER fail_substitution BEFORE INSERT ON order_events WHEN NEW.action='substitute' BEGIN SELECT RAISE(ABORT,'test event failure'); END`,
				"decision": `CREATE TRIGGER fail_substitution BEFORE UPDATE ON substitution_proposals BEGIN SELECT RAISE(ABORT,'test decision failure'); END`,
			}
			testExec(t, s, triggers[point])
			before := fingerprintTest(t, s.db)
			if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err == nil {
				t.Fatal("injected failure accepted")
			}
			substitutionAssertUnchanged(t, s, before)
			testExec(t, s, `DROP TRIGGER fail_substitution`)
			if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
				t.Fatal("rollback prevented exact retry", err)
			}
		})
	}
}
func TestSubstitutionExpiredBasketHoldsOnlyReleaseOnApproval(t *testing.T) {
	s, clock, _ := newReservationStore(t)
	owner, id, g := handheldFixture(t, s)
	other := testSession(t, s, "")
	testCart(t, s, other.ID, 3, 10)
	clock.at(testBasket(t, s, other.ID).HoldUntil)
	before := fingerprintTest(t, s.db)
	p := substitutionPreviewTest(t, s, g, 1, 3, 2, "restock")
	if p.Available != 10 {
		t.Fatal(p.Available)
	}
	substitutionAssertUnchanged(t, s, before)
	saved, err := s.SendSubstitution(g.Token, g.CSRF, p.Command)
	if err != nil {
		t.Fatal(err)
	}
	if got := migrationQuerySnapshot(t, s.db, `SELECT stock FROM products WHERE id=3`); got[0][0] != int64(0) {
		t.Fatal("send released holds", got)
	}
	d := substitutionDecisionTest(t, s, owner, id, saved, "approve")
	if err = s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
		t.Fatal(err)
	}
	assertReservedStock(t, s, 3, 8, 0)
}
func TestSubstitutionExistingDestinationRetainsSnapshotAndCountBounds(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	substitutionPickProductTest(t, s, g, 2, 1)
	testExec(t, s, `UPDATE products SET price=999,price_version=price_version+1 WHERE id=2`)
	p, d := substitutionProposalTest(t, s, owner, id, g, 1, 2, 2, "writeoff")
	if p.Preview.ReplacementPrice != 299 || p.Preview.NewAmount != 598 || p.Preview.ReplacementQuantity != 1 || p.Preview.ReplacementPicked != 1 {
		t.Fatal(p.Preview)
	}
	if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
		t.Fatal(err)
	}
	dest := promotionWorkingLine(t, s, id, owner.ID, 2)
	if dest.Quantity != 3 || dest.Allocated != 3 || dest.Picked != 1 || dest.Price != 299 || dest.Subtotal != 897 {
		t.Fatal(dest)
	}
	s2 := newTestStore(t)
	_, _, g2 := handheldFixture(t, s2)
	testExec(t, s2, `UPDATE products SET stock=100 WHERE id=2`)
	tooMany := substitutionInputTest(t, s2, g2, 1, 2, 99, "restock")
	if _, err := s2.PreviewSubstitution(g2.Token, g2.CSRF, tooMany); !errors.Is(err, ErrInvalid) {
		t.Fatal("combined quantity above 99", err)
	}
}
func TestSubstitutionInvalidDraftsNeverWrite(t *testing.T) {
	s := newTestStore(t)
	_, _, g := handheldFixture(t, s)
	base := substitutionInputTest(t, s, g, 1, 3, 1, "restock")
	for _, mutate := range []func(*SubstitutionCommand){func(c *SubstitutionCommand) { c.Quantity = 0 }, func(c *SubstitutionCommand) { c.Quantity = 100 }, func(c *SubstitutionCommand) { c.LineID = 0 }, func(c *SubstitutionCommand) { c.PickVersion = 0 }, func(c *SubstitutionCommand) { c.Version = 0 }, func(c *SubstitutionCommand) { c.AssignmentVersion = 0 }, func(c *SubstitutionCommand) { c.ReplacementID = 0 }, func(c *SubstitutionCommand) { c.Disposition = "" }, func(c *SubstitutionCommand) { c.Note = "  " }, func(c *SubstitutionCommand) { c.Note = "bad\x00note" }, func(c *SubstitutionCommand) { c.Note = strings.Repeat("a", 501) }, func(c *SubstitutionCommand) { c.Key = "bad" }} {
		c := base
		mutate(&c)
		before := fingerprintTest(t, s.db)
		if _, err := s.PreviewSubstitution(g.Token, g.CSRF, c); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid preview", c, err)
		}
		if _, err := s.SendSubstitution(g.Token, g.CSRF, c); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid send", err)
		}
		substitutionAssertUnchanged(t, s, before)
	}
	before := fingerprintTest(t, s.db)
	if _, err := s.SendSubstitution(g.Token, g.CSRF, base); !errors.Is(err, ErrSubstitutionStale) {
		t.Fatal("unreviewed send", err)
	}
	base.ReplacementID = 1
	if _, err := s.PreviewSubstitution(g.Token, g.CSRF, base); !errors.Is(err, ErrSubstitutionStale) {
		t.Fatal("same product replacement", err)
	}
	substitutionAssertUnchanged(t, s, before)
	for _, delta := range []struct {
		old, new int64
		want     string
	}{{100, 1, "−$0.99"}, {100, 100, "$0.00"}, {1, 100, "+$0.99"}} {
		if got := (SubstitutionPreview{OldAmount: delta.old, NewAmount: delta.new}).AmountChange(); got != delta.want {
			t.Fatalf("delta %d→%d: %q", delta.old, delta.new, got)
		}
	}
}
func TestSubstitutionTwoConnectionsCompetingProposalsAndDecisions(t *testing.T) {
	for _, mode := range []string{"proposal", "same-approval", "conflicting-decision", "competing-stock"} {
		t.Run(mode, func(t *testing.T) {
			s, clock, path := newReservationStore(t)
			owner, id, g := handheldFixture(t, s)
			other := reservationStore(t, path, clock)
			begin := make(chan struct{})
			done := make(chan error, 2)
			if mode == "proposal" {
				a := substitutionPreviewTest(t, s, g, 1, 3, 1, "restock")
				b := substitutionPreviewTest(t, s, g, 1, 4, 1, "restock")
				for i, db := range []*Store{s, other} {
					c := a.Command
					if i == 1 {
						c = b.Command
					}
					go func(db *Store, c SubstitutionCommand) {
						<-begin
						_, err := db.SendSubstitution(g.Token, g.CSRF, c)
						done <- err
					}(db, c)
				}
			} else if mode == "competing-stock" {
				owner2 := testSession(t, s, "")
				testCart(t, s, owner2.ID, 1, 1)
				id2 := testCheckout(t, s, owner2.ID)
				testAssign(t, s, id2, owner2.ID, 2)
				g2 := connectHandheldFixture(t, s, owner2, id2)
				testExec(t, s, `UPDATE products SET stock=1 WHERE id=3`)
				_, a := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
				_, b := substitutionProposalTest(t, s, owner2, id2, g2, 1, 3, 1, "restock")
				go func() { <-begin; done <- s.DecideSubstitution(id, owner.ID, owner.CSRF, a) }()
				go func() { <-begin; done <- other.DecideSubstitution(id2, owner2.ID, owner2.CSRF, b) }()
			} else {
				_, a := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
				b := a
				if mode == "conflicting-decision" {
					b.Key = token()
					b.Action = "reject"
				}
				for i, db := range []*Store{s, other} {
					c := a
					if i == 1 {
						c = b
					}
					go func(db *Store, c SubstitutionDecision) {
						<-begin
						done <- db.DecideSubstitution(id, owner.ID, owner.CSRF, c)
					}(db, c)
				}
			}
			close(begin)
			wins, losses := 0, 0
			for i := 0; i < 2; i++ {
				err := <-done
				if err == nil {
					wins++
				} else {
					want := ErrConflict
					if mode == "proposal" {
						want = ErrSubstitutionPending
					}
					if mode == "competing-stock" {
						want = ErrStock
					}
					if !errors.Is(err, want) {
						t.Fatal(err)
					}
					losses++
				}
			}
			if mode == "same-approval" {
				if wins != 2 || losses != 0 {
					t.Fatal(wins, losses)
				}
			} else if wins != 1 || losses != 1 {
				t.Fatal(wins, losses)
			}
			if mode == "proposal" && testCount(t, s, "substitution_proposals") != 1 {
				t.Fatal("duplicate pending proposals")
			}
			if mode == "same-approval" || mode == "competing-stock" {
				var events int
				if err := s.db.QueryRow(`SELECT count(*) FROM order_events WHERE action='substitute'`).Scan(&events); err != nil || events != 1 {
					t.Fatal("double economic mutation", events, err)
				}
			}
		})
	}
}
func TestSubstitutionPendingBecomesStaleAndFreshProposalSupersedes(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	old, _ := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
	substitutionPickProductTest(t, s, g, 1, 1)
	next, _ := substitutionProposalTest(t, s, owner, id, g, 1, 4, 1, "writeoff")
	w, err := s.Substitutions(id, owner.ID, "", false)
	if err != nil || len(w.Proposals) != 2 || w.Proposals[0].ID != next.ID || w.Proposals[1].ID != old.ID || w.Proposals[1].Status != "stale" || w.Proposals[1].CanDecide {
		t.Fatal(w, err)
	}
}

func TestSubstitutionEmployeeContinuePreservesOnlySameTaskAuthority(t *testing.T) {
	for _, mode := range []string{"approve", "reject", "withdraw", "employee-revoked", "employee-expired", "owner-expired", "release-reclaim"} {
		t.Run(mode, func(t *testing.T) {
			s, clock, _ := newReservationStore(t)
			owner, id := pickingFixture(t, s)
			employee := employeeFixture(t, s)
			enrollEmployeeOrderTest(t, s, id)
			g := employeeClaimTest(t, s, employee, id)
			p, d := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
			next := employeeClaimTest(t, s, employee, id)
			if next.Token == g.Token {
				t.Fatal("fixture did not rotate grant")
			}
			w, err := s.Substitutions(id, owner.ID, "", false)
			if err != nil || len(w.Proposals) != 1 || !w.Proposals[0].CanDecide {
				t.Fatal("Continue made customer review stale", w, err)
			}
			phone, err := s.Substitutions(id, "", next.Token, true)
			if err != nil || !phone.Proposals[0].CanWithdraw {
				t.Fatal("Continue lost withdrawal authority", phone, err)
			}
			switch mode {
			case "employee-revoked":
				testExec(t, s, `UPDATE employee_sessions SET revoked=1 WHERE token_hash=?`, handheldHash(employee.Token))
			case "employee-expired":
				clock.at(employee.Expires)
			case "owner-expired":
				testExec(t, s, `UPDATE sessions SET expires=? WHERE id=?`, clock.now().Unix(), owner.ID)
			case "release-reclaim":
				o := testOrder(t, s, id, owner.ID)
				if err = s.EmployeeRelease(employee.Token, employee.CSRF, token(), id, o.Version); err != nil {
					t.Fatal(err)
				}
				next = employeeClaimTest(t, s, employee, id)
			}
			before := fingerprintTest(t, s.db)
			if _, err = s.SendSubstitution(g.Token, g.CSRF, p.Preview.Command); !errors.Is(err, ErrHandheldAccess) {
				t.Fatal("old device replay", err)
			}
			switch mode {
			case "approve":
				if err = s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
					t.Fatal(err)
				}
			case "reject":
				d.Action = "reject"
				if err = s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
					t.Fatal(err)
				}
			case "withdraw":
				if err = s.WithdrawSubstitution(next.Token, next.CSRF, p.ID, p.Review); err != nil {
					t.Fatal(err)
				}
			default:
				want := ErrSubstitutionStale
				if mode == "owner-expired" {
					want = ErrNotFound
				}
				if err = s.DecideSubstitution(id, owner.ID, owner.CSRF, d); !errors.Is(err, want) {
					t.Fatal("different/expired authority approved", err)
				}
				substitutionAssertUnchanged(t, s, before)
			}
		})
	}
}
func TestSubstitutionEconomicStalenessCanBeRejectedButNotApproved(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	p, d := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
	testExec(t, s, `UPDATE products SET price=price+1,price_version=price_version+1 WHERE id=3`)
	before := fingerprintTest(t, s.db)
	w, err := s.Substitutions(id, owner.ID, "", false)
	if err != nil || w.Proposals[0].CanDecide || !w.Proposals[0].CanReject {
		t.Fatal(w, err)
	}
	if err = s.DecideSubstitution(id, owner.ID, owner.CSRF, d); !errors.Is(err, ErrSubstitutionStale) {
		t.Fatal(err)
	}
	substitutionAssertUnchanged(t, s, before)
	d.Action = "reject"
	if err = s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
		t.Fatal(err)
	}
	w, err = s.Substitutions(id, owner.ID, "", false)
	if err != nil || w.Proposals[0].ID != p.ID || w.Proposals[0].Status != "rejected" {
		t.Fatal(w, err)
	}
}
func TestSubstitutionHistoryQuotaNeverDeletesOrPartiallyStalesHistory(t *testing.T) {
	for _, scope := range []string{"order", "global"} {
		t.Run(scope, func(t *testing.T) {
			s := newTestStore(t)
			owner, id, g := handheldFixture(t, s)
			p, _ := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
			if scope == "global" {
				if err := s.WithdrawSubstitution(g.Token, g.CSRF, p.ID, p.Review); err != nil {
					t.Fatal(err)
				}
			}
			limit := substitutionOrderLimit
			if scope == "global" {
				limit = substitutionGlobalLimit
			}
			testExec(t, s, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<?) INSERT INTO substitution_proposals(order_id,line_id,grant_id,assignment_id,assignment_version,epoch,command_key,command_hash,snapshot,status,created,decided) SELECT p.order_id,p.line_id,p.grant_id,p.assignment_id,p.assignment_version,p.epoch,printf('quota-copy-%020d',n.x),p.command_hash,p.snapshot,'withdrawn',p.created,p.created FROM substitution_proposals p,n WHERE p.id=?`, limit-1, p.ID)
			if scope == "global" {
				owner2 := testSession(t, s, "")
				testCart(t, s, owner2.ID, 1, 1)
				id2 := testCheckout(t, s, owner2.ID)
				testAssign(t, s, id2, owner2.ID, 2)
				g = connectHandheldFixture(t, s, owner2, id2)
			}
			if scope == "order" {
				substitutionPickProductTest(t, s, g, 1, 1)
			}
			preview := substitutionPreviewTest(t, s, g, 1, 3, 1, "restock")
			before := fingerprintTest(t, s.db)
			if _, err := s.SendSubstitution(g.Token, g.CSRF, preview.Command); !errors.Is(err, ErrSubstitutionLimit) {
				t.Fatal("quota failed", err)
			}
			substitutionAssertUnchanged(t, s, before)
			if got := testCount(t, s, "substitution_proposals"); got != limit {
				t.Fatal("history purged", got)
			}
		})
	}
}
func TestSubstitutionSchemaRetainsImmutableProposalShape(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	p, d := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
	before := fingerprintTest(t, s.db)
	for _, statement := range []string{
		`UPDATE substitution_proposals SET snapshot='{}'`,
		`UPDATE substitution_proposals SET order_id=999`,
		`UPDATE substitution_proposals SET line_id=999`,
		`UPDATE substitution_proposals SET grant_id=999`,
		`UPDATE substitution_proposals SET assignment_id=999`,
		`UPDATE substitution_proposals SET assignment_version=assignment_version+1`,
		`UPDATE substitution_proposals SET epoch=printf('%064d',1)`,
		`UPDATE substitution_proposals SET command_key='changed-history-key'`,
		`UPDATE substitution_proposals SET command_hash=printf('%064d',1)`,
		`UPDATE substitution_proposals SET created=created+1`,
		`UPDATE substitution_proposals SET status='pending'`,
		`DELETE FROM substitution_proposals`,
		`INSERT OR REPLACE INTO substitution_proposals SELECT * FROM substitution_proposals`,
	} {
		if _, err := s.db.Exec(statement); err == nil {
			t.Fatal("mutable retained history", statement)
		}
		substitutionAssertUnchanged(t, s, before)
	}
	if err := s.DecideSubstitution(id, owner.ID, owner.CSRF, d); err != nil {
		t.Fatal(err)
	}
	before = fingerprintTest(t, s.db)
	for _, statement := range []string{`UPDATE substitution_proposals SET status='rejected'`, `UPDATE substitution_proposals SET decision_key='new-decision-key-123'`, `UPDATE substitution_proposals SET decided=decided+1`, `DELETE FROM substitution_proposals`, `INSERT OR REPLACE INTO substitution_proposals SELECT * FROM substitution_proposals`} {
		if _, err := s.db.Exec(statement); err == nil {
			t.Fatal("terminal history changed", statement)
		}
		substitutionAssertUnchanged(t, s, before)
	}
	var snapshot string
	if err := s.db.QueryRow(`SELECT snapshot FROM substitution_proposals WHERE id=?`, p.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, column string
		value        any
	}{
		{"bad-status", "status", "invented"}, {"bad-assignment-version", "assignment_version", 0}, {"bad-epoch", "epoch", "short"}, {"bad-key", "command_key", "short"}, {"bad-hash", "command_hash", "short"}, {"oversize-snapshot", "snapshot", strings.Repeat("a", 16001)},
		{"empty-snapshot", "snapshot", ""}, {"invalid-json", "snapshot", "not json"}, {"array-snapshot", "snapshot", "[]"}, {"missing-command", "snapshot", "{}"}, {"null-command", "snapshot", `{"Command":null}`}, {"created-zero", "created", 0}, {"approved-no-decision", "status", "approved"}, {"rejected-no-decision", "status", "rejected"}, {"withdrawn-no-time", "status", "withdrawn"}, {"stale-no-time", "status", "stale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]any{"order_id": id, "line_id": p.LineID, "grant_id": p.GrantID, "assignment_id": p.AssignmentID, "assignment_version": p.AssignmentVersion, "epoch": p.Epoch, "command_key": token(), "command_hash": strings.Repeat("a", 64), "snapshot": snapshot, "status": "pending", "created": s.now().Unix()}
			values[tc.column] = tc.value
			if _, err := s.db.Exec(`INSERT INTO substitution_proposals(order_id,line_id,grant_id,assignment_id,assignment_version,epoch,command_key,command_hash,snapshot,status,created) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, values["order_id"], values["line_id"], values["grant_id"], values["assignment_id"], values["assignment_version"], values["epoch"], values["command_key"], values["command_hash"], values["snapshot"], values["status"], values["created"]); err == nil {
				t.Fatal("invalid history shape inserted")
			}
			substitutionAssertUnchanged(t, s, before)
		})
	}
}

func TestSubstitutionCountedOnlyAndResolvedLineGuards(t *testing.T) {
	for _, which := range []string{"source-unavailable", "source-cancelled", "destination-unavailable", "destination-cancelled", "source-weighted", "destination-weighted"} {
		t.Run(which, func(t *testing.T) {
			s := newTestStore(t)
			_, id, g := handheldFixture(t, s)
			source, replacement := int64(1), int64(2)
			if which == "source-weighted" {
				_, _, g, _ = handheldWeightFixture(t, s, 2000, 349, 500)
				task := handheldTaskTest(t, s, g)
				source = task.Lines[0].ProductID
				replacement = 3
			}
			if which == "destination-weighted" {
				replacement = weightedProduct(t, s, 2000, 349, 1).ID
			}
			column := "unavailable_quantity"
			if strings.Contains(which, "cancelled") {
				column = "cancelled_quantity"
			}
			if strings.Contains(which, "unavailable") || strings.Contains(which, "cancelled") {
				pid := source
				if strings.HasPrefix(which, "destination") {
					pid = replacement
				}
				testExec(t, s, `UPDATE working_order_items SET `+column+`=1 WHERE order_id=? AND product_id=?`, id, pid)
			}
			c := substitutionInputTest(t, s, g, source, replacement, 1, "restock")
			before := fingerprintTest(t, s.db)
			_, err := s.PreviewSubstitution(g.Token, g.CSRF, c)
			if err == nil {
				t.Fatal("unsupported/resolved line accepted")
			}
			if !substitutionProblem(err) {
				t.Fatal("unexpected storage error", err)
			}
			substitutionAssertUnchanged(t, s, before)
		})
	}
}
func TestSubstitutionBlockedApprovalRereadsCommittedState(t *testing.T) {
	for _, change := range []string{"revoke", "source-pick", "price", "stock"} {
		t.Run(change, func(t *testing.T) {
			s, clock, path := newReservationStore(t)
			owner, id, g := handheldFixture(t, s)
			_, d := substitutionProposalTest(t, s, owner, id, g, 1, 3, 1, "restock")
			customer := reservationStore(t, path, clock)
			tx, err := s.beginWrite()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			done := make(chan error, 1)
			go func() { done <- customer.DecideSubstitution(id, owner.ID, owner.CSRF, d) }()
			select {
			case err = <-done:
				t.Fatal("decision bypassed writer lock", err)
			case <-time.After(25 * time.Millisecond):
			}
			want := ErrSubstitutionStale
			switch change {
			case "revoke":
				_, err = tx.Exec(`UPDATE handheld_grants SET revoked=1 WHERE token_hash=?`, handheldHash(g.Token))
			case "source-pick":
				_, err = tx.Exec(`UPDATE working_order_items SET picked_quantity=1,pick_version=pick_version+1 WHERE order_id=? AND product_id=1`, id)
			case "price":
				_, err = tx.Exec(`UPDATE products SET price=price+1,price_version=price_version+1 WHERE id=3`)
			case "stock":
				_, err = tx.Exec(`UPDATE products SET stock=0,version=version+1 WHERE id=3`)
				want = ErrStock
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, want) {
				t.Fatal("stale pre-lock state accepted", change, err)
			}
			var pending, events int
			if err = s.db.QueryRow(`SELECT count(*) FROM substitution_proposals WHERE status='pending'`).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if err = s.db.QueryRow(`SELECT count(*) FROM order_events WHERE action='substitute'`).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if pending != 1 || events != 0 || promotionWorkingLine(t, s, id, owner.ID, 1).Quantity != 2 {
				t.Fatal("stale decision partially applied", pending, events)
			}
		})
	}
}
