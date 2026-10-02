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

var (
	ErrShopperUnavailable = errors.New("This shopper is unavailable. Return them to Available or choose another shopper; existing work remains assigned.")
	ErrShopperCapacity    = errors.New("This shopper is at their active-order capacity. Reassign or finish existing work, increase capacity, or choose another shopper.")
	ErrShopperActive      = errors.New("This shopper still has active work. Reassign or cancel their tasks before archiving; picking progress stays with each order.")
	ErrShopperArchived    = errors.New("This shopper is archived. Restore their profile before editing or assigning new work.")
	ErrShopperDuplicate   = errors.New("A shopper with this name already exists in your roster, including archived profiles. Choose another name or restore that profile.")
	ErrShopperRosterLimit = errors.New("This roster already contains 100 simulated identities. Edit or restore an existing profile instead.")
)

type ShopperRosterCommand struct {
	Action, Key, Name, Initials, Availability string
	Version, Capacity                         int64
}
type ShopperRosterDraft struct {
	Action, Name, Initials, Availability, Capacity string
	Truncated                                      bool
}
type ShopperRosterEvent struct{ Name, Action, Details, Created string }

func shopperScope(sid string, all bool) (string, error) {
	if all {
		return "", nil
	}
	if sid == "" {
		return "", ErrNotFound
	}
	return sid, nil
}

// All roster state and counts resolve within one scope. Immutable seed names
// supply a missing session profile; global edits never become demo defaults.
func loadShopperRoster(tx *sql.Tx, sid string, all bool) ([]Shopper, error) {
	scope, err := shopperScope(sid, all)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(shopperOrderCTE+`SELECT s.id,COALESCE(p.name,s.name),COALESCE(p.initials,s.initials),COALESCE(p.version,1),COALESCE(p.availability,'available'),COALESCE(p.capacity,0),COALESCE(p.archived,0),COUNT(t.id),CASE WHEN COALESCE(MAX(t.has_weight),0)=1 THEN COALESCE(SUM(t.lines_picked),0) ELSE COALESCE(SUM(t.counted_picked),0) END,CASE WHEN COALESCE(MAX(t.has_weight),0)=1 THEN COALESCE(SUM(t.lines_required),0) ELSE COALESCE(SUM(t.counted_required),0) END,COALESCE(MAX(t.has_weight),0)
 FROM shoppers s LEFT JOIN shopper_roster_profiles p ON p.shopper_id=s.id AND p.scope=? LEFT JOIN tasks t ON t.shopper_id=s.id
 WHERE s.scope=? OR (s.scope='' AND s.baseline=1) GROUP BY s.id ORDER BY s.id`, all, sid, scope, scope)
	if err != nil {
		return nil, err
	}
	var roster []Shopper
	for rows.Next() {
		var sh Shopper
		if err = rows.Scan(&sh.ID, &sh.Name, &sh.Initials, &sh.Version, &sh.Availability, &sh.Capacity, &sh.Archived, &sh.ActiveOrders, &sh.Picked, &sh.Required, &sh.HasWeight); err != nil {
			rows.Close()
			return nil, err
		}
		if sh.Required > 0 {
			sh.Percent = 100 * sh.Picked / sh.Required
		}
		sh.RemainingCapacity = -1
		if sh.Capacity > 0 {
			sh.RemainingCapacity = max(int64(0), sh.Capacity-sh.ActiveOrders)
		}
		sh.Assignable = !sh.Archived && sh.Availability == "available" && (sh.Capacity == 0 || sh.ActiveOrders < sh.Capacity)
		roster = append(roster, sh)
	}
	err = rows.Err()
	rows.Close()
	return roster, err
}

func shopperInRoster(roster []Shopper, id int64) *Shopper {
	for i := range roster {
		if roster[i].ID == id {
			return &roster[i]
		}
	}
	return nil
}

func (s *Store) ShopperRosterProfile(id int64, sid string, all bool) (*Shopper, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	roster, err := loadShopperRoster(tx, sid, all)
	if err != nil {
		return nil, err
	}
	profile := shopperInRoster(roster, id)
	if profile == nil {
		return nil, ErrNotFound
	}
	return profile, tx.Commit()
}

func validShopperText(value string, minRunes, maxRunes int) bool {
	n := utf8.RuneCountInString(value)
	return utf8.ValidString(value) && n >= minRunes && n <= maxRunes && strings.IndexFunc(value, unicode.IsControl) < 0
}
func shopperInitials(name string) string {
	words := strings.Fields(name)
	if len(words) == 0 {
		return ""
	}
	first, _ := utf8.DecodeRuneInString(words[0])
	value := string(unicode.ToUpper(first))
	if len(words) > 1 {
		last, _ := utf8.DecodeRuneInString(words[len(words)-1])
		value += string(unicode.ToUpper(last))
	}
	return value
}

// SaveShopperRoster is a replay-safe profile command. It cannot change order
// state, picking, allocations, receipts, assignment snapshots or stock.
func (s *Store) SaveShopperRoster(id int64, sid string, all bool, c ShopperRosterCommand) (int64, error) {
	tx, err := s.beginWrite()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	scope, err := shopperScope(sid, all)
	if err != nil {
		return 0, err
	}
	roster, err := loadShopperRoster(tx, sid, all)
	if err != nil {
		return 0, err
	}
	current := shopperInRoster(roster, id)
	// Ownership precedes payload validation and replay lookup.
	if id != 0 && current == nil {
		return 0, ErrNotFound
	}
	if len(c.Key) < 16 || len(c.Key) > 150 {
		return 0, ErrInvalid
	}
	if c.Action != "create" && c.Action != "edit" && c.Action != "archive" && c.Action != "restore" {
		return 0, ErrInvalid
	}
	if (c.Action == "create" && (id != 0 || c.Version != 0)) || (c.Action != "create" && (id < 1 || c.Version < 1)) {
		return 0, ErrInvalid
	}
	if c.Action == "create" || c.Action == "edit" {
		if !utf8.ValidString(c.Name) || !utf8.ValidString(c.Initials) || strings.IndexFunc(c.Name, unicode.IsControl) >= 0 || strings.IndexFunc(c.Initials, unicode.IsControl) >= 0 {
			return 0, ErrInvalid
		}
		c.Name = strings.Join(strings.Fields(strings.TrimSpace(c.Name)), " ")
		c.Initials = strings.TrimSpace(c.Initials)
		if c.Initials == "" {
			c.Initials = shopperInitials(c.Name)
		}
		if !validShopperText(c.Name, 1, 80) || !validShopperText(c.Initials, 1, 4) || c.Capacity < 0 || c.Capacity > 20 || (c.Availability != "available" && c.Availability != "break" && c.Availability != "off_shift") {
			return 0, ErrInvalid
		}
	} else {
		c.Name = ""
		c.Initials = ""
		c.Availability = ""
		c.Capacity = 0
	}
	encoded, _ := json.Marshal(struct {
		ID      int64
		Command ShopperRosterCommand
	}{id, c})
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	var priorHash string
	var priorID int64
	err = tx.QueryRow(`SELECT shopper_id,command_hash FROM shopper_roster_events WHERE scope=? AND command_key=?`, scope, c.Key).Scan(&priorID, &priorHash)
	if err == nil {
		if priorHash == hash {
			return priorID, nil
		}
		return 0, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if current != nil && current.Version != c.Version {
		return 0, ErrConflict
	}
	if c.Action == "create" || c.Action == "edit" {
		for _, other := range roster {
			if other.ID != id && strings.EqualFold(other.Name, c.Name) {
				return 0, ErrShopperDuplicate
			}
		}
	}
	if c.Action == "create" && len(roster) >= 100 {
		return 0, ErrShopperRosterLimit
	}
	if current != nil {
		if c.Action == "restore" {
			if !current.Archived {
				return 0, ErrConflict
			}
		} else if current.Archived {
			return 0, ErrShopperArchived
		}
		if c.Action == "archive" && current.ActiveOrders > 0 {
			return 0, ErrShopperActive
		}
	}
	var result Shopper
	if current != nil {
		result = *current
	}
	switch c.Action {
	case "create":
		inserted, e := tx.Exec(`INSERT INTO shoppers(name,initials,scope) VALUES(?,?,?)`, c.Name, c.Initials, scope)
		if e != nil {
			return 0, e
		}
		id, err = inserted.LastInsertId()
		if err != nil {
			return 0, err
		}
		result = Shopper{ID: id, Version: 1, Name: c.Name, Initials: c.Initials, Availability: c.Availability, Capacity: c.Capacity}
	case "edit":
		result.Name, result.Initials, result.Availability, result.Capacity = c.Name, c.Initials, c.Availability, c.Capacity
		result.Version++
	case "archive":
		result.Archived = true
		result.Version++
	case "restore":
		result.Archived = false
		result.Version++
	}
	_, err = tx.Exec(`INSERT INTO shopper_roster_profiles(scope,shopper_id,name,initials,availability,capacity,archived,version) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(scope,shopper_id) DO UPDATE SET name=excluded.name,initials=excluded.initials,availability=excluded.availability,capacity=excluded.capacity,archived=excluded.archived,version=excluded.version`, scope, id, result.Name, result.Initials, result.Availability, result.Capacity, result.Archived, result.Version)
	if err != nil {
		return 0, err
	}
	details := fmt.Sprintf("Created simulated identity %q (%s); %s; %s.", result.Name, result.Initials, result.AvailabilityLabel(), result.CapacityLabel())
	if c.Action == "edit" {
		changes := []string{}
		if current.Name != result.Name {
			changes = append(changes, fmt.Sprintf("Name: %q → %q", current.Name, result.Name))
		}
		if current.Initials != result.Initials {
			changes = append(changes, fmt.Sprintf("Initials: %s → %s", current.Initials, result.Initials))
		}
		if current.Availability != result.Availability {
			changes = append(changes, fmt.Sprintf("Availability: %s → %s", current.AvailabilityLabel(), result.AvailabilityLabel()))
		}
		if current.Capacity != result.Capacity {
			changes = append(changes, fmt.Sprintf("Capacity: %s → %s", current.CapacityLabel(), result.CapacityLabel()))
		}
		if len(changes) == 0 {
			changes = append(changes, "No profile changes")
		}
		details = strings.Join(changes, "; ") + ". Existing assignments and recorded picking progress are unchanged."
	}
	if c.Action == "archive" {
		details = "Profile archived with no active assignments. Identity and assignment history retained."
	}
	if c.Action == "restore" {
		details = fmt.Sprintf("Profile restored: %s; %s. Historical assignments and recorded picking progress are unchanged.", result.AvailabilityLabel(), result.CapacityLabel())
	}
	_, err = tx.Exec(`INSERT INTO shopper_roster_events(scope,shopper_id,command_key,command_hash,name,action,details) VALUES(?,?,?,?,?,?,?)`, scope, id, c.Key, hash, result.Name, c.Action, details)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s Shopper) AvailabilityLabel() string {
	if s.Archived {
		return "Archived"
	}
	switch s.Availability {
	case "available":
		return "Available"
	case "break":
		return "On break"
	case "off_shift":
		return "Off shift"
	}
	return "Unavailable"
}
func (s Shopper) CapacityLabel() string {
	if s.Capacity == 0 {
		return "No set limit"
	}
	return fmt.Sprintf("%d active-order capacity", s.Capacity)
}
func (s Shopper) AssignmentBlockReason() string {
	if s.Archived {
		return "Archived; restore before assigning"
	}
	if s.Availability != "available" {
		return s.AvailabilityLabel() + "; return to Available before assigning"
	}
	if s.Capacity > 0 && s.ActiveOrders >= s.Capacity {
		return "At capacity; finish or reassign existing work"
	}
	return ""
}
func shopperRosterProblem(err error) bool {
	return orderCommandProblem(err) || errors.Is(err, ErrShopperUnavailable) || errors.Is(err, ErrShopperCapacity) || errors.Is(err, ErrShopperActive) || errors.Is(err, ErrShopperArchived) || errors.Is(err, ErrShopperDuplicate) || errors.Is(err, ErrShopperRosterLimit)
}
