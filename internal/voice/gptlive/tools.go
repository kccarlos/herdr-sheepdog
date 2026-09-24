package gptlive

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// delegationSettle is how long after the last input fragment a
// delegation takes the utterance (the delegation often arrives with the
// last fragment, and the docs warn it can precede the full transcript).
const delegationSettle = 400 * time.Millisecond

// settledKeep is how long a settled, unclaimed utterance stays available
// to a delegation that arrives after it, and how close a further settle
// must follow for its words to join it (the same sentence, paused).
const settledKeep = 4 * time.Second

// onDelegation starts handling a client delegation: once the input
// transcript settles, the unclaimed utterance becomes the tool call.
func (s *Session) onDelegation(lc *liveConn, id string, offsetMS int64) {
	s.mu.Lock()
	s.lastActivity = s.now()
	ctx, cancel := context.WithCancel(s.ctx)
	s.inflight[id] = cancel
	s.mu.Unlock()
	s.log.Log(logSource, "voice.delegation", "", map[string]any{"id": id, "offset_ms": offsetMS, "session_id": lc.id})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		text := s.awaitUtterance(ctx)
		if ctx.Err() != nil {
			s.doneInflight(id)
			return
		}
		if text == "" {
			// Nothing the shepherd said is left to relay (a stop already
			// took it, or the transcript never came): tell the model so it
			// does not wait for a result.
			s.log.Log(logSource, "voice.delegation_empty", "", map[string]any{"id": id})
			s.appendFor(lc, id, evThinkingAppend, "Nothing new from the shepherd to act on; no result will follow for this request.")
			s.doneInflight(id)
			return
		}
		call := s.classify(text)
		call.ID = id
		s.runTool(ctx, lc, id, call)
	}()
}

// awaitUtterance waits (bounded by SettleMax) for the input transcript to
// pause, then claims the text no delegation has taken yet.
func (s *Session) awaitUtterance(ctx context.Context) string {
	deadline := s.now().Add(s.cfg.SettleMax)
	for {
		s.mu.Lock()
		quiet := !s.inputOpen || s.now().Sub(s.lastInput) >= delegationSettle
		if quiet || !s.now().Before(deadline) {
			text := strings.TrimSpace(s.unclaimed)
			if s.settledText != "" && s.now().Sub(s.settledAt) < settledKeep {
				text = strings.TrimSpace(s.settledText + " " + text)
			}
			s.unclaimed, s.settledText = "", ""
			s.mu.Unlock()
			return text
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// startTool runs a tool call that did not come from a delegation (typed
// text, a bare stop word): its result is pushed.
func (s *Session) startTool(lc *liveConn, delegationID string, call voice.ToolCall) {
	s.mu.Lock()
	if s.ctx == nil || s.closed {
		s.mu.Unlock()
		return
	}
	s.toolSeq++
	if call.ID == "" {
		call.ID = fmt.Sprintf("local-%d", s.toolSeq)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.inflight[call.ID] = cancel
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runTool(ctx, lc, delegationID, call)
	}()
}

func (s *Session) doneInflight(id string) {
	s.mu.Lock()
	if c, ok := s.inflight[id]; ok {
		c()
		delete(s.inflight, id)
	}
	s.lastActivity = s.now()
	s.mu.Unlock()
}

// runTool calls the handler and delivers the result: on the delegation it
// answers while that session is still the live one, else as a push.
func (s *Session) runTool(ctx context.Context, lc *liveConn, delegationID string, call voice.ToolCall) {
	defer s.doneInflight(call.ID)
	s.log.Log(logSource, "voice.tool_call", "", map[string]any{"id": call.ID, "name": call.Name, "args": call.Args, "delegation_id": delegationID})
	s.emit(voice.EventToolCall{Call: call})
	handler := s.cfg.Handler
	if handler == nil {
		handler = func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
			return voice.ToolResult{}, errors.New("no tool handler configured")
		}
	}
	started := s.now()
	res, err := handler(ctx, call)
	handlerMS := s.now().Sub(started).Milliseconds()
	if ctx.Err() != nil {
		s.log.Log(logSource, "voice.tool_result", "", map[string]any{"id": call.ID, "name": call.Name, "cancelled": true})
		s.emit(voice.EventToolResult{Call: call, Err: ctx.Err(), Cancelled: true})
		return
	}
	if err != nil {
		res = voice.ToolResult{Spoken: "Sorry, that didn't work. Try again.", Scheduling: voice.UrgencyIdle}
	}
	spoken := spokenText(res)
	s.mu.Lock()
	onDelegation := delegationID != "" && lc != nil && s.live == lc
	s.mu.Unlock()
	how := "push"
	switch {
	case onDelegation:
		how = "delegation"
		if res.Detail != "" {
			s.appendFor(lc, delegationID, evThinkingAppend, "Plain explanation for follow-ups such as \"what does that mean\" (do not read it out): "+res.Detail)
		}
		switch {
		case spoken == "" && res.Pending:
			// The brain is still working: its reply comes later as a push
			// (brain.reply_wait_s). Say nothing that would contradict it.
			s.appendFor(lc, delegationID, evThinkingAppend, "The answer will come on its own; say nothing more for now.")
		case spoken == "":
			s.appendFor(lc, delegationID, evThinkingAppend, "The supervisor has nothing to add for this request.")
		case res.Scheduling == voice.UrgencySilent:
			s.appendFor(lc, delegationID, evThinkingAppend, spoken)
		default:
			s.appendFor(lc, delegationID, evCommentaryAppend, spoken)
		}
	case spoken != "":
		tags := map[string]any{}
		if call.Name == voice.ToolStop {
			tags["stop"] = true
		}
		sched := res.Scheduling
		if call.Name == voice.ToolStop {
			sched = voice.UrgencyInterrupt
		}
		_ = s.PushTagged(spoken, sched, tags)
	}
	payload := map[string]any{
		"id": call.ID, "name": call.Name, "spoken": res.Spoken, "clarify": res.Clarify,
		"scheduling": res.Scheduling.String(), "dur_ms": handlerMS, "delivered_as": how,
	}
	if res.Detail != "" {
		payload["detail"] = res.Detail
	}
	if len(res.Options) > 0 {
		payload["options"] = res.Options
	}
	if res.Pending {
		payload["pending"] = true
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	s.log.Log(logSource, "voice.tool_result", "", payload)
	s.emit(voice.EventToolResult{Call: call, Result: res, Err: err})
}

// spokenText is what the model should say for a result: the spoken
// sentence with the options as a choice. The question is heard last (the
// ear contract: the supervisor ends spoken with the reply's ask), so when
// spoken ends with one the options go in before it.
func spokenText(res voice.ToolResult) string {
	text := strings.TrimSpace(res.Spoken)
	if len(res.Options) == 0 {
		return text
	}
	opts := "Options: " + strings.Join(res.Options, "; ") + "."
	if !strings.HasSuffix(text, "?") {
		return strings.TrimSpace(text + " " + opts)
	}
	if i := strings.LastIndexAny(text[:len(text)-1], ".!?"); i >= 0 {
		return text[:i+1] + " " + opts + " " + strings.TrimSpace(text[i+1:])
	}
	return opts + " " + text
}

// appendFor sends one append event tied to a client delegation.
func (s *Session) appendFor(lc *liveConn, delegationID, typ, content string) {
	s.mu.Lock()
	s.toolSeq++
	eid := fmt.Sprintf("res_%d", s.toolSeq)
	s.lastActivity = s.now()
	s.mu.Unlock()
	id := delegationID
	if err := s.send(lc, appendEvent{Type: typ, EventID: eid, DelegationID: &id, Content: clip(content)}); err != nil {
		s.emit(voice.EventError{Err: err})
	}
}
