package shop

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func imageUploadRequest(t *testing.T, a *App, s Session, raw []byte, fields url.Values, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for key, values := range fields {
		for _, v := range values {
			if err := mw.WriteField(key, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	f, err := mw.CreateFormFile("image", "not-stored-private-name.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(raw); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, testOrigin+"/manager/catalog/products/1/image/preview", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Origin", testOrigin)
	r.AddCookie(&http.Cookie{Name: "shop_session", Value: s.ID})
	if htmx {
		r.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func imagePreviewToken(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	m := regexp.MustCompile(`name="preview_token" value="([a-f0-9]{64})"`).FindStringSubmatch(w.Body.String())
	if len(m) != 2 {
		t.Fatalf("preview missing: %d %s", w.Code, w.Body.String())
	}
	return m[1]
}

func TestHTTPImagePreviewConfirmMediaAndRecovery(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner := pickingLogin(t, a, testSession(t, s, ""))
	im, raw := imageFixture(t, 50)
	p := testProduct(t, s, 1)
	fields := url.Values{"csrf": {owner.CSRF}, "catalog_version": {fmt.Sprint(p.CatalogVersion)}, "q": {"milk"}, "page": {"2"}}
	page := testRequest(t, a, http.MethodGet, "/manager/catalog/products/1/image?q=milk&page=2", owner, nil, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `enctype="multipart/form-data"`) || !strings.Contains(page.Body.String(), "q=milk") {
		t.Fatalf("editor %d %s", page.Code, page.Body.String())
	}
	w := imageUploadRequest(t, a, owner, raw, fields, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `id="workspace"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	key := imagePreviewToken(t, w)
	if testProduct(t, s, 1).ImageHash != "" {
		t.Fatal("preview published image")
	}
	stats, _ := s.ImageLibraryStats()
	if stats.Count != 0 {
		t.Fatal("preview persisted blob")
	}
	previewPath := "/manager/catalog/products/1/image/preview/" + key
	preview := testRequest(t, a, http.MethodGet, previewPath, owner, nil, nil)
	if preview.Code != 200 || !bytes.Equal(preview.Body.Bytes(), im.Master.Data) || preview.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("preview bytes/cache", preview.Code)
	}
	other := pickingLogin(t, a, testSession(t, s, ""))
	if got := testRequest(t, a, http.MethodGet, previewPath, other, nil, nil); got.Code != 404 {
		t.Fatal("cross-session preview", got.Code)
	}
	confirm := url.Values{"csrf": {owner.CSRF}, "preview_token": {key}, "confirm": {"public-demo-image"}, "q": {"milk"}, "page": {"2"}}
	path := "/manager/catalog/products/1/image/confirm"
	w = testRequest(t, a, http.MethodPost, path, owner, confirm, pickingHeaders(true))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Product image saved") {
		t.Fatal(w.Code, w.Body.String())
	}
	before := fingerprintTest(t, s.db)
	w = testRequest(t, a, http.MethodPost, path, owner, confirm, nil)
	if w.Code != 200 || fingerprintTest(t, s.db) != before {
		t.Fatal("confirm replay changed database")
	}
	mediaPath := "/media/products/" + im.SHA256 + "/thumb.jpg"
	media := testRequest(t, a, http.MethodGet, mediaPath, Session{}, nil, nil)
	if media.Code != 200 || !bytes.Equal(media.Body.Bytes(), im.Thumbnail.Data) || media.Header().Get("Content-Type") != "image/jpeg" || media.Header().Get("X-Content-Type-Options") != "nosniff" || media.Header().Get("Set-Cookie") != "" {
		t.Fatal("public image response", media.Code, media.Header())
	}
	conditional := testRequest(t, a, http.MethodGet, mediaPath, Session{}, nil, map[string]string{"If-None-Match": media.Header().Get("ETag")})
	if conditional.Code != 304 || conditional.Body.Len() != 0 {
		t.Fatal("conditional media", conditional.Code)
	}
	detail := testRequest(t, a, http.MethodGet, "/products/1", owner, nil, nil)
	if !strings.Contains(detail.Body.String(), "/media/products/"+im.SHA256+"/master.jpg") {
		t.Fatal("detail missing uploaded image")
	}
	if strings.Contains(detail.Body.String(), "not-stored-private-name") {
		t.Fatal("filename exposed")
	}
	// Restarts invalidate private preview tokens, while saved media remains.
	restarted := pickingApp(t, s, true)
	w = testRequest(t, restarted, http.MethodPost, path, owner, confirm, nil)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "preview expired") {
		t.Fatal("stale preview restart", w.Code, w.Body.String())
	}
}

func TestHTTPImageAuthorizationCSRFBoundsAndSingleDecoder(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner := pickingLogin(t, a, testSession(t, s, ""))
	_, raw := imageFixture(t, 55)
	fields := url.Values{"csrf": {owner.CSRF}, "catalog_version": {"1"}}
	if w := imageUploadRequest(t, a, Session{}, raw, fields, false); w.Code != 403 || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("anonymous upload", w.Code)
	}
	bad := url.Values{"csrf": {"bad"}, "catalog_version": {"1"}}
	before := fingerprintTest(t, s.db)
	if w := imageUploadRequest(t, a, owner, raw, bad, true); w.Code != 403 || w.Header().Get("X-Shop-CSRF") != owner.CSRF {
		t.Fatal("csrf upload", w.Code)
	}
	if fingerprintTest(t, s.db) != before {
		t.Fatal("bad CSRF changed quota or catalog")
	}
	oversized := url.Values{"csrf": {owner.CSRF}, "catalog_version": {"1"}, "a": {strings.Repeat("x", 4096)}, "b": {strings.Repeat("x", 4096)}}
	if w := imageUploadRequest(t, a, owner, raw, oversized, false); w.Code != 400 {
		t.Fatal("field budget", w.Code)
	}
	oversized = url.Values{"csrf": {owner.CSRF}, "catalog_version": {"1"}, strings.Repeat("n", 65): {"x"}}
	if w := imageUploadRequest(t, a, owner, raw, oversized, false); w.Code != 400 {
		t.Fatal("field name budget", w.Code)
	}
	w := imageUploadRequest(t, a, owner, []byte("<svg onload='bad()'>"), fields, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Image not accepted") {
		t.Fatal("specific HTMX rejection", w.Code, w.Body.String())
	}
	w = imageUploadRequest(t, a, owner, raw, fields, false)
	key := imagePreviewToken(t, w)
	a.images.decode <- struct{}{}
	w = imageUploadRequest(t, a, owner, raw, fields, false)
	if w.Code != 429 {
		t.Fatal("parallel upload decoder", w.Code)
	}
	confirm := url.Values{"csrf": {owner.CSRF}, "preview_token": {key}, "confirm": {"public-demo-image"}}
	w = testRequest(t, a, http.MethodPost, "/manager/catalog/products/1/image/confirm", owner, confirm, nil)
	if w.Code != 429 {
		t.Fatal("parallel confirmation decoder", w.Code)
	}
	<-a.images.decode
	if testProduct(t, s, 1).ImageHash != "" {
		t.Fatal("busy confirmation attached image")
	}
	// Existing-session CSRF rotation keeps a rejected multipart draft retryable.
	if err := s.Manager(owner.ID, true); err != nil {
		t.Fatal(err)
	}
	w = imageUploadRequest(t, a, owner, raw, fields, true)
	if w.Code != 403 || w.Header().Get("X-Shop-CSRF") == "" || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("expired-form recovery", w.Code, w.Header())
	}
}

type expireImageReader struct {
	r      io.Reader
	expire func()
	once   bool
}

func (r *expireImageReader) Read(p []byte) (int, error) {
	if !r.once {
		r.once = true
		r.expire()
	}
	return r.r.Read(p)
}
func TestHTTPImageExpiredSessionDuringInvalidBodyDoesNotMintIdentity(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner := pickingLogin(t, a, testSession(t, s, ""))
	r := httptest.NewRequest(http.MethodPost, testOrigin+"/manager/catalog/products/1/image/preview", nil)
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=bad")
	r.AddCookie(&http.Cookie{Name: "shop_session", Value: owner.ID})
	r.Body = io.NopCloser(&expireImageReader{r: strings.NewReader("malformed body"), expire: func() { testExec(t, s, `UPDATE sessions SET expires=1 WHERE id=?`, owner.ID) }})
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("expired upload minted identity", w.Code, w.Header())
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil || count != 1 {
		t.Fatal("unexpected session", count, err)
	}
}

func TestHTTPImagePreviewExpiryStaleCatalogAndConfirmation(t *testing.T) {
	s := newTestStore(t)
	a := pickingApp(t, s, true)
	owner := pickingLogin(t, a, testSession(t, s, ""))
	_, raw := imageFixture(t, 90)
	w := imageUploadRequest(t, a, owner, raw, url.Values{"csrf": {owner.CSRF}, "catalog_version": {"1"}}, false)
	key := imagePreviewToken(t, w)
	fields := url.Values{"csrf": {owner.CSRF}, "preview_token": {key}}
	path := "/manager/catalog/products/1/image/confirm"
	if w = testRequest(t, a, http.MethodPost, path, owner, fields, nil); w.Code != 400 {
		t.Fatal("missing confirmation", w.Code)
	}
	fields.Set("confirm", "public-demo-image")
	p := testProduct(t, s, 1)
	p.Name = "Changed after preview"
	if _, err := s.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	w = testRequest(t, a, http.MethodPost, path, owner, fields, nil)
	if w.Code != 409 || testProduct(t, s, 1).ImageHash != "" {
		t.Fatal("stale preview applied", w.Code)
	}
	a.images.mu.Lock()
	preview := a.images.entries[key]
	preview.Expires = time.Now().Add(-time.Second)
	a.images.entries[key] = preview
	a.images.mu.Unlock()
	w = testRequest(t, a, http.MethodPost, path, owner, fields, nil)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "preview expired") {
		t.Fatal("expired preview", w.Code)
	}
}
