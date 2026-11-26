package factory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

const ProofInputTap, ProofInputVoice, proofRetention = "tap", "voice", 24 * time.Hour

// AnswerProof proves an owner answer came from a paired device tap or live voice (KB-ASK-19).
type AnswerProof struct {
	ID        string    `json:"proof_id"`
	AskID     string    `json:"ask_id"`
	Option    string    `json:"option"`
	InputKind string    `json:"input_kind"`
	DeviceID  string    `json:"device_id"`
	At        time.Time `json:"answered_at"`
}

func proofSourceOK(kind, dev string) bool {
	return (kind == ProofInputTap || kind == ProofInputVoice) && dev != "" && dev != "local"
}

// PrepareAnswerProof normalizes and checks the inputs shared by proof stores.
func PrepareAnswerProof(askID, option, inputKind, deviceID string) (AnswerProof, *Refusal) {
	p := AnswerProof{AskID: strings.TrimSpace(askID), Option: flow.CleanText(option), InputKind: strings.TrimSpace(inputKind), DeviceID: strings.TrimSpace(deviceID)}
	if !proofSourceOK(p.InputKind, p.DeviceID) || p.AskID == "" || p.Option == "" {
		return AnswerProof{}, &Refusal{Code: "proof_rejected", Why: "Owner answer proof requires tap or voice input from a paired device or live voice session."}
	}
	return p, nil
}

// MintProof records an in-process owner AnswerProof (mode 0600, 24 h retention).
func (e *Engine) MintProof(askID, option, inputKind, deviceID string) (AnswerProof, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, r := PrepareAnswerProof(askID, option, inputKind, deviceID)
	if r != nil {
		return AnswerProof{}, e.refuse("", r.Code, r.Why)
	}
	now := e.now()
	all, err := e.loadProofsLocked(now)
	if err == nil {
		var b [6]byte
		_, _ = rand.Read(b[:])
		p.ID, p.At = "prf-"+hex.EncodeToString(b[:]), now
		if err = writeJSON(e.Paths.proofs(), append(all, p)); err == nil {
			return p, nil
		}
	}
	return AnswerProof{}, err
}

func (e *Engine) loadProofsLocked(now time.Time) ([]AnswerProof, error) {
	if strings.TrimSpace(e.Paths.DataDir) == "" {
		return nil, errors.New("factory: DataDir is required for answer proofs")
	}
	var all []AnswerProof
	if err := readJSON(e.Paths.proofs(), &all); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return slices.DeleteFunc(all, func(p AnswerProof) bool { return now.Sub(p.At) > proofRetention }), nil
}

func verifyAnswerProof(askID, resolved, raw string, proof AnswerProof, load func() ([]AnswerProof, error)) error {
	if strings.TrimSpace(proof.ID) == "" {
		return &Refusal{Code: "proof_required", Why: "Gated asks require an owner AnswerProof from a paired device or live voice."}
	}
	if !proofSourceOK(proof.InputKind, proof.DeviceID) {
		return &Refusal{Code: "proof_rejected", Why: "Owner answer proof must come from a paired device tap or live voice."}
	}
	all, err := load()
	if err != nil || slices.ContainsFunc(all, func(p AnswerProof) bool {
		return p.ID == proof.ID && p.AskID == askID && (p.Option == resolved || p.Option == raw) && p.InputKind == proof.InputKind && p.DeviceID == proof.DeviceID
	}) {
		return err
	}
	return &Refusal{Code: "proof_rejected", Why: "AnswerProof is unknown, expired, or does not match this ask and option."}
}
