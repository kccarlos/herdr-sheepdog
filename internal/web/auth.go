package web

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/identity"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// tokenCookie is the cookie POST /api/login sets once web.token is
// configured, so the token is typed once: httpOnly (page script never
// reads it back), SameSite=Lax (same-origin only), Secure only over TLS
// (there is none in v1 — plain HTTP on the LAN or a Tailscale address).
const tokenCookie = "sheepdog_token"

// authFunc wraps h with the web.token check (CONFIG.md "web.token"):
// missing or wrong answers 401 {"error":"unauthorized"} and logs
// web.unauthorized (the path and remote address, never the token).
// allowQueryToken permits ?token=<token> alongside the Authorization
// header and the login cookie — only /ws and /events get it, since a
// browser cannot set a header on a WebSocket or EventSource request.
func (s *Server) authFunc(h http.HandlerFunc, allowQueryToken bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r, allowQueryToken) {
			s.log.Log(logSource, "web.unauthorized", "", map[string]any{"path": r.URL.Path, "remote": r.RemoteAddr})
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		s.phones.note(r, s.deps.Now())
		h(w, r)
	}
}

// authorized reports whether r carries the configured token (true when
// no token is configured: v1's public-by-default behaviour). Every
// comparison is constant-time.
func (s *Server) authorized(r *http.Request, allowQueryToken bool) bool {
	token := s.deps.Token
	if token == "" {
		return true
	}
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && constEqual(bearer, token) {
		return true
	}
	if allowQueryToken && constEqual(r.URL.Query().Get("token"), token) {
		return true
	}
	if c, err := r.Cookie(tokenCookie); err == nil && constEqual(c.Value, token) {
		return true
	}
	return false
}

// constEqual compares a and b without leaking their difference through
// timing.
func constEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// HelloResponse is the body of GET /api/hello: public, so a client (a
// phone app, docs/dev/ws-contract.md "Auth") can check it found a sheepdog
// daemon, whether it needs a token, and the audio format /ws expects
// before it connects.
type HelloResponse struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// VoiceName is the voice's own name (config voice.name).
	VoiceName string `json:"voice_name,omitempty"`
	// Auth is "token" (present a bearer token; see CONFIG.md "web.token")
	// or "none".
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

func (s *Server) handleHello(w http.ResponseWriter, r *http.Request) {
	auth := "none"
	if s.deps.Token != "" {
		auth = "token"
	}
	writeJSON(w, http.StatusOK, HelloResponse{
		Name: "sheepdog", Version: s.deps.Version, VoiceName: s.deps.VoiceName, Auth: auth, WS: "/ws",
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

// LoginRequest is the body of POST /api/login: the token typed once into
// the /voice or /debug prompt (docs/dev/ws-contract.md "Auth").
type LoginRequest struct {
	Token string `json:"token"`
}

// handleLogin trades a correct token for the sheepdog_token cookie every
// later request on this origin carries automatically — including the
// WebSocket and EventSource ones that cannot set a header. Public (like
// GET /api/hello): a client has to be able to reach it before it has the
// cookie. When no token is configured it always succeeds, so the pages'
// gate never gets stuck open with nothing to type.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.deps.Token == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	var req LoginRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if !constEqual(req.Token, s.deps.Token) {
		s.log.Log(logSource, "web.unauthorized", "", map[string]any{"path": r.URL.Path, "remote": r.RemoteAddr})
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: tokenCookie, Value: req.Token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
