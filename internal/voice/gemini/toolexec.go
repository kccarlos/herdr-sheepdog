package gemini

import (
	"context"
	"errors"

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
	s.mu.Unlock()

	s.log.Log(logSource, "voice.tool_call", "", map[string]any{"id": call.ID, "name": call.Name, "args": call.Args})
	s.emit(voice.EventToolCall{Call: call})

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, call.ID)
			s.mu.Unlock()
		}()
		s.execTool(ctx, gen, call)
	}()
}

func (s *Session) execTool(ctx context.Context, gen int, call voice.ToolCall) {
	handler := s.cfg.Handler
	if handler == nil {
		handler = func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
			return voice.ToolResult{}, errors.New("no tool handler configured")
		}
	}
	res, err := handler(ctx, call)
	if ctx.Err() != nil {
		s.log.Log(logSource, "voice.tool_result", "", map[string]any{"id": call.ID, "name": call.Name, "cancelled": true})
		s.emit(voice.EventToolResult{Call: call, Err: ctx.Err(), Cancelled: true})
		return
	}
	if err != nil {
		res = errorResult(call.Name)
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
	payload := map[string]any{
		"id": call.ID, "name": call.Name, "spoken": res.Spoken, "clarify": res.Clarify,
		"scheduling": res.Scheduling.String(),
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
