package shop

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrPromotionOverlap = errors.New("This product already has a sale in that time window. Edit or cancel the overlapping sale first.")
var ErrPromotionPrice = errors.New("Sale prices must be below the regular price. Adjust or cancel active/upcoming sales before changing their regular price or selling unit.")
var ErrExampleSales = errors.New("Example sales require the unchanged demo apples, bread and milk, with no overlapping sales or existing featured choices. Nothing was changed.")

type Promotion struct {
	ID, ProductID, SalePrice, Starts, Ends, Version int64
	ProductVersion, RegularPrice                    int64
	ProductName, SKU, Status                        string
	Cancelled, Unavailable                          bool
}

func (p Promotion) StartInput() string {
	return time.Unix(p.Starts, 0).UTC().Format("2006-01-02T15:04")
}
func (p Promotion) EndInput() string   { return time.Unix(p.Ends, 0).UTC().Format("2006-01-02T15:04") }
func (p Promotion) StartLabel() string { return utcSaleLabel(p.Starts) }
func (p Promotion) EndLabel() string   { return utcSaleLabel(p.Ends) }
func utcSaleLabel(n int64) string      { return time.Unix(n, 0).UTC().Format("2 Jan 2006, 15:04 UTC") }
func (p Product) OnSale() bool         { return p.PromotionID > 0 && p.SalePrice > 0 }
func (p Product) EffectivePrice() int64 {
	if p.OnSale() {
		return p.SalePrice
	}
	return p.Price
}
func (p Product) SaleEndLabel() string {
	return time.Unix(p.SaleEnds, 0).UTC().Format("2 Jan, 15:04 UTC")
}

type PromotionEvent struct {
	ID, ProductID, PromotionID     int64
	Action, Name, Details, Created string
}
type featuredPrice struct {
	Featured bool
	Version  int64
}
type pricingState struct {
	sales      map[int64]Promotion
	features   map[int64]featuredPrice
	boundaries map[int64]int64
}

// Capture one Store-clock instant per operation and load these rows before any
// product cursor. A transaction caller sees rates and product data atomically.
func loadPricing(q querier, now int64) (pricingState, error) {
	state := pricingState{sales: map[int64]Promotion{}, features: map[int64]featuredPrice{}, boundaries: map[int64]int64{}}
	rows, err := q.Query(`SELECT id,product_id,sale_price,starts,ends,version FROM promotions WHERE cancelled=0 AND starts<=? AND ends>? ORDER BY id`, now, now)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var p Promotion
		if err = rows.Scan(&p.ID, &p.ProductID, &p.SalePrice, &p.Starts, &p.Ends, &p.Version); err != nil {
			rows.Close()
			return state, err
		}
		state.sales[p.ProductID] = p
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return state, err
	}
	rows, err = q.Query(`SELECT product_id,MAX(MAX(CASE WHEN starts<=? THEN starts ELSE 0 END,CASE WHEN ends<=? THEN ends ELSE 0 END)) FROM promotions WHERE cancelled=0 GROUP BY product_id`, now, now)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var id, boundary int64
		if err = rows.Scan(&id, &boundary); err != nil {
			rows.Close()
			return state, err
		}
		state.boundaries[id] = boundary
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return state, err
	}
	rows, err = q.Query(`SELECT product_id,featured,version FROM product_features`)
	if err != nil {
		return state, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var f featuredPrice
		if err = rows.Scan(&id, &f.Featured, &f.Version); err != nil {
			return state, err
		}
		state.features[id] = f
	}
	return state, rows.Err()
}
func (s pricingState) apply(p *Product) {
	p.PricingBoundary = s.boundaries[p.ID]
	f := s.features[p.ID]
	p.Featured = f.Featured
	p.FeatureVersion = f.Version
	if sale, ok := s.sales[p.ID]; ok && !p.Archived && p.SaleUnit == "each" && sale.SalePrice < p.Price {
		p.SalePrice, p.PromotionID, p.PromotionVersion, p.SaleStarts, p.SaleEnds = sale.SalePrice, sale.ID, sale.Version, sale.Starts, sale.Ends
	}
}

const promotionSelect = `s.id,s.product_id,s.sale_price,s.starts,s.ends,s.version,s.cancelled,p.name,p.sku,p.price,p.price_version,(p.archived OR c.archived OR COALESCE(t.archived,0) OR p.sale_unit<>'each') FROM promotions s JOIN products p ON p.id=s.product_id JOIN categories c ON c.id=p.category_id LEFT JOIN product_types t ON t.id=p.type_id `

func scanPromotion(row scanner, now int64) (Promotion, error) {
	var p Promotion
	err := row.Scan(&p.ID, &p.ProductID, &p.SalePrice, &p.Starts, &p.Ends, &p.Version, &p.Cancelled, &p.ProductName, &p.SKU, &p.RegularPrice, &p.ProductVersion, &p.Unavailable)
	switch {
	case p.Cancelled:
		p.Status = "Cancelled"
	case now < p.Starts:
		p.Status = "Upcoming"
	case now >= p.Ends:
		p.Status = "Expired"
	default:
		p.Status = "Active"
	}
	return p, err
}
func (s *Store) Promotions() ([]Promotion, error) {
	now := s.now().Unix()
	rows, err := s.db.Query(`SELECT ` + promotionSelect + ` ORDER BY s.starts DESC,s.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []Promotion
	for rows.Next() {
		p, e := scanPromotion(rows, now)
		if e != nil {
			return nil, e
		}
		all = append(all, p)
	}
	return all, rows.Err()
}
func promotionAudit(tx *sql.Tx, p Promotion, action string, now int64) error {
	details := fmt.Sprintf("%s; starts %s, ends %s; SKU %s", Money(p.SalePrice), p.StartLabel(), p.EndLabel(), p.SKU)
	_, err := tx.Exec(`INSERT INTO promotion_events(product_id,promotion_id,action,name,details,created) VALUES(?,?,?,?,?,?)`, p.ProductID, p.ID, action, p.ProductName, details, time.Unix(now, 0).UTC().Format("2006-01-02 15:04:05 UTC"))
	return err
}
func promotionProduct(tx *sql.Tx, id int64) (Product, error) {
	p, err := scanProduct(tx.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	var inactive bool
	if err = tx.QueryRow(`SELECT c.archived OR COALESCE(t.archived,0) FROM products p JOIN categories c ON c.id=p.category_id LEFT JOIN product_types t ON t.id=p.type_id WHERE p.id=?`, id).Scan(&inactive); err != nil {
		return p, err
	}
	if p.Archived || inactive || p.SaleUnit != "each" {
		return p, ErrUnavailable
	}
	return p, nil
}
func savePromotion(tx *sql.Tx, p Promotion, now int64, action string) (int64, error) {
	if p.ID < 0 || p.ProductID < 1 || p.SalePrice < 1 || p.SalePrice > 1000000 || p.Starts < 0 || p.Ends <= p.Starts || p.Ends > 253402300799 || p.Cancelled {
		return 0, ErrInvalid
	}
	product, err := promotionProduct(tx, p.ProductID)
	if err != nil {
		return 0, err
	}
	if p.ProductVersion < 1 || product.PriceVersion != p.ProductVersion {
		return 0, ErrConflict
	}
	if p.SalePrice >= product.Price {
		return 0, ErrPromotionPrice
	}
	if p.ID > 0 {
		var priorProduct, version int64
		var cancelled bool
		err = tx.QueryRow(`SELECT product_id,version,cancelled FROM promotions WHERE id=?`, p.ID).Scan(&priorProduct, &version, &cancelled)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		if err != nil {
			return 0, err
		}
		if priorProduct != p.ProductID || version != p.Version || cancelled {
			return 0, ErrConflict
		}
	}
	var overlaps int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM promotions WHERE product_id=? AND id<>? AND cancelled=0 AND starts<? AND ends>?`, p.ProductID, p.ID, p.Ends, p.Starts).Scan(&overlaps); err != nil {
		return 0, err
	}
	if overlaps > 0 {
		return 0, ErrPromotionOverlap
	}
	if p.ID == 0 {
		r, e := tx.Exec(`INSERT INTO promotions(product_id,sale_price,starts,ends) VALUES(?,?,?,?)`, p.ProductID, p.SalePrice, p.Starts, p.Ends)
		if e != nil {
			return 0, e
		}
		p.ID, err = r.LastInsertId()
	} else {
		_, err = tx.Exec(`UPDATE promotions SET sale_price=?,starts=?,ends=?,version=version+1 WHERE id=?`, p.SalePrice, p.Starts, p.Ends, p.ID)
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`UPDATE products SET price_version=price_version+1 WHERE id=?`, p.ProductID); err != nil {
		return 0, err
	}
	p.ProductName, p.SKU = product.Name, product.SKU
	if err = promotionAudit(tx, p, action, now); err != nil {
		return 0, err
	}
	return p.ID, nil
}
func (s *Store) SavePromotion(p Promotion) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	action := "create"
	if p.ID > 0 {
		action = "edit"
	}
	id, err := savePromotion(tx, p, s.now().Unix(), action)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
func (s *Store) CancelPromotion(id, version int64) error {
	if id < 1 || version < 1 {
		return ErrInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	p, err := scanPromotion(tx.QueryRow(`SELECT `+promotionSelect+`WHERE s.id=?`, id), now)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if p.Version != version || p.Cancelled {
		return ErrConflict
	}
	if _, err = tx.Exec(`UPDATE promotions SET cancelled=1,version=version+1 WHERE id=?`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE products SET price_version=price_version+1 WHERE id=?`, p.ProductID); err != nil {
		return err
	}
	if err = promotionAudit(tx, p, "cancel", now); err != nil {
		return err
	}
	return tx.Commit()
}
func setFeatured(tx *sql.Tx, id, version int64, featured bool, now int64) error {
	if id < 1 || version < 0 {
		return ErrInvalid
	}
	p, err := scanProduct(tx.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if featured {
		if _, err = promotionProduct(tx, id); err != nil {
			return err
		}
	}
	var current int64
	err = tx.QueryRow(`SELECT version FROM product_features WHERE product_id=?`, id).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current != version {
		return ErrConflict
	}
	if current == 0 {
		_, err = tx.Exec(`INSERT INTO product_features(product_id,featured,version) VALUES(?,?,1)`, id, featured)
	} else {
		_, err = tx.Exec(`UPDATE product_features SET featured=?,version=version+1 WHERE product_id=?`, featured, id)
	}
	if err != nil {
		return err
	}
	action := "unfeature"
	if featured {
		action = "feature"
	}
	_, err = tx.Exec(`INSERT INTO promotion_events(product_id,action,name,details,created) VALUES(?,?,?,?,?)`, id, action, p.Name, fmt.Sprintf("Featured: %t; SKU %s", featured, p.SKU), time.Unix(now, 0).UTC().Format("2006-01-02 15:04:05 UTC"))
	return err
}
func (s *Store) SetFeatured(id, version int64, featured bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = setFeatured(tx, id, version, featured, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) PromotionEvents() ([]PromotionEvent, error) {
	rows, err := s.db.Query(`SELECT id,product_id,COALESCE(promotion_id,0),action,name,details,created FROM promotion_events ORDER BY id DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []PromotionEvent
	for rows.Next() {
		var e PromotionEvent
		if err = rows.Scan(&e.ID, &e.ProductID, &e.PromotionID, &e.Action, &e.Name, &e.Details, &e.Created); err != nil {
			return nil, err
		}
		all = append(all, e)
	}
	return all, rows.Err()
}
func utcWeek(t time.Time) (int64, int64) {
	t = t.UTC()
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(int(t.Weekday())+6)%7)
	return start.Unix(), start.AddDate(0, 0, 7).Unix()
}

// Demo examples never overwrite existing offers or merchandising choices. The
// preview binds all rates, product versions and the UTC week to one reviewed hash.
func exampleSales(q querier, now int64) ([]Promotion, string, error) {
	start, end := utcWeek(time.Unix(now, 0))
	spec := []struct {
		id, regular, sale int64
		name              string
	}{{1, 349, 249, "Honeycrisp apples"}, {3, 599, 399, "Sourdough loaf"}, {4, 429, 329, "Whole milk"}}
	var all []Promotion
	h := sha256.New()
	for _, want := range spec {
		var name, sku, unit string
		var regular, priceVersion, catalogVersion int64
		var archived bool
		err := q.QueryRow(`SELECT p.name,p.sku,p.sale_unit,p.price,p.price_version,p.catalog_version,(p.archived OR c.archived OR COALESCE(t.archived,0)) FROM products p JOIN categories c ON c.id=p.category_id LEFT JOIN product_types t ON t.id=p.type_id WHERE p.id=?`, want.id).Scan(&name, &sku, &unit, &regular, &priceVersion, &catalogVersion, &archived)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrExampleSales
		}
		if err != nil {
			return nil, "", err
		}
		if name != want.name || sku != fmt.Sprintf("SHOPDEMO-%06d", want.id) || unit != "each" || regular != want.regular || archived || catalogVersion != 1 || priceVersion != 1 {
			return nil, "", ErrExampleSales
		}
		var conflicts int
		if err = q.QueryRow(`SELECT (SELECT COUNT(*) FROM promotions WHERE product_id=? AND cancelled=0 AND starts<? AND ends>?) + (SELECT COUNT(*) FROM product_features WHERE product_id=?)`, want.id, end, start, want.id).Scan(&conflicts); err != nil {
			return nil, "", err
		}
		if conflicts > 0 {
			return nil, "", ErrExampleSales
		}
		p := Promotion{ProductID: want.id, ProductName: name, SKU: sku, SalePrice: want.sale, RegularPrice: regular, ProductVersion: priceVersion, Starts: start, Ends: end, Status: "Active"}
		all = append(all, p)
		fmt.Fprintf(h, "%d:%d:%d:%d:%d:%d:%s;", want.id, regular, want.sale, priceVersion, start, end, name)
	}
	return all, hex.EncodeToString(h.Sum(nil)), nil
}
func (s *Store) ExampleSales() ([]Promotion, string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	return exampleSales(tx, s.now().Unix())
}
func (s *Store) CreateExampleSales(key, quote string) error {
	if len(key) < 16 || len(key) > 150 || len(quote) != 64 || strings.TrimSpace(key) != key {
		return ErrInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prior string
	err = tx.QueryRow(`SELECT command_hash FROM promotion_commands WHERE command_key=?`, key).Scan(&prior)
	if err == nil {
		if prior == quote {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := s.now().Unix()
	examples, current, err := exampleSales(tx, now)
	if err != nil {
		return err
	}
	if current != quote {
		return ErrConflict
	}
	for _, p := range examples {
		if _, err = savePromotion(tx, p, now, "example"); err != nil {
			return err
		}
		if err = setFeatured(tx, p.ProductID, 0, true, now); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO promotion_commands(command_key,command_hash) VALUES(?,?)`, key, quote); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) seedExampleSales() error {
	_, quote, err := s.ExampleSales()
	if err != nil {
		return err
	}
	return s.CreateExampleSales("explicit-reset-examples", quote)
}
