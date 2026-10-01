package shop

import (
	"bytes"
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
	"time"
)

type Config struct {
	Origin, ManagerPassword string
	SecureCookies, DemoMode bool
}
type App struct {
	store        *Store
	config       Config
	templates    *template.Template
	mux          *http.ServeMux
	mu           sync.Mutex
	failures     int
	blockedUntil time.Time
}
type View struct {
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
	tmpl, e := template.New("").Funcs(template.FuncMap{"catalogForm": catalogForm, "catalogTaxonomyPanel": catalogTaxonomyPanel, "newCatalogProduct": newCatalogProduct, "illustrations": catalogIllustrations, "money": Money, "nextStatus": func(s string) string {
		return map[string]string{"Placed": "Picking", "Picking": "Ready", "Ready": "Completed"}[s]
	}, "eqInt": func(a, b int64) bool { return a == b }}).ParseFS(web.Files, "templates/*.html")
	if e != nil {
		return nil, e
	}
	a := &App{store: store, config: cfg, templates: tmpl, mux: http.NewServeMux()}
	statics, _ := fs.Sub(web.Files, "static")
	a.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(statics))))
	a.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	a.mux.HandleFunc("GET /{$}", a.showStore)
	a.mux.HandleFunc("GET /cart", a.showCart)
	a.mux.HandleFunc("POST /cart", a.changeCart)
	a.mux.HandleFunc("POST /checkout", a.checkout)
	a.mux.HandleFunc("GET /orders", a.showOrders)
	a.mux.HandleFunc("GET /orders/{id}", a.showOrder)
	a.mux.HandleFunc("GET /orders/{id}/status", a.showOrder)
	a.mux.HandleFunc("GET /manager/login", a.showLogin)
	a.mux.HandleFunc("POST /manager/login", a.login)
	a.mux.HandleFunc("POST /manager/logout", a.logout)
	a.mux.HandleFunc("GET /manager", a.showManager)
	a.mux.HandleFunc("GET /manager/catalog", a.showCatalog)
	a.mux.HandleFunc("POST /manager/catalog/products", a.saveCatalogProduct)
	a.mux.HandleFunc("POST /manager/catalog/products/{id}", a.saveCatalogProduct)
	a.mux.HandleFunc("POST /manager/catalog/products/{id}/archive", a.archiveCatalogProduct)
	a.mux.HandleFunc("POST /manager/catalog/products/{id}/restore", a.restoreCatalogProduct)
	a.mux.HandleFunc("POST /manager/catalog/{kind}", a.saveCatalogTaxonomy)
	a.mux.HandleFunc("POST /manager/catalog/{kind}/{id}", a.saveCatalogTaxonomy)
	a.mux.HandleFunc("POST /manager/catalog/{kind}/{id}/archive", a.archiveCatalogTaxonomy)
	a.mux.HandleFunc("POST /manager/catalog/{kind}/{id}/restore", a.restoreCatalogTaxonomy)
	a.mux.HandleFunc("GET /manager/orders/{id}", a.showPicking)
	a.mux.HandleFunc("POST /manager/orders/{id}/items/{product_id}", a.recordPicked)
	a.mux.HandleFunc("POST /manager/inventory", a.inventory)
	a.mux.HandleFunc("POST /manager/orders/{id}/advance", a.advance)
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
	a.mux.ServeHTTP(w, r)
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
	b, e := a.store.Basket(s.ID)
	if e != nil {
		a.fail(w, e)
		return View{}, false
	}
	return View{Session: s, Basket: b, Manager: a.config.ManagerPassword != "" && s.ManagerUntil > time.Now().Unix(), ManagerEnabled: a.config.ManagerPassword != "", DemoMode: a.config.DemoMode}, true
}
func (a *App) render(w http.ResponseWriter, r *http.Request, v View, status int) {
	name := "layout"
	if r.Header.Get("HX-Request") == "true" {
		name = "workspace"
	}
	a.renderNamed(w, r, name, v, status)
}
func (a *App) renderNamed(w http.ResponseWriter, r *http.Request, name string, v View, status int) {
	var b bytes.Buffer
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
	http.Error(w, "Something went wrong. Please try again.", 500)
}
func (a *App) form(w http.ResponseWriter, r *http.Request) (Session, bool) {
	s, e := a.session(w, r)
	if e != nil {
		a.fail(w, e)
		return s, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if e = r.ParseForm(); e != nil {
		http.Error(w, "Invalid or oversized form", 400)
		return s, false
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(s.CSRF)) != 1 {
		http.Error(w, "Your form expired. Refresh and try again.", 403)
		return s, false
	}
	return s, true
}
func (a *App) guard(w http.ResponseWriter, r *http.Request, s Session) bool {
	if a.config.ManagerPassword == "" || s.ManagerUntil <= time.Now().Unix() {
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
	if v.Products, err = a.store.Products(v.Search, v.Category); err != nil {
		return err
	}
	v.ProductCount = int64(len(v.Products))
	if v.Search != "" || v.Category != "" {
		all, err := a.store.Products("", "")
		if err != nil {
			return err
		}
		v.ProductCount = int64(len(all))
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
	var e error
	if e1 != nil || e2 != nil {
		e = ErrInvalid
	} else {
		e = a.store.SetCart(s.ID, pid, qty, r.PostForm.Get("mode") == "add")
	}
	v, ok := a.view(w, r)
	if !ok {
		return
	}
	v.Section = r.PostForm.Get("return")
	if v.Section != "cart" {
		v.Section = "store"
	}
	v.Title = "Neighborhood Market"
	v.Search = r.PostForm.Get("q")
	v.Category = r.PostForm.Get("category")
	if e != nil {
		if !errors.Is(e, ErrInvalid) && !errors.Is(e, ErrStock) && !errors.Is(e, ErrNotFound) && !errors.Is(e, ErrUnavailable) {
			a.fail(w, e)
			return
		}
		v.Error = e.Error()
	} else {
		v.Message = "Basket updated."
	}
	if v.Section == "store" {
		if e = a.populateStore(&v); e != nil {
			a.fail(w, e)
			return
		}
	}
	// Return 200 for a rendered validation state so HTMX swaps and announces it.
	if r.Header.Get("HX-Request") != "true" && v.Error == "" {
		path := "/"
		if v.Section == "cart" {
			path = "/cart"
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
		id, e = a.store.Checkout(s.ID, r.PostForm.Get("checkout_key"), rev, r.PostForm.Get("quote"))
	} else {
		e = ErrInvalid
	}
	if e == nil {
		redirect(w, r, fmt.Sprintf("/orders/%d", id))
		return
	}
	if !errors.Is(e, ErrConflict) && !errors.Is(e, ErrEmpty) && !errors.Is(e, ErrStock) && !errors.Is(e, ErrInvalid) && !errors.Is(e, ErrQuote) && !errors.Is(e, ErrUnavailable) {
		a.fail(w, e)
		return
	}
	v, ok := a.view(w, r)
	if !ok {
		return
	}
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
	if v.Orders, err = a.store.Orders(v.Session.ID, !a.config.DemoMode); err != nil {
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
	a.render(w, r, v, 200)
}
func (a *App) showManager(w http.ResponseWriter, r *http.Request) { a.managerView(w, r, "", nil) }
func (a *App) inventory(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	pid, e1 := num(r.PostForm.Get("product_id"))
	delta, e2 := num(r.PostForm.Get("delta"))
	ver, e3 := num(r.PostForm.Get("version"))
	var e error
	if e1 != nil || e2 != nil || e3 != nil {
		e = ErrInvalid
	} else {
		e = a.store.Adjust(pid, delta, ver, r.PostForm.Get("reason"))
	}
	if e != nil && !errors.Is(e, ErrInvalid) && !errors.Is(e, ErrConflict) && !errors.Is(e, ErrUnavailable) && !errors.Is(e, ErrNotFound) {
		a.fail(w, e)
		return
	}
	if e == nil && r.Header.Get("HX-Request") != "true" {
		redirect(w, r, "/manager")
		return
	}
	message := ""
	if e == nil {
		message = "Inventory adjusted. The change is recorded below."
	}
	a.managerView(w, r, message, e)
}
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
	err = a.store.AdvanceScoped(id, r.PostForm.Get("status"), session.ID, !a.config.DemoMode)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrIncomplete) {
		a.fail(w, err)
		return
	}
	if err == nil && r.Header.Get("HX-Request") != "true" {
		redirect(w, r, fmt.Sprintf("/manager/orders/%d", id))
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
		target := fmt.Sprintf("/manager/orders/%d", id)
		current, _ := url.Parse(r.Header.Get("HX-Current-URL"))
		if current == nil || current.Path != target {
			w.Header().Set("HX-Push-Url", target)
		}
	}
	v.Title = "Pick " + v.Order.Reference
	v.Section = "picking"
	v.Message = message
	if problem != nil {
		v.Error = problem.Error()
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
		redirect(w, r, fmt.Sprintf("/manager/orders/%d", id))
		return
	}
	message := ""
	if err == nil {
		message = "Picked quantity saved."
	}
	a.pickingView(w, r, id, message, err)
}
