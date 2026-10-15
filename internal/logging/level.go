package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Levels of the runtime log (config log.level), most verbose first.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Levels lists the valid log.level values.
var Levels = []string{LevelDebug, LevelInfo, LevelWarn, LevelError}

// IsErrorEvent reports whether an event type records a failure
// (voice.connect_error, push.failed, investigation.error, …).
func IsErrorEvent(typ string) bool {
	return strings.HasSuffix(typ, "_error") || strings.HasSuffix(typ, ".error") ||
		strings.HasSuffix(typ, "_failed") || strings.HasSuffix(typ, ".failed")
}

// isWarnEvent reports whether an event type is worth a line at warn.
func isWarnEvent(typ string) bool {
	return strings.Contains(typ, "warn") || strings.HasSuffix(typ, "_dropped") ||
		strings.HasSuffix(typ, ".unauthorized") || strings.HasSuffix(typ, "_exhausted")
}

// EventMirror writes the events a level selects to the runtime log as
// one text line each: "ERROR voice.connect_error pane=… {payload}".
//
//	debug  every event
//	info   errors, warnings and the daemon's own lifecycle (daemon.*)
//	warn   errors and warnings
//	error  errors only
type EventMirror struct {
	mu    sync.Mutex
	w     io.Writer
	level string
}

// NewEventMirror returns a Logger writing the events level selects to w
// (an unknown level is treated as info).
func NewEventMirror(w io.Writer, level string) *EventMirror {
	return &EventMirror{w: w, level: level}
}

// tag is the event's severity tag, or "" when the level drops it.
func (m *EventMirror) tag(typ string) string {
	switch {
	case IsErrorEvent(typ):
		return "ERROR"
	case isWarnEvent(typ):
		if m.level == LevelError {
			return ""
		}
		return "WARN"
	case strings.HasPrefix(typ, "daemon."):
		if m.level == LevelWarn || m.level == LevelError {
			return ""
		}
		return "INFO"
	case m.level == LevelDebug:
		return "DEBUG"
	}
	return ""
}

// maxPayload bounds the payload printed on one line.
const maxPayload = 600

// Log implements Logger.
func (m *EventMirror) Log(source, typ, paneID string, payload any) {
	tag := m.tag(typ)
	if tag == "" {
		return
	}
	var b strings.Builder
	b.WriteString(tag)
	b.WriteByte(' ')
	b.WriteString(typ)
	if paneID != "" {
		b.WriteString(" pane=" + paneID)
	}
	if payload != nil {
		if p, err := json.Marshal(payload); err == nil && string(p) != "null" && string(p) != "{}" {
			s := string(p)
			if len(s) > maxPayload {
				s = s[:maxPayload] + fmt.Sprintf("…(%d bytes)", len(p))
			}
			b.WriteByte(' ')
			b.WriteString(s)
		}
	}
	b.WriteByte('\n')
	m.mu.Lock()
	defer m.mu.Unlock()
	_, _ = io.WriteString(m.w, b.String())
}

// Tee is a Logger that sends every event to each of its Loggers.
type Tee []Logger

// Log implements Logger.
func (t Tee) Log(source, typ, paneID string, payload any) {
	for _, l := range t {
		l.Log(source, typ, paneID, payload)
	}
}
