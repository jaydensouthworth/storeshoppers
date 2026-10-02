package shop

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type LineWorkflow struct {
	Line         WorkingOrderItem
	View         View
	PickerActive bool
}

func lineWorkflow(line WorkingOrderItem, view View) LineWorkflow {
	active := view.ProductPicker != nil && view.ProductPicker.Context == "sub" && view.ProductPicker.SourceLineID == line.LineID
	if view.ProductPicker == nil || view.ProductPicker.Context != "sub" || view.ProductPicker.SourceLineID != line.LineID {
		view.ProductPicker = &ProductPicker{ID: fmt.Sprintf("order-picker-sub-%d", line.LineID), ActionURL: fmt.Sprintf("/manager/orders/%d/products", view.Order.ID), Context: "sub", SourceLineID: line.LineID, CatalogQuote: view.OrderCatalogQuote, OrderVersion: view.Order.Version}
	}
	return LineWorkflow{Line: line, View: view, PickerActive: active}
}

// MarkWorkingLinePicked is one idempotent command: a first pick starts Placed
// and records the absolute count atomically. It never alters stock or receipts.
func (s *Store) MarkWorkingLinePicked(orderID, lineID, picked, pickVersion, orderVersion int64, key, sid string, all bool) error {
	if picked < 0 || picked > 99 || pickVersion < 1 || orderVersion < 1 || len(key) < 16 || len(key) > 150 {
		return ErrInvalid
	}
	payload, _ := json.Marshal([]any{"mark-picked", lineID, picked, pickVersion, orderVersion, key})
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	var currentVersion int64
	err = tx.QueryRow(`SELECT status,order_version FROM orders WHERE id=? AND (? OR session_id=?)`, orderID, all, sid).Scan(&status, &currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var prior string
	err = tx.QueryRow(`SELECT command_hash FROM order_events WHERE order_id=? AND command_key=?`, orderID, key).Scan(&prior)
	if err == nil {
		if prior == hash {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if status != "Placed" && status != "Picking" {
		return ErrTerminal
	}
	if currentVersion != orderVersion {
		return ErrConflict
	}
	var quantity, currentPickVersion, before, productID int64
	err = tx.QueryRow(`SELECT product_id,quantity,picked_quantity,pick_version FROM working_order_items WHERE id=? AND order_id=? AND quantity>0 AND sale_unit='each'`, lineID, orderID).Scan(&productID, &quantity, &before, &currentPickVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if currentPickVersion != pickVersion {
		return ErrConflict
	}
	if picked > quantity {
		return ErrInvalid
	}
	if _, err = tx.Exec(`UPDATE working_order_items SET picked_quantity=?,pick_version=pick_version+1 WHERE id=?`, picked, lineID); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE orders SET status='Picking',order_version=order_version+1 WHERE id=?`, orderID); err != nil {
		return err
	}
	details := fmt.Sprintf("Working line %d, product %d: picked %d → %d of %d; status %s → Picking", lineID, productID, before, picked, quantity, status)
	if _, err = tx.Exec(`INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details) VALUES(?,?,?,'pick','Manager saved picked count',?)`, orderID, key, hash, details); err != nil {
		return err
	}
	return tx.Commit()
}
func routineOrderReason(action string, quantity int64, reason string) string {
	reason = strings.TrimSpace(reason)
	if reason != "" {
		if (action == "set" || action == "substitute") && utf8.RuneCountInString(reason) < 3 {
			return "Manager note: " + reason
		}
		return reason
	}
	if action == "set" {
		if quantity == 0 {
			return "Manager removed item from working list"
		}
		return "Manager changed working-list quantity"
	}
	if action == "substitute" {
		return "Manager substituted working-list item"
	}
	return reason
}

// Mutations never mint a replacement browser identity. A stale page after a
// reset/expiry must be explicitly refreshed, even after repeated rejected clicks.
func (s *Store) existingFormSession(id string) (Session, error) {
	var session Session
	err := s.db.QueryRow(`SELECT id,csrf,checkout_key,revision,manager_until FROM sessions WHERE id=? AND expires>?`, id, s.now().Unix()).Scan(&session.ID, &session.CSRF, &session.CheckoutKey, &session.Revision, &session.ManagerUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return session, ErrNotFound
	}
	return session, err
}
