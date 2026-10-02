package shop

import (
	"database/sql"
	"fmt"
)

// This is called only while creating a genuinely new database (including the
// separately confirmed reset fixture). Existing catalogs are never enriched on
// open. Package and nutrition values are fictional portfolio examples.
func seedProductDetails(tx *sql.Tx) error {
	packages := []string{
		"4 apples", "150 g bag", "650 g loaf", "Half-gallon carton", "12 eggs", "500 g box", "500 ml bottle", "340 g jar",
		"1 bunch", "1.5 kg bag", "3 lemons", "4 limes", "250 g punnet", "200 g punnet", "500 g box", "2 avocados", "1 kg bag", "1 crown", "6 tomatoes", "1 cucumber", "3 peppers", "2 kg bag", "1 kg bag",
		"600 g loaf", "1 baguette", "4 bagels", "2 croissants", "4 muffins", "6 rolls", "6 pitas", "8 tortillas", "500 g loaf",
		"1 litre carton", "1 litre carton", "1 litre carton", "250 g pack", "200 g pack", "125 g pack", "500 g tub", "150 g pot", "200 g tub", "6 eggs",
		"500 g box", "500 g bag", "1 kg bag", "1 kg bag", "750 g bag", "500 g box", "400 g bag", "400 g tin", "400 g tin", "500 g bag", "400 g tin", "500 g jar", "190 g jar", "340 g jar", "340 g jar", "350 g bottle", "1 litre bottle", "250 ml bottle", "200 g box", "100 g bar",
		"500 g bag", "500 g bag", "750 g bag", "400 g bag", "400 g bag", "1 litre tub", "400 g pizza", "2 baguettes",
	}
	if len(packages) != 70 {
		return fmt.Errorf("product package seed has %d rows", len(packages))
	}
	for i, label := range packages {
		id := int64(i + 1)
		var description string
		if err := tx.QueryRow(`SELECT description FROM products WHERE id=?`, id).Scan(&description); err != nil {
			return err
		}
		d := ProductDetails{ProductID: id, Kind: "food", Body: description + " This is a fictional neighborhood-market product prepared for the portfolio demo.", PackageLabel: label, PackageDetails: "Illustrative package contents. One basket item represents one complete package; package weight does not enable weighed ordering."}
		switch id {
		case 1:
			d.Nutrition = &DemoNutrition{ServingLabel: "1 demo apple (150 g)", EnergyKcal: 80, FatTenths: 2, CarbohydrateTenths: 210, ProteinTenths: 4, SodiumMG: 2}
		case 3:
			d.Nutrition = &DemoNutrition{ServingLabel: "1 demo slice (40 g)", EnergyKcal: 100, FatTenths: 8, CarbohydrateTenths: 195, ProteinTenths: 35, SodiumMG: 180}
		case 4:
			d.Nutrition = &DemoNutrition{ServingLabel: "1 demo glass (200 ml)", EnergyKcal: 125, FatTenths: 70, CarbohydrateTenths: 95, ProteinTenths: 65, SodiumMG: 85}
		case 6:
			d.Nutrition = &DemoNutrition{ServingLabel: "1 demo portion (75 g dry)", EnergyKcal: 265, FatTenths: 12, CarbohydrateTenths: 530, ProteinTenths: 90, SodiumMG: 5}
		}
		if err := validateProductDetails(&d); err != nil {
			return err
		}
		if err := saveProductDetails(tx, id, d); err != nil {
			return err
		}
	}
	return addProductExamples(tx, false)
}
