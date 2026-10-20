package gemini

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// startTool runs the handler for one function call in its own goroutine
// so the reader is never blocked, then sends the function response with
// the scheduling the handler chose.
func (s *Session) startTool(gen int, fc *genai.FunctionCall) {
	call := voice.ToolCall{ID: fc.ID, Name: fc.Name, Args: fc.Args}
	if call.Args == nil {
		call.Args = map[string]any{}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	if call.ID != "" {
		s.inflight[call.ID] = cancel
	}
	turnDone := s.turnDone
	s.noteToolCallLocked(s.now())
	s.mu.Unlock()
	s.clearStalled()

	s.log.Log(logSource, "voice.tool_call", "", map[string]any{"id": call.ID, "name": call.Name, "args": call.Args})
	s.emit(voice.EventToolCall{Call: call})

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, call.ID)
			s.noteToolDoneLocked()
			s.mu.Unlock()
		}()
		s.execTool(ctx, gen, call, turnDone)
	}()
}

// execTool runs the handler, then waits (bounded) for the model to close
// the turn that issued the call before sending the response: a
// NON_BLOCKING call ends the model's turn, and answering before that
// turnComplete arrives makes the model discard the result.
func (s *Session) execTool(ctx context.Context, gen int, call voice.ToolCall, turnDone <-chan struct{}) {
	handler := s.cfg.Handler
	if handler == nil {
		handler = func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
			return voice.ToolResult{}, errors.New("no tool handler configured")
		}
	}
	started := s.now()
	res, err := handler(ctx, call)
	answeredAt := s.now()
	handlerMS := answeredAt.Sub(started).Milliseconds()
	if ctx.Err() != nil {
		s.log.Log(logSource, "voice.tool_result", "", map[string]any{"id": call.ID, "name": call.Name, "cancelled": true})
		s.emit(voice.EventToolResult{Call: call, Err: ctx.Err(), Cancelled: true})
		return
	}
	if err != nil {
		res = errorResult(call.Name)
	}
	select {
	case <-turnDone:
	case <-time.After(s.cfg.ToolResponseWait):
	case <-ctx.Done():
		s.log.Log(logSource, "voice.tool_result", "", map[string]any{"id": call.ID, "name": call.Name, "cancelled": true})
		s.emit(voice.EventToolResult{Call: call, Err: ctx.Err(), Cancelled: true})
		s.pushUnsentAnswer(call, res, "cancelled")
		return
	}
	if !s.holdForShepherd(ctx, call, res) {
		// Cancelled while the user spoke: an answer still reaches
		// them, the late way (the supervisor's barge-in rule, C7).
		s.log.Log(logSource, "voice.tool_result", "", map[string]any{"id": call.ID, "name": call.Name, "cancelled": true})
		s.emit(voice.EventToolResult{Call: call, Err: ctx.Err(), Cancelled: true})
		s.pushUnsentAnswer(call, res, "cancelled")
		return
	}
	// An INTERRUPT result is downgraded to WHEN_IDLE at send time, now
	// that turnDone (or ToolResponseWait) has settled Speaking(), while
	// the model is speaking — except stop(), the one tool result the
	// user is always owed at once (docs/dev/roles.md "Pushes").
	sched := res.Scheduling
	deferred := sched == voice.UrgencyInterrupt && call.Name != voice.ToolStop && s.Speaking()
	if deferred {
		sched = voice.UrgencyIdle
		s.log.Log(logSource, "voice.push_deferred", "", map[string]any{
			"kind": "tool_result", "name": call.Name, "reason": "speaking", "urgency_requested": voice.UrgencyInterrupt.String(),
		})
	}
	resp := map[string]any{"spoken": res.Spoken}
	if s.useBriefings() {
		resp = s.answerBriefing(call, res, answeredAt)
	}
	if res.Clarify {
		resp["clarify"] = true
	}
	if res.Detail != "" {
		resp["detail"] = res.Detail
	}
	if len(res.Options) > 0 {
		resp["options"] = res.Options
	}
	if res.Pending {
		resp["pending"] = true
	}
	sendErr := s.sendOn(gen, func(c Conn) error {
		return c.SendToolResponse(genai.LiveToolResponseInput{FunctionResponses: []*genai.FunctionResponse{{
			ID: call.ID, Name: call.Name, Response: resp, Scheduling: scheduling(sched),
		}}})
	})
	if sendErr == nil && (sched != voice.UrgencySilent || res.Pending) {
		s.mu.Lock()
		if sched != voice.UrgencySilent {
			s.openTurnLocked()
			if res.Spoken != "" {
				s.playUtt.Handed(res.UtteranceID)
			}
			if isAnswer(call, res) {
				// The re-seed recap keeps the answer.
				line, _ := answerLine(call, res)
				s.flushRecapLocked()
				s.recap.Add(voice.RecapSupervisor, line)
			}
		}
		if res.Pending && call.ID != "" {
			s.owedReplies[call.ID] = s.now() // its reply comes as a push
		}
		s.mu.Unlock()
	}
	if sendErr != nil {
		// The connection the call came on is gone (docs/dev/timelines.md
		// C23): the answer goes through the push queue, which outlives
		// connections.
		s.pushUnsentAnswer(call, res, "send_error")
	}
	// dur_ms is the handler's own time; wait_ms the pause for the model to
	// close its turn (the /debug swimlane shows both as the hop latency).
	payload := map[string]any{
		"id": call.ID, "name": call.Name, "spoken": res.Spoken, "clarify": res.Clarify,
		"scheduling": res.Scheduling.String(), "scheduling_sent": sched.SchedulingName(), "dur_ms": handlerMS,
		"wait_ms": s.now().Sub(started).Milliseconds() - handlerMS,
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
	if sendErr != nil {
		payload["send_error"] = sendErr.Error()
		err = errors.Join(err, sendErr)
	}
	s.log.Log(logSource, "voice.tool_result", "", payload)
	s.emit(voice.EventToolResult{Call: call, Result: res, Err: err})
}

// cancelTools cancels the handlers for the given call ids (the user
// barged in while the model was waiting for them).
func (s *Session) cancelTools(ids []string) {
	s.mu.Lock()
	var cancels []context.CancelFunc
	for _, id := range ids {
		if c, ok := s.inflight[id]; ok {
			cancels = append(cancels, c)
		}
	}
	s.mu.Unlock()
	s.log.Log(logSource, "voice.tool_cancelled", "", map[string]any{"ids": ids, "inflight": len(cancels)})
	for _, c := range cancels {
		c()
	}
}

// shepherdHoldMax bounds how long a tool result waits for the user
// to stop speaking: past it the result goes out anyway (WHEN_IDLE lets
// the model place it).
const shepherdHoldMax = 10 * time.Second

// shepherdHoldStep is how often a held result looks again.
const shepherdHoldStep = 250 * time.Millisecond

// holdForShepherd holds a spoken result while the user is speaking
// (speakingUntilLocked, the rule the push queue follows): a reply that
// lands seconds after the call (brain.reply_wait_s) would otherwise be
// voiced over their next sentence. stop() is never held. It reports
// false when ctx ended during the hold.
func (s *Session) holdForShepherd(ctx context.Context, call voice.ToolCall, res voice.ToolResult) bool {
	if call.Name == voice.ToolStop || res.Spoken == "" || res.Scheduling == voice.UrgencySilent {
		return true
	}
	deadline := s.now().Add(shepherdHoldMax)
	held := false
	for {
		s.mu.Lock()
		now := s.now()
		until := s.speakingUntilLocked(now)
		s.mu.Unlock()
		if until <= 0 || !now.Before(deadline) {
			break
		}
		if !held {
			held = true
			s.log.Log(logSource, "voice.push_deferred", "", map[string]any{"kind": "tool_result", "name": call.Name, "reason": "shepherd_speaking"})
		}
		select {
		case <-time.After(min(until, shepherdHoldStep)):
		case <-ctx.Done():
			return false
		}
	}
	return true
}

// isAnswer reports whether res is a spoken answer to the user's
// words (not pending, not silent context, not the stop tool).
func isAnswer(call voice.ToolCall, res voice.ToolResult) bool {
	return call.Name == voice.ToolToSupervisor && res.Spoken != "" && !res.Pending && res.Scheduling != voice.UrgencySilent
}

// answerLine is an answer as a push would carry it: the spoken text with
// its options as a choice, tagged with the utterance it answers.
func answerLine(call voice.ToolCall, res voice.ToolResult) (string, map[string]any) {
	line := res.Spoken
	if len(res.Options) > 0 {
		line += " Options: " + strings.Join(res.Options, "; ") + "."
	}
	tags := map[string]any{voice.TagAnswersUtterance: res.UtteranceID, voice.TagUtteranceID: res.UtteranceID}
	if res.UtteranceID == "" {
		tags = map[string]any{}
	}
	if call.ID != "" {
		tags[ctxcap.TagCallID] = call.ID
	}
	if len(res.NotVerified) > 0 {
		tags[voice.TagNotVerified] = res.NotVerified
	}
	if len(res.Agents) > 0 {
		tags[voice.TagAgents] = res.Agents
	}
	if len(res.Shown) > 0 {
		tags[voice.TagOnScreen] = res.Shown
	}
	return line, tags
}

// pushUnsentAnswer hands an answer whose function response could not be
// sent to the push queue, so it is still spoken (reason: send_error or
// cancelled). Anything that is not an answer is dropped as before.
func (s *Session) pushUnsentAnswer(call voice.ToolCall, res voice.ToolResult, reason string) {
	if !isAnswer(call, res) {
		return
	}
	line, tags := answerLine(call, res)
	tags["fallback"] = reason
	if err := s.PushTagged(line, res.Scheduling, tags); err != nil {
		return
	}
	s.log.Log(logSource, "voice.tool_result_pushed", "", map[string]any{"id": call.ID, "name": call.Name, "reason": reason})
}
