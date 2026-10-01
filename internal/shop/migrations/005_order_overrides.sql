-- Placed receipt rows remain untouched. Working lines own fulfillment from v5.
ALTER TABLE orders ADD COLUMN order_version INTEGER NOT NULL DEFAULT 1 CHECK(order_version>0);
ALTER TABLE orders ADD COLUMN final_total INTEGER CHECK(final_total>=0);
ALTER TABLE orders ADD COLUMN completion_kind TEXT NOT NULL DEFAULT '' CHECK(completion_kind IN ('','full','partial','cancelled'));
UPDATE orders SET final_total=total,completion_kind='full' WHERE status IN ('Ready','Completed');
CREATE TABLE working_order_items (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 order_id INTEGER NOT NULL REFERENCES orders(id),
 product_id INTEGER NOT NULL REFERENCES products(id),
 name TEXT NOT NULL,price INTEGER NOT NULL CHECK(price>0),
 quantity INTEGER NOT NULL CHECK(quantity BETWEEN 0 AND 99),
 picked_quantity INTEGER NOT NULL DEFAULT 0 CHECK(picked_quantity>=0 AND picked_quantity<=quantity),
 pick_version INTEGER NOT NULL DEFAULT 1 CHECK(pick_version>0),
 sku TEXT NOT NULL,sale_unit TEXT NOT NULL CHECK(sale_unit IN ('each','g')),
 price_basis INTEGER NOT NULL CHECK(price_basis>0),quantity_step INTEGER NOT NULL CHECK(quantity_step>0),
 unavailable_quantity INTEGER NOT NULL DEFAULT 0 CHECK(unavailable_quantity>=0),
 cancelled_quantity INTEGER NOT NULL DEFAULT 0 CHECK(cancelled_quantity>=0),
 CHECK(picked_quantity+unavailable_quantity+cancelled_quantity<=quantity),
 UNIQUE(order_id,product_id)
);
INSERT INTO working_order_items(order_id,product_id,name,price,quantity,picked_quantity,pick_version,sku,sale_unit,price_basis,quantity_step)
 SELECT order_id,product_id,name,price,quantity,picked_quantity,pick_version,sku,sale_unit,price_basis,quantity_step FROM order_items;
CREATE TABLE order_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,order_id INTEGER NOT NULL REFERENCES orders(id),
 command_key TEXT NOT NULL,command_hash TEXT NOT NULL,
 action TEXT NOT NULL,reason TEXT NOT NULL,details TEXT NOT NULL,
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now')),
 UNIQUE(order_id,command_key)
);
INSERT INTO schema_version VALUES(5);
