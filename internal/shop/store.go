package shop

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

//go:embed schema.sql
var schema string

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := u.Query()
	q.Set("_foreign_keys", "on")
	q.Set("_journal_mode", "WAL")
	q.Set("_busy_timeout", "5000")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func (s *Store) Session(id string) (Session, error) {
	var v Session
	err := s.db.QueryRow(`SELECT id,csrf,checkout_key,revision,manager_until FROM sessions WHERE id=? AND expires>?`, id, time.Now().Unix()).Scan(&v.ID, &v.CSRF, &v.CheckoutKey, &v.Revision, &v.ManagerUntil)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	v = Session{ID: token(), CSRF: token(), CheckoutKey: token(), Revision: 1}
	_, err = s.db.Exec(`INSERT INTO sessions(id,csrf,checkout_key,expires) VALUES(?,?,?,?)`, v.ID, v.CSRF, v.CheckoutKey, time.Now().Add(24*time.Hour).Unix())
	return v, err
}
func (s *Store) Products(search, category string) ([]Product, error) {
	return s.catalogProducts(search, category, false)
}

type querier interface {
	Query(string, ...any) (*sql.Rows, error)
}

func basket(q querier, sid string) (Basket, error) {
	b := Basket{CanCheckout: true}
	rows, err := q.Query(`SELECT `+productSelect+`,cart.quantity`+productJoins+`JOIN cart ON cart.product_id=p.id WHERE cart.session_id=? ORDER BY p.id`, sid)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	quote := sha256.New()
	fmt.Fprintf(quote, "basket-v3:%s;", sid)
	for rows.Next() {
		var l CartLine
		fields := append(productFields(&l.Product), &l.Quantity)
		if err = rows.Scan(fields...); err != nil {
			return b, err
		}
		p := l.Product
		// Weighed products are metadata-only until quantity constraints and weighted
		// checkout are deliberately migrated. Never add grams to an item count.
		if p.SaleUnit == "each" {
			l.Subtotal = l.Quantity * p.Price
			b.Total += l.Subtotal
			b.Count += l.Quantity
		}
		if p.Archived || p.SaleUnit != "each" || l.Quantity > p.Stock {
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
func (s *Store) Basket(sid string) (Basket, error) { return basket(s.db, sid) }
func (s *Store) SetCart(sid string, pid, qty int64, add bool) error {
	if qty < 0 || qty > 99 || pid < 1 {
		return ErrInvalid
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var stock, current int64
	var archived bool
	var unit string
	if e = tx.QueryRow(`SELECT stock,archived,sale_unit FROM products WHERE id=?`, pid).Scan(&stock, &archived, &unit); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	}
	if add {
		e = tx.QueryRow(`SELECT quantity FROM cart WHERE session_id=? AND product_id=?`, sid, pid).Scan(&current)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		qty += current
	}
	if qty > 99 {
		return ErrInvalid
	}
	if qty > 0 && (archived || unit != "each") {
		return ErrUnavailable
	}
	if qty > stock {
		return ErrStock
	}
	if qty == 0 {
		_, e = tx.Exec(`DELETE FROM cart WHERE session_id=? AND product_id=?`, sid, pid)
	} else {
		_, e = tx.Exec(`INSERT INTO cart(session_id,product_id,quantity) VALUES(?,?,?) ON CONFLICT(session_id,product_id) DO UPDATE SET quantity=excluded.quantity`, sid, pid, qty)
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE sessions SET revision=revision+1 WHERE id=?`, sid); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Checkout(sid, key string, revision int64, quote string) (int64, error) {
	tx, e := s.db.BeginTx(context.Background(), nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	var existing int64
	e = tx.QueryRow(`SELECT id FROM orders WHERE session_id=? AND checkout_key=?`, sid, key).Scan(&existing)
	if e == nil {
		return existing, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return 0, e
	}
	var want string
	var rev int64
	if e = tx.QueryRow(`SELECT checkout_key,revision FROM sessions WHERE id=?`, sid).Scan(&want, &rev); e != nil {
		return 0, e
	}
	if want != key || rev != revision {
		return 0, ErrConflict
	}
	b, e := basket(tx, sid)
	if e != nil {
		return 0, e
	}
	if len(b.Lines) == 0 {
		return 0, ErrEmpty
	}
	for _, l := range b.Lines {
		if l.Product.Archived || l.Product.SaleUnit != "each" {
			return 0, ErrUnavailable
		}
	}
	if len(quote) != 64 || quote != b.Quote {
		return 0, ErrQuote
	}
	if !b.CanCheckout {
		return 0, ErrStock
	}
	result, e := tx.Exec(`INSERT INTO orders(reference,session_id,checkout_key,total) VALUES(?,?,?,?)`, "DEMO-"+strings.ToUpper(token()[:8]), sid, key, b.Total)
	if e != nil {
		return 0, e
	}
	id, e := result.LastInsertId()
	if e != nil {
		return 0, e
	}
	for _, l := range b.Lines {
		result, e = tx.Exec(`UPDATE products SET stock=stock-?,version=version+1 WHERE id=? AND stock>=? AND archived=0 AND sale_unit='each'`, l.Quantity, l.Product.ID, l.Quantity)
		if e != nil {
			return 0, e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return 0, e
		}
		if n != 1 {
			return 0, ErrStock
		}
		if _, e = tx.Exec(`INSERT INTO order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,subtotal) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, l.Product.ID, l.Product.Name, l.Product.Price, l.Quantity, l.Product.SKU, l.Product.SaleUnit, l.Product.PriceBasis, l.Product.QuantityStep, l.Subtotal); e != nil {
			return 0, e
		}
	}
	if _, e = tx.Exec(`DELETE FROM cart WHERE session_id=?`, sid); e != nil {
		return 0, e
	}
	if _, e = tx.Exec(`UPDATE sessions SET revision=revision+1,checkout_key=? WHERE id=?`, token(), sid); e != nil {
		return 0, e
	}
	if e = tx.Commit(); e != nil {
		return 0, e
	}
	return id, nil
}
func (s *Store) Orders(sid string, manager bool) ([]Order, error) {
	rows, e := s.db.Query(`SELECT o.id,o.reference,o.status,o.created,o.total,COALESCE(SUM(CASE WHEN i.sale_unit='each' THEN i.picked_quantity ELSE 0 END),0),COALESCE(SUM(CASE WHEN i.sale_unit='each' THEN i.quantity ELSE 0 END),0) FROM orders o LEFT JOIN order_items i ON i.order_id=o.id WHERE (? OR o.session_id=?) GROUP BY o.id ORDER BY o.id DESC LIMIT 100`, manager, sid)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var all []Order
	for rows.Next() {
		var o Order
		if e = rows.Scan(&o.ID, &o.Reference, &o.Status, &o.Created, &o.Total, &o.PickedCount, &o.RequiredCount); e != nil {
			return nil, e
		}
		o.AllPicked = o.RequiredCount > 0 && o.PickedCount == o.RequiredCount
		all = append(all, o)
	}
	return all, rows.Err()
}
func (s *Store) Order(id int64, sid string, manager bool) (Order, error) {
	var o Order
	e := s.db.QueryRow(`SELECT id,reference,status,created,total FROM orders WHERE id=? AND (? OR session_id=?)`, id, manager, sid).Scan(&o.ID, &o.Reference, &o.Status, &o.Created, &o.Total)
	if errors.Is(e, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	if e != nil {
		return o, e
	}
	rows, e := s.db.Query(`SELECT product_id,name,price,quantity,picked_quantity,pick_version,sku,sale_unit,price_basis,quantity_step,subtotal FROM order_items WHERE order_id=? ORDER BY product_id`, id)
	if e != nil {
		return o, e
	}
	defer rows.Close()
	for rows.Next() {
		var i OrderItem
		if e = rows.Scan(&i.ProductID, &i.Name, &i.Price, &i.Quantity, &i.Picked, &i.PickVersion, &i.SKU, &i.SaleUnit, &i.PriceBasis, &i.QuantityStep, &i.Subtotal); e != nil {
			return o, e
		}
		o.Items = append(o.Items, i)
		if i.SaleUnit == "each" {
			o.PickedCount += i.Picked
			o.RequiredCount += i.Quantity
		}
	}
	o.AllPicked = o.RequiredCount > 0 && o.PickedCount == o.RequiredCount
	return o, rows.Err()
}
func (s *Store) Adjust(pid, delta, version int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if delta == 0 || delta < -10000 || delta > 10000 || len(reason) < 3 || len(reason) > 120 {
		return ErrInvalid
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	result, e := tx.Exec(`UPDATE products SET stock=stock+?,version=version+1 WHERE id=? AND version=? AND stock+? BETWEEN 0 AND 10000 AND archived=0`, delta, pid, version, delta)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrConflict
	}
	if _, e = tx.Exec(`INSERT INTO adjustments(product_id,delta,reason,sale_unit) SELECT id,?,?,sale_unit FROM products WHERE id=?`, delta, reason, pid); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Adjustments() ([]Adjustment, error) {
	rows, e := s.db.Query(`SELECT p.name,a.delta,a.reason,a.created,a.sale_unit FROM adjustments a JOIN products p ON p.id=a.product_id ORDER BY a.id DESC LIMIT 10`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var all []Adjustment
	for rows.Next() {
		var a Adjustment
		if e = rows.Scan(&a.Product, &a.Delta, &a.Reason, &a.Created, &a.SaleUnit); e != nil {
			return nil, e
		}
		all = append(all, a)
	}
	return all, rows.Err()
}

// Advance is the internal unrestricted operation. HTTP handlers use AdvanceScoped.
func (s *Store) Advance(id int64, from string) error { return s.AdvanceScoped(id, from, "", true) }

func (s *Store) AdvanceScoped(id int64, from, sid string, allOrders bool) error {
	next := map[string]string{"Placed": "Picking", "Picking": "Ready", "Ready": "Completed"}[from]
	if next == "" {
		return ErrInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRow(`SELECT status FROM orders WHERE id=? AND (? OR session_id=?)`, id, allOrders, sid).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != from {
		return ErrConflict
	}
	if from == "Picking" {
		var total, incomplete int
		if err = tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN picked_quantity < quantity THEN 1 ELSE 0 END),0) FROM order_items WHERE order_id=?`, id).Scan(&total, &incomplete); err != nil {
			return err
		}
		if total == 0 || incomplete != 0 {
			return ErrIncomplete
		}
	}
	if _, err = tx.Exec(`UPDATE orders SET status=? WHERE id=? AND status=?`, next, id, from); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordPicked sets an absolute quantity. Version checks reject stale duplicate
// forms; picking never deducts stock again or changes immutable receipt amounts.
func (s *Store) RecordPicked(orderID, productID, picked, version int64, sid string, allOrders bool) error {
	if picked < 0 || picked > 99 || version < 1 {
		return ErrInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRow(`SELECT status FROM orders WHERE id=? AND (? OR session_id=?)`, orderID, allOrders, sid).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "Picking" {
		return ErrConflict
	}
	var quantity, currentVersion int64
	err = tx.QueryRow(`SELECT quantity,pick_version FROM order_items WHERE order_id=? AND product_id=?`, orderID, productID).Scan(&quantity, &currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if picked > quantity {
		return ErrInvalid
	}
	if version != currentVersion {
		return ErrConflict
	}
	if _, err = tx.Exec(`UPDATE order_items SET picked_quantity=?,pick_version=pick_version+1 WHERE order_id=? AND product_id=? AND pick_version=?`, picked, orderID, productID, version); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Manager(sid string, on bool) error {
	until := int64(0)
	if on {
		until = time.Now().Add(30 * time.Minute).Unix()
	}
	r, e := s.db.Exec(`UPDATE sessions SET manager_until=?,csrf=? WHERE id=?`, until, token(), sid)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("session absent")
	}
	return nil
}
