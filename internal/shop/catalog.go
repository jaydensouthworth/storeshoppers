package shop

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-sqlite3"
)

// Catalog writes keep merchant SKUs separate from barcode identities. Archived
// rows and their unique identifiers are retained permanently by the application.
const productSelect = `p.id,p.name,p.description,c.name,p.barcode,p.icon,p.price,p.stock,p.version,p.sku,COALESCE(t.name,''),p.sale_unit,p.category_id,COALESCE(p.type_id,0),p.archived,p.catalog_version,p.price_version,p.price_basis,p.quantity_step,(SELECT COALESCE(SUM(reserved),0) FROM cart WHERE product_id=p.id)`
const productJoins = ` FROM products p JOIN categories c ON c.id=p.category_id LEFT JOIN product_types t ON t.id=p.type_id `

type scanner interface{ Scan(...any) error }

func productFields(p *Product) []any {
	return []any{&p.ID, &p.Name, &p.Description, &p.Category, &p.Barcode, &p.Icon, &p.Price, &p.Stock, &p.Version, &p.SKU, &p.ProductType, &p.SaleUnit, &p.CategoryID, &p.TypeID, &p.Archived, &p.CatalogVersion, &p.PriceVersion, &p.PriceBasis, &p.QuantityStep, &p.Reserved}
}
func scanProduct(row scanner) (Product, error) {
	var p Product
	err := row.Scan(productFields(&p)...)
	return p, err
}
func (s *Store) catalogProducts(search, category string, archived bool) ([]Product, error) {
	if err := s.ExpireHolds(); err != nil {
		return nil, err
	}
	state, err := loadPricing(s.db, s.now().Unix())
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT `+productSelect+productJoins+`WHERE (? OR p.archived=0) AND (?='' OR instr(lower(p.name || ' ' || p.description),lower(?))>0) AND (?='' OR c.name=?) ORDER BY p.id`, archived, search, search, category, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []Product
	for rows.Next() {
		p, e := scanProduct(rows)
		if e != nil {
			return nil, e
		}
		state.apply(&p)
		all = append(all, p)
	}
	return all, rows.Err()
}
func (s *Store) CatalogProducts() ([]Product, error) { return s.catalogProducts("", "", true) }
func taxonomyTable(kind string) (string, error) {
	switch kind {
	case "category":
		return "categories", nil
	case "type":
		return "product_types", nil
	}
	return "", ErrInvalid
}
func normalizedName(name string) (string, string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 80 {
		return "", "", ErrInvalid
	}
	return name, strings.ToLower(name), nil
}
func catalogError(err error) error {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && (sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique || sqliteErr.ExtendedCode == sqlite3.ErrConstraintPrimaryKey) {
		return ErrDuplicate
	}
	return err
}
func (s *Store) Taxonomies(kind string, includeArchived bool) ([]Taxonomy, error) {
	table, err := taxonomyTable(kind)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,name,archived,version FROM `+table+` WHERE (? OR archived=0) ORDER BY normalized_name`, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []Taxonomy
	for rows.Next() {
		var t Taxonomy
		if err = rows.Scan(&t.ID, &t.Name, &t.Archived, &t.Version); err != nil {
			return nil, err
		}
		all = append(all, t)
	}
	return all, rows.Err()
}
func (s *Store) SaveTaxonomy(kind string, id, version int64, name string) (int64, error) {
	table, err := taxonomyTable(kind)
	if err != nil {
		return 0, err
	}
	name, normalized, err := normalizedName(name)
	if err != nil || id < 0 {
		return 0, ErrInvalid
	}

	if id != 0 && version < 1 {
		return 0, ErrInvalid
	}
	tx, err := s.beginWrite()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if id == 0 {
		r, e := tx.Exec(`INSERT INTO `+table+`(name,normalized_name) VALUES(?,?)`, name, normalized)
		if e != nil {
			return 0, catalogError(e)
		}
		id, e = r.LastInsertId()
		if e != nil {
			return 0, e
		}
		if e = catalogEvent(tx, kind, id, "create", name, "Created catalog label"); e != nil {
			return 0, e
		}
		if e = tx.Commit(); e != nil {
			return 0, e
		}
		return id, nil
	}
	var before string
	if err = tx.QueryRow(`SELECT name FROM `+table+` WHERE id=?`, id).Scan(&before); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, err
	}
	r, err := tx.Exec(`UPDATE `+table+` SET name=?,normalized_name=?,version=version+1 WHERE id=? AND version=? AND archived=0`, name, normalized, id, version)
	if err != nil {
		return 0, catalogError(err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, ErrConflict
	}
	// Keep the legacy text column consistent; relational category_id is canonical.
	if kind == "category" {
		if _, err = tx.Exec(`UPDATE products SET category=? WHERE category_id=?`, name, id); err != nil {
			return 0, err
		}
	}
	if err = catalogEvent(tx, kind, id, "edit", name, "Previous name: "+before); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}
func (s *Store) ArchiveTaxonomy(kind string, id, version int64) error {
	table, err := taxonomyTable(kind)
	if err != nil {
		return err
	}
	if id < 1 || version < 1 {
		return ErrInvalid
	}
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	var archived bool
	var name string
	err = tx.QueryRow(`SELECT version,archived,name FROM `+table+` WHERE id=?`, id).Scan(&current, &archived, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if archived || current != version {
		return ErrConflict
	}
	field := "category_id"
	if kind == "type" {
		field = "type_id"
	}
	var count int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM products WHERE `+field+`=? AND archived=0`, id).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrReferenced
	}
	if _, err = tx.Exec(`UPDATE `+table+` SET archived=1,version=version+1 WHERE id=?`, id); err != nil {
		return err
	}
	if err = catalogEvent(tx, kind, id, "archive", name, "Archived catalog label; identifiers retained"); err != nil {
		return err
	}
	return tx.Commit()
}
func validIcon(icon string) bool {
	switch icon {
	case "apple", "leaf", "bread", "milk", "egg", "pasta", "oil", "jam", "deodorant", "razor":
		return true
	}
	return false
}
func validateProduct(p *Product) error {
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	if !utf8.ValidString(p.Name) || !utf8.ValidString(p.Description) || utf8.RuneCountInString(p.Name) < 1 || utf8.RuneCountInString(p.Name) > 80 || utf8.RuneCountInString(p.Description) > 400 || p.ID < 0 || p.CategoryID < 1 || p.TypeID < 0 || p.Price < 1 || p.Price > 1000000 || !validIcon(p.Icon) {
		return ErrInvalid
	}
	if p.SaleUnit == "each" {
		if p.PriceBasis != 1 || p.QuantityStep != 1 {
			return ErrInvalid
		}
	} else if p.SaleUnit == "g" {
		if p.PriceBasis != 1000 || p.QuantityStep < 1 || p.QuantityStep > 1000 {
			return ErrInvalid
		}
	} else {
		return ErrInvalid
	}
	p.SKU = strings.ToUpper(strings.TrimSpace(p.SKU))
	if len(p.SKU) > 40 {
		return ErrInvalid
	}
	for _, c := range p.SKU {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ErrInvalid
		}
	}
	return nil
}
func activeTaxonomy(tx *sql.Tx, kind string, id int64) (string, error) {
	table, err := taxonomyTable(kind)
	if err != nil {
		return "", err
	}
	var name string
	err = tx.QueryRow(`SELECT name FROM `+table+` WHERE id=? AND archived=0`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalid
	}
	return name, err
}
func (s *Store) SaveProduct(p Product) (int64, error) {
	return s.saveProductAndDetails(p, nil)
}

func (s *Store) saveProductAndDetails(p Product, details *ProductDetails) (int64, error) {
	if err := s.ExpireHolds(); err != nil {
		return 0, err
	}
	if err := validateProduct(&p); err != nil {
		return 0, err
	}
	tx, err := s.beginWrite()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := saveProductAt(tx, p, s.now().Unix())
	if err != nil {
		return 0, catalogError(err)
	}
	if details != nil {
		if err = saveProductDetails(tx, id, *details); err != nil {
			return 0, err
		}
	}
	action := "edit"
	if p.ID == 0 {
		action = "create"
	}
	current, err := scanProduct(tx.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, id))
	if err != nil {
		return 0, err
	}
	audit := productAuditDetails(current)
	if details != nil {
		audit += productDetailsAudit(*details)
	}
	if err = catalogEvent(tx, "product", id, action, current.Name, audit); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}
func saveProduct(tx *sql.Tx, p Product) (int64, error) {
	return saveProductAt(tx, p, time.Now().Unix())
}
func saveProductAt(tx *sql.Tx, p Product, now int64) (int64, error) {
	category, err := activeTaxonomy(tx, "category", p.CategoryID)
	if err != nil {
		return 0, err
	}
	var typeID any
	if p.TypeID != 0 {
		if _, err = activeTaxonomy(tx, "type", p.TypeID); err != nil {
			return 0, err
		}
		typeID = p.TypeID
	}
	if p.ID == 0 {
		if p.Stock != 0 || p.Archived || strings.HasPrefix(p.SKU, "SHOPDEMO-") {
			return 0, ErrInvalid
		}
		if err = tx.QueryRow(`SELECT next_product_id FROM catalog_sequence WHERE id=1`).Scan(&p.ID); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(`UPDATE catalog_sequence SET next_product_id=next_product_id+1 WHERE id=1`); err != nil {
			return 0, err
		}
		code := fmt.Sprintf("SHOPDEMO-%06d", p.ID)
		if p.SKU == "" {
			p.SKU = code
		}
		_, err = tx.Exec(`INSERT INTO products(id,name,description,category,barcode,icon,price,stock,category_id,type_id,sku,sale_unit,price_basis,quantity_step) VALUES(?,?,?,?,?,?,?,0,?,?,?,?,?,?)`, p.ID, p.Name, p.Description, category, code, p.Icon, p.Price, p.CategoryID, typeID, p.SKU, p.SaleUnit, p.PriceBasis, p.QuantityStep)
		if err != nil {
			return 0, err
		}
		if _, err = tx.Exec(`INSERT INTO product_codes(product_id,scheme,raw_value,normalized_value,symbology) VALUES(?,'demo_local',?,?,'Code128')`, p.ID, code, code); err != nil {
			return 0, err
		}
		return p.ID, nil
	}
	old, err := scanProduct(tx.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, p.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if old.Archived || old.CatalogVersion != p.CatalogVersion {
		return 0, ErrConflict
	}
	if p.SKU != "" && p.SKU != old.SKU {
		return 0, ErrInvalid
	}
	unitChanged := old.SaleUnit != p.SaleUnit || old.PriceBasis != p.PriceBasis
	if unitChanged {
		var used int
		if err = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM order_items WHERE product_id=?)+(SELECT COUNT(*) FROM working_order_items WHERE product_id=?)`, p.ID, p.ID).Scan(&used); err != nil {
			return 0, err
		}
		if used > 0 || old.Stock != 0 || old.Reserved != 0 {
			return 0, ErrUnitLocked
		}
	}
	var invalidSales int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM promotions WHERE product_id=? AND cancelled=0 AND ends>? AND (sale_price>=? OR ?)`, p.ID, now, p.Price, unitChanged || old.QuantityStep != p.QuantityStep).Scan(&invalidSales); err != nil {
		return 0, err
	}
	if invalidSales > 0 {
		return 0, ErrPromotionPrice
	}
	priceChanged := old.Price != p.Price || unitChanged || old.QuantityStep != p.QuantityStep
	_, err = tx.Exec(`UPDATE products SET name=?,description=?,category=?,category_id=?,type_id=?,icon=?,price=?,sale_unit=?,price_basis=?,quantity_step=?,catalog_version=catalog_version+1,price_version=price_version+?,version=version+? WHERE id=? AND catalog_version=? AND archived=0`, p.Name, p.Description, category, p.CategoryID, typeID, p.Icon, p.Price, p.SaleUnit, p.PriceBasis, p.QuantityStep, priceChanged, unitChanged, p.ID, p.CatalogVersion)
	if err != nil {
		return 0, err
	}
	return p.ID, nil
}
func (s *Store) ArchiveProduct(id, version int64) error {
	if id < 1 || version < 1 {
		return ErrInvalid
	}
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE products SET archived=1,catalog_version=catalog_version+1,price_version=price_version+1 WHERE id=? AND catalog_version=? AND archived=0`, id, version)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(`UPDATE product_codes SET archived=1 WHERE product_id=?`, id); err != nil {
		return err
	}
	p, err := scanProduct(tx.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, id))
	if err != nil {
		return err
	}
	if err = catalogEvent(tx, "product", id, "archive", p.Name, productAuditDetails(p)); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveProductCode is a read-only boundary for future manual/camera scanners.
// Legacy numbers are deliberately never treated as UPC/GTIN identifiers. Code
// resolution does not authorize or execute any picking or stock mutation.
func (s *Store) ResolveProductCode(raw, detectedFormat string) (Product, error) {
	if err := s.ExpireHolds(); err != nil {
		return Product{}, err
	}
	format := strings.ToLower(strings.ReplaceAll(detectedFormat, "_", ""))
	if format != "code128" || len(raw) > 64 || !strings.HasPrefix(raw, "SHOPDEMO-") {
		return Product{}, ErrNotFound
	}
	p, err := scanProduct(s.db.QueryRow(`SELECT `+productSelect+productJoins+`JOIN product_codes pc ON pc.product_id=p.id WHERE pc.scheme='demo_local' AND pc.normalized_value=? AND pc.symbology='Code128' AND pc.archived=0 AND p.archived=0`, raw))
	if errors.Is(err, sql.ErrNoRows) {
		return Product{}, ErrNotFound
	}
	return p, err
}

func productAuditDetails(p Product) string {
	return fmt.Sprintf("SKU %s; category %s (#%d); type %s (#%d); %d cents per %d %s; step %d; art %s; description: %s", p.SKU, p.Category, p.CategoryID, p.ProductType, p.TypeID, p.Price, p.PriceBasis, p.SaleUnit, p.QuantityStep, p.Icon, p.Description)
}
func catalogEvent(tx *sql.Tx, kind string, id int64, action, name, details string) error {
	_, err := tx.Exec(`INSERT INTO catalog_events(kind,entity_id,action,name,details) VALUES(?,?,?,?,?)`, kind, id, action, name, details)
	return err
}
func (s *Store) CatalogEvents() ([]CatalogEvent, error) {
	rows, err := s.db.Query(`SELECT kind,entity_id,action,name,details,created FROM catalog_events ORDER BY id DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []CatalogEvent
	for rows.Next() {
		var e CatalogEvent
		if err = rows.Scan(&e.Kind, &e.EntityID, &e.Action, &e.Name, &e.Details, &e.Created); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// RestoreTaxonomy restores the original label identity only. Its products remain
// archived, and no historical or stock data is changed.
func (s *Store) RestoreTaxonomy(kind string, id, version int64) error {
	table, err := taxonomyTable(kind)
	if err != nil {
		return err
	}
	if id < 1 || version < 1 {
		return ErrInvalid
	}
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE `+table+` SET archived=0,version=version+1 WHERE id=? AND version=? AND archived=1`, id, version)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	var name string
	if err = tx.QueryRow(`SELECT name FROM `+table+` WHERE id=?`, id).Scan(&name); err != nil {
		return err
	}
	if err = catalogEvent(tx, kind, id, "restore", name, "Restored catalog label only; archived products unchanged"); err != nil {
		return err
	}
	return tx.Commit()
}

// RestoreProduct reactivates only this archived product and its code identities.
// Historical receipts, available stock, and other archives remain unchanged.
func (s *Store) RestoreProduct(id, version int64) error {
	if id < 1 || version < 1 {
		return ErrInvalid
	}
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := scanProduct(tx.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !p.Archived || p.CatalogVersion != version {
		return ErrConflict
	}
	if _, err = activeTaxonomy(tx, "category", p.CategoryID); err != nil {
		if errors.Is(err, ErrInvalid) {
			return ErrTaxonomyInactive
		}
		return err
	}
	if p.TypeID != 0 {
		if _, err = activeTaxonomy(tx, "type", p.TypeID); err != nil {
			if errors.Is(err, ErrInvalid) {
				return ErrTaxonomyInactive
			}
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE products SET archived=0,catalog_version=catalog_version+1,price_version=price_version+1 WHERE id=? AND catalog_version=? AND archived=1`, id, version); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE product_codes SET archived=0 WHERE product_id=?`, id); err != nil {
		return err
	}
	if err = catalogEvent(tx, "product", id, "restore", p.Name, productAuditDetails(p)); err != nil {
		return err
	}
	return tx.Commit()
}
