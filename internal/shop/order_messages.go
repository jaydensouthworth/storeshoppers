package shop

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MessageMaxCodepoints = 500
	MessageMaxBytes      = 2000
	MessageMaxPage       = 50
	messageActorRate     = 20
	messageOrderRate     = 40
	messageGlobalRate    = 120
	messageOrderCount    = 200
	messageOrderBytes    = 256 << 10
	messageGlobalCount   = 10000
	messageGlobalBytes   = 8 << 20
)

var (
	ErrMessageCSRF   = errors.New("This message form expired. Refresh the conversation before sending.")
	ErrMessageClosed = errors.New("Messages can be sent only while this order has an active shopping task and is not ready or closed.")
	ErrMessageRate   = errors.New("Messages are arriving too quickly. Wait a minute, then review the conversation before sending.")
	ErrMessageLimit  = errors.New("This demo conversation or shared message storage is full. Existing messages are kept; new messages cannot be sent.")
)

type MessageCommand struct {
	ConversationKey, Key, Body      string
	AssignmentID, AssignmentVersion int64
}
type MessageQuery struct {
	After, Before int64
	Limit         int
}

// No customer session ID, grant token, command key or manager note is projected.
type OrderMessage struct {
	Revision, AssignmentID, AssignmentVersion, ShopperID, CreatedAt int64
	Actor, SenderName, Body, Created                                string
}
type MessageConversation struct {
	OrderID, AssignmentID, AssignmentVersion, Expires, ContextVersion, Revision, Cursor, OldestRevision int64
	Reference, Status, AssignmentName, ConversationKey, CSRF, ReadOnlyReason                            string
	CanSend, HasMore, HasOlder                                                                          bool
	Messages                                                                                            []OrderMessage
}
type MessageResult struct {
	Message  OrderMessage
	Replayed bool
}
type messageIdentity struct {
	actor, customer string
	grant           int64
	conversation    *MessageConversation
	assignment      *ShopperAssignment
}

// The epoch and owner are never disclosed. This is an opaque context guard,
// not a bearer capability; every operation separately authenticates ownership.
func messageConversationKey(epoch string, id int64, owner string) string {
	return handheldPayload([]any{"order-conversation-v1", epoch, id, owner})
}
func customerMessageIdentityTx(tx *sql.Tx, id int64, sid, csrf string, write bool, now int64) (messageIdentity, error) {
	out := messageIdentity{actor: "customer", customer: sid, conversation: &MessageConversation{OrderID: id}}
	v := out.conversation
	var epoch string
	err := tx.QueryRow(`SELECT o.reference,o.status,o.order_version,s.csrf,s.expires,h.epoch FROM orders o JOIN sessions s ON s.id=o.session_id AND s.expires>? CROSS JOIN handheld_state h WHERE o.id=? AND o.session_id=? AND h.id=1`, now, id, sid).Scan(&v.Reference, &v.Status, &v.ContextVersion, &v.CSRF, &v.Expires, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if write && subtle.ConstantTimeCompare([]byte(csrf), []byte(v.CSRF)) != 1 {
		return out, ErrMessageCSRF
	}
	v.ConversationKey = messageConversationKey(epoch, id, sid)
	out.assignment, err = activeAssignment(tx, id)
	if err != nil {
		return out, err
	}
	setMessageAvailability(v, out.assignment)
	return out, nil
}
func handheldMessageIdentityTx(tx *sql.Tx, raw, csrf string, write bool, now int64) (messageIdentity, error) {
	g, err := handheldGrantTx(tx, raw, csrf, write, now)
	if err != nil {
		return messageIdentity{}, err
	}
	var practice bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM employee_store_orders WHERE order_id=? AND epoch=? AND practice=1)`, g.OrderID, g.Epoch).Scan(&practice); err != nil {
		return messageIdentity{}, err
	}
	if practice {
		return messageIdentity{}, ErrEmployeePractice
	}
	v := &MessageConversation{OrderID: g.OrderID, Status: g.Status, ContextVersion: g.Version, CSRF: g.CSRF, Expires: g.Expires, ConversationKey: messageConversationKey(g.Epoch, g.OrderID, g.Owner)}
	if err = tx.QueryRow(`SELECT reference FROM orders WHERE id=?`, g.OrderID).Scan(&v.Reference); err != nil {
		return messageIdentity{}, err
	}
	a := &ShopperAssignment{ID: g.AssignmentID, OrderID: g.OrderID, Version: g.AssignmentVersion, ShopperID: g.ShopperID, ShopperName: g.ShopperName, State: "active"}
	setMessageAvailability(v, a)
	return messageIdentity{actor: "worker", grant: g.ID, conversation: v, assignment: a}, nil
}
func setMessageAvailability(v *MessageConversation, a *ShopperAssignment) {
	if a != nil {
		v.AssignmentID = a.ID
		v.AssignmentVersion = a.Version
		v.AssignmentName = a.ShopperName
	}
	v.CanSend = a != nil && (v.Status == "Placed" || v.Status == "Picking")
	if !v.CanSend {
		v.ReadOnlyReason = ErrMessageClosed.Error()
	}
}

// Authorization and the complete projection share the same SQLite snapshot as
// lifecycle changes, reset fencing and sends. Reads do not extend any session.
func (s *Store) CustomerMessages(id int64, sid string, q MessageQuery) (*MessageConversation, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	who, err := customerMessageIdentityTx(tx, id, sid, "", false, s.now().Unix())
	if err != nil {
		return nil, err
	}
	if err = readMessagesTx(tx, who.conversation, q); err != nil {
		return nil, err
	}
	return who.conversation, tx.Commit()
}
func (s *Store) HandheldMessages(raw string, q MessageQuery) (*MessageConversation, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	who, err := handheldMessageIdentityTx(tx, raw, "", false, s.now().Unix())
	if err != nil {
		return nil, err
	}
	if err = readMessagesTx(tx, who.conversation, q); err != nil {
		return nil, err
	}
	return who.conversation, tx.Commit()
}

const messageColumns = `revision,assignment_id,assignment_version,shopper_id,created,actor,sender_name,body`

type messageScanner interface{ Scan(...any) error }

func scanMessage(row messageScanner) (OrderMessage, error) {
	var m OrderMessage
	err := row.Scan(&m.Revision, &m.AssignmentID, &m.AssignmentVersion, &m.ShopperID, &m.CreatedAt, &m.Actor, &m.SenderName, &m.Body)
	m.Created = handheldTime(m.CreatedAt)
	return m, err
}
func readMessagesTx(tx *sql.Tx, v *MessageConversation, q MessageQuery) error {
	if q.After < 0 || q.Before < 0 || (q.After > 0 && q.Before > 0) || q.Limit < 0 || q.Limit > MessageMaxPage {
		return ErrInvalid
	}
	if q.Limit == 0 {
		q.Limit = MessageMaxPage
	}
	if v.CanSend {
		if err := messageStorageQuotaTx(tx, v.OrderID, 1); errors.Is(err, ErrMessageLimit) {
			v.CanSend = false
			v.ReadOnlyReason = ErrMessageLimit.Error()
		} else if err != nil {
			return err
		}
	}
	if err := tx.QueryRow(`SELECT COALESCE(MAX(revision),0) FROM order_messages WHERE order_id=?`, v.OrderID).Scan(&v.Revision); err != nil {
		return err
	}
	condition, ordering := "", "DESC"
	args := []any{v.OrderID}
	if q.After > 0 {
		condition = " AND revision>?"
		ordering = "ASC"
		args = append(args, q.After)
	} else if q.Before > 0 {
		condition = " AND revision<?"
		args = append(args, q.Before)
	}
	args = append(args, q.Limit)
	rows, err := tx.Query(`SELECT `+messageColumns+` FROM order_messages WHERE order_id=?`+condition+` ORDER BY revision `+ordering+` LIMIT ?`, args...)
	if err != nil {
		return err
	}
	v.Messages = make([]OrderMessage, 0, q.Limit)
	for rows.Next() {
		m, e := scanMessage(rows)
		if e != nil {
			rows.Close()
			return e
		}
		v.Messages = append(v.Messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if ordering == "DESC" {
		for i, j := 0, len(v.Messages)-1; i < j; i, j = i+1, j-1 {
			v.Messages[i], v.Messages[j] = v.Messages[j], v.Messages[i]
		}
	}
	if len(v.Messages) > 0 {
		v.OldestRevision = v.Messages[0].Revision
		v.Cursor = v.Messages[len(v.Messages)-1].Revision
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM order_messages WHERE order_id=? AND revision<?),EXISTS(SELECT 1 FROM order_messages WHERE order_id=? AND revision>?)`, v.OrderID, v.OldestRevision, v.OrderID, v.Cursor).Scan(&v.HasOlder, &v.HasMore); err != nil {
			return err
		}
	}
	return nil
}
func normalizeMessageBody(body string) (string, error) {
	if !utf8.ValidString(body) {
		return "", ErrInvalid
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	// Reject controls before trimming so a pasted NUL or bare CR cannot disappear.
	for _, r := range body {
		if unicode.IsControl(r) && r != '\n' {
			return "", ErrInvalid
		}
	}
	body = strings.TrimSpace(body)
	if body == "" || len(body) > MessageMaxBytes || utf8.RuneCountInString(body) > MessageMaxCodepoints {
		return "", ErrInvalid
	}
	return body, nil
}
func validMessageKey(key string) bool {
	if len(key) < 16 || len(key) > 100 {
		return false
	}
	// Successful keys are echoed in an acknowledgment header. Keep this
	// server-generated namespace ASCII-safe across HTTP intermediaries.
	for _, c := range key {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func (s *Store) SendCustomerMessage(id int64, sid, csrf string, c MessageCommand) (MessageResult, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return MessageResult{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	who, err := customerMessageIdentityTx(tx, id, sid, csrf, true, now)
	if err != nil {
		return MessageResult{}, err
	}
	out, err := sendMessageTx(tx, who, c, now)
	if err != nil {
		return MessageResult{}, err
	}
	return out, tx.Commit()
}
func (s *Store) SendHandheldMessage(raw, csrf string, c MessageCommand) (MessageResult, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return MessageResult{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	who, err := handheldMessageIdentityTx(tx, raw, csrf, true, now)
	if err != nil {
		return MessageResult{}, err
	}
	out, err := sendMessageTx(tx, who, c, now)
	if err != nil {
		return MessageResult{}, err
	}
	return out, tx.Commit()
}
func messageNamespace(who messageIdentity) (string, any) {
	if who.actor == "customer" {
		return "customer_session_id", who.customer
	}
	return "grant_id", who.grant
}
func findOwnMessageTx(tx *sql.Tx, who messageIdentity, key string) (OrderMessage, string, error) {
	column, identity := messageNamespace(who)
	var m OrderMessage
	var hash string
	err := tx.QueryRow(`SELECT `+messageColumns+`,command_hash FROM order_messages WHERE order_id=? AND actor=? AND `+column+`=? AND command_key=?`, who.conversation.OrderID, who.actor, identity, key).Scan(&m.Revision, &m.AssignmentID, &m.AssignmentVersion, &m.ShopperID, &m.CreatedAt, &m.Actor, &m.SenderName, &m.Body, &hash)
	m.Created = handheldTime(m.CreatedAt)
	return m, hash, err
}
func sendMessageTx(tx *sql.Tx, who messageIdentity, c MessageCommand, now int64) (MessageResult, error) {
	v := who.conversation
	if !v.CanSend {
		return MessageResult{}, ErrMessageClosed
	}
	if c.ConversationKey != v.ConversationKey {
		return MessageResult{}, ErrConflict
	}
	if !validMessageKey(c.Key) || now < 1 || now > 253402300799 {
		return MessageResult{}, ErrInvalid
	}
	body, bodyErr := normalizeMessageBody(c.Body)
	c.Body = body
	hash := handheldPayload([]any{"order-message-v1", who.actor, c})
	prior, priorHash, err := findOwnMessageTx(tx, who, c.Key)
	if err == nil {
		// A consumed key with any altered payload is a conflict, even if the
		// changed body or assignment fields would also be invalid for a new send.
		if bodyErr != nil || hash != priorHash {
			return MessageResult{}, ErrConflict
		}
		return MessageResult{Message: prior, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MessageResult{}, err
	}
	if bodyErr != nil || c.AssignmentID < 1 || c.AssignmentVersion < 1 {
		return MessageResult{}, ErrInvalid
	}
	if c.AssignmentID != v.AssignmentID || c.AssignmentVersion != v.AssignmentVersion {
		return MessageResult{}, ErrConflict
	}
	if err = messageQuotaTx(tx, v.OrderID, who.actor, len(c.Body), now); err != nil {
		return MessageResult{}, err
	}
	a := who.assignment
	m := OrderMessage{AssignmentID: a.ID, AssignmentVersion: a.Version, ShopperID: a.ShopperID, CreatedAt: now, Actor: who.actor, SenderName: a.ShopperName, Body: c.Body, Created: handheldTime(now)}
	if who.actor == "customer" {
		m.SenderName = "Demo customer"
	}
	if err = tx.QueryRow(`SELECT COALESCE(MAX(revision),0)+1 FROM order_messages WHERE order_id=?`, v.OrderID).Scan(&m.Revision); err != nil {
		return MessageResult{}, err
	}
	_, err = tx.Exec(`INSERT INTO order_messages(order_id,revision,actor,customer_session_id,grant_id,assignment_id,assignment_version,shopper_id,shopper_name,sender_name,body,created,command_key,command_hash) VALUES(?,?,?,NULLIF(?,''),NULLIF(?,0),?,?,?,?,?,?,?,?,?)`, v.OrderID, m.Revision, m.Actor, who.customer, who.grant, m.AssignmentID, m.AssignmentVersion, m.ShopperID, a.ShopperName, m.SenderName, m.Body, m.CreatedAt, c.Key, hash)
	return MessageResult{Message: m}, err
}
func messageStorageQuotaTx(tx *sql.Tx, id int64, size int) error {
	var count, bytes int64
	// The count caps bound both scans even if a manually altered database exceeds
	// the supported limit. No quota failure retains a command or purges history.
	if err := tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(CAST(body AS BLOB))),0) FROM (SELECT body FROM order_messages WHERE order_id=? LIMIT ?)`, id, messageOrderCount+1).Scan(&count, &bytes); err != nil {
		return err
	}
	if count >= messageOrderCount || bytes+int64(size) > messageOrderBytes {
		return ErrMessageLimit
	}
	if err := tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(CAST(body AS BLOB))),0) FROM (SELECT body FROM order_messages LIMIT ?)`, messageGlobalCount+1).Scan(&count, &bytes); err != nil {
		return err
	}
	if count >= messageGlobalCount || bytes+int64(size) > messageGlobalBytes {
		return ErrMessageLimit
	}
	return nil
}
func messageQuotaTx(tx *sql.Tx, id int64, actor string, size int, now int64) error {
	if err := messageStorageQuotaTx(tx, id, size); err != nil {
		return err
	}
	var count int64
	for _, q := range []struct {
		sql   string
		args  []any
		limit int64
	}{
		{`SELECT COUNT(*) FROM (SELECT 1 FROM order_messages WHERE order_id=? AND actor=? AND created>? LIMIT ?)`, []any{id, actor, now - 60, messageActorRate}, messageActorRate},
		{`SELECT COUNT(*) FROM (SELECT 1 FROM order_messages WHERE order_id=? AND created>? LIMIT ?)`, []any{id, now - 60, messageOrderRate}, messageOrderRate},
		{`SELECT COUNT(*) FROM (SELECT 1 FROM order_messages WHERE created>? LIMIT ?)`, []any{now - 60, messageGlobalRate}, messageGlobalRate},
	} {
		if err := tx.QueryRow(q.sql, q.args...).Scan(&count); err != nil {
			return err
		}
		if count >= q.limit {
			return ErrMessageRate
		}
	}
	return nil
}

// These read-only lookups resolve an ambiguous submission only in the caller's
// own namespace. Absence never implies that resubmitting is automatically safe.
func (s *Store) FindCustomerMessage(id int64, sid, conversation, key string) (*OrderMessage, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	who, err := customerMessageIdentityTx(tx, id, sid, "", false, s.now().Unix())
	if err != nil {
		return nil, err
	}
	return findMessageTx(tx, who, conversation, key)
}
func (s *Store) FindHandheldMessage(raw, conversation, key string) (*OrderMessage, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	who, err := handheldMessageIdentityTx(tx, raw, "", false, s.now().Unix())
	if err != nil {
		return nil, err
	}
	return findMessageTx(tx, who, conversation, key)
}
func findMessageTx(tx *sql.Tx, who messageIdentity, conversation, key string) (*OrderMessage, error) {
	if conversation != who.conversation.ConversationKey {
		return nil, ErrConflict
	}
	if !validMessageKey(key) {
		return nil, ErrInvalid
	}
	m, _, err := findOwnMessageTx(tx, who, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	return &m, tx.Commit()
}
