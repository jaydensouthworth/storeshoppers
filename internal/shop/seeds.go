package shop

import (
	"database/sql"
	"fmt"
)

// The first eight IDs/numbers are the original demo's identities. These values
// remain placeholders, never retail UPC/GTIN registrations. This seed is called
// only inside creation of a genuinely new database, never on an ordinary open.
func seedOriginal(tx *sql.Tx) error {
	products := []struct {
		name, description, category, barcode, icon string
		price, stock                               int64
	}{
		{"Honeycrisp apples", "Crisp, sweet and ready for the fruit bowl.", "Produce", "000000000101", "apple", 349, 24},
		{"Baby spinach", "Tender leaves for quick lunches and green dinners.", "Produce", "000000000102", "leaf", 299, 18},
		{"Sourdough loaf", "A golden crust and a soft, tangy center.", "Bakery", "000000000103", "bread", 599, 10},
		{"Whole milk", "A half gallon of the everyday essential.", "Dairy", "000000000104", "milk", 429, 16},
		{"Free range eggs", "A dozen large eggs for a well-stocked kitchen.", "Dairy", "000000000105", "egg", 649, 6},
		{"Penne pasta", "A pantry staple made for your favorite sauce.", "Pantry", "000000000106", "pasta", 249, 30},
		{"Extra virgin olive oil", "Smooth and peppery. Finish something delicious.", "Pantry", "000000000107", "oil", 1099, 8},
		{"Strawberry jam", "Small-batch style preserves for your morning toast.", "Pantry", "000000000108", "jam", 479, 0},
	}
	for i, p := range products {
		if _, err := tx.Exec(`INSERT INTO products(id,name,description,category,barcode,icon,price,stock) VALUES(?,?,?,?,?,?,?,?)`, i+1, p.name, p.description, p.category, p.barcode, p.icon, p.price, p.stock); err != nil {
			return err
		}
	}
	return nil
}
func originalDemoCatalog(tx *sql.Tx) (bool, error) {
	// A catalog with additional bespoke products is already customized. Only the
	// exact eight-product legacy demo receives the one-time expansion.
	var total int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM products`).Scan(&total); err != nil {
		return false, err
	}
	if total != 8 {
		return false, nil
	}
	var found int
	for id := 1; id <= 8; id++ {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM products WHERE id=? AND barcode=?`, id, fmt.Sprintf("%012d", 100+id)).Scan(&count); err != nil {
			return false, err
		}
		found += count
	}
	return found == 8, nil
}
func seedTaxonomy(tx *sql.Tx, kind, name string) (int64, error) {
	table, err := taxonomyTable(kind)
	if err != nil {
		return 0, err
	}
	clean, normalized, err := normalizedName(name)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`INSERT INTO `+table+`(name,normalized_name) VALUES(?,?) ON CONFLICT(normalized_name) DO NOTHING`, clean, normalized); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(`SELECT id FROM `+table+` WHERE normalized_name=? AND archived=0`, normalized).Scan(&id)
	return id, err
}

// Expand the recognizable original demo once, in migration 3. Custom/partial
// legacy databases are not filled. Every existing name, price, stock and ID is
// untouched. The schema version prevents later archive resurrection.
func expandDemoCatalog(tx *sql.Tx) error {
	products := []struct {
		name, category, kind, icon string
		price                      int64
	}{
		{"Banana bunch", "Produce", "Fruit", "apple", 189},
		{"Navel oranges · bag", "Produce", "Fruit", "apple", 499},
		{"Lemons · 3 pack", "Produce", "Fruit", "apple", 229},
		{"Limes · 4 pack", "Produce", "Fruit", "apple", 199},
		{"Strawberries · punnet", "Produce", "Fruit", "apple", 449},
		{"Blueberries · punnet", "Produce", "Fruit", "apple", 399},
		{"Seedless grapes · box", "Produce", "Fruit", "apple", 549},
		{"Avocados · pair", "Produce", "Fruit", "apple", 329},
		{"Carrots · bag", "Produce", "Vegetables", "leaf", 179},
		{"Broccoli crown", "Produce", "Vegetables", "leaf", 249},
		{"Roma tomatoes · pack", "Produce", "Vegetables", "apple", 299},
		{"Cucumber", "Produce", "Vegetables", "leaf", 129},
		{"Bell peppers · trio", "Produce", "Vegetables", "leaf", 399},
		{"Russet potatoes · bag", "Produce", "Vegetables", "leaf", 449},
		{"Yellow onions · bag", "Produce", "Vegetables", "leaf", 279},
		{"Whole wheat loaf", "Bakery", "Bread", "bread", 349},
		{"French baguette", "Bakery", "Bread", "bread", 299},
		{"Sesame bagels · 4 pack", "Bakery", "Bread", "bread", 449},
		{"Butter croissants · pair", "Bakery", "Baked goods", "bread", 399},
		{"Blueberry muffins · 4 pack", "Bakery", "Baked goods", "bread", 549},
		{"Dinner rolls · 6 pack", "Bakery", "Bread", "bread", 329},
		{"Pita bread · 6 pack", "Bakery", "Bread", "bread", 279},
		{"Flour tortillas · 8 pack", "Bakery", "Bread", "bread", 249},
		{"Cinnamon raisin loaf", "Bakery", "Bread", "bread", 429},
		{"Reduced fat milk", "Dairy", "Milk", "milk", 399},
		{"Oat drink", "Dairy", "Milk alternatives", "milk", 449},
		{"Almond drink", "Dairy", "Milk alternatives", "milk", 429},
		{"Salted butter", "Dairy", "Butter", "milk", 479},
		{"Cheddar cheese", "Dairy", "Cheese", "milk", 399},
		{"Mozzarella ball", "Dairy", "Cheese", "milk", 349},
		{"Plain Greek yogurt", "Dairy", "Yogurt", "milk", 549},
		{"Strawberry yogurt", "Dairy", "Yogurt", "milk", 129},
		{"Sour cream", "Dairy", "Cultured dairy", "milk", 249},
		{"Cage-free eggs · 6 pack", "Dairy", "Eggs", "egg", 349},
		{"Spaghetti", "Pantry", "Pasta", "pasta", 229},
		{"Fusilli pasta", "Pantry", "Pasta", "pasta", 249},
		{"Basmati rice", "Pantry", "Rice", "pasta", 449},
		{"Brown rice", "Pantry", "Rice", "pasta", 349},
		{"Rolled oats", "Pantry", "Cereal", "pasta", 329},
		{"Corn flakes", "Pantry", "Cereal", "pasta", 399},
		{"Granola", "Pantry", "Cereal", "pasta", 549},
		{"Black beans", "Pantry", "Beans", "jam", 129},
		{"Chickpeas", "Pantry", "Beans", "jam", 139},
		{"Red lentils", "Pantry", "Beans", "pasta", 249},
		{"Diced tomatoes", "Pantry", "Sauce", "jam", 149},
		{"Tomato pasta sauce", "Pantry", "Sauce", "jam", 329},
		{"Basil pesto", "Pantry", "Sauce", "jam", 449},
		{"Peanut butter", "Pantry", "Spreads", "jam", 349},
		{"Orange marmalade", "Pantry", "Spreads", "jam", 429},
		{"Wildflower honey", "Pantry", "Spreads", "oil", 599},
		{"Vegetable oil", "Pantry", "Oil", "oil", 379},
		{"Balsamic vinegar", "Pantry", "Condiments", "oil", 499},
		{"Sea salt crackers", "Pantry", "Snacks", "bread", 279},
		{"Dark chocolate bar", "Pantry", "Snacks", "bread", 249},
		{"Frozen garden peas", "Frozen", "Frozen vegetables", "leaf", 199},
		{"Frozen sweetcorn", "Frozen", "Frozen vegetables", "leaf", 199},
		{"Frozen mixed vegetables", "Frozen", "Frozen vegetables", "leaf", 249},
		{"Frozen blueberries", "Frozen", "Frozen fruit", "apple", 449},
		{"Frozen mango chunks", "Frozen", "Frozen fruit", "apple", 399},
		{"Vanilla ice cream", "Frozen", "Frozen desserts", "milk", 549},
		{"Vegetable pizza", "Frozen", "Frozen meals", "bread", 599},
		{"Garlic bread", "Frozen", "Frozen meals", "bread", 329},
	}
	for i, v := range products {
		category, err := seedTaxonomy(tx, "category", v.category)
		if err != nil {
			return err
		}
		kind, err := seedTaxonomy(tx, "type", v.kind)
		if err != nil {
			return err
		}
		p := Product{Name: v.name, Description: "A neighborhood-market demo favorite. Pack sizes are part of the product name; sold as one item.", CategoryID: category, TypeID: kind, Icon: v.icon, Price: v.price, SaleUnit: "each", PriceBasis: 1, QuantityStep: 1}
		id, err := saveProduct(tx, p)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE products SET stock=? WHERE id=?`, 12+(i%5)*6, id); err != nil {
			return err
		}
	}
	return nil
}
