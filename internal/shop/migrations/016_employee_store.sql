-- Anonymous, explicitly fictional employee capabilities are independent of
-- customer ownership and manager sessions. Existing identities are unchanged.
CREATE TABLE employee_sessions (
 token_hash TEXT PRIMARY KEY CHECK(length(token_hash)=64),
 csrf TEXT NOT NULL,
 epoch TEXT NOT NULL,
 shopper_id INTEGER NOT NULL REFERENCES shoppers(id),
 created INTEGER NOT NULL,
 expires INTEGER NOT NULL,
 revoked INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX employee_sessions_expiry ON employee_sessions(epoch,expires);
CREATE INDEX employee_sessions_shopper ON employee_sessions(shopper_id,epoch,expires);

-- Membership is explicit: migration never exposes any historical order to the
-- shared queue. New demo checkout/practice commands enroll orders atomically.
CREATE TABLE employee_store_orders (
 order_id INTEGER PRIMARY KEY REFERENCES orders(id),
 epoch TEXT NOT NULL,
 practice INTEGER NOT NULL DEFAULT 0 CHECK(practice IN (0,1))
);
CREATE INDEX employee_store_orders_epoch ON employee_store_orders(epoch,order_id);

-- Keep the existing one-order handheld grant schema and its historical rows.
-- Employee grants additionally require a live matching employee capability.
CREATE TABLE employee_grant_links (
 grant_id INTEGER PRIMARY KEY REFERENCES handheld_grants(id),
 employee_hash TEXT NOT NULL REFERENCES employee_sessions(token_hash)
);
CREATE INDEX employee_grant_links_employee ON employee_grant_links(employee_hash,grant_id);

-- Practice creation is deliberate runtime work, never a migration/backfill.
-- The flag also records a completed attempt with fewer/zero available seeds.
CREATE TABLE employee_store_state (
 id INTEGER PRIMARY KEY CHECK(id=1),
 epoch TEXT NOT NULL,
 seeded INTEGER NOT NULL DEFAULT 0 CHECK(seeded IN (0,1))
);
INSERT INTO employee_store_state(id,epoch,seeded)
 SELECT 1,epoch,0 FROM handheld_state WHERE id=1;

-- Durable command results keep claim/transition retries harmless. Capability
-- secrets are reconstructed only with the employee secret, never stored raw.
CREATE TABLE employee_commands (
 employee_hash TEXT NOT NULL REFERENCES employee_sessions(token_hash),
 command_key TEXT NOT NULL,
 command_hash TEXT NOT NULL,
 order_id INTEGER NOT NULL REFERENCES orders(id),
 assignment_id INTEGER REFERENCES shopper_assignments(id),
 assignment_version INTEGER NOT NULL DEFAULT 0,
 grant_id INTEGER REFERENCES handheld_grants(id),
 result TEXT NOT NULL,
 created INTEGER NOT NULL,
 PRIMARY KEY(employee_hash,command_key)
);
