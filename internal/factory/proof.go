package factory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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

// MintProof records an in-process owner AnswerProof (mode 0600, 24 h retention).
func (e *Engine) MintProof(askID, option, inputKind, deviceID string) (AnswerProof, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	askID, option, inputKind, deviceID = strings.TrimSpace(askID), flow.CleanText(option), strings.TrimSpace(inputKind), strings.TrimSpace(deviceID)
	if !proofSourceOK(inputKind, deviceID) || askID == "" || option == "" {
		return AnswerProof{}, e.refuse("", "proof_rejected", "Owner answer proof requires tap or voice input from a paired device or live voice session.")
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return AnswerProof{}, err
	}
	now := e.now()
	p := AnswerProof{ID: "prf-" + hex.EncodeToString(b[:]), AskID: askID, Option: option, InputKind: inputKind, DeviceID: deviceID, At: now}
	if e.Paths.DataDir != "" {
		data, err := json.MarshalIndent(append(e.loadProofsLocked(now), p), "", "  ")
		if err != nil {
			return AnswerProof{}, err
		}
		if err := writeAtomic(filepath.Join(e.Paths.DataDir, "factory", "proofs.json"), append(data, '\n')); err != nil {
			return AnswerProof{}, err
		}
	}
	return p, nil
}

func (e *Engine) loadProofsLocked(now time.Time) []AnswerProof {
	var all []AnswerProof
	if data, err := os.ReadFile(filepath.Join(e.Paths.DataDir, "factory", "proofs.json")); e.Paths.DataDir == "" || err != nil || json.Unmarshal(data, &all) != nil {
		return nil
	}
	return slices.DeleteFunc(all, func(p AnswerProof) bool { return now.Sub(p.At) > proofRetention })
}

func (e *Engine) verifyProofLocked(askID, resolved, raw string, proof AnswerProof, now time.Time) (string, string) {
	switch {
	case strings.TrimSpace(proof.ID) == "":
		return "proof_required", "Gated asks require an owner AnswerProof from a paired device or live voice."
	case !proofSourceOK(proof.InputKind, proof.DeviceID):
		return "proof_rejected", "Owner answer proof must come from a paired device tap or live voice."
	case slices.ContainsFunc(e.loadProofsLocked(now), func(p AnswerProof) bool {
		return p.ID == proof.ID && p.AskID == askID && (p.Option == resolved || p.Option == raw) && p.InputKind == proof.InputKind && p.DeviceID == proof.DeviceID
	}):
		return "", ""
	}
	return "proof_rejected", "AnswerProof is unknown, expired, or does not match this ask and option."
}
