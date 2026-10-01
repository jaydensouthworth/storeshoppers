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

type OrderCommand struct {
	Action, Key, Reason, Disposition, Remainder, CatalogQuote string
	Version, ProductID, ReplacementID, Quantity               int64
}

type orderQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}

func workingOrderItems(q orderQuerier, id int64) ([]WorkingOrderItem, error) {
	rows, err := q.Query(`SELECT id,product_id,name,price,quantity,picked_quantity,pick_version,sku,sale_unit,price_basis,quantity_step,unavailable_quantity,cancelled_quantity FROM working_order_items WHERE order_id=? AND quantity>0 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []WorkingOrderItem
	for rows.Next() {
		var i WorkingOrderItem
		if err = rows.Scan(&i.LineID, &i.ProductID, &i.Name, &i.Price, &i.Quantity, &i.Picked, &i.PickVersion, &i.SKU, &i.SaleUnit, &i.PriceBasis, &i.QuantityStep, &i.Unavailable, &i.Cancelled); err != nil {
			return nil, err
		}
		i.Subtotal = (i.Quantity - i.Unavailable - i.Cancelled) * i.Price
		items = append(items, i)
	}
	return items, rows.Err()
}

// OverrideOrder is a reasoned manager command against the working allocation,
// never the placed receipt. Ownership, replay, version, inventory and audit all
// share the immediate SQLite transaction. HTTP is responsible for manager+CSRF.
func (s *Store) OverrideOrder(id int64, sid string, allOrders bool, c OrderCommand) error {
	c.Reason = strings.TrimSpace(c.Reason)
	if c.Version < 1 || len(c.Key) < 16 || len(c.Key) > 150 || utf8.RuneCountInString(c.Reason) < 3 || utf8.RuneCountInString(c.Reason) > 240 || !utf8.ValidString(c.Reason) || strings.IndexFunc(c.Reason, unicode.IsControl) >= 0 {
		return ErrInvalid
	}
	if c.Quantity < 0 || c.Quantity > 99 {
		return ErrInvalid
	}
	if c.Action != "set" && c.Action != "substitute" && c.Action != "finish" && c.Action != "cancel" {
		return ErrInvalid
	}
	if err := s.ExpireHolds(); err != nil {
		return err
	}
	encoded, _ := json.Marshal(c)
	sum := sha256.Sum256(encoded)
	hash := hex.EncodeToString(sum[:])
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	var version int64
	err = tx.QueryRow(`SELECT status,order_version FROM orders WHERE id=? AND (? OR session_id=?)`, id, allOrders, sid).Scan(&status, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var prior string
	err = tx.QueryRow(`SELECT command_hash FROM order_events WHERE order_id=? AND command_key=?`, id, c.Key).Scan(&prior)
	if err == nil {
		if prior == hash {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if status == "Completed" || status == "Ready" {
		return ErrTerminal
	}
	if version != c.Version {
		return ErrConflict
	}
	items, err := workingOrderItems(tx, id)
	if err != nil {
		return err
	}
	for _, i := range items {
		if i.SaleUnit != "each" {
			return ErrUnavailable
		}
	}
	details := ""
	switch c.Action {
	case "set":
		details, err = setWorkingQuantity(tx, id, c.ProductID, c.Quantity, c.Disposition, c.CatalogQuote)
	case "substitute":
		if c.ProductID < 1 || c.ReplacementID < 1 || c.ProductID == c.ReplacementID || c.Quantity < 1 {
			return ErrInvalid
		}
		var oldQty int64
		if err = tx.QueryRow(`SELECT quantity FROM working_order_items WHERE order_id=? AND product_id=? AND quantity>0`, id, c.ProductID).Scan(&oldQty); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var replacementQty int64
		err = tx.QueryRow(`SELECT quantity FROM working_order_items WHERE order_id=? AND product_id=?`, id, c.ReplacementID).Scan(&replacementQty)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var added, removed string
		// Reserve replacement first; failure rolls the whole command back.
		added, err = setWorkingQuantity(tx, id, c.ReplacementID, replacementQty+c.Quantity, "", c.CatalogQuote)
		if err == nil {
			removed, err = setWorkingQuantity(tx, id, c.ProductID, 0, c.Disposition, c.CatalogQuote)
		}
		details = "Manager substitution (instruction override): " + removed + "; " + added
	case "finish", "cancel":
		if c.Action == "finish" && c.Remainder != "unavailable" && c.Remainder != "cancelled" {
			return ErrInvalid
		}
		if c.Action == "finish" {
			var requested, picked int64
			for _, i := range items {
				requested += i.Quantity
				picked += i.Picked
			}
			if requested > 0 && requested == picked {
				return ErrUseReady
			}
		}
		var final, picked, requested, returned, lost int64
		for _, i := range items {
			outgoing := i.Quantity - i.Picked
			if c.Action == "cancel" {
				outgoing = i.Quantity
			}
			if outgoing > 0 {
				if err = disposeOrderStock(tx, i.ProductID, outgoing, c.Disposition); err != nil {
					return err
				}
				if c.Disposition == "restock" {
					returned += outgoing
				} else {
					lost += outgoing
				}
			}
			requested += i.Quantity
			picked += i.Picked
			final += i.Picked * i.Price
			// Actual picked counts are retained even when the entire order is cancelled.
			// The terminal cancellation event accounts for all allocations, including picks.
			unavailable, cancelled := int64(0), int64(0)
			if c.Action == "cancel" || c.Remainder == "cancelled" {
				cancelled = i.Quantity - i.Picked
			} else {
				unavailable = i.Quantity - i.Picked
			}
			if _, err = tx.Exec(`UPDATE working_order_items SET unavailable_quantity=?,cancelled_quantity=?,pick_version=pick_version+1 WHERE id=?`, unavailable, cancelled, i.LineID); err != nil {
				return err
			}
		}
		kind := "partial"
		if picked == requested && requested > 0 {
			kind = "full"
		}
		if c.Action == "cancel" {
			kind = "cancelled"
			final = 0
		}
		if _, err = tx.Exec(`UPDATE orders SET status='Completed',final_total=?,completion_kind=? WHERE id=?`, final, kind, id); err != nil {
			return err
		}
		details = fmt.Sprintf("%s: %d of %d units actually picked; remainder %s; returned %d; written off %d; final %s", kind, picked, requested, c.Remainder, returned, lost, Money(final))
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE orders SET order_version=order_version+1 WHERE id=? AND order_version=?`, id, c.Version); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details) VALUES(?,?,?,?,?,?)`, id, c.Key, hash, c.Action, c.Reason, details); err != nil {
		return err
	}
	return tx.Commit()
}

func disposeOrderStock(tx *sql.Tx, pid, quantity int64, disposition string) error {
	if quantity == 0 {
		return nil
	}
	if disposition == "writeoff" {
		return nil
	}
	if disposition != "restock" {
		return ErrInvalid
	}
	result, err := tx.Exec(`UPDATE products SET stock=stock+?,version=version+1 WHERE id=? AND stock+?+(SELECT COALESCE(SUM(reserved),0) FROM cart WHERE product_id=products.id)<=10000`, quantity, pid, quantity)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStockCapacity
	}
	return nil
}

func setWorkingQuantity(tx *sql.Tx, id, pid, quantity int64, disposition, quote string) (string, error) {
	if pid < 1 || quantity < 0 || quantity > 99 {
		return "", ErrInvalid
	}
	var old, picked, price int64
	var name, unit string
	err := tx.QueryRow(`SELECT quantity,picked_quantity,price,name,sale_unit FROM working_order_items WHERE order_id=? AND product_id=?`, id, pid).Scan(&old, &picked, &price, &name, &unit)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if !exists && quantity == 0 {
		return "", ErrNotFound
	}
	if exists && unit != "each" {
		return "", ErrUnavailable
	}
	if quantity > old {
		var archived bool
		var sku string
		var basis, step int64
		err = tx.QueryRow(`SELECT p.name,p.price,p.sale_unit,p.sku,p.price_basis,p.quantity_step,(p.archived OR c.archived OR COALESCE(t.archived,0)) FROM products p JOIN categories c ON c.id=p.category_id LEFT JOIN product_types t ON t.id=p.type_id WHERE p.id=?`, pid).Scan(&name, &price, &unit, &sku, &basis, &step, &archived)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		if err != nil {
			return "", err
		}
		if archived || unit != "each" {
			return "", ErrUnavailable
		}
		if !exists {
			current, err := currentOrderCatalogQuote(tx)
			if err != nil {
				return "", err
			}
			if len(quote) != 64 || quote != current {
				return "", ErrOrderQuote
			}
		}
		result, e := tx.Exec(`UPDATE products SET stock=stock-?,version=version+1 WHERE id=? AND stock>=?`, quantity-old, pid, quantity-old)
		if e != nil {
			return "", e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return "", e
		}
		if n != 1 {
			return "", ErrStock
		}
		if !exists {
			_, err = tx.Exec(`INSERT INTO working_order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step) VALUES(?,?,?,?,?,?,?,?,?)`, id, pid, name, price, quantity, sku, unit, basis, step)
			if err != nil {
				return "", err
			}
		}
	}
	returned, lost := int64(0), int64(0)
	if quantity < old {
		if err = disposeOrderStock(tx, pid, old-quantity, disposition); err != nil {
			return "", err
		}
		if disposition == "restock" {
			returned = old - quantity
		} else {
			lost = old - quantity
		}
	}
	newPicked := picked
	if newPicked > quantity {
		newPicked = quantity
	}
	if exists {
		_, err = tx.Exec(`UPDATE working_order_items SET quantity=?,picked_quantity=?,pick_version=pick_version+1 WHERE order_id=? AND product_id=?`, quantity, newPicked, id, pid)
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s (product %d): quantity %d → %d; picked %d → %d; returned %d; written off %d", name, pid, old, quantity, picked, newPicked, returned, lost), nil
}

// Only displayed catalog/rate identity participates; changing shelf stock alone
// does not invalidate a price quote. Existing working snapshots ignore this quote.
func orderCatalogQuote(products []Product) string {
	h := sha256.New()
	for _, p := range products {
		if !p.Archived && p.SaleUnit == "each" {
			fmt.Fprintf(h, "%d:%d:%d:%s:%s\n", p.ID, p.Price, p.PriceVersion, p.Name, p.SKU)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
func currentOrderCatalogQuote(tx *sql.Tx) (string, error) {
	rows, err := tx.Query(`SELECT ` + productSelect + productJoins + `WHERE p.archived=0 ORDER BY p.id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var products []Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return "", err
		}
		products = append(products, p)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return orderCatalogQuote(products), nil
}
