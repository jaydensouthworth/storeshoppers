package shop

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ProductDetails is optional current-catalog presentation. Package text never
// changes the product's selling unit, rate, inventory or historical snapshots.
type ProductDetails struct {
	ProductID                                int64
	Kind, Body, PackageLabel, PackageDetails string
	Nutrition                                *DemoNutrition
}

// DemoNutrition is deliberately a small fictional label, not dietary advice.
// Macro amounts are stored in tenths of a gram; energy and sodium are integers.
type DemoNutrition struct {
	ServingLabel                                                       string
	EnergyKcal, FatTenths, CarbohydrateTenths, ProteinTenths, SodiumMG int64
}

func tenthsLabel(value int64) string              { return fmt.Sprintf("%d.%d", value/10, value%10) }
func (n DemoNutrition) FatGrams() string          { return tenthsLabel(n.FatTenths) }
func (n DemoNutrition) CarbohydrateGrams() string { return tenthsLabel(n.CarbohydrateTenths) }
func (n DemoNutrition) ProteinGrams() string      { return tenthsLabel(n.ProteinTenths) }

func validateProductDetails(d *ProductDetails) error {
	if d.Kind == "" {
		d.Kind = "unspecified"
	}
	if d.ProductID < 0 || (d.Kind != "unspecified" && d.Kind != "food" && d.Kind != "nonfood") {
		return ErrInvalid
	}
	for _, field := range []struct {
		text  *string
		limit int
	}{{&d.Body, 1600}, {&d.PackageLabel, 80}, {&d.PackageDetails, 240}} {
		*field.text = strings.TrimSpace(*field.text)
		if !utf8.ValidString(*field.text) || utf8.RuneCountInString(*field.text) > field.limit || strings.ContainsRune(*field.text, 0) {
			return ErrInvalid
		}
	}
	if n := d.Nutrition; n != nil {
		// Copy before normalization so a rejected request cannot mutate its caller.
		copy := *n
		d.Nutrition = &copy
		n = d.Nutrition
		n.ServingLabel = strings.TrimSpace(n.ServingLabel)
		if d.Kind != "food" || !utf8.ValidString(n.ServingLabel) || utf8.RuneCountInString(n.ServingLabel) < 1 || utf8.RuneCountInString(n.ServingLabel) > 80 || strings.ContainsRune(n.ServingLabel, 0) || n.EnergyKcal < 0 || n.EnergyKcal > 10000 || n.FatTenths < 0 || n.FatTenths > 100000 || n.CarbohydrateTenths < 0 || n.CarbohydrateTenths > 100000 || n.ProteinTenths < 0 || n.ProteinTenths > 100000 || n.SodiumMG < 0 || n.SodiumMG > 1000000 {
			return ErrInvalid
		}
	}
	return nil
}

func productDetails(q querier, id int64) (ProductDetails, error) {
	d := ProductDetails{ProductID: id, Kind: "unspecified"}
	var serving sql.NullString
	var energy, fat, carbs, protein, sodium sql.NullInt64
	err := q.QueryRow(`SELECT COALESCE(d.kind,'unspecified'),COALESCE(d.body,''),COALESCE(d.package_label,''),COALESCE(d.package_details,''),d.nutrition_serving,d.nutrition_energy,d.nutrition_fat,d.nutrition_carbs,d.nutrition_protein,d.nutrition_sodium FROM products p LEFT JOIN product_details d ON d.product_id=p.id WHERE p.id=?`, id).Scan(&d.Kind, &d.Body, &d.PackageLabel, &d.PackageDetails, &serving, &energy, &fat, &carbs, &protein, &sodium)
	if errors.Is(err, sql.ErrNoRows) {
		return ProductDetails{}, ErrNotFound
	}
	if err != nil {
		return ProductDetails{}, err
	}
	if serving.Valid {
		d.Nutrition = &DemoNutrition{ServingLabel: serving.String, EnergyKcal: energy.Int64, FatTenths: fat.Int64, CarbohydrateTenths: carbs.Int64, ProteinTenths: protein.Int64, SodiumMG: sodium.Int64}
	}
	return d, nil
}

func (s *Store) ProductDetails(id int64) (ProductDetails, error) { return productDetails(s.db, id) }

func saveProductDetails(tx *sql.Tx, id int64, d ProductDetails) error {
	var serving, energy, fat, carbs, protein, sodium any
	if n := d.Nutrition; n != nil {
		serving = n.ServingLabel
		energy = n.EnergyKcal
		fat = n.FatTenths
		carbs = n.CarbohydrateTenths
		protein = n.ProteinTenths
		sodium = n.SodiumMG
	}
	_, err := tx.Exec(`INSERT INTO product_details(product_id,kind,body,package_label,package_details,nutrition_serving,nutrition_energy,nutrition_fat,nutrition_carbs,nutrition_protein,nutrition_sodium) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(product_id) DO UPDATE SET kind=excluded.kind,body=excluded.body,package_label=excluded.package_label,package_details=excluded.package_details,nutrition_serving=excluded.nutrition_serving,nutrition_energy=excluded.nutrition_energy,nutrition_fat=excluded.nutrition_fat,nutrition_carbs=excluded.nutrition_carbs,nutrition_protein=excluded.nutrition_protein,nutrition_sodium=excluded.nutrition_sodium`, id, d.Kind, d.Body, d.PackageLabel, d.PackageDetails, serving, energy, fat, carbs, protein, sodium)
	return err
}

func (s *Store) SaveProductWithDetails(p Product, d ProductDetails) (int64, error) {
	if d.ProductID != 0 && d.ProductID != p.ID {
		return 0, ErrInvalid
	}
	if err := validateProductDetails(&d); err != nil {
		return 0, err
	}
	return s.saveProductAndDetails(p, &d)
}

func productDetailsAudit(d ProductDetails) string {
	result := fmt.Sprintf("; kind %s; package %s; package details: %s; full description: %s", d.Kind, d.PackageLabel, d.PackageDetails, d.Body)
	if n := d.Nutrition; n != nil {
		result += fmt.Sprintf("; illustrative demo nutrition per %s: %d kcal; fat %s g; carbohydrate %s g; protein %s g; sodium %d mg", n.ServingLabel, n.EnergyKcal, n.FatGrams(), n.CarbohydrateGrams(), n.ProteinGrams(), n.SodiumMG)
	} else {
		result += "; no nutrition panel"
	}
	return result
}

// PublicProduct loads availability and effective pricing from the same rules as
// the shelves. Archived identities remain available to manager/history paths.
func (s *Store) PublicProduct(id int64) (Product, error) {
	p, _, err := s.publicProductDetails(id)
	return p, err
}

// Read the product, its details and its effective price in one snapshot so a
// concurrent manager save cannot render a mixed old/new detail page.
func (s *Store) publicProductDetails(id int64) (Product, ProductDetails, error) {
	if err := s.ExpireHolds(); err != nil {
		return Product{}, ProductDetails{}, err
	}
	tx, err := s.beginWrite()
	if err != nil {
		return Product{}, ProductDetails{}, err
	}
	defer tx.Rollback()
	p, err := scanProduct(tx.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=? AND p.archived=0`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Product{}, ProductDetails{}, ErrNotFound
	}
	if err != nil {
		return Product{}, ProductDetails{}, err
	}
	state, err := loadPricing(tx, s.now().Unix())
	if err != nil {
		return Product{}, ProductDetails{}, err
	}
	state.apply(&p)
	d, err := productDetails(tx, id)
	return p, d, err
}
