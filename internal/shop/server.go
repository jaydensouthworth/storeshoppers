package shop

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"instore-shopper/web"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Origin, ManagerPassword string
	SecureCookies, DemoMode bool
	DemoBackupMaxBytes      int64
}
type App struct {
	images       imagePreviewCache
	store        *Store
	config       Config
	templates    *template.Template
	mux          *http.ServeMux
	mu           sync.Mutex
	failures     int
	blockedUntil time.Time
	requestGate  sync.RWMutex
	resetPending atomic.Bool
}
type View struct {
	Images                                                   *ImageWorkspace
	CartDraft                                                *ManagerDraft
	ProductPicker                                            *ProductPicker
	ProductPage                                              *ProductDetailPage
	ProductExamples                                          *ProductExamplesWorkspace
	CatalogDetails                                           ProductDetails
	CatalogDetailsDraft                                      map[string]string
	FeaturedCount                                            int
	FeaturedOnly                                             bool
	Promotions                                               *PromotionWorkspace
	WeeklySales, FeaturedProducts                            []Product
	SaleCount                                                int
	SalesOnly                                                bool
	Shoppers                                                 *ShoppersWorkspace
	OrderCatalogQuote                                        string
	OrderOverrideDraft                                       map[string]string
	OrderCommandKey                                          string
	CatalogPage                                              CatalogPage
	StockWorkspace                                           *StockWorkspace
	CatalogFilters                                           CatalogFilters
	BasketDraft, StockDraft                                  *ManagerDraft
	DemoPasswordHint                                         bool
	ManagerTab                                               string
	Baskets                                                  []Basket
	ManagedBasket                                            Basket
	BasketEvents                                             []BasketEvent
	Instructions                                             string
	Title, Section, Search, Category, Message, Error         string
	Session                                                  Session
	Manager, ManagerEnabled, DemoMode                        bool
	Products                                                 []Product
	CatalogProducts                                          []Product
	Categories, Types                                        []Taxonomy
	CatalogEvents                                            []CatalogEvent
	CatalogDraft                                             *Product
	Basket                                                   Basket
	Orders                                                   []Order
	Order                                                    Order
	Adjustments                                              []Adjustment
	ProductCount, CategoryCount, Units, LowStock, OpenOrders int64
}

func New(store *Store, cfg Config) (*App, error) {
	if cfg.DemoBackupMaxBytes < 0 {
		return nil, errors.New("DEMO_BACKUP_MAX_BYTES must be positive")
	}
	u, e := url.Parse(cfg.Origin)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("APP_ORIGIN must be an http(s) origin without a path")
	}

	host := u.Hostname()
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if cfg.ManagerPassword != "" && !loopback {
		if u.Scheme != "https" {
			return nil, errors.New("manager access requires an HTTPS public origin; leave MANAGER_PASSWORD unset for an HTTP storefront demo")
		}
		if !cfg.DemoMode && (len(cfg.ManagerPassword) < 24 || cfg.ManagerPassword == "local-demo-only") {
			return nil, errors.New("public manager access requires a unique password of at least 24 characters")
		}
	}
	if u.Scheme == "https" {
		cfg.SecureCookies = true
	}
	if !cfg.DemoMode && cfg.ManagerPassword != "" && len(cfg.ManagerPassword) < 12 {
		return nil, errors.New("MANAGER_PASSWORD must contain at least 12 characters")
	}
	tmpl, e := template.New("").Funcs(template.FuncMap{"lineWorkflow": lineWorkflow, "catalogForm": catalogForm, "catalogTaxonomyPanel": catalogTaxonomyPanel, "newCatalogProduct": newCatalogProduct, "illustrations": catalogIllustrations, "money": Money, "nextStatus": func(s string) string {
		return map[string]string{"Placed": "Picking", "Picking": "Ready", "Ready": "Completed"}[s]
	}, "eqInt": func(a, b int64) bool { return a == b }}).ParseFS(web.Files, "templates/*.html")
	if e != nil {
		return nil, e
	}
	a := &App{store: store, config: cfg, templates: tmpl, mux: http.NewServeMux(), images: imagePreviewCache{entries: make(map[string]ImagePreview), decode: make(chan struct{}, 1)}}
	statics, _ := fs.Sub(web.Files, "static")
	a.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(statics))))
	a.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	a.mux.HandleFunc("GET /{$}", a.showStore)
	a.mux.HandleFunc("GET /products/{id}", a.showProduct)
	a.mux.HandleFunc("GET /cart", a.showCart)
	a.mux.HandleFunc("POST /cart", a.changeCart)
	a.mux.HandleFunc("POST /cart/renew", a.renewCart)
	a.mux.HandleFunc("GET /manager/baskets", a.showBaskets)
	a.mux.HandleFunc("GET /manager/baskets/{id}", a.showBasket)
	a.mux.HandleFunc("POST /manager/baskets/practice", a.createPracticeBaskets)
	a.mux.HandleFunc("POST /manager/baskets/{id}/items", a.managerChangeBasket)
	a.mux.HandleFunc("POST /manager/baskets/{id}/renew", a.managerRenewBasket)
	a.mux.HandleFunc("POST /checkout", a.checkout)
	a.mux.HandleFunc("GET /orders", a.showOrders)
	a.mux.HandleFunc("GET /orders/{id}", a.showOrder)
	a.mux.HandleFunc("GET /orders/{id}/status", a.showOrder)
	a.mux.HandleFunc("GET /manager/login", a.showLogin)
	a.mux.HandleFunc("POST /manager/login", a.login)
	a.mux.HandleFunc("POST /manager/logout", a.logout)
	a.mux.HandleFunc("GET /manager", a.showManager)
	a.mux.HandleFunc("GET /manager/orders", a.showManager)
	a.mux.HandleFunc("GET /manager/shoppers", a.showShoppers)
	a.mux.HandleFunc("POST /manager/shoppers/orders/{id}", a.changeShopper)
	a.mux.HandleFunc("GET /manager/stock", a.showStock)

	a.mux.HandleFunc("GET /manager/promotions", a.showPromotions)
	a.mux.HandleFunc("GET /manager/promotions/new", a.showPromotions)
	a.mux.HandleFunc("GET /manager/promotions/examples", a.showPromotions)
	a.mux.HandleFunc("POST /manager/promotions/examples", a.createExampleSales)
	a.mux.HandleFunc("GET /manager/promotions/{id}", a.showPromotions)
	a.mux.HandleFunc("POST /manager/promotions", a.savePromotion)
	a.mux.HandleFunc("POST /manager/promotions/{id}", a.savePromotion)
	a.mux.HandleFunc("POST /manager/promotions/{id}/cancel", a.cancelPromotion)
	a.mux.HandleFunc("GET /manager/featured", a.showPromotions)
	a.mux.HandleFunc("POST /manager/featured/{id}", a.setFeatured)
	a.mux.HandleFunc("GET /manager/catalog", a.showCatalog)
	a.mux.HandleFunc("GET /media/products/{hash}/{variant}", a.serveProductImage)
	a.mux.HandleFunc("GET /manager/catalog/products/{id}/image", a.showProductImage)
	a.mux.HandleFunc("POST /manager/catalog/products/{id}/image/preview", a.uploadProductImage)
	a.mux.HandleFunc("GET /manager/catalog/products/{id}/image/preview/{token}", a.showImagePreview)
	a.mux.HandleFunc("POST /manager/catalog/products/{id}/image/confirm", a.confirmProductImage)
	a.mux.HandleFunc("POST /manager/catalog/products/{id}/image/illustration", a.useProductIllustration)
	a.mux.HandleFunc("GET /manager/catalog/examples", a.showProductExamples)
	a.mux.HandleFunc("POST /manager/catalog/examples", a.createProductExamples)
	a.mux.HandleFunc("POST /manager/catalog/products", a.saveCatalogProduct)
	a.mux.HandleFunc("POST /manager/catalog/products/{id}", a.saveCatalogProduct)
	a.mux.HandleFunc("POST /manager/catalog/products/{id}/archive", a.archiveCatalogProduct)
	a.mux.HandleFunc("POST /manager/catalog/products/{id}/restore", a.restoreCatalogProduct)
	a.mux.HandleFunc("POST /manager/catalog/{kind}", a.saveCatalogTaxonomy)
	a.mux.HandleFunc("POST /manager/catalog/{kind}/{id}", a.saveCatalogTaxonomy)
	a.mux.HandleFunc("POST /manager/catalog/{kind}/{id}/archive", a.archiveCatalogTaxonomy)
	a.mux.HandleFunc("POST /manager/catalog/{kind}/{id}/restore", a.restoreCatalogTaxonomy)
	a.mux.HandleFunc("POST /manager/orders/{id}/override", a.overrideOrder)
	a.mux.HandleFunc("POST /manager/orders/{id}/lines/{line_id}/pick", a.recordWorkingLinePicked)
	a.mux.HandleFunc("GET /manager/orders/{id}/products", a.searchOrderProducts)
	a.mux.HandleFunc("GET /manager/baskets/{id}/products", a.searchBasketProducts)
	a.mux.HandleFunc("GET /manager/orders/{id}", a.showPicking)
	a.mux.HandleFunc("POST /manager/orders/{id}/items/{product_id}", a.recordPicked)
	a.mux.HandleFunc("POST /manager/inventory", a.inventory)
	a.mux.HandleFunc("POST /manager/orders/{id}/advance", a.advance)
	if cfg.DemoMode {
		a.mux.HandleFunc("GET /manager/demo/reset", a.showDemoReset)
		a.mux.HandleFunc("POST /manager/demo/reset", a.performDemoReset)
	}
	return a, nil
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	origin, _ := url.Parse(a.config.Origin)
	if r.Host != origin.Host {
		http.Error(w, "Unrecognized host", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost {
		if v := r.Header.Get("Origin"); v != "" && v != a.config.Origin {
			http.Error(w, "Cross-origin request rejected", http.StatusForbidden)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Cross-site request rejected", http.StatusForbidden)
			return
		}
	}
	if !strings.HasPrefix(r.URL.Path, "/static/") {
		w.Header().Set("Cache-Control", "no-store")
	}
	// Reset POSTs perform their own read-lease validation before requesting
	// maintenance. Parsing an untrusted request body must never hold that gate.
	if a.config.DemoMode && !(r.Method == http.MethodPost && r.URL.Path == demoResetPath) {
		release, err := a.acquireRequestGate(r.Context(), false)
		if err != nil {
			w.Header().Set("Retry-After", "3")
			http.Error(w, "The demo is busy or being reset. Wait a moment and try again.", http.StatusServiceUnavailable)
			return
		}
		defer release()
	}
	if strings.HasPrefix(r.URL.Path, "/static/") {
		a.mux.ServeHTTP(w, r)
		return
	}
	a.serveCompatible(w, r)
}
func (a *App) session(w http.ResponseWriter, r *http.Request) (Session, error) {
	id := ""
	if c, e := r.Cookie("shop_session"); e == nil {
		id = c.Value
	}
	s, e := a.store.Session(id)
	if e != nil {
		return s, e
	}
	if s.ID != id {
		http.SetCookie(w, &http.Cookie{Name: "shop_session", Value: s.ID, Path: "/", MaxAge: 86400, HttpOnly: true, Secure: a.config.SecureCookies, SameSite: http.SameSiteLaxMode})
	}
	return s, nil
}
func (a *App) view(w http.ResponseWriter, r *http.Request) (View, bool) {
	s, e := a.session(w, r)
	if e != nil {
		a.fail(w, e)
		return View{}, false
	}
	return a.viewForSession(w, s)
}
func (a *App) viewForSession(w http.ResponseWriter, s Session) (View, bool) {
	b, e := a.store.Basket(s.ID)
	if e != nil {
		a.fail(w, e)
		return View{}, false
	}
	// Expiry reconciliation may advance the basket revision. Use the same
	// displayed revision for subsequent customer forms.
	s.Revision = b.Revision
	return View{DemoPasswordHint: a.config.DemoMode && a.config.ManagerPassword == "password", Session: s, Basket: b, Manager: a.config.ManagerPassword != "" && s.ManagerUntil > time.Now().Unix(), ManagerEnabled: a.config.ManagerPassword != "", DemoMode: a.config.DemoMode}, true
}
func (a *App) render(w http.ResponseWriter, r *http.Request, v View, status int) {
	name := "layout"
	if r.Header.Get("HX-Request") == "true" {
		name = "workspace"
	}
	a.renderNamed(w, r, name, v, status)
}
func (a *App) renderNamed(w http.ResponseWriter, r *http.Request, name string, v View, status int) {
	var b boundedResponseBody
	if e := a.templates.ExecuteTemplate(&b, name, v); e != nil {
		a.fail(w, e)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Add("Vary", "HX-Request")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}
func (a *App) fail(w http.ResponseWriter, e error) {
	log.Printf("request error: %v", e)
	if errors.Is(e, ErrSchemaIncompatible) {
		schemaUnavailable(w)
		return
	}
	http.Error(w, "Something went wrong. Please try again.", 500)
}
func (a *App) form(w http.ResponseWriter, r *http.Request) (Session, bool) {
	return a.formWithLimit(w, r, 8192)
}
func (a *App) formWithLimit(w http.ResponseWriter, r *http.Request, limit int64) (Session, bool) {
	var s Session
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if e := r.ParseForm(); e != nil {
		http.Error(w, "Invalid or oversized form", 400)
		return s, false
	}
	cookie, cookieErr := r.Cookie("shop_session")
	if cookieErr != nil {
		w.Header().Set("X-Shop-Error", "csrf-expired")
		http.Error(w, "This session changed or expired. Refresh the page before submitting again.", http.StatusForbidden)
		return s, false
	}
	s, e := a.store.existingFormSession(cookie.Value)
	if errors.Is(e, ErrNotFound) {
		w.Header().Set("X-Shop-Error", "csrf-expired")
		http.Error(w, "This session changed or expired. Refresh the page before submitting again.", http.StatusForbidden)
		return s, false
	}
	if e != nil {
		a.fail(w, e)
		return s, false
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(s.CSRF)) != 1 {
		w.Header().Set("X-Shop-Error", "csrf-expired")
		if cookie, err := r.Cookie("shop_session"); err == nil && cookie.Value == s.ID {
			w.Header().Set("X-Shop-CSRF", s.CSRF)
		}
		http.Error(w, "Your form expired. Your change was not saved. Review your values and submit again, or refresh if this session changed.", 403)
		return s, false
	}
	return s, true
}
func (a *App) guard(w http.ResponseWriter, r *http.Request, s Session) bool {
	if a.config.ManagerPassword == "" || s.ManagerUntil <= time.Now().Unix() {
		if r.Method == http.MethodPost && r.Header.Get("HX-Request") == "true" {
			w.Header().Set("X-Shop-Error", "manager-expired")
			http.Error(w, "Manager access expired. This change was not saved. Sign in again, then review and submit your form.", http.StatusForbidden)
			return false
		}
		redirect(w, r, "/manager/login")
		return false
	}
	return true
}
func redirect(w http.ResponseWriter, r *http.Request, path string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", path)
		w.WriteHeader(200)
		return
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}
func num(v string) (int64, error) { return strconv.ParseInt(v, 10, 64) }
func (a *App) populateStore(v *View) error {
	var err error
	if v.Categories, err = a.store.Taxonomies("category", false); err != nil {
		return err
	}
	v.CategoryCount = int64(len(v.Categories))
	all, err := a.store.Products("", "")
	if err != nil {
		return err
	}
	v.ProductCount = int64(len(all))
	v.Products = nil
	for _, p := range all {
		if p.OnSale() {
			v.SaleCount++
			if len(v.WeeklySales) < 3 {
				v.WeeklySales = append(v.WeeklySales, p)
			}
		}
		if p.Featured && !p.Archived && p.SaleUnit == "each" {
			v.FeaturedCount++
		}
		if v.FeaturedOnly && (!p.Featured || p.SaleUnit != "each") {
			continue
		}
		if v.SalesOnly && !p.OnSale() {
			continue
		}
		if v.Category != "" && p.Category != v.Category {
			continue
		}
		if v.Search != "" && !strings.Contains(strings.ToLower(p.Name+" "+p.Description), strings.ToLower(v.Search)) {
			continue
		}
		v.Products = append(v.Products, p)
	}
	// The circular already presents these products. Keep featured discovery and
	// filters intact, but use the separate shelf for additional featured picks.
	circularIDs := make(map[int64]bool, len(v.WeeklySales))
	for _, p := range v.WeeklySales {
		circularIDs[p.ID] = true
	}
	for _, p := range all {
		if p.Featured && !p.Archived && p.SaleUnit == "each" && !circularIDs[p.ID] {
			v.FeaturedProducts = append(v.FeaturedProducts, p)
			if len(v.FeaturedProducts) == 4 {
				break
			}
		}
	}
	return nil
}
func (a *App) showStore(w http.ResponseWriter, r *http.Request) {
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	v.Title = "The daily shop"
	v.Section = "store"
	v.Search = strings.TrimSpace(r.URL.Query().Get("q"))
	v.Category = r.URL.Query().Get("category")
	v.SalesOnly = r.URL.Query().Get("sales") == "1"
	v.FeaturedOnly = r.URL.Query().Get("featured") == "1"
	if len(v.Search) > 100 {
		v.Search = v.Search[:100]
	}
	if e := a.populateStore(&v); e != nil {
		a.fail(w, e)
		return
	}
	a.render(w, r, v, 200)
}
func (a *App) showCart(w http.ResponseWriter, r *http.Request) {
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	v.Title = "Your basket"
	v.Section = "cart"
	a.render(w, r, v, 200)
}
func (a *App) changeCart(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok {
		return
	}
	pid, e1 := num(r.PostForm.Get("product_id"))
	qty, e2 := num(r.PostForm.Get("quantity"))
	revision, e3 := num(r.PostForm.Get("revision"))
	var e error
	if e1 != nil || e2 != nil || e3 != nil {
		e = ErrInvalid
	} else {
		e = a.store.SetCartVersion(s.ID, pid, qty, r.PostForm.Get("mode") == "add", revision)
	}
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	v.Section = r.PostForm.Get("return")
	if v.Section != "cart" && v.Section != "product" {
		v.Section = "store"
	}
	v.Title = "Neighborhood Market"
	v.Search, v.Category, v.SalesOnly, v.FeaturedOnly = storefrontContext(r.PostForm)
	if e != nil {
		if !errors.Is(e, ErrInvalid) && !errors.Is(e, ErrStock) && !errors.Is(e, ErrNotFound) && !errors.Is(e, ErrUnavailable) && !errors.Is(e, ErrConflict) {
			a.fail(w, e)
			return
		}
		v.Error = e.Error()
		v.CartDraft = managerDraft(r, "cart")
		if errors.Is(e, ErrConflict) {
			w.Header().Set("X-Shop-Error", "stale-version")
		}
	} else {
		v.Message = "Basket updated."
	}
	if v.Section == "store" {
		if e = a.populateStore(&v); e != nil {
			a.fail(w, e)
			return
		}
	}
	if v.Section == "product" {
		if err := a.populateProductPage(&v, pid); err != nil {
			if errors.Is(err, ErrNotFound) {
				a.productUnavailable(w, r, v)
			} else {
				a.fail(w, err)
			}
			return
		}
	}
	// Return 200 for a rendered validation state so HTMX swaps and announces it.
	if r.Header.Get("HX-Request") != "true" && v.Error == "" {
		path := "/"
		if v.Section == "cart" {
			path = "/cart"
		} else if v.Section == "product" {
			path = productPageURL(pid, v.Search, v.Category, v.SalesOnly, v.FeaturedOnly)
		} else {
			values := url.Values{}
			if v.Search != "" {
				values.Set("q", v.Search)
			}
			if v.Category != "" {
				values.Set("category", v.Category)
			}
			if v.SalesOnly {
				values.Set("sales", "1")
			}
			if v.FeaturedOnly {
				values.Set("featured", "1")
			}
			if len(values) > 0 {
				path += "?" + values.Encode()
			}
		}
		redirect(w, r, path)
		return
	}
	a.render(w, r, v, 200)
}
func (a *App) checkout(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok {
		return
	}
	rev, e := num(r.PostForm.Get("revision"))
	var id int64
	if e == nil {
		id, e = a.store.CheckoutWithInstructions(s.ID, r.PostForm.Get("checkout_key"), rev, r.PostForm.Get("quote"), r.PostForm.Get("instructions"))
	} else {
		e = ErrInvalid
	}
	if e == nil {
		redirect(w, r, fmt.Sprintf("/orders/%d", id))
		return
	}
	if !errors.Is(e, ErrConflict) && !errors.Is(e, ErrEmpty) && !errors.Is(e, ErrStock) && !errors.Is(e, ErrInvalid) && !errors.Is(e, ErrQuote) && !errors.Is(e, ErrUnavailable) && !errors.Is(e, ErrHold) {
		a.fail(w, e)
		return
	}
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	v.Instructions = r.PostForm.Get("instructions")
	v.Title = "Review your basket"
	v.Section = "cart"
	v.Error = e.Error()
	a.render(w, r, v, 200)
}
func (a *App) showOrders(w http.ResponseWriter, r *http.Request) {
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	var e error
	v.Orders, e = a.store.Orders(v.Session.ID, false)
	if e != nil {
		a.fail(w, e)
		return
	}
	v.Title = "Your demo orders"
	v.Section = "orders"
	a.render(w, r, v, 200)
}
func (a *App) showOrder(w http.ResponseWriter, r *http.Request) {
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	id, e := num(r.PathValue("id"))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	v.Order, e = a.store.Order(id, v.Session.ID, v.Manager && !a.config.DemoMode)
	if errors.Is(e, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if e != nil {
		a.fail(w, e)
		return
	}
	v.Title = v.Order.Reference
	v.Section = "order"
	if strings.HasSuffix(r.URL.Path, "/status") && r.Header.Get("HX-Request") == "true" {
		a.renderNamed(w, r, "order-status", v, 200)
		return
	}
	a.render(w, r, v, 200)
}
func (a *App) showLogin(w http.ResponseWriter, r *http.Request) {
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	if v.Manager {
		redirect(w, r, "/manager")
		return
	}
	v.Title = "Manager demo access"
	v.Section = "login"
	a.render(w, r, v, 200)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok {
		return
	}
	a.mu.Lock()
	blocked := time.Now().Before(a.blockedUntil)
	provided := sha256.Sum256([]byte(r.PostForm.Get("password")))
	want := sha256.Sum256([]byte(a.config.ManagerPassword))
	valid := !blocked && a.config.ManagerPassword != "" && subtle.ConstantTimeCompare(provided[:], want[:]) == 1
	if valid {
		a.failures = 0
	} else if !blocked {
		a.failures++
		if a.failures >= 5 {
			a.blockedUntil = time.Now().Add(time.Minute)
			a.failures = 0
		}
	}
	a.mu.Unlock()
	if valid {
		if e := a.store.Manager(s.ID, true); e != nil {
			a.fail(w, e)
			return
		}
		redirect(w, r, "/manager")
		return
	}
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	v.Title = "Manager demo access"
	v.Section = "login"
	v.Error = "Could not sign in. Check the local demo password or wait a minute after repeated attempts."
	a.render(w, r, v, 200)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok {
		return
	}
	if e := a.store.Manager(s.ID, false); e != nil {
		a.fail(w, e)
		return
	}
	redirect(w, r, "/")
}
func (a *App) managerView(w http.ResponseWriter, r *http.Request, message string, e error) {
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	if !a.guard(w, r, v.Session) {
		return
	}
	v.Title = "Store workspace"
	v.ManagerTab = "orders"
	if r.URL.Path == "/manager/stock" || r.URL.Path == "/manager/inventory" {
		v.ManagerTab = "stock"
	}
	v.Search = managerSearch(r)
	v.Section = "manager"
	v.Message = message
	if e != nil {
		v.Error = e.Error()
	}
	var err error
	if v.Products, err = a.store.Products("", ""); err != nil {
		a.fail(w, err)
		return
	}
	if v.Orders, err = a.store.OrdersSearch(v.Session.ID, !a.config.DemoMode, v.Search); err != nil {
		a.fail(w, err)
		return
	}
	if v.Adjustments, err = a.store.Adjustments(); err != nil {
		a.fail(w, err)
		return
	}
	if v.Categories, err = a.store.Taxonomies("category", false); err != nil {
		a.fail(w, err)
		return
	}
	v.ProductCount = int64(len(v.Products))
	v.CategoryCount = int64(len(v.Categories))
	for _, p := range v.Products {
		if p.SaleUnit == "each" {
			v.Units += p.Stock
			if p.Stock < 8 {
				v.LowStock++
			}
		}
	}
	for _, o := range v.Orders {
		if o.Status != "Completed" {
			v.OpenOrders++
		}
	}
	if e != nil && r.Method == http.MethodPost && r.URL.Path == "/manager/inventory" {
		v.StockDraft = managerDraft(r, "stock")
	}
	if v.ManagerTab == "stock" && v.Search != "" {
		filtered := []Product{}
		needle := strings.ToLower(v.Search)
		for _, p := range v.Products {
			if strings.Contains(strings.ToLower(p.Name+" "+p.SKU+" "+p.Category), needle) || (v.StockDraft != nil && v.StockDraft.ProductID == p.ID) {
				filtered = append(filtered, p)
			}
		}
		v.Products = filtered
	}
	a.render(w, r, v, 200)
}
func (a *App) showManager(w http.ResponseWriter, r *http.Request) { a.managerView(w, r, "", nil) }
func (a *App) advance(w http.ResponseWriter, r *http.Request) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	id, err := num(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	version, parseErr := num(r.PostForm.Get("order_version"))
	if parseErr != nil || version < 1 {
		if _, scopeErr := a.store.Order(id, session.ID, !a.config.DemoMode); scopeErr != nil {
			err = scopeErr
		} else {
			err = ErrInvalid
		}
	} else {
		err = a.store.AdvanceVersioned(id, r.PostForm.Get("status"), version, session.ID, !a.config.DemoMode)
	}
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrIncomplete) {
		a.fail(w, err)
		return
	}
	if err == nil && r.Header.Get("HX-Request") != "true" {
		redirect(w, r, fmt.Sprintf("/manager/orders/%d", id)+searchQuery(managerSearch(r)))
		return
	}
	message := ""
	if err == nil {
		message = "Order status updated."
	}
	a.pickingView(w, r, id, message, err)
}

func (a *App) showPicking(w http.ResponseWriter, r *http.Request) {
	id, err := num(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.pickingView(w, r, id, "", nil)
}
func (a *App) pickingView(w http.ResponseWriter, r *http.Request, id int64, message string, problem error) {
	v, ok := a.view(w, r)
	if !ok || !a.guard(w, r, v.Session) {
		return
	}
	var err error
	v.Order, err = a.store.Order(id, v.Session.ID, !a.config.DemoMode)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}

	if r.Method == http.MethodPost && r.Header.Get("HX-Request") == "true" {
		target := fmt.Sprintf("/manager/orders/%d", id) + searchQuery(managerSearch(r))
		current, _ := url.Parse(r.Header.Get("HX-Current-URL"))
		if current == nil || current.RequestURI() != target {
			w.Header().Set("HX-Push-Url", target)
		}
	}
	v.Products, err = a.store.Products("", "")
	if err != nil {
		a.fail(w, err)
		return
	}
	v.OrderCatalogQuote = orderCatalogQuote(v.Products)
	v.OrderCommandKey = token()
	if problem != nil && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/override") {
		v.OrderOverrideDraft = make(map[string]string)
		for _, field := range []string{"form_id", "product_id", "replacement_id", "quantity", "reason", "disposition", "remainder", "product_q", "picker_context", "picker_line_id"} {
			value := r.PostForm.Get(field)
			if len(value) > 1024 {
				value = value[:1024]
			}
			v.OrderOverrideDraft[field] = value
		}
	}
	v.Title = "Pick " + v.Order.Reference
	v.Section = "picking"
	v.ManagerTab = "orders"
	v.Search = managerSearch(r)
	v.Message = message
	if problem != nil {
		v.Error = problem.Error()
		if errors.Is(problem, ErrConflict) {
			w.Header().Set("X-Shop-Error", "stale-version")
		}
	}
	if a.handleProductPickerError(w, r, a.populateProductPicker(r, &v, false)) {
		return
	}
	if a.renderProductPicker(w, r, v) {
		return
	}
	a.render(w, r, v, 200)
}
func (a *App) recordPicked(w http.ResponseWriter, r *http.Request) {
	session, ok := a.form(w, r)
	if !ok || !a.guard(w, r, session) {
		return
	}
	id, e1 := num(r.PathValue("id"))
	pid, e2 := num(r.PathValue("product_id"))
	picked, e3 := num(r.PostForm.Get("picked"))
	version, e4 := num(r.PostForm.Get("version"))
	if e1 != nil {
		http.NotFound(w, r)
		return
	}
	var err error
	if e2 != nil || e3 != nil || e4 != nil {
		err = ErrInvalid
	} else {
		err = a.store.RecordPicked(id, pid, picked, version, session.ID, !a.config.DemoMode)
	}
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) {
		a.fail(w, err)
		return
	}
	if err == nil && r.Header.Get("HX-Request") != "true" {
		redirect(w, r, fmt.Sprintf("/manager/orders/%d", id)+searchQuery(managerSearch(r)))
		return
	}
	message := ""
	if err == nil {
		message = "Picked quantity saved."
	}
	a.pickingView(w, r, id, message, err)
}
