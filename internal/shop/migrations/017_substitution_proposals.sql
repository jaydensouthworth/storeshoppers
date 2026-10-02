-- Proposals are separate from chat and from the immutable placed receipt.
CREATE TABLE substitution_proposals (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 order_id INTEGER NOT NULL REFERENCES orders(id),
 line_id INTEGER NOT NULL REFERENCES working_order_items(id),
 grant_id INTEGER NOT NULL REFERENCES handheld_grants(id),
 assignment_id INTEGER NOT NULL REFERENCES shopper_assignments(id),
 assignment_version INTEGER NOT NULL CHECK(assignment_version>0),
 epoch TEXT NOT NULL CHECK(length(epoch)=64),
 command_key TEXT NOT NULL CHECK(length(command_key) BETWEEN 16 AND 100),
 command_hash TEXT NOT NULL CHECK(length(command_hash)=64),
 snapshot TEXT NOT NULL CHECK(length(CAST(snapshot AS BLOB)) BETWEEN 2 AND 16000 AND json_valid(snapshot) AND json_type(snapshot)='object' AND json_type(snapshot,'$.Command') IS 'object'),
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','rejected','withdrawn','stale')),
 created INTEGER NOT NULL CHECK(created>0),
 decided INTEGER NOT NULL DEFAULT 0,
 decision_key TEXT NOT NULL DEFAULT '',
 decision_hash TEXT NOT NULL DEFAULT '',
 CHECK((status='pending' AND decided=0 AND decision_key='' AND decision_hash='')
 OR (status IN ('approved','rejected') AND decided>0 AND length(decision_key) BETWEEN 16 AND 100 AND length(decision_hash)=64)
 OR (status IN ('withdrawn','stale') AND decided>0 AND decision_key='' AND decision_hash='')),
 UNIQUE(grant_id,command_key)
);
CREATE UNIQUE INDEX substitution_pending_line ON substitution_proposals(order_id,line_id) WHERE status='pending';
CREATE UNIQUE INDEX substitution_decision_key ON substitution_proposals(order_id,decision_key) WHERE decision_key<>'';
CREATE INDEX substitution_order_history ON substitution_proposals(order_id,id);
CREATE TRIGGER substitution_snapshot_immutable BEFORE UPDATE ON substitution_proposals
 WHEN NEW.id<>OLD.id OR NEW.order_id<>OLD.order_id OR NEW.line_id<>OLD.line_id OR NEW.grant_id<>OLD.grant_id
 OR NEW.assignment_id<>OLD.assignment_id OR NEW.assignment_version<>OLD.assignment_version OR NEW.epoch<>OLD.epoch
 OR NEW.command_key<>OLD.command_key OR NEW.command_hash<>OLD.command_hash OR NEW.snapshot<>OLD.snapshot OR NEW.created<>OLD.created
 OR OLD.status<>'pending' OR NEW.status='pending'
 BEGIN SELECT RAISE(ABORT,'substitution history is immutable'); END;
CREATE TRIGGER substitution_no_delete BEFORE DELETE ON substitution_proposals
 BEGIN SELECT RAISE(ABORT,'substitution history is retained'); END;
CREATE TRIGGER substitution_no_replace BEFORE INSERT ON substitution_proposals
 WHEN EXISTS(SELECT 1 FROM substitution_proposals WHERE id=NEW.id OR (grant_id=NEW.grant_id AND command_key=NEW.command_key))
 BEGIN SELECT RAISE(ABORT,'substitution history cannot be replaced'); END;
