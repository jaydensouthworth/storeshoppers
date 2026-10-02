package shop

import (
	"context"
	"crypto/rand"
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

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func Open(path string) (*Store, error) { return OpenWithClock(path, time.Now) }

// OpenWithClock supports deterministic reservation expiry and race tests. The clock must be concurrency-safe.
func OpenWithClock(path string, now func() time.Time) (*Store, error) {
	if now == nil {
		return nil, ErrInvalid
	}
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
	s := &Store{db: db, now: now}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.ExpireHolds(); err != nil {
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
	err := s.db.QueryRow(`SELECT id,csrf,checkout_key,revision,manager_until FROM sessions WHERE id=? AND expires>?`, id, s.now().Unix()).Scan(&v.ID, &v.CSRF, &v.CheckoutKey, &v.Revision, &v.ManagerUntil)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	v = Session{ID: token(), CSRF: token(), CheckoutKey: token(), Revision: 1}
	tx, err := s.db.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO sessions(id,csrf,checkout_key,expires) VALUES(?,?,?,?)`, v.ID, v.CSRF, v.CheckoutKey, s.now().Add(24*time.Hour).Unix())
	if err != nil {
		return v, err
	}
	if _, err = tx.Exec(`INSERT INTO baskets(id,owner_session_id) VALUES(?,?)`, token()[:32], v.ID); err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (s *Store) Products(search, category string) ([]Product, error) {
	return s.catalogProducts(search, category, false)
}

func (s *Store) Checkout(sid, key string, revision int64, quote string) (int64, error) {
	return s.CheckoutWithInstructions(sid, key, revision, quote, "")
}
func (s *Store) CheckoutWithInstructions(sid, key string, revision int64, quote, instructions string) (int64, error) {
	if err := s.ExpireHolds(); err != nil {
		return 0, err
	}
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
	instructions, e = cleanInstructions(instructions)
	if e != nil {
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
	now := s.now().Unix()
	b, e := customerBasket(tx, sid, now)
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
	if b.NeedsReview || b.HoldUntil <= now {
		return 0, ErrHold
	}
	if !b.CanCheckout {
		return 0, ErrStock
	}
	result, e := tx.Exec(`INSERT INTO orders(reference,session_id,checkout_key,total,instructions) VALUES(?,?,?,?,?)`, "DEMO-"+strings.ToUpper(token()[:8]), sid, key, b.Total, instructions)
	if e != nil {
		return 0, e
	}
	id, e := result.LastInsertId()
	if e != nil {
		return 0, e
	}
	for _, l := range b.Lines {
		if _, e = tx.Exec(`INSERT INTO order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,subtotal) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, l.Product.ID, l.Product.Name, l.Product.EffectivePrice(), l.Quantity, l.Product.SKU, l.Product.SaleUnit, l.Product.PriceBasis, l.Product.QuantityStep, l.Subtotal); e != nil {
			return 0, e
		}
	}
	if _, e = tx.Exec(`INSERT INTO working_order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step) SELECT order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step FROM order_items WHERE order_id=?`, id); e != nil {
		return 0, e
	}
	if _, e = tx.Exec(`DELETE FROM cart WHERE basket_id=?`, b.ID); e != nil {
		return 0, e
	}
	if _, e = tx.Exec(`UPDATE baskets SET revision=revision+1,hold_until=0 WHERE id=?`, b.ID); e != nil {
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
	return s.OrdersSearch(sid, manager, "")
}
func (s *Store) OrdersSearch(sid string, manager bool, search string) ([]Order, error) {
	rows, err := s.db.Query(`SELECT o.id,o.reference,o.status,o.created,o.total,o.order_version,o.final_total,o.completion_kind,COALESCE(SUM(CASE WHEN i.sale_unit='each' THEN i.picked_quantity ELSE 0 END),0),COALESCE(SUM(CASE WHEN i.sale_unit='each' THEN i.quantity ELSE 0 END),0),COALESCE(SUM((i.quantity-i.unavailable_quantity-i.cancelled_quantity)*i.price),0) FROM orders o LEFT JOIN working_order_items i ON i.order_id=o.id WHERE (? OR o.session_id=?) AND (?='' OR instr(lower(o.reference||' '||o.status||' '||o.completion_kind),lower(?))>0) GROUP BY o.id ORDER BY o.id DESC LIMIT 100`, manager, sid, search, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []Order
	for rows.Next() {
		var o Order
		var final sql.NullInt64
		if err = rows.Scan(&o.ID, &o.Reference, &o.Status, &o.Created, &o.Total, &o.Version, &final, &o.CompletionKind, &o.PickedCount, &o.RequiredCount, &o.WorkingTotal); err != nil {
			return nil, err
		}
		o.Finalized, o.FinalTotal = final.Valid, final.Int64
		setOrderProgress(&o)
		all = append(all, o)
	}
	return all, rows.Err()
}
func setOrderProgress(o *Order) {
	o.AllPicked = o.RequiredCount > 0 && o.PickedCount == o.RequiredCount && o.CompletionKind != "cancelled"
	if o.RequiredCount > 0 {
		o.Percent = 100 * o.PickedCount / o.RequiredCount
	}
}
func (s *Store) Order(id int64, sid string, manager bool) (Order, error) {
	var o Order
	var final sql.NullInt64
	err := s.db.QueryRow(`SELECT id,reference,status,created,total,instructions,order_version,final_total,completion_kind FROM orders WHERE id=? AND (? OR session_id=?)`, id, manager, sid).Scan(&o.ID, &o.Reference, &o.Status, &o.Created, &o.Total, &o.Instructions, &o.Version, &final, &o.CompletionKind)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	if err != nil {
		return o, err
	}
	o.Finalized, o.FinalTotal = final.Valid, final.Int64
	rows, err := s.db.Query(`SELECT i.product_id,i.name,i.price,i.quantity,COALESCE(w.picked_quantity,0),COALESCE(w.pick_version,i.pick_version),i.sku,i.sale_unit,i.price_basis,i.quantity_step,i.subtotal FROM order_items i LEFT JOIN working_order_items w ON w.order_id=i.order_id AND w.product_id=i.product_id WHERE i.order_id=? ORDER BY i.product_id`, id)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var i OrderItem
		if err = rows.Scan(&i.ProductID, &i.Name, &i.Price, &i.Quantity, &i.Picked, &i.PickVersion, &i.SKU, &i.SaleUnit, &i.PriceBasis, &i.QuantityStep, &i.Subtotal); err != nil {
			rows.Close()
			return o, err
		}
		o.Items = append(o.Items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return o, err
	}
	o.WorkingItems, err = workingOrderItems(s.db, id)
	if err != nil {
		return o, err
	}
	for _, i := range o.WorkingItems {
		o.WorkingTotal += i.Subtotal
		if i.SaleUnit == "each" {
			o.PickedCount += i.Picked
			o.RequiredCount += i.Quantity
		}
	}
	setOrderProgress(&o)
	o.WorkingPrices = make(map[int64]int64)
	rows, err = s.db.Query(`SELECT product_id,price FROM working_order_items WHERE order_id=?`, id)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var pid, price int64
		if err = rows.Scan(&pid, &price); err != nil {
			rows.Close()
			return o, err
		}
		o.WorkingPrices[pid] = price
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return o, err
	}
	rows, err = s.db.Query(`SELECT action,reason,details,created FROM order_events WHERE order_id=? ORDER BY id DESC LIMIT 100`, id)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var e OrderEvent
		if err = rows.Scan(&e.Action, &e.Reason, &e.Details, &e.Created); err != nil {
			return o, err
		}
		o.Events = append(o.Events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return o, err
	}
	o.Assignment, err = activeAssignment(s.db, id)
	return o, err
}
func (s *Store) Adjust(pid, delta, version int64, reason string) error {
	if err := s.ExpireHolds(); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if delta == 0 || delta < -10000 || delta > 10000 || len(reason) < 3 || len(reason) > 120 {
		return ErrInvalid
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	result, e := tx.Exec(`UPDATE products SET stock=stock+?,version=version+1 WHERE id=? AND version=? AND stock+? >= 0 AND stock+?+(SELECT COALESCE(SUM(reserved),0) FROM cart WHERE product_id=products.id)<=10000 AND archived=0`, delta, pid, version, delta, delta)
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
	return s.AdvanceVersioned(id, from, 0, sid, allOrders)
}
func (s *Store) AdvanceVersioned(id int64, from string, version int64, sid string, allOrders bool) error {
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
	var currentVersion int64
	err = tx.QueryRow(`SELECT status,order_version FROM orders WHERE id=? AND (? OR session_id=?)`, id, allOrders, sid).Scan(&current, &currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != from || (version != 0 && version != currentVersion) {
		return ErrConflict
	}
	if from == "Picking" {
		var total, incomplete int
		if err = tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN picked_quantity < quantity THEN 1 ELSE 0 END),0) FROM working_order_items WHERE order_id=? AND quantity>0`, id).Scan(&total, &incomplete); err != nil {
			return err
		}
		if total == 0 || incomplete != 0 {
			return ErrIncomplete
		}
	}
	if _, err = tx.Exec(`UPDATE orders SET status=?,order_version=order_version+1,final_total=CASE WHEN ? IN ('Ready','Completed') THEN COALESCE(final_total,(SELECT COALESCE(SUM(picked_quantity*price),0) FROM working_order_items WHERE order_id=orders.id)) ELSE NULL END,completion_kind=CASE WHEN ? IN ('Ready','Completed') THEN 'full' ELSE '' END WHERE id=? AND status=?`, next, next, next, id, from); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details) VALUES(?,?,'','status','Fulfillment transition',?)`, id, token(), from+" → "+next); err != nil {
		return err
	}
	if next == "Ready" || next == "Completed" {
		if err = endShopperAssignment(tx, id, next); err != nil {
			return err
		}
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
	err = tx.QueryRow(`SELECT quantity,pick_version FROM working_order_items WHERE order_id=? AND product_id=? AND quantity>0`, orderID, productID).Scan(&quantity, &currentVersion)
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
	if _, err = tx.Exec(`UPDATE working_order_items SET picked_quantity=?,pick_version=pick_version+1 WHERE order_id=? AND product_id=? AND pick_version=?`, picked, orderID, productID, version); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE orders SET order_version=order_version+1 WHERE id=?`, orderID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details) VALUES(?,?,'','pick','Picked count saved',?)`, orderID, token(), fmt.Sprintf("Product %d: picked %d", productID, picked)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Manager(sid string, on bool) error {
	until := int64(0)
	if on {
		until = s.now().Add(30 * time.Minute).Unix()
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
