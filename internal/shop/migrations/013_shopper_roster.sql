-- Stable fake identities; names need only be unique within an effective roster.
-- Rebuilding removes the former global UNIQUE(name) which would expose another
-- visitor's identity through duplicate-name errors. Foreign keys are disabled
-- only by the existing pinned, all-or-nothing migration transaction.
CREATE TABLE shoppers_v13 (
 id INTEGER PRIMARY KEY,
 name TEXT NOT NULL,
 initials TEXT NOT NULL,
 scope TEXT NOT NULL DEFAULT '',
 baseline INTEGER NOT NULL DEFAULT 0 CHECK(baseline IN (0,1))
);
INSERT INTO shoppers_v13(id,name,initials,baseline) SELECT id,name,initials,1 FROM shoppers;
DROP TABLE shoppers;
CREATE TABLE shoppers (
 id INTEGER PRIMARY KEY,
 name TEXT NOT NULL,
 initials TEXT NOT NULL,
 scope TEXT NOT NULL DEFAULT '',
 baseline INTEGER NOT NULL DEFAULT 0 CHECK(baseline IN (0,1))
);
INSERT INTO shoppers SELECT * FROM shoppers_v13;
DROP TABLE shoppers_v13;
CREATE INDEX shoppers_scope ON shoppers(scope,id);
CREATE TABLE shopper_roster_profiles (
 scope TEXT NOT NULL,
 shopper_id INTEGER NOT NULL REFERENCES shoppers(id),
 name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 80),
 initials TEXT NOT NULL CHECK(length(initials) BETWEEN 1 AND 4),
 availability TEXT NOT NULL DEFAULT 'available' CHECK(availability IN ('available','break','off_shift')),
 capacity INTEGER NOT NULL DEFAULT 0 CHECK(capacity BETWEEN 0 AND 20),
 archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>0),
 PRIMARY KEY(scope,shopper_id)
);
-- A missing session profile means the immutable seed defaults. Thus merely
-- looking at a roster cannot make another visitor's edits visible or copy them.
INSERT INTO shopper_roster_profiles(scope,shopper_id,name,initials)
 SELECT '',id,name,initials FROM shoppers;
CREATE TABLE shopper_roster_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 scope TEXT NOT NULL,
 shopper_id INTEGER NOT NULL REFERENCES shoppers(id),
 command_key TEXT NOT NULL,
 command_hash TEXT NOT NULL,
 name TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('create','edit','archive','restore')),
 details TEXT NOT NULL,
 created TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M UTC','now')),
 UNIQUE(scope,command_key)
);
ALTER TABLE shopper_assignments ADD COLUMN shopper_name TEXT NOT NULL DEFAULT '';
ALTER TABLE shopper_assignments ADD COLUMN shopper_initials TEXT NOT NULL DEFAULT '';
UPDATE shopper_assignments SET shopper_name=(SELECT name FROM shoppers WHERE id=shopper_id),shopper_initials=(SELECT initials FROM shoppers WHERE id=shopper_id);
ALTER TABLE shopper_event_links ADD COLUMN from_shopper_name TEXT NOT NULL DEFAULT '';
ALTER TABLE shopper_event_links ADD COLUMN to_shopper_name TEXT NOT NULL DEFAULT '';
UPDATE shopper_event_links SET from_shopper_name=COALESCE((SELECT name FROM shoppers WHERE id=from_shopper_id),''),to_shopper_name=COALESCE((SELECT name FROM shoppers WHERE id=to_shopper_id),'');
