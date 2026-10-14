package web

import (
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// Account problems on /ws (docs/ws-contract.md "Account problems"): an
// invalid key, a used-up quota or balance of the orchestrator's or the
// voice's provider is shown as an error message naming the service and
// the fix, once when it starts and again to every client that attaches
// while it lasts. Information only: nothing is retried or switched here.

// accountMsg is the /ws message for a.
func accountMsg(a brain.AccountAlert) wsproto.Message {
	return wsproto.AccountError(a.Message, string(a.Problem), a.Role, a.Provider, a.Fix)
}

// onAccount sends a new account problem to the client (Accounts'
// subscriber). A cleared one sends nothing: the next status or answer
// shows it works again.
func (h *hub) onAccount(a brain.AccountAlert, active bool) {
	if !active {
		return
	}
	h.s.log.Log(logSource, "web.account_problem", "", map[string]any{
		"role": a.Role, "provider": a.Provider, "problem": string(a.Problem), "message": a.Message, "fix": a.Fix,
	})
	h.send(accountMsg(a))
}

// sendAccounts sends the problems in force to c, a client just attached.
func (h *hub) sendAccounts(c *client) {
	for _, a := range h.s.deps.Accounts.Active() {
		c.enqueue(controlFrame(accountMsg(a)))
	}
}

// voiceAccountError records err, a voice session error, when it is an
// account problem of the voice provider, and reports whether it was one
// (the raw error is then not sent: the plain one was, once).
func (h *hub) voiceAccountError(err error) bool {
	p := brain.AccountProblemOfErr(err)
	if p == brain.ProblemNone {
		return false
	}
	h.s.deps.Accounts.Report(brain.NewAccountAlert(brain.RoleVoice, h.s.voiceProvider(), p, false))
	return true
}

// voiceConnected ends the voice provider's account problem once a
// session is really open: GPT-Live's standby (no billed session yet)
// proves nothing about the key.
func (h *hub) voiceConnected(e voice.EventStatus) {
	if e.Status != voice.StatusConnected || e.Reason == "standby" {
		return
	}
	h.s.deps.Accounts.Clear(brain.RoleVoice, h.s.voiceProvider())
}
