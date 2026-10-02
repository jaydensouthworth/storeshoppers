package shop

import (
	"bytes"
	"errors"
	"net/http"
)

const maxDynamicResponseBytes = 4 << 20

var errResponseTooLarge = errors.New("dynamic response exceeds size limit")

// Dynamic HTML is already generated as a complete document/fragment; none of
// these routes stream. Bound buffering so a future route cannot silently turn
// the compatibility fence into unbounded response memory. Static files bypass it.
type boundedResponseBody struct{ data bytes.Buffer }

func (b *boundedResponseBody) Len() int      { return b.data.Len() }
func (b *boundedResponseBody) Bytes() []byte { return b.data.Bytes() }

func (b *boundedResponseBody) Write(p []byte) (int, error) {
	if len(p) > maxDynamicResponseBytes-b.Len() {
		return 0, errResponseTooLarge
	}
	return b.data.Write(p)
}

type compatibilityResponse struct {
	header, sentHeader http.Header
	status             int
	body               boundedResponseBody
	err                error
}

func (w *compatibilityResponse) Header() http.Header { return w.header }
func (w *compatibilityResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.sentHeader = w.header.Clone()
	}
}
func (w *compatibilityResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		if w.header.Get("Content-Type") == "" {
			w.header.Set("Content-Type", http.DetectContentType(p))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.body.Write(p)
	w.err = err
	return n, err
}

func (a *App) serveCompatible(w http.ResponseWriter, r *http.Request) {
	if err := a.store.CheckCompatibility(r.Context()); err != nil {
		a.fail(w, err)
		return
	}
	buffer := &compatibilityResponse{header: w.Header().Clone()}
	a.mux.ServeHTTP(buffer, r)
	// A migration can commit between any two handler reads. Do not release
	// their HTML, cookies, redirects or even a healthy status until this check.
	// Version markers must be monotonic and commit with the schema/data change.
	if err := a.store.CheckCompatibility(r.Context()); err != nil {
		a.fail(w, err)
		return
	}
	if buffer.err != nil {
		a.fail(w, buffer.err)
		return
	}
	if buffer.status == 0 {
		buffer.WriteHeader(http.StatusOK)
	}
	for name := range w.Header() {
		w.Header().Del(name)
	}
	for name, values := range buffer.sentHeader {
		w.Header()[name] = values
	}
	w.WriteHeader(buffer.status)
	_, _ = w.Write(buffer.body.Bytes())
}

func schemaUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "3")
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "The shop is temporarily unavailable while the application updates. Wait a moment and refresh.", http.StatusServiceUnavailable)
}
