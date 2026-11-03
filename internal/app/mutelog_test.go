package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

type lineLog struct{ lines []logging.Line }

func (l *lineLog) Log(source, typ, paneID string, payload any) {
	l.lines = append(l.lines, logging.Line{Source: source, Type: typ, PaneID: paneID, Payload: payload})
}

// The events a notification is made from carry muted: true for an agent in a
// muted space, whoever logs them, and every other event is left alone.
func TestMutedLoggerMarksNotificationEvents(t *testing.T) {
	tr := herdr.Transition{PaneID: "w5:p1", From: herdr.StateWorking, To: herdr.StateDone, At: time.Unix(1, 0).UTC(), Agent: "codex"}
	tests := []struct {
		name      string
		typ, pane string
		payload   any
		muted     bool
		want      map[string]any // fields that must be in the encoded event
		wantNo    string         // field that must not be
	}{
		{name: "muted question", typ: "pending.created", pane: "w5:p1", payload: map[string]any{"id": "a1"}, muted: true, want: map[string]any{"id": "a1", "muted": true}},
		{name: "unmuted question", typ: "pending.created", pane: "w5:p1", payload: map[string]any{"id": "a1"}, want: map[string]any{"id": "a1"}, wantNo: "muted"},
		{name: "muted transition keeps its own fields", typ: "herdr.transition", pane: "w5:p1", payload: tr, muted: true, want: map[string]any{"pane_id": "w5:p1", "to": "done", "agent": "codex", "muted": true}},
		{name: "unmuted transition", typ: "herdr.transition", pane: "w5:p1", payload: tr, want: map[string]any{"to": "done"}, wantNo: "muted"},
		{name: "other events are not marked", typ: "voice.push", pane: "w5:p1", payload: map[string]any{"text": "x"}, muted: true, want: map[string]any{"text": "x"}, wantNo: "muted"},
		{name: "an event with no pane is not marked", typ: "pending.created", pane: "", payload: map[string]any{"id": "a1"}, muted: true, want: map[string]any{"id": "a1"}, wantNo: "muted"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner := &lineLog{}
			l := &mutedLogger{Logger: inner}
			muted := func(pane string) bool { return tc.muted && pane == "w5:p1" }
			l.muted.Store(&muted)
			l.Log("src", tc.typ, tc.pane, tc.payload)
			if len(inner.lines) != 1 {
				t.Fatalf("lines = %d", len(inner.lines))
			}
			b, err := json.Marshal(inner.lines[0].Payload)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %v, want %v (event %s)", k, got[k], v, b)
				}
			}
			if _, has := got[tc.wantNo]; tc.wantNo != "" && has {
				t.Errorf("event has %q: %s", tc.wantNo, b)
			}
		})
	}
}

// Before the supervisor exists nothing is muted, and a payload the logger
// does not know is passed on as it is.
func TestMutedLoggerWithoutAResolver(t *testing.T) {
	inner := &lineLog{}
	l := &mutedLogger{Logger: inner}
	l.Log("src", "pending.created", "w5:p1", map[string]any{"id": "a1"})
	type custom struct{ A int }
	muted := func(string) bool { return true }
	l.muted.Store(&muted)
	l.Log("src", "pending.created", "w5:p1", custom{A: 1})
	if len(inner.lines) != 2 || inner.lines[1].Payload != any(custom{A: 1}) {
		t.Errorf("lines = %+v", inner.lines)
	}
	if m := inner.lines[0].Payload.(map[string]any); m["muted"] != nil {
		t.Errorf("marked before the resolver was set: %v", m)
	}
}
