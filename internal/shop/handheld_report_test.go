package shop

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func handheldReportInput(t *testing.T, s *Store, g HandheldSession, kind, note string) HandheldReport {
	t.Helper()
	task := handheldTaskTest(t, s, g)
	line := task.Lines[0]
	return HandheldReport{LineID: line.LineID, AssignmentVersion: task.Assignment.Version, Version: task.Version, PickVersion: line.PickVersion, Kind: kind, Note: note, Key: token()}
}

func TestHandheldReportReviewHoldPrivacyAndReplay(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, kind := range []string{"unavailable", "damaged", "weight_stock"} {
			t.Run(fmt.Sprintf("%t-%s", existing, kind), func(t *testing.T) {
				s := newTestStore(t)
				owner, id, g, _ := handheldWeightFixture(t, s, 1000, 349, 500)
				if existing {
					attentionApply(t, s, id, owner.ID, "hold", "PRIVATE_MANAGER_REASON")
				}
				before := attentionManagerOrder(t, s, id, owner.ID)
				stock := migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)
				receipt := placedSnapshot(t, s, id)
				// Deliberately no successful recognition, and even an archived item
				// can need a report. This path has no code/label precondition.
				testExec(t, s, `UPDATE products SET archived=1 WHERE id=?`, before.WorkingItems[0].ProductID)
				c := handheldReportInput(t, s, g, kind, "  PRIVATE_WORKER_NOTE  ")
				saved, err := s.ReportHandheldItem(g.Token, g.CSRF, c)
				if err != nil || saved.Version != c.Version+1 || saved.PickVersion != c.PickVersion || saved.Replayed {
					t.Fatal(saved, err)
				}
				after := attentionManagerOrder(t, s, id, owner.ID)
				if !after.Held || after.AttentionReason == "" || after.Status != before.Status || after.WorkingTotal != before.WorkingTotal || after.PickedTotal != before.PickedTotal || after.FinalTotal != before.FinalTotal || after.Finalized != before.Finalized || !reflect.DeepEqual(before.WorkingItems, after.WorkingItems) || !reflect.DeepEqual(receipt, placedSnapshot(t, s, id)) || !reflect.DeepEqual(stock, migrationQuerySnapshot(t, s.db, `SELECT id,stock,version FROM products ORDER BY id`)) {
					t.Fatalf("report altered fulfillment %+v", after)
				}
				if existing && (after.AttentionReason != before.AttentionReason || after.AttentionSince != before.AttentionSince) {
					t.Fatal("report replaced private manager hold")
				}
				if !strings.Contains(fmt.Sprintf("%+v", after.Events), "PRIVATE_WORKER_NOTE") {
					t.Fatal("manager lost report note")
				}
				customer := testOrder(t, s, id, owner.ID)
				phone := handheldTaskTest(t, s, g)
				for _, projection := range []any{customer, phone, saved} {
					if text := fmt.Sprintf("%+v", projection); strings.Contains(text, "PRIVATE_WORKER_NOTE") || strings.Contains(text, "PRIVATE_MANAGER_REASON") {
						t.Fatal("private report/hold leaked", text)
					}
				}
				var actor, source, name, visibility string
				if err = s.db.QueryRow(`SELECT a.actor,a.source,a.shopper_name,e.visibility FROM order_event_actors a JOIN order_events e ON e.id=a.event_id WHERE e.action='report'`).Scan(&actor, &source, &name, &visibility); err != nil || actor != "worker" || source != "manual" || name != "Avery Morgan" || visibility != "internal" {
					t.Fatal("report attribution", actor, source, name, visibility, err)
				}
				attentionApply(t, s, id, owner.ID, "release", "")
				fingerprint := fingerprintTest(t, s.db)
				replayed, err := s.ReportHandheldItem(g.Token, g.CSRF, c)
				if err != nil || !replayed.Replayed || replayed.Version != saved.Version || fingerprintTest(t, s.db) != fingerprint || attentionManagerOrder(t, s, id, owner.ID).Held {
					t.Fatal("retry reopened released hold", replayed, err)
				}
				c.Note = "Changed payload"
				if _, err = s.ReportHandheldItem(g.Token, g.CSRF, c); !errors.Is(err, ErrConflict) {
					t.Fatal("changed replay", err)
				}
			})
		}
	}
}

func TestHandheldReportValidationScopeAndAtomicAudit(t *testing.T) {
	s := newTestStore(t)
	owner, id, g := handheldFixture(t, s)
	_, foreignID := pickingFixture(t, s)
	var foreignLine int64
	if err := s.db.QueryRow(`SELECT id FROM working_order_items WHERE order_id=? LIMIT 1`, foreignID).Scan(&foreignLine); err != nil {
		t.Fatal(err)
	}
	c := handheldReportInput(t, s, g, "unavailable", "")
	for _, tc := range []struct {
		edit func(*HandheldReport)
		want error
	}{
		{func(c *HandheldReport) { c.Kind = "cancel" }, ErrInvalid},
		{func(c *HandheldReport) { c.Note = strings.Repeat("界", 241) }, ErrInvalid},
		{func(c *HandheldReport) { c.Note = "bad\ncontrol" }, ErrInvalid},
		{func(c *HandheldReport) { c.Note = "\xff" }, ErrInvalid},
		{func(c *HandheldReport) { c.Version++ }, ErrConflict},
		{func(c *HandheldReport) { c.PickVersion++ }, ErrConflict},
		{func(c *HandheldReport) { c.AssignmentVersion++ }, ErrConflict},
		{func(c *HandheldReport) { c.LineID = foreignLine }, ErrConflict},
		{func(c *HandheldReport) { c.Key = "short" }, ErrInvalid},
	} {
		bad := c
		tc.edit(&bad)
		before := fingerprintTest(t, s.db)
		if _, err := s.ReportHandheldItem(g.Token, g.CSRF, bad); !errors.Is(err, tc.want) {
			t.Fatal("invalid report", err, tc.want)
		}
		if before != fingerprintTest(t, s.db) {
			t.Fatal("invalid report changed data")
		}
	}
	if _, err := s.ReportHandheldItem(g.Token, "bad", c); !errors.Is(err, ErrHandheldCSRF) {
		t.Fatal(err)
	}
	for _, table := range []string{"order_events", "order_event_actors", "handheld_pick_commands"} {
		testExec(t, s, fmt.Sprintf(`CREATE TRIGGER reject_report BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT,'report audit failure'); END`, table))
		before := fingerprintTest(t, s.db)
		if _, err := s.ReportHandheldItem(g.Token, g.CSRF, c); err == nil {
			t.Fatal("ignored audit failure", table)
		}
		if before != fingerprintTest(t, s.db) {
			t.Fatal("partial report after audit failure", table)
		}
		testExec(t, s, `DROP TRIGGER reject_report`)
	}
	if _, err := s.ReportHandheldItem(g.Token, g.CSRF, c); err != nil {
		t.Fatal(err)
	}
	attentionApply(t, s, id, owner.ID, "release", "")
	if err := s.DisconnectHandheld(g.Token, g.CSRF); err != nil {
		t.Fatal(err)
	}
	before := fingerprintTest(t, s.db)
	for _, input := range []HandheldReport{c, {}} {
		if _, err := s.ReportHandheldItem(g.Token, g.CSRF, input); !errors.Is(err, ErrHandheldAccess) {
			t.Fatal("revocation before validation/replay", err)
		}
	}
	if before != fingerprintTest(t, s.db) {
		t.Fatal("disconnected report mutated")
	}
}
