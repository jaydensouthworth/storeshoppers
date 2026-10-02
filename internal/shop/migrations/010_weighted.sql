-- The caller pins one connection, disables foreign_keys before BEGIN IMMEDIATE,
-- and preserves affected indices/triggers and sqlite_sequence high-water marks.
-- Never rename the old parent table: existing foreign keys must keep its name.
CREATE TABLE products_v10 (
 id INTEGER PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, category TEXT NOT NULL,
 barcode TEXT NOT NULL UNIQUE, icon TEXT NOT NULL, price INTEGER NOT NULL CHECK(price BETWEEN 1 AND 1000000),
 stock INTEGER NOT NULL CHECK(stock>=0 AND stock<=CASE sale_unit WHEN 'each' THEN 10000 ELSE 1000000 END),
 version INTEGER NOT NULL DEFAULT 1,
 category_id INTEGER REFERENCES categories(id), type_id INTEGER REFERENCES product_types(id),
 sku TEXT NOT NULL DEFAULT '', archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN(0,1)),
 catalog_version INTEGER NOT NULL DEFAULT 1 CHECK(catalog_version>0),
 price_version INTEGER NOT NULL DEFAULT 1 CHECK(price_version>0),
 sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN('each','g')),
 price_basis INTEGER NOT NULL DEFAULT 1 CHECK((sale_unit='each' AND price_basis=1) OR (sale_unit='g' AND price_basis=1000)),
 quantity_step INTEGER NOT NULL DEFAULT 1 CHECK((sale_unit='each' AND quantity_step=1) OR (sale_unit='g' AND quantity_step BETWEEN 1 AND 1000)),
 image_hash TEXT REFERENCES product_images(hash)
);
INSERT INTO products_v10 SELECT * FROM products;
DROP TABLE products;
ALTER TABLE products_v10 RENAME TO products;

CREATE TABLE cart_v10 (
 basket_id TEXT NOT NULL REFERENCES baskets(id),
 product_id INTEGER NOT NULL REFERENCES products(id),
 quantity INTEGER NOT NULL CHECK(quantity BETWEEN 1 AND 100000),
 reserved INTEGER NOT NULL DEFAULT 0 CHECK(reserved>=0 AND reserved<=quantity),
 PRIMARY KEY(basket_id,product_id)
);
INSERT INTO cart_v10 SELECT * FROM cart;
DROP TABLE cart;
ALTER TABLE cart_v10 RENAME TO cart;

CREATE TABLE order_items_v10 (
 order_id INTEGER NOT NULL REFERENCES orders(id), product_id INTEGER NOT NULL REFERENCES products(id),
 name TEXT NOT NULL, price INTEGER NOT NULL CHECK(price>0),
 quantity INTEGER NOT NULL CHECK(quantity>=1 AND quantity<=CASE sale_unit WHEN 'each' THEN 99 ELSE 100000 END),
 picked_quantity INTEGER NOT NULL DEFAULT 0 CHECK(picked_quantity>=0 AND picked_quantity<=quantity),
 pick_version INTEGER NOT NULL DEFAULT 1 CHECK(pick_version>0),
 sku TEXT NOT NULL DEFAULT '', sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN('each','g')),
 price_basis INTEGER NOT NULL DEFAULT 1 CHECK(price_basis>0),
 quantity_step INTEGER NOT NULL DEFAULT 1 CHECK(quantity_step>0),
 subtotal INTEGER NOT NULL DEFAULT 0 CHECK(subtotal>=0),
 PRIMARY KEY(order_id,product_id)
);
INSERT INTO order_items_v10 SELECT * FROM order_items;
DROP TABLE order_items;
ALTER TABLE order_items_v10 RENAME TO order_items;

CREATE TABLE working_order_items_v10 (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 order_id INTEGER NOT NULL REFERENCES orders(id), product_id INTEGER NOT NULL REFERENCES products(id),
 name TEXT NOT NULL, price INTEGER NOT NULL CHECK(price>0),
 quantity INTEGER NOT NULL CHECK(quantity>=0 AND quantity<=CASE sale_unit WHEN 'each' THEN 99 ELSE 100000 END),
 picked_quantity INTEGER NOT NULL DEFAULT 0 CHECK(picked_quantity>=0 AND picked_quantity<=allocated_quantity),
 pick_version INTEGER NOT NULL DEFAULT 1 CHECK(pick_version>0),
 sku TEXT NOT NULL, sale_unit TEXT NOT NULL CHECK(sale_unit IN('each','g')),
 price_basis INTEGER NOT NULL CHECK(price_basis>0), quantity_step INTEGER NOT NULL CHECK(quantity_step>0),
 unavailable_quantity INTEGER NOT NULL DEFAULT 0 CHECK(unavailable_quantity>=0),
 cancelled_quantity INTEGER NOT NULL DEFAULT 0 CHECK(cancelled_quantity>=0),
 allocated_quantity INTEGER NOT NULL CHECK(allocated_quantity>=0 AND allocated_quantity<=CASE sale_unit WHEN 'each' THEN 99 ELSE 100000 END),
 measurement_confirmed INTEGER NOT NULL DEFAULT 0 CHECK(measurement_confirmed IN(0,1)),
 CHECK(sale_unit<>'each' OR allocated_quantity=quantity),
 CHECK(picked_quantity+unavailable_quantity+cancelled_quantity<=allocated_quantity),
 UNIQUE(order_id,product_id)
);
INSERT INTO working_order_items_v10 SELECT *,quantity,0 FROM working_order_items;
DROP TABLE working_order_items;
ALTER TABLE working_order_items_v10 RENAME TO working_order_items;

-- A cart is a mutable product request, so validate against current product
-- metadata instead of storing an independently mutable unit copy in the cart.
CREATE TRIGGER cart_quantity_insert BEFORE INSERT ON cart
 WHEN NOT EXISTS(SELECT 1 FROM products p WHERE p.id=NEW.product_id
 AND NEW.quantity<=CASE p.sale_unit WHEN 'each' THEN 99 ELSE 100000 END
 AND NEW.quantity%p.quantity_step=0)
 BEGIN SELECT RAISE(ABORT,'cart quantity does not match product unit or step'); END;
CREATE TRIGGER cart_quantity_update BEFORE UPDATE ON cart
 WHEN NOT EXISTS(SELECT 1 FROM products p WHERE p.id=NEW.product_id
 AND NEW.quantity<=CASE p.sale_unit WHEN 'each' THEN 99 ELSE 100000 END
 AND NEW.quantity%p.quantity_step=0)
 BEGIN SELECT RAISE(ABORT,'cart quantity does not match product unit or step'); END;
CREATE TRIGGER products_cart_quantity_update BEFORE UPDATE OF sale_unit,quantity_step ON products
 WHEN EXISTS(SELECT 1 FROM cart c WHERE c.product_id=OLD.id
 AND (c.quantity>CASE NEW.sale_unit WHEN 'each' THEN 99 ELSE 100000 END OR c.quantity%NEW.quantity_step<>0))
 BEGIN SELECT RAISE(ABORT,'product unit or step would invalidate a cart'); END;
