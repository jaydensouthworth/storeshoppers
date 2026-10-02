package shop

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrHandheldAccess   = errors.New("This phone is disconnected, its task ended, or the connection expired. Ask the desktop manager for a new connection code. Your recorded picks are kept.")
	ErrHandheldPairing  = errors.New("That connection code is unavailable or expired. Check the code, or ask the desktop manager to create a new one.")
	ErrHandheldAttempts = errors.New("Too many connection attempts. Wait five minutes before trying again.")
	ErrHandheldCSRF     = errors.New("This phone form expired. Refresh the phone workspace and review the task before trying again.")
	ErrHandheldIssued   = errors.New("This connection code was already issued and cannot be displayed again. Use the code on the original screen, or deliberately create a new code.")
	ErrHandheldWeight   = errors.New("This label identifies a weighed product, not its weight. Enter actual grams from the scale, then explicitly review and confirm the weight.")
)

const handheldInvitationTTL = 10 * time.Minute
const handheldGrantTTL = 8 * time.Hour
const handheldPairSessionTTL = 30 * time.Minute

// HandheldSession is deliberately not a customer Session. Token is returned
// only at creation and sent in an HttpOnly cookie, never into page data or URLs.
type HandheldSession struct {
	Token, CSRF string
	Expires     int64
}
type HandheldLine struct {
	WorkingOrderItem
	Department, Icon, ImageURL string
	Archived                   bool
}
type HandheldTask struct {
	ID, Version, Expires, PickedLines, RequiredLines                      int64
	Reference, Status, Instructions, LastRecorded, LastSync, ExpiresLabel string
	Held                                                                  bool
	Assignment                                                            ShopperAssignment
	Lines                                                                 []HandheldLine
}
type HandheldRecognition struct {
	State, Code, Source, Format, Message, ProductName string
	CanPick, CanMeasure                               bool
}
type HandheldScan struct {
	LineID, AssignmentVersion, Version, PickVersion int64
	Code, Source, Format                            string
}
type HandheldPick struct {
	HandheldScan
	Picked int64
	Key    string
}
type HandheldPickResult struct {
	LineID, Picked, Version, PickVersion int64
	Replayed                             bool
}
type HandheldPairCommand struct {
	Version, AssignmentVersion int64
	Key                        string
}
type HandheldPairing struct {
	OrderID, Version, AssignmentVersion, Expires int64
	Reference, PairingCode, LastRecorded         string
	Assignment                                   *ShopperAssignment
	ActiveGrant, PendingInvitation               bool
}
type handheldGrant struct {
	ID, OrderID, AssignmentID, AssignmentVersion, ShopperID, Expires, Version, LastRecorded int64
	CSRF, Owner, Scope, Epoch, ShopperName, Status                                          string
}

func handheldHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func handheldPayload(value any) string {
	data, _ := json.Marshal(value)
	return handheldHash(string(data))
}
func validHandheldToken(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func handheldTime(value int64) string {
	if value == 0 {
		return ""
	}
	return time.Unix(value, 0).UTC().Format("2006-01-02 15:04 UTC")
}

// All scoped reads obtain the same writer lock as mutation, so assignment,
// owner expiry, epoch and projection cannot come from different revisions.
func (s *Store) HandheldTask(raw string) (*HandheldTask, HandheldSession, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return nil, HandheldSession{}, err
	}
	defer tx.Rollback()
	g, err := handheldGrantTx(tx, raw, "", false, s.now().Unix())
	if err != nil {
		return nil, HandheldSession{}, err
	}
	task, err := handheldTaskTx(tx, g, s.now().Unix())
	if err != nil {
		return nil, HandheldSession{}, err
	}
	return task, HandheldSession{CSRF: g.CSRF, Expires: g.Expires}, tx.Commit()
}
func handheldGrantTx(tx *sql.Tx, raw, csrf string, requireCSRF bool, now int64) (handheldGrant, error) {
	var g handheldGrant
	if !validHandheldToken(raw) {
		return g, ErrHandheldAccess
	}
	err := tx.QueryRow(`SELECT g.id,a.order_id,g.assignment_id,g.assignment_version,g.shopper_id,g.expires,g.csrf,g.owner_session_id,g.scope,g.epoch,a.shopper_name,o.status,o.order_version,g.last_recorded
 FROM handheld_grants g JOIN handheld_state h ON h.id=1 AND h.epoch=g.epoch
 JOIN sessions owner ON owner.id=g.owner_session_id AND owner.expires>?
 JOIN shopper_assignments a ON a.id=g.assignment_id AND a.version=g.assignment_version AND a.shopper_id=g.shopper_id AND a.state='active'
 JOIN orders o ON o.id=a.order_id AND o.session_id=g.owner_session_id AND o.status IN ('Placed','Picking')
 JOIN shoppers s ON s.id=g.shopper_id
 LEFT JOIN shopper_roster_profiles p ON p.shopper_id=s.id AND p.scope=g.scope
 WHERE g.token_hash=? AND g.revoked=0 AND g.expires>? AND COALESCE(p.archived,0)=0 AND (s.baseline=1 OR s.scope=g.scope)`, now, handheldHash(raw), now).Scan(&g.ID, &g.OrderID, &g.AssignmentID, &g.AssignmentVersion, &g.ShopperID, &g.Expires, &g.CSRF, &g.Owner, &g.Scope, &g.Epoch, &g.ShopperName, &g.Status, &g.Version, &g.LastRecorded)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrHandheldAccess
	}
	if err != nil {
		return g, err
	}
	if requireCSRF && subtle.ConstantTimeCompare([]byte(csrf), []byte(g.CSRF)) != 1 {
		return g, ErrHandheldCSRF
	}
	return g, nil
}
func handheldTaskTx(tx *sql.Tx, g handheldGrant, now int64) (*HandheldTask, error) {
	out := &HandheldTask{ID: g.OrderID, Version: g.Version, Expires: g.Expires, Status: g.Status, LastRecorded: handheldTime(g.LastRecorded), LastSync: handheldTime(now), ExpiresLabel: handheldTime(g.Expires)}
	err := tx.QueryRow(`SELECT reference,instructions,attention_reason<>'' FROM orders WHERE id=?`, g.OrderID).Scan(&out.Reference, &out.Instructions, &out.Held)
	if err != nil {
		return nil, err
	}
	assignment, err := activeAssignment(tx, g.OrderID)
	if err != nil {
		return nil, err
	}
	if assignment == nil {
		return nil, ErrHandheldAccess
	}
	out.Assignment = *assignment
	rows, err := tx.Query(`SELECT w.id,w.product_id,w.name,w.price,w.quantity,w.picked_quantity,w.pick_version,w.sku,w.sale_unit,w.price_basis,w.quantity_step,w.unavailable_quantity,w.cancelled_quantity,w.allocated_quantity,w.measurement_confirmed,c.name,p.icon,COALESCE(p.image_hash,''),p.archived
 FROM working_order_items w JOIN products p ON p.id=w.product_id JOIN categories c ON c.id=p.category_id WHERE w.order_id=? AND w.quantity>0 ORDER BY c.name,w.id`, g.OrderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var line HandheldLine
		var imageHash string
		if err = rows.Scan(&line.LineID, &line.ProductID, &line.Name, &line.Price, &line.Quantity, &line.Picked, &line.PickVersion, &line.SKU, &line.SaleUnit, &line.PriceBasis, &line.QuantityStep, &line.Unavailable, &line.Cancelled, &line.Allocated, &line.Measured, &line.Department, &line.Icon, &imageHash, &line.Archived); err != nil {
			return nil, err
		}
		if imageHash != "" {
			line.ImageURL = "/media/products/" + imageHash + "/thumb.jpg"
		}
		line.Subtotal, err = lineAmount(line.Allocated-line.Unavailable-line.Cancelled, line.Price, line.PriceBasis, line.SaleUnit)
		if err != nil {
			return nil, err
		}
		out.RequiredLines++
		if (line.SaleUnit == "each" && line.Picked == line.Quantity) || (line.SaleUnit == "g" && line.Measured && line.Unavailable == 0 && line.Cancelled == 0) {
			out.PickedLines++
		}
		out.Lines = append(out.Lines, line)
	}
	return out, rows.Err()
}

func (s *Store) HandheldPairSession(raw string) (HandheldSession, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return HandheldSession{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	var session HandheldSession
	if validHandheldToken(raw) {
		err = tx.QueryRow(`SELECT p.csrf,p.expires FROM handheld_pair_sessions p JOIN handheld_state h ON h.epoch=p.epoch WHERE p.token_hash=? AND p.expires>?`, handheldHash(raw), now).Scan(&session.CSRF, &session.Expires)
		if err == nil {
			return session, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return session, err
		}
	}
	if _, err = tx.Exec(`DELETE FROM handheld_pair_sessions WHERE expires<=?`, now); err != nil {
		return session, err
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM handheld_pair_sessions`).Scan(&count); err != nil {
		return session, err
	}
	if count >= 2048 {
		return session, ErrHandheldAttempts
	}
	session = HandheldSession{Token: token(), CSRF: token(), Expires: now + int64(handheldPairSessionTTL/time.Second)}
	_, err = tx.Exec(`INSERT INTO handheld_pair_sessions(token_hash,csrf,epoch,expires) SELECT ?,?,epoch,? FROM handheld_state WHERE id=1`, handheldHash(session.Token), session.CSRF, session.Expires)
	if err != nil {
		return session, err
	}
	return session, tx.Commit()
}
func normalizedInvitation(raw string) string {
	if len(raw) > 80 {
		return ""
	}
	raw = strings.ToUpper(strings.TrimSpace(raw))
	raw = strings.ReplaceAll(raw, "-", "")
	raw = strings.ReplaceAll(raw, " ", "")
	if len(raw) != 26 {
		return ""
	}
	for _, c := range raw {
		if !(c >= 'A' && c <= 'Z') && !(c >= '2' && c <= '7') {
			return ""
		}
	}
	return raw
}
func newHandheldInvitation() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}
func groupedInvitation(raw string) string {
	var groups []string
	for len(raw) > 4 {
		groups = append(groups, raw[:4])
		raw = raw[4:]
	}
	groups = append(groups, raw)
	return strings.Join(groups, "-")
}

// Redemption consumes one invite and rotates a fresh worker token/CSRF in one
// transaction. A lost cookie cannot be recovered by replaying a consumed code;
// the manager can safely replace the connection without losing recorded work.
func (s *Store) RedeemHandheld(rawPair, csrf, code string) (HandheldSession, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return HandheldSession{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	var expected, epoch string
	var attempts, window int64
	if !validHandheldToken(rawPair) {
		return HandheldSession{}, ErrHandheldCSRF
	}
	err = tx.QueryRow(`SELECT p.csrf,p.epoch,p.attempts,p.attempt_window FROM handheld_pair_sessions p JOIN handheld_state h ON h.epoch=p.epoch WHERE p.token_hash=? AND p.expires>?`, handheldHash(rawPair), now).Scan(&expected, &epoch, &attempts, &window)
	if errors.Is(err, sql.ErrNoRows) {
		return HandheldSession{}, ErrHandheldCSRF
	}
	if err != nil {
		return HandheldSession{}, err
	}
	if subtle.ConstantTimeCompare([]byte(csrf), []byte(expected)) != 1 {
		return HandheldSession{}, ErrHandheldCSRF
	}
	if window <= now-300 {
		window = now
		attempts = 0
	}
	if attempts >= 8 {
		return HandheldSession{}, ErrHandheldAttempts
	}
	var globalAttempts, globalWindow int64
	if err = tx.QueryRow(`SELECT attempts,attempt_window FROM handheld_state WHERE id=1`).Scan(&globalAttempts, &globalWindow); err != nil {
		return HandheldSession{}, err
	}
	if globalWindow <= now-300 {
		globalWindow = now
		globalAttempts = 0
	}
	if globalAttempts >= 120 {
		return HandheldSession{}, ErrHandheldAttempts
	}
	if _, err = tx.Exec(`UPDATE handheld_pair_sessions SET attempts=?,attempt_window=? WHERE token_hash=?`, attempts+1, window, handheldHash(rawPair)); err != nil {
		return HandheldSession{}, err
	}
	if _, err = tx.Exec(`UPDATE handheld_state SET attempts=?,attempt_window=? WHERE id=1`, globalAttempts+1, globalWindow); err != nil {
		return HandheldSession{}, err
	}
	// All unsuccessful code attempts have the same response, and commit only the
	// bounded attempt counters. No catalog/order/assignment state is touched.
	reject := func() (HandheldSession, error) {
		if e := tx.Commit(); e != nil {
			return HandheldSession{}, e
		}
		return HandheldSession{}, ErrHandheldPairing
	}
	code = normalizedInvitation(code)
	if code == "" {
		return reject()
	}
	var inviteID, assignmentID, assignmentVersion, shopperID, ownerExpiry int64
	var owner, scope string
	err = tx.QueryRow(`SELECT i.id,i.assignment_id,i.assignment_version,i.shopper_id,i.owner_session_id,i.scope,owner.expires
 FROM handheld_invitations i JOIN sessions owner ON owner.id=i.owner_session_id AND owner.expires>?
 JOIN shopper_assignments a ON a.id=i.assignment_id AND a.version=i.assignment_version AND a.shopper_id=i.shopper_id AND a.state='active'
 JOIN orders o ON o.id=a.order_id AND o.session_id=i.owner_session_id AND o.status IN ('Placed','Picking')
 JOIN shoppers s ON s.id=i.shopper_id LEFT JOIN shopper_roster_profiles p ON p.shopper_id=s.id AND p.scope=i.scope
 WHERE i.secret_hash=? AND i.epoch=? AND i.expires>? AND i.revoked=0 AND i.consumed=0 AND COALESCE(p.archived,0)=0 AND (s.baseline=1 OR s.scope=i.scope)`, now, handheldHash(code), epoch, now).Scan(&inviteID, &assignmentID, &assignmentVersion, &shopperID, &owner, &scope, &ownerExpiry)
	if errors.Is(err, sql.ErrNoRows) {
		return reject()
	}
	if err != nil {
		return HandheldSession{}, err
	}
	grant := HandheldSession{Token: token(), CSRF: token(), Expires: min(ownerExpiry, now+int64(handheldGrantTTL/time.Second))}
	if _, err = tx.Exec(`UPDATE handheld_invitations SET consumed=? WHERE id=?`, now, inviteID); err != nil {
		return HandheldSession{}, err
	}
	if _, err = tx.Exec(`UPDATE handheld_grants SET revoked=? WHERE assignment_id=? AND revoked=0`, now, assignmentID); err != nil {
		return HandheldSession{}, err
	}
	if _, err = tx.Exec(`INSERT INTO handheld_grants(token_hash,csrf,epoch,owner_session_id,scope,assignment_id,assignment_version,shopper_id,invitation_id,created,expires) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, handheldHash(grant.Token), grant.CSRF, epoch, owner, scope, assignmentID, assignmentVersion, shopperID, inviteID, now, grant.Expires); err != nil {
		return HandheldSession{}, err
	}
	if _, err = tx.Exec(`DELETE FROM handheld_pair_sessions WHERE token_hash=?`, handheldHash(rawPair)); err != nil {
		return HandheldSession{}, err
	}
	return grant, tx.Commit()
}

func (s *Store) DisconnectHandheld(raw, csrf string) error {
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	g, err := handheldGrantTx(tx, raw, csrf, true, s.now().Unix())
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE handheld_grants SET revoked=? WHERE id=?`, s.now().Unix(), g.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func managerHandheldPairingTx(tx *sql.Tx, id int64, sid string, all bool, now int64) (*HandheldPairing, error) {
	out := &HandheldPairing{OrderID: id}
	var status string
	err := tx.QueryRow(`SELECT reference,order_version,status FROM orders WHERE id=? AND (? OR session_id=?) AND EXISTS(SELECT 1 FROM sessions WHERE id=? AND expires>?)`, id, all, sid, sid, now).Scan(&out.Reference, &out.Version, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out.Assignment, err = activeAssignment(tx, id)
	if err != nil {
		return nil, err
	}
	if out.Assignment == nil {
		return out, nil
	}
	out.AssignmentVersion = out.Assignment.Version
	var last int64
	err = tx.QueryRow(`SELECT COALESCE(MAX(g.expires),0),COALESCE(MAX(g.last_recorded),0) FROM handheld_grants g JOIN handheld_state h ON h.epoch=g.epoch JOIN sessions owner ON owner.id=g.owner_session_id AND owner.expires>? WHERE g.assignment_id=? AND g.assignment_version=? AND g.revoked=0 AND g.expires>?`, now, out.Assignment.ID, out.AssignmentVersion, now).Scan(&out.Expires, &last)
	if err != nil {
		return nil, err
	}
	out.ActiveGrant = out.Expires > now && (status == "Placed" || status == "Picking")
	out.LastRecorded = handheldTime(last)
	var inviteExpiry int64
	err = tx.QueryRow(`SELECT COALESCE(MAX(i.expires),0) FROM handheld_invitations i JOIN handheld_state h ON h.epoch=i.epoch JOIN sessions owner ON owner.id=i.owner_session_id AND owner.expires>? WHERE i.assignment_id=? AND i.assignment_version=? AND i.revoked=0 AND i.consumed=0 AND i.expires>?`, now, out.Assignment.ID, out.AssignmentVersion, now).Scan(&inviteExpiry)
	if err != nil {
		return nil, err
	}
	out.PendingInvitation = inviteExpiry > now && (status == "Placed" || status == "Picking")
	if out.PendingInvitation {
		out.Expires = inviteExpiry
	}
	return out, nil
}
func (s *Store) HandheldPairing(id int64, sid string, all bool) (*HandheldPairing, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out, err := managerHandheldPairingTx(tx, id, sid, all, s.now().Unix())
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
func (s *Store) ChangeHandheldPairing(id int64, sid string, all bool, c HandheldPairCommand, action string) (*HandheldPairing, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	out, err := managerHandheldPairingTx(tx, id, sid, all, now)
	if err != nil {
		return nil, err
	}
	if out.Assignment == nil {
		return nil, ErrConflict
	}
	if len(c.Key) < 16 || len(c.Key) > 100 || c.Version < 1 || c.AssignmentVersion < 1 || (action != "issue" && action != "revoke") {
		return nil, ErrInvalid
	}
	hash := handheldPayload([]any{"handheld-pair", id, c, action})
	key := "handheld-pair:" + c.Key
	var prior string
	err = tx.QueryRow(`SELECT command_hash FROM handheld_pair_commands WHERE assignment_id=? AND command_key=?`, out.Assignment.ID, key).Scan(&prior)
	if err == nil {
		if prior != hash {
			return nil, ErrConflict
		}
		if action == "issue" {
			return nil, ErrHandheldIssued
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if out.Version != c.Version || out.Assignment.Version != c.AssignmentVersion {
		return nil, ErrConflict
	}
	var owner, epoch, status string
	var ownerExpiry int64
	err = tx.QueryRow(`SELECT o.session_id,owner.expires,h.epoch,o.status FROM orders o JOIN sessions owner ON owner.id=o.session_id JOIN handheld_state h ON h.id=1 WHERE o.id=?`, id).Scan(&owner, &ownerExpiry, &epoch, &status)
	if err != nil {
		return nil, err
	}
	if status != "Placed" && status != "Picking" {
		return nil, ErrTerminal
	}
	if ownerExpiry <= now {
		return nil, ErrHandheldAccess
	}
	scope := ""
	if !all {
		scope = sid
	}
	var archived bool
	if err = tx.QueryRow(`SELECT COALESCE(p.archived,0) FROM shoppers s LEFT JOIN shopper_roster_profiles p ON p.shopper_id=s.id AND p.scope=? WHERE s.id=? AND (s.baseline=1 OR s.scope=?)`, scope, out.Assignment.ShopperID, scope).Scan(&archived); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if archived {
		return nil, ErrShopperArchived
	}
	if _, err = tx.Exec(`UPDATE handheld_invitations SET revoked=? WHERE assignment_id=? AND revoked=0`, now, out.Assignment.ID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE handheld_grants SET revoked=? WHERE assignment_id=? AND revoked=0`, now, out.Assignment.ID); err != nil {
		return nil, err
	}
	out.ActiveGrant = false
	out.PendingInvitation = false
	out.Expires = 0
	if action == "issue" {
		code := newHandheldInvitation()
		out.PairingCode = groupedInvitation(code)
		out.Expires = min(ownerExpiry, now+int64(handheldInvitationTTL/time.Second))
		out.PendingInvitation = true
		_, err = tx.Exec(`INSERT INTO handheld_invitations(secret_hash,epoch,owner_session_id,scope,assignment_id,assignment_version,shopper_id,created,expires,command_key,command_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, handheldHash(code), epoch, owner, scope, out.Assignment.ID, out.Assignment.Version, out.Assignment.ShopperID, now, out.Expires, key, hash)
		if err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(`INSERT INTO handheld_pair_commands(assignment_id,command_key,command_hash,action,created) VALUES(?,?,?,?,?)`, out.Assignment.ID, key, hash, action, now); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func normalizeHandheldScan(c HandheldScan) (HandheldScan, error) {
	if c.LineID < 1 || c.AssignmentVersion < 1 || c.Version < 1 || c.PickVersion < 1 || len(c.Code) > 64 || len(c.Format) > 20 {
		return c, ErrInvalid
	}
	c.Code = strings.TrimSpace(c.Code)
	c.Format = strings.ToLower(strings.ReplaceAll(c.Format, "_", ""))
	if c.Source != "manual" && c.Source != "camera" && c.Source != "photo" {
		return c, ErrInvalid
	}
	return c, nil
}
func recognizeHandheldTx(tx *sql.Tx, g handheldGrant, c HandheldScan) (HandheldRecognition, error) {
	result := HandheldRecognition{Code: c.Code, Format: "Code128", Source: c.Source, State: "unknown", Message: "This is not a supported demo-local label. Try the product's SHOPDEMO Code 128 label or enter its code. Nothing was picked."}
	if c.AssignmentVersion != g.AssignmentVersion || c.Version != g.Version {
		return result, ErrConflict
	}
	var productID, pickVersion, quantity, picked int64
	var saleUnit string
	err := tx.QueryRow(`SELECT product_id,pick_version,quantity,picked_quantity,sale_unit FROM working_order_items WHERE id=? AND order_id=? AND quantity>0`, c.LineID, g.OrderID).Scan(&productID, &pickVersion, &quantity, &picked, &saleUnit)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrConflict
	}
	if err != nil {
		return result, err
	}
	if c.PickVersion != pickVersion {
		return result, ErrConflict
	}
	if c.Format != "code128" || !strings.HasPrefix(c.Code, "SHOPDEMO-") {
		return result, nil
	}
	var found int64
	var archived bool
	err = tx.QueryRow(`SELECT p.id,p.name,(p.archived=1 OR pc.archived=1) FROM product_codes pc JOIN products p ON p.id=pc.product_id WHERE pc.scheme='demo_local' AND pc.normalized_value=? AND pc.symbology='Code128'`, c.Code).Scan(&found, &result.ProductName, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if archived {
		result.State = "archived"
		result.Message = "This demo label or product has been archived. Ask the desktop manager to review this item. Nothing was picked."
		return result, nil
	}
	if found != productID {
		var present int
		err = tx.QueryRow(`SELECT count(*) FROM working_order_items WHERE order_id=? AND product_id=? AND quantity>0`, g.OrderID, found).Scan(&present)
		if err != nil {
			return result, err
		}
		result.State = "not-in-task"
		result.Message = "That product is not on this task. Check the selected item and try its label. Nothing was picked."
		if present > 0 {
			result.State = "wrong-item"
			result.Message = "That label belongs to another item in this task. Select that item deliberately, or scan the current item's label. Nothing was picked."
		}
		return result, nil
	}
	if saleUnit == "g" {
		result.State = "weight-required"
		result.CanMeasure = true
		result.Message = ErrHandheldWeight.Error()
		return result, nil
	}
	result.State = "recognized"
	result.Message = "Label matches the selected item. Review the absolute picked count, then confirm to save it."
	result.CanPick = true
	if picked == quantity {
		result.State = "already-picked"
		result.Message = "This item is already fully picked. No additional units will be added. You may explicitly correct the absolute count."
	}
	return result, nil
}
func (s *Store) PreviewHandheldScan(raw, csrf string, c HandheldScan) (HandheldRecognition, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return HandheldRecognition{}, err
	}
	defer tx.Rollback()
	g, err := handheldGrantTx(tx, raw, csrf, true, s.now().Unix())
	if err != nil {
		return HandheldRecognition{}, err
	}
	c, err = normalizeHandheldScan(c)
	if err != nil {
		return HandheldRecognition{}, err
	}
	result, err := recognizeHandheldTx(tx, g, c)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func (s *Store) ConfirmHandheldPick(raw, csrf string, c HandheldPick) (HandheldPickResult, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return HandheldPickResult{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	g, err := handheldGrantTx(tx, raw, csrf, true, now)
	if err != nil {
		return HandheldPickResult{}, err
	}
	c.HandheldScan, err = normalizeHandheldScan(c.HandheldScan)
	if err != nil {
		return HandheldPickResult{}, err
	}
	if len(c.Key) < 16 || len(c.Key) > 100 || c.Picked < 0 || c.Picked > 99 {
		return HandheldPickResult{}, ErrInvalid
	}
	hash := handheldPayload([]any{"handheld-pick", g.ID, g.AssignmentID, c})
	key := "handheld-pick:" + c.Key
	var prior string
	result := HandheldPickResult{}
	err = tx.QueryRow(`SELECT command_hash,line_id,picked,order_version,pick_version FROM handheld_pick_commands WHERE grant_id=? AND command_key=?`, g.ID, key).Scan(&prior, &result.LineID, &result.Picked, &result.Version, &result.PickVersion)
	if err == nil {
		if prior != hash {
			return HandheldPickResult{}, ErrConflict
		}
		result.Replayed = true
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	recognition, err := recognizeHandheldTx(tx, g, c.HandheldScan)
	if err != nil {
		return result, err
	}
	if !recognition.CanPick {
		if recognition.State == "weight-required" {
			return result, ErrHandheldWeight
		}
		return result, ErrInvalid
	}
	actor := pickActor{Kind: "worker", Source: c.Source, ShopperName: g.ShopperName, GrantID: g.ID, AssignmentID: g.AssignmentID, ShopperID: g.ShopperID}
	eventID, err := markWorkingLinePickedTx(tx, g.OrderID, c.LineID, c.Picked, c.PickVersion, c.Version, g.Version, g.Status, fmt.Sprintf("handheld-pick:%d:%s", g.ID, c.Key), hash, actor)
	if err != nil {
		return result, err
	}
	result = HandheldPickResult{LineID: c.LineID, Picked: c.Picked, Version: c.Version + 1, PickVersion: c.PickVersion + 1}
	if _, err = tx.Exec(`INSERT INTO handheld_pick_commands(grant_id,command_key,command_hash,event_id,line_id,picked,order_version,pick_version) VALUES(?,?,?,?,?,?,?,?)`, g.ID, key, hash, eventID, result.LineID, result.Picked, result.Version, result.PickVersion); err != nil {
		return result, err
	}
	if _, err = tx.Exec(`UPDATE handheld_grants SET last_recorded=? WHERE id=?`, now, g.ID); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
