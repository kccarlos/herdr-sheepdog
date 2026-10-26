package web

import (
	"bytes"
	"net/http"
	"sync"
	"time"
)

// Idempotency keys (docs/dev/ws-contract.md "Idempotency keys"). A paired
// device may send `Idempotency-Key: <key>` with any POST. The daemon
// remembers the reply for idemTTL per device, route and key; a repeat
// gets the same reply without the action running again, and a repeat
// that arrives while the first is still running waits for it. The app
// uses this to retry a typed line, a New agent or agent input after a
// timeout without doing it twice.
const (
	idemHeader = "Idempotency-Key"
	// idemTTL is how long a reply is kept.
	idemTTL = 10 * time.Minute
	// idemMax and idemMaxPerDevice bound the entries; idemMaxBody bounds
	// one reply. A bigger reply is not kept: a repeat runs again.
	idemMax          = 1024
	idemMaxPerDevice = 256
	idemMaxBody      = 64 << 10
	idemMaxKey       = 128
)

// idemStore is the remembered replies.
type idemStore struct {
	now func() time.Time

	mu      sync.Mutex
	entries map[idemKey]*idemEntry
}

type idemKey struct {
	device, route, key string
}

// idemEntry is one action: in flight until done is closed, then its
// reply (kept is false when the reply was too big to keep).
type idemEntry struct {
	done   chan struct{}
	at     time.Time // when it finished
	kept   bool
	status int
	ctype  string
	body   []byte
}

func newIdemStore(now func() time.Time) *idemStore {
	return &idemStore{now: now, entries: map[idemKey]*idemEntry{}}
}

// begin returns the entry for k and whether the caller runs the action
// (first). ok is false when the store is full of actions in flight, and
// the caller runs the action without a key.
func (st *idemStore) begin(k idemKey) (e *idemEntry, first, ok bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	if e := st.entries[k]; e != nil {
		select {
		case <-e.done:
			if now.Sub(e.at) < idemTTL {
				return e, false, true
			}
			delete(st.entries, k)
		default:
			return e, false, true
		}
	}
	st.pruneLocked(now)
	if !st.roomLocked(func(o idemKey) bool { return o.device == k.device }, idemMaxPerDevice) ||
		!st.roomLocked(func(idemKey) bool { return true }, idemMax) {
		return nil, false, false
	}
	e = &idemEntry{done: make(chan struct{})}
	st.entries[k] = e
	return e, true, true
}

// finish records the reply of an action begun with begin.
func (st *idemStore) finish(k idemKey, e *idemEntry, status int, ctype string, body []byte, complete bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e.at, e.status, e.ctype = st.now(), status, ctype
	if complete && len(body) <= idemMaxBody {
		e.kept, e.body = true, bytes.Clone(body)
	} else {
		// Not kept: forget the key so a later repeat runs again rather
		// than getting nothing.
		if st.entries[k] == e {
			delete(st.entries, k)
		}
	}
	close(e.done)
}

// pruneLocked drops expired replies.
func (st *idemStore) pruneLocked(now time.Time) {
	for k, e := range st.entries {
		select {
		case <-e.done:
			if now.Sub(e.at) >= idemTTL {
				delete(st.entries, k)
			}
		default:
		}
	}
}

// roomLocked makes room for one more entry among those match selects,
// evicting the oldest finished one when there are max already. It is
// false when every one of them is still in flight.
func (st *idemStore) roomLocked(match func(idemKey) bool, max int) bool {
	n := 0
	var oldest idemKey
	var oldestAt time.Time
	found := false
	for k, e := range st.entries {
		if !match(k) {
			continue
		}
		n++
		select {
		case <-e.done:
			if !found || e.at.Before(oldestAt) {
				oldest, oldestAt, found = k, e.at, true
			}
		default:
		}
	}
	if n < max {
		return true
	}
	if !found {
		return false
	}
	delete(st.entries, oldest)
	return true
}

// len is the number of entries (tests).
func (st *idemStore) len() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.entries)
}

// validIdemKey is 1–128 visible ASCII characters.
func validIdemKey(k string) bool {
	if k == "" || len(k) > idemMaxKey {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] <= ' ' || k[i] > '~' {
			return false
		}
	}
	return true
}

// idemRecorder captures a reply to keep it, and writes it through.
type idemRecorder struct {
	w      http.ResponseWriter
	status int
	buf    bytes.Buffer
	over   bool
}

func (r *idemRecorder) Header() http.Header { return r.w.Header() }

func (r *idemRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.w.WriteHeader(code)
}

func (r *idemRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if !r.over {
		if r.buf.Len()+len(p) > idemMaxBody {
			r.over = true
			r.buf.Reset()
		} else {
			r.buf.Write(p)
		}
	}
	return r.w.Write(p)
}

// idempotent wraps a device POST: with an Idempotency-Key header, the
// first request runs h and its reply is kept; a repeat replays it.
func (s *Server) idempotent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(idemHeader)
		if key == "" {
			h(w, r)
			return
		}
		if !validIdemKey(key) {
			writeError(w, http.StatusBadRequest, "bad Idempotency-Key: 1 to 128 visible ASCII characters")
			return
		}
		k := idemKey{device: deviceOf(r), route: r.Method + " " + r.URL.Path, key: key}
		e, first, ok := s.idem.begin(k)
		if !ok {
			h(w, r)
			return
		}
		if first {
			rec := &idemRecorder{w: w}
			complete := false
			defer func() {
				// A handler that panicked is not kept (complete false).
				s.idem.finish(k, e, rec.status, w.Header().Get("Content-Type"), rec.buf.Bytes(), complete && !rec.over)
			}()
			h(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			complete = true
			return
		}
		select {
		case <-e.done:
		case <-r.Context().Done():
			return
		}
		if !e.kept {
			writeError(w, http.StatusConflict, "the first request with this Idempotency-Key did not keep its reply; send a new key")
			return
		}
		s.log.Log(logSource, "web.idempotent_replay", "", map[string]any{"path": r.URL.Path, "status": e.status})
		if e.ctype != "" {
			w.Header().Set("Content-Type", e.ctype)
		}
		w.Header().Set("Idempotent-Replayed", "true")
		w.WriteHeader(e.status)
		_, _ = w.Write(e.body)
	}
}
