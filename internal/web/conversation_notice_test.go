package web

import (
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// The notice subkind, the daemon version and an approval's reason are
// fields the app reads instead of matching the text.
func TestConversationNoticeSubkindsAndReasons(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	var evs []eventlog.Event
	add := func(typ, payload string) {
		n := int64(len(evs) + 1)
		evs = append(evs, eventlog.Event{ID: n, TS: t0.Add(time.Duration(n) * time.Second), Type: typ, Payload: []byte(payload)})
	}
	add("daemon.start", `{"version":"v1.2.3"}`)
	add("voice.handle_dropped", `{"reason":"session_expired"}`)
	add("voice.resume", `{"resumed":false}`)
	add("voice.resume", `{"resumed":true}`)
	add("voice.closed", `{"reason":"shutdown"}`)
	add("conversation.reset", `{}`)
	add("daemon.start", `{}`) // an older daemon: no version
	for _, k := range []string{"a", "b", "c", "d"} {
		add("pending.created", `{"id":"`+k+`","kind":"answer_prompt","summary":"q `+k+`"}`)
	}
	add("pending.cancelled", `{"id":"a","reason":"prompt_gone"}`)
	add("pending.expired", `{"id":"b"}`)
	add("pending.replaced", `{"old_id":"c","new_id":"d"}`)
	items := buildConversation(evs)

	type want struct{ subkind, reason, version string }
	wants := []want{
		{"start", "", "v1.2.3"}, {"context_lost", "session_expired", ""}, {"fresh", "", ""}, {"resumed", "", ""},
		{"closed", "shutdown", ""}, {"reset", "", ""}, {"start", "", ""},
		{"", "prompt_gone", ""}, {"", "expired", ""}, {"", "replaced", ""}, {"", "", ""},
	}
	if len(items) != len(wants) {
		t.Fatalf("%d items, want %d: %s", len(items), len(wants), summarise(items))
	}
	for i, w := range wants {
		if got := (want{items[i].Subkind, items[i].Reason, items[i].Version}); got != w {
			t.Errorf("item %d (%s) = %+v, want %+v", i, items[i].Text, got, w)
		}
	}
	if items[10].Resolution != "open" {
		t.Errorf("last approval = %+v, want open", items[10])
	}
}
