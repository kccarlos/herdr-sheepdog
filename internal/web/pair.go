package web

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

// EnrollCodeLifetime is how long a pairing code works (once).
const EnrollCodeLifetime = 10 * time.Minute

// enrollCodes is the one current pairing code and the recent ones that
// no longer work, so a refusal can say why. Showing a new code
// invalidates the previous one.
type enrollCodes struct {
	mu      sync.Mutex
	cur     string
	expires time.Time
	old     map[string]string // code -> code_used | code_expired
	order   []string
}

// current returns the valid code, making a new one when there is none or
// fresh is set.
func (e *enrollCodes) current(now time.Time, fresh bool) (string, time.Time, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cur != "" && !fresh && now.Before(e.expires) {
		return e.cur, e.expires, false, nil
	}
	if e.cur != "" {
		e.retireLocked(e.cur, "code_expired")
	}
	code, err := randomID(16)
	if err != nil {
		return "", time.Time{}, false, err
	}
	e.cur, e.expires = code, now.Add(EnrollCodeLifetime)
	return e.cur, e.expires, true, nil
}

func (e *enrollCodes) retireLocked(code, reason string) {
	if e.old == nil {
		e.old = map[string]string{}
	}
	e.old[code] = reason
	e.order = append(e.order, code)
	if len(e.order) > 32 {
		delete(e.old, e.order[0])
		e.order = e.order[1:]
	}
	if e.cur == code {
		e.cur = ""
	}
}

// burn uses code once: "" when it worked, else code_used or
// code_expired.
func (e *enrollCodes) burn(code string, now time.Time) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cur != "" && subtle.ConstantTimeCompare([]byte(code), []byte(e.cur)) == 1 {
		if now.After(e.expires) {
			e.retireLocked(code, "code_expired")
			return "code_expired"
		}
		e.retireLocked(code, "code_used")
		return ""
	}
	if r, ok := e.old[code]; ok {
		return r
	}
	return "code_used"
}

// PairResponse is GET /api/pair (loopback only): the one-time pairing
// link, when it stops working, the addresses it lists, or why there is
// none.
type PairResponse struct {
	Link      string         `json:"link,omitempty"`
	Expires   *time.Time     `json:"expires,omitempty"`
	Addresses []pair.Address `json:"addresses"`
	Error     string         `json:"error,omitempty"`
}

// pairSysAddrs lists the machine's addresses (tests replace it).
var pairSysAddrs = pair.SystemAddrs

// pairAddresses are the addresses a pairing code lists for a server
// listening on addr: the machine's own, then web.advertise.
func (s *Server) pairAddresses(addr string) ([]pair.Address, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	p, _ := strconv.Atoi(port)
	sys, err := pairSysAddrs()
	if err != nil {
		return nil, err
	}
	addrs, err := pair.Addresses(host, p, sys)
	extra := pair.Advertised(s.deps.Advertise)
	if err != nil && len(extra) == 0 {
		return nil, err
	}
	return pair.Merge(addrs, extra), nil
}

// pairing is the pairing response; fresh makes a new code.
func (s *Server) pairing(fresh bool) PairResponse {
	resp := PairResponse{Addresses: []pair.Address{}}
	addrs, err := s.pairAddresses(s.Addr())
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	resp.Addresses = addrs
	if s.deps.Computer.ID == "" {
		resp.Error = errNoComputerID.Error()
		return resp
	}
	code, exp, made, err := s.enroll.current(s.deps.Now(), fresh)
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	if made {
		s.log.Log(logSource, "web.pair.code", "", map[string]any{"expires": exp.UTC()})
	}
	resp.Link = pair.Link(pair.LinkInfo{
		DaemonID: s.deps.Computer.ID, Name: s.deps.Computer.Name, Pin: s.deps.TLS.Pin(),
		Code: code, Addresses: addrs, Host: s.tailscaleHost(port(s.Addr())),
	})
	e := exp.UTC()
	resp.Expires = &e
	return resp
}

// handlePair answers the current pairing code (?new=1 makes a new one).
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.pairing(r.URL.Query().Get("new") == "1"))
}

// pairQRLink returns the current pairing link for a QR renderer, or
// writes the same conflict /api/pair would answer when there is none.
func (s *Server) pairQRLink(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := s.pairing(r.URL.Query().Get("new") == "1")
	if p.Link == "" {
		writeError(w, http.StatusConflict, p.Error)
		return "", false
	}
	return p.Link, true
}

// handlePairQR serves the current pairing code as SVG. It holds a
// one-time enroll code: the route is loopback only, and nothing logs the
// body.
func (s *Server) handlePairQR(w http.ResponseWriter, r *http.Request) {
	link, ok := s.pairQRLink(w, r)
	if !ok {
		return
	}
	svg, err := pair.SVG(link)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(svg))
}

// handlePairQRJSON serves the current pairing code as a module matrix.
// Like the SVG, it holds a one-time enroll code, is loopback only, and
// logs neither the link nor the body.
func (s *Server) handlePairQRJSON(w http.ResponseWriter, r *http.Request) {
	link, ok := s.pairQRLink(w, r)
	if !ok {
		return
	}
	matrix, err := pair.RenderMatrix(link)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, matrix)
}

// EnrollRequest is POST /api/devices/enroll.
type EnrollRequest struct {
	Code      string `json:"code"`
	PublicKey string `json:"public_key"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Hardware  bool   `json:"hardware"`
}

// EnrollResponse is its answer.
type EnrollResponse struct {
	DeviceID string `json:"device_id"`
	DaemonID string `json:"daemon_id"`
	Name     string `json:"name"`
}

// handleEnroll records a device's public key in exchange for the one-time
// pairing code (public: the code is the credential).
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	remote := !isLoopbackAddr(r.RemoteAddr)
	if remote && s.limits.isBlocked("enroll", sourceIP(r.RemoteAddr), s.deps.Now()) {
		writeRateLimited(w)
		return
	}
	var req EnrollRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	switch req.Kind {
	case DeviceIOS, DeviceIPadOS, DeviceBrowser:
	default:
		writeError(w, http.StatusBadRequest, "kind must be ios, ipados or browser")
		return
	}
	_, pub, err := parseDeviceKey(req.PublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "public_key: "+err.Error())
		return
	}
	if reason := s.enroll.burn(req.Code, s.deps.Now()); reason != "" {
		if remote {
			s.limits.fail("enroll", sourceIP(r.RemoteAddr), badCodesPerMinute, s.deps.Now())
		}
		s.log.Log(logSource, "web.pair.code_refused", "", map[string]any{"reason": reason, "remote": r.RemoteAddr})
		writeJSON(w, http.StatusGone, map[string]string{"error": "This code was already used or has expired. Show a new code on your computer and scan it.", "reason": reason})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = deviceKindName(req.Kind)
	}
	if len(name) > 64 {
		name = name[:64]
	}
	d, err := s.devices.enroll(pub, name, req.Kind, req.Hardware, s.deps.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Log(logSource, "web.device.enroll", "", map[string]any{"device_id": d.ID, "kind": d.Kind, "name": d.Name, "hardware": d.Hardware, "remote_kind": s.kindOf(r.RemoteAddr)})
	s.devicePaired(d)
	writeJSON(w, http.StatusOK, EnrollResponse{DeviceID: d.ID, DaemonID: s.deps.Computer.ID, Name: d.Name})
}

// handleDeviceSelf answers the calling device's own record.
func (s *Server) handleDeviceSelf(w http.ResponseWriter, r *http.Request) {
	id := deviceOf(r)
	d, ok := s.devices.get(id)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": "This computer", "kind": "local"})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// handleDeviceUnpair is DELETE /api/devices/self: the app removing this
// computer revokes its own key here.
func (s *Server) handleDeviceUnpair(w http.ResponseWriter, r *http.Request) {
	id := deviceOf(r)
	if id == localDevice {
		writeError(w, http.StatusBadRequest, "this computer is not a paired device")
		return
	}
	if _, err := s.revokeDevices([]string{id}, "self"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDevices lists the paired devices (loopback only: the setup page).
func (s *Server) handleDevices(w http.ResponseWriter, _ *http.Request) {
	list := s.devices.active()
	if list == nil {
		list = []Device{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": list})
}

// handleDeviceRename is PATCH /api/devices/{id} {"name"}.
func (s *Server) handleDeviceRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "want {\"name\": \"…\"}")
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 64 {
		name = name[:64]
	}
	d, ok, err := s.devices.rename(r.PathValue("id"), name)
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	case !ok:
		writeError(w, http.StatusNotFound, "no such device")
	default:
		writeJSON(w, http.StatusOK, d)
	}
}

// handleDeviceRevoke is DELETE /api/devices/{id}.
func (s *Server) handleDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	done, err := s.revokeDevices([]string{r.PathValue("id")}, "setup")
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	case len(done) == 0:
		writeError(w, http.StatusNotFound, "no such device")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleDisconnectAll revokes every device; the certificate stays.
func (s *Server) handleDisconnectAll(w http.ResponseWriter, _ *http.Request) {
	done, err := s.revokeDevices(nil, "setup")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Log(logSource, "web.device.disconnect_all", "", map[string]any{"count": len(done)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(done)})
}

// handleResetPairing revokes every device and replaces the TLS key and
// certificate (the daemon id stays); the pairing code is replaced too.
func (s *Server) handleResetPairing(w http.ResponseWriter, _ *http.Request) {
	done, err := s.revokeDevices(nil, "setup")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	old := s.deps.TLS.Pin()
	if err := s.deps.TLS.Reset(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, _, _, _ = s.enroll.current(s.deps.Now(), true)
	s.log.Log(logSource, "web.device.reset_pairing", "", map[string]any{"count": len(done)})
	s.log.Log(logSource, "web.tls.rotate", "", map[string]any{"old_pin": old, "new_pin": s.deps.TLS.Pin(), "phase": "reset"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(done)})
}

// port is the port of a host:port address ("" when there is none).
func port(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	return p
}
