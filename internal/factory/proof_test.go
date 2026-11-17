package factory

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProofStoreLifecycleAndPersistence(t *testing.T) {
	dir := t.TempDir()
	now := time.UnixMilli(1763301030000)
	clock := func() time.Time { return now }

	store, err := NewProofStore(dir, clock)
	if err != nil {
		t.Fatalf("NewProofStore: %v", err)
	}

	proof, err := store.Mint(MintInput{
		AskID:       "ask002",
		Project:     "beacon",
		Kind:        AskKindGate,
		OptionIndex: 1,
		OptionLabel: "Accept and merge",
		InputKind:   "tap",
		InputSeq:    17,
		UtteranceID: "utt-1-8",
		DeviceID:    "dev-iphone-1",
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !strings.HasPrefix(proof.ProofID, "prf-") || len(proof.ProofID) != 16 {
		t.Fatalf("ProofID = %q, want prf-<12hex>", proof.ProofID)
	}

	info, err := os.Stat(ProofsPath(dir))
	if err != nil {
		t.Fatalf("stat %s: %v", ProofsPath(dir), err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("proofs file mode = %o, want 0600", perm)
	}

	// Reload from disk and verify exact match, mismatch, and expiry.
	reloaded, err := NewProofStore(dir, clock)
	if err != nil {
		t.Fatalf("reload ProofStore: %v", err)
	}
	got, reason, status, err := reloaded.Verify("ask002", proof.ProofID, "Accept and merge")
	if err != nil || status != http.StatusOK || reason != "" {
		t.Fatalf("Verify valid: status=%d reason=%q err=%v", status, reason, err)
	}
	if got != proof {
		t.Fatalf("Verify returned %+v, want %+v", got, proof)
	}

	// Wrong option is 403 option_mismatch.
	if _, reason, status, err := reloaded.Verify("ask002", proof.ProofID, "Send back to worker"); !errors.Is(err, ErrProofRejected) || status != http.StatusForbidden || reason != "option_mismatch" {
		t.Fatalf("Verify wrong option: status=%d reason=%q err=%v", status, reason, err)
	}

	// Wrong ask_id is 403 ask_mismatch.
	if _, reason, status, err := reloaded.Verify("ask999", proof.ProofID, "Accept and merge"); !errors.Is(err, ErrProofRejected) || status != http.StatusForbidden || reason != "ask_mismatch" {
		t.Fatalf("Verify wrong ask: status=%d reason=%q err=%v", status, reason, err)
	}

	// Unknown proof is 404 unknown_proof.
	if _, reason, status, err := reloaded.Verify("ask002", "prf-000000000000", "Accept and merge"); !errors.Is(err, ErrProofRejected) || status != http.StatusNotFound || reason != "unknown_proof" {
		t.Fatalf("Verify unknown proof: status=%d reason=%q err=%v", status, reason, err)
	}

	// Advance past 24h retention: proof expires cleanly.
	now = now.Add(DefaultProofRetention + time.Second)
	if _, reason, status, err := reloaded.Verify("ask002", proof.ProofID, "Accept and merge"); !errors.Is(err, ErrProofRejected) || status != http.StatusNotFound || reason != "unknown_proof" {
		t.Fatalf("Verify expired proof: status=%d reason=%q err=%v", status, reason, err)
	}
}

func TestProofStoreRefusesLocalDeviceTypedInputAndUnknownKind(t *testing.T) {
	store, err := NewProofStore("", func() time.Time { return time.UnixMilli(1763301030000) })
	if err != nil {
		t.Fatalf("NewProofStore: %v", err)
	}

	// Ticketless local device must never mint a proof.
	for _, dev := range []string{"", "local", "  "} {
		_, err := store.Mint(MintInput{
			AskID:       "ask002",
			Project:     "beacon",
			Kind:        AskKindGate,
			OptionIndex: 1,
			OptionLabel: "Accept and merge",
			InputKind:   "tap",
			InputSeq:    1,
			DeviceID:    dev,
		})
		if !errors.Is(err, ErrProofRejected) {
			t.Fatalf("Mint(device=%q) err = %v, want ErrProofRejected", dev, err)
		}
	}

	// Typed input must never mint a proof.
	_, err = store.Mint(MintInput{
		AskID:       "ask002",
		Project:     "beacon",
		Kind:        AskKindGate,
		OptionIndex: 1,
		OptionLabel: "Accept and merge",
		InputKind:   "typed",
		InputSeq:    1,
		DeviceID:    "dev-iphone-1",
	})
	if !errors.Is(err, ErrProofRejected) {
		t.Fatalf("Mint(typed) err = %v, want ErrProofRejected", err)
	}

	// Unknown ask kind must be refused with ErrUnknownAskKind.
	_, err = store.Mint(MintInput{
		AskID:       "ask002",
		Project:     "beacon",
		Kind:        AskKind("deploy"),
		OptionIndex: 1,
		OptionLabel: "Accept and merge",
		InputKind:   "tap",
		InputSeq:    1,
		DeviceID:    "dev-iphone-1",
	})
	if !errors.Is(err, ErrUnknownAskKind) {
		t.Fatalf("Mint(unknown kind) err = %v, want ErrUnknownAskKind", err)
	}

	if store.Count() != 0 {
		t.Fatalf("Count = %d, want 0 minted proofs", store.Count())
	}
}

func TestProofStorePreservesExactPaddedOptionLabel(t *testing.T) {
	store, err := NewProofStore("", func() time.Time { return time.UnixMilli(1763301030000) })
	if err != nil {
		t.Fatalf("NewProofStore: %v", err)
	}
	proof, err := store.Mint(MintInput{
		AskID:       "ask002",
		Project:     "beacon",
		Kind:        AskKindGate,
		OptionIndex: 1,
		OptionLabel: " Accept ",
		InputKind:   "tap",
		InputSeq:    3,
		DeviceID:    "dev-iphone-1",
	})
	if err != nil {
		t.Fatalf("Mint(padded option): %v", err)
	}
	if _, _, status, err := store.Verify("ask002", proof.ProofID, " Accept "); err != nil || status != http.StatusOK {
		t.Fatalf("Verify exact padded option: status=%d err=%v", status, err)
	}
	if _, reason, status, err := store.Verify("ask002", proof.ProofID, "Accept"); !errors.Is(err, ErrProofRejected) || status != http.StatusForbidden || reason != "option_mismatch" {
		t.Fatalf("Verify trimmed mismatch: status=%d reason=%q err=%v", status, reason, err)
	}
}
