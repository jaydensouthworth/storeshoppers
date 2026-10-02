package shop

import (
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestMessagesHTTPCSRFRotationDoesNotForkSendableKey(t *testing.T) {
	s, a, owner, id, customer, _, _ := messageHTTPFixture(t)
	path := fmt.Sprintf("/orders/%d/messages", id)
	page := handheldHTTPRequest(t, a, "GET", path, []*http.Cookie{customer}, nil, nil)
	old := handheldHTTPForm(t, page, path)
	old.Set("body", "Exact note with ambiguous result")
	s.db.SetMaxOpenConns(1)
	oldNow := s.now
	var fired atomic.Bool
	rotationDone := make(chan error, 1)
	s.now = func() time.Time {
		if fired.CompareAndSwap(false, true) {
			waitBefore := s.db.Stats().WaitCount
			go func() { rotationDone <- s.Manager(owner.ID, false) }()
			until := time.Now().Add(5 * time.Second)
			for s.db.Stats().WaitCount == waitBefore && time.Now().Before(until) {
				time.Sleep(time.Millisecond)
			}
			if s.db.Stats().WaitCount == waitBefore {
				t.Error("rotation did not queue")
			}
		}
		return oldNow()
	}
	// /check authenticates CSRF from its first snapshot; the queued rotation wins
	// the sole DB connection before lookup/projection, rendering K + newer CSRF.
	checked := handheldHTTPRequest(t, a, "POST", path+"/check", []*http.Cookie{customer}, old, nil)
	if err := <-rotationDone; err != nil {
		t.Fatal(err)
	}
	s.now = oldNow
	if checked.Header().Get("X-Messages-Result") != "absent" {
		t.Fatal("fixture check did not return absent", checked.Header())
	}
	currentKeyForm := handheldHTTPForm(t, checked, path)
	currentKeyForm.Set("body", old.Get("body"))
	if currentKeyForm.Get("command_key") != old.Get("command_key") || currentKeyForm.Get("csrf") == old.Get("csrf") {
		t.Fatal("fixture failed to render original key with new CSRF")
	}
	// Meanwhile, an old-token retry of that same exact command takes recovery.
	refreshed := handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer}, old, map[string]string{"HX-Request": "true"})
	fresh := handheldHTTPForm(t, refreshed, path)
	fresh.Set("body", old.Get("body"))
	if result := refreshed.Header().Get("X-Messages-Result"); result != "stale" && result != "absent" {
		t.Fatal("expected explicit recovery", refreshed.Header())
	}
	// Both forms now legitimately exist. The old-key/current-token retry can be
	// queued or network-delayed until after the recovery read returns absent.
	post := func(f url.Values) {
		r := handheldHTTPRequest(t, a, "POST", path, []*http.Cookie{customer}, f, nil)
		if r.Code != 303 {
			t.Fatal("explicit recovered send failed", r.Code)
		}
	}
	post(currentKeyForm)
	post(fresh)
	count := testCount(t, s, "order_messages")
	if count != 1 {
		t.Fatal("CSRF recovery forked one exact intent into two sendable command keys")
	}
}
