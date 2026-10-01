CREATE TABLE categories (
 id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
 normalized_name TEXT NOT NULL UNIQUE, archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0)
);
CREATE TABLE product_types (
 id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
 normalized_name TEXT NOT NULL UNIQUE, archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0)
);
ALTER TABLE products ADD COLUMN category_id INTEGER REFERENCES categories(id);
ALTER TABLE products ADD COLUMN type_id INTEGER REFERENCES product_types(id);
ALTER TABLE products ADD COLUMN sku TEXT NOT NULL DEFAULT '';
ALTER TABLE products ADD COLUMN archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1));
ALTER TABLE products ADD COLUMN catalog_version INTEGER NOT NULL DEFAULT 1 CHECK(catalog_version > 0);
ALTER TABLE products ADD COLUMN price_version INTEGER NOT NULL DEFAULT 1 CHECK(price_version > 0);
ALTER TABLE products ADD COLUMN sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN ('each','g'));
ALTER TABLE products ADD COLUMN price_basis INTEGER NOT NULL DEFAULT 1 CHECK((sale_unit='each' AND price_basis=1) OR (sale_unit='g' AND price_basis=1000));
ALTER TABLE products ADD COLUMN quantity_step INTEGER NOT NULL DEFAULT 1 CHECK((sale_unit='each' AND quantity_step=1) OR (sale_unit='g' AND quantity_step BETWEEN 1 AND 1000));
CREATE TABLE product_codes (
 id INTEGER PRIMARY KEY AUTOINCREMENT, product_id INTEGER NOT NULL REFERENCES products(id),
 scheme TEXT NOT NULL CHECK(scheme IN ('legacy_placeholder','demo_local')),
 raw_value TEXT NOT NULL, normalized_value TEXT NOT NULL, symbology TEXT NOT NULL,
 archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
 UNIQUE(scheme,normalized_value)
);
CREATE TABLE catalog_sequence (id INTEGER PRIMARY KEY CHECK(id=1), next_product_id INTEGER NOT NULL CHECK(next_product_id>0));
INSERT INTO catalog_sequence SELECT 1,COALESCE(MAX(id),0)+1 FROM products;
ALTER TABLE order_items ADD COLUMN sku TEXT NOT NULL DEFAULT '';
ALTER TABLE order_items ADD COLUMN sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN ('each','g'));
ALTER TABLE order_items ADD COLUMN price_basis INTEGER NOT NULL DEFAULT 1 CHECK(price_basis > 0);
ALTER TABLE order_items ADD COLUMN quantity_step INTEGER NOT NULL DEFAULT 1 CHECK(quantity_step > 0);
ALTER TABLE order_items ADD COLUMN subtotal INTEGER NOT NULL DEFAULT 0 CHECK(subtotal >= 0);
UPDATE order_items SET subtotal=price*quantity;
ALTER TABLE adjustments ADD COLUMN sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN ('each','g'));

CREATE TABLE catalog_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 kind TEXT NOT NULL CHECK(kind IN ('product','category','type')),
 entity_id INTEGER NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('create','edit','archive','restore')),
 name TEXT NOT NULL, details TEXT NOT NULL,
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now'))
);
