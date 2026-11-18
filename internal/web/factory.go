package web

import (
	"net/http"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
)

type proofRejectedBody struct {
	OK       bool   `json:"ok"`
	Verified bool   `json:"verified"`
	Code     string `json:"code"`
	Error    string `json:"error"`
}

func writeProofRejected(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, proofRejectedBody{
		OK:       false,
		Verified: false,
		Code:     factory.CodeProofRejected,
		Error:    msg,
	})
}

// handleFactoryAnswerProof serves `GET /api/factory/answer-proof/{ask_id}?proof=<id>&option=<label>`
// strictly on untunnelled local loopback (docs/dev/factory/owner-contract.md §5.2 item 4).
func (s *Server) handleFactoryAnswerProof(w http.ResponseWriter, r *http.Request) {
	askID := r.PathValue("ask_id")
	proofID := r.URL.Query().Get("proof")
	optionLabel := r.URL.Query().Get("option")

	// Belt-and-braces local loopback check alongside accessLoopback: reject any
	// non-loopback peer, spoofed Host, reverse-proxy header, or active Funnel.
	var untrusted string
	switch {
	case !isLoopbackAddr(r.RemoteAddr) || s.kindOf(r.RemoteAddr) != "loopback":
		untrusted = "not_loopback"
	case !isLoopbackHost(r.Host):
		untrusted = "host"
	default:
		untrusted = s.isUntrustedLoopback(r)
	}
	if untrusted != "" {
		s.log.Log("factory", "factory.proof_rejected", "", map[string]any{
			"ask_id": askID,
			"reason": untrusted,
			"remote": r.RemoteAddr,
		})
		writeProofRejected(w, http.StatusForbidden, "answer proof verification requires a direct local loopback request")
		return
	}

	if s.deps.FactoryProofs == nil {
		s.log.Log("factory", "factory.proof_rejected", "", map[string]any{
			"ask_id": askID,
			"reason": "proof_store_unavailable",
		})
		writeProofRejected(w, http.StatusForbidden, "answer proof store is not configured")
		return
	}

	proof, reason, status, err := s.deps.FactoryProofs.Verify(askID, proofID, optionLabel)
	if err != nil {
		s.log.Log("factory", "factory.proof_rejected", "", map[string]any{
			"ask_id":   askID,
			"proof_id": proofID,
			"reason":   reason,
			"error":    err.Error(),
		})
		writeProofRejected(w, status, err.Error())
		return
	}

	s.log.Log("factory", "factory.proof_verified", "factory:"+proof.Project, map[string]any{
		"ask_id":       proof.AskID,
		"proof_id":     proof.ProofID,
		"kind":         string(proof.Kind),
		"option_index": proof.OptionIndex,
		"input_kind":   proof.InputKind,
		"device_id":    proof.DeviceID,
	})
	writeJSON(w, http.StatusOK, proof)
}
