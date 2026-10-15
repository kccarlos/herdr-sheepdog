package gemini

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The briefing channel (docs/dev/design/dual-brain.md): everything the
// daemon and the orchestrator tell the voice model — answers, worker
// news, context — reaches it as a function response, never as a
// user-role turn. Gemini Live has no mid-session system role; a
// clientContent turn is, on the wire, the user speaking, and the voice
// took pushes for requests ("On it.", a push relayed back as the user's
// words). A function response is a tool's result: the model knows it is
// not the user, and its scheduling (WHEN_IDLE, INTERRUPT, SILENT) is the
// one Live offers.
//
// The function response answers a call the model never made: an id the
// daemon makes up ("b-7") under the name "briefing", which is not among
// the declared tools. Live probes on Vertex gemini-3.8-live, 2026-10-04
// (5 sessions): every such response was accepted; WHEN_IDLE was voiced
// at the next pause, INTERRUPT cut a long answer and was voiced at once,
// SILENT was remembered and used for a later question, a Chinese
// briefing was voiced in the conversation's language, and the session
// kept issuing resumable handles. The alternatives failed: willContinue
// on an open call is rejected by Vertex (close 1007, unknown field), and
// an always-open "updates" call the model must re-open was not re-opened
// after its second answer.
//
// Each briefing is self-contained, because Live's memory is short and
// lossy: who it comes from, the time, what it is, and how it relates to
// earlier briefings (answers the user's words of 12:03:10; updates b-4
// about the same agent; the 2nd briefing about the same prompt; the user
// cut off the previous line after 2 s). Facts, not a script: the voice
// decides whether, when and how to say it.

// BriefingName is the function name briefings arrive under.
const BriefingName = "briefing"

// Briefing channel names (Config.BriefingChannel).
const (
	// ChannelFunctionResponse delivers briefings as function responses
	// (the default).
	ChannelFunctionResponse = "function_response"
	// ChannelUserTurn delivers them as the older marked user-role
	// client turns (pushTurn), kept as the A/B control.
	ChannelUserTurn = "user_turn"
)

// briefingLabel opens every briefing: provenance, the way Claude Code
// labels a message relayed from another agent.
const briefingLabel = "Not the user speaking. Facts for you from the orchestrator or the daemon; you decide whether, when and how to tell the user, in your own words."

// briefingKeep is how many briefings the ledger remembers for relations
// and for re-sending after a resume.
const briefingKeep = 48

// briefRec is one briefing sent to the model.
type briefRec struct {
	id string
	at time.Time
	// factsAt is when the facts were written (the push was queued): a
	// push may wait in the queue while the user talks or no phone
	// listens, so it can reach the model well after it was true.
	factsAt time.Time
	urgency voice.Urgency
	kind    string
	about   string
	pending string
	answers string
	payload map[string]any
	// spoken: the model was asked to speak it (not SILENT).
	spoken bool
}

// briefLedger is the session's record of briefings, under s.mu.
type briefLedger struct {
	seq  int
	recs []briefRec
	// handleAt is when the last resumable handle arrived: briefings sent
	// after it are not in the context a resume restores.
	handleAt time.Time
	// cut is the user's last barge-in over a spoken line, told in the
	// next briefing when the silent one has not gone yet.
	cut string
	// flushBy and flushAt are who last flushed the output (interrupt:
	// "model" when Live heard the user, "push" when the daemon made way
	// for an urgent briefing) and when.
	flushBy string
	flushAt time.Time
	// watch is the spoken briefing awaiting the model's speech;
	// unvoiced the facts about briefings that drew none, carried to the
	// next spoken briefing or answer (unvoiced.go).
	watch    *voiceWatch
	unvoiced []string
}

// clock renders t for a briefing: the local wall-clock time.
func clock(t time.Time) string { return t.Format("15:04:05") }

// briefingKind names what a push is, from its tags.
func briefingKind(tags map[string]any, u voice.Urgency) string {
	switch {
	case tags["kind"] != nil:
		return fmt.Sprint(tags["kind"])
	case voice.IsFocus(tags):
		return "current focus"
	case tags[voice.TagAnswersUtterance] != nil:
		return "answer"
	case tags["answers_job"] != nil:
		return "result of an investigation the orchestrator started"
	case tags["stop"] == true:
		return "stop result"
	case u == voice.UrgencySilent:
		return "context"
	default:
		return "news"
	}
}

// briefingFrom names who a push comes from, from its tags.
func briefingFrom(tags map[string]any) string {
	if f, ok := tags["from"].(string); ok && f != "" {
		return f
	}
	switch {
	case voice.IsFocus(tags), tags["stop"] == true:
		return "daemon"
	case tags[voice.TagAnswersUtterance] != nil, tags["answers_job"] != nil, tags["event_id"] != nil:
		return "orchestrator"
	default:
		return "supervisor"
	}
}

// newBriefingLocked builds the payload for one briefing and records it.
// factsAt is when its facts were written (zero: now). Caller holds s.mu.
func (s *Session) newBriefingLocked(text string, u voice.Urgency, tags map[string]any, factsAt time.Time) briefRec {
	now := s.now()
	if factsAt.IsZero() || factsAt.After(now) {
		factsAt = now
	}
	s.brief.seq++
	r := briefRec{
		id:      fmt.Sprintf("b-%d", s.brief.seq),
		at:      now,
		factsAt: factsAt,
		urgency: u,
		kind:    briefingKind(tags, u),
		spoken:  u != voice.UrgencySilent,
	}
	r.about, _ = tags["agent"].(string)
	r.pending, _ = tags["pending_id"].(string)
	r.answers, _ = tags[voice.TagAnswersUtterance].(string)
	p := map[string]any{
		"about_this": briefingLabel,
		"briefing":   r.id,
		"at":         clock(now),
		"from":       briefingFrom(tags),
		"kind":       r.kind,
		"facts":      text,
		"age":        ageLine(now.Sub(factsAt)),
	}
	if r.about != "" {
		p["agent"] = r.about
		if name, st, ok := s.agentNow(r.about); ok {
			p["agent"] = name
			p["agent_state"] = st
		}
	}
	s.addAnswerFactsLocked(p, tags)
	if words, _ := tags["answers_text"].(string); words != "" {
		asked, _ := tags["asked_at"].(string)
		p["answers"] = fmt.Sprintf("the user's words at %s: %q", asked, words)
	}
	if tags["needs_user"] == true {
		p["decision"] = "the user's own decision; only their words can answer it"
	}
	if r.spoken && tags["event_id"] != nil && s.cfg.DecisionsNow != nil {
		if d := s.cfg.DecisionsNow(); d != "" {
			p["decision_open"] = d
		}
	}
	if tags["due_promise"] == true {
		p["promised"] = "the user asked to be told this"
	}
	if rel := s.relationsLocked(r); len(rel) > 0 {
		p["relation"] = rel
	}
	r.payload = p
	s.brief.recs = append(s.brief.recs, r)
	if len(s.brief.recs) > briefingKeep {
		s.brief.recs = s.brief.recs[len(s.brief.recs)-briefingKeep:]
	}
	return r
}

// relationsLocked says how r relates to earlier briefings: the same
// prompt again, a newer word about the same agent, the user's last cut.
// Facts only; the voice decides what they mean.
func (s *Session) relationsLocked(r briefRec) []string {
	var out []string
	if r.pending != "" {
		n, first := 0, ""
		for _, e := range s.brief.recs {
			if e.pending == r.pending && e.spoken {
				n++
				if first == "" {
					first = e.id + " at " + clock(e.at)
				}
			}
		}
		if n > 0 && r.spoken {
			out = append(out, fmt.Sprintf("briefing number %d about the same open prompt (first: %s)", n+1, first))
		}
	}
	if r.about != "" && r.pending == "" {
		for i := len(s.brief.recs) - 1; i >= 0; i-- {
			e := s.brief.recs[i]
			if e.about == r.about && e.kind != "current focus" {
				out = append(out, fmt.Sprintf("newer than %s (%s) about %s; where they differ, this one holds", e.id, clock(e.at), r.about))
				break
			}
		}
	}
	if r.spoken && len(s.brief.unvoiced) > 0 {
		out = append(out, s.brief.unvoiced...)
		s.brief.unvoiced = nil
	}
	if s.brief.cut != "" && r.spoken {
		out = append(out, s.brief.cut)
		s.brief.cut = ""
	}
	return out
}

// noteCutLocked records that the user cut off a spoken line (a flushed
// playback run): how much of it played and what they heard. It goes
// out as a silent briefing at once and is repeated on the next spoken
// one. Caller holds s.mu.
func (s *Session) noteCutLocked(rep voice.PlaybackReport) string {
	if rep.Cut == nil || rep.RunMs <= 0 {
		return ""
	}
	heard := strings.TrimSpace(rep.Heard)
	who := "the user cut you off"
	if s.cutByPushLocked(rep.At) {
		who = "the daemon cut you off to make way for an urgent briefing"
	}
	fact := fmt.Sprintf("at %s %s after %.1f s of %.1f s", clock(rep.At), who, float64(rep.PlayedMs)/1000, float64(rep.RunMs)/1000)
	if heard != "" {
		fact += fmt.Sprintf("; they heard only: %q", heard)
	} else {
		fact += "; they heard nothing of it"
	}
	s.brief.cut = fact
	return fact
}

// cutByPushLocked reports whether a run flushed at at was cut by the
// daemon for an urgent push (within 3 s of its flush), not by the user.
// Caller holds s.mu.
func (s *Session) cutByPushLocked(at time.Time) bool {
	return s.brief.flushBy == "push" && at.Sub(s.brief.flushAt) < 3*time.Second
}

// noteBriefHandleLocked records a resumable handle for the briefing re-send. Caller holds s.mu.
func (s *Session) noteBriefHandleLocked() { s.brief.handleAt = s.now() }

// afterHandleLocked lists the briefings sent after the last resumable
// handle: a resume with that handle does not restore them (a silent
// one never completes a turn, so no newer handle follows it). Caller
// holds s.mu.
func (s *Session) afterHandleLocked() []briefRec {
	var out []briefRec
	for _, r := range s.brief.recs {
		if r.at.After(s.brief.handleAt) {
			out = append(out, r)
		}
	}
	return out
}

// briefingResponse is the function response carrying one briefing.
func briefingResponse(r briefRec, sched voice.Urgency) genai.LiveToolResponseInput {
	return genai.LiveToolResponseInput{FunctionResponses: []*genai.FunctionResponse{{
		ID: r.id, Name: BriefingName, Response: r.payload, Scheduling: scheduling(sched),
	}}}
}

// useBriefings reports whether pushes go as function responses.
func (s *Session) useBriefings() bool { return s.cfg.BriefingChannel != ChannelUserTurn }

// resendAfterResume re-sends, silently, the briefings that went out
// after the last resumable handle, once a resume restored the context
// that handle names (handover 2026-10-04: silent context sent after the
// last completed turn is lost on resume). Each is marked as re-sent
// with its original time.
func (s *Session) resendAfterResume() {
	gen := s.liveGen()
	s.mu.Lock()
	recs := s.afterHandleLocked()
	s.mu.Unlock()
	for _, r := range recs {
		rr := r
		rr.payload = s.resentPayload(r, "re-sent after a reconnect; first sent at "+clock(r.at)+"; it may already be in your context")
		err := s.sendOn(gen, func(c Conn) error { return c.SendToolResponse(briefingResponse(rr, voice.UrgencySilent)) })
		s.log.Log(logSource, "voice.briefing_resent", "", map[string]any{"briefing": r.id, "at": clock(r.at), "reason": "after_resume", "error": errString(err)})
		if err != nil {
			return
		}
	}
}

// resentPayload is a copy of r's payload marked as re-sent (note), its
// age and the agent's state told as of now.
func (s *Session) resentPayload(r briefRec, note string) map[string]any {
	p := make(map[string]any, len(r.payload)+1)
	for k, v := range r.payload {
		p[k] = v
	}
	p["resent"] = note
	if !r.factsAt.IsZero() {
		p["age"] = ageLine(s.now().Sub(r.factsAt))
	}
	if r.about != "" {
		if _, st, ok := s.agentNow(r.about); ok {
			p["agent_state"] = st
		}
	}
	if _, had := p["decision_open"]; had && s.cfg.DecisionsNow != nil {
		p["decision_open"] = s.cfg.DecisionsNow()
	}
	return p
}

// sendBriefing is doPush's send for the briefing channel: the
// payload goes as a function response with the push's scheduling.
func sendBriefing(c Conn, item pushItem, r briefRec) error {
	setLinks(c, ctxcap.LinksFromTags(item.tags))
	return c.SendToolResponse(briefingResponse(r, item.urgency))
}

// answerLabel opens a to_supervisor result: what it is and whose.
const answerLabel = "The orchestrator's answer to your to_supervisor call, not the user speaking: facts for you to tell the user in your own words."

// answerBriefing is a tool result in the briefing shape: the time, who
// answers, how old the answer is (answeredAt: when the orchestrator
// returned it), and the facts in place of a line to read. detail,
// options, clarify and pending are added by the caller as before.
func (s *Session) answerBriefing(call voice.ToolCall, res voice.ToolResult, answeredAt time.Time) map[string]any {
	now := s.now()
	if answeredAt.IsZero() || answeredAt.After(now) {
		answeredAt = now
	}
	resp := map[string]any{"about_this": answerLabel, "at": clock(now), "from": "orchestrator", "age": ageLine(now.Sub(answeredAt))}
	tags := map[string]any{}
	if len(res.NotVerified) > 0 {
		tags[voice.TagNotVerified] = res.NotVerified
	}
	if len(res.Agents) > 0 {
		tags[voice.TagAgents] = res.Agents
	}
	s.mu.Lock()
	s.addAnswerFactsLocked(resp, tags)
	if rel := s.pendingDeliveryFactsLocked(); len(rel) > 0 {
		resp["relation"] = rel
	}
	s.mu.Unlock()
	if call.Name == voice.ToolStop {
		resp["about_this"] = "The daemon's result for your stop call: facts for you to tell the user."
		resp["from"] = "daemon"
	}
	if res.Spoken != "" {
		resp["facts"] = res.Spoken
	}
	if res.Pending {
		resp["about_this"] = "The orchestrator is still working on your to_supervisor call; its answer will arrive later as a briefing on its own. Say nothing more about it unless the user asks."
	}
	return resp
}

// ageLine says how old a briefing's facts were when it reached the
// model, which has no clock of its own.
func ageLine(d time.Duration) string {
	if d < 3*time.Second {
		return "fresh: written as it was sent to you"
	}
	return "written " + spokenDuration(d) + " before it reached you; things may have moved on since"
}

// spokenDuration renders d coarsely: "45 s", "4 min", "2 h 5 min".
func spokenDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) - 60*h
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %d min", h, m)
	}
}

// agentNow asks the supervisor for an agent's spoken name and its state
// as of now (Config.AgentNow).
func (s *Session) agentNow(alias string) (name, state string, ok bool) {
	if s.cfg.AgentNow == nil || alias == "" {
		return "", "", false
	}
	return s.cfg.AgentNow(alias)
}

// notVerifiedLabel opens the not_verified fact.
const notVerifiedLabel = "these checks by the orchestrator did not finish, so nothing they would have shown is verified: "

// addAnswerFactsLocked adds the facts an answer's tags carry: the
// orchestrator's checks that did not finish, and the state now of the
// agents the answer is about. Caller holds s.mu (agentNow takes no
// session lock).
func (s *Session) addAnswerFactsLocked(p map[string]any, tags map[string]any) {
	if nv := stringList(tags[voice.TagNotVerified]); len(nv) > 0 {
		p["not_verified"] = notVerifiedLabel + strings.Join(nv, "; ")
	}
	var states []string
	for _, alias := range stringList(tags[voice.TagAgents]) {
		if name, st, ok := s.agentNow(alias); ok {
			states = append(states, name+": "+st)
		}
	}
	if len(states) > 0 {
		p["agents_now"] = states
	}
}

// stringList reads a []string or []any tag.
func stringList(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, e := range l {
			if str, ok := e.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}
