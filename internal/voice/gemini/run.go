package gemini

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// connect dials with the current key, rotating to the next key when the
// failure looks like a rate limit (or a handshake rejection). It returns
// ErrAllKeysExhausted once every key was tried. resumed reports whether a
// resumption handle was presented.
func (s *Session) connect(ctx context.Context) (conn Conn, resumed bool, err error) {
	tried := 0
	for {
		s.mu.Lock()
		key, idx, handle := s.cfg.APIKeys[s.keyIdx], s.keyIdx, s.handle
		s.mu.Unlock()

		// Rendered fresh on every connect (not cached at New) so a
		// prompts.Loader.Reload() between reconnects takes effect without
		// restarting the daemon (docs/roles.md "Prompt files").
		prompt, perr := systemPrompt(s.cfg)
		if perr != nil {
			return nil, false, perr
		}
		started := s.now()
		conn, err = s.dial.Dial(ctx, key, s.cfg.Model, liveConfig(s.cfg, prompt, handle))
		if err == nil {
			s.log.Log(logSource, "voice.connect", "", map[string]any{
				"key_index": idx, "resumed": handle != "", "dur_ms": s.now().Sub(started).Milliseconds(),
			})
			// The prompt this connection runs with, so /debug can show it
			// per session straight from the log (D10).
			s.log.Log(logSource, "voice.system_prompt", "", map[string]any{"text": prompt, "aliases": s.cfg.Aliases, "model": s.cfg.Model})
			return conn, handle != "", nil
		}
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		s.log.Log(logSource, "voice.connect_error", "", map[string]any{
			"key_index": idx, "resumed": handle != "", "dur_ms": s.now().Sub(started).Milliseconds(), "error": err.Error(),
		})
		if !isRotatable(err) {
			return nil, false, err
		}
		tried++
		if tried >= len(s.cfg.APIKeys) {
			return nil, false, fmt.Errorf("%w: %v", ErrAllKeysExhausted, err)
		}
		s.rotateKey(err.Error())
	}
}

// hasHandle reports whether a resumption handle would be presented.
func (s *Session) hasHandle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handle != ""
}

// dropHandle forgets the resumption handle so the next connect starts a
// fresh session (context lost), logging voice.handle_dropped.
func (s *Session) dropHandle(reason string) {
	s.mu.Lock()
	s.handle = ""
	s.mu.Unlock()
	s.log.Log(logSource, "voice.handle_dropped", "", map[string]any{"reason": reason})
}

// rotateKey moves to the next key. A resumption handle belongs to the
// project that issued it, so the handle is dropped when the key changes.
func (s *Session) rotateKey(reason string) {
	s.mu.Lock()
	from := s.keyIdx
	s.keyIdx = (s.keyIdx + 1) % len(s.cfg.APIKeys)
	if s.keyIdx != from {
		s.handle = ""
	}
	to := s.keyIdx
	s.mu.Unlock()
	s.log.Log(logSource, "voice.key_rotated", "", map[string]any{"from": from, "index": to, "reason": reason})
}

// install makes conn the live connection, starts its reader and replays
// audio buffered during the gap. reconnect distinguishes the first
// connection from a later one (which emits EventResumed).
func (s *Session) install(conn Conn, resumed, reconnect bool) {
	s.mu.Lock()
	s.conn = conn
	s.connGen++
	gen := s.connGen
	s.status = voice.StatusConnected
	pending := s.pending
	s.pending, s.pendingBytes = nil, 0
	handle := s.handle
	s.turnOpen = false
	s.speaking = false
	s.stalls, s.pushTimeouts = 0, 0
	s.stalledNow = false // the connected status below clears it on the page
	turn := s.finishTurnLocked("reconnect")
	timing := s.finishAudioTimingLocked()
	s.mu.Unlock()
	s.logTurn(turn)
	s.logAudioTiming(timing)

	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.readLoop(conn, gen) }()

	for _, chunk := range pending {
		if err := s.sendAudio(conn, chunk); err != nil {
			break
		}
	}
	s.emit(voice.EventStatus{Status: voice.StatusConnected})
	if reconnect {
		s.log.Log(logSource, "voice.resume", "", map[string]any{"handle": handle, "resumed": resumed, "replayed_chunks": len(pending)})
		s.emit(voice.EventResumed{Handle: handle, ContextLost: !resumed})
	}
	s.poke()
}

// run owns the connection lifecycle after Start: it waits for the reader
// to fail or a GoAway, then reconnects with backoff, rotating keys and
// honouring retry delays on rate limits. It never hot-loops.
func (s *Session) run(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		conn := s.conn
		s.conn = nil
		s.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
	}()
	backoff := s.cfg.ReconnectBackoff
	for {
		var readErr error
		select {
		case <-ctx.Done():
			return
		case <-s.goAway:
			s.proactiveReconnect(ctx, "goaway")
			continue
		case <-s.dropReq:
			// Reset already dropped the handle: proactiveReconnect's
			// connect() finds none and comes back fresh.
			s.log.Log(logSource, "voice.reset", "", nil)
			s.proactiveReconnect(ctx, "reset")
			continue
		case reason := <-s.recycle:
			// The connection looks sick (repeated stalls or pushes the
			// model never answered): swap it for a fresh one while the
			// resumption handle is still valid, as on GoAway.
			s.mu.Lock()
			handle := s.handle != ""
			s.mu.Unlock()
			s.log.Log(logSource, "voice.recycle", "", map[string]any{"reason": reason, "handle": handle})
			s.proactiveReconnect(ctx, "recycle")
			continue
		case r := <-s.readerExit:
			s.mu.Lock()
			stale := r.gen != s.connGen
			s.mu.Unlock()
			if stale {
				continue
			}
			readErr = r.err
		}

		// The live connection is gone: reconnect until it works.
		s.mu.Lock()
		old := s.conn
		s.conn = nil
		s.status = voice.StatusReconnecting
		s.mu.Unlock()
		if old != nil {
			_ = old.Close()
		}
		s.log.Log(logSource, "voice.disconnected", "", map[string]any{"error": errString(readErr)})
		s.emit(voice.EventStatus{Status: voice.StatusReconnecting, Reason: "connection lost"})
		if isRateLimited(readErr) {
			// A 429 mid-session: cool down, then move to the next key.
			s.emit(voice.EventError{Err: readErr})
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.rateLimitWait(readErr)):
			}
			if len(s.cfg.APIKeys) > 1 {
				s.rotateKey(readErr.Error())
			}
		} else if isServerInternalError(readErr) && s.hasHandle() {
			// The server itself closed on an internal error (1011): a
			// resumption handle across one is usually dead too, and paying
			// for a resume attempt that will just fail with the same 1011
			// only delays getting the shepherd back a working connection
			// (evidence 2026-09-23 06:31/06:33). Skip straight to a fresh
			// connect; other close codes (GoAway, a network drop) still try
			// to resume first.
			s.dropHandle("server_internal_error")
		}

		resumeFailures := 0
		for {
			conn, resumed, err := s.connect(ctx)
			if err == nil {
				backoff = s.cfg.ReconnectBackoff
				s.install(conn, resumed, true)
				break
			}
			if ctx.Err() != nil {
				return
			}
			s.emit(voice.EventError{Err: err})
			// A resumption handle the server keeps rejecting is dead (seen
			// live: every attempt failed at setup with "Internal error
			// encountered", ~30 s each, forever). Drop it after a couple of
			// tries and reconnect fresh: the supervisor re-pushes the
			// briefing on EventResumed{ContextLost:true}.
			if s.hasHandle() && !isRateLimited(err) && !errors.Is(err, ErrAllKeysExhausted) {
				resumeFailures++
				if resumeFailures >= s.cfg.ResumeAttempts {
					s.dropHandle(err.Error())
					resumeFailures = 0
				}
			}
			wait := backoff
			if errors.Is(err, ErrAllKeysExhausted) || isRateLimited(err) {
				wait = s.rateLimitWait(err)
			} else {
				backoff = min(backoff*2, 30*time.Second)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
}

// rateLimitWait logs voice.rate_limited and returns how long to wait:
// RetryInfo.retryDelay when the error carries one, else the configured
// backoff.
func (s *Session) rateLimitWait(err error) time.Duration {
	wait := s.cfg.RateLimitBackoff
	if d, ok := retryDelay(err); ok {
		wait = d
	}
	s.mu.Lock()
	idx := s.keyIdx
	s.mu.Unlock()
	s.log.Log(logSource, "voice.rate_limited", "", map[string]any{"key_index": idx, "wait_ms": wait.Milliseconds(), "error": err.Error()})
	return wait
}

// proactiveReconnect opens a new connection with the current handle while
// the old one is still alive (GoAway, recycle), then swaps them. On
// failure the old connection is kept until the server closes it and the
// normal path runs.
func (s *Session) proactiveReconnect(ctx context.Context, why string) {
	conn, resumed, err := s.connect(ctx)
	if err != nil {
		s.emit(voice.EventError{Err: fmt.Errorf("%s reconnect: %w", why, err)})
		return
	}
	s.mu.Lock()
	old := s.conn
	s.mu.Unlock()
	s.install(conn, resumed, true)
	if old != nil {
		_ = old.Close()
	}
}

// handleReadError is called by the reader when Receive fails; the run
// loop decides what to do with it.
func (s *Session) handleReadError(gen int, err error) {
	select {
	case s.readerExit <- readerResult{gen: gen, err: err}:
	case <-s.ctx.Done():
	}
}

// sendOn sends through conn only if it is still the live connection; a
// message for a dead connection is dropped with ErrNotConnected.
func (s *Session) sendOn(gen int, send func(Conn) error) error {
	s.mu.Lock()
	conn, current := s.conn, gen == s.connGen
	s.mu.Unlock()
	if conn == nil || !current {
		return ErrNotConnected
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return send(conn)
}

// liveGen returns the current connection generation (for tool responses).
func (s *Session) liveGen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connGen
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
