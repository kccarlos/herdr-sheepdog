package web

import (
	"errors"
	"net/http"

	"github.com/kccarlos/herdr-sheepdog/internal/push"
)

// PushService is the device registry and status of the phone
// notification sender (*push.Sender).
type PushService interface {
	RegisterDevice(push.Registration) (push.Device, error)
	Unregister(token string) error
	Status() push.Status
}

// pushRegisterRequest is the body of POST and DELETE /api/push/register.
type pushRegisterRequest struct {
	Token       string `json:"token"`
	RelayToken  string `json:"relay_token"`
	Environment string `json:"environment"`
	Device      string `json:"device"`
}

// pushRegisterResponse answers POST /api/push/register: the phone's
// end-to-end content key, which the app keeps for its notification
// service extension (docs/dev/push.md "End-to-end encryption").
type pushRegisterResponse struct {
	OK         bool   `json:"ok"`
	ContentKey string `json:"content_key"`
	Encryption string `json:"encryption"`
}

// handlePushRegister stores the iOS app's device token or relay token
// (idempotent) and answers with the phone's content key.
func (s *Server) handlePushRegister(w http.ResponseWriter, r *http.Request) {
	if s.deps.Push == nil {
		writeError(w, http.StatusServiceUnavailable, "push not available")
		return
	}
	var req pushRegisterRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	dev, err := s.deps.Push.RegisterDevice(push.Registration{Token: req.Token, RelayToken: req.RelayToken, Environment: req.Environment, Name: req.Device})
	if err != nil {
		writePushError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pushRegisterResponse{OK: true, ContentKey: dev.ContentKey, Encryption: "aes-256-gcm"})
}

// handlePushUnregister removes a device token; an unknown one is fine.
func (s *Server) handlePushUnregister(w http.ResponseWriter, r *http.Request) {
	if s.deps.Push == nil {
		writeError(w, http.StatusServiceUnavailable, "push not available")
		return
	}
	var req pushRegisterRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	tok := req.Token
	if tok == "" {
		tok = req.RelayToken
	}
	if err := s.deps.Push.Unregister(tok); err != nil {
		writePushError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePushStatus reports whether notifications are sent, why not, and
// how many devices are registered.
func (s *Server) handlePushStatus(w http.ResponseWriter, r *http.Request) {
	if s.deps.Push == nil {
		writeJSON(w, http.StatusOK, push.Status{Reason: "push not available"})
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Push.Status())
}

func writePushError(w http.ResponseWriter, err error) {
	if errors.Is(err, push.ErrBadToken) || errors.Is(err, push.ErrBadRelayToken) || errors.Is(err, push.ErrBadEnvironment) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
