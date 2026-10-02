package shop

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"instore-shopper/internal/productimage"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const maxImagePreviews = 8
const imagePreviewTTL = 10 * time.Minute

type ImagePreview struct {
	Token, SessionID   string
	ProductID, Version int64
	Expires            time.Time
	Image              productimage.Result
}
type ImageWorkspace struct {
	Product    Product
	Stats      ImageLibraryStats
	Preview    *ImagePreview
	CommandKey string
}
type imagePreviewCache struct {
	mu      sync.Mutex
	entries map[string]ImagePreview
	decode  chan struct{}
}

func (a *App) existingImageManager(w http.ResponseWriter, r *http.Request) (Session, bool) {
	cookie, err := r.Cookie("shop_session")
	if err != nil {
		http.Error(w, "Open the manager workspace before uploading an image.", http.StatusForbidden)
		return Session{}, false
	}
	s, err := a.store.existingFormSession(cookie.Value)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "This session expired. Sign in again before uploading.", http.StatusForbidden)
		} else {
			a.fail(w, err)
		}
		return Session{}, false
	}
	return s, a.guard(w, r, s)
}

func (a *App) imageView(w http.ResponseWriter, r *http.Request, preview *ImagePreview, message string, problem error, status int) {
	var v View
	var ok bool
	if r.Method == http.MethodPost {
		s, valid := a.existingImageManager(w, r)
		if !valid {
			return
		}
		v, ok = a.viewForSession(w, s)
	} else {
		v, ok = a.view(w, r)
	}
	if !ok || !a.guard(w, r, v.Session) {
		return
	}
	id, err := num(r.PathValue("id"))
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	p, err := scanProduct(a.store.db.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	stats, err := a.store.ImageLibraryStats()
	if err != nil {
		a.fail(w, err)
		return
	}
	v.Images = &ImageWorkspace{Product: p, Stats: stats, Preview: preview, CommandKey: token()}
	v.CatalogFilters = catalogFilters(r)
	v.Title = "Product image"
	v.Section = "catalog"
	v.ManagerTab = "image"
	v.Message = message
	if problem != nil {
		v.Error = problem.Error()
	}
	if r.Header.Get("HX-Request") == "true" && status >= 400 {
		status = http.StatusOK
	}
	a.render(w, r, v, status)
}
func (a *App) showProductImage(w http.ResponseWriter, r *http.Request) {
	a.imageView(w, r, nil, "", nil, http.StatusOK)
}

func (a *App) uploadProductImage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.existingImageManager(w, r); !ok {
		return
	}
	// Bound active request buffers and decoders before reading multipart bytes.
	select {
	case a.images.decode <- struct{}{}:
		defer func() { <-a.images.decode }()
	default:
		http.Error(w, "Another image is being prepared. Try again in a moment.", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, productimage.MaxInputBytes+(32<<10))
	reader, err := r.MultipartReader()
	if err != nil {
		a.imageView(w, r, nil, "", errors.New("Choose a JPEG or PNG file to preview."), http.StatusBadRequest)
		return
	}
	values := url.Values{}
	var raw []byte
	parts, fieldBytes := 0, 0
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			err = e
			break
		}
		parts++
		if parts > 16 {
			part.Close()
			err = ErrInvalid
			break
		}
		name := part.FormName()
		if len(name) < 1 || len(name) > 64 {
			part.Close()
			err = ErrInvalid
			break
		}
		if name == "image" {
			if raw != nil {
				part.Close()
				err = ErrInvalid
				break
			}
			raw, e = io.ReadAll(io.LimitReader(part, productimage.MaxInputBytes+1))
			if len(raw) > productimage.MaxInputBytes {
				e = productimage.ErrInputLimit
			}
		} else {
			var field []byte
			field, e = io.ReadAll(io.LimitReader(part, 4097))
			fieldBytes += len(name) + len(field)
			if len(field) > 4096 || fieldBytes > 8192 || len(values[name]) != 0 {
				e = ErrInvalid
			} else {
				values.Set(name, string(field))
			}
		}
		part.Close()
		if e != nil {
			err = e
			break
		}
	}
	r.PostForm = values
	r.Form = values
	if err != nil {
		a.imageView(w, r, nil, "", errors.New("The image form is invalid or too large. Maximum file size is 4 MiB."), http.StatusBadRequest)
		return
	}
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	id, e := num(r.PathValue("id"))
	version, ve := num(values.Get("catalog_version"))
	if e != nil || ve != nil || id < 1 || version < 1 {
		a.imageView(w, r, nil, "", ErrInvalid, http.StatusBadRequest)
		return
	}
	p, e := scanProduct(a.store.db.QueryRow(`SELECT `+productSelect+productJoins+`WHERE p.id=?`, id))
	if e != nil || p.Archived || p.CatalogVersion != version {
		a.imageView(w, r, nil, "", errors.New("This product changed. Review its current details, then choose the image again."), http.StatusConflict)
		return
	}
	if err = a.store.ClaimImagePreview(); err != nil {
		if errors.Is(err, ErrImageRate) {
			a.imageView(w, r, nil, "", err, http.StatusTooManyRequests)
		} else {
			a.fail(w, err)
		}
		return
	}
	image, err := productimage.NormalizeBytes(raw)
	if err != nil {
		a.imageView(w, r, nil, "", fmt.Errorf("Image not accepted: %v. Use a static JPEG or PNG up to 4 MiB and 4 million pixels; no edge may exceed 4,096 pixels.", err), http.StatusBadRequest)
		return
	}
	preview := ImagePreview{Token: token(), SessionID: s.ID, ProductID: id, Version: version, Expires: time.Now().Add(imagePreviewTTL), Image: image}
	a.images.mu.Lock()
	for key, p := range a.images.entries {
		if !p.Expires.After(time.Now()) {
			delete(a.images.entries, key)
		}
	}
	if len(a.images.entries) >= maxImagePreviews {
		a.images.mu.Unlock()
		a.imageView(w, r, nil, "", errors.New("The shared demo has several image previews open. Try again in ten minutes; nothing was changed."), http.StatusTooManyRequests)
		return
	}
	a.images.entries[preview.Token] = preview
	a.images.mu.Unlock()
	a.imageView(w, r, &preview, "Preview ready. Review the normalized image before making it public.", nil, http.StatusOK)
}

func (a *App) imagePreview(id int64, key, sid string) (ImagePreview, bool) {
	a.images.mu.Lock()
	defer a.images.mu.Unlock()
	p, ok := a.images.entries[key]
	return p, ok && p.ProductID == id && p.SessionID == sid && p.Expires.After(time.Now())
}
func (a *App) showImagePreview(w http.ResponseWriter, r *http.Request) {
	s, ok := a.existingImageManager(w, r)
	if !ok {
		return
	}
	id, err := num(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, ok := a.imagePreview(id, r.PathValue("token"), s.ID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", productimage.OutputMIME)
	w.Header().Set("Content-Length", strconv.Itoa(len(p.Image.Master.Data)))
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodHead {
		_, _ = w.Write(p.Image.Master.Data)
	}
}
func (a *App) confirmProductImage(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	id, err := num(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, ok := a.imagePreview(id, r.PostForm.Get("preview_token"), s.ID)
	if !ok {
		a.imageView(w, r, nil, "", errors.New("This preview expired or the app restarted. Choose the file again; no image was attached by this request."), http.StatusConflict)
		return
	}
	if r.PostForm.Get("confirm") != "public-demo-image" {
		a.imageView(w, r, &p, "", errors.New("Confirm that this demo image may appear on the public storefront."), http.StatusBadRequest)
		return
	}
	// Validation decodes normalized variants too; share the upload bound.
	select {
	case a.images.decode <- struct{}{}:
		defer func() { <-a.images.decode }()
	default:
		a.imageView(w, r, &p, "", errors.New("Another image is being prepared. Try again in a moment."), http.StatusTooManyRequests)
		return
	}
	if err = a.store.AttachProductImage(id, p.Version, p.Token, p.Image); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrImageQuota) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) {
			a.imageView(w, r, nil, "", err, http.StatusConflict)
		} else {
			a.fail(w, err)
		}
		return
	}
	a.imageView(w, r, nil, "Product image saved. The normalized artwork is now used on the storefront.", nil, http.StatusOK)
}
func (a *App) useProductIllustration(w http.ResponseWriter, r *http.Request) {
	s, ok := a.form(w, r)
	if !ok || !a.guard(w, r, s) {
		return
	}
	id, err := num(r.PathValue("id"))
	version, ve := num(r.PostForm.Get("catalog_version"))
	if err != nil || ve != nil {
		a.imageView(w, r, nil, "", ErrInvalid, http.StatusBadRequest)
		return
	}
	if err = a.store.UseProductIllustration(id, version, r.PostForm.Get("command_key")); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) {
			a.imageView(w, r, nil, "", err, http.StatusConflict)
		} else {
			a.fail(w, err)
		}
		return
	}
	a.imageView(w, r, nil, "Bundled illustration restored. The uploaded image remains in recovery storage.", nil, http.StatusOK)
}

func (a *App) serveProductImage(w http.ResponseWriter, r *http.Request) {
	variant := r.PathValue("variant")
	if variant != "master.jpg" && variant != "thumb.jpg" {
		http.NotFound(w, r)
		return
	}
	image, err := a.store.ProductImage(r.PathValue("hash"))
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	etag := `"` + image.SHA256 + "-" + variant + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("Content-Type", productimage.OutputMIME)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	data := image.Master.Data
	if variant == "thumb.jpg" {
		data = image.Thumbnail.Data
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, bytes.NewReader(data))
	}
}
