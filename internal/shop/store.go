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
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err = s.seed(); err != nil {
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
func (s *Store) seed() error {
	products := []Product{
		{1, "Honeycrisp apples", "Crisp, sweet and ready for the fruit bowl.", "Produce", "000000000101", "apple", 349, 24, 1},
		{2, "Baby spinach", "Tender leaves for quick lunches and green dinners.", "Produce", "000000000102", "leaf", 299, 18, 1},
		{3, "Sourdough loaf", "A golden crust and a soft, tangy center.", "Bakery", "000000000103", "bread", 599, 10, 1},
		{4, "Whole milk", "A half gallon of the everyday essential.", "Dairy", "000000000104", "milk", 429, 16, 1},
		{5, "Free range eggs", "A dozen large eggs for a well-stocked kitchen.", "Dairy", "000000000105", "egg", 649, 6, 1},
		{6, "Penne pasta", "A pantry staple made for your favorite sauce.", "Pantry", "000000000106", "pasta", 249, 30, 1},
		{7, "Extra virgin olive oil", "Smooth and peppery. Finish something delicious.", "Pantry", "000000000107", "oil", 1099, 8, 1},
		{8, "Strawberry jam", "Small-batch style preserves for your morning toast.", "Pantry", "000000000108", "jam", 479, 0, 1},
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range products {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO products(id,name,description,category,barcode,icon,price,stock) VALUES(?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.Description, p.Category, p.Barcode, p.Icon, p.Price, p.Stock); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	rows, err := s.db.Query(`SELECT id,name,description,category,barcode,icon,price,stock,version FROM products WHERE (?='' OR instr(lower(name || ' ' || description),lower(?))>0) AND (?='' OR category=?) ORDER BY id`, search, search, category, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []Product
	for rows.Next() {
		var p Product
		if err = rows.Scan(&p.ID, &p.Name, &p.Description, &p.Category, &p.Barcode, &p.Icon, &p.Price, &p.Stock, &p.Version); err != nil {
			return nil, err
		}
		all = append(all, p)
	}
	return all, rows.Err()
}

type querier interface {
	Query(string, ...any) (*sql.Rows, error)
}

func basket(q querier, sid string) (Basket, error) {
	b := Basket{CanCheckout: true}
	rows, err := q.Query(`SELECT p.id,p.name,p.description,p.category,p.barcode,p.icon,p.price,p.stock,p.version,c.quantity FROM cart c JOIN products p ON p.id=c.product_id WHERE c.session_id=? ORDER BY p.id`, sid)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var l CartLine
		p := &l.Product
		if err = rows.Scan(&p.ID, &p.Name, &p.Description, &p.Category, &p.Barcode, &p.Icon, &p.Price, &p.Stock, &p.Version, &l.Quantity); err != nil {
			return b, err
		}
		l.Subtotal = l.Quantity * p.Price
		b.Total += l.Subtotal
		b.Count += l.Quantity
		if l.Quantity > p.Stock {
			b.CanCheckout = false
		}
		b.Lines = append(b.Lines, l)
	}
	if b.Count == 0 {
		b.CanCheckout = false
	}
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
	if e = tx.QueryRow(`SELECT stock FROM products WHERE id=?`, pid).Scan(&stock); e != nil {
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
func (s *Store) Checkout(sid, key string, revision int64) (int64, error) {
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
	if b.Count == 0 {
		return 0, ErrEmpty
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
		result, e = tx.Exec(`UPDATE products SET stock=stock-?,version=version+1 WHERE id=? AND stock>=?`, l.Quantity, l.Product.ID, l.Quantity)
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
		if _, e = tx.Exec(`INSERT INTO order_items(order_id,product_id,name,price,quantity) VALUES(?,?,?,?,?)`, id, l.Product.ID, l.Product.Name, l.Product.Price, l.Quantity); e != nil {
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
	rows, e := s.db.Query(`SELECT id,reference,status,created,total FROM orders WHERE (? OR session_id=?) ORDER BY id DESC LIMIT 100`, manager, sid)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var all []Order
	for rows.Next() {
		var o Order
		if e = rows.Scan(&o.ID, &o.Reference, &o.Status, &o.Created, &o.Total); e != nil {
			return nil, e
		}
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
	rows, e := s.db.Query(`SELECT name,price,quantity FROM order_items WHERE order_id=? ORDER BY product_id`, id)
	if e != nil {
		return o, e
	}
	defer rows.Close()
	for rows.Next() {
		var i OrderItem
		if e = rows.Scan(&i.Name, &i.Price, &i.Quantity); e != nil {
			return o, e
		}
		i.Subtotal = i.Price * i.Quantity
		o.Items = append(o.Items, i)
	}
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
	result, e := tx.Exec(`UPDATE products SET stock=stock+?,version=version+1 WHERE id=? AND version=? AND stock+? BETWEEN 0 AND 10000`, delta, pid, version, delta)
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
	if _, e = tx.Exec(`INSERT INTO adjustments(product_id,delta,reason) VALUES(?,?,?)`, pid, delta, reason); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Adjustments() ([]Adjustment, error) {
	rows, e := s.db.Query(`SELECT p.name,a.delta,a.reason,a.created FROM adjustments a JOIN products p ON p.id=a.product_id ORDER BY a.id DESC LIMIT 10`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var all []Adjustment
	for rows.Next() {
		var a Adjustment
		if e = rows.Scan(&a.Product, &a.Delta, &a.Reason, &a.Created); e != nil {
			return nil, e
		}
		all = append(all, a)
	}
	return all, rows.Err()
}
func (s *Store) Advance(id int64, from string) error {
	next := map[string]string{"Placed": "Picking", "Picking": "Ready", "Ready": "Completed"}[from]
	if next == "" {
		return ErrInvalid
	}
	r, e := s.db.Exec(`UPDATE orders SET status=? WHERE id=? AND status=?`, next, id, from)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
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
