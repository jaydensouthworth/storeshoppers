package shop

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type OrderFilters struct {
	Query, State, Held, Assigned, Sort string
	Page                               int
}
type OrderQueue struct {
	Orders                  []Order
	Total, Open, HeldCount  int64
	Page, Pages, Start, End int
	Filters                 OrderFilters
	PreviousURL, NextURL    string
}

func parseOrderFilters(v url.Values) OrderFilters {
	f := OrderFilters{State: "all", Held: "all", Assigned: "all", Sort: "newest", Page: 1}
	f.Query, _ = boundedManagerDraft(strings.TrimSpace(v.Get("q")), 100)
	switch v.Get("state") {
	case "Placed", "Picking", "Ready", "Completed":
		f.State = v.Get("state")
	}
	switch v.Get("held") {
	case "held", "clear":
		f.Held = v.Get("held")
	}
	switch v.Get("assigned") {
	case "assigned", "unassigned":
		f.Assigned = v.Get("assigned")
	}
	switch v.Get("sort") {
	case "oldest", "reference":
		f.Sort = v.Get("sort")
	}
	if p, err := strconv.Atoi(v.Get("page")); err == nil && p > 0 && p <= 1000000 {
		f.Page = p
	}
	return f
}
func orderFilters(r *http.Request) OrderFilters {
	if r.Method == http.MethodPost {
		return parseOrderFilters(r.PostForm)
	}
	return parseOrderFilters(r.URL.Query())
}
func (f OrderFilters) Values() url.Values {
	v := url.Values{}
	if f.Query != "" {
		v.Set("q", f.Query)
	}
	if f.State != "" && f.State != "all" {
		v.Set("state", f.State)
	}
	if f.Held != "" && f.Held != "all" {
		v.Set("held", f.Held)
	}
	if f.Assigned != "" && f.Assigned != "all" {
		v.Set("assigned", f.Assigned)
	}
	if f.Sort != "" && f.Sort != "newest" {
		v.Set("sort", f.Sort)
	}
	if f.Page > 1 {
		v.Set("page", strconv.Itoa(f.Page))
	}
	return v
}
func (f OrderFilters) Fields() []catalogFilterField {
	v := f.Values()
	fields := []catalogFilterField{}
	for _, k := range []string{"q", "state", "held", "assigned", "sort", "page"} {
		fields = append(fields, catalogFilterField{k, v.Get(k)})
	}
	return fields
}
func (f OrderFilters) QueryString() string {
	if q := f.Values().Encode(); q != "" {
		return "?" + q
	}
	return ""
}
func (f OrderFilters) URL(id int64) string {
	if id > 0 {
		return fmt.Sprintf("/manager/orders/%d", id) + f.QueryString()
	}
	return "/manager/orders" + f.QueryString()
}
func (v View) OrderURL(id int64) string     { return v.OrderFilters.URL(id) }
func (v View) QueueURL() string             { return v.OrderFilters.URL(0) }
func (f OrderFilters) AttentionURL() string { f.Held = "held"; f.Page = 1; return f.URL(0) }

// OrderQueue holds a single snapshot for scope-wide counts, filtered count,
// page and row summaries. SQL identifiers/order expressions are fixed choices.
func (s *Store) OrderQueue(sid string, allOrders bool, f OrderFilters) (OrderQueue, error) {
	return s.readOrderQueue(sid, allOrders, f, true)
}
func (s *Store) readOrderQueue(sid string, allOrders bool, f OrderFilters, private bool) (OrderQueue, error) {
	f = parseOrderFilters(f.Values())
	out := OrderQueue{Filters: f, Page: f.Page}
	tx, err := s.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	scope := ` FROM orders o WHERE (? OR o.session_id=?)`
	held := `(o.attention_reason<>'' AND o.status IN ('Placed','Picking'))`
	err = tx.QueryRow(`SELECT COUNT(CASE WHEN o.status<>'Completed' THEN 1 END),COUNT(CASE WHEN `+held+` THEN 1 END)`+scope, allOrders, sid).Scan(&out.Open, &out.HeldCount)
	if err != nil {
		return out, err
	}
	filter := scope + ` AND (?='all' OR o.status=?) AND (?='all' OR (?='held' AND ` + held + `) OR (?='clear' AND NOT ` + held + `))
 AND (?='all' OR (?='assigned' AND EXISTS(SELECT 1 FROM shopper_assignments a WHERE a.order_id=o.id AND a.state='active')) OR (?='unassigned' AND NOT EXISTS(SELECT 1 FROM shopper_assignments a WHERE a.order_id=o.id AND a.state='active')))
 AND (?='' OR instr(lower(o.reference),lower(?))>0 OR EXISTS(SELECT 1 FROM order_items i WHERE i.order_id=o.id AND instr(lower(i.name||' '||i.sku),lower(?))>0) OR EXISTS(SELECT 1 FROM working_order_items i WHERE i.order_id=o.id AND i.quantity>0 AND instr(lower(i.name||' '||i.sku),lower(?))>0))`
	args := []any{allOrders, sid, f.State, f.State, f.Held, f.Held, f.Held, f.Assigned, f.Assigned, f.Assigned, f.Query, f.Query, f.Query, f.Query}
	if err = tx.QueryRow(`SELECT COUNT(*)`+filter, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	out.Pages = int((out.Total + 19) / 20)
	if out.Pages < 1 {
		out.Pages = 1
	}
	if out.Page > out.Pages {
		out.Page = out.Pages
	}
	f.Page = out.Page
	out.Filters = f
	sorting := `o.created DESC,o.id DESC`
	if f.Sort == "oldest" {
		sorting = `o.created,o.id`
	} else if f.Sort == "reference" {
		sorting = `o.reference COLLATE NOCASE,o.id`
	}
	args = append(args, 20, (out.Page-1)*20)
	args = append([]any{private, private}, args...)
	rows, err := tx.Query(`SELECT o.id,o.reference,o.status,o.created,o.total,o.order_version,o.final_total,o.completion_kind,CASE WHEN ? THEN o.attention_reason ELSE '' END,CASE WHEN ? THEN o.attention_since ELSE 0 END,`+held+filter+` ORDER BY `+sorting+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var o Order
		var final sql.NullInt64
		if err = rows.Scan(&o.ID, &o.Reference, &o.Status, &o.Created, &o.Total, &o.Version, &final, &o.CompletionKind, &o.AttentionReason, &o.AttentionSince, &o.Held); err != nil {
			rows.Close()
			return out, err
		}
		o.Finalized, o.FinalTotal = final.Valid, final.Int64
		out.Orders = append(out.Orders, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for i := range out.Orders {
		o := &out.Orders[i]
		items, e := workingOrderItems(tx, o.ID)
		if e != nil {
			return out, e
		}
		if e = summarizeWorking(o, items); e != nil {
			return out, e
		}
		if o.Assignment, e = activeAssignment(tx, o.ID); e != nil {
			return out, e
		}
	}
	if out.Total > 0 {
		out.Start = (out.Page-1)*20 + 1
		out.End = out.Start + len(out.Orders) - 1
	}
	if out.Page > 1 {
		p := f
		p.Page--
		out.PreviousURL = p.URL(0)
	}
	if out.Page < out.Pages {
		p := f
		p.Page++
		out.NextURL = p.URL(0)
	}
	return out, tx.Commit()
}

func (v View) AssignmentURL() string {
	q := url.Values{"order": {strconv.FormatInt(v.Order.ID, 10)}}
	if context := v.OrderFilters.Values().Encode(); context != "" {
		q.Set("queue", context)
	}
	return "/manager/shoppers?" + q.Encode()
}
