package gemini

import (
	"context"
	"errors"
	"time"

	"google.golang.org/genai"

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
	handlerMS := s.now().Sub(started).Milliseconds()
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
		return
	}
	resp := map[string]any{"spoken": res.Spoken}
	if res.Clarify {
		resp["clarify"] = true
	}
	sendErr := s.sendOn(gen, func(c Conn) error {
		return c.SendToolResponse(genai.LiveToolResponseInput{FunctionResponses: []*genai.FunctionResponse{{
			ID: call.ID, Name: call.Name, Response: resp, Scheduling: scheduling(res.Scheduling),
		}}})
	})
	if sendErr == nil && res.Scheduling != voice.UrgencySilent {
		s.mu.Lock()
		s.openTurnLocked()
		s.mu.Unlock()
	}
	// dur_ms is the handler's own time; wait_ms the pause for the model to
	// close its turn (the /debug swimlane shows both as the hop latency).
	payload := map[string]any{
		"id": call.ID, "name": call.Name, "spoken": res.Spoken, "clarify": res.Clarify,
		"scheduling": res.Scheduling.String(), "dur_ms": handlerMS,
		"wait_ms": s.now().Sub(started).Milliseconds() - handlerMS,
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
