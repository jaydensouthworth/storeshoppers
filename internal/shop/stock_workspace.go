package shop

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const stockPageSize = 12
const activityPageSize = 12

// StockFilters contains display/query state only, never write authorization.
type StockFilters struct {
	Search, State, Lifecycle, Unit, Sort, View string
	Department, Product, ActivityProduct       int64
	Page, ActivityPage                         int
	Action, From, To                           string
	DateError                                  string
}

type StockPage struct {
	Products                        []Product
	Total, Page, Pages, First, Last int
}
type StockSummary struct{ Products, Available, Reserved, Low int64 }
type StockEvent struct {
	Source, Action, Name, Details, Reason, Created, URL, LinkLabel string
	Delta                                                          int64
	Unit                                                           string
}
type StockActivity struct {
	Events                          []StockEvent
	Total, Page, Pages, First, Last int
}
type StockChoice struct {
	ID        int64
	Name, SKU string
	Archived  bool
}
type StockWorkspace struct {
	Filters   StockFilters
	Inventory StockPage
	Summary   StockSummary
	Activity  StockActivity
	Recent    []StockEvent
	Selected  *Product
	Choices   []StockChoice
}

func stockChoice(value string, choices ...string) string {
	for _, choice := range choices {
		if value == choice {
			return value
		}
	}
	return ""
}
func stockPositive(value string) int64 {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 {
		return 0
	}
	return n
}
func stockPageNumber(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 1000000 {
		return 1
	}
	return n
}
func parseStockFilters(v url.Values) StockFilters {
	search, _ := boundedManagerDraft(strings.TrimSpace(v.Get("q")), 100)
	f := StockFilters{Search: search, Department: stockPositive(v.Get("department")), Product: stockPositive(v.Get("product")), ActivityProduct: stockPositive(v.Get("activity_product")), Page: stockPageNumber(v.Get("page")), ActivityPage: stockPageNumber(v.Get("activity_page"))}
	f.State = stockChoice(v.Get("stock"), "available", "low", "out", "reserved")
	f.Lifecycle = stockChoice(v.Get("lifecycle"), "all", "archived")
	f.Unit = stockChoice(v.Get("unit"), "each", "g")
	f.Sort = stockChoice(v.Get("sort"), "name", "name_desc", "available", "available_desc", "reserved", "department")
	f.View = stockChoice(v.Get("view"), "activity")
	f.Action = stockChoice(v.Get("action"), "stock", "catalog-create", "catalog-edit", "catalog-archive", "catalog-restore", "basket-quantity", "basket-reserve", "basket-practice", "order-placed", "order-change")
	for _, field := range []struct {
		name string
		dst  *string
	}{{"from", &f.From}, {"to", &f.To}} {
		value := v.Get(field.name)
		if value == "" {
			continue
		}
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil || parsed.Format("2006-01-02") != value {
			f.DateError = "Enter valid dates in YYYY-MM-DD format."
			continue
		}
		*field.dst = value
	}
	if f.From != "" && f.To != "" && f.From > f.To {
		f.DateError = "The start date must be on or before the end date."
	}
	return f
}
func (f StockFilters) Values() url.Values {
	v := url.Values{}
	for key, value := range map[string]string{"q": f.Search, "stock": f.State, "lifecycle": f.Lifecycle, "unit": f.Unit, "sort": f.Sort, "view": f.View, "action": f.Action, "from": f.From, "to": f.To} {
		if value != "" {
			v.Set(key, value)
		}
	}
	for key, value := range map[string]int64{"department": f.Department, "product": f.Product, "activity_product": f.ActivityProduct} {
		if value > 0 {
			v.Set(key, strconv.FormatInt(value, 10))
		}
	}
	if f.Page > 1 {
		v.Set("page", strconv.Itoa(f.Page))
	}
	if f.ActivityPage > 1 {
		v.Set("activity_page", strconv.Itoa(f.ActivityPage))
	}
	return v
}
func stockURL(v url.Values, fragment string) string {
	path := "/manager/stock"
	if encoded := v.Encode(); encoded != "" {
		path += "?" + encoded
	}
	if fragment != "" {
		path += "#" + fragment
	}
	return path
}
func (f StockFilters) URL() string { return stockURL(f.Values(), "") }
func (f StockFilters) SelectURL(id int64) string {
	v := f.Values()
	v.Del("view")
	v.Set("product", strconv.FormatInt(id, 10))
	return stockURL(v, "stock-selection")
}
func (f StockFilters) CloseURL() string {
	v := f.Values()
	v.Del("product")
	return stockURL(v, "inventory")
}
func (f StockFilters) InventoryURL() string { v := f.Values(); v.Del("view"); return stockURL(v, "") }
func (f StockFilters) ActivityURL() string {
	v := f.Values()
	v.Set("view", "activity")
	return stockURL(v, "")
}
func (f StockFilters) ProductActivityURL(id int64) string {
	v := f.Values()
	v.Set("view", "activity")
	v.Set("activity_product", strconv.FormatInt(id, 10))
	v.Del("activity_page")
	return stockURL(v, "")
}
func (f StockFilters) InventoryPageURL(page int) string {
	v := f.Values()
	v.Set("page", strconv.Itoa(page))
	return stockURL(v, "inventory")
}
func (f StockFilters) ActivityPageURL(page int) string {
	v := f.Values()
	v.Set("activity_page", strconv.Itoa(page))
	return stockURL(v, "activity")
}
func (f StockFilters) ClearInventoryURL() string {
	v := f.Values()
	for _, key := range []string{"q", "stock", "lifecycle", "unit", "sort", "department", "page", "product"} {
		v.Del(key)
	}
	return stockURL(v, "")
}
func (f StockFilters) ClearActivityURL() string {
	v := f.Values()
	for _, key := range []string{"action", "activity_product", "from", "to", "activity_page"} {
		v.Del(key)
	}
	return stockURL(v, "")
}
func (f StockFilters) InventoryFiltered() bool {
	return f.Search != "" || f.State != "" || f.Lifecycle != "" || f.Unit != "" || f.Department != 0
}
func (f StockFilters) ActivityFiltered() bool {
	return f.Action != "" || f.ActivityProduct != 0 || f.From != "" || f.To != ""
}
func (p StockPage) Previous() int     { return p.Page - 1 }
func (p StockPage) Next() int         { return p.Page + 1 }
func (p StockActivity) Previous() int { return p.Page - 1 }
func (p StockActivity) Next() int     { return p.Page + 1 }
func stockBounds(total, page, size int) (int, int, int, int) {
	pages := (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	first, last := (page-1)*size+1, page*size
	if last > total {
		last = total
	}
	if total == 0 {
		first = 0
	}
	return page, pages, first, last
}

func stockWhere(f StockFilters) (string, []any) {
	parts := []string{"1=1"}
	args := []any{}
	switch f.Lifecycle {
	case "all":
	case "archived":
		parts = append(parts, "p.archived=1")
	default:
		parts = append(parts, "p.archived=0")
	}
	if f.Search != "" {
		parts = append(parts, `instr(lower(p.name || ' ' || p.sku || ' ' || c.name || ' ' || COALESCE(t.name,'')),lower(?))>0`)
		args = append(args, f.Search)
	}
	if f.Department > 0 {
		parts = append(parts, "p.category_id=?")
		args = append(args, f.Department)
	}
	if f.Unit != "" {
		parts = append(parts, "p.sale_unit=?")
		args = append(args, f.Unit)
	}
	switch f.State {
	case "available":
		parts = append(parts, "p.stock>0")
	case "low":
		parts = append(parts, "p.sale_unit='each' AND p.stock<8")
	case "out":
		parts = append(parts, "p.stock=0")
	case "reserved":
		parts = append(parts, "EXISTS(SELECT 1 FROM cart WHERE product_id=p.id AND reserved>0)")
	}
	return " WHERE " + strings.Join(parts, " AND "), args
}
func (s *Store) StockInventory(f StockFilters) (StockPage, error) {
	var result StockPage
	if err := s.ExpireHolds(); err != nil {
		return result, err
	}
	where, args := stockWhere(f)
	if err := s.db.QueryRow("SELECT COUNT(*)"+productJoins+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	result.Page, result.Pages, result.First, result.Last = stockBounds(result.Total, f.Page, stockPageSize)
	order := map[string]string{"name": "lower(p.name),p.id", "name_desc": "lower(p.name) DESC,p.id", "available": "p.stock,p.id", "available_desc": "p.stock DESC,p.id", "reserved": "(SELECT COALESCE(SUM(reserved),0) FROM cart WHERE product_id=p.id) DESC,p.id", "department": "lower(c.name),lower(p.name),p.id"}[f.Sort]
	if order == "" {
		order = "lower(p.name),p.id"
	}
	rows, err := s.db.Query("SELECT "+productSelect+productJoins+where+" ORDER BY "+order+" LIMIT ? OFFSET ?", append(args, stockPageSize, (result.Page-1)*stockPageSize)...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return result, err
		}
		result.Products = append(result.Products, p)
	}
	return result, rows.Err()
}
func (s *Store) StockSummary() (StockSummary, error) {
	var result StockSummary
	err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN sale_unit='each' THEN stock ELSE 0 END),0),COALESCE(SUM(CASE WHEN sale_unit='each' THEN (SELECT COALESCE(SUM(reserved),0) FROM cart WHERE product_id=p.id) ELSE 0 END),0),COALESCE(SUM(CASE WHEN sale_unit='each' AND stock<8 THEN 1 ELSE 0 END),0) FROM products p WHERE archived=0`).Scan(&result.Products, &result.Available, &result.Reserved, &result.Low)
	return result, err
}
func (s *Store) stockProduct(id int64) (*Product, error) {
	p, err := scanProduct(s.db.QueryRow("SELECT "+productSelect+productJoins+" WHERE p.id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
func (s *Store) stockChoices() ([]StockChoice, error) {
	rows, err := s.db.Query("SELECT id,name,sku,archived FROM products ORDER BY lower(name),id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var choices []StockChoice
	for rows.Next() {
		var p StockChoice
		if err = rows.Scan(&p.ID, &p.Name, &p.SKU, &p.Archived); err != nil {
			return nil, err
		}
		choices = append(choices, p)
	}
	return choices, rows.Err()
}

// Existing audit rows are authoritative. Order creation comes from orders.created;
// no status-transition history or historical stock balances are reconstructed.
// Product filtering excludes basket events: their old details are unstructured,
// and current basket contents cannot prove which product an old event changed.
const stockActivitySQL = `WITH events AS (
 SELECT 'stock' AS source,a.id AS event_id,'stock' AS action,p.name AS name,'' AS details,a.reason AS reason,a.created AS created,a.delta AS delta,a.sale_unit AS unit,p.id AS resource_id,'' AS basket_id,p.id AS product_id,'' AS resource_kind FROM adjustments a JOIN products p ON p.id=a.product_id
 UNION ALL
 SELECT 'catalog',e.id,'catalog-'||e.action,e.name,e.details,'',e.created,0,'',e.entity_id,'',CASE WHEN e.kind='product' THEN e.entity_id ELSE 0 END,e.kind FROM catalog_events e
 UNION ALL
 SELECT 'basket',e.id,CASE e.action WHEN 'set quantity' THEN 'basket-quantity' WHEN 'review and reserve' THEN 'basket-reserve' WHEN 'create practice' THEN 'basket-practice' ELSE 'basket-other' END,b.label,e.details,e.reason,e.created,0,'',0,b.id,0,'' FROM basket_events e JOIN baskets b ON b.id=e.basket_id WHERE (? OR b.owner_session_id=?)
 UNION ALL
 SELECT 'order',o.id,'order-placed',o.reference,'Demo order placed; receipt prices and quantities are preserved.','',o.created,0,'',o.id,'',0,'' FROM orders o WHERE (? OR o.session_id=?)
 UNION ALL
 SELECT 'order',e.id,'order-change',o.reference,e.action||': '||e.details,e.reason,e.created,0,'',o.id,'',0,'' FROM order_events e JOIN orders o ON o.id=e.order_id WHERE (? OR o.session_id=?)
) `

func (s *Store) StockActivity(f StockFilters, sid string, all bool) (StockActivity, error) {
	var result StockActivity
	if f.DateError != "" {
		result.Page, result.Pages = 1, 1
		return result, nil
	}
	where := ` WHERE (?='' OR action=?) AND (?='' OR substr(created,1,10)>=?) AND (?='' OR substr(created,1,10)<=?) AND (?=0 OR product_id=? OR (source='order' AND EXISTS(SELECT 1 FROM working_order_items i WHERE i.order_id=events.resource_id AND i.product_id=?)))`
	args := []any{all, sid, all, sid, all, sid, f.Action, f.Action, f.From, f.From, f.To, f.To, f.ActivityProduct, f.ActivityProduct, f.ActivityProduct}
	if err := s.db.QueryRow(stockActivitySQL+"SELECT COUNT(*) FROM events"+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	result.Page, result.Pages, result.First, result.Last = stockBounds(result.Total, f.ActivityPage, activityPageSize)
	rows, err := s.db.Query(stockActivitySQL+`SELECT source,action,name,details,reason,created,delta,unit,resource_id,basket_id,resource_kind FROM events`+where+" ORDER BY created DESC,source,event_id DESC,action LIMIT ? OFFSET ?", append(args, activityPageSize, (result.Page-1)*activityPageSize)...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var e StockEvent
		var id int64
		var basket, kind string
		if err = rows.Scan(&e.Source, &e.Action, &e.Name, &e.Details, &e.Reason, &e.Created, &e.Delta, &e.Unit, &id, &basket, &kind); err != nil {
			return result, err
		}
		switch e.Source {
		case "stock":
			e.URL = fmt.Sprintf("/manager/stock?product=%d#stock-selection", id)
			e.LinkLabel = "Review stock"
		case "catalog":
			if kind == "product" {
				e.URL = fmt.Sprintf("/manager/catalog?edit=%d", id)
				e.LinkLabel = "Review product"
			} else {
				e.URL = "/manager/catalog?tab=labels"
				e.LinkLabel = "Review labels"
			}
		case "basket":
			e.URL = "/manager/baskets/" + url.PathEscape(basket)
			e.LinkLabel = "Review basket"
		case "order":
			e.URL = fmt.Sprintf("/manager/orders/%d", id)
			e.LinkLabel = "Open pick ticket"
		}
		e.Action = map[string]string{"stock": "Stock adjustment", "catalog-create": "Catalog created", "catalog-edit": "Catalog edited", "catalog-archive": "Catalog archived", "catalog-restore": "Catalog restored", "basket-quantity": "Basket quantity changed", "basket-reserve": "Basket reviewed & reserved", "basket-practice": "Practice basket created", "basket-other": "Basket action", "order-placed": "Order placed", "order-change": "Order changed"}[e.Action]
		result.Events = append(result.Events, e)
	}
	return result, rows.Err()
}
