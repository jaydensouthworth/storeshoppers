package shop

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

type AttentionCommand struct {
	Action, Key, Reason string
	Version             int64
}

// AttentionOrder records a private manager hold or note. The transaction owns
// scope, replay and version checks; holds never change allocations or status.
func (s *Store) AttentionOrder(id int64, sid string, allOrders bool, c AttentionCommand) error {
	tx, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, reason string
	var version int64
	err = tx.QueryRow(`SELECT status,order_version,attention_reason FROM orders WHERE id=? AND (? OR session_id=?)`, id, allOrders, sid).Scan(&status, &version, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	c.Reason = strings.TrimSpace(c.Reason)
	if c.Action == "release" && c.Reason == "" {
		c.Reason = "Manager released the review hold"
	}
	if c.Action != "hold" && c.Action != "release" && c.Action != "note" {
		return ErrInvalid
	}
	if c.Version < 1 || len(c.Key) < 16 || len(c.Key) > 150 || !utf8.ValidString(c.Reason) || utf8.RuneCountInString(c.Reason) < 3 || utf8.RuneCountInString(c.Reason) > 240 || strings.IndexFunc(c.Reason, unicode.IsControl) >= 0 {
		return ErrInvalid
	}
	encoded, _ := json.Marshal(c)
	sum := sha256.Sum256(append([]byte("attention:"), encoded...))
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
	if status != "Placed" && status != "Picking" {
		return ErrTerminal
	}
	if version != c.Version {
		return ErrConflict
	}
	err = nil
	details := "Private manager note; fulfillment and stock unchanged."
	switch c.Action {
	case "hold":
		if reason != "" {
			return ErrConflict
		}
		_, err = tx.Exec(`UPDATE orders SET attention_reason=?,attention_since=? WHERE id=?`, c.Reason, s.now().Unix(), id)
		details = "Manager review hold opened. Item changes, picking, weight review and assignment remain available."
	case "release":
		if reason == "" {
			return ErrConflict
		}
		_, err = tx.Exec(`UPDATE orders SET attention_reason='',attention_since=0 WHERE id=?`, id)
		details = "Manager review hold released. Fulfillment and stock unchanged."
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE orders SET order_version=order_version+1 WHERE id=?`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO order_events(order_id,command_key,command_hash,action,reason,details,visibility) VALUES(?,?,?,?,?,?,'internal')`, id, c.Key, hash, c.Action, c.Reason, details); err != nil {
		return err
	}
	return tx.Commit()
}
