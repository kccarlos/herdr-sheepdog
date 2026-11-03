package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// Attention: seen, pinned and space mute (docs/dev/ws-contract.md
// "Attention", features attention and space-mute). The marks belong to the
// agent's terminal and live in herdr (labels) or in the daemon's own store;
// a space mute is kept by the space's folder. Each call is logged as
// web.agent_action or web.space_action with metadata only, and a change is
// told to every client as agent.attention or space.muted on /events.

// Attention is the optional half of Supervisor behind the seen, pin and
// space mute routes and /api/state's attention fields. A Supervisor
// without it (or whose HasAttention and HasSpaceMutes are false) serves
// none of them, and the matching features are not listed.
type Attention interface {
	HasAttention() bool
	HasSpaceMutes() bool
	AttentionState(ctx context.Context, agents []state.Agent) supervisor.AttentionState
	MarkSeen(ctx context.Context, paneID string, turn *uint64, unread bool, via string) (supervisor.AgentAttention, error)
	SetPinned(ctx context.Context, paneID string, pinned bool, via string) (supervisor.AgentAttention, error)
	SetSpaceMute(ctx context.Context, spaceID string, muted bool, until *time.Time, via string) (supervisor.SpaceMuteResult, error)
	PaneMuted(paneID string) bool
}

// AgentView is one agent of GET /api/state: the state table's row and, from a
// daemon with the attention feature, its attention. state.Agent and
// state.json do not change.
type AgentView struct {
	state.Agent
	Attention *supervisor.AgentAttention `json:"attention,omitempty"`
}

// attentionHost is the supervisor's attention half, when it has one.
func (s *Server) attentionHost() (Attention, bool) {
	a, ok := s.deps.Supervisor.(Attention)
	if !ok || s.deps.Supervisor == nil {
		return nil, false
	}
	return a, true
}

// agentViews adds each agent's attention and the top-level attention_counts
// and space_mutes to /api/state. Without the feature the agents go out as
// they were and the other two are nil.
func (s *Server) agentViews(ctx context.Context, agents []state.Agent) ([]AgentView, *supervisor.AttentionCounts, []supervisor.SpaceMuteInfo) {
	views := make([]AgentView, len(agents))
	for i, a := range agents {
		views[i].Agent = a
	}
	host, ok := s.attentionHost()
	if !ok {
		return views, nil, nil
	}
	var counts *supervisor.AttentionCounts
	var mutes []supervisor.SpaceMuteInfo
	if host.HasAttention() {
		st := host.AttentionState(ctx, agents)
		for i, a := range agents {
			if att, ok := st.Agents[a.PaneID]; ok {
				views[i].Attention = &att
			}
		}
		counts = &st.Counts
		if host.HasSpaceMutes() {
			mutes = st.SpaceMutes
		}
	}
	return views, counts, mutes
}

// via names who made a change: this computer itself is the panel, a paired
// device the phone.
func via(r *http.Request) string {
	if deviceOf(r) == localDevice {
		return supervisor.ViaPanel
	}
	return supervisor.ViaPhone
}

// attentionStatus maps an attention error to its HTTP status.
func attentionStatus(err error) int {
	switch {
	case errors.Is(err, supervisor.ErrNoSuchAgent), errors.Is(err, herdr.ErrPaneNotFound):
		return http.StatusNotFound
	default:
		return actionStatus(err)
	}
}

// markHost is the attention half when the marks are kept here, else a 503.
func (s *Server) markHost(w http.ResponseWriter) (Attention, bool) {
	a, ok := s.attentionHost()
	if !ok || !a.HasAttention() {
		writeError(w, http.StatusServiceUnavailable, "seen and pinned marks are not available here")
		return nil, false
	}
	return a, true
}

// handleAgentSeen is POST /api/agents/{pane_id}/seen {"turn": n} (the turn the
// client showed) or {"unread": true} (Mark as unread).
func (s *Server) handleAgentSeen(w http.ResponseWriter, r *http.Request) {
	a, ok := s.markHost(w)
	if !ok {
		return
	}
	var req struct {
		Turn   *uint64 `json:"turn"`
		Unread *bool   `json:"unread"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	unread := req.Unread != nil && *req.Unread
	switch {
	case req.Turn != nil && req.Unread != nil:
		writeError(w, http.StatusBadRequest, "give the turn or unread, not both")
		return
	case req.Turn == nil && !unread:
		writeError(w, http.StatusBadRequest, `give the turn the client showed ({"turn": n}), or {"unread": true}`)
		return
	}
	pane := r.PathValue("pane_id")
	ctx, cancel := s.toolCtx()
	defer cancel()
	att, err := a.MarkSeen(ctx, pane, req.Turn, unread, via(r))
	action := "seen"
	if unread {
		action = "unread"
	}
	s.attentionDone(w, pane, action, att, err)
}

// handleAgentPin is POST /api/agents/{pane_id}/pin {"pinned": bool}.
func (s *Server) handleAgentPin(w http.ResponseWriter, r *http.Request) {
	a, ok := s.markHost(w)
	if !ok {
		return
	}
	var req struct {
		Pinned *bool `json:"pinned"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if req.Pinned == nil {
		writeError(w, http.StatusBadRequest, "pinned must be a boolean")
		return
	}
	pane := r.PathValue("pane_id")
	ctx, cancel := s.toolCtx()
	defer cancel()
	att, err := a.SetPinned(ctx, pane, *req.Pinned, via(r))
	s.attentionDone(w, pane, "pin", att, err)
}

func (s *Server) attentionDone(w http.ResponseWriter, pane, action string, att supervisor.AgentAttention, err error) {
	fields := map[string]any{"action": action}
	if err != nil {
		fields["error"] = err.Error()
		s.log.Log(logSource, "web.agent_action", pane, fields)
		writeError(w, attentionStatus(err), err.Error())
		return
	}
	s.log.Log(logSource, "web.agent_action", pane, fields)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "attention": att})
}

// handleSpaceMute is POST /api/spaces/{id}/mute {"muted": bool, "until"?:
// RFC 3339}. A space is muted by its folder; muted false removes the mute.
func (s *Server) handleSpaceMute(w http.ResponseWriter, r *http.Request) {
	a, ok := s.attentionHost()
	if !ok || !a.HasSpaceMutes() {
		writeError(w, http.StatusServiceUnavailable, "space mutes are not available here")
		return
	}
	var req struct {
		Muted *bool      `json:"muted"`
		Until *time.Time `json:"until"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	switch {
	case req.Muted == nil:
		writeError(w, http.StatusBadRequest, "muted must be a boolean")
		return
	case !*req.Muted && req.Until != nil:
		writeError(w, http.StatusBadRequest, "until goes with muted: true")
		return
	case req.Until != nil && !req.Until.After(s.deps.Now()):
		writeError(w, http.StatusBadRequest, "until is in the past")
		return
	}
	id := r.PathValue("id")
	ctx, cancel := s.toolCtx()
	defer cancel()
	res, err := a.SetSpaceMute(ctx, id, *req.Muted, req.Until, via(r))
	fields := map[string]any{"action": "mute", "space": id, "muted": *req.Muted}
	if err != nil {
		fields["error"] = err.Error()
		s.log.Log(logSource, "web.space_action", "", fields)
		writeError(w, actionStatus(err), err.Error())
		return
	}
	s.log.Log(logSource, "web.space_action", "", fields)
	space := map[string]any{"id": res.ID, "path": res.Path, "muted": res.Muted}
	if res.Until != nil {
		space["until"] = res.Until
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "space": space})
}
