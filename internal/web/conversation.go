package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// GET /api/conversation: the conversation between the user and the
// voice, rebuilt server-side from the event log across day files and
// restarts (docs/dev/ws-contract.md "History and resume"), newest last and
// paged backwards by event id. The merging rules are
// web/debug/conversation.js's, for the kinds a chat view shows:
//
//	user  voice.transcript.in fragments merged per user turn (via
//	          voice), or typed text: voice.text, web.utterance other than
//	          via=voice (that one is followed by voice.text) — via typed
//	voice       voice.transcript.out fragments merged per model turn, or a
//	          tool result spoken without a voice model (voice.tool_result
//	          with a via other than voice)
//	push      voice.push that is not silent (the skipped pushes of
//	          --voice=off included: they are what the voice would say)
//	approval  pending.created of an answer_prompt, with its resolution
//	          (pending.confirmed / expired / cancelled / replaced)
//	notice    daemon.start and the voice session's reconnects, as dividers
//
// A fragment run is closed by the other side speaking, a tool call, a
// push, a voice turn boundary, an interruption (voice) or a divider.

// ConversationItem is one entry of GET /api/conversation. ID is the
// event id of its first event (what ?before= pages by), TS its time.
type ConversationItem struct {
	ID   int64     `json:"id"`
	TS   time.Time `json:"ts"`
	Kind string    `json:"kind"`
	// Subkind narrows a notice: "reset" for a New-conversation divider
	// (conversation.reset), "" for the others (daemon start, voice
	// reconnects/closes).
	Subkind string `json:"subkind,omitempty"`
	Text    string `json:"text"`
	// Via is how the user spoke (voice | typed) or how an approval
	// was answered (tap | fast_path | orchestrator; confirm in logs from before
	// the confirm tool was removed).
	Via     string `json:"via,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Urgency string `json:"urgency,omitempty"`
	// PendingID is the question an approval item is (what
	// POST /api/pending/answer takes while it is open).
	PendingID string                     `json:"pending_id,omitempty"`
	Options   []supervisor.PendingOption `json:"options,omitempty"`
	Answered  *bool                      `json:"answered,omitempty"`
	Answer    string                     `json:"answer,omitempty"`
	// Resolution is an approval's fate: open, confirmed, expired,
	// cancelled or replaced.
	Resolution string `json:"resolution,omitempty"`
}

// ConversationResponse is the body of GET /api/conversation. NextBefore
// is the ?before= of the next older page; null when there is none.
type ConversationResponse struct {
	Items      []ConversationItem `json:"items"`
	NextBefore *int64             `json:"next_before"`
}

// Conversation paging bounds.
const (
	defaultConversationLimit = 200
	maxConversationLimit     = 1000
	// resolutionMargin is how far past ?before= the log is still read so
	// an approval near the page edge carries its resolution (a pending
	// action lives pending_ttl_s, minutes).
	resolutionMargin = time.Hour
)

// handleConversation serves GET /api/conversation?limit=&before=.
func (s *Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = defaultConversationLimit
	}
	limit = min(limit, maxConversationLimit)
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	items, next, err := s.conversation(before, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := ConversationResponse{Items: items}
	if next > 0 {
		resp.NextBefore = &next
	}
	writeJSON(w, http.StatusOK, resp)
}

// conversation returns the newest limit items older than before (0: no
// bound), oldest first, and the id to page from next (0: no older page).
// Day files are read newest first until enough items precede before.
func (s *Server) conversation(before int64, limit int) ([]ConversationItem, int64, error) {
	upper := int64(0) // read events with id < upper (0: all)
	if before > 0 {
		upper = before + resolutionMargin.Microseconds()
	}
	days, err := s.deps.Log.Days()
	if err != nil {
		return nil, 0, err
	}
	if len(days) == 0 {
		// An in-memory log: the ring is all there is.
		items := buildConversation(relevant(s.deps.Log.Tail(0), upper))
		page, next := pageItems(items, before, limit, false)
		return page, next, nil
	}
	end := len(days) - 1
	if upper > 0 {
		last := time.UnixMicro(upper).UTC().Format("2006-01-02")
		for end >= 0 && days[end] > last {
			end--
		}
	}
	var events []eventlog.Event
	for i := end; i >= 0; i-- {
		evs, err := s.deps.Log.ReadDay(days[i])
		if err != nil {
			return nil, 0, err
		}
		events = append(relevant(evs, upper), events...)
		items := buildConversation(events)
		if countBefore(items, before) > limit || i == 0 {
			page, next := pageItems(items, before, limit, i > 0)
			return page, next, nil
		}
	}
	return []ConversationItem{}, 0, nil
}

// countBefore counts the items older than before (all when before is 0).
func countBefore(items []ConversationItem, before int64) int {
	n := 0
	for _, it := range items {
		if before == 0 || it.ID < before {
			n++
		}
	}
	return n
}

// pageItems cuts the newest limit items older than before; next is the
// oldest returned item's id when older items (or unread days) remain.
func pageItems(items []ConversationItem, before int64, limit int, moreDays bool) ([]ConversationItem, int64) {
	out := make([]ConversationItem, 0, min(len(items), limit))
	for _, it := range items {
		if before == 0 || it.ID < before {
			out = append(out, it)
		}
	}
	more := moreDays
	if len(out) > limit {
		out, more = out[len(out)-limit:], true
	}
	var next int64
	if more && len(out) > 0 {
		next = out[0].ID
	}
	return out, next
}

// conversationTypes are the event types the builder reads.
var conversationTypes = map[string]bool{
	"daemon.start": true, "web.utterance": true, "conversation.reset": true,
	"pending.created": true, "pending.confirmed": true, "pending.expired": true, "pending.cancelled": true, "pending.replaced": true,
}

// relevant keeps the events the builder reads, with id < upper (0: all).
func relevant(evs []eventlog.Event, upper int64) []eventlog.Event {
	out := make([]eventlog.Event, 0, len(evs)/4)
	for _, ev := range evs {
		if upper > 0 && ev.ID >= upper {
			continue
		}
		if conversationTypes[ev.Type] || strings.HasPrefix(ev.Type, "voice.") {
			out = append(out, ev)
		}
	}
	return out
}

// convoBuilder folds events into items (conversation.js's Grouper, cut
// down to the chat kinds).
type convoBuilder struct {
	items     []*ConversationItem
	user, dog *ConversationItem // fragment runs in progress
	approvals map[string]*ConversationItem
}

// buildConversation folds events (oldest first) into items.
func buildConversation(evs []eventlog.Event) []ConversationItem {
	b := &convoBuilder{approvals: map[string]*ConversationItem{}}
	for _, ev := range evs {
		b.add(ev)
	}
	out := make([]ConversationItem, 0, len(b.items))
	for _, it := range b.items {
		it.Text = strings.TrimSpace(it.Text)
		if it.Text == "" && it.Kind != "approval" {
			continue
		}
		out = append(out, *it)
	}
	return out
}

// convoPayload is the union of the payload fields the builder reads.
type convoPayload struct {
	Text      string          `json:"text"`
	Final     bool            `json:"final"`
	Via       string          `json:"via"`
	Spoken    string          `json:"spoken"`
	Cancelled bool            `json:"cancelled"`
	Urgency   string          `json:"urgency"`
	Agent     string          `json:"agent"`
	ID        string          `json:"id"`
	OldID     string          `json:"old_id"`
	Kind      string          `json:"kind"`
	Summary   string          `json:"summary"`
	Question  string          `json:"question"`
	Answer    string          `json:"answer"`
	Reason    string          `json:"reason"`
	Resumed   bool            `json:"resumed"`
	Options   json.RawMessage `json:"options"`
}

func (b *convoBuilder) add(ev eventlog.Event) {
	var p convoPayload
	if len(ev.Payload) > 0 {
		_ = json.Unmarshal(ev.Payload, &p)
	}
	switch ev.Type {
	case "daemon.start":
		b.closeAll()
		b.push(ev, "notice", "Sheepdog started.")
	case "conversation.reset":
		b.closeAll()
		b.push(ev, "notice", "New conversation").Subkind = "reset"
	case "voice.resume":
		b.closeAll()
		if p.Resumed {
			b.push(ev, "notice", "Voice reconnected; the conversation continues.")
		} else {
			b.push(ev, "notice", "Voice reconnected with a fresh session; earlier context was lost.")
		}
	case "voice.handle_dropped":
		b.closeAll()
		b.push(ev, "notice", "Voice context lost.")
	case "voice.closed":
		b.closeAll()
		text := "Voice session closed."
		if p.Reason != "" {
			text = "Voice session closed: " + p.Reason + "."
		}
		b.push(ev, "notice", text)
	case "voice.connect", "voice.connect_error", "voice.recycle", "voice.stalled", "voice.disconnected", "voice.goaway":
		b.closeAll() // dividers in /debug; not items here
	case "voice.transcript.in":
		b.dog = nil
		if b.user == nil {
			b.user = b.push(ev, "shepherd", "")
			b.user.Via = "voice"
		}
		b.user.Text += p.Text
		if p.Final {
			b.user = nil
		}
	case "voice.text":
		b.closeAll()
		b.push(ev, "shepherd", p.Text).Via = "typed"
	case "web.utterance":
		if p.Via == "voice" {
			return // voice.text follows: the model path
		}
		b.closeAll()
		b.push(ev, "shepherd", p.Text).Via = "typed"
	case "voice.transcript.out":
		b.user = nil
		if b.dog == nil {
			b.dog = b.push(ev, "dog", "")
		}
		b.dog.Text += p.Text
		if p.Final {
			b.dog = nil
		}
	case "voice.tool_call", "voice.turn":
		b.closeAll()
	case "voice.interrupted":
		b.dog = nil
	case "voice.tool_result":
		// Without a voice model the daemon speaks the result itself.
		if p.Via != "" && p.Via != "voice" && !p.Cancelled && strings.TrimSpace(p.Spoken) != "" {
			b.dog = nil
			b.push(ev, "dog", p.Spoken)
		}
	case "voice.push":
		b.closeAll()
		if p.Urgency == "silent" {
			return
		}
		it := b.push(ev, "push", p.Text)
		it.Urgency, it.Agent = p.Urgency, p.Agent
	case "pending.created":
		if p.Kind != "answer_prompt" {
			return
		}
		text := p.Summary
		if text == "" {
			text = p.Question
		}
		it := b.push(ev, "approval", text)
		answered := false
		it.Agent, it.PendingID, it.Answered, it.Resolution = p.Agent, p.ID, &answered, "open"
		// Older lines carry a count, not the options.
		_ = json.Unmarshal(p.Options, &it.Options)
		b.approvals[p.ID] = it
	case "pending.confirmed":
		if it := b.approvals[p.ID]; it != nil {
			answered := true
			it.Answered, it.Answer, it.Via, it.Resolution = &answered, p.Answer, p.Via, "confirmed"
		}
	case "pending.expired":
		b.resolve(p.ID, "expired")
	case "pending.cancelled":
		b.resolve(p.ID, "cancelled")
	case "pending.replaced":
		b.resolve(p.OldID, "replaced")
	}
}

// resolve marks an open approval with how it ended unanswered.
func (b *convoBuilder) resolve(id, how string) {
	if it := b.approvals[id]; it != nil && it.Resolution == "open" {
		it.Resolution = how
	}
}

func (b *convoBuilder) push(ev eventlog.Event, kind, text string) *ConversationItem {
	it := &ConversationItem{ID: ev.ID, TS: ev.TS, Kind: kind, Text: text}
	b.items = append(b.items, it)
	return it
}

func (b *convoBuilder) closeAll() { b.user, b.dog = nil, nil }

// conversationResetRequest is the body of POST /api/conversation/reset.
type conversationResetRequest struct {
	Via string `json:"via"`
}

// conversationResetVias are the accepted "via" values.
var conversationResetVias = map[string]bool{"app": true, "debug": true, "voice_page": true}

// handleConversationReset resets the orchestrator's transcript window and the
// voice session's memory ("New conversation", docs/dev/roles.md): body
// {"via":"app"|"debug"|"voice_page"} → {"ok":true,"reset_id":"…",
// "at":"…"}. Logs conversation.reset (supervisor.ResetConversation);
// GET /api/conversation shows a "New conversation" divider at this
// point and the SSE stream carries the event live.
func (s *Server) handleConversationReset(w http.ResponseWriter, r *http.Request) {
	if s.deps.Supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "no supervisor")
		return
	}
	var req conversationResetRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if !conversationResetVias[req.Via] {
		writeError(w, http.StatusBadRequest, `via must be "app", "debug" or "voice_page"`)
		return
	}
	ctx, cancel := s.toolCtx()
	defer cancel()
	id, at, err := s.deps.Supervisor.ResetConversation(ctx, req.Via)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reset_id": id, "at": at.UTC().Format(time.RFC3339)})
}
