-- Immutable, normalized demo artwork. No original upload, filename or metadata
-- is stored. Image associations are current catalog presentation only.
CREATE TABLE product_images (
 hash TEXT PRIMARY KEY CHECK(length(hash)=64),
 normalization_version TEXT NOT NULL,
 mime TEXT NOT NULL CHECK(mime='image/jpeg'),
 width INTEGER NOT NULL CHECK(width BETWEEN 1 AND 1024),
 height INTEGER NOT NULL CHECK(height BETWEEN 1 AND 1024),
 thumbnail_width INTEGER NOT NULL CHECK(thumbnail_width BETWEEN 1 AND 256),
 thumbnail_height INTEGER NOT NULL CHECK(thumbnail_height BETWEEN 1 AND 256),
 master BLOB NOT NULL CHECK(length(master) BETWEEN 1 AND 524288),
 thumbnail BLOB NOT NULL CHECK(length(thumbnail) BETWEEN 1 AND 98304),
 created TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
ALTER TABLE products ADD COLUMN image_hash TEXT REFERENCES product_images(hash);
CREATE TABLE product_image_commands (
 command_key TEXT PRIMARY KEY,
 command_hash TEXT NOT NULL
);
CREATE TABLE product_image_limits (
 id INTEGER PRIMARY KEY CHECK(id=1),
 minute_bucket INTEGER NOT NULL DEFAULT 0,
 minute_count INTEGER NOT NULL DEFAULT 0 CHECK(minute_count>=0),
 day_bucket INTEGER NOT NULL DEFAULT 0,
 day_count INTEGER NOT NULL DEFAULT 0 CHECK(day_count>=0)
);
INSERT INTO product_image_limits(id) VALUES(1);
INSERT INTO schema_version VALUES(9);
