package gemini

import (
	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/audiotiming"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// readLoop is the single Receive caller for one connection. It returns
// when Receive fails (connection closed, server error, rate limit).
func (s *Session) readLoop(conn Conn, gen int) {
	for {
		msg, err := conn.Receive()
		if err != nil {
			s.handleReadError(gen, err)
			return
		}
		s.handleMessage(msg, gen)
	}
}

// handleMessage dispatches one server message (notes §6). Keep-alive
// frames decode to a message with every field nil and fall through.
func (s *Session) handleMessage(msg *genai.LiveServerMessage, gen int) {
	if msg == nil {
		return
	}
	switch {
	case msg.SetupComplete != nil:
		// First message after Connect; nothing to do.
	case msg.GoAway != nil:
		s.log.Log(logSource, "voice.goaway", "", map[string]any{"time_left_ms": msg.GoAway.TimeLeft.Milliseconds()})
		select {
		case s.goAway <- struct{}{}:
		default:
		}
	case msg.SessionResumptionUpdate != nil:
		u := msg.SessionResumptionUpdate
		if u.Resumable && u.NewHandle != "" {
			s.mu.Lock()
			s.handle = u.NewHandle
			s.mu.Unlock()
		}
	case msg.ToolCall != nil:
		for _, fc := range msg.ToolCall.FunctionCalls {
			if fc != nil {
				s.startTool(gen, fc)
			}
		}
	case msg.ToolCallCancellation != nil:
		s.cancelTools(msg.ToolCallCancellation.IDs)
	case msg.ServerContent != nil:
		s.handleServerContent(msg.ServerContent)
	}
	if msg.UsageMetadata != nil {
		u := msg.UsageMetadata
		s.log.Log(logSource, "voice.usage", "", map[string]any{
			"prompt": u.PromptTokenCount, "response": u.ResponseTokenCount, "total": u.TotalTokenCount,
		})
	}
}

func (s *Session) handleServerContent(sc *genai.LiveServerContent) {
	if sc.Interrupted {
		s.interrupt("model")
	}
	if sc.ModelTurn != nil {
		for _, p := range sc.ModelTurn.Parts {
			if p != nil && p.InlineData != nil && len(p.InlineData.Data) > 0 {
				now := s.now()
				s.mu.Lock()
				s.openTurnLocked()
				s.speaking = true
				s.noteResponseLocked(now)
				if s.audioTiming == nil {
					s.audioTurnSeq++
					s.audioTiming = audiotiming.New(s.audioTurnSeq, wsproto.ServerSampleRate)
				}
				s.audioTiming.Chunk(now, len(p.InlineData.Data))
				s.play.Sent(now, len(p.InlineData.Data))
				s.mu.Unlock()
				s.clearStalled()
				s.out.Push(p.InlineData.Data)
			}
		}
	}
	if t := sc.InputTranscription; t != nil && t.Text != "" {
		now := s.now()
		s.mu.Lock()
		s.lastInput = now
		s.inputDone = t.Finished
		s.inText += t.Text
		if t.Finished {
			s.recap.Add(voice.RecapUser, s.inText)
			s.inText = ""
		}
		s.mu.Unlock()
		s.logTurn(s.noteInput(now))
		s.log.Log(logSource, "voice.transcript.in", "", map[string]any{"text": t.Text, "final": t.Finished})
		s.emit(voice.EventTranscript{Dir: voice.DirIn, Text: t.Text, Final: t.Finished})
		s.poke()
	}
	if t := sc.OutputTranscription; t != nil && t.Text != "" {
		s.mu.Lock()
		s.openTurnLocked()
		s.speaking = true
		s.noteResponseLocked(s.now())
		s.play.Said(t.Text)
		// The voice answering means the shepherd finished (Gemini
		// rarely marks its input transcription final).
		s.recap.Add(voice.RecapUser, s.inText)
		s.inText = ""
		s.outText += t.Text
		s.mu.Unlock()
		s.clearStalled()
		s.log.Log(logSource, "voice.transcript.out", "", map[string]any{"text": t.Text, "final": t.Finished})
		s.emit(voice.EventTranscript{Dir: voice.DirOut, Text: t.Text, Final: t.Finished})
	}
	if sc.TurnComplete {
		s.mu.Lock()
		s.turnOpen = false
		s.speaking = false
		close(s.turnDone)
		s.turnDone = make(chan struct{})
		s.flushRecapLocked()
		turn := s.noteTurnCompleteLocked()
		timing := s.finishAudioTimingLocked()
		s.mu.Unlock()
		s.logTurn(turn)
		s.logAudioTiming(timing)
		s.emit(voice.EventTurnComplete{})
		s.poke()
	}
}

// interrupt drops queued output audio and tells the consumer to flush;
// the model is no longer speaking once its output is cut off, whether
// the server reported it ("model") or a push flushed ahead of sending
// ("push").
func (s *Session) interrupt(by string) {
	s.mu.Lock()
	s.speaking = false
	s.flushRecapLocked() // a cut report may shorten it to what was heard
	s.play.Flush(s.now())
	timing := s.finishAudioTimingLocked()
	s.mu.Unlock()
	dropped := s.out.Flush()
	s.log.Log(logSource, "voice.interrupted", "", map[string]any{"by": by, "dropped_chunks": dropped})
	s.logAudioTiming(timing)
	s.emit(voice.EventInterrupted{})
}
