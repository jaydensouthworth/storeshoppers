CREATE TABLE baskets (
 id TEXT PRIMARY KEY,
 owner_session_id TEXT NOT NULL REFERENCES sessions(id),
 synthetic INTEGER NOT NULL DEFAULT 0 CHECK(synthetic IN (0,1)),
 label TEXT NOT NULL DEFAULT 'Customer basket',
 practice_slot INTEGER NOT NULL DEFAULT 0 CHECK(practice_slot BETWEEN 0 AND 2),
 revision INTEGER NOT NULL DEFAULT 1,
 hold_until INTEGER NOT NULL DEFAULT 0 CHECK(hold_until>=0),
 UNIQUE(owner_session_id,practice_slot),
 CHECK((synthetic=0 AND practice_slot=0) OR (synthetic=1 AND practice_slot>0))
);
INSERT INTO baskets(id,owner_session_id,revision) SELECT lower(hex(randomblob(16))),id,revision FROM sessions;
ALTER TABLE cart RENAME TO cart_v3;
CREATE TABLE cart (
 basket_id TEXT NOT NULL REFERENCES baskets(id),
 product_id INTEGER NOT NULL REFERENCES products(id),
 quantity INTEGER NOT NULL CHECK(quantity BETWEEN 1 AND 99),
 reserved INTEGER NOT NULL DEFAULT 0 CHECK(reserved>=0 AND reserved<=quantity),
 PRIMARY KEY(basket_id,product_id)
);
INSERT INTO cart(basket_id,product_id,quantity) SELECT b.id,c.product_id,c.quantity FROM cart_v3 c JOIN baskets b ON b.owner_session_id=c.session_id AND b.synthetic=0;
DROP TABLE cart_v3;
CREATE INDEX cart_product ON cart(product_id);
CREATE INDEX baskets_expiry ON baskets(hold_until) WHERE hold_until>0;
CREATE TABLE basket_events (
 id INTEGER PRIMARY KEY,
 basket_id TEXT NOT NULL REFERENCES baskets(id),
 action TEXT NOT NULL,
 reason TEXT NOT NULL,
 details TEXT NOT NULL,
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now'))
);
ALTER TABLE orders ADD COLUMN instructions TEXT NOT NULL DEFAULT '' CHECK(length(instructions)<=500);
INSERT INTO schema_version VALUES(4);
