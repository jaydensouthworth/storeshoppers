-- Rates on placed receipts and working lines remain immutable snapshots.
CREATE TABLE promotions (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 product_id INTEGER NOT NULL REFERENCES products(id),
 sale_price INTEGER NOT NULL CHECK(sale_price BETWEEN 1 AND 1000000),
 starts INTEGER NOT NULL CHECK(starts>=0),
 ends INTEGER NOT NULL CHECK(ends>starts),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>0),
 cancelled INTEGER NOT NULL DEFAULT 0 CHECK(cancelled IN(0,1))
);
CREATE INDEX promotions_product_window ON promotions(product_id,starts,ends) WHERE cancelled=0;
CREATE TRIGGER promotions_no_overlap_insert BEFORE INSERT ON promotions
 WHEN NEW.cancelled=0 AND EXISTS(SELECT 1 FROM promotions WHERE product_id=NEW.product_id AND cancelled=0 AND starts<NEW.ends AND ends>NEW.starts)
 BEGIN SELECT RAISE(ABORT,'overlapping promotion'); END;
CREATE TRIGGER promotions_no_overlap_update BEFORE UPDATE ON promotions
 WHEN NEW.cancelled=0 AND EXISTS(SELECT 1 FROM promotions WHERE id<>NEW.id AND product_id=NEW.product_id AND cancelled=0 AND starts<NEW.ends AND ends>NEW.starts)
 BEGIN SELECT RAISE(ABORT,'overlapping promotion'); END;
CREATE TABLE product_features (
 product_id INTEGER PRIMARY KEY REFERENCES products(id),
 featured INTEGER NOT NULL CHECK(featured IN(0,1)),
 version INTEGER NOT NULL CHECK(version>0)
);
CREATE TABLE promotion_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 product_id INTEGER NOT NULL REFERENCES products(id),
 promotion_id INTEGER REFERENCES promotions(id),
 action TEXT NOT NULL CHECK(action IN('create','edit','cancel','feature','unfeature','example')),
 name TEXT NOT NULL,details TEXT NOT NULL,
 created TEXT NOT NULL
);
CREATE TABLE promotion_commands (
 command_key TEXT PRIMARY KEY,
 command_hash TEXT NOT NULL
);
INSERT INTO schema_version VALUES(7);
