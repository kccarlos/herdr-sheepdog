package gptlive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/audiotiming"
)

// startOpenLocked opens a live session in the background. Caller holds
// s.mu and has checked that none is open or opening.
func (s *Session) startOpenLocked(reason string) {
	if s.live != nil || s.opening || s.closed {
		return
	}
	s.opening = true
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.open(reason) }()
}

// open dials, sends session.start with the instructions and a recap of
// the conversation so far, and waits for session.started.
func (s *Session) open(reason string) {
	started := s.now()
	lc, err := s.dialStart(reason)
	s.mu.Lock()
	s.opening = false
	if err != nil {
		wait := s.cfg.OpenBackoff
		if isRateLimited(err) {
			wait = s.cfg.RateLimitBackoff
		}
		s.nextOpen = s.now().Add(wait)
		s.pending, s.pendingN = nil, 0
		s.mu.Unlock()
		s.log.Log(logSource, "voice.session_error", "", map[string]any{"reason": reason, "error": err.Error(), "retry_ms": wait.Milliseconds(), "dur_ms": s.now().Sub(started).Milliseconds()})
		s.emit(voice.EventError{Err: err})
		return
	}
	if s.closed {
		s.mu.Unlock()
		s.closeLive(lc, "shutdown")
		return
	}
	s.live = lc
	s.sessions++
	s.lastActivity = s.now()
	s.lastVoiced, s.lastOutText, s.speakingNow = time.Time{}, time.Time{}, false
	pending := s.pending
	s.pending, s.pendingN = nil, 0
	n := s.sessions
	s.mu.Unlock()
	for _, chunk := range pending {
		if err := s.sendAudio(lc, chunk); err != nil {
			break
		}
	}
	s.log.Log(logSource, "voice.session_open", "", map[string]any{
		"reason": reason, "session_id": lc.id, "model": s.cfg.Model, "voice": s.cfg.Voice,
		"dur_ms": s.now().Sub(started).Milliseconds(), "replayed_chunks": len(pending), "sessions": n,
	})
	s.emit(voice.EventStatus{Status: voice.StatusConnected, Reason: ReasonLive})
}

func (s *Session) dialStart(reason string) (*liveConn, error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.StartTimeout)
	defer cancel()
	conn, err := s.dial.Dial(ctx, s.cfg.URL, s.cfg.APIKey)
	if err != nil {
		return nil, err
	}
	conn = s.recordConn(conn)
	instructions := ""
	if s.cfg.Instructions != nil {
		if instructions, err = s.cfg.Instructions(); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	rctx, rcancel := context.WithCancel(context.Background())
	s.mu.Lock()
	seq := s.sessions + 1
	input := s.recapItemsLocked()
	s.held = nil
	s.mu.Unlock()
	lc := &liveConn{conn: conn, seq: seq, reason: reason, openedAt: s.now(), cancel: rcancel, started: make(chan struct{}), done: make(chan struct{})}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.readLoop(rctx, lc) }()
	start := startEvent{Type: evSessionStart, EventID: fmt.Sprintf("start_%d", seq), Session: sessionConfig{
		Model: s.cfg.Model, Instructions: instructions,
		Audio:      audioConfig{Format: audioFormat{Type: "audio/pcm", Rate: SampleRate}, Output: audioOutput{Voice: s.cfg.Voice}},
		Delegation: delegationCfg{Type: "client"},
		Input:      input,
	}}
	if err := s.send(lc, start); err != nil {
		rcancel()
		_ = conn.Close()
		return nil, err
	}
	s.log.Log(logSource, "voice.system_prompt", "", map[string]any{"text": instructions, "model": s.cfg.Model, "provider": "openai", "recap_items": len(input)})
	select {
	case <-lc.started:
		return lc, nil
	case <-lc.done:
		rcancel()
		_ = conn.Close()
		return nil, errors.New("gptlive: connection closed before session.started")
	case <-ctx.Done():
		rcancel()
		_ = conn.Close()
		return nil, fmt.Errorf("gptlive: waiting for session.started: %w", ctx.Err())
	}
}

// closeLive asks the server to close lc (session.close) and releases the
// connection once session.closed arrived or CloseTimeout passed. lc
// leaves s.live at once, so new speech opens a fresh session.
func (s *Session) closeLive(lc *liveConn, why string) {
	s.mu.Lock()
	if lc.closeWhy != "" {
		s.mu.Unlock()
		return
	}
	lc.closeWhy = why
	if s.live == lc {
		s.live = nil
	}
	s.mu.Unlock()
	s.log.Log(logSource, "voice.session_closing", "", map[string]any{"reason": why, "session_id": lc.id})
	_ = s.send(lc, closeEvent{Type: evSessionClose, EventID: fmt.Sprintf("close_%d", lc.seq)})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		select {
		case <-lc.done:
		case <-time.After(s.cfg.CloseTimeout):
			s.finalize(lc, "", false)
		}
		lc.cancel()
		_ = lc.conn.Close()
	}()
}

// finalize books lc's usage and logs voice.session_close once, whether
// session.closed arrived (confirmed) or the connection just went away.
func (s *Session) finalize(lc *liveConn, serverReason string, confirmed bool) {
	now := s.now()
	s.mu.Lock()
	if lc.finalized {
		s.mu.Unlock()
		return
	}
	lc.finalized = true
	wasLive := s.live == lc
	if wasLive {
		s.live = nil
	}
	secs := lc.usage
	wall := now.Sub(lc.openedAt).Seconds()
	if !confirmed && wall > secs {
		// No final usage: bill the wall clock so the tally errs high.
		secs = wall
	}
	s.billed += secs
	total := s.billed
	why := lc.closeWhy
	if why == "" {
		why = serverReason
		if why == "" {
			why = "connection_lost"
		}
	}
	wasSpeaking := s.speakingNow
	s.speakingNow = false
	s.unclaimed, s.settledText, s.turnDoneOwed = "", "", false
	s.lastVoiced, s.lastOutText = time.Time{}, time.Time{}
	s.flushReplyLocked()
	timing := s.finishAudioTimingLocked()
	closing := s.closed
	s.mu.Unlock()
	s.out.Flush()
	if timing != nil {
		s.log.Log(logSource, "voice.audio_timing", "", timing)
	}
	s.log.Log(logSource, "voice.session_close", "", map[string]any{
		"reason": why, "server_reason": serverReason, "session_id": lc.id, "dur_ms": now.Sub(lc.openedAt).Milliseconds(),
		"billed_s": secs, "confirmed": confirmed, "billed_total_s": total,
	})
	s.log.Log(logSource, "voice.billed_seconds", "", map[string]any{"total_s": total, "provider": "openai"})
	if wasSpeaking {
		s.emit(voice.EventTurnComplete{})
	}
	if wasLive && !closing {
		s.emit(voice.EventStatus{Status: voice.StatusConnected, Reason: ReasonStandby})
	}
}

// readLoop is the single reader of lc.
func (s *Session) readLoop(ctx context.Context, lc *liveConn) {
	defer close(lc.done)
	for {
		data, err := lc.conn.Receive(ctx)
		if err != nil {
			s.finalize(lc, "", false)
			return
		}
		var ev serverEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			s.log.Log(logSource, "voice.bad_event", "", map[string]any{"error": err.Error()})
			continue
		}
		if s.handleEvent(lc, &ev) {
			return
		}
	}
}

// handleEvent dispatches one server event; true ends the reader.
func (s *Session) handleEvent(lc *liveConn, ev *serverEvent) bool {
	switch ev.Type {
	case evSessionStarted:
		s.mu.Lock()
		if ev.Session != nil {
			lc.id = ev.Session.ID
		}
		s.mu.Unlock()
		lc.startedOnce.Do(func() { close(lc.started) })
	case evOutputAudioDelta:
		chunk, err := base64.StdEncoding.DecodeString(ev.Delta)
		if err == nil && len(chunk) > 0 {
			s.onOutputAudio(chunk)
		}
	case evInputTranscript:
		if ev.Delta != "" {
			s.onInputText(ev.Delta)
		}
	case evOutputTranscript:
		if ev.Delta != "" {
			s.onOutputText(ev.Delta)
		}
	case evDelegationCreated:
		if ev.Delegation != nil && ev.Delegation.Target == "client" {
			s.onDelegation(lc, ev.Delegation.ID, ev.OffsetMS)
		}
	case evUsageUpdated:
		if ev.Usage != nil {
			s.mu.Lock()
			if ev.Usage.Seconds > lc.usage {
				lc.usage = ev.Usage.Seconds
			}
			s.mu.Unlock()
		}
	case evSessionClosed:
		s.mu.Lock()
		if ev.Usage != nil && ev.Usage.Seconds > lc.usage {
			lc.usage = ev.Usage.Seconds
		}
		s.mu.Unlock()
		s.finalize(lc, ev.Reason, true)
		return true
	case evError:
		payload := map[string]any{"session_id": lc.id}
		msg := "gptlive: error event"
		if e := ev.Error; e != nil {
			payload["code"], payload["message"], payload["param"], payload["client_event_id"] = e.Code, e.Message, e.Param, e.ClientEventID
			msg = fmt.Sprintf("gptlive: %s: %s", e.Code, e.Message)
		}
		s.log.Log(logSource, "voice.error_event", "", payload)
		s.emit(voice.EventError{Err: errors.New(msg)})
	case evInstructionsApplied, evThinkingApplied, evCommentaryApplied:
		s.log.Log(logSource, "voice.append_ack", "", map[string]any{"type": ev.Type, "client_event_id": ev.ClientEventID, "start_ms": ev.StartMS, "end_ms": ev.EndMS})
	}
	return false
}

func (s *Session) onOutputAudio(chunk []byte) {
	now := s.now()
	voiced := peak(chunk) >= s.cfg.VoicedPeak
	s.mu.Lock()
	if s.audioTiming == nil {
		s.audioTurnSeq++
		s.audioTiming = audiotiming.New(s.audioTurnSeq, SampleRate)
	}
	s.audioTiming.Chunk(now, len(chunk))
	if voiced {
		s.lastVoiced = now
		s.lastActivity = now
		s.speakingNow = true
	}
	forward := voiced || (!s.lastVoiced.IsZero() && now.Sub(s.lastVoiced) < s.cfg.PauseHold)
	if forward {
		s.outBytes += int64(len(chunk))
		s.play.Sent(now, len(chunk))
	}
	s.mu.Unlock()
	if forward {
		s.out.Push(chunk)
	}
}

func (s *Session) onInputText(delta string) {
	now := s.now()
	s.mu.Lock()
	s.lastInput = now
	s.inputOpen = true
	s.lastActivity = now
	s.unclaimed += delta
	s.inText += delta
	s.mu.Unlock()
	s.log.Log(logSource, "voice.transcript.in", "", map[string]any{"text": delta, "final": false})
	s.emit(voice.EventTranscript{Dir: voice.DirIn, Text: delta})
}

func (s *Session) onOutputText(delta string) {
	now := s.now()
	s.mu.Lock()
	s.lastOutText = now
	s.lastActivity = now
	s.speakingNow = true
	s.outText += delta
	s.play.Said(delta)
	s.mu.Unlock()
	s.log.Log(logSource, "voice.transcript.out", "", map[string]any{"text": delta, "final": false})
	s.emit(voice.EventTranscript{Dir: voice.DirOut, Text: delta})
}

// addRecapLocked remembers one finished utterance for the next session's
// history.
func (s *Session) addRecapLocked(role, text string) {
	s.recap.Add(role, text)
}

// flushReplyLocked moves the reply being spoken into the recap.
func (s *Session) flushReplyLocked() {
	s.addRecapLocked("assistant", s.outText)
	s.outText = ""
}

// recapItemsLocked renders the recap (newest kept within RecapChars) and
// any silent context received in standby as session.input messages.
func (s *Session) recapItemsLocked() []initialItem {
	var items []initialItem
	for _, e := range s.recap.Recent(s.cfg.RecapChars) {
		part := contentPart{Type: "input_text", Text: e.Text}
		if e.Role == voice.RecapAssistant {
			part.Type = "output_text"
		}
		items = append(items, initialItem{Type: "message", Role: e.Role, Content: []contentPart{part}})
	}
	if len(items) > 0 {
		note := "The voice line was closed while idle and has just reopened; the messages above are the recent conversation. Carry on from them; do not greet the shepherd again."
		items = append(items, initialItem{Type: "message", Role: "developer", Content: []contentPart{{Type: "input_text", Text: note}}})
	}
	if len(s.held) > 0 {
		text := "Background from the supervisor while the line was closed (do not read it out unless asked): " + strings.Join(s.held, " ")
		if len(text) > 1500 {
			text = text[:1500]
		}
		items = append(items, initialItem{Type: "message", Role: "developer", Content: []contentPart{{Type: "input_text", Text: text}}})
	}
	return items
}
