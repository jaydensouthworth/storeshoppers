-- Fixed, explicitly simulated identities. No existing orders are assigned.
CREATE TABLE shoppers (
 id INTEGER PRIMARY KEY,
 name TEXT NOT NULL UNIQUE,
 initials TEXT NOT NULL
);
INSERT INTO shoppers(id,name,initials) VALUES
 (1,'Avery Morgan','AM'),(2,'Jordan Lee','JL'),(3,'Casey Rivera','CR');
CREATE TABLE shopper_assignments (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 order_id INTEGER NOT NULL REFERENCES orders(id),
 shopper_id INTEGER NOT NULL REFERENCES shoppers(id),
 state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','cancelled','ended')),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>0),
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now')),
 ended TEXT NOT NULL DEFAULT '',
 CHECK((state='active' AND ended='') OR (state<>'active' AND ended<>''))
);
CREATE UNIQUE INDEX shopper_one_active_order ON shopper_assignments(order_id) WHERE state='active';
CREATE INDEX shopper_active_roster ON shopper_assignments(shopper_id,state);
-- The audit itself remains in order_events, with structured assignee snapshots
-- for history filtering even after a reassignment changes the active task.
CREATE TABLE shopper_event_links (
 event_id INTEGER PRIMARY KEY REFERENCES order_events(id),
 assignment_id INTEGER NOT NULL REFERENCES shopper_assignments(id),
 from_shopper_id INTEGER REFERENCES shoppers(id),
 to_shopper_id INTEGER REFERENCES shoppers(id)
);
INSERT INTO schema_version VALUES(6);
