package shop

import (
	"errors"
	"net/http"
)

func (a *App) showProductBarcode(w http.ResponseWriter, r *http.Request) {
	// Never cache an active label beyond a catalog archive or identity change.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id, err := num(r.PathValue("id"))
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	label, err := a.store.ProductBarcode(id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	data, err := productBarcodePNG(label.Code)
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(data)
}
