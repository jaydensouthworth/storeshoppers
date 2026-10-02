package shop

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type WeightCommand struct {
	LineID, PickVersion, OrderVersion, Actual int64
	Key, Reason, Disposition, Review          string
}
type WeightPreview struct {
	Command                                                                            WeightCommand
	Name                                                                               string
	Target, Allocated, Picked, Price, OldSubtotal, NewSubtotal, PriceDelta, StockDelta int64
}

func measurementReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "Manager confirmed scale reading"
	}
	if utf8.RuneCountInString(reason) < 3 {
		return "Manager note: " + reason
	}
	return reason
}

func validWeightCommand(c WeightCommand) error {
	if c.LineID < 1 || c.PickVersion < 1 || c.OrderVersion < 1 || c.Actual < 0 || c.Actual > maxGrams || len(c.Key) < 16 || len(c.Key) > 150 || !utf8.ValidString(c.Reason) || utf8.RuneCountInString(c.Reason) < 3 || utf8.RuneCountInString(c.Reason) > 240 || strings.IndexFunc(c.Reason, unicode.IsControl) >= 0 {
		return ErrInvalid
	}
	if c.Disposition != "" && c.Disposition != "restock" && c.Disposition != "writeoff" {
		return ErrInvalid
	}
	return nil
}
func weightHash(v any) string {
	encoded, _ := json.Marshal(v)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// prepareWeight reads one coherent working snapshot. Its review token binds the
// displayed amount and allocation to the exact command and optimistic versions.
// It is not authorization: scope, manager access and CSRF remain independent.
func prepareWeight(tx *sql.Tx, orderID int64, sid string, all bool, c WeightCommand) (WeightPreview, error) {
	p := WeightPreview{Command: c}
	var status string
	var version int64
	err := tx.QueryRow(`SELECT status,order_version FROM orders WHERE id=? AND (? OR session_id=?)`, orderID, all, sid).Scan(&status, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if status != "Placed" && status != "Picking" {
		return p, ErrTerminal
	}
	if version != c.OrderVersion {
		return p, ErrConflict
	}
	var pickVersion, basis int64
	err = tx.QueryRow(`SELECT name,quantity,allocated_quantity,picked_quantity,price,price_basis,pick_version FROM working_order_items WHERE id=? AND order_id=? AND quantity>0 AND sale_unit='g'`, c.LineID, orderID).Scan(&p.Name, &p.Target, &p.Allocated, &p.Picked, &p.Price, &basis, &pickVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if pickVersion != c.PickVersion {
		return p, ErrConflict
	}
	p.StockDelta = c.Actual - p.Allocated
	if p.StockDelta < 0 && c.Disposition == "" {
		return p, ErrPickedDisposition
	}
	p.OldSubtotal, err = lineAmount(p.Allocated, p.Price, basis, "g")
	if err != nil {
		return p, err
	}
	p.NewSubtotal, err = lineAmount(c.Actual, p.Price, basis, "g")
	if err != nil {
		return p, err
	}
	p.PriceDelta = p.NewSubtotal - p.OldSubtotal
	p.Command.Review = ""
	p.Command.Review = weightHash([]any{"weight-review-v1", orderID, p})
	return p, nil
}

// PreviewWeight does not reserve stock, change picking state, or renew holds.
func (s *Store) PreviewWeight(orderID int64, sid string, all bool, c WeightCommand) (WeightPreview, error) {
	c.Reason = measurementReason(c.Reason)
	if err := validWeightCommand(c); err != nil {
		return WeightPreview{}, err
	}
	tx, err := s.beginWrite()
	if err != nil {
		return WeightPreview{}, err
	}
	defer tx.Rollback()
	return prepareWeight(tx, orderID, sid, all, c)
}

// ConfirmWeight accepts actual grams and adjusts only the allocation delta in
// the same immediate transaction as measurement, order version and scoped fulfillment audit.
// The requested receipt and snapped rate are never updated.
func (s *Store) ConfirmWeight(orderID int64, sid string, all bool, c WeightCommand) error {
	c.Reason = measurementReason(c.Reason)
	if err := validWeightCommand(c); err != nil {
		return err
	}
	if len(c.Review) != 64 {
		return ErrWeightReview
	}
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int64
	err = tx.QueryRow(`SELECT id FROM orders WHERE id=? AND (? OR session_id=?)`, orderID, all, sid).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	hash := weightHash(c)
	var prior string
	err = tx.QueryRow(`SELECT command_hash FROM order_events WHERE order_id=? AND command_key=?`, orderID, c.Key).Scan(&prior)
	if err == nil {
		if prior == hash {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	p, err := prepareWeight(tx, orderID, sid, all, c)
	if err != nil {
		return err
	}
	if p.Command.Review != c.Review {
		return ErrWeightReview
	}
	// A held basket may expire between review and confirmation. Reconcile it
	// inside this transaction so a failed measurement rolls everything back.
	if err = expireHolds(tx, s.now().Unix()); err != nil {
		return err
	}
	var pid int64
	if err = tx.QueryRow(`SELECT product_id FROM working_order_items WHERE id=? AND order_id=?`, c.LineID, orderID).Scan(&pid); err != nil {
		return err
	}
	if p.StockDelta > 0 {
		result, e := tx.Exec(`UPDATE products SET stock=stock-?,version=version+1 WHERE id=? AND stock>=?`, p.StockDelta, pid, p.StockDelta)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStock
		}
	} else if p.StockDelta < 0 {
		if err = disposeOrderStock(tx, pid, -p.StockDelta, c.Disposition); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE working_order_items SET allocated_quantity=?,picked_quantity=?,measurement_confirmed=1,pick_version=pick_version+1 WHERE id=?`, c.Actual, c.Actual, c.LineID); err != nil {
		return err
	}
	items, err := workingOrderItems(tx, orderID)
	if err != nil {
		return err
	}
	var order Order
	if err = summarizeWorking(&order, items); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE orders SET status='Picking',order_version=order_version+1 WHERE id=?`, orderID); err != nil {
		return err
	}
	details := fmt.Sprintf("%s: target %d g; accepted allocation %d → %d g; actual measured %d g at %s/kg; line amount %s → %s; stock delta %d g; reduction disposition %s", p.Name, p.Target, p.Allocated, c.Actual, c.Actual, Money(p.Price), Money(p.OldSubtotal), Money(p.NewSubtotal), p.StockDelta, c.Disposition)
	if _, err = tx.Exec(`INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details) VALUES(?,?,?,'measure',?,?)`, orderID, c.Key, hash, c.Reason, details); err != nil {
		return err
	}
	return tx.Commit()
}
