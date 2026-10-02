package shop

import (
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ManagerDraft is display-only request data. Never use it as current inventory,
// a concurrency token or authorization evidence. Templates escape every value.
type ManagerDraft struct {
	ProductID                                     int64
	ProductValue, Quantity, Delta, Reason, Action string
	Truncated                                     bool
}

func boundedManagerDraft(value string, limit int) (string, bool) {
	changed := !utf8.ValidString(value)
	value = strings.ToValidUTF8(value, "�")
	runes := []rune(value)
	if len(runes) > limit {
		runes = runes[:limit]
		changed = true
	}
	for i, r := range runes {
		if unicode.IsControl(r) {
			runes[i] = '�'
			changed = true
		}
	}
	return string(runes), changed
}
func managerDraft(r *http.Request, action string) *ManagerDraft {
	d := &ManagerDraft{Action: action}
	d.ProductID, _ = num(r.PostForm.Get("product_id"))
	fields := []struct {
		key   string
		dst   *string
		limit int
	}{{"product_id", &d.ProductValue, 64}, {"quantity", &d.Quantity, 64}, {"delta", &d.Delta, 64}, {"reason", &d.Reason, 500}}
	for _, f := range fields {
		v, changed := boundedManagerDraft(r.PostForm.Get(f.key), f.limit)
		*f.dst = v
		d.Truncated = d.Truncated || changed
	}
	return d
}
func basketFormDraft(r *http.Request, b Basket) *ManagerDraft {
	action := "add"
	if strings.HasSuffix(r.URL.Path, "/renew") {
		action = "renew"
	} else {
		pid, _ := num(r.PostForm.Get("product_id"))
		for _, l := range b.Lines {
			if l.Product.ID == pid {
				action = "line"
				break
			}
		}
	}
	return managerDraft(r, action)
}
