package herdr

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Logger receives structured events (plan §3.3): source is the emitting
// component ("herdr"), typ the dotted event type (herdr.state, herdr.read,
// herdr.send, herdr.reconnect, …), paneID may be empty, and payload is any
// JSON-encodable value. WP5's event log implements this interface; until
// then JSONLLogger writes the same shape to an io.Writer.
type Logger interface {
	Log(source, typ, paneID string, payload any)
}

// NopLogger discards everything.
type NopLogger struct{}

// Log implements Logger.
func (NopLogger) Log(string, string, string, any) {}

// JSONLLogger writes one JSON object per event:
//
//	{"ts":"…","source":"herdr","type":"herdr.state","pane_id":"w5:p1","payload":{…}}
type JSONLLogger struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// NewJSONLLogger returns a logger writing to w.
func NewJSONLLogger(w io.Writer) *JSONLLogger {
	return &JSONLLogger{w: w, now: time.Now}
}

type logLine struct {
	TS      time.Time `json:"ts"`
	Source  string    `json:"source"`
	Type    string    `json:"type"`
	PaneID  string    `json:"pane_id,omitempty"`
	Payload any       `json:"payload,omitempty"`
}

// Log implements Logger. Encoding errors are swallowed: a log line that
// cannot be encoded must not take the daemon down.
func (l *JSONLLogger) Log(source, typ, paneID string, payload any) {
	b, err := json.Marshal(logLine{TS: l.now().UTC(), Source: source, Type: typ, PaneID: paneID, Payload: payload})
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}
