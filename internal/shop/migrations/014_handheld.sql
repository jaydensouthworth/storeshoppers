-- Bounded worker capabilities are separate from customer/manager sessions.
-- Secrets are never persisted: invites and device tokens are SHA-256 digests.
CREATE TABLE handheld_state (
 id INTEGER PRIMARY KEY CHECK(id=1),
 epoch TEXT NOT NULL,
 attempt_window INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0
);
INSERT INTO handheld_state(id,epoch) VALUES(1,lower(hex(randomblob(32))));
CREATE TABLE handheld_pair_sessions (
 token_hash TEXT PRIMARY KEY CHECK(length(token_hash)=64),
 csrf TEXT NOT NULL,
 epoch TEXT NOT NULL,
 expires INTEGER NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 attempt_window INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE handheld_invitations (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 secret_hash TEXT NOT NULL UNIQUE CHECK(length(secret_hash)=64),
 epoch TEXT NOT NULL,
 owner_session_id TEXT NOT NULL REFERENCES sessions(id),
 scope TEXT NOT NULL,
 assignment_id INTEGER NOT NULL REFERENCES shopper_assignments(id),
 assignment_version INTEGER NOT NULL CHECK(assignment_version>0),
 shopper_id INTEGER NOT NULL REFERENCES shoppers(id),
 created INTEGER NOT NULL,
 expires INTEGER NOT NULL,
 consumed INTEGER NOT NULL DEFAULT 0,
 revoked INTEGER NOT NULL DEFAULT 0,
 command_key TEXT NOT NULL,
 command_hash TEXT NOT NULL,
 UNIQUE(assignment_id,command_key)
);
CREATE INDEX handheld_invitation_task ON handheld_invitations(assignment_id,assignment_version);
CREATE TABLE handheld_grants (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 token_hash TEXT NOT NULL UNIQUE CHECK(length(token_hash)=64),
 csrf TEXT NOT NULL,
 epoch TEXT NOT NULL,
 owner_session_id TEXT NOT NULL REFERENCES sessions(id),
 scope TEXT NOT NULL,
 assignment_id INTEGER NOT NULL REFERENCES shopper_assignments(id),
 assignment_version INTEGER NOT NULL CHECK(assignment_version>0),
 shopper_id INTEGER NOT NULL REFERENCES shoppers(id),
 invitation_id INTEGER NOT NULL UNIQUE REFERENCES handheld_invitations(id),
 created INTEGER NOT NULL,
 expires INTEGER NOT NULL,
 revoked INTEGER NOT NULL DEFAULT 0,
 last_recorded INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX handheld_grant_task ON handheld_grants(assignment_id,assignment_version);
-- Absence means legacy unknown attribution, not an invented worker identity.
CREATE TABLE order_event_actors (
 event_id INTEGER PRIMARY KEY REFERENCES order_events(id),
 actor TEXT NOT NULL CHECK(actor IN ('manager','worker')),
 source TEXT NOT NULL CHECK(source IN ('manager','manual','camera','photo')),
 grant_id INTEGER REFERENCES handheld_grants(id),
 assignment_id INTEGER REFERENCES shopper_assignments(id),
 shopper_id INTEGER REFERENCES shoppers(id),
 shopper_name TEXT NOT NULL DEFAULT '',
 CHECK((actor='manager' AND source='manager' AND grant_id IS NULL) OR
       (actor='worker' AND source IN ('manual','camera','photo') AND grant_id IS NOT NULL AND assignment_id IS NOT NULL AND shopper_id IS NOT NULL))
);
-- Replay stores the exact successful outcome, even if another valid command
-- changes the task later. Grant validity is checked before reading this table.
CREATE TABLE handheld_pick_commands (
 grant_id INTEGER NOT NULL REFERENCES handheld_grants(id),
 command_key TEXT NOT NULL,
 command_hash TEXT NOT NULL,
 event_id INTEGER NOT NULL REFERENCES order_events(id),
 line_id INTEGER NOT NULL REFERENCES working_order_items(id),
 picked INTEGER NOT NULL,
 order_version INTEGER NOT NULL,
 pick_version INTEGER NOT NULL,
 PRIMARY KEY(grant_id,command_key)
);
CREATE TABLE handheld_pair_commands (
 assignment_id INTEGER NOT NULL REFERENCES shopper_assignments(id),
 command_key TEXT NOT NULL,
 command_hash TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('issue','revoke')),
 created INTEGER NOT NULL,
 PRIMARY KEY(assignment_id,command_key)
);
