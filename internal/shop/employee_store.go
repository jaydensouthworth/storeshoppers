package shop

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const employeeStoreScope = "employee-store"
const employeeTTL = 8 * time.Hour

var ErrEmployeeAccess = errors.New("Your demo employee session ended. Open the employee dashboard to start a new shift. Saved work is kept.")
var ErrEmployeePractice = errors.New("Practice orders have no customer or manager. Use a storefront demo order to try messages and manager reports.")
var ErrEmployeeClaim = errors.New("That order changed or another employee claimed it. Refresh the queue; no work was overwritten.")

type EmployeeSession struct {
	Token, Hash, CSRF, Epoch, Name string
	ShopperID, Expires             int64
}
type EmployeeQueueItem struct {
	ID, Version, Lines, Picked int64
	Reference, Status          string
	Mine, Assigned             bool
}
type EmployeeWorkspace struct {
	Employee                  EmployeeSession
	Queue                     []EmployeeQueueItem
	CSRF, Key, Message, Error string
	Seeded                    bool
	MineCount, AvailableCount int
}

func employeeSessionTx(tx *sql.Tx, raw, csrf string, write bool, now int64) (EmployeeSession, error) {
	var e EmployeeSession
	if !validHandheldToken(raw) {
		return e, ErrEmployeeAccess
	}
	err := tx.QueryRow(`SELECT e.token_hash,e.csrf,e.epoch,e.shopper_id,e.expires,s.name FROM employee_sessions e JOIN handheld_state h ON h.id=1 AND h.epoch=e.epoch JOIN shoppers s ON s.id=e.shopper_id AND s.scope=? LEFT JOIN shopper_roster_profiles p ON p.shopper_id=s.id AND p.scope=? WHERE e.token_hash=? AND e.expires>? AND e.revoked=0 AND COALESCE(p.archived,0)=0`, employeeStoreScope, employeeStoreScope, handheldHash(raw), now).Scan(&e.Hash, &e.CSRF, &e.Epoch, &e.ShopperID, &e.Expires, &e.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrEmployeeAccess
	}
	if err != nil {
		return e, err
	}
	if write && subtle.ConstantTimeCompare([]byte(csrf), []byte(e.CSRF)) != 1 {
		return e, ErrHandheldCSRF
	}
	return e, nil
}
func (s *Store) EmployeeSession(raw string) (EmployeeSession, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return EmployeeSession{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	e, err := employeeSessionTx(tx, raw, "", false, now)
	if err == nil {
		return e, tx.Commit()
	}
	if !errors.Is(err, ErrEmployeeAccess) {
		return e, err
	}
	var active int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM employee_sessions e JOIN handheld_state h ON h.id=1 AND h.epoch=e.epoch WHERE e.expires>? AND e.revoked=0`, now).Scan(&active); err != nil {
		return e, err
	}
	if active >= 500 {
		return e, ErrHandheldAttempts
	}
	e = EmployeeSession{Token: token(), CSRF: token(), Expires: now + int64(employeeTTL/time.Second)}
	e.Hash = handheldHash(e.Token)
	if err = tx.QueryRow(`SELECT epoch FROM handheld_state WHERE id=1`).Scan(&e.Epoch); err != nil {
		return e, err
	}
	if err = reconcileEmployeeAssignmentsTx(tx, e.Epoch, now); err != nil {
		return e, err
	}
	// Generic, stable, explicitly fictitious identity. Never copies a visitor's name.
	result, err := tx.Exec(`INSERT INTO shoppers(name,initials,scope,baseline) VALUES('Demo employee','DE',?,0)`, employeeStoreScope)
	if err != nil {
		return e, err
	}
	e.ShopperID, err = result.LastInsertId()
	if err != nil {
		return e, err
	}
	e.Name = fmt.Sprintf("Demo employee %04d", e.ShopperID)
	if _, err = tx.Exec(`UPDATE shoppers SET name=? WHERE id=?`, e.Name, e.ShopperID); err != nil {
		return e, err
	}
	if _, err = tx.Exec(`INSERT INTO shopper_roster_profiles(scope,shopper_id,name,initials,capacity) VALUES(?,?,?,'DE',3)`, employeeStoreScope, e.ShopperID, e.Name); err != nil {
		return e, err
	}
	if _, err = tx.Exec(`INSERT INTO employee_sessions(token_hash,csrf,epoch,shopper_id,created,expires) VALUES(?,?,?,?,?,?)`, e.Hash, e.CSRF, e.Epoch, e.ShopperID, now, e.Expires); err != nil {
		return e, err
	}
	return e, tx.Commit()
}

// Only explicitly enrolled public work is reconciled. Losing an employee
// capability releases ownership, never the order's allocation or saved picks.
func reconcileEmployeeAssignmentsTx(tx *sql.Tx, epoch string, now int64) error {
	rows, err := tx.Query(`SELECT a.id,a.order_id,a.shopper_id,a.shopper_name FROM shopper_assignments a JOIN employee_store_orders so ON so.order_id=a.order_id AND so.epoch=? JOIN orders o ON o.id=a.order_id AND o.status IN ('Placed','Picking') JOIN shoppers s ON s.id=a.shopper_id AND s.scope=? LEFT JOIN shopper_roster_profiles p ON p.shopper_id=s.id AND p.scope=? WHERE a.state='active' AND (COALESCE(p.archived,0)=1 OR NOT EXISTS(SELECT 1 FROM employee_sessions e WHERE e.shopper_id=a.shopper_id AND e.epoch=? AND e.expires>? AND e.revoked=0))`, epoch, employeeStoreScope, employeeStoreScope, epoch, now)
	if err != nil {
		return err
	}
	var expired []ShopperAssignment
	for rows.Next() {
		var a ShopperAssignment
		if err = rows.Scan(&a.ID, &a.OrderID, &a.ShopperID, &a.ShopperName); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, a := range expired {
		if err = releaseEmployeeAssignmentTx(tx, a, "employee-expired:"+token(), "", "Employee shift ended; available work returned to queue"); err != nil {
			return err
		}
	}
	return nil
}
func releaseEmployeeAssignmentTx(tx *sql.Tx, a ShopperAssignment, key, hash, reason string) error {
	if _, err := tx.Exec(`UPDATE shopper_assignments SET state='cancelled',version=version+1,ended=strftime('%Y-%m-%d %H:%M UTC','now') WHERE id=? AND state='active'`, a.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE orders SET order_version=order_version+1 WHERE id=?`, a.OrderID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE handheld_grants SET revoked=1 WHERE assignment_id=?`, a.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE handheld_invitations SET revoked=1 WHERE assignment_id=?`, a.ID); err != nil {
		return err
	}
	return recordShopperEvent(tx, a.OrderID, a.ID, a.ShopperID, 0, a.ShopperName, "", key, hash, "employee-release", reason, "Assignment released; receipt, allocation and saved picks retained")
}
func (s *Store) EmployeeQueue(raw string) (EmployeeWorkspace, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return EmployeeWorkspace{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	e, err := employeeSessionTx(tx, raw, "", false, now)
	if err != nil {
		return EmployeeWorkspace{}, err
	}
	out := EmployeeWorkspace{Employee: e, CSRF: e.CSRF, Key: token()}
	if err = reconcileEmployeeAssignmentsTx(tx, e.Epoch, now); err != nil {
		return out, err
	}
	if err = tx.QueryRow(`SELECT seeded FROM employee_store_state WHERE id=1 AND epoch=?`, e.Epoch).Scan(&out.Seeded); err != nil {
		return out, err
	}
	rows, err := tx.Query(`SELECT o.id,o.reference,o.status,o.order_version,COUNT(w.id),COALESCE(SUM(CASE WHEN (w.sale_unit='each' AND w.picked_quantity=w.quantity) OR (w.sale_unit='g' AND w.measurement_confirmed=1 AND w.unavailable_quantity=0 AND w.cancelled_quantity=0) THEN 1 ELSE 0 END),0),a.id IS NOT NULL,COALESCE(a.shopper_id=?,0) FROM employee_store_orders so JOIN orders o ON o.id=so.order_id LEFT JOIN shopper_assignments a ON a.order_id=o.id AND a.state='active' LEFT JOIN working_order_items w ON w.order_id=o.id AND w.quantity>0 WHERE so.epoch=? AND o.status IN ('Placed','Picking') GROUP BY o.id ORDER BY CASE WHEN a.shopper_id=? THEN 0 WHEN a.id IS NULL THEN 1 ELSE 2 END,o.id LIMIT 100`, e.ShopperID, e.Epoch, e.ShopperID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item EmployeeQueueItem
		if err = rows.Scan(&item.ID, &item.Reference, &item.Status, &item.Version, &item.Lines, &item.Picked, &item.Assigned, &item.Mine); err != nil {
			rows.Close()
			return out, err
		}
		out.Queue = append(out.Queue, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if err = tx.QueryRow(`SELECT COALESCE(SUM(a.shopper_id=?),0),COALESCE(SUM(a.id IS NULL),0) FROM employee_store_orders so JOIN orders o ON o.id=so.order_id LEFT JOIN shopper_assignments a ON a.order_id=o.id AND a.state='active' WHERE so.epoch=? AND o.status IN ('Placed','Picking')`, e.ShopperID, e.Epoch).Scan(&out.MineCount, &out.AvailableCount); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

type employeeOrder struct {
	Owner, Status string
	Version       int64
	Held          bool
}

func employeeOrderTx(tx *sql.Tx, e EmployeeSession, id int64) (employeeOrder, error) {
	var o employeeOrder
	err := tx.QueryRow(`SELECT o.session_id,o.status,o.order_version,o.attention_reason<>'' FROM employee_store_orders so JOIN orders o ON o.id=so.order_id WHERE so.order_id=? AND so.epoch=?`, id, e.Epoch).Scan(&o.Owner, &o.Status, &o.Version, &o.Held)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrEmployeeClaim
	}
	return o, err
}
func employeeCommand(action, key string, id, version int64) (string, string, error) {
	if len(key) < 16 || len(key) > 150 || id < 1 || version < 1 {
		return "", "", ErrInvalid
	}
	return action + ":" + key, handheldPayload([]any{"employee-v1", action, id, version}), nil
}
func employeeEventKey(e EmployeeSession, key string) string { return "employee:" + e.Hash + ":" + key }

// Capability derivation uses the high-entropy employee bearer secret. Neither
// secret is stored raw. Identical claim retries return the SAME capability, so
// duplicate responses cannot revoke each other or strand a lost response.
func employeeGrantToken(raw, key string, aid, av int64) string {
	return handheldPayload([]any{"employee-grant-v1", raw, key, aid, av})
}
func (s *Store) EmployeeClaim(raw, csrf, key string, id, version int64) (HandheldSession, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return HandheldSession{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	e, err := employeeSessionTx(tx, raw, csrf, true, now)
	if err != nil {
		return HandheldSession{}, err
	}
	command, hash, err := employeeCommand("claim", key, id, version)
	if err != nil {
		return HandheldSession{}, err
	}
	o, err := employeeOrderTx(tx, e, id)
	if err != nil {
		return HandheldSession{}, err
	}
	if o.Status != "Placed" && o.Status != "Picking" {
		return HandheldSession{}, ErrEmployeeClaim
	}
	if err = reconcileEmployeeAssignmentsTx(tx, e.Epoch, now); err != nil {
		return HandheldSession{}, err
	}
	// A released expired assignment has advanced the order version; callers must
	// refresh and review instead of claiming with their stale queue snapshot.
	o, err = employeeOrderTx(tx, e, id)
	if err != nil {
		return HandheldSession{}, err
	}
	assignment, err := activeAssignment(tx, id)
	if err != nil {
		return HandheldSession{}, err
	}
	var prior string
	var priorAssignment, priorVersion, grantID int64
	err = tx.QueryRow(`SELECT command_hash,assignment_id,assignment_version,grant_id FROM employee_commands WHERE employee_hash=? AND command_key=?`, e.Hash, command).Scan(&prior, &priorAssignment, &priorVersion, &grantID)
	if err == nil {
		if prior != hash {
			return HandheldSession{}, ErrConflict
		}
		if assignment == nil || assignment.ShopperID != e.ShopperID || assignment.ID != priorAssignment || assignment.Version != priorVersion {
			return HandheldSession{}, ErrEmployeeClaim
		}
		g := HandheldSession{Token: employeeGrantToken(raw, command, priorAssignment, priorVersion)}
		err = tx.QueryRow(`SELECT csrf,expires FROM handheld_grants WHERE id=? AND token_hash=? AND revoked=0 AND expires>?`, grantID, handheldHash(g.Token), now).Scan(&g.CSRF, &g.Expires)
		if errors.Is(err, sql.ErrNoRows) {
			return HandheldSession{}, ErrEmployeeClaim
		}
		if err != nil {
			return HandheldSession{}, err
		}
		return g, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return HandheldSession{}, err
	}
	if assignment != nil && assignment.ShopperID != e.ShopperID {
		return HandheldSession{}, ErrEmployeeClaim
	}
	if o.Version != version {
		return HandheldSession{}, ErrEmployeeClaim
	}
	if assignment == nil {
		var count int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM shopper_assignments a JOIN orders o ON o.id=a.order_id WHERE a.shopper_id=? AND a.state='active' AND o.status IN ('Placed','Picking')`, e.ShopperID).Scan(&count); err != nil {
			return HandheldSession{}, err
		}
		if count >= 3 {
			return HandheldSession{}, ErrShopperCapacity
		}
		res, x := tx.Exec(`INSERT INTO shopper_assignments(order_id,shopper_id,shopper_name,shopper_initials) VALUES(?,?,?,'DE')`, id, e.ShopperID, e.Name)
		if x != nil {
			return HandheldSession{}, x
		}
		aid, x := res.LastInsertId()
		if x != nil {
			return HandheldSession{}, x
		}
		assignment = &ShopperAssignment{ID: aid, OrderID: id, Version: 1, ShopperID: e.ShopperID, ShopperName: e.Name}
		if _, err = tx.Exec(`UPDATE orders SET status='Picking',order_version=order_version+1 WHERE id=?`, id); err != nil {
			return HandheldSession{}, err
		}
		if err = recordShopperEvent(tx, id, aid, 0, e.ShopperID, "", e.Name, employeeEventKey(e, command), hash, "employee-claim", "Demo employee claimed available work", "Claimed atomically; receipt, stock allocation and saved picks retained"); err != nil {
			return HandheldSession{}, err
		}
	}
	if _, err = tx.Exec(`UPDATE handheld_grants SET revoked=1 WHERE id IN (SELECT grant_id FROM employee_grant_links WHERE employee_hash=?) AND assignment_id=?`, e.Hash, assignment.ID); err != nil {
		return HandheldSession{}, err
	}
	g := HandheldSession{Token: employeeGrantToken(raw, command, assignment.ID, assignment.Version), CSRF: token(), Expires: e.Expires}
	res, err := tx.Exec(`INSERT INTO handheld_invitations(secret_hash,epoch,owner_session_id,scope,assignment_id,assignment_version,shopper_id,created,expires,consumed,command_key,command_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, handheldHash(token()), e.Epoch, o.Owner, employeeStoreScope, assignment.ID, assignment.Version, e.ShopperID, now, g.Expires, now, token(), hash)
	if err != nil {
		return g, err
	}
	iid, err := res.LastInsertId()
	if err != nil {
		return g, err
	}
	res, err = tx.Exec(`INSERT INTO handheld_grants(token_hash,csrf,epoch,owner_session_id,scope,assignment_id,assignment_version,shopper_id,invitation_id,created,expires) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, handheldHash(g.Token), g.CSRF, e.Epoch, o.Owner, employeeStoreScope, assignment.ID, assignment.Version, e.ShopperID, iid, now, g.Expires)
	if err != nil {
		return g, err
	}
	gid, err := res.LastInsertId()
	if err != nil {
		return g, err
	}
	if _, err = tx.Exec(`INSERT INTO employee_grant_links(grant_id,employee_hash) VALUES(?,?)`, gid, e.Hash); err != nil {
		return g, err
	}
	if _, err = tx.Exec(`INSERT INTO employee_commands(employee_hash,command_key,command_hash,order_id,assignment_id,assignment_version,grant_id,result,created) VALUES(?,?,?,?,?,?,?,'claimed',?)`, e.Hash, command, hash, id, assignment.ID, assignment.Version, gid, now); err != nil {
		return g, err
	}
	return g, tx.Commit()
}

func (s *Store) EmployeeRelease(raw, csrf, key string, id, version int64) error {
	return s.employeeTransition(raw, csrf, key, id, version, false)
}
func (s *Store) EmployeeReady(raw, csrf, key string, id, version int64) error {
	return s.employeeTransition(raw, csrf, key, id, version, true)
}
func (s *Store) employeeTransition(raw, csrf, key string, id, version int64, ready bool) error {
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	e, err := employeeSessionTx(tx, raw, csrf, true, now)
	if err != nil {
		return err
	}
	action := "release"
	if ready {
		action = "ready"
	}
	command, hash, err := employeeCommand(action, key, id, version)
	if err != nil {
		return err
	}
	o, err := employeeOrderTx(tx, e, id)
	if err != nil {
		return err
	}
	var prior string
	err = tx.QueryRow(`SELECT command_hash FROM employee_commands WHERE employee_hash=? AND command_key=?`, e.Hash, command).Scan(&prior)
	if err == nil {
		if prior != hash {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	a, err := activeAssignment(tx, id)
	if err != nil {
		return err
	}
	if a == nil || a.ShopperID != e.ShopperID || o.Version != version || (o.Status != "Placed" && o.Status != "Picking") {
		return ErrEmployeeClaim
	}
	if ready {
		if o.Held {
			return ErrAttention
		}
		items, x := workingOrderItems(tx, id)
		if x != nil {
			return x
		}
		if len(items) == 0 {
			return ErrIncomplete
		}
		var final int64
		for _, item := range items {
			if !item.Complete() {
				return ErrIncomplete
			}
			amount, x := lineAmount(item.Picked, item.Price, item.PriceBasis, item.SaleUnit)
			if x != nil {
				return x
			}
			final, x = addAmount(final, amount)
			if x != nil {
				return x
			}
		}
		if _, err = tx.Exec(`UPDATE orders SET status='Ready',order_version=order_version+1,final_total=?,completion_kind='full' WHERE id=?`, final, id); err != nil {
			return err
		}
		if err = recordShopperEvent(tx, id, a.ID, e.ShopperID, e.ShopperID, e.Name, e.Name, employeeEventKey(e, command), hash, "employee-ready", "Employee confirmed all items picked", "Ready for collection; immutable placed receipt retained"); err != nil {
			return err
		}
		if err = endShopperAssignment(tx, id, "Ready"); err != nil {
			return err
		}
	} else if err = releaseEmployeeAssignmentTx(tx, *a, employeeEventKey(e, command), hash, "Employee returned unfinished work to the shared queue"); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO employee_commands(employee_hash,command_key,command_hash,order_id,assignment_id,assignment_version,result,created) VALUES(?,?,?,?,?,?,?,?)`, e.Hash, command, hash, id, a.ID, a.Version, action, now); err != nil {
		return err
	}
	return tx.Commit()
}

// A deliberate POST seeds at most three one-line counted orders once per reset.
// All allocations, receipts, membership and the one-time flag commit together.
// The synthetic owner secret is never returned or used as a visitor session.
func (s *Store) SeedEmployeePractice(raw, csrf string) error {
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	e, err := employeeSessionTx(tx, raw, csrf, true, now)
	if err != nil {
		return err
	}
	var seeded bool
	if err = tx.QueryRow(`SELECT seeded FROM employee_store_state WHERE id=1 AND epoch=?`, e.Epoch).Scan(&seeded); err != nil {
		return err
	}
	if seeded {
		return tx.Commit()
	}
	if err = expireHolds(tx, now); err != nil {
		return err
	}
	prices, err := loadPricing(tx, now)
	if err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT ` + productSelect + productJoins + `WHERE p.archived=0 AND c.archived=0 AND COALESCE(t.archived,0)=0 AND p.sale_unit='each' AND p.stock>=1 ORDER BY p.id LIMIT 3`)
	if err != nil {
		return err
	}
	var products []Product
	for rows.Next() {
		p, x := scanProduct(rows)
		if x != nil {
			rows.Close()
			return x
		}
		prices.apply(&p)
		products = append(products, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(products) > 0 {
		owner := token()
		if _, err = tx.Exec(`INSERT INTO sessions(id,csrf,checkout_key,expires) VALUES(?,?,?,?)`, owner, token(), token(), now); err != nil {
			return err
		}
		for _, p := range products {
			if _, err = tx.Exec(`UPDATE products SET stock=stock-1,version=version+1 WHERE id=? AND stock>=1`, p.ID); err != nil {
				return err
			}
			price := p.EffectivePrice()
			res, x := tx.Exec(`INSERT INTO orders(reference,session_id,checkout_key,total,instructions) VALUES(?,?,?,?,?)`, "PRACTICE-"+strings.ToUpper(token()[:8]), owner, token(), price, "Fictional employee practice order. Scan the demo label and record one unit.")
			if x != nil {
				return x
			}
			id, x := res.LastInsertId()
			if x != nil {
				return x
			}
			if _, err = tx.Exec(`INSERT INTO order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,subtotal) VALUES(?,?,?,?,1,?,'each',1,1,?)`, id, p.ID, p.Name, price, p.SKU, price); err != nil {
				return err
			}
			if _, err = tx.Exec(`INSERT INTO working_order_items(order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,allocated_quantity) SELECT order_id,product_id,name,price,quantity,sku,sale_unit,price_basis,quantity_step,quantity FROM order_items WHERE order_id=?`, id); err != nil {
				return err
			}
			if _, err = tx.Exec(`INSERT INTO employee_store_orders(order_id,epoch,practice) VALUES(?,?,1)`, id, e.Epoch); err != nil {
				return err
			}
			if _, err = tx.Exec(`INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details) VALUES(?,?,'','employee-practice','Created one-time fictional practice work','One counted unit reserved atomically from shared stock')`, id, "employee-practice:"+token()); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(`UPDATE employee_store_state SET seeded=1 WHERE id=1 AND epoch=?`, e.Epoch); err != nil {
		return err
	}
	return tx.Commit()
}
