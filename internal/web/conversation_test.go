package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

type convoClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *convoClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Second)
	return c.now
}

func (c *convoClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// syntheticLog writes a two-day, two-run log the way the daemon would:
// a typed exchange with --voice=off, then a voice run with fragments, a
// push, an approval answered by tap and one that expired.
func syntheticLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	c := &convoClock{now: time.Date(2026, 9, 22, 23, 50, 0, 0, time.UTC)}
	l, err := eventlog.Open(dir, eventlog.Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	l.Log("daemon", "daemon.start", "", map[string]any{"version": "t", "voice": "off"})
	l.Log("web", "web.utterance", "", map[string]any{"text": "what is auth doing", "via": "text_router"})
	l.Log("web", "voice.tool_call", "", map[string]any{"id": "t1", "name": "to_supervisor", "via": "text_router"})
	l.Log("brain", "brain.turn", "", map[string]any{"kind": "utterance"}) // not a chat item
	l.Log("web", "voice.tool_result", "", map[string]any{"id": "t1", "name": "to_supervisor", "spoken": "Auth is running the tests.", "via": "text_router"})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	c.Set(time.Date(2026, 9, 23, 0, 52, 0, 0, time.UTC))
	l, err = eventlog.Open(dir, eventlog.Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	l.Log("daemon", "daemon.start", "", map[string]any{"version": "t", "voice": "gemini"})
	l.Log("voice", "voice.connect", "", map[string]any{"key_index": 0})
	l.Log("voice", "voice.transcript.in", "", map[string]any{"text": " what's", "final": false})
	l.Log("voice", "voice.transcript.in", "", map[string]any{"text": " everyone doing?", "final": false})
	l.Log("voice", "voice.tool_call", "", map[string]any{"id": "c1", "name": "to_supervisor"})
	l.Log("voice", "voice.tool_result", "", map[string]any{"id": "c1", "name": "to_supervisor", "spoken": "Auth is working."})
	l.Log("voice", "voice.transcript.out", "", map[string]any{"text": "Auth is", "final": false})
	l.Log("voice", "voice.transcript.out", "", map[string]any{"text": " working.", "final": false})
	l.Log("voice", "voice.turn", "", map[string]any{"responded": true})
	l.Log("supervisor", "pending.created", "w5:p1", map[string]any{"id": "ap-1", "kind": "answer_prompt", "agent": "auth", "summary": "auth asks to run git diff.", "question": "run git diff",
		"options": []map[string]any{{"index": 1, "label": "Yes", "keys": []string{"y"}, "default": true}, {"index": 2, "label": "No", "keys": []string{"esc"}}}})
	l.Log("voice", "voice.push", "", map[string]any{"text": "auth asks to run git diff.", "urgency": "interrupt", "agent": "auth"})
	l.Log("voice", "voice.push", "", map[string]any{"text": "context only", "urgency": "silent"})
	l.Log("supervisor", "pending.confirmed", "w5:p1", map[string]any{"id": "ap-1", "answer": "Yes", "keys": []string{"y"}, "via": "tap", "option": 1})
	l.Log("supervisor", "pending.created", "w6:p1", map[string]any{"id": "ap-2", "kind": "answer_prompt", "agent": "docs", "summary": "docs asks to write a file.", "options": 2})
	l.Log("supervisor", "pending.expired", "w6:p1", map[string]any{"id": "ap-2"})
	l.Log("voice", "voice.resume", "", map[string]any{"resumed": true})
	l.Log("web", "web.utterance", "", map[string]any{"text": "tell docs to stop", "via": "voice"})
	l.Log("voice", "voice.text", "", map[string]any{"text": "tell docs to stop"})
	return dir
}

func convoServer(t *testing.T, dir string) (*Server, *httptest.Server) {
	t.Helper()
	l, err := eventlog.Open(dir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	store, err := state.Open("", state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Deps{Log: l, Store: store, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs
}

func getConversation(t *testing.T, hs *httptest.Server, query string) ConversationResponse {
	t.Helper()
	h := &harness{t: t, http: hs}
	var resp ConversationResponse
	if code := h.getJSON("/api/conversation"+query, &resp); code != http.StatusOK {
		t.Fatalf("/api/conversation%s = %d", query, code)
	}
	return resp
}

func summarise(items []ConversationItem) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, it.Kind+":"+it.Text)
	}
	return strings.Join(parts, " | ")
}

func TestConversationRebuiltFromTheLog(t *testing.T) {
	_, hs := convoServer(t, syntheticLog(t))
	resp := getConversation(t, hs, "")
	want := "notice:Sheepdog started. | shepherd:what is auth doing | dog:Auth is running the tests. | " +
		"notice:Sheepdog started. | shepherd:what's everyone doing? | dog:Auth is working. | " +
		"approval:auth asks to run git diff. | push:auth asks to run git diff. | approval:docs asks to write a file. | " +
		"notice:Voice reconnected; the conversation continues. | shepherd:tell docs to stop"
	if got := summarise(resp.Items); got != want {
		t.Fatalf("items:\n got %s\nwant %s", got, want)
	}
	if resp.NextBefore != nil {
		t.Errorf("next_before = %v, want null (everything returned)", *resp.NextBefore)
	}
	for i := 1; i < len(resp.Items); i++ {
		if resp.Items[i].ID <= resp.Items[i-1].ID {
			t.Fatalf("ids not increasing at %d: %+v", i, resp.Items)
		}
	}
	it := resp.Items
	if it[1].Via != "typed" || it[4].Via != "voice" || it[10].Via != "typed" {
		t.Errorf("shepherd via = %q %q %q", it[1].Via, it[4].Via, it[10].Via)
	}
	ap := it[6]
	if ap.Agent != "auth" || ap.PendingID != "ap-1" || ap.Answered == nil || !*ap.Answered || ap.Answer != "Yes" || ap.Via != "tap" || ap.Resolution != "confirmed" || len(ap.Options) != 2 || !ap.Options[0].Default {
		t.Errorf("approval = %+v", ap)
	}
	if push := it[7]; push.Agent != "auth" || push.Urgency != "interrupt" {
		t.Errorf("push = %+v", push)
	}
	if exp := it[8]; exp.Answered == nil || *exp.Answered || exp.Resolution != "expired" || len(exp.Options) != 0 {
		t.Errorf("expired approval = %+v", exp)
	}
}

func TestConversationPagesBackwards(t *testing.T) {
	_, hs := convoServer(t, syntheticLog(t))
	all := getConversation(t, hs, "").Items
	var got []ConversationItem
	query := "?limit=3"
	for pages := 0; pages < 10; pages++ {
		resp := getConversation(t, hs, query)
		if len(resp.Items) > 3 {
			t.Fatalf("page of %d", len(resp.Items))
		}
		got = append(append([]ConversationItem(nil), resp.Items...), got...)
		if resp.NextBefore == nil {
			break
		}
		query = fmt.Sprintf("?limit=3&before=%d", *resp.NextBefore)
	}
	if summarise(got) != summarise(all) {
		t.Fatalf("paged:\n%s\nwhole:\n%s", summarise(got), summarise(all))
	}
	// A page ending right after the approval still carries its
	// resolution, which was logged after the page's edge.
	resp := getConversation(t, hs, fmt.Sprintf("?limit=1&before=%d", all[7].ID))
	if len(resp.Items) != 1 || resp.Items[0].Resolution != "confirmed" {
		t.Errorf("edge page = %+v", resp.Items)
	}
}

// An SSE client resuming with Last-Event-ID (or ?since=<id>) gets what
// it missed from the log — across the restart and the day boundary —
// then the live stream, every frame with its event id.
func TestSSEResumesFromTheLog(t *testing.T) {
	srv, hs := convoServer(t, syntheticLog(t))
	days, err := srv.deps.Log.Days()
	if err != nil || len(days) != 2 {
		t.Fatalf("days = %v %v", days, err)
	}
	first, _ := srv.deps.Log.ReadDay(days[0])
	second, _ := srv.deps.Log.ReadDay(days[1])
	from := first[2] // the typed utterance's tool call
	want := append(append([]eventlog.Event(nil), first[3:]...), second...)

	streams := []struct {
		name string
		sse  *sseClient
	}{
		{"Last-Event-ID", openSSE(t, hs.URL+"/events?since=100", "Last-Event-ID", fmt.Sprint(from.ID))},
		{"since", openSSE(t, hs.URL+fmt.Sprintf("/events?since=%d", from.ID))},
	}
	for _, c := range streams {
		for i, w := range want {
			ev, ok := c.sse.next(t, time.Second)
			if !ok || ev.ID != w.ID || int64(ev.Seq) != w.ID || ev.Type != w.Type {
				t.Fatalf("%s: event %d = %+v ok=%v, want id %d %s", c.name, i, ev, ok, w.ID, w.Type)
			}
		}
	}
	srv.deps.Log.Log("t", "live", "", nil)
	for _, c := range streams {
		if ev, ok := c.sse.next(t, time.Second); !ok || ev.Type != "live" || ev.ID <= want[len(want)-1].ID {
			t.Fatalf("%s: live = %+v ok=%v", c.name, ev, ok)
		}
	}
}
