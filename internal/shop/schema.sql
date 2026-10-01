PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS products (
 id INTEGER PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, category TEXT NOT NULL,
 barcode TEXT NOT NULL UNIQUE, icon TEXT NOT NULL, price INTEGER NOT NULL CHECK(price BETWEEN 1 AND 1000000),
 stock INTEGER NOT NULL CHECK(stock BETWEEN 0 AND 10000), version INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS sessions (
 id TEXT PRIMARY KEY, csrf TEXT NOT NULL, checkout_key TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1, manager_until INTEGER NOT NULL DEFAULT 0, expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS cart (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 product_id INTEGER NOT NULL REFERENCES products(id), quantity INTEGER NOT NULL CHECK(quantity BETWEEN 1 AND 99),
 PRIMARY KEY(session_id,product_id)
);
CREATE TABLE IF NOT EXISTS orders (
 id INTEGER PRIMARY KEY AUTOINCREMENT, reference TEXT NOT NULL UNIQUE,
 session_id TEXT NOT NULL, checkout_key TEXT NOT NULL UNIQUE, total INTEGER NOT NULL CHECK(total > 0),
 status TEXT NOT NULL DEFAULT 'Placed' CHECK(status IN ('Placed','Picking','Ready','Completed')),
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now'))
);
CREATE INDEX IF NOT EXISTS orders_session ON orders(session_id);
CREATE TABLE IF NOT EXISTS order_items (
 order_id INTEGER NOT NULL REFERENCES orders(id), product_id INTEGER NOT NULL REFERENCES products(id),
 name TEXT NOT NULL, price INTEGER NOT NULL CHECK(price > 0), quantity INTEGER NOT NULL CHECK(quantity BETWEEN 1 AND 99),
 PRIMARY KEY(order_id,product_id)
);
CREATE TABLE IF NOT EXISTS adjustments (
 id INTEGER PRIMARY KEY, product_id INTEGER NOT NULL REFERENCES products(id), delta INTEGER NOT NULL,
 reason TEXT NOT NULL, created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now'))
);
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY);
INSERT OR IGNORE INTO schema_version VALUES(1);
