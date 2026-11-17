package factory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// ProofsFileName is the filename inside DataDir storing minted AnswerProof records.
	ProofsFileName = "factory_proofs.json"

	// DefaultProofRetention is how long minted AnswerProof records stay verifiable.
	DefaultProofRetention = 24 * time.Hour
)

type proofStoreFile struct {
	Protocol int           `json:"protocol"`
	Proofs   []AnswerProof `json:"proofs"`
}

// MintInput carries the rail-verified owner input fields used to mint an AnswerProof.
type MintInput struct {
	AskID       string
	Project     string
	Kind        AskKind
	OptionIndex int
	OptionLabel string
	InputKind   string
	InputSeq    uint64
	UtteranceID string
	DeviceID    string
	AnsweredAt  time.Time
}

// ProofStore persists and verifies loopback AnswerProof records in
// `<data_dir>/factory_proofs.json` (mode 0600, atomic rename, 24h retention).
// When dataDir is empty, proofs are kept in memory for the life of the store.
type ProofStore struct {
	mu        sync.Mutex
	path      string
	retention time.Duration
	now       func() time.Time
	proofs    map[string]AnswerProof
	order     []string
}

// ProofsPath returns `<data_dir>/factory_proofs.json`, or "" when dataDir is empty.
func ProofsPath(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(dataDir), ProofsFileName)
}

// NewProofStore opens (or initializes) the AnswerProof store in dataDir.
func NewProofStore(dataDir string, now func() time.Time) (*ProofStore, error) {
	if now == nil {
		now = time.Now
	}
	s := &ProofStore{
		path:      ProofsPath(dataDir),
		retention: DefaultProofRetention,
		now:       now,
		proofs:    map[string]AnswerProof{},
	}
	if s.path != "" {
		if err := s.loadLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *ProofStore) loadLocked() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var doc proofStoreFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	cutoff := s.cutoffMillis()
	for _, p := range doc.Proofs {
		if p.AnsweredAt < cutoff {
			continue
		}
		if err := p.Validate(); err != nil {
			continue
		}
		if _, exists := s.proofs[p.ProofID]; !exists {
			s.order = append(s.order, p.ProofID)
		}
		s.proofs[p.ProofID] = p
	}
	return nil
}

func (s *ProofStore) cutoffMillis() int64 {
	return s.now().Add(-s.retention).UnixMilli()
}

func (s *ProofStore) pruneLocked() {
	cutoff := s.cutoffMillis()
	kept := s.order[:0]
	for _, id := range s.order {
		p, ok := s.proofs[id]
		if !ok {
			continue
		}
		if p.AnsweredAt < cutoff {
			delete(s.proofs, id)
			continue
		}
		kept = append(kept, id)
	}
	s.order = kept
}

func (s *ProofStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	list := make([]AnswerProof, 0, len(s.order))
	for _, id := range s.order {
		if p, ok := s.proofs[id]; ok {
			list = append(list, p)
		}
	}
	raw, err := json.MarshalIndent(proofStoreFile{
		Protocol: ProtocolVersion,
		Proofs:   list,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode proofs: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".factory-proofs-*")
	if err != nil {
		return fmt.Errorf("stage %s: %w", s.path, err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename %s: %w", s.path, err)
	}
	return nil
}

func newProofID(existing map[string]AnswerProof) (string, error) {
	var buf [6]byte
	for range 8 {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		id := "prf-" + hex.EncodeToString(buf[:])
		if _, taken := existing[id]; !taken {
			return id, nil
		}
	}
	return "", fmt.Errorf("factory: could not generate unique proof_id")
}

// Mint validates in against the v1 AnswerProof safety rail, generates a fresh
// `prf-<12hex>` identifier, and persists the record atomically.
func (s *ProofStore) Mint(in MintInput) (AnswerProof, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneLocked()
	id, err := newProofID(s.proofs)
	if err != nil {
		return AnswerProof{}, err
	}
	at := in.AnsweredAt
	if at.IsZero() {
		at = s.now()
	}
	proof := AnswerProof{
		OK:          true,
		Verified:    true,
		ProofID:     id,
		AskID:       in.AskID,
		Project:     in.Project,
		Kind:        in.Kind,
		OptionIndex: in.OptionIndex,
		OptionLabel: in.OptionLabel,
		InputKind:   in.InputKind,
		InputSeq:    in.InputSeq,
		UtteranceID: in.UtteranceID,
		DeviceID:    in.DeviceID,
		AnsweredAt:  at.UnixMilli(),
	}
	if err := proof.Validate(); err != nil {
		return AnswerProof{}, err
	}
	s.proofs[id] = proof
	s.order = append(s.order, id)
	if err := s.saveLocked(); err != nil {
		delete(s.proofs, id)
		s.order = s.order[:len(s.order)-1]
		return AnswerProof{}, err
	}
	return proof, nil
}

// Delete removes proofID from the store (for example when rolling back a failed
// engine command before the ask was answered).
func (s *ProofStore) Delete(proofID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.proofs[proofID]; !ok {
		return nil
	}
	delete(s.proofs, proofID)
	kept := s.order[:0]
	for _, id := range s.order {
		if id != proofID {
			kept = append(kept, id)
		}
	}
	s.order = kept
	return s.saveLocked()
}

// Count returns the number of currently retained AnswerProof records.
func (s *ProofStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	return len(s.proofs)
}

// Verify checks whether proofID authorizes answering askID with optionLabel.
// On failure it returns a machine-readable reason, an HTTP status code (403 or
// 404), and a ContractError with CodeProofRejected.
func (s *ProofStore) Verify(askID, proofID, optionLabel string) (AnswerProof, string, int, error) {
	askID = strings.TrimSpace(askID)
	proofID = strings.TrimSpace(proofID)
	optionLabel = strings.TrimSpace(optionLabel)

	if askID == "" || !askIDRe.MatchString(askID) {
		return AnswerProof{}, "invalid_ask_id", http.StatusForbidden,
			contractErr(CodeProofRejected, ErrProofRejected, "invalid ask_id %q", askID)
	}
	if proofID == "" {
		return AnswerProof{}, "missing_proof", http.StatusForbidden,
			contractErr(CodeProofRejected, ErrProofRejected, "missing proof parameter")
	}
	if optionLabel == "" {
		return AnswerProof{}, "missing_option", http.StatusForbidden,
			contractErr(CodeProofRejected, ErrProofRejected, "missing option parameter")
	}

	s.mu.Lock()
	s.pruneLocked()
	proof, ok := s.proofs[proofID]
	s.mu.Unlock()

	if !ok {
		return AnswerProof{}, "unknown_proof", http.StatusNotFound,
			contractErr(CodeProofRejected, ErrProofRejected, "unknown or expired proof %q", proofID)
	}
	if proof.AskID != askID {
		return AnswerProof{}, "ask_mismatch", http.StatusForbidden,
			contractErr(CodeProofRejected, ErrProofRejected, "proof %s belongs to ask %s, not %s", proofID, proof.AskID, askID)
	}
	if proof.OptionLabel != optionLabel {
		return AnswerProof{}, "option_mismatch", http.StatusForbidden,
			contractErr(CodeProofRejected, ErrProofRejected, "proof %s option %q does not match %q", proofID, proof.OptionLabel, optionLabel)
	}
	if err := proof.Validate(); err != nil {
		return AnswerProof{}, "invalid_proof", http.StatusForbidden,
			contractErr(CodeProofRejected, ErrProofRejected, "stored proof %s failed validation: %v", proofID, err)
	}
	return proof, "", http.StatusOK, nil
}
