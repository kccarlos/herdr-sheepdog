package factory

import (
	"errors"
	"time"
)

// Owner answer proof input kinds (design.md 7.1, KB-ASK-19).
const (
	ProofInputTap   = "tap"
	ProofInputVoice = "voice"
)

// AnswerProof proves an owner answer came from a paired device tap or live voice
// session (design.md 7.1, D15, KB-ASK-19).
type AnswerProof struct {
	ID        string    `json:"proof_id"`
	AskID     string    `json:"ask_id"`
	Option    string    `json:"option"`
	InputKind string    `json:"input_kind"`
	DeviceID  string    `json:"device_id"`
	At        time.Time `json:"answered_at"`
}

// MintProof is a test stub for PR 6.1 TDD step 1.
func (e *Engine) MintProof(_, _, _, _ string) (AnswerProof, error) {
	return AnswerProof{}, errors.New("MintProof not implemented")
}
