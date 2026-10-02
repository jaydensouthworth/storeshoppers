package shop

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A shared basket carries every line plus URL-encoded Unicode instructions.
// Keep its larger request budget confined to customer basket mutations.
const customerCartFormLimit = 32 << 10

var errCartQuantityDraft = errors.New("Some quantities have not been saved. Update each changed item, or restore its saved quantity, then review your basket before placing the order.")

// Customer drafts live only in this response. They never update the basket,
// become concurrency tokens, or enter cookies, browser storage or the database.
func (v *View) retainCustomerCartDraft(r *http.Request) {
	// A checkout rotates this key. Do not carry an old request's notes into the
	// next basket, even if another tab placed its order while this request ran.
	if key := r.PostForm.Get("checkout_key"); key != "" && key != v.Session.CheckoutKey {
		return
	}
	v.Instructions, v.CartDraftShortened = boundedCartInstructions(r.PostForm.Get("instructions"))
	v.CartQuantities = make(map[int64]string)
	for _, line := range v.Basket.Lines {
		key := "quantity-" + strconv.FormatInt(line.Product.ID, 10)
		if values, ok := r.PostForm[key]; ok && len(values) > 0 {
			value, shortened := boundedManagerDraft(values[0], 64)
			v.CartDraftShortened = v.CartDraftShortened || shortened
			if value != strconv.FormatInt(line.Quantity, 10) {
				v.CartQuantities[line.Product.ID] = value
			}
		}
	}
}

func boundedCartInstructions(value string) (string, bool) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	changed := !utf8.ValidString(value)
	runes := []rune(strings.ToValidUTF8(value, "�"))
	if len(runes) > 500 {
		runes = runes[:500]
		changed = true
	}
	for i, r := range runes {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			runes[i] = '�'
			changed = true
		}
	}
	return string(runes), changed
}

func (v View) CustomerQuantity(line CartLine) string {
	if value, ok := v.CartQuantities[line.Product.ID]; ok {
		return value
	}
	if v.CartDraft != nil && v.CartDraft.ProductID == line.Product.ID {
		return v.CartDraft.Quantity
	}
	return strconv.FormatInt(line.Quantity, 10)
}

func (v View) CustomerQuantityUnsaved(line CartLine) bool {
	return v.CustomerQuantity(line) != strconv.FormatInt(line.Quantity, 10)
}

func hasCustomerQuantityDraft(r *http.Request, b Basket) bool {
	for _, line := range b.Lines {
		if values, ok := r.PostForm["quantity-"+strconv.FormatInt(line.Product.ID, 10)]; ok {
			if len(values) != 1 {
				return true
			}
			quantity, err := num(values[0])
			if err != nil || quantity != line.Quantity {
				return true
			}
		}
	}
	return false
}
