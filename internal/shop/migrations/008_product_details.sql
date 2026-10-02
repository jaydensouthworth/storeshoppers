-- Product presentation is current catalog metadata, never a receipt rewrite.
CREATE TABLE product_details (
 product_id INTEGER PRIMARY KEY REFERENCES products(id),
 kind TEXT NOT NULL DEFAULT 'unspecified' CHECK(kind IN('unspecified','food','nonfood')),
 body TEXT NOT NULL DEFAULT '' CHECK(length(body)<=1600),
 package_label TEXT NOT NULL DEFAULT '' CHECK(length(package_label)<=80),
 package_details TEXT NOT NULL DEFAULT '' CHECK(length(package_details)<=240),
 nutrition_serving TEXT,
 nutrition_energy INTEGER CHECK(nutrition_energy BETWEEN 0 AND 10000),
 nutrition_fat INTEGER CHECK(nutrition_fat BETWEEN 0 AND 100000),
 nutrition_carbs INTEGER CHECK(nutrition_carbs BETWEEN 0 AND 100000),
 nutrition_protein INTEGER CHECK(nutrition_protein BETWEEN 0 AND 100000),
 nutrition_sodium INTEGER CHECK(nutrition_sodium BETWEEN 0 AND 1000000),
 CHECK((nutrition_serving IS NULL AND nutrition_energy IS NULL AND nutrition_fat IS NULL AND nutrition_carbs IS NULL AND nutrition_protein IS NULL AND nutrition_sodium IS NULL)
 OR (kind='food' AND nutrition_serving IS NOT NULL AND length(nutrition_serving) BETWEEN 1 AND 80 AND nutrition_energy IS NOT NULL AND nutrition_fat IS NOT NULL AND nutrition_carbs IS NOT NULL AND nutrition_protein IS NOT NULL AND nutrition_sodium IS NOT NULL))
);
CREATE TABLE product_example_products (
 example_key TEXT PRIMARY KEY,
 product_id INTEGER NOT NULL UNIQUE REFERENCES products(id)
);
CREATE TABLE product_detail_commands (
 command_key TEXT PRIMARY KEY,
 command_hash TEXT NOT NULL
);
INSERT INTO schema_version VALUES(8);
