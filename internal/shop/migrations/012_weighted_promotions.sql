-- Before this version, only counted products could receive a promotion.
-- Preserve that original price basis even if a later catalog edit changes units.
ALTER TABLE promotions ADD COLUMN sale_unit TEXT NOT NULL DEFAULT 'each' CHECK(sale_unit IN('each','g'));
ALTER TABLE promotions ADD COLUMN price_basis INTEGER NOT NULL DEFAULT 1 CHECK((sale_unit='each' AND price_basis=1) OR (sale_unit='g' AND price_basis=1000));
