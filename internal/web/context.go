package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
)

// The Context tab's API (docs/dev/architecture.md "Context capture"): the
// orchestrator's exact request at a step, the voice model's reconstructed
// context at a turn, and the timeline of both. The tab follows live on
// the context.step event.

// BrainContextResponse is the body of GET /api/context/brain.
type BrainContextResponse struct {
	// Step is the step's metadata (turn, kind, provider/model, time,
	// tokens, usage).
	Step ctxcap.BrainStep `json:"step"`
	// Source says what the pane shows ("exact request").
	Source   string           `json:"source"`
	Sections []ctxcap.Section `json:"sections"`
	// Body is the provider-native request.
	Body json.RawMessage `json:"body"`
	// Tokens are the estimated tokens per class, and the total.
	Tokens map[string]int `json:"tokens"`
	Diff   ctxcap.Diff    `json:"diff"`
}

// VoiceContextResponse is the body of GET /api/context/voice.
type VoiceContextResponse struct {
	Turn ctxcap.VoiceTurn `json:"turn"`
	// Source says what the pane shows ("reconstructed from what the
	// daemon sent").
	Source   string           `json:"source"`
	Evicted  bool             `json:"evicted,omitempty"`
	Sections []ctxcap.Section `json:"sections"`
	Tokens   map[string]int   `json:"tokens"`
	Diff     ctxcap.Diff      `json:"diff"`
}

// TimelineResponse is the body of GET /api/context/timeline: every orchestrator
// step and voice turn held, oldest first, for the scrubber to interleave
// by time.
type TimelineResponse struct {
	Brain []ctxcap.BrainStep `json:"brain"`
	Voice []ctxcap.VoiceTurn `json:"voice"`
	// WindowTokens is the orchestrator's budget (brain.window_tokens).
	WindowTokens int `json:"window_tokens"`
	// BrainOff / VoiceOff say why a pane has nothing (empty when on).
	BrainOff string `json:"brain_off,omitempty"`
	VoiceOff string `json:"voice_off,omitempty"`
}

// pointParam parses ?<name>=<n|latest> (0: latest; -1: bad).
func pointParam(r *http.Request, name string) int {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" || v == "latest" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return -1
	}
	return n
}

// tokensByClass sums the sections' estimated tokens per class, with the
// total under "total".
func tokensByClass(secs []ctxcap.Section) map[string]int {
	out := map[string]int{"total": 0}
	for _, s := range secs {
		out[s.Class] += s.Tokens
		out["total"] += s.Tokens
	}
	return out
}

// handleContextBrain answers the orchestrator's request at ?step= (default the
// latest), with its diff against the step before it.
func (s *Server) handleContextBrain(w http.ResponseWriter, r *http.Request) {
	b, ok := s.brainOrUnavailable(w)
	if !ok {
		return
	}
	n := pointParam(r, "step")
	if n < 0 {
		writeError(w, http.StatusBadRequest, "step: a positive number or latest")
		return
	}
	cur, prev, hasPrev, err := b.Requests().Get(n)
	if errors.Is(err, ctxcap.ErrNoStep) {
		writeError(w, http.StatusNotFound, "no such step (the orchestrator has made no model call yet, or it is older than the last "+strconv.Itoa(ctxcap.BrainSteps)+")")
		return
	}
	secs := cur.WithTexts()
	writeJSON(w, http.StatusOK, BrainContextResponse{
		Step: cur.Meta(), Source: "exact request", Sections: secs, Body: cur.Body,
		Tokens: tokensByClass(secs), Diff: ctxcap.DiffBrain(cur, prev, hasPrev),
	})
}

// voiceOff says why there is no voice context log ("" when there is).
func (s *Server) voiceOff() string {
	if s.deps.VoiceContext == nil {
		return "no voice context log"
	}
	return ""
}

// handleContextVoice answers the voice model's reconstructed context at
// ?turn= (default the latest), with its diff against the turn before.
func (s *Server) handleContextVoice(w http.ResponseWriter, r *http.Request) {
	if off := s.voiceOff(); off != "" {
		writeError(w, http.StatusServiceUnavailable, off)
		return
	}
	n := pointParam(r, "turn")
	if n < 0 {
		writeError(w, http.StatusBadRequest, "turn: a positive number or latest")
		return
	}
	cur, prev, hasPrev, err := s.deps.VoiceContext.Get(n)
	if errors.Is(err, ctxcap.ErrNoStep) {
		writeError(w, http.StatusNotFound, "no such turn (nothing was sent to a voice model yet, or it is older than the last "+strconv.Itoa(ctxcap.VoiceTurns)+")")
		return
	}
	secs := cur.WithTexts()
	writeJSON(w, http.StatusOK, VoiceContextResponse{
		Turn: cur.VoiceTurn, Source: "reconstructed from what the daemon sent", Evicted: cur.Evicted,
		Sections: secs, Tokens: tokensByClass(secs), Diff: ctxcap.DiffVoice(cur, prev, hasPrev),
	})
}

// handleContextTimeline answers the orchestrator steps and voice turns held.
func (s *Server) handleContextTimeline(w http.ResponseWriter, r *http.Request) {
	resp := TimelineResponse{Brain: []ctxcap.BrainStep{}, Voice: []ctxcap.VoiceTurn{}}
	if b := s.deps.Brain; b != nil {
		if steps := b.Requests().Steps(); len(steps) > 0 {
			resp.Brain = steps
		}
		resp.WindowTokens = b.Config().WindowTokens
	} else {
		resp.BrainOff = s.deps.BrainOffReason
		if resp.BrainOff == "" {
			resp.BrainOff = "not configured"
		}
	}
	if vc := s.deps.VoiceContext; vc != nil {
		if turns := vc.Turns(); len(turns) > 0 {
			resp.Voice = turns
		}
	} else {
		resp.VoiceOff = s.voiceOff()
	}
	writeJSON(w, http.StatusOK, resp)
}
