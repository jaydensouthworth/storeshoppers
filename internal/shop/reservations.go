package shop

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const basketHold = 15 * time.Minute

type querier interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

// ExpireHolds reconciles durable allocations under SQLite's immediate lock.
// Clearing reserved quantities and returning stock share one commit, so restart,
// concurrent requests, and repeated reads cannot return the same units twice.
func (s *Store) ExpireHolds() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	const expired = `SELECT b.id FROM baskets b JOIN sessions s ON s.id=b.owner_session_id WHERE b.hold_until>0 AND (b.hold_until<=? OR s.expires<=?)`
	rows, err := tx.Query(`SELECT product_id,SUM(reserved) FROM cart WHERE reserved>0 AND basket_id IN (`+expired+`) GROUP BY product_id`, now, now)
	if err != nil {
		return err
	}
	type release struct{ pid, qty int64 }
	var releases []release
	for rows.Next() {
		var r release
		if err = rows.Scan(&r.pid, &r.qty); err != nil {
			rows.Close()
			return err
		}
		releases = append(releases, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range releases {
		if _, err = tx.Exec(`UPDATE products SET stock=stock+?,version=version+1 WHERE id=?`, r.qty, r.pid); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE cart SET reserved=0 WHERE basket_id IN (`+expired+`)`, now, now); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE sessions SET revision=revision+1 WHERE id IN (SELECT owner_session_id FROM baskets WHERE synthetic=0 AND id IN (`+expired+`))`, now, now); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE baskets SET hold_until=0,revision=revision+1 WHERE id IN (`+expired+`)`, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

func customerBasket(q querier, sid string, now int64) (Basket, error) {
	var id string
	err := q.QueryRow(`SELECT id FROM baskets WHERE owner_session_id=? AND synthetic=0`, sid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Basket{}, ErrNotFound
	}
	if err != nil {
		return Basket{}, err
	}
	return loadBasket(q, id, now)
}
func loadBasket(q querier, id string, now int64) (Basket, error) {
	b := Basket{CanCheckout: true}
	err := q.QueryRow(`SELECT id,label,synthetic,revision,hold_until FROM baskets WHERE id=?`, id).Scan(&b.ID, &b.Label, &b.Synthetic, &b.Revision, &b.HoldUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	if b.HoldUntil > now {
		b.HoldLabel = time.Unix(b.HoldUntil, 0).UTC().Format("15:04:05 UTC · 2 Jan")
	}
	rows, err := q.Query(`SELECT `+productSelect+`,cart.quantity,cart.reserved`+productJoins+`JOIN cart ON cart.product_id=p.id WHERE cart.basket_id=? ORDER BY p.id`, id)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	quote := sha256.New()
	fmt.Fprintf(quote, "basket-v4:%s;", id)
	for rows.Next() {
		var l CartLine
		fields := append(productFields(&l.Product), &l.Quantity, &l.Reserved)
		if err = rows.Scan(fields...); err != nil {
			return b, err
		}
		p := l.Product
		if p.SaleUnit == "each" {
			l.Subtotal = l.Quantity * p.Price
			b.Total += l.Subtotal
			b.Count += l.Quantity
		}
		if p.Archived || p.SaleUnit != "each" {
			b.CanCheckout = false
		}
		if l.Reserved != l.Quantity || b.HoldUntil <= now {
			b.NeedsReview = true
			b.CanCheckout = false
		}
		fmt.Fprintf(quote, "%d:%d:%d:%d:%t:%s:%d:%d:%q;", p.ID, l.Quantity, p.Price, p.PriceVersion, p.Archived, p.SaleUnit, p.PriceBasis, p.QuantityStep, p.Name)
		b.Lines = append(b.Lines, l)
	}
	if len(b.Lines) == 0 {
		b.CanCheckout = false
	}
	b.Quote = hex.EncodeToString(quote.Sum(nil))
	return b, rows.Err()
}
func (s *Store) Basket(sid string) (Basket, error) {
	if err := s.ExpireHolds(); err != nil {
		return Basket{}, err
	}
	return customerBasket(s.db, sid, s.now().Unix())
}
func (s *Store) SetCart(sid string, pid, qty int64, add bool) error {
	return s.setCart(sid, pid, qty, add, 0)
}
func (s *Store) SetCartVersion(sid string, pid, qty int64, add bool, revision int64) error {
	if revision < 1 {
		return ErrInvalid
	}
	return s.setCart(sid, pid, qty, add, revision)
}
func (s *Store) setCart(sid string, pid, qty int64, add bool, revision int64) error {
	if pid < 1 || qty < 0 || qty > 99 {
		return ErrInvalid
	}
	if err := s.ExpireHolds(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, err := customerBasket(tx, sid, s.now().Unix())
	if err != nil {
		return err
	}
	if revision != 0 && revision != b.Revision {
		return ErrConflict
	}
	if err = s.changeBasket(tx, b, pid, qty, add); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RenewBasket(sid string, revision int64) error {
	if revision < 1 {
		return ErrInvalid
	}
	if err := s.ExpireHolds(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, err := customerBasket(tx, sid, s.now().Unix())
	if err != nil {
		return err
	}
	if b.Revision != revision {
		return ErrConflict
	}
	if len(b.Lines) == 0 {
		return ErrEmpty
	}
	if err = s.reserveBasket(tx, b, b.Lines); err != nil {
		return err
	}
	return tx.Commit()
}

// changeBasket preflights the entire resulting allocation before any writes.
// Removing an unavailable line always works, even if another line cannot renew.
func (s *Store) changeBasket(tx *sql.Tx, b Basket, pid, qty int64, add bool) error {
	p, err := scanProduct(tx.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, pid))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	old := int64(0)
	for _, l := range b.Lines {
		if l.Product.ID == pid {
			old = l.Quantity
		}
	}
	if add {
		if qty > 99-old {
			return ErrInvalid
		}
		qty += old
	}
	if qty > 0 && (p.Archived || p.SaleUnit != "each") {
		return ErrUnavailable
	}
	var desired []CartLine
	found := false
	for _, l := range b.Lines {
		if l.Product.ID == pid {
			found = true
			if qty == 0 {
				continue
			}
			l.Quantity = qty
		}
		desired = append(desired, l)
	}
	if !found && qty > 0 {
		desired = append(desired, CartLine{Product: p, Quantity: qty})
	}
	err = s.reserveBasket(tx, b, desired)
	if qty != 0 || (!errors.Is(err, ErrStock) && !errors.Is(err, ErrUnavailable)) {
		return err
	}
	// A failed preflight made no writes. Remove just the requested line without
	// renewing the remaining hold or hiding stale/unavailable basket contents.
	for _, l := range b.Lines {
		if l.Product.ID == pid && l.Reserved > 0 {
			if _, err = tx.Exec(`UPDATE products SET stock=stock+?,version=version+1 WHERE id=?`, l.Reserved, pid); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(`DELETE FROM cart WHERE basket_id=? AND product_id=?`, b.ID, pid); err != nil {
		return err
	}
	return bumpBasket(tx, b, b.HoldUntil)
}
func (s *Store) reserveBasket(tx *sql.Tx, b Basket, desired []CartLine) error {
	old := map[int64]int64{}
	next := map[int64]int64{}
	for _, l := range b.Lines {
		old[l.Product.ID] = l.Reserved
	}
	for _, l := range desired {
		if l.Quantity < 1 || l.Quantity > 99 {
			return ErrInvalid
		}
		if l.Product.Archived || l.Product.SaleUnit != "each" {
			return ErrUnavailable
		}
		if l.Quantity > l.Product.Stock+old[l.Product.ID] {
			return ErrStock
		}
		next[l.Product.ID] = l.Quantity
	}
	for _, l := range b.Lines {
		if _, ok := next[l.Product.ID]; !ok {
			next[l.Product.ID] = 0
		}
	}
	for pid, qty := range next {
		delta := qty - old[pid]
		if delta != 0 {
			result, err := tx.Exec(`UPDATE products SET stock=stock-?,version=version+1 WHERE id=? AND stock-? BETWEEN 0 AND 10000`, delta, pid, delta)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrStock
			}
		}
		if qty == 0 {
			if _, err := tx.Exec(`DELETE FROM cart WHERE basket_id=? AND product_id=?`, b.ID, pid); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(`INSERT INTO cart(basket_id,product_id,quantity,reserved) VALUES(?,?,?,?) ON CONFLICT(basket_id,product_id) DO UPDATE SET quantity=excluded.quantity,reserved=excluded.reserved`, b.ID, pid, qty, qty); err != nil {
				return err
			}
		}
	}
	until := int64(0)
	if len(desired) > 0 {
		until = s.now().Add(basketHold).Unix()
	}
	return bumpBasket(tx, b, until)
}
func bumpBasket(tx *sql.Tx, b Basket, until int64) error {
	if _, err := tx.Exec(`UPDATE baskets SET revision=revision+1,hold_until=? WHERE id=?`, until, b.ID); err != nil {
		return err
	}
	if !b.Synthetic {
		_, err := tx.Exec(`UPDATE sessions SET revision=revision+1 WHERE id=(SELECT owner_session_id FROM baskets WHERE id=?)`, b.ID)
		return err
	}
	return nil
}
func cleanInstructions(v string) (string, error) {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) > 500 {
		return "", ErrInvalid
	}
	for _, r := range v {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", ErrInvalid
		}
	}
	return strings.TrimSpace(v), nil
}
func (s *Store) Baskets(sid string, all bool) ([]Basket, error) {
	if err := s.ExpireHolds(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT b.id FROM baskets b WHERE (? OR owner_session_id=?) AND (synthetic=1 OR EXISTS(SELECT 1 FROM cart WHERE basket_id=b.id)) ORDER BY b.synthetic,b.id LIMIT 100`, all, sid)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []Basket
	for _, id := range ids {
		b, e := loadBasket(s.db, id, s.now().Unix())
		if e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, nil
}
func scopedBasket(q querier, id, sid string, all bool, now int64) (Basket, error) {
	var found string
	err := q.QueryRow(`SELECT id FROM baskets WHERE id=? AND (? OR owner_session_id=?)`, id, all, sid).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return Basket{}, ErrNotFound
	}
	if err != nil {
		return Basket{}, err
	}
	return loadBasket(q, id, now)
}
func (s *Store) ManagedBasket(id, sid string, all bool) (Basket, error) {
	if err := s.ExpireHolds(); err != nil {
		return Basket{}, err
	}
	return scopedBasket(s.db, id, sid, all, s.now().Unix())
}
func (s *Store) ManagerSetBasket(id, sid string, all bool, pid, qty, revision int64, reason string) error {
	if pid < 1 || qty < 0 || qty > 99 {
		return ErrInvalid
	}
	return s.managerBasketCommand(id, sid, all, revision, reason, "set quantity", func(tx *sql.Tx, b Basket) error { return s.changeBasket(tx, b, pid, qty, false) }, func(before, after Basket) string {
		oldQty, oldHeld, newHeld := int64(0), int64(0), int64(0)
		for _, l := range before.Lines {
			if l.Product.ID == pid {
				oldQty, oldHeld = l.Quantity, l.Reserved
			}
		}
		for _, l := range after.Lines {
			if l.Product.ID == pid {
				newHeld = l.Reserved
			}
		}
		return fmt.Sprintf("Product %d: quantity %d → %d; held %d → %d", pid, oldQty, qty, oldHeld, newHeld)
	})
}
func (s *Store) ManagerRenewBasket(id, sid string, all bool, revision int64, reason string) error {
	return s.managerBasketCommand(id, sid, all, revision, reason, "review and reserve", func(tx *sql.Tx, b Basket) error {
		if len(b.Lines) == 0 {
			return ErrEmpty
		}
		return s.reserveBasket(tx, b, b.Lines)
	}, func(before, after Basket) string {
		return "Reviewed basket and reserved available units for 15 minutes"
	})
}
func (s *Store) managerBasketCommand(id, sid string, all bool, revision int64, reason, action string, command func(*sql.Tx, Basket) error, details func(Basket, Basket) string) error {
	reason = strings.TrimSpace(reason)
	if revision < 1 || !utf8.ValidString(reason) || utf8.RuneCountInString(reason) < 3 || utf8.RuneCountInString(reason) > 120 {
		return ErrInvalid
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return ErrInvalid
		}
	}
	if err := s.ExpireHolds(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, err := scopedBasket(tx, id, sid, all, s.now().Unix())
	if err != nil {
		return err
	}
	if revision != b.Revision {
		return ErrConflict
	}
	if err = command(tx, b); err != nil {
		return err
	}
	after, err := loadBasket(tx, id, s.now().Unix())
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO basket_events(basket_id,action,reason,details) VALUES(?,?,?,?)`, id, action, reason, details(b, after)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) BasketEvents(id, sid string, all bool) ([]BasketEvent, error) {
	rows, err := s.db.Query(`SELECT e.basket_id,b.label,e.action,e.reason,e.details,e.created FROM basket_events e JOIN baskets b ON b.id=e.basket_id WHERE (? OR b.owner_session_id=?) AND (?='' OR b.id=?) ORDER BY e.id DESC LIMIT 20`, all, sid, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BasketEvent
	for rows.Next() {
		var e BasketEvent
		if err = rows.Scan(&e.BasketID, &e.Label, &e.Action, &e.Reason, &e.Details, &e.Created); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Practice baskets are explicit, idempotent, per-visitor fake content. Creation
// does not allocate shared stock; the manager must review/reserve it explicitly.
func (s *Store) CreatePracticeBaskets(sid string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	if err = tx.QueryRow(`SELECT id FROM sessions WHERE id=? AND expires>?`, sid, s.now().Unix()).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	for slot := 1; slot <= 2; slot++ {
		id := token()[:32]
		r, e := tx.Exec(`INSERT INTO baskets(id,owner_session_id,synthetic,label,practice_slot) VALUES(?,?,1,?,?) ON CONFLICT(owner_session_id,practice_slot) DO NOTHING`, id, sid, fmt.Sprintf("Synthetic practice basket %d", slot), slot)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			continue
		}
		if _, err = tx.Exec(`INSERT INTO cart(basket_id,product_id,quantity) SELECT ?,id,? FROM products WHERE archived=0 AND sale_unit='each' ORDER BY id LIMIT 2 OFFSET ?`, id, slot, slot-1); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO basket_events(basket_id,action,reason,details) VALUES(?,'create practice','Explicit practice setup','Initialized synthetic fake groceries; no stock held until review')`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
