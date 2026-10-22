package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/identity"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// Device authentication (docs/dev/design/security.md "Device
// authentication"): a device signs a one-time challenge with its key and
// gets a session ticket, kept in memory only. Every request then carries
// the ticket: the app as `Authorization: Bearer sds_<ticket>`, a browser
// as the sheepdog_session cookie. This computer itself (loopback) needs
// no ticket.

const (
	// sessionCookie carries a browser's ticket: HttpOnly, Secure,
	// SameSite=Strict.
	sessionCookie = "sheepdog_session"
	// ticketPrefix marks a ticket in the Authorization header.
	ticketPrefix = "sds_"
	// TicketLifetime is how long a session ticket lasts.
	TicketLifetime = 12 * time.Hour
	// challengeLifetime is how long a challenge nonce may be used.
	challengeLifetime = 60 * time.Second
	// signatureSkew is how far a signature's time may be from ours.
	signatureSkew = 5 * time.Minute
	// localDevice is the device id of this computer itself.
	localDevice = "local"
)

// Refusal reasons in a 401 body, so the app can say why.
const (
	reasonNoSession      = "no_session"
	reasonSessionExpired = "session_expired"
	reasonDeviceRevoked  = "device_revoked"
	reasonUnknownDevice  = "unknown_device"
)

// ticket is one session.
type ticket struct {
	device  string
	expires time.Time
}

// authState is the in-memory half of device auth: open challenges and
// session tickets. A restart forgets both; clients sign again.
type authState struct {
	mu         sync.Mutex
	challenges map[string]time.Time
	tickets    map[string]ticket
}

func newAuthState() *authState {
	return &authState{challenges: map[string]time.Time{}, tickets: map[string]ticket{}}
}

// deviceCtxKey carries the authenticated device id on a request.
type deviceCtxKey struct{}

// deviceOf is the id of the device a request came from ("local" for
// this computer, "" before auth).
func deviceOf(r *http.Request) string {
	id, _ := r.Context().Value(deviceCtxKey{}).(string)
	return id
}

// authFunc wraps h with device auth. A missing or bad ticket answers 401
// {"error":"unauthorized","reason":…} and logs web.device.auth_failed
// (never the ticket). A ticket presented on loopback is checked too, so a
// revoked device is refused on every path.
func (s *Server) authFunc(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dev, reason := s.authenticate(r)
		if reason != "" {
			s.log.Log(logSource, "web.device.auth_failed", "", map[string]any{"reason": reason, "remote": r.RemoteAddr, "path": r.URL.Path})
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized", "reason": reason})
			return
		}
		if dev != localDevice {
			s.devices.seen(dev, s.kindOf(r.RemoteAddr), s.deps.Now(), false)
			s.phones.note(r, s.deps.Now())
		}
		h(w, r.WithContext(context.WithValue(r.Context(), deviceCtxKey{}, dev)))
	}
}

// presentedTicket is the ticket a request carries: the bearer header,
// else the session cookie.
func presentedTicket(r *http.Request) string {
	if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "+ticketPrefix); ok {
		return strings.TrimSpace(b)
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

// authenticate returns the request's device id, or why it has none.
func (s *Server) authenticate(r *http.Request) (string, string) {
	t := presentedTicket(r)
	if t == "" {
		if s.kindOf(r.RemoteAddr) == "loopback" {
			return localDevice, ""
		}
		return "", reasonNoSession
	}
	now := s.deps.Now()
	s.auth.mu.Lock()
	tk, ok := s.auth.tickets[t]
	if ok && now.After(tk.expires) {
		delete(s.auth.tickets, t)
		ok = false
	}
	s.auth.mu.Unlock()
	if !ok {
		return "", reasonSessionExpired
	}
	d, found := s.devices.get(tk.device)
	switch {
	case !found:
		return "", reasonUnknownDevice
	case d.Revoked != nil:
		return "", reasonDeviceRevoked
	}
	return d.ID, ""
}

// ChallengeResponse is GET /api/auth/challenge.
type ChallengeResponse struct {
	Nonce     string `json:"nonce"`
	DaemonID  string `json:"daemon_id"`
	ExpiresIn int    `json:"expires_in"`
}

// handleChallenge hands out a one-time nonce (public).
func (s *Server) handleChallenge(w http.ResponseWriter, _ *http.Request) {
	nonce, err := randomID(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := s.deps.Now()
	s.auth.mu.Lock()
	for n, exp := range s.auth.challenges {
		if now.After(exp) {
			delete(s.auth.challenges, n)
		}
	}
	if len(s.auth.challenges) >= 4096 {
		s.auth.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, "too many open challenges")
		return
	}
	s.auth.challenges[nonce] = now.Add(challengeLifetime)
	s.auth.mu.Unlock()
	writeJSON(w, http.StatusOK, ChallengeResponse{Nonce: nonce, DaemonID: s.deps.Computer.ID, ExpiresIn: int(challengeLifetime / time.Second)})
}

// SessionRequest is POST /api/auth/session.
type SessionRequest struct {
	DeviceID  string `json:"device_id"`
	Nonce     string `json:"nonce"`
	Time      int64  `json:"time"`
	Signature string `json:"signature"`
	// Client is "app" or "browser"; a browser also gets the cookie.
	Client string `json:"client"`
}

// SessionResponse is the answer: the ticket, its lifetime, the device and,
// during a certificate rotation, the next pin.
type SessionResponse struct {
	Ticket    string        `json:"ticket"`
	ExpiresIn int           `json:"expires_in"`
	Device    SessionDevice `json:"device"`
	NextPin   string        `json:"next_pin,omitempty"`
}

// SessionDevice names the device a ticket belongs to.
type SessionDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SignedText is what a device signs for a session: the daemon id and pin
// bind it to this daemon (a fake daemon cannot reuse it), the nonce makes
// it one-time. A browser, which cannot know the pin for sure, signs an
// empty pin line.
func SignedText(daemonID, pin, deviceID, nonce string, unix int64) string {
	return strings.Join([]string{"sheepdog-auth-v1", daemonID, pin, deviceID, nonce, strconv.FormatInt(unix, 10), "session"}, "\n")
}

// handleSession checks a signed challenge and issues a ticket.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	var req SessionRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	fail := func(reason string) {
		s.log.Log(logSource, "web.device.auth_failed", "", map[string]any{"reason": reason, "remote": r.RemoteAddr, "device_id": req.DeviceID})
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized", "reason": reason})
	}
	now := s.deps.Now()
	s.auth.mu.Lock()
	exp, ok := s.auth.challenges[req.Nonce]
	delete(s.auth.challenges, req.Nonce)
	s.auth.mu.Unlock()
	if !ok || now.After(exp) {
		fail("bad_nonce")
		return
	}
	if d := now.Sub(time.Unix(req.Time, 0)); d > signatureSkew || d < -signatureSkew {
		fail("bad_time")
		return
	}
	d, found := s.devices.get(req.DeviceID)
	switch {
	case !found:
		fail(reasonUnknownDevice)
		return
	case d.Revoked != nil:
		fail(reasonDeviceRevoked)
		return
	}
	pub, _, err := parseDeviceKey(d.PublicKey)
	if err != nil {
		fail(reasonUnknownDevice)
		return
	}
	sig, err := decodeB64(req.Signature)
	if err != nil {
		fail("bad_signature")
		return
	}
	pins := []string{s.deps.TLS.Pin(), s.deps.TLS.NextPin(), ""}
	valid := false
	for _, pin := range pins {
		if verifySignature(pub, []byte(SignedText(s.deps.Computer.ID, pin, d.ID, req.Nonce, req.Time)), sig) {
			valid = true
			break
		}
	}
	if !valid {
		fail("bad_signature")
		return
	}
	t, err := randomID(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.auth.mu.Lock()
	for k, v := range s.auth.tickets {
		if now.After(v.expires) {
			delete(s.auth.tickets, k)
		}
	}
	s.auth.tickets[t] = ticket{device: d.ID, expires: now.Add(TicketLifetime)}
	s.auth.mu.Unlock()
	kind := s.kindOf(r.RemoteAddr)
	s.devices.seen(d.ID, kind, now, true)
	s.log.Log(logSource, "web.device.session", "", map[string]any{"device_id": d.ID, "remote_kind": kind})
	if req.Client == "browser" {
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: t, Path: "/", MaxAge: int(TicketLifetime / time.Second),
			HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
		})
	}
	writeJSON(w, http.StatusOK, SessionResponse{
		Ticket: t, ExpiresIn: int(TicketLifetime / time.Second),
		Device:  SessionDevice{ID: d.ID, Name: d.Name},
		NextPin: s.deps.TLS.NextPin(),
	})
}

// verifySignature checks an ECDSA P-256 SHA-256 signature in either form
// clients produce: ASN.1 DER (Security framework) or r||s (WebCrypto).
func verifySignature(pub *ecdsa.PublicKey, msg, sig []byte) bool {
	sum := sha256.Sum256(msg)
	if len(sig) == 64 {
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if ecdsa.Verify(pub, sum[:], r, s) {
			return true
		}
	}
	return ecdsa.VerifyASN1(pub, sum[:], sig)
}

// dropTickets forgets every ticket of the given devices.
func (s *Server) dropTickets(ids []string) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	for k, v := range s.auth.tickets {
		if contains(ids, v.device) {
			delete(s.auth.tickets, k)
		}
	}
}

// closeDeviceRevoked is the WebSocket close code for a revoked device
// (/ws and /term): the client shows why and does not retry.
const closeDeviceRevoked websocket.StatusCode = 4003

// liveConns tracks every long-lived connection (/ws, /term, /events) by
// device, so revoking a device closes them at once.
type liveConns struct {
	mu    sync.Mutex
	seq   int
	conns map[string]map[int]func()
}

// track registers close for the request's device; the returned func
// unregisters it.
func (s *Server) track(r *http.Request, close func()) func() {
	dev := deviceOf(r)
	if dev == "" || dev == localDevice {
		return func() {}
	}
	l := &s.live
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conns == nil {
		l.conns = map[string]map[int]func(){}
	}
	l.seq++
	n := l.seq
	if l.conns[dev] == nil {
		l.conns[dev] = map[int]func(){}
	}
	l.conns[dev][n] = close
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.conns[dev], n)
	}
}

// closeDevices closes every live connection of the given devices.
func (s *Server) closeDevices(ids []string) {
	l := &s.live
	l.mu.Lock()
	var fns []func()
	for _, id := range ids {
		for _, f := range l.conns[id] {
			fns = append(fns, f)
		}
		delete(l.conns, id)
	}
	l.mu.Unlock()
	for _, f := range fns {
		f()
	}
}

// revokeDevices revokes devices (all when ids is empty): their tickets go,
// their connections close and their push registrations are dropped.
func (s *Server) revokeDevices(ids []string, by string) ([]string, error) {
	done, err := s.devices.revoke(ids, s.deps.Now())
	if err != nil {
		return nil, err
	}
	s.dropTickets(done)
	s.closeDevices(done)
	s.forgetPush(done)
	for _, id := range done {
		s.log.Log(logSource, "web.device.revoke", "", map[string]any{"device_id": id, "by": by})
	}
	return done, nil
}

var errNoComputerID = errors.New("this daemon has no id (its data directory is not writable)")

// HelloResponse is the body of GET /api/hello: public, so a client (a
// phone app, docs/dev/ws-contract.md "Auth") can check it found a sheepdog
// daemon and the audio format /ws expects before it connects.
type HelloResponse struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// VoiceName is the voice's own name (config voice.name).
	VoiceName string `json:"voice_name,omitempty"`
	// Auth is "device": a paired device key and a session ticket.
	Auth  string     `json:"auth"`
	WS    string     `json:"ws"`
	Audio HelloAudio `json:"audio"`
	// VoiceProvider and VoiceModel are the voice model behind /ws right
	// now ("gemini"/"gemini-3.8-live", "openai"/"gpt-live-1"; "off" and
	// "" with --voice=off).
	VoiceProvider string `json:"voice_provider"`
	VoiceModel    string `json:"voice_model"`
	// Protocol is the /ws protocol this daemon speaks and MinProtocol
	// the oldest client protocol it serves (docs/dev/ws-contract.md
	// "Protocol version and handshake"). Version is the binary's
	// version; the two ship and move separately.
	Protocol    int `json:"protocol"`
	MinProtocol int `json:"min_protocol"`
	// Computer is this computer's id (the daemon id) and name
	// (computer.name, else the system's): the app keys everything by
	// the id. Omitted when the daemon has no id (a test server).
	Computer *identity.Computer `json:"computer,omitempty"`
	// Security is the TLS key the daemon serves (docs/dev/design/security.md
	// "GET /api/hello"). The pin is a public key hash, safe to publish;
	// the app never trusts this field, it trusts the pairing code's pin.
	Security HelloSecurity `json:"security"`
}

// HelloSecurity is /api/hello's security object.
type HelloSecurity struct {
	Pin         string    `json:"pin"`
	CertExpires time.Time `json:"cert_expires"`
}

// HelloAudio is the /ws audio contract (docs/dev/ws-contract.md): what a
// client must send and should expect back.
type HelloAudio struct {
	InHz   int    `json:"in_hz"`
	OutHz  int    `json:"out_hz"`
	Format string `json:"format"`
}

func (s *Server) handleHello(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HelloResponse{
		Name: "sheepdog", Version: s.deps.Version, VoiceName: s.deps.VoiceName, Auth: "device", WS: "/ws",
		Audio:         HelloAudio{InHz: 16000, OutHz: 24000, Format: "pcm16le mono"},
		VoiceProvider: s.voiceProvider(), VoiceModel: s.voiceModel(),
		Protocol: wsproto.ProtocolVersion, MinProtocol: s.deps.MinProtocol,
		Computer: s.computer(),
		Security: HelloSecurity{Pin: s.deps.TLS.Pin(), CertExpires: s.deps.TLS.Expires().UTC()},
	})
}

// computer is Deps.Computer, nil without an id.
func (s *Server) computer() *identity.Computer {
	if s.deps.Computer.ID == "" {
		return nil
	}
	c := s.deps.Computer
	return &c
}
