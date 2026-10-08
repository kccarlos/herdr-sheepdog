package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/feedback"
)

// FeedbackRequest is the body of POST /api/feedback: the user marked a
// moment of the conversation (docs/ws-contract.md "Feedback and drains").
type FeedbackRequest struct {
	// Tag is one of feedback.Tags; empty means "paused".
	Tag string `json:"tag"`
	// Note is optional free text (at most feedback.MaxNote runes kept).
	Note string `json:"note,omitempty"`
	// ClientTS is when the user tapped, RFC 3339 on the app's clock.
	ClientTS string `json:"client_ts,omitempty"`
	// ConversationTurn optionally names the conversation item the user
	// was looking at (the app's id for it).
	ConversationTurn string `json:"conversation_turn,omitempty"`
}

// FeedbackResponse answers POST /api/feedback.
type FeedbackResponse struct {
	OK bool   `json:"ok"`
	ID string `json:"id"`
	// TS is the server time the mark was logged at.
	TS time.Time `json:"ts"`
}

// handleFeedback logs a user.feedback event carrying the snapshot of the
// last 30 s (feedback.Build): `sheepdog feedback` diagnoses it later.
func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	var req FeedbackRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if req.Tag == "" {
		req.Tag = feedback.TagPaused
	}
	if !feedback.ValidTag(req.Tag) {
		writeError(w, http.StatusBadRequest, "unknown tag "+strconv.Quote(req.Tag))
		return
	}
	now := s.deps.Now()
	p := feedback.Payload{
		ID:               "fb-" + strconv.FormatInt(now.UnixMilli(), 36),
		Tag:              req.Tag,
		Note:             feedback.TruncateNote(req.Note),
		ServerTS:         now.UTC(),
		ConversationTurn: req.ConversationTurn,
	}
	if req.ClientTS != "" {
		ct, err := time.Parse(time.RFC3339Nano, req.ClientTS)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad client_ts: "+err.Error())
			return
		}
		p.ClientTS = req.ClientTS
		skew := now.Sub(ct).Milliseconds()
		p.SkewMs = &skew
	}
	p.Snapshot = feedback.Build(now, s.voiceProvider(), s.voiceModel(), s.audio.Since(now.Add(-feedback.Window)), s.deps.Log.Tail(0))
	s.log.Log(logSource, feedback.EventType, "", p)
	writeJSON(w, http.StatusOK, FeedbackResponse{OK: true, ID: p.ID, TS: p.ServerTS})
}
