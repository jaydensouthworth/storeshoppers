-- Holds are an overlay; fulfillment and existing history keep their meanings.
ALTER TABLE orders ADD COLUMN attention_reason TEXT NOT NULL DEFAULT '' CHECK(attention_reason='' OR length(attention_reason) BETWEEN 3 AND 240);
ALTER TABLE orders ADD COLUMN attention_since INTEGER NOT NULL DEFAULT 0 CHECK(attention_since>=0);
ALTER TABLE order_events ADD COLUMN visibility TEXT NOT NULL DEFAULT 'customer' CHECK(visibility IN ('customer','internal'));
CREATE INDEX orders_queue_state ON orders(status,created,id);
CREATE INDEX orders_queue_owner ON orders(session_id,created,id);
CREATE INDEX order_events_visibility ON order_events(order_id,visibility,id);
