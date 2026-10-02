package shop

import (
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
	db, err := sql.Open(applicationSQLiteDriver, u.String())
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
	tx, err := s.beginWrite()
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
	tx, e := s.beginWrite()
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
		if l.Product.Archived {
			return 0, ErrUnavailable
		}
	}
	if len(quote) != 64 || quote != b.Quote {
		return 0, ErrQuote
	}
	if b.NeedsReview || b.HoldUntil <= now {
		return 0, ErrHold
	}
	if b.Total == 0 {
		return 0, ErrZeroEstimate
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
	if _, e = tx.Exec(`INSERT INTO working_order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,allocated_quantity) SELECT order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,quantity FROM order_items WHERE order_id=?`, id); e != nil {
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

// OrdersSearch is a customer-safe first-page convenience. HTTP queues expose
// the full count and navigation through readOrderQueue/OrderQueue.
func (s *Store) OrdersSearch(sid string, allOrders bool, search string) ([]Order, error) {
	q, err := s.readOrderQueue(sid, allOrders, OrderFilters{Query: search}, false)
	return q.Orders, err
}
func setOrderProgress(o *Order) {
	o.AllPicked = o.RequiredCount > 0 && o.PickedCount == o.RequiredCount && o.CompletionKind != "cancelled"
	if o.RequiredCount > 0 {
		o.Percent = 100 * o.PickedCount / o.RequiredCount
	}
}

// Order is customer-safe regardless of its ownership-scope flag.
func (s *Store) Order(id int64, sid string, allOrders bool) (Order, error) {
	return s.readOrder(id, sid, allOrders, false)
}

// ManagerOrder explicitly opts into private manager history after the HTTP gate.
func (s *Store) ManagerOrder(id int64, sid string, allOrders bool) (Order, error) {
	return s.readOrder(id, sid, allOrders, true)
}
func (s *Store) readOrder(id int64, sid string, allOrders, private bool) (Order, error) {
	var o Order
	var final sql.NullInt64
	err := s.db.QueryRow(`SELECT id,reference,status,created,total,instructions,order_version,final_total,completion_kind,attention_reason<>'' AND status IN ('Placed','Picking'),CASE WHEN ? THEN attention_reason ELSE '' END,CASE WHEN ? THEN attention_since ELSE 0 END FROM orders WHERE id=? AND (? OR session_id=?)`, private, private, id, allOrders, sid).Scan(&o.ID, &o.Reference, &o.Status, &o.Created, &o.Total, &o.Instructions, &o.Version, &final, &o.CompletionKind, &o.Held, &o.AttentionReason, &o.AttentionSince)
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
	if err = summarizeWorking(&o, o.WorkingItems); err != nil {
		return o, err
	}
	o.WorkingPrices = make(map[int64]int64)
	o.WorkingSteps = make(map[int64]int64)
	rows, err = s.db.Query(`SELECT product_id,price,quantity_step FROM working_order_items WHERE order_id=?`, id)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var pid, price, step int64
		if err = rows.Scan(&pid, &price, &step); err != nil {
			rows.Close()
			return o, err
		}
		o.WorkingPrices[pid] = price
		o.WorkingSteps[pid] = step
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return o, err
	}
	rows, err = s.db.Query(`SELECT action,reason,details,created,visibility FROM order_events WHERE order_id=? AND (? OR visibility='customer') ORDER BY id DESC LIMIT 100`, id, private)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var e OrderEvent
		if err = rows.Scan(&e.Action, &e.Reason, &e.Details, &e.Created, &e.Visibility); err != nil {
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
	if delta == 0 || delta < -maxGramStock || delta > maxGramStock || len(reason) < 3 || len(reason) > 120 {
		return ErrInvalid
	}
	tx, e := s.beginWrite()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var unit string
	if err := tx.QueryRow(`SELECT sale_unit FROM products WHERE id=?`, pid).Scan(&unit); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if delta < -stockLimit(unit) || delta > stockLimit(unit) {
		return ErrInvalid
	}
	result, e := tx.Exec(`UPDATE products SET stock=stock+?,version=version+1 WHERE id=? AND version=? AND stock+? >= 0 AND stock+?+(SELECT COALESCE(SUM(reserved),0) FROM cart WHERE product_id=products.id)<=CASE sale_unit WHEN 'g' THEN 1000000 ELSE 10000 END AND ABS(?)<=CASE sale_unit WHEN 'g' THEN 1000000 ELSE 10000 END AND archived=0`, delta, pid, version, delta, delta, delta)
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
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	var currentVersion int64
	var held bool
	err = tx.QueryRow(`SELECT status,order_version,attention_reason<>'' FROM orders WHERE id=? AND (? OR session_id=?)`, id, allOrders, sid).Scan(&current, &currentVersion, &held)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != from || (version != 0 && version != currentVersion) {
		return ErrConflict
	}
	if held && next == "Ready" {
		return ErrAttention
	}
	var final int64
	if next == "Ready" || next == "Completed" {
		items, e := workingOrderItems(tx, id)
		if e != nil {
			return e
		}
		if from == "Picking" && len(items) == 0 {
			return ErrIncomplete
		}
		for _, item := range items {
			if from == "Picking" && !item.Complete() {
				return ErrIncomplete
			}
			amount, e := lineAmount(item.Picked, item.Price, item.PriceBasis, item.SaleUnit)
			if e != nil {
				return e
			}
			final, e = addAmount(final, amount)
			if e != nil {
				return e
			}
		}
	}
	if _, err = tx.Exec(`UPDATE orders SET status=?,order_version=order_version+1,final_total=CASE WHEN ? IN ('Ready','Completed') THEN COALESCE(final_total,?) ELSE NULL END,completion_kind=CASE WHEN ? IN ('Ready','Completed') THEN 'full' ELSE '' END WHERE id=? AND status=?`, next, next, final, next, id, from); err != nil {
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
	tx, err := s.beginWrite()
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
	err = tx.QueryRow(`SELECT quantity,pick_version FROM working_order_items WHERE order_id=? AND product_id=? AND quantity>0 AND sale_unit='each'`, orderID, productID).Scan(&quantity, &currentVersion)
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
	tx, e := s.beginWrite()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	r, e := tx.Exec(`UPDATE sessions SET manager_until=?,csrf=? WHERE id=?`, until, token(), sid)
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
	return tx.Commit()
}
