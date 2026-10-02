package shop

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Shopper is a fixed simulated roster identity, not a login or a live device.
// Progress comes only from currently assigned orders' recorded working lines.
type Shopper struct {
	ID                                      int64
	Name, Initials                          string
	ActiveOrders, Picked, Required, Percent int64
}
type ShopperAssignment struct {
	ID, OrderID, ShopperID, Version    int64
	ShopperName, State, Created, Ended string
}
type ShopperTask struct {
	Order      Order
	Assignment *ShopperAssignment
}
type ShopperEvent struct {
	OrderID                                     int64
	Reference, Action, Reason, Details, Created string
}
type ShopperCommand struct {
	Action, Key, Reason                   string
	Version, AssignmentVersion, ShopperID int64
}
type ShopperDraft struct {
	Action, ShopperID, Reason string
	Truncated                 bool
}
type ShoppersWorkspace struct {
	Query, Status, ShopperFilter, ContextQuery          string
	Roster                                              []Shopper
	Tasks                                               []ShopperTask
	Selected                                            *ShopperTask
	Events                                              []ShopperEvent
	OpenOrders, AssignedOrders, UnassignedOrders, Total int64
	CommandKey                                          string
	Page, Pages, First, Last                            int
	PreviousURL, NextURL                                string
	Draft                                               *ShopperDraft
}

type assignmentQuerier interface{ QueryRow(string, ...any) *sql.Row }

func activeAssignment(q assignmentQuerier, id int64) (*ShopperAssignment, error) {
	var a ShopperAssignment
	err := q.QueryRow(`SELECT a.id,a.order_id,a.shopper_id,a.version,s.name,a.state,a.created,a.ended FROM shopper_assignments a JOIN shoppers s ON s.id=a.shopper_id WHERE a.order_id=? AND a.state='active'`, id).Scan(&a.ID, &a.OrderID, &a.ShopperID, &a.Version, &a.ShopperName, &a.State, &a.Created, &a.Ended)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

// AssignShopper changes task ownership, never order allocation or picking. The
// order revision serializes assignment edits with working edits and closure.
func (s *Store) AssignShopper(id int64, sid string, allOrders bool, c ShopperCommand) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	var version int64
	err = tx.QueryRow(`SELECT status,order_version FROM orders WHERE id=? AND (? OR session_id=?)`, id, allOrders, sid).Scan(&status, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	c.Reason = strings.TrimSpace(c.Reason)
	if c.Action == "cancel" {
		c.ShopperID = 0
	}
	if c.Version < 1 || c.AssignmentVersion < 0 || len(c.Key) < 16 || len(c.Key) > 150 || !utf8.ValidString(c.Reason) || utf8.RuneCountInString(c.Reason) < 3 || utf8.RuneCountInString(c.Reason) > 240 || strings.IndexFunc(c.Reason, unicode.IsControl) >= 0 {
		return ErrInvalid
	}
	if c.Action != "assign" && c.Action != "reassign" && c.Action != "cancel" {
		return ErrInvalid
	}
	if (c.Action == "cancel" && c.ShopperID != 0) || (c.Action != "cancel" && c.ShopperID < 1) {
		return ErrInvalid
	}
	encoded, _ := json.Marshal(c)
	sum := sha256.Sum256(encoded)
	hash := hex.EncodeToString(sum[:])
	var prior string
	err = tx.QueryRow(`SELECT command_hash FROM order_events WHERE order_id=? AND command_key=?`, id, c.Key).Scan(&prior)
	if err == nil {
		if prior == hash {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if status == "Ready" || status == "Completed" {
		return ErrTerminal
	}
	if version != c.Version {
		return ErrConflict
	}
	current, err := activeAssignment(tx, id)
	if err != nil {
		return err
	}
	if current == nil {
		if c.Action != "assign" || c.AssignmentVersion != 0 {
			return ErrConflict
		}
	} else if c.Action == "assign" || c.AssignmentVersion != current.Version {
		return ErrConflict
	}
	var name string
	if c.Action != "cancel" {
		err = tx.QueryRow(`SELECT name FROM shoppers WHERE id=?`, c.ShopperID).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalid
		}
		if err != nil {
			return err
		}
		if current != nil && current.ShopperID == c.ShopperID {
			return ErrInvalid
		}
	}
	var assignmentID, from, to int64
	var details string
	switch c.Action {
	case "assign":
		result, e := tx.Exec(`INSERT INTO shopper_assignments(order_id,shopper_id) VALUES(?,?)`, id, c.ShopperID)
		if e != nil {
			return e
		}
		assignmentID, err = result.LastInsertId()
		to = c.ShopperID
		details = fmt.Sprintf("Assigned task %d to %s (simulated shopper); order remains %s", assignmentID, name, status)
	case "reassign":
		assignmentID, from, to = current.ID, current.ShopperID, c.ShopperID
		_, err = tx.Exec(`UPDATE shopper_assignments SET shopper_id=?,version=version+1 WHERE id=?`, to, assignmentID)
		details = fmt.Sprintf("Task %d: %s → %s (simulated shoppers); recorded picking progress retained", assignmentID, current.ShopperName, name)
	case "cancel":
		assignmentID, from = current.ID, current.ShopperID
		_, err = tx.Exec(`UPDATE shopper_assignments SET state='cancelled',version=version+1,ended=strftime('%Y-%m-%d %H:%M UTC','now') WHERE id=?`, assignmentID)
		details = fmt.Sprintf("Cancelled task %d for %s; order remains %s and is unassigned. Assign another shopper to continue; stock and picked quantities unchanged", assignmentID, current.ShopperName, status)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE orders SET order_version=order_version+1 WHERE id=?`, id); err != nil {
		return err
	}
	if err = recordShopperEvent(tx, id, assignmentID, from, to, c.Key, hash, "shopper-"+c.Action, c.Reason, details); err != nil {
		return err
	}
	return tx.Commit()
}
func recordShopperEvent(tx *sql.Tx, orderID, assignmentID, from, to int64, key, hash, action, reason, details string) error {
	result, err := tx.Exec(`INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details) VALUES(?,?,?,?,?,?)`, orderID, key, hash, action, reason, details)
	if err != nil {
		return err
	}
	eventID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO shopper_event_links(event_id,assignment_id,from_shopper_id,to_shopper_id) VALUES(?,?,NULLIF(?,0),NULLIF(?,0))`, eventID, assignmentID, from, to)
	return err
}

// Called inside the transaction that freezes the order. An audit failure must
// roll back both closure and assignment state, including every stock effect.
func endShopperAssignment(tx *sql.Tx, id int64, outcome string) error {
	current, err := activeAssignment(tx, id)
	if err != nil || current == nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE shopper_assignments SET state='ended',version=version+1,ended=strftime('%Y-%m-%d %H:%M UTC','now') WHERE id=?`, current.ID); err != nil {
		return err
	}
	return recordShopperEvent(tx, id, current.ID, current.ShopperID, 0, token(), "", "shopper-ended", "Order fulfillment ended the shopping task", fmt.Sprintf("Task %d for %s ended automatically: %s", current.ID, current.ShopperName, outcome))
}

// Every aggregate, filter and page starts with scoped_orders. The fixed roster
// is public demo data; all workload and history is derived from authorized rows.
const shopperOrderCTE = `WITH scoped_orders AS (
 SELECT o.* FROM orders o WHERE (? OR o.session_id=?)
), progress AS (
 SELECT o.id,COALESCE(SUM(CASE WHEN w.sale_unit='each' THEN w.picked_quantity ELSE 0 END),0) picked,
 COALESCE(SUM(CASE WHEN w.sale_unit='each' THEN w.quantity ELSE 0 END),0) required
 FROM scoped_orders o LEFT JOIN working_order_items w ON w.order_id=o.id GROUP BY o.id
), historical_shoppers AS (
 SELECT DISTINCT e.order_id,s.id shopper_id,s.name
 FROM scoped_orders o JOIN order_events e ON e.order_id=o.id
 JOIN shopper_event_links l ON l.event_id=e.id
 JOIN shoppers s ON s.id=l.from_shopper_id OR s.id=l.to_shopper_id
), tasks AS (
 SELECT o.*,p.picked,p.required,
 COALESCE((SELECT group_concat(h.name,' ') FROM historical_shoppers h WHERE h.order_id=o.id),'') historical_names,
 a.id assignment_id,a.shopper_id,a.version assignment_version,a.state assignment_state,a.created assignment_created,a.ended assignment_ended,s.name shopper_name
 FROM scoped_orders o JOIN progress p ON p.id=o.id
 LEFT JOIN shopper_assignments a ON a.order_id=o.id AND a.state='active'
 LEFT JOIN shoppers s ON s.id=a.shopper_id
) `
const shopperTaskFilter = ` WHERE (?='' OR instr(lower(reference||' '||status||' '||completion_kind||' '||COALESCE(shopper_name,'')||CASE WHEN status IN ('Ready','Completed') THEN ' '||historical_names ELSE '' END),lower(?))>0)
 AND (?='all' OR (?='open' AND status IN ('Placed','Picking')) OR (?='active' AND assignment_id IS NOT NULL) OR (?='unassigned' AND status IN ('Placed','Picking') AND assignment_id IS NULL) OR (?='closed' AND status IN ('Ready','Completed')))
 AND (?=0 OR shopper_id=? OR (status IN ('Ready','Completed') AND EXISTS(
 SELECT 1 FROM historical_shoppers h WHERE h.order_id=tasks.id AND h.shopper_id=?))) `

func (s *Store) Shoppers(f ShopperFilters, sid string, allOrders bool, selected int64) (*ShoppersWorkspace, error) {
	out := &ShoppersWorkspace{Query: f.Query, Status: f.Status, ShopperFilter: f.ShopperFilter, Page: f.Page, CommandKey: token()}
	if selected > 0 {
		o, err := s.Order(selected, sid, allOrders)
		if err != nil {
			return nil, err
		}
		out.Selected = &ShopperTask{Order: o, Assignment: o.Assignment}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(shopperOrderCTE+`SELECT COALESCE(SUM(CASE WHEN status IN ('Placed','Picking') THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN assignment_id IS NOT NULL THEN 1 ELSE 0 END),0) FROM tasks`, allOrders, sid).Scan(&out.OpenOrders, &out.AssignedOrders)
	if err != nil {
		return nil, err
	}
	out.UnassignedOrders = out.OpenOrders - out.AssignedOrders
	rows, err := tx.Query(shopperOrderCTE+`SELECT s.id,s.name,s.initials,COUNT(t.id),COALESCE(SUM(t.picked),0),COALESCE(SUM(t.required),0) FROM shoppers s LEFT JOIN tasks t ON t.shopper_id=s.id GROUP BY s.id ORDER BY s.id`, allOrders, sid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sh Shopper
		if err = rows.Scan(&sh.ID, &sh.Name, &sh.Initials, &sh.ActiveOrders, &sh.Picked, &sh.Required); err != nil {
			rows.Close()
			return nil, err
		}
		if sh.Required > 0 {
			sh.Percent = 100 * sh.Picked / sh.Required
		}
		out.Roster = append(out.Roster, sh)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	args := []any{allOrders, sid, f.Query, f.Query, f.Status, f.Status, f.Status, f.Status, f.Status, f.ShopperID, f.ShopperID, f.ShopperID}
	if err = tx.QueryRow(shopperOrderCTE+`SELECT COUNT(*) FROM tasks`+shopperTaskFilter, args...).Scan(&out.Total); err != nil {
		return nil, err
	}
	out.Pages = int((out.Total + 19) / 20)
	if out.Pages < 1 {
		out.Pages = 1
	}
	if out.Page > out.Pages {
		out.Page = out.Pages
	}
	f.Page = out.Page
	out.ContextQuery = f.Values().Encode()
	if out.Page > 1 {
		previous := f
		previous.Page--
		out.PreviousURL = previous.URL(selected)
	}
	if out.Page < out.Pages {
		next := f
		next.Page++
		out.NextURL = next.URL(selected)
	}
	if out.Total > 0 {
		out.First = (out.Page-1)*20 + 1
		out.Last = min(out.First+19, int(out.Total))
	}
	args = append(args, 20, (out.Page-1)*20)
	rows, err = tx.Query(shopperOrderCTE+`SELECT id,reference,status,created,total,order_version,completion_kind,picked,required,assignment_id,shopper_id,assignment_version,shopper_name,assignment_state,assignment_created,assignment_ended FROM tasks`+shopperTaskFilter+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var task ShopperTask
		var aid, shopper, version sql.NullInt64
		var name, state, created, ended sql.NullString
		o := &task.Order
		if err = rows.Scan(&o.ID, &o.Reference, &o.Status, &o.Created, &o.Total, &o.Version, &o.CompletionKind, &o.PickedCount, &o.RequiredCount, &aid, &shopper, &version, &name, &state, &created, &ended); err != nil {
			rows.Close()
			return nil, err
		}
		setOrderProgress(o)
		if aid.Valid {
			task.Assignment = &ShopperAssignment{ID: aid.Int64, OrderID: o.ID, ShopperID: shopper.Int64, Version: version.Int64, ShopperName: name.String, State: state.String, Created: created.String, Ended: ended.String}
			o.Assignment = task.Assignment
		}
		out.Tasks = append(out.Tasks, task)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Selected history follows its authorized order even outside the current list
	// filters/page. General history matches historical assignee IDs, never names.
	historyArgs := []any{allOrders, sid, selected, selected, selected, f.Query, f.Query, f.Status, f.Status, f.Status, f.Status, f.Status, f.ShopperID, f.ShopperID, f.ShopperID}
	rows, err = tx.Query(shopperOrderCTE+`SELECT e.order_id,t.reference,e.action,e.reason,e.details,e.created FROM order_events e JOIN shopper_event_links l ON l.event_id=e.id JOIN tasks t ON t.id=e.order_id
 WHERE (?<>0 AND t.id=?) OR (?=0
 AND (?='' OR instr(lower(t.reference||' '||e.details||' '||e.reason),lower(?))>0)
 AND (?='all' OR (?='open' AND t.status IN ('Placed','Picking')) OR (?='active' AND t.assignment_id IS NOT NULL) OR (?='unassigned' AND t.status IN ('Placed','Picking') AND t.assignment_id IS NULL) OR (?='closed' AND t.status IN ('Ready','Completed')))
 AND (?=0 OR l.from_shopper_id=? OR l.to_shopper_id=?)) ORDER BY e.id DESC LIMIT 40`, historyArgs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var event ShopperEvent
		if err = rows.Scan(&event.OrderID, &event.Reference, &event.Action, &event.Reason, &event.Details, &event.Created); err != nil {
			rows.Close()
			return nil, err
		}
		out.Events = append(out.Events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
