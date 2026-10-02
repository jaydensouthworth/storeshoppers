package shop

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrProductExamples = errors.New("These examples cannot be added because a reserved demo SKU or required archived label conflicts with the current catalog. Nothing was changed.")

type ProductExample struct {
	Product Product
	Details ProductDetails
}
type ProductExamplesWorkspace struct {
	Examples                 []ProductExample
	Quote, CommandKey, Error string
	AlreadyAdded             bool
}

func nonfoodExamples() []ProductExample {
	return []ProductExample{
		{Product: Product{Name: "Everyday deodorant", Description: "An everyday personal-care staple in a simple twist-up package.", SKU: "DEMO-DEODORANT-75G", Category: "Personal care", ProductType: "Deodorant", Icon: "deodorant", Price: 499, Stock: 24, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1}, Details: ProductDetails{Kind: "nonfood", Body: "A fictional twist-up deodorant included to show how personal-care products fit into the same catalog as groceries. The package information is illustrative demo content.", PackageLabel: "75 g stick", PackageDetails: "One twist-up stick per package. Sold as one counted item; the printed package weight is not a weighed order quantity."}},
		{Product: Product{Name: "Three-blade razors · 3 pack", Description: "A three-pack of demo razors for the personal-care shelf.", SKU: "DEMO-RAZORS-3PK", Category: "Personal care", ProductType: "Shaving", Icon: "razor", Price: 699, Stock: 18, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1}, Details: ProductDetails{Kind: "nonfood", Body: "A fictional pack of manual razors that demonstrates nonfood product types, package counts and ordinary counted inventory. No real product performance claims are made.", PackageLabel: "3 razors", PackageDetails: "Three manual razors in one retail package. Adding one item to the basket reserves one whole three-pack."}},
	}
}

func productExamplePlan(q querier) (ProductExamplesWorkspace, error) {
	w := ProductExamplesWorkspace{Examples: nonfoodExamples()}
	h := sha256.New()
	if err := json.NewEncoder(h).Encode(w.Examples); err != nil {
		return w, err
	}
	installed := 0
	for i, example := range w.Examples {
		var id int64
		err := q.QueryRow(`SELECT product_id FROM product_example_products WHERE example_key=?`, example.Product.SKU).Scan(&id)
		if err == nil {
			installed++
			p, e := scanProduct(q.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, id))
			if e != nil {
				return w, e
			}
			d, e := productDetails(q, id)
			if e != nil {
				return w, e
			}
			w.Examples[i] = ProductExample{Product: p, Details: d}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return w, err
		}
		fmt.Fprintf(h, "installed:%s:%d;", example.Product.SKU, id)
	}
	if installed == len(w.Examples) {
		w.AlreadyAdded = true
		return w, nil
	}
	if installed != 0 {
		return w, ErrProductExamples
	}
	for _, example := range w.Examples {
		var used int
		if err := q.QueryRow(`SELECT COUNT(*) FROM products WHERE sku=? COLLATE NOCASE`, example.Product.SKU).Scan(&used); err != nil {
			return w, err
		}
		if used != 0 {
			return w, ErrProductExamples
		}
	}
	for _, label := range []struct{ kind, name string }{{"category", "Personal care"}, {"type", "Deodorant"}, {"type", "Shaving"}} {
		table, _ := taxonomyTable(label.kind)
		var id, version int64
		var archived bool
		var name string
		err := q.QueryRow(`SELECT id,name,version,archived FROM `+table+` WHERE normalized_name=?`, strings.ToLower(label.name)).Scan(&id, &name, &version, &archived)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return w, err
		}
		if archived {
			return w, ErrProductExamples
		}
		fmt.Fprintf(h, "%s:%d:%q:%d:%t;", label.kind, id, name, version, archived)
	}
	w.Quote = hex.EncodeToString(h.Sum(nil))
	return w, nil
}

func (s *Store) ProductExamples() (ProductExamplesWorkspace, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return ProductExamplesWorkspace{}, err
	}
	defer tx.Rollback()
	return productExamplePlan(tx)
}

func exampleTaxonomy(tx *sql.Tx, kind, name string, audit bool) (int64, error) {
	table, _ := taxonomyTable(kind)
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE normalized_name=?`, strings.ToLower(name)).Scan(&exists); err != nil {
		return 0, err
	}
	id, err := seedTaxonomy(tx, kind, name)
	if err != nil {
		return 0, err
	}
	if exists == 0 && audit {
		err = catalogEvent(tx, kind, id, "create", name, "Created label for reviewed nonfood demo examples")
	}
	return id, err
}

func addProductExamples(tx *sql.Tx, audit bool) error {
	for _, example := range nonfoodExamples() {
		p := example.Product
		var err error
		p.CategoryID, err = exampleTaxonomy(tx, "category", p.Category, audit)
		if err != nil {
			return err
		}
		p.TypeID, err = exampleTaxonomy(tx, "type", p.ProductType, audit)
		if err != nil {
			return err
		}
		stock := p.Stock
		p.Stock = 0
		if err = validateProduct(&p); err != nil {
			return err
		}
		id, err := saveProduct(tx, p)
		if err != nil {
			return catalogError(err)
		}
		if err = validateProductDetails(&example.Details); err != nil {
			return err
		}
		if err = saveProductDetails(tx, id, example.Details); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE products SET stock=?,version=version+? WHERE id=?`, stock, audit, id); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO product_example_products(example_key,product_id) VALUES(?,?)`, p.SKU, id); err != nil {
			return err
		}
		if audit {
			p, err = scanProduct(tx.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, id))
			if err != nil {
				return err
			}
			if err = catalogEvent(tx, "product", id, "create", p.Name, productAuditDetails(p)+productDetailsAudit(example.Details)); err != nil {
				return err
			}
			if _, err = tx.Exec(`INSERT INTO adjustments(product_id,delta,reason,sale_unit) VALUES(?,?,'Initial stock for reviewed nonfood demo example','each')`, id, stock); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) CreateProductExamples(key, quote string) error {
	if len(key) < 16 || len(key) > 150 || strings.TrimSpace(key) != key || len(quote) != 64 {
		return ErrInvalid
	}
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prior string
	err = tx.QueryRow(`SELECT command_hash FROM product_detail_commands WHERE command_key=?`, key).Scan(&prior)
	if err == nil {
		if prior == quote {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	plan, err := productExamplePlan(tx)
	if err != nil {
		return err
	}
	if plan.AlreadyAdded || plan.Quote != quote {
		return ErrConflict
	}
	if err = addProductExamples(tx, true); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO product_detail_commands(command_key,command_hash) VALUES(?,?)`, key, quote); err != nil {
		return err
	}
	return tx.Commit()
}
