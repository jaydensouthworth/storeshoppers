-- Add fulfillment progress without altering order snapshots or inventory.
ALTER TABLE order_items ADD COLUMN picked_quantity INTEGER NOT NULL DEFAULT 0
    CHECK(picked_quantity >= 0 AND picked_quantity <= quantity);
ALTER TABLE order_items ADD COLUMN pick_version INTEGER NOT NULL DEFAULT 1
    CHECK(pick_version > 0);
-- Historical Ready/Completed orders were explicitly advanced by a manager.
-- Preserve that state rather than reopening or invalidating existing receipts.
UPDATE order_items SET picked_quantity = quantity
WHERE order_id IN (SELECT id FROM orders WHERE status IN ('Ready', 'Completed'));
INSERT INTO schema_version(version) VALUES(2);
