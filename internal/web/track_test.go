package web

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// trackedRequest is a request of the paired device d, as authFunc hands it
// to a stream handler.
func (h *harness) trackedRequest(d testDevice) *http.Request {
	h.t.Helper()
	r := h.authedRequest("GET", "/events", d.ticket, nil, nil)
	return r.WithContext(context.WithValue(r.Context(), deviceCtxKey{}, d.id))
}

// A stream that registers after its device was revoked (the revoke ran
// between authenticate and track) is closed at once, not left open.
func TestTrackClosesAStreamOfARevokedDevice(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	d := h.pairDevice()
	r := h.trackedRequest(d)
	if _, err := h.srv.devices.revoke([]string{d.id}, time.Now()); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	untrack, _ := h.srv.track(r, func(string) { close(closed) })
	defer untrack()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("a stream of a revoked device stayed open")
	}
}

// An open stream ends when its ticket does, as the next request would be
// refused.
func TestTrackClosesAStreamWhenItsTicketExpires(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	d := h.pairDevice()
	r := h.trackedRequest(d)
	h.srv.auth.mu.Lock()
	tk := h.srv.auth.tickets[d.ticket]
	tk.expires = h.srv.deps.Now().Add(100 * time.Millisecond)
	h.srv.auth.tickets[d.ticket] = tk
	h.srv.auth.mu.Unlock()

	closed := make(chan struct{})
	untrack, _ := h.srv.track(r, func(string) { close(closed) })
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("a stream outlived its ticket")
	}
	untrack()

	// A stream that ends first leaves no timer behind to close it later.
	tk.expires = h.srv.deps.Now().Add(100 * time.Millisecond)
	h.srv.auth.mu.Lock()
	h.srv.auth.tickets[d.ticket] = tk
	h.srv.auth.mu.Unlock()
	late := make(chan struct{})
	untrackLate, _ := h.srv.track(r, func(string) { close(late) })
	untrackLate()
	select {
	case <-late:
		t.Fatal("close ran after the stream ended")
	case <-time.After(400 * time.Millisecond):
	}
}
