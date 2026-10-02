-- Messages are an independent append-only stream, not fulfillment or manager audit.
-- Assignment values are immutable snapshots: reassignment mutates the source row.
CREATE TABLE order_messages (
 order_id INTEGER NOT NULL REFERENCES orders(id),
 revision INTEGER NOT NULL CHECK(revision>0),
 actor TEXT NOT NULL CHECK(actor IN ('customer','worker')),
 customer_session_id TEXT REFERENCES sessions(id),
 grant_id INTEGER REFERENCES handheld_grants(id),
 assignment_id INTEGER NOT NULL REFERENCES shopper_assignments(id),
 assignment_version INTEGER NOT NULL CHECK(assignment_version>0),
 shopper_id INTEGER NOT NULL REFERENCES shoppers(id),
 shopper_name TEXT NOT NULL CHECK(length(shopper_name) BETWEEN 1 AND 100),
 sender_name TEXT NOT NULL CHECK(length(sender_name) BETWEEN 1 AND 100),
 body TEXT NOT NULL CHECK(length(body) BETWEEN 1 AND 500 AND length(CAST(body AS BLOB))<=2000 AND instr(body,char(0))=0),
 created INTEGER NOT NULL CHECK(created BETWEEN 1 AND 253402300799),
 command_key TEXT NOT NULL CHECK(length(command_key) BETWEEN 16 AND 100),
 command_hash TEXT NOT NULL CHECK(length(command_hash)=64),
 PRIMARY KEY(order_id,revision),
 CHECK((actor='customer' AND customer_session_id IS NOT NULL AND grant_id IS NULL AND sender_name='Demo customer') OR
       (actor='worker' AND customer_session_id IS NULL AND grant_id IS NOT NULL AND sender_name=shopper_name))
);
CREATE UNIQUE INDEX order_messages_customer_command ON order_messages(order_id,customer_session_id,command_key) WHERE actor='customer';
CREATE UNIQUE INDEX order_messages_worker_command ON order_messages(order_id,grant_id,command_key) WHERE actor='worker';
CREATE INDEX order_messages_actor_time ON order_messages(order_id,actor,created);
CREATE INDEX order_messages_order_time ON order_messages(order_id,created);
CREATE INDEX order_messages_global_time ON order_messages(created);
CREATE TRIGGER order_messages_immutable_update BEFORE UPDATE ON order_messages BEGIN SELECT RAISE(ABORT,'messages are append-only'); END;
CREATE TRIGGER order_messages_immutable_delete BEFORE DELETE ON order_messages BEGIN SELECT RAISE(ABORT,'messages are append-only'); END;
-- SQLite REPLACE can bypass DELETE triggers unless recursive_triggers is set.
-- Reject every replacement explicitly so history is immutable in either mode.
CREATE TRIGGER order_messages_immutable_replace BEFORE INSERT ON order_messages
 WHEN EXISTS(SELECT 1 FROM order_messages m WHERE m.order_id=NEW.order_id AND
  (m.revision=NEW.revision OR (m.actor=NEW.actor AND m.command_key=NEW.command_key AND
   ((NEW.actor='customer' AND m.customer_session_id=NEW.customer_session_id) OR
    (NEW.actor='worker' AND m.grant_id=NEW.grant_id)))))
 BEGIN SELECT RAISE(ABORT,'messages are append-only'); END;
