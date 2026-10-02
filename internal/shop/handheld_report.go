package shop

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// HandheldReport asks for a manager's review, never marks an item unavailable,
// changes its fulfillment outcome or supplies authority to release a hold.
type HandheldReport struct {
	LineID, AssignmentVersion, Version, PickVersion int64
	Kind, Note, Key                                 string
}

type HandheldReportResult struct {
	LineID, Version, PickVersion int64
	Replayed                     bool
}

func (s *Store) ReportHandheldItem(raw, csrf string, c HandheldReport) (HandheldReportResult, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return HandheldReportResult{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	g, err := handheldGrantTx(tx, raw, csrf, true, now)
	if err != nil {
		return HandheldReportResult{}, err
	}
	c.Note = strings.TrimSpace(c.Note)
	if c.LineID < 1 || c.AssignmentVersion < 1 || c.Version < 1 || c.PickVersion < 1 || len(c.Key) < 16 || len(c.Key) > 100 || !validHandheldNote(c.Note) {
		return HandheldReportResult{}, ErrInvalid
	}
	label := ""
	switch c.Kind {
	case "unavailable":
		label = "item not found"
	case "damaged":
		label = "item damaged"
	case "weight_stock":
		label = "weight or stock needs review"
	default:
		return HandheldReportResult{}, ErrInvalid
	}
	hash := handheldPayload([]any{"handheld-report-v1", g.ID, g.AssignmentID, c})
	key := "handheld-report:" + c.Key
	out := HandheldReportResult{}
	var prior string
	err = tx.QueryRow(`SELECT command_hash,line_id,order_version,pick_version FROM handheld_pick_commands WHERE grant_id=? AND command_key=?`, g.ID, key).Scan(&prior, &out.LineID, &out.Version, &out.PickVersion)
	if err == nil {
		if prior != hash {
			return HandheldReportResult{}, ErrConflict
		}
		out.Replayed = true
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if c.AssignmentVersion != g.AssignmentVersion || c.Version != g.Version {
		return out, ErrConflict
	}
	var name string
	var picked, pickVersion int64
	err = tx.QueryRow(`SELECT name,picked_quantity,pick_version FROM working_order_items WHERE id=? AND order_id=? AND quantity>0`, c.LineID, g.OrderID).Scan(&name, &picked, &pickVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrConflict
	}
	if err != nil {
		return out, err
	}
	if pickVersion != c.PickVersion {
		return out, ErrConflict
	}
	// A previous manager's reason and opening timestamp are private and remain
	// intact. A new hold uses a bounded generic reason; the note lives only in
	// the internal event. No private reason is projected into the phone result.
	holdReason := "Paired shopper requested manager review: " + label
	if _, err = tx.Exec(`UPDATE orders SET attention_since=CASE WHEN attention_reason='' THEN ? ELSE attention_since END,attention_reason=CASE WHEN attention_reason='' THEN ? ELSE attention_reason END,order_version=order_version+1 WHERE id=?`, now, holdReason, g.OrderID); err != nil {
		return out, err
	}
	reason := "Paired shopper requested manager review: " + label
	details := fmt.Sprintf("Working line %d (%s): %s. Stock, picked quantity, measurement, requested receipt and totals unchanged.", c.LineID, name, label)
	if c.Note != "" {
		details += " Shopper note: " + c.Note
	}
	result, err := tx.Exec(`INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details,visibility) VALUES(?,?,?,'report',?,?,'internal')`, g.OrderID, fmt.Sprintf("handheld-report:%d:%s", g.ID, c.Key), hash, reason, details)
	if err != nil {
		return out, err
	}
	eventID, err := result.LastInsertId()
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(`INSERT INTO order_event_actors(event_id,actor,source,grant_id,assignment_id,shopper_id,shopper_name) VALUES(?,'worker','manual',?,?,?,?)`, eventID, g.ID, g.AssignmentID, g.ShopperID, g.ShopperName); err != nil {
		return out, err
	}
	out = HandheldReportResult{LineID: c.LineID, Version: c.Version + 1, PickVersion: c.PickVersion}
	if _, err = tx.Exec(`INSERT INTO handheld_pick_commands(grant_id,command_key,command_hash,event_id,line_id,picked,order_version,pick_version) VALUES(?,?,?,?,?,?,?,?)`, g.ID, key, hash, eventID, out.LineID, picked, out.Version, out.PickVersion); err != nil {
		return out, err
	}
	if _, err = tx.Exec(`UPDATE handheld_grants SET last_recorded=? WHERE id=?`, now, g.ID); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
