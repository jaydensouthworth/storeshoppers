package shop

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	ErrSubstitutionStale    = errors.New("This replacement needs a fresh shopper review: the order, item, assignment, or catalog price changed. Check saved request history before trying again.")
	ErrSubstitutionPending  = errors.New("This item already has a pending replacement. Withdraw it or wait for the customer's decision.")
	ErrSubstitutionLimit    = errors.New("This demo's replacement proposal history is full. Existing proposals are retained.")
	ErrSubstitutionCustomer = errors.New("This order has no active customer browser to approve a replacement. Continue the existing pick list or ask for review.")
)

const substitutionOrderLimit = 100
const substitutionGlobalLimit = 10000

type SubstitutionCommand struct {
	LineID, Version, PickVersion, AssignmentVersion, ReplacementID, Quantity int64
	Key, Note, Disposition, Review                                           string
}
type SubstitutionPreview struct {
	Command                                                                                                    SubstitutionCommand
	Source                                                                                                     WorkingOrderItem
	ReplacementID, ReplacementQuantity, ReplacementPicked, ReplacementPickVersion, ReplacementPrice, Available int64
	ReplacementName, ReplacementSKU, CatalogQuote, ShopperName, AllocationKey                                  string
	OldAmount, NewAmount, WorkingTotal, ProposedTotal                                                          int64
}

func (p SubstitutionPreview) AmountChange() string {
	delta := p.NewAmount - p.OldAmount
	if delta > 0 {
		return "+" + Money(delta)
	}
	if delta < 0 {
		return "−" + Money(-delta)
	}
	return Money(0)
}
func (p SubstitutionPreview) StockEffect() string {
	if p.Command.Disposition == "restock" {
		return fmt.Sprintf("Return all %d original units to available stock only because they are physically returned and resalable. Reserve %d replacement units when approved.", p.Source.Allocated, p.Command.Quantity)
	}
	return fmt.Sprintf("Write off all %d original units as missing or damaged; none return to available stock. Reserve %d replacement units when approved.", p.Source.Allocated, p.Command.Quantity)
}

type SubstitutionProposal struct {
	ID, OrderID, LineID, GrantID, AssignmentID, AssignmentVersion, CreatedAt, DecidedAt int64
	Epoch, Status, State, Created, Decided, Review, Problem                             string
	Preview                                                                             SubstitutionPreview
	CanDecide, CanReject, CanWithdraw                                                   bool
}
type SubstitutionWorkspace struct {
	OrderID, Version, AssignmentVersion      int64
	Reference, CSRF, Binding, ReadOnlyReason string
	Phone, CanPropose                        bool
	Lines                                    []WorkingOrderItem
	Products                                 []Product
	Proposals                                []SubstitutionProposal
}
type SubstitutionDecision struct {
	ProposalID                   int64
	Key, Binding, Review, Action string
}

func normalizeSubstitution(c SubstitutionCommand) (SubstitutionCommand, error) {
	c.Note = strings.TrimSpace(c.Note)
	if utf8.RuneCountInString(c.Note) < 3 || !validHandheldNote(c.Note) {
		return c, fmt.Errorf("Use a single-line reason with 3–240 characters and no control characters: %w", ErrInvalid)
	}
	if c.LineID < 1 || c.Version < 1 || c.PickVersion < 1 || c.AssignmentVersion < 1 || c.ReplacementID < 1 || c.Quantity < 1 || c.Quantity > 99 || !validMessageKey(c.Key) || (c.Disposition != "restock" && c.Disposition != "writeoff") {
		return c, ErrInvalid
	}
	return c, nil
}
func substitutionGrantTx(tx *sql.Tx, g handheldGrant, now int64) error {
	var practice bool
	var expiry int64
	if err := tx.QueryRow(`SELECT s.expires,EXISTS(SELECT 1 FROM employee_store_orders so WHERE so.order_id=o.id AND so.epoch=? AND so.practice=1) FROM orders o JOIN sessions s ON s.id=o.session_id WHERE o.id=?`, g.Epoch, g.OrderID).Scan(&expiry, &practice); err != nil {
		return err
	}
	if practice {
		return ErrEmployeePractice
	}
	if expiry <= now {
		return ErrSubstitutionCustomer
	}
	return nil
}

// Preview projects released expired holds without writing them. Approval uses the
// shared expiry/allocation helpers in its transaction. Other shoppers' harmless
// stock movement does not invalidate a price/quantity review.
func substitutionAvailableTx(tx *sql.Tx, pid, now int64) (int64, error) {
	var stock int64
	err := tx.QueryRow(`SELECT p.stock+COALESCE((SELECT SUM(c.reserved) FROM cart c JOIN baskets b ON b.id=c.basket_id JOIN sessions s ON s.id=b.owner_session_id WHERE c.product_id=p.id AND b.hold_until>0 AND (b.hold_until<=? OR s.expires<=?)),0) FROM products p WHERE p.id=?`, now, now, pid).Scan(&stock)
	return stock, err
}
func prepareSubstitutionTx(tx *sql.Tx, g handheldGrant, c SubstitutionCommand, now int64) (SubstitutionPreview, error) {
	p := SubstitutionPreview{Command: c, ShopperName: g.ShopperName}
	if err := substitutionGrantTx(tx, g, now); err != nil {
		return p, err
	}
	if c.Version != g.Version || c.AssignmentVersion != g.AssignmentVersion {
		return p, ErrSubstitutionStale
	}
	items, err := workingOrderItems(tx, g.OrderID)
	if err != nil {
		return p, err
	}
	found := false
	for _, i := range items {
		if i.LineID == c.LineID {
			p.Source = i
			found = true
		}
	}
	if !found || p.Source.SaleUnit != "each" || p.Source.PickVersion != c.PickVersion || p.Source.ProductID == c.ReplacementID || p.Source.Unavailable != 0 || p.Source.Cancelled != 0 {
		return p, ErrSubstitutionStale
	}
	var product Product
	var inactive bool
	fields := append(productFields(&product), &inactive)
	err = tx.QueryRow(`SELECT `+productSelect+`,(c.archived OR COALESCE(t.archived,0))`+productJoins+`WHERE p.id=?`, c.ReplacementID).Scan(fields...)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if product.Archived || inactive || product.SaleUnit != "each" {
		return p, ErrUnavailable
	}
	pricing, err := loadPricing(tx, now)
	if err != nil {
		return p, err
	}
	pricing.apply(&product)
	p.ReplacementID, p.ReplacementName, p.ReplacementSKU, p.ReplacementPrice = product.ID, product.Name, product.SKU, product.EffectivePrice()
	var oldUnit string
	var resolved int64
	err = tx.QueryRow(`SELECT quantity,picked_quantity,pick_version,price,name,sku,sale_unit,unavailable_quantity+cancelled_quantity FROM working_order_items WHERE order_id=? AND product_id=?`, g.OrderID, c.ReplacementID).Scan(&p.ReplacementQuantity, &p.ReplacementPicked, &p.ReplacementPickVersion, &p.ReplacementPrice, &p.ReplacementName, &p.ReplacementSKU, &oldUnit, &resolved)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	if err == nil && (oldUnit != "each" || resolved != 0) {
		return p, ErrUnavailable
	}
	if p.ReplacementQuantity+c.Quantity > 99 {
		return p, ErrInvalid
	}
	p.Available, err = substitutionAvailableTx(tx, c.ReplacementID, now)
	if err != nil {
		return p, err
	}
	if p.Available < c.Quantity {
		return p, ErrStock
	}
	if c.Disposition == "restock" {
		var occupied int64
		if err = tx.QueryRow(`SELECT stock+COALESCE((SELECT SUM(reserved) FROM cart WHERE product_id=p.id),0) FROM products p WHERE p.id=?`, p.Source.ProductID).Scan(&occupied); err != nil {
			return p, err
		}
		if occupied+p.Source.Allocated > 10000 {
			return p, ErrStockCapacity
		}
	}
	p.CatalogQuote, err = currentOrderCatalogQuote(tx, now)
	if err != nil {
		return p, err
	}
	var order Order
	if err = summarizeWorking(&order, items); err != nil {
		return p, err
	}
	p.OldAmount = p.Source.Subtotal
	p.NewAmount, err = lineAmount(c.Quantity, p.ReplacementPrice, 1, "each")
	if err != nil {
		return p, err
	}
	// Unrelated pick progress may continue while a customer is deciding. Bind
	// all economic allocations, but the source/destination pick state only.
	economic := append([]WorkingOrderItem(nil), items...)
	for i := range economic {
		economic[i].Picked = 0
		economic[i].PickVersion = 0
		economic[i].Measured = false
	}
	p.AllocationKey = handheldPayload(economic)
	p.WorkingTotal = order.WorkingTotal
	p.ProposedTotal = p.WorkingTotal - p.OldAmount + p.NewAmount
	p.Command.Review = ""
	p.Command.Review = substitutionReview(p, g)
	return p, nil
}
func substitutionReview(p SubstitutionPreview, g handheldGrant) string {
	p.Command.Review = ""
	p.Command.Version = 0
	p.Available = 0
	return handheldPayload([]any{"counted-substitution-v1", g.ID, g.AssignmentID, g.AssignmentVersion, g.Epoch, p})
}
func (s *Store) PreviewSubstitution(raw, csrf string, c SubstitutionCommand) (SubstitutionPreview, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return SubstitutionPreview{}, err
	}
	defer tx.Rollback()
	g, err := handheldGrantTx(tx, raw, csrf, true, s.now().Unix())
	if err != nil {
		return SubstitutionPreview{}, err
	}
	if err = substitutionGrantTx(tx, g, s.now().Unix()); err != nil {
		return SubstitutionPreview{}, err
	}
	c, err = normalizeSubstitution(c)
	if err != nil {
		return SubstitutionPreview{}, err
	}
	p, err := prepareSubstitutionTx(tx, g, c, s.now().Unix())
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

const substitutionColumns = `id,order_id,line_id,grant_id,assignment_id,assignment_version,epoch,status,created,decided,snapshot`

func scanSubstitution(row messageScanner) (SubstitutionProposal, error) {
	var p SubstitutionProposal
	var snapshot string
	err := row.Scan(&p.ID, &p.OrderID, &p.LineID, &p.GrantID, &p.AssignmentID, &p.AssignmentVersion, &p.Epoch, &p.Status, &p.CreatedAt, &p.DecidedAt, &snapshot)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(snapshot), &p.Preview); err != nil {
		return p, err
	}
	p.State = p.Status
	p.Created = handheldTime(p.CreatedAt)
	p.Decided = handheldTime(p.DecidedAt)
	p.Review = handheldPayload([]any{"substitution-decision-v1", p.ID, p.Epoch, p.Preview})
	return p, nil
}
func proposalGrantTx(tx *sql.Tx, p SubstitutionProposal, now int64) (handheldGrant, error) {
	var hash string
	err := tx.QueryRow(`SELECT token_hash FROM handheld_grants WHERE id=? AND assignment_id=? AND assignment_version=? AND epoch=?`, p.GrantID, p.AssignmentID, p.AssignmentVersion, p.Epoch).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return handheldGrant{}, ErrSubstitutionStale
	}
	if err != nil {
		return handheldGrant{}, err
	}
	g, err := handheldGrantHashTx(tx, hash, "", false, now)
	if errors.Is(err, ErrHandheldAccess) {
		// Continue shopping rotates a device grant, not this employee's task.
		// Follow only a live successor bound to the exact same employee capability,
		// assignment version and epoch. A different shift cannot inherit approval.
		var successor string
		e := tx.QueryRow(`SELECT next.token_hash FROM handheld_grants original JOIN employee_grant_links oldlink ON oldlink.grant_id=original.id JOIN employee_grant_links newlink ON newlink.employee_hash=oldlink.employee_hash JOIN handheld_grants next ON next.id=newlink.grant_id WHERE original.id=? AND original.expires>? AND next.assignment_id=original.assignment_id AND next.assignment_version=original.assignment_version AND next.epoch=original.epoch AND next.revoked=0 AND next.expires>? ORDER BY next.id DESC LIMIT 1`, p.GrantID, now, now).Scan(&successor)
		if errors.Is(e, sql.ErrNoRows) {
			return g, ErrSubstitutionStale
		}
		if e != nil {
			return g, e
		}
		g, err = handheldGrantHashTx(tx, successor, "", false, now)
		// The immutable proposal remains signed with its originating grant ID.
		g.ID = p.GrantID
	}
	if errors.Is(err, ErrHandheldAccess) {
		return g, ErrSubstitutionStale
	}
	return g, err
}
func currentProposalTx(tx *sql.Tx, p SubstitutionProposal, now int64) error {
	g, err := proposalGrantTx(tx, p, now)
	if err != nil {
		return err
	}
	command := p.Preview.Command
	command.Version = g.Version
	current, err := prepareSubstitutionTx(tx, g, command, now)
	if err != nil {
		return err
	}
	if current.Command.Review != p.Preview.Command.Review {
		return ErrSubstitutionStale
	}
	return nil
}
func (s *Store) SendSubstitution(raw, csrf string, c SubstitutionCommand) (SubstitutionProposal, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return SubstitutionProposal{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	g, err := handheldGrantTx(tx, raw, csrf, true, now)
	if err != nil {
		return SubstitutionProposal{}, err
	}
	if err = substitutionGrantTx(tx, g, now); err != nil {
		return SubstitutionProposal{}, err
	}
	c, err = normalizeSubstitution(c)
	if err != nil {
		return SubstitutionProposal{}, err
	}
	if len(c.Review) != 64 {
		return SubstitutionProposal{}, ErrSubstitutionStale
	}
	hash := handheldPayload([]any{"send-substitution", g.ID, c})
	var prior string
	err = tx.QueryRow(`SELECT command_hash FROM substitution_proposals WHERE grant_id=? AND command_key=?`, g.ID, c.Key).Scan(&prior)
	if err == nil {
		if prior != hash {
			return SubstitutionProposal{}, ErrConflict
		}
		p, e := scanSubstitution(tx.QueryRow(`SELECT `+substitutionColumns+` FROM substitution_proposals WHERE grant_id=? AND command_key=?`, g.ID, c.Key))
		return p, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SubstitutionProposal{}, err
	}
	preview, err := prepareSubstitutionTx(tx, g, c, now)
	if err != nil {
		return SubstitutionProposal{}, err
	}
	if preview.Command.Review != c.Review {
		return SubstitutionProposal{}, ErrSubstitutionStale
	}
	pending, err := scanSubstitution(tx.QueryRow(`SELECT `+substitutionColumns+` FROM substitution_proposals WHERE order_id=? AND line_id=? AND status='pending'`, g.OrderID, c.LineID))
	if err == nil {
		if stale := currentProposalTx(tx, pending, now); stale == nil {
			return SubstitutionProposal{}, ErrSubstitutionPending
		} else if !substitutionProblem(stale) {
			return SubstitutionProposal{}, stale
		}
		if _, err = tx.Exec(`UPDATE substitution_proposals SET status='stale',decided=? WHERE id=?`, now, pending.ID); err != nil {
			return SubstitutionProposal{}, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return SubstitutionProposal{}, err
	}
	var orderCount, globalCount int64
	if err = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM substitution_proposals WHERE order_id=?),(SELECT COUNT(*) FROM substitution_proposals)`, g.OrderID).Scan(&orderCount, &globalCount); err != nil {
		return SubstitutionProposal{}, err
	}
	if orderCount >= substitutionOrderLimit || globalCount >= substitutionGlobalLimit {
		return SubstitutionProposal{}, ErrSubstitutionLimit
	}
	snapshot, _ := json.Marshal(preview)
	result, err := tx.Exec(`INSERT INTO substitution_proposals(order_id,line_id,grant_id,assignment_id,assignment_version,epoch,command_key,command_hash,snapshot,created) VALUES(?,?,?,?,?,?,?,?,?,?)`, g.OrderID, c.LineID, g.ID, g.AssignmentID, g.AssignmentVersion, g.Epoch, c.Key, hash, string(snapshot), now)
	if err != nil {
		return SubstitutionProposal{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return SubstitutionProposal{}, err
	}
	p, err := scanSubstitution(tx.QueryRow(`SELECT `+substitutionColumns+` FROM substitution_proposals WHERE id=?`, id))
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// The customer approves precisely the stored proposal. Posting a price, product,
// quantity, manager flag or a chat message cannot change that approved command.
func (s *Store) DecideSubstitution(id int64, sid, csrf string, c SubstitutionDecision) error {
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	who, err := customerMessageIdentityTx(tx, id, sid, csrf, true, now)
	if err != nil {
		return err
	}
	if !validMessageKey(c.Key) || c.ProposalID < 1 || (c.Action != "approve" && c.Action != "reject") {
		return ErrInvalid
	}
	if c.Binding != who.conversation.ConversationKey {
		return ErrSubstitutionStale
	}
	p, err := scanSubstitution(tx.QueryRow(`SELECT `+substitutionColumns+` FROM substitution_proposals WHERE id=? AND order_id=?`, c.ProposalID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if c.Review != p.Review {
		return ErrSubstitutionStale
	}
	// No mutation or replay authority survives the proposal's task/grant lifetime.
	if _, err = proposalGrantTx(tx, p, now); err != nil {
		return err
	}
	hash := handheldPayload([]any{"decide-substitution", sid, c})
	var key, prior string
	if err = tx.QueryRow(`SELECT decision_key,decision_hash FROM substitution_proposals WHERE id=?`, p.ID).Scan(&key, &prior); err != nil {
		return err
	}
	if key != "" {
		if key == c.Key && prior == hash {
			return nil
		}
		return ErrConflict
	}
	var used bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM substitution_proposals WHERE order_id=? AND decision_key=?)`, id, c.Key).Scan(&used); err != nil {
		return err
	}
	if used {
		return ErrConflict
	}
	if p.Status != "pending" {
		return ErrConflict
	}
	state := "rejected"
	if c.Action == "approve" {
		if err = currentProposalTx(tx, p, now); err != nil {
			return err
		}
		if err = expireHolds(tx, now); err != nil {
			return err
		}
		v := p.Preview
		added, e := setWorkingAllocation(tx, id, v.ReplacementID, v.ReplacementQuantity+v.Command.Quantity, v.ReplacementQuantity+v.Command.Quantity, "", v.CatalogQuote, now)
		if e != nil {
			return e
		}
		removed, e := setWorkingQuantity(tx, id, v.Source.ProductID, 0, v.Command.Disposition, v.CatalogQuote, now)
		if e != nil {
			return e
		}
		items, e := workingOrderItems(tx, id)
		if e != nil {
			return e
		}
		var order Order
		if e = summarizeWorking(&order, items); e != nil {
			return e
		}
		if order.WorkingTotal != v.ProposedTotal {
			return ErrSubstitutionStale
		}
		if _, err = tx.Exec(`UPDATE orders SET order_version=order_version+1 WHERE id=?`, id); err != nil {
			return err
		}
		details := fmt.Sprintf("Customer approved proposal %d from %s: %s; %s; working estimate %s → %s", p.ID, v.ShopperName, removed, added, Money(v.WorkingTotal), Money(v.ProposedTotal))
		if _, err = tx.Exec(`INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details) VALUES(?,?,?,?,?,?)`, id, fmt.Sprintf("customer-substitution:%d", p.ID), hash, "substitute", v.Command.Note, details); err != nil {
			return err
		}
		state = "approved"
	}
	if _, err = tx.Exec(`UPDATE substitution_proposals SET status=?,decided=?,decision_key=?,decision_hash=? WHERE id=?`, state, now, c.Key, hash, p.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) WithdrawSubstitution(raw, csrf string, id int64, review string) error {
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	g, err := handheldGrantTx(tx, raw, csrf, true, now)
	if err != nil {
		return err
	}
	p, err := scanSubstitution(tx.QueryRow(`SELECT `+substitutionColumns+` FROM substitution_proposals WHERE id=? AND order_id=?`, id, g.OrderID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !sameSubstitutionAuthorityTx(tx, p, g) {
		return ErrNotFound
	}
	if review != p.Review {
		return ErrSubstitutionStale
	}
	if p.Status == "withdrawn" {
		return nil
	}
	if p.Status != "pending" {
		return ErrConflict
	}
	if _, err = tx.Exec(`UPDATE substitution_proposals SET status='withdrawn',decided=? WHERE id=?`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}
func substitutionProblem(err error) bool {
	return errors.Is(err, ErrSubstitutionStale) || errors.Is(err, ErrSubstitutionPending) || errors.Is(err, ErrSubstitutionLimit) || errors.Is(err, ErrSubstitutionCustomer) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) || errors.Is(err, ErrStock) || errors.Is(err, ErrStockCapacity) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrEmployeePractice)
}
func (s *Store) Substitutions(id int64, sid, raw string, phone bool) (SubstitutionWorkspace, error) {
	out := SubstitutionWorkspace{Phone: phone}
	tx, err := s.beginWrite()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	var grant handheldGrant
	if phone {
		grant, err = handheldGrantTx(tx, raw, "", false, now)
		if err != nil {
			return out, err
		}
		id = grant.OrderID
		out.CSRF = grant.CSRF
		out.Version = grant.Version
		out.AssignmentVersion = grant.AssignmentVersion
		if err = substitutionGrantTx(tx, grant, now); err != nil {
			if errors.Is(err, ErrEmployeePractice) {
				return out, err
			}
			out.ReadOnlyReason = err.Error()
		} else {
			out.CanPropose = true
		}
		if err = tx.QueryRow(`SELECT reference FROM orders WHERE id=?`, id).Scan(&out.Reference); err != nil {
			return out, err
		}
	} else {
		who, e := customerMessageIdentityTx(tx, id, sid, "", false, now)
		if e != nil {
			return out, e
		}
		out.CSRF = who.conversation.CSRF
		out.Binding = who.conversation.ConversationKey
		out.Reference = who.conversation.Reference
	}
	out.OrderID = id
	rows, err := tx.Query(`SELECT `+substitutionColumns+` FROM substitution_proposals WHERE order_id=? ORDER BY id DESC LIMIT 100`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		p, e := scanSubstitution(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Proposals = append(out.Proposals, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for i := range out.Proposals {
		p := &out.Proposals[i]
		if p.Status == "pending" {
			e := currentProposalTx(tx, *p, now)
			if e != nil {
				if !substitutionProblem(e) {
					return out, e
				}
				p.State = "stale"
				p.Problem = e.Error()
			} else {
				p.CanDecide = !phone
			}
			if _, e := proposalGrantTx(tx, *p, now); e == nil {
				p.CanReject = !phone
			}
			p.CanWithdraw = phone && sameSubstitutionAuthorityTx(tx, *p, grant)
		}
	}
	if phone && out.CanPropose {
		out.Lines, err = workingOrderItems(tx, id)
		if err != nil {
			return out, err
		}
		pricing, e := loadPricing(tx, now)
		if e != nil {
			return out, e
		}
		rows, e = tx.Query(`SELECT ` + productSelect + productJoins + `WHERE p.archived=0 AND p.sale_unit='each' AND c.archived=0 AND COALESCE(t.archived,0)=0 ORDER BY p.name LIMIT 500`)
		if e != nil {
			return out, e
		}
		for rows.Next() {
			p, e := scanProduct(rows)
			if e != nil {
				rows.Close()
				return out, e
			}
			pricing.apply(&p)
			out.Products = append(out.Products, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}

func sameSubstitutionAuthorityTx(tx *sql.Tx, p SubstitutionProposal, g handheldGrant) bool {
	if p.AssignmentID != g.AssignmentID || p.AssignmentVersion != g.AssignmentVersion || p.Epoch != g.Epoch {
		return false
	}
	if p.GrantID == g.ID {
		return true
	}
	var same bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM employee_grant_links old JOIN employee_grant_links current ON current.employee_hash=old.employee_hash WHERE old.grant_id=? AND current.grant_id=?)`, p.GrantID, g.ID).Scan(&same)
	return err == nil && same
}
