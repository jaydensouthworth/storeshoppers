package shop

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

type ProductDetailPage struct {
	Barcode *ProductBarcodeLabel
	Product Product
	Details ProductDetails
	BackURL string
}

func storefrontContext(values url.Values) (string, string, bool, bool) {
	search, _ := boundedManagerDraft(strings.TrimSpace(values.Get("q")), 100)
	category, _ := boundedManagerDraft(strings.TrimSpace(values.Get("category")), 80)
	return search, category, values.Get("sales") == "1", values.Get("featured") == "1"
}
func storefrontURL(search, category string, sales, featured bool) string {
	v := url.Values{}
	if search != "" {
		v.Set("q", search)
	}
	if category != "" {
		v.Set("category", category)
	}
	if sales {
		v.Set("sales", "1")
	}
	if featured {
		v.Set("featured", "1")
	}
	if len(v) == 0 {
		return "/"
	}
	return "/?" + v.Encode()
}
func (v View) ProductURL(id int64) string {
	return productPageURL(id, v.Search, v.Category, v.SalesOnly, v.FeaturedOnly)
}
func productPageURL(id int64, search, category string, sales, featured bool) string {
	return fmt.Sprintf("/products/%d", id) + strings.TrimPrefix(storefrontURL(search, category, sales, featured), "/")
}
func (a *App) populateProductPage(v *View, id int64) error {
	p, d, err := a.store.publicProductDetails(id)
	if err != nil {
		return err
	}
	v.Section = "product"
	v.Title = p.Name
	v.ProductPage = &ProductDetailPage{Product: p, Details: d, BackURL: storefrontURL(v.Search, v.Category, v.SalesOnly, v.FeaturedOnly)}
	label, labelErr := a.store.ProductBarcode(id)
	if labelErr != nil && !errors.Is(labelErr, ErrNotFound) {
		return labelErr
	}
	v.ProductPage.Barcode = label
	return nil
}
func (a *App) showProduct(w http.ResponseWriter, r *http.Request) {
	id, err := num(r.PathValue("id"))
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	v.Search, v.Category, v.SalesOnly, v.FeaturedOnly = storefrontContext(r.URL.Query())
	if err = a.populateProductPage(&v, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			a.productUnavailable(w, r, v)
		} else {
			a.fail(w, err)
		}
		return
	}
	a.render(w, r, v, http.StatusOK)
}

func (a *App) productUnavailable(w http.ResponseWriter, r *http.Request, v View) {
	v.Section = "product-unavailable"
	v.Title = "Product unavailable"
	v.Message, v.Error = "", ""
	v.ProductPage = &ProductDetailPage{BackURL: storefrontURL(v.Search, v.Category, v.SalesOnly, v.FeaturedOnly)}
	status := http.StatusNotFound
	// A cart action can race an archive after the product page was opened.
	// HTMX must swap this useful validation state rather than retain a stale
	// Add button and replace the message with its generic response-error notice.
	if r.Method == http.MethodPost && r.Header.Get("HX-Request") == "true" {
		status = http.StatusOK
	}
	a.render(w, r, v, status)
}

var productDetailFields = map[string]int{"details_kind": 12, "details_body": 1600, "package_label": 80, "package_details": 240, "nutrition_enabled": 1, "nutrition_serving": 80, "nutrition_energy": 10, "nutrition_fat": 12, "nutrition_carbs": 12, "nutrition_protein": 12, "nutrition_sodium": 10}

func boundedProductDetailDraft(raw string, limit int) (string, bool) {
	changed := !utf8.ValidString(raw)
	value := []rune(strings.ToValidUTF8(raw, "�"))
	if len(value) > limit {
		value = value[:limit]
		changed = true
	}
	return string(value), changed
}

func parseProductDetails(values url.Values) (ProductDetails, map[string]string, bool, error) {
	d := ProductDetails{Kind: "unspecified"}
	draft := make(map[string]string)
	invalid, posted := false, false
	for key, limit := range productDetailFields {
		_, exists := values[key]
		posted = posted || exists
		value, tooLong := boundedProductDetailDraft(values.Get(key), limit+1)
		draft[key] = value
		invalid = invalid || tooLong
	}
	if !posted {
		return d, nil, false, nil
	}
	d.Kind = draft["details_kind"]
	d.Body = draft["details_body"]
	d.PackageLabel = draft["package_label"]
	d.PackageDetails = draft["package_details"]
	if enabled := draft["nutrition_enabled"]; enabled != "" && enabled != "1" {
		invalid = true
	}
	if draft["nutrition_enabled"] == "1" {
		n := &DemoNutrition{ServingLabel: draft["nutrition_serving"]}
		d.Nutrition = n
		for key, dest := range map[string]*int64{"nutrition_energy": &n.EnergyKcal, "nutrition_sodium": &n.SodiumMG} {
			value, err := parseNutritionInteger(draft[key])
			*dest = value
			invalid = invalid || err != nil
		}
		for key, dest := range map[string]*int64{"nutrition_fat": &n.FatTenths, "nutrition_carbs": &n.CarbohydrateTenths, "nutrition_protein": &n.ProteinTenths} {
			value, err := parseNutritionTenths(draft[key])
			*dest = value
			invalid = invalid || err != nil
		}
	}
	if invalid {
		return d, draft, true, ErrInvalid
	}
	if err := validateProductDetails(&d); err != nil {
		return d, draft, true, err
	}
	return d, draft, true, nil
}

func parseNutritionInteger(raw string) (int64, error) {
	if raw == "" || len(raw) > 7 {
		return 0, ErrInvalid
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, ErrInvalid
		}
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, ErrInvalid
	}
	return v, nil
}
func parseNutritionTenths(raw string) (int64, error) {
	parts := strings.Split(raw, ".")
	if len(parts) > 2 {
		return 0, ErrInvalid
	}
	whole, err := parseNutritionInteger(parts[0])
	if err != nil {
		return 0, err
	}
	value := whole * 10
	if len(parts) == 2 {
		if len(parts[1]) != 1 || parts[1][0] < '0' || parts[1][0] > '9' {
			return 0, ErrInvalid
		}
		value += int64(parts[1][0] - '0')
	}
	return value, nil
}

// DetailValue preserves rejected raw drafts (including malformed numbers). A
// missing nutrition panel renders blank controls instead of invented zeroes.
func (f CatalogProductForm) DetailValue(key string) string {
	if f.DetailDraft != nil {
		return f.DetailDraft[key]
	}
	d := f.Details
	switch key {
	case "details_kind":
		if d.Kind == "" {
			return "unspecified"
		}
		return d.Kind
	case "details_body":
		return d.Body
	case "package_label":
		return d.PackageLabel
	case "package_details":
		return d.PackageDetails
	}
	n := d.Nutrition
	if n == nil {
		return ""
	}
	switch key {
	case "nutrition_enabled":
		return "1"
	case "nutrition_serving":
		return n.ServingLabel
	case "nutrition_energy":
		return fmt.Sprint(n.EnergyKcal)
	case "nutrition_fat":
		return n.FatGrams()
	case "nutrition_carbs":
		return n.CarbohydrateGrams()
	case "nutrition_protein":
		return n.ProteinGrams()
	case "nutrition_sodium":
		return fmt.Sprint(n.SodiumMG)
	}
	return ""
}

func (a *App) showProductExamples(w http.ResponseWriter, r *http.Request) {
	a.productExamplesView(w, r, "", nil)
}
func (a *App) productExamplesView(w http.ResponseWriter, r *http.Request, message string, problem error) {
	if !a.config.DemoMode {
		http.NotFound(w, r)
		return
	}
	v, ok := a.view(w, r)
	if !ok || !a.guard(w, r, v.Session) {
		return
	}
	ws, err := a.store.ProductExamples()
	if err != nil {
		if !errors.Is(err, ErrProductExamples) {
			a.fail(w, err)
			return
		}
		ws.Error = err.Error()
	}
	ws.CommandKey = token()
	if problem != nil {
		v.Error = problem.Error()
		if key := r.PostForm.Get("command_key"); len(key) >= 16 && len(key) <= 150 {
			ws.CommandKey = key
		}
	}
	v.Title = "Nonfood demo examples"
	v.Section = "catalog-examples"
	v.Message = message
	v.ProductExamples = &ws
	a.render(w, r, v, http.StatusOK)
}
func (a *App) createProductExamples(w http.ResponseWriter, r *http.Request) {
	if !a.config.DemoMode {
		http.NotFound(w, r)
		return
	}
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	var err error
	if r.PostForm.Get("confirm") != "1" {
		err = ErrInvalid
	} else {
		err = a.store.CreateProductExamples(r.PostForm.Get("command_key"), r.PostForm.Get("quote"))
	}
	if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrProductExamples) {
		a.fail(w, err)
		return
	}
	if err == nil {
		redirect(w, r, "/manager/catalog/examples")
		return
	}
	a.productExamplesView(w, r, "", err)
}
