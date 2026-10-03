package shop

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// HandheldWeight is an explicit measured-grams command. Code recognition only
// establishes the selected product's identity; no scan supplies Actual.
type HandheldWeight struct {
	HandheldScan
	Actual                         int64
	Key, Disposition, Review, Note string
}

type HandheldWeightPreview struct {
	Command                                                                            HandheldWeight
	Recognition                                                                        HandheldRecognition
	Name                                                                               string
	Target, Allocated, Picked, Price, OldSubtotal, NewSubtotal, PriceDelta, StockDelta int64
	WorkingTotal, ProposedWorkingTotal                                                 int64
}

type HandheldWeightResult struct {
	LineID, Actual, Version, PickVersion int64
	Replayed                             bool
}

func normalizeHandheldWeight(c HandheldWeight) (HandheldWeight, error) {
	var err error
	c.HandheldScan, err = normalizeHandheldScan(c.HandheldScan)
	if err != nil {
		return c, err
	}
	c.Note = strings.TrimSpace(c.Note)
	if c.Actual < 0 || c.Actual > maxGrams || len(c.Key) < 16 || len(c.Key) > 100 || !validHandheldNote(c.Note) {
		return c, ErrInvalid
	}
	if c.Disposition != "" && c.Disposition != "restock" && c.Disposition != "writeoff" {
		return c, ErrInvalid
	}
	return c, nil
}

func validHandheldNote(note string) bool {
	return utf8.ValidString(note) && utf8.RuneCountInString(note) <= 240 && strings.IndexFunc(note, unicode.IsControl) < 0
}

func handheldWeightCommand(c HandheldWeight, g handheldGrant) WeightCommand {
	reason := c.Note
	if reason == "" {
		reason = "Paired shopper confirmed actual grams from the scale"
	}
	return WeightCommand{LineID: c.LineID, PickVersion: c.PickVersion, OrderVersion: c.Version, Actual: c.Actual, Key: fmt.Sprintf("handheld-weight:%d:%s", g.ID, c.Key), Reason: reason, Disposition: c.Disposition}
}

// prepareHandheldWeightTx binds the phone's grant, assignment, code, source,
// proposed grams, disposition, note, versions and displayed totals to one review.
// It reads only: preview neither expires basket holds nor reserves stock.
func prepareHandheldWeightTx(tx *sql.Tx, g handheldGrant, c HandheldWeight) (HandheldWeightPreview, WeightPreview, error) {
	out := HandheldWeightPreview{Command: c}
	r, err := recognizeHandheldTx(tx, g, c.HandheldScan)
	if err != nil {
		return out, WeightPreview{}, err
	}
	if !r.CanMeasure {
		return out, WeightPreview{}, ErrInvalid
	}
	p, err := prepareWeight(tx, g.OrderID, g.Owner, false, handheldWeightCommand(c, g))
	if err != nil {
		return out, p, err
	}
	items, err := workingOrderItems(tx, g.OrderID)
	if err != nil {
		return out, p, err
	}
	var current, proposed Order
	if err = summarizeWorking(&current, items); err != nil {
		return out, p, err
	}
	for i := range items {
		if items[i].LineID == c.LineID {
			items[i].Allocated, items[i].Picked, items[i].Measured = c.Actual, c.Actual, true
			items[i].Subtotal = p.NewSubtotal
		}
	}
	if err = summarizeWorking(&proposed, items); err != nil {
		return out, p, err
	}
	out = HandheldWeightPreview{Command: c, Recognition: r, Name: p.Name, Target: p.Target, Allocated: p.Allocated, Picked: p.Picked, Price: p.Price, OldSubtotal: p.OldSubtotal, NewSubtotal: p.NewSubtotal, PriceDelta: p.PriceDelta, StockDelta: p.StockDelta, WorkingTotal: current.WorkingTotal, ProposedWorkingTotal: proposed.WorkingTotal}
	out.Command.Review = ""
	out.Command.Review = handheldPayload([]any{"handheld-weight-review-v1", g.ID, g.AssignmentID, g.AssignmentVersion, out})
	return out, p, nil
}

func (s *Store) PreviewHandheldWeight(raw, csrf string, c HandheldWeight) (HandheldWeightPreview, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return HandheldWeightPreview{}, err
	}
	defer tx.Rollback()
	g, err := handheldGrantTx(tx, raw, csrf, true, s.now().Unix())
	if err != nil {
		return HandheldWeightPreview{}, err
	}
	c, err = normalizeHandheldWeight(c)
	if err != nil {
		return HandheldWeightPreview{}, err
	}
	out, _, err := prepareHandheldWeightTx(tx, g, c)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// ConfirmHandheldWeight owns authorization, replay and the shared measurement
// mutation in one immediate transaction. The successful grams and versions are
// persisted separately from current line state so retries remain truthful after
// a subsequent correction. A revoked or ended grant cannot replay old commands.
func (s *Store) ConfirmHandheldWeight(raw, csrf string, c HandheldWeight) (HandheldWeightResult, error) {
	return s.confirmHandheldWeight(raw, csrf, c, nil)
}
func (s *Store) confirmHandheldWeight(raw, csrf string, c HandheldWeight, activity *HandheldSaved) (HandheldWeightResult, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return HandheldWeightResult{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	g, err := handheldGrantTx(tx, raw, csrf, true, now)
	if err != nil {
		return HandheldWeightResult{}, err
	}
	c, err = normalizeHandheldWeight(c)
	if err != nil {
		return HandheldWeightResult{}, err
	}
	if len(c.Review) != 64 {
		return HandheldWeightResult{}, ErrWeightReview
	}
	hash := handheldPayload([]any{"handheld-weight-v1", g.ID, g.AssignmentID, c})
	key := "handheld-weight:" + c.Key
	var prior string
	out := HandheldWeightResult{}
	err = tx.QueryRow(`SELECT command_hash,line_id,picked,order_version,pick_version FROM handheld_pick_commands WHERE grant_id=? AND command_key=?`, g.ID, key).Scan(&prior, &out.LineID, &out.Actual, &out.Version, &out.PickVersion)
	if err == nil {
		if prior != hash {
			return HandheldWeightResult{}, ErrConflict
		}
		out.Replayed = true
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	preview, prepared, err := prepareHandheldWeightTx(tx, g, c)
	if err != nil {
		return out, err
	}
	if preview.Command.Review != c.Review {
		return out, ErrWeightReview
	}
	var wasComplete bool
	if err = tx.QueryRow(`SELECT measurement_confirmed=1 AND unavailable_quantity=0 AND cancelled_quantity=0 FROM working_order_items WHERE id=? AND order_id=?`, c.LineID, g.OrderID).Scan(&wasComplete); err != nil {
		return out, err
	}
	actor := pickActor{Kind: "worker", Source: c.Source, ShopperName: g.ShopperName, GrantID: g.ID, AssignmentID: g.AssignmentID, ShopperID: g.ShopperID}
	saved, err := confirmWeightTx(tx, g.OrderID, handheldWeightCommand(c, g), prepared, hash, actor, now)
	if err != nil {
		return out, err
	}
	out = HandheldWeightResult{LineID: c.LineID, Actual: c.Actual, Version: saved.Version, PickVersion: saved.PickVersion}
	if _, err = tx.Exec(`INSERT INTO handheld_pick_commands(grant_id,command_key,command_hash,event_id,line_id,picked,order_version,pick_version) VALUES(?,?,?,?,?,?,?,?)`, g.ID, key, hash, saved.EventID, out.LineID, out.Actual, out.Version, out.PickVersion); err != nil {
		return out, err
	}
	if _, err = tx.Exec(`UPDATE handheld_grants SET last_recorded=? WHERE id=?`, now, g.ID); err != nil {
		return out, err
	}
	if activity != nil {
		activity.LineDelta = completeDelta(wasComplete, true)
	}
	return out, tx.Commit()
}
