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
	rows, err := q.Query(`SELECT id,product_id,name,price,quantity,picked_quantity,pick_version,sku,sale_unit,price_basis,quantity_step,unavailable_quantity,cancelled_quantity,allocated_quantity,measurement_confirmed FROM working_order_items WHERE order_id=? AND quantity>0 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []WorkingOrderItem
	for rows.Next() {
		var i WorkingOrderItem
		if err = rows.Scan(&i.LineID, &i.ProductID, &i.Name, &i.Price, &i.Quantity, &i.Picked, &i.PickVersion, &i.SKU, &i.SaleUnit, &i.PriceBasis, &i.QuantityStep, &i.Unavailable, &i.Cancelled, &i.Allocated, &i.Measured); err != nil {
			return nil, err
		}
		i.Subtotal, err = lineAmount(i.Allocated-i.Unavailable-i.Cancelled, i.Price, i.PriceBasis, i.SaleUnit)
		if err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// OverrideOrder is a reasoned manager command against the working allocation,
// never the placed receipt. Ownership, replay, version, inventory and audit all
// share the immediate SQLite transaction. HTTP is responsible for manager+CSRF.
func (s *Store) OverrideOrder(id int64, sid string, allOrders bool, c OrderCommand) error {
	c.Reason = routineOrderReason(c.Action, c.Quantity, c.Reason)
	if c.Version < 1 || len(c.Key) < 16 || len(c.Key) > 150 || utf8.RuneCountInString(c.Reason) < 3 || utf8.RuneCountInString(c.Reason) > 240 || !utf8.ValidString(c.Reason) || strings.IndexFunc(c.Reason, unicode.IsControl) >= 0 {
		return ErrInvalid
	}
	if c.Quantity < 0 || c.Quantity > maxGrams {
		return ErrInvalid
	}
	if c.Action != "set" && c.Action != "substitute" && c.Action != "finish" && c.Action != "cancel" {
		return ErrInvalid
	}
	encoded, _ := json.Marshal(c)
	sum := sha256.Sum256(encoded)
	hash := hex.EncodeToString(sum[:])
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	var held bool
	var version int64
	err = tx.QueryRow(`SELECT status,order_version,attention_reason<>'' FROM orders WHERE id=? AND (? OR session_id=?)`, id, allOrders, sid).Scan(&status, &version, &held)
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
	if held && c.Action == "finish" {
		return ErrAttention
	}
	if err = expireHolds(tx, s.now().Unix()); err != nil {
		return err
	}
	items, err := workingOrderItems(tx, id)
	if err != nil {
		return err
	}
	now := s.now().Unix()
	details := ""
	switch c.Action {
	case "set":
		details, err = setWorkingQuantity(tx, id, c.ProductID, c.Quantity, c.Disposition, c.CatalogQuote, now)
	case "substitute":
		if c.ProductID < 1 || c.ReplacementID < 1 || c.ProductID == c.ReplacementID || c.Quantity < 1 {
			return ErrInvalid
		}
		var oldUnit string
		if err = tx.QueryRow(`SELECT sale_unit FROM working_order_items WHERE order_id=? AND product_id=? AND quantity>0`, id, c.ProductID).Scan(&oldUnit); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var replacementUnit string
		if err = tx.QueryRow(`SELECT COALESCE((SELECT sale_unit FROM working_order_items WHERE order_id=? AND product_id=p.id),p.sale_unit) FROM products p WHERE p.id=?`, id, c.ReplacementID).Scan(&replacementUnit); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if replacementUnit != oldUnit {
			return ErrInvalid
		}
		var replacementQty, replacementAllocated int64
		err = tx.QueryRow(`SELECT quantity,allocated_quantity FROM working_order_items WHERE order_id=? AND product_id=?`, id, c.ReplacementID).Scan(&replacementQty, &replacementAllocated)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var added, removed string
		// Reserve replacement first; failure rolls the whole command back.
		added, err = setWorkingAllocation(tx, id, c.ReplacementID, replacementQty+c.Quantity, replacementAllocated+c.Quantity, "", c.CatalogQuote, now)
		if err == nil {
			removed, err = setWorkingQuantity(tx, id, c.ProductID, 0, c.Disposition, c.CatalogQuote, now)
		}
		details = "Manager substitution (instruction override): " + removed + "; " + added
	case "finish", "cancel":
		if c.Action == "finish" && c.Remainder != "unavailable" && c.Remainder != "cancelled" {
			return ErrInvalid
		}
		allComplete := len(items) > 0
		for _, i := range items {
			allComplete = allComplete && i.Complete()
		}
		if c.Action == "finish" && allComplete {
			return ErrUseReady
		}
		var final, picked, requested, returned, lost, returnedGrams, lostGrams int64
		weighted := false
		for _, i := range items {
			weighted = weighted || i.SaleUnit == "g"
			outgoing := i.Allocated - i.Picked
			if c.Action == "cancel" {
				outgoing = i.Allocated
			}
			if outgoing > 0 {
				if err = disposeOrderStock(tx, i.ProductID, outgoing, c.Disposition); err != nil {
					return err
				}
				if i.SaleUnit == "g" {
					if c.Disposition == "restock" {
						returnedGrams += outgoing
					} else {
						lostGrams += outgoing
					}
				} else {
					if c.Disposition == "restock" {
						returned += outgoing
					} else {
						lost += outgoing
					}
				}
			}
			if i.SaleUnit == "each" {
				requested += i.Quantity
				picked += i.Picked
			}
			amount, e := lineAmount(i.Picked, i.Price, i.PriceBasis, i.SaleUnit)
			if e != nil {
				return e
			}
			final, err = addAmount(final, amount)
			if err != nil {
				return err
			}
			unavailable, cancelled := int64(0), int64(0)
			if c.Action == "cancel" || c.Remainder == "cancelled" {
				cancelled = i.Allocated - i.Picked
			} else {
				unavailable = i.Allocated - i.Picked
			}
			if _, err = tx.Exec(`UPDATE working_order_items SET unavailable_quantity=?,cancelled_quantity=?,measurement_confirmed=CASE WHEN sale_unit='g' THEN 1 ELSE measurement_confirmed END,pick_version=pick_version+1 WHERE id=?`, unavailable, cancelled, i.LineID); err != nil {
				return err
			}
		}
		kind := "partial"
		if allComplete {
			kind = "full"
		}
		if c.Action == "cancel" {
			kind = "cancelled"
			final = 0
		}
		if _, err = tx.Exec(`UPDATE orders SET status='Completed',final_total=?,completion_kind=?,attention_reason='',attention_since=0 WHERE id=?`, final, kind, id); err != nil {
			return err
		}
		details = fmt.Sprintf("%s: %d of %d counted units actually picked; remainder %s; returned %d units; written off %d units; final %s", kind, picked, requested, c.Remainder, returned, lost, Money(final))
		if weighted {
			details += fmt.Sprintf("; weighed stock returned %d g; written off %d g; per-line measured amounts retained", returnedGrams, lostGrams)
		}
	}
	if err != nil {
		return err
	}
	checkItems, err := workingOrderItems(tx, id)
	if err != nil {
		return err
	}
	var checkOrder Order
	if err = summarizeWorking(&checkOrder, checkItems); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE orders SET order_version=order_version+1 WHERE id=? AND order_version=?`, id, c.Version); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details) VALUES(?,?,?,?,?,?)`, id, c.Key, hash, c.Action, c.Reason, details); err != nil {
		return err
	}
	if c.Action == "finish" || c.Action == "cancel" {
		if err = endShopperAssignment(tx, id, "Completed · "+c.Action); err != nil {
			return err
		}
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
	result, err := tx.Exec(`UPDATE products SET stock=stock+?,version=version+1 WHERE id=? AND stock+?+(SELECT COALESCE(SUM(reserved),0) FROM cart WHERE product_id=products.id)<=CASE sale_unit WHEN 'g' THEN 1000000 ELSE 10000 END`, quantity, pid, quantity)
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

func setWorkingQuantity(tx *sql.Tx, id, pid, quantity int64, disposition, quote string, now int64) (string, error) {
	return setWorkingAllocation(tx, id, pid, quantity, quantity, disposition, quote, now)
}

// Substitution adds to both the existing target and its accepted allocation.
// An already measured bag stays reserved while the combined line is remeasured.
func setWorkingAllocation(tx *sql.Tx, id, pid, quantity, allocation int64, disposition, quote string, now int64) (string, error) {
	if pid < 1 || quantity < 0 || quantity > maxGrams || allocation < 0 || allocation > maxGrams {
		return "", ErrInvalid
	}
	var old, allocated, picked, price, basis, step int64
	var name, unit, sku string
	err := tx.QueryRow(`SELECT quantity,allocated_quantity,picked_quantity,price,name,sale_unit,sku,price_basis,quantity_step FROM working_order_items WHERE order_id=? AND product_id=?`, id, pid).Scan(&old, &allocated, &picked, &price, &name, &unit, &sku, &basis, &step)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if !exists && quantity == 0 {
		return "", ErrNotFound
	}
	if exists && quantity == old {
		if !validQuantity(unit, quantity, step, true) {
			return "", ErrInvalid
		}
		return fmt.Sprintf("%s: target unchanged at %d %s", name, quantity, unitLabel(unit)), nil
	}
	if !exists || allocation > allocated {
		var p Product
		var taxonomyInactive bool
		fields := append(productFields(&p), &taxonomyInactive)
		e := tx.QueryRow(`SELECT `+productSelect+`,(c.archived OR COALESCE(t.archived,0))`+productJoins+`WHERE p.id=?`, pid).Scan(fields...)
		if errors.Is(e, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		if e != nil {
			return "", e
		}
		if p.Archived || taxonomyInactive {
			return "", ErrUnavailable
		}
		if !exists {
			current, e := currentOrderCatalogQuote(tx, now)
			if e != nil {
				return "", e
			}
			if len(quote) != 64 || quote != current {
				return "", ErrOrderQuote
			}
			state, e := loadPricing(tx, now)
			if e != nil {
				return "", e
			}
			state.apply(&p)
			name, unit, sku, price, basis, step = p.Name, p.SaleUnit, p.SKU, p.EffectivePrice(), p.PriceBasis, p.QuantityStep
		} else if p.SaleUnit != unit {
			return "", ErrUnitLocked
		}
	}
	if !validQuantity(unit, quantity, step, true) || allocation > quantityLimit(unit) || (unit == "each" && allocation != quantity) {
		return "", ErrInvalid
	}
	if allocation > allocated {
		result, e := tx.Exec(`UPDATE products SET stock=stock-?,version=version+1 WHERE id=? AND stock>=?`, allocation-allocated, pid, allocation-allocated)
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
	}
	returned, lost := int64(0), int64(0)
	if allocation < allocated {
		if disposition == "" {
			if unit == "g" || allocation < picked {
				return "", ErrPickedDisposition
			}
			disposition = "restock"
		}
		if err = disposeOrderStock(tx, pid, allocated-allocation, disposition); err != nil {
			return "", err
		}
		if disposition == "restock" {
			returned = allocated - allocation
		} else {
			lost = allocated - allocation
		}
	}
	newPicked := min(picked, allocation)
	if unit == "g" {
		newPicked = 0
	}
	if exists {
		_, err = tx.Exec(`UPDATE working_order_items SET quantity=?,allocated_quantity=?,picked_quantity=?,measurement_confirmed=0,unavailable_quantity=0,cancelled_quantity=0,pick_version=pick_version+1 WHERE order_id=? AND product_id=?`, quantity, allocation, newPicked, id, pid)
	} else {
		_, err = tx.Exec(`INSERT INTO working_order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,allocated_quantity) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, pid, name, price, quantity, sku, unit, basis, step, allocation)
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s (product %d): target %d → %d %s; allocation %d → %d %s; picked %d → %d; returned %d; written off %d", name, pid, old, quantity, unitLabel(unit), allocated, allocation, unitLabel(unit), picked, newPicked, returned, lost), nil
}

// Only displayed catalog/rate identity participates; changing shelf stock alone
// does not invalidate a price quote. Existing working snapshots ignore this quote.
func orderCatalogQuote(products []Product) string {
	h := sha256.New()
	for _, p := range products {
		if !p.Archived {
			fmt.Fprintf(h, "%d:%d:%d:%s:%s\n", p.ID, p.EffectivePrice(), p.PriceVersion, p.Name, p.SKU)
			fmt.Fprintf(h, "unit:%s:%d:%d\n", p.SaleUnit, p.PriceBasis, p.QuantityStep)
			fmt.Fprintf(h, "sale:%d:%d:%d:%d:%d\n", p.PromotionID, p.PromotionVersion, p.SaleStarts, p.SaleEnds, p.PricingBoundary)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
func currentOrderCatalogQuote(tx *sql.Tx, now int64) (string, error) {
	state, err := loadPricing(tx, now)
	if err != nil {
		return "", err
	}
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
		state.apply(&p)
		products = append(products, p)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return orderCatalogQuote(products), nil
}
