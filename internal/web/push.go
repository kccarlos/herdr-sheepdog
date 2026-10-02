package web

import (
	"errors"
	"net/http"

	"github.com/kccarlos/herdr-sheepdog/internal/push"
)

// PushService is the device registry and status of the phone
// notification sender (*push.Sender).
type PushService interface {
	Register(token, environment, name string) error
	Unregister(token string) error
	Status() push.Status
}

// pushRegisterRequest is the body of POST and DELETE /api/push/register.
type pushRegisterRequest struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	Device      string `json:"device"`
}

// handlePushRegister stores the iOS app's device token (idempotent).
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
	if err := s.deps.Push.Register(req.Token, req.Environment, req.Device); err != nil {
		writePushError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	if err := s.deps.Push.Unregister(req.Token); err != nil {
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
	if errors.Is(err, push.ErrBadToken) || errors.Is(err, push.ErrBadEnvironment) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
