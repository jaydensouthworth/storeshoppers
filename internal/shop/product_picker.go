package shop

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type ProductPicker struct {
	ID, ActionURL, Search, Context, CatalogQuote           string
	SourceLineID, OrderVersion, BasketRevision, SelectedID int64
	Results                                                []Product
	More                                                   bool
	DefaultQuantity                                        int64
	QuantityDraft                                          string
}

func (p ProductPicker) QuantityValue(product Product) string {
	if p.SelectedID == product.ID && p.QuantityDraft != "" {
		return p.QuantityDraft
	}
	quantity := product.QuantityStep
	if p.DefaultQuantity > 0 && p.DefaultQuantity%product.QuantityStep == 0 && p.DefaultQuantity <= product.QuantityLimit() {
		quantity = p.DefaultQuantity
	}
	return fmt.Sprint(quantity)
}

// Only the selected product's field is interpreted. Keep the old generic field
// as a fallback for open forms and API clients from the counted workflow.
func selectedPickerQuantity(r *http.Request, productID int64) {
	if values, ok := r.PostForm[fmt.Sprintf("quantity_%d", productID)]; ok && len(values) > 0 {
		r.PostForm.Set("quantity", values[0])
	}
}

// ProductPickerResults is bounded server-side. Caller must verify the owning
// order/basket before invoking it; catalog data itself remains shared in demo.
func (s *Store) ProductPickerResults(search string, selected, source int64, excluded map[int64]bool) ([]Product, bool, error) {
	search, _ = boundedManagerDraft(strings.TrimSpace(search), 100)
	state, err := loadPricing(s.db, s.now().Unix())
	if err != nil {
		return nil, false, err
	}
	args := []any{source, source, source, search, search, search, selected}
	where := `WHERE p.archived=0 AND c.archived=0 AND COALESCE(t.archived,0)=0 AND (?=0 OR p.sale_unit=(SELECT sale_unit FROM products WHERE id=?)) AND p.stock>0 AND p.id<>? AND (?='' OR instr(lower(p.name||' '||p.sku||' '||p.barcode),lower(?))>0 OR EXISTS(SELECT 1 FROM product_codes pc WHERE pc.product_id=p.id AND pc.archived=0 AND instr(lower(pc.raw_value||' '||pc.normalized_value),lower(?))>0) OR p.id=?)`
	for id := range excluded {
		where += ` AND p.id<>?`
		args = append(args, id)
	}
	args = append(args, selected)
	rows, err := s.db.Query(`SELECT `+productSelect+productJoins+where+` ORDER BY CASE WHEN p.id=? THEN 0 ELSE 1 END,lower(p.name),p.id LIMIT 7`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var products []Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, false, err
		}
		state.apply(&p)
		products = append(products, p)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(products) > 6
	if more {
		products = products[:6]
	}
	return products, more, nil
}
func (a *App) populateProductPicker(r *http.Request, v *View, basket bool) error {
	values := r.URL.Query()
	context := values.Get("context")
	if context == "" {
		context = "add"
	}
	lineID, _ := num(values.Get("line_id"))
	selected, _ := num(values.Get("selected_id"))
	search, _ := boundedManagerDraft(strings.TrimSpace(values.Get("product_q")), 100)
	if r.Method == http.MethodPost {
		search, _ = boundedManagerDraft(strings.TrimSpace(r.PostForm.Get("product_q")), 100)
	}
	if r.Method == http.MethodPost && v.OrderOverrideDraft != nil {
		if strings.HasPrefix(v.OrderOverrideDraft["form_id"], "sub-") {
			context = "sub"
			lineID, _ = num(strings.TrimPrefix(v.OrderOverrideDraft["form_id"], "sub-"))
			selected, _ = num(v.OrderOverrideDraft["replacement_id"])
		} else if v.OrderOverrideDraft["form_id"] == "add" {
			context = "add"
			selected, _ = num(v.OrderOverrideDraft["product_id"])
		}
	}
	if basket && v.BasketDraft != nil && v.BasketDraft.Action == "add" {
		selected = v.BasketDraft.ProductID
	}
	p := &ProductPicker{Search: search, Context: context, SourceLineID: lineID, SelectedID: selected, CatalogQuote: v.OrderCatalogQuote, OrderVersion: v.Order.Version, BasketRevision: v.ManagedBasket.Revision}
	if r.Method == http.MethodPost {
		p.QuantityDraft, _ = boundedManagerDraft(r.PostForm.Get("quantity"), 64)
	}
	excluded := make(map[int64]bool)
	var source int64
	if basket {
		if context != "add" || lineID != 0 {
			return ErrNotFound
		}
		p.ID = "basket-picker-add"
		p.ActionURL = "/manager/baskets/" + v.ManagedBasket.ID + "/products"
		for _, line := range v.ManagedBasket.Lines {
			excluded[line.Product.ID] = true
		}
	} else {
		p.ActionURL = fmt.Sprintf("/manager/orders/%d/products", v.Order.ID)
		switch context {
		case "add":
			if lineID != 0 {
				return ErrNotFound
			}
			p.ID = "order-picker-add"
			for _, line := range v.Order.WorkingItems {
				excluded[line.ProductID] = true
			}
		case "sub":
			p.ID = fmt.Sprintf("order-picker-sub-%d", lineID)
			for _, line := range v.Order.WorkingItems {
				if line.LineID == lineID {
					source = line.ProductID
					p.DefaultQuantity = line.Quantity
				}
			}
			if source == 0 {
				return ErrNotFound
			}
		default:
			return ErrNotFound
		}
	}
	var err error
	p.Results, p.More, err = a.store.ProductPickerResults(search, selected, source, excluded)
	if err != nil {
		return err
	}
	if !basket {
		for i := range p.Results {
			if step, exists := v.Order.WorkingSteps[p.Results[i].ID]; exists {
				p.Results[i].QuantityStep = step
			}
		}
	}
	v.ProductPicker = p
	return nil
}
func (a *App) searchOrderProducts(w http.ResponseWriter, r *http.Request) {
	id, err := num(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.pickingView(w, r, id, "", nil)
}
func (a *App) searchBasketProducts(w http.ResponseWriter, r *http.Request) {
	a.basketsView(w, r, "", nil)
}
func (a *App) renderProductPicker(w http.ResponseWriter, r *http.Request, v View) bool {
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/products") && r.Header.Get("HX-Request") == "true" {
		a.renderNamed(w, r, "manager-product-picker-results", v, http.StatusOK)
		return true
	}
	return false
}
func (a *App) handleProductPickerError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
	} else {
		a.fail(w, err)
	}
	return true
}
