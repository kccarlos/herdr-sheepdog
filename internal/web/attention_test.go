package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/attention"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// stubAttention is the attention half of the supervisor with a script: the
// HTTP layer's tests do not need herdr, only the answers.
type stubAttention struct {
	*supervisor.Supervisor
	mu    sync.Mutex
	calls []string
	err   error
	att   supervisor.AgentAttention
	state supervisor.AttentionState
	space supervisor.SpaceMuteResult
	muted map[string]bool
}

func (s *stubAttention) HasAttention() bool  { return true }
func (s *stubAttention) HasSpaceMutes() bool { return true }
func (s *stubAttention) AttentionState(context.Context, []state.Agent) supervisor.AttentionState {
	return s.state
}
func (s *stubAttention) PaneMuted(pane string) bool { return s.muted[pane] }

func (s *stubAttention) record(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fmt.Sprintf(format, args...))
}

func (s *stubAttention) MarkSeen(_ context.Context, pane string, turn *uint64, unread bool, via string) (supervisor.AgentAttention, error) {
	if turn != nil {
		s.record("seen %s turn=%d via=%s", pane, *turn, via)
	} else {
		s.record("seen %s unread=%v via=%s", pane, unread, via)
	}
	return s.att, s.err
}

func (s *stubAttention) SetPinned(_ context.Context, pane string, pinned bool, via string) (supervisor.AgentAttention, error) {
	s.record("pin %s %v via=%s", pane, pinned, via)
	return s.att, s.err
}

func (s *stubAttention) SetSpaceMute(_ context.Context, id string, muted bool, until *time.Time, via string) (supervisor.SpaceMuteResult, error) {
	if until != nil {
		s.record("mute %s %v until=%s via=%s", id, muted, until.UTC().Format(time.RFC3339), via)
	} else {
		s.record("mute %s %v via=%s", id, muted, via)
	}
	r := s.space
	r.Muted, r.Until = muted, until
	return r, s.err
}

func (s *stubAttention) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func stubHarness(t *testing.T) (*harness, *stubAttention) {
	t.Helper()
	stub := &stubAttention{
		att:   supervisor.AgentAttention{Group: attention.GroupDone, Unread: true, Turn: 7, Shared: true},
		space: supervisor.SpaceMuteResult{ID: "w3", Path: "/code/auth", Name: "auth"},
	}
	h := newHarness(t, harnessOpts{wrap: func(s *supervisor.Supervisor) Supervisor {
		stub.Supervisor = s
		return stub
	}})
	return h, stub
}

// Every body and error of the three routes: the statuses, the answer, and
// what reaches the supervisor.
func TestAttentionRoutes(t *testing.T) {
	soon := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	soonStr := soon.Format(time.RFC3339)
	pastStr := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	tests := []struct {
		name      string
		path      string
		body      any
		err       error
		wantCode  int
		wantCalls []string
		wantKeys  []string // keys of the answer
	}{
		{name: "seen with the shown turn", path: "/api/agents/w5:p1/seen", body: `{"turn": 7}`, wantCode: 200, wantCalls: []string{"seen w5:p1 turn=7 via=panel"}, wantKeys: []string{"attention", "ok"}},
		{name: "seen with turn 0", path: "/api/agents/w5:p1/seen", body: `{"turn": 0}`, wantCode: 200, wantCalls: []string{"seen w5:p1 turn=0 via=panel"}},
		{name: "mark as unread", path: "/api/agents/w5:p1/seen", body: `{"unread": true}`, wantCode: 200, wantCalls: []string{"seen w5:p1 unread=true via=panel"}},
		{name: "seen with nothing", path: "/api/agents/w5:p1/seen", body: `{}`, wantCode: 400},
		{name: "seen with both", path: "/api/agents/w5:p1/seen", body: `{"turn": 1, "unread": true}`, wantCode: 400},
		{name: "unread false is not a request", path: "/api/agents/w5:p1/seen", body: `{"unread": false}`, wantCode: 400},
		{name: "negative turn", path: "/api/agents/w5:p1/seen", body: `{"turn": -1}`, wantCode: 400},
		{name: "turn as text", path: "/api/agents/w5:p1/seen", body: `{"turn": "7"}`, wantCode: 400},
		{name: "empty body", path: "/api/agents/w5:p1/seen", body: ``, wantCode: 400},
		{name: "bad json", path: "/api/agents/w5:p1/seen", body: `{`, wantCode: 400},
		{name: "seen for an agent not tracked", path: "/api/agents/w9:p9/seen", body: `{"turn": 1}`, err: supervisor.ErrNoSuchAgent, wantCode: 404, wantCalls: []string{"seen w9:p9 turn=1 via=panel"}},
		{name: "seen refused by herdr", path: "/api/agents/w5:p1/seen", body: `{"turn": 1}`, err: errors.New("herdr did not take the mark"), wantCode: 502, wantCalls: []string{"seen w5:p1 turn=1 via=panel"}},

		{name: "pin", path: "/api/agents/w5:p1/pin", body: `{"pinned": true}`, wantCode: 200, wantCalls: []string{"pin w5:p1 true via=panel"}, wantKeys: []string{"attention", "ok"}},
		{name: "unpin", path: "/api/agents/w5:p1/pin", body: `{"pinned": false}`, wantCode: 200, wantCalls: []string{"pin w5:p1 false via=panel"}},
		{name: "pin without pinned", path: "/api/agents/w5:p1/pin", body: `{}`, wantCode: 400},
		{name: "pin with text", path: "/api/agents/w5:p1/pin", body: `{"pinned": "yes"}`, wantCode: 400},
		{name: "pin an agent not tracked", path: "/api/agents/w9:p9/pin", body: `{"pinned": true}`, err: supervisor.ErrNoSuchAgent, wantCode: 404, wantCalls: []string{"pin w9:p9 true via=panel"}},
		{name: "pin refused by herdr", path: "/api/agents/w5:p1/pin", body: `{"pinned": true}`, err: errors.New("hc refused terminal.label"), wantCode: 502, wantCalls: []string{"pin w5:p1 true via=panel"}},

		{name: "mute until unmuted", path: "/api/spaces/w3/mute", body: `{"muted": true}`, wantCode: 200, wantCalls: []string{"mute w3 true via=panel"}, wantKeys: []string{"ok", "space"}},
		{name: "mute until a time", path: "/api/spaces/w3/mute", body: `{"muted": true, "until": "` + soonStr + `"}`, wantCode: 200, wantCalls: []string{"mute w3 true until=" + soonStr + " via=panel"}},
		{name: "unmute", path: "/api/spaces/w3/mute", body: `{"muted": false}`, wantCode: 200, wantCalls: []string{"mute w3 false via=panel"}},
		{name: "mute without muted", path: "/api/spaces/w3/mute", body: `{}`, wantCode: 400},
		{name: "until with unmute", path: "/api/spaces/w3/mute", body: `{"muted": false, "until": "` + soonStr + `"}`, wantCode: 400},
		{name: "until in the past", path: "/api/spaces/w3/mute", body: `{"muted": true, "until": "` + pastStr + `"}`, wantCode: 400},
		{name: "until that is not a time", path: "/api/spaces/w3/mute", body: `{"muted": true, "until": "tomorrow"}`, wantCode: 400},
		{name: "mute a space not tracked", path: "/api/spaces/w404/mute", body: `{"muted": true}`, err: supervisor.ErrNoSuchSpace, wantCode: 404, wantCalls: []string{"mute w404 true via=panel"}},
		{name: "mute a space with no folder", path: "/api/spaces/w3/mute", body: `{"muted": true}`, err: fmt.Errorf("%w: auth has no folder to mute by", supervisor.ErrUserAction), wantCode: 400, wantCalls: []string{"mute w3 true via=panel"}},
		{name: "mute refused by herdr", path: "/api/spaces/w3/mute", body: `{"muted": true}`, err: errors.New("could not list herdr's workspaces"), wantCode: 502, wantCalls: []string{"mute w3 true via=panel"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, stub := stubHarness(t)
			stub.err = tc.err
			code, body := h.rawPost(tc.path, tc.body.(string), nil)
			if code != tc.wantCode {
				t.Fatalf("status = %d %s, want %d", code, body, tc.wantCode)
			}
			if got := stub.all(); !slices.Equal(got, tc.wantCalls) {
				t.Errorf("supervisor calls = %q, want %q", got, tc.wantCalls)
			}
			var m map[string]json.RawMessage
			_ = json.Unmarshal([]byte(body), &m)
			if code >= 400 {
				if _, ok := m["error"]; !ok {
					t.Errorf("error body has no error: %s", body)
				}
				return
			}
			if tc.wantKeys != nil {
				var keys []string
				for k := range m {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				if !slices.Equal(keys, tc.wantKeys) {
					t.Errorf("answer keys = %v, want %v (%s)", keys, tc.wantKeys, body)
				}
			}
		})
	}
}

// The answers carry the shapes the phone decodes.
func TestAttentionAnswerShapes(t *testing.T) {
	h, stub := stubHarness(t)
	var seen struct {
		OK        bool                      `json:"ok"`
		Attention supervisor.AgentAttention `json:"attention"`
	}
	if code, body := h.rawPost("/api/agents/w5:p1/seen", `{"turn": 7}`, &seen); code != 200 || !seen.OK || seen.Attention != stub.att {
		t.Errorf("seen = %d %s", code, body)
	}
	var muted struct {
		OK    bool `json:"ok"`
		Space struct {
			ID    string     `json:"id"`
			Path  string     `json:"path"`
			Muted bool       `json:"muted"`
			Until *time.Time `json:"until"`
		} `json:"space"`
	}
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if code, body := h.rawPost("/api/spaces/w3/mute", `{"muted": true, "until": "`+until.Format(time.RFC3339)+`"}`, &muted); code != 200 ||
		muted.Space.ID != "w3" || muted.Space.Path != "/code/auth" || !muted.Space.Muted || muted.Space.Until == nil || !muted.Space.Until.Equal(until) {
		t.Errorf("mute = %d %s", code, body)
	}
	var plain map[string]map[string]json.RawMessage
	if code, body := h.rawPost("/api/spaces/w3/mute", `{"muted": true}`, &plain); code != 200 {
		t.Fatalf("mute = %d %s", code, body)
	}
	if _, ok := plain["space"]["until"]; ok {
		t.Error("a mute until unmuted has an until")
	}
}

// A phone says "phone", this computer says "panel"; a repeated POST with the
// same Idempotency-Key runs once.
func TestAttentionViaAndIdempotency(t *testing.T) {
	h, stub := stubHarness(t)
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodPost, h.http.URL+"/api/agents/w5:p1/pin", strings.NewReader(`{"pinned": true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "k1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	}
	if got := stub.all(); len(got) != 1 {
		t.Errorf("the repeated request ran %d times: %q", len(got), got)
	}
	r, _ := http.NewRequest(http.MethodPost, "/", nil)
	if via(r) != supervisor.ViaPhone {
		t.Errorf("a request without the local device is %q, want phone", via(r))
	}
}

// Without the feature the routes answer 503, the state carries no attention
// fields and bot-ready lists neither feature (an old phone and a new daemon,
// or the other way round, both keep working).
func TestAttentionAbsentWithoutTheFeature(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	for _, p := range []struct{ path, body string }{
		{"/api/agents/w5:p1/seen", `{"turn": 1}`},
		{"/api/agents/w5:p1/pin", `{"pinned": true}`},
		{"/api/spaces/w3/mute", `{"muted": true}`},
	} {
		if code, body := h.rawPost(p.path, p.body, nil); code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d %s, want 503", p.path, code, body)
		}
	}
	trackWorking(t, h, "w1:p2", "auth", "working")
	var raw map[string]json.RawMessage
	if code := h.getJSON("/api/state", &raw); code != 200 {
		t.Fatalf("state = %d", code)
	}
	for _, k := range []string{"attention_counts", "space_mutes"} {
		if _, ok := raw[k]; ok {
			t.Errorf("/api/state has %s without the feature", k)
		}
	}
	var agents []map[string]json.RawMessage
	_ = json.Unmarshal(raw["agents"], &agents)
	if len(agents) != 1 {
		t.Fatalf("agents = %s", raw["agents"])
	}
	if _, ok := agents[0]["attention"]; ok {
		t.Error("an agent has attention without the feature")
	}
	if _, ok := agents[0]["pane_id"]; !ok {
		t.Error("the agent row lost its own fields")
	}
	for _, f := range h.srv.features() {
		if f == "attention" || f == "space-mute" {
			t.Errorf("features %v list %s", h.srv.features(), f)
		}
	}
}

// With the feature the agent rows, the counts and the muted spaces are in
// /api/state and both features are listed.
func TestStateCarriesAttention(t *testing.T) {
	h, stub := stubHarness(t)
	trackWorking(t, h, "w1:p2", "auth", "working")
	seen := uint64(6)
	stub.state = supervisor.AttentionState{
		Available:  true,
		Agents:     map[string]supervisor.AgentAttention{"w1:p2": {Group: attention.GroupDone, Unread: true, Turn: 7, Seen: &seen, Muted: true, Shared: true}},
		Counts:     supervisor.AttentionCounts{NeedsYou: 1, Working: 2, DoneUnread: 1},
		SpaceMutes: []supervisor.SpaceMuteInfo{{Path: "/code/auth", Name: "auth"}},
	}
	var raw struct {
		Agents []struct {
			PaneID    string `json:"pane_id"`
			Alias     string `json:"alias"`
			Attention *struct {
				Group  string  `json:"group"`
				Unread bool    `json:"unread"`
				Pinned bool    `json:"pinned"`
				Turn   uint64  `json:"turn"`
				Seen   *uint64 `json:"seen"`
				Muted  bool    `json:"muted"`
				Shared bool    `json:"shared"`
			} `json:"attention"`
		} `json:"agents"`
		Counts map[string]int      `json:"attention_counts"`
		Mutes  []map[string]string `json:"space_mutes"`
	}
	if code := h.getJSON("/api/state", &raw); code != 200 {
		t.Fatalf("state = %d", code)
	}
	if len(raw.Agents) != 1 || raw.Agents[0].PaneID != "w1:p2" || raw.Agents[0].Alias != "auth" {
		t.Fatalf("agents = %+v", raw.Agents)
	}
	a := raw.Agents[0].Attention
	if a == nil || a.Group != "done" || !a.Unread || a.Pinned || a.Turn != 7 || a.Seen == nil || *a.Seen != 6 || !a.Muted || !a.Shared {
		t.Errorf("attention = %+v", a)
	}
	if raw.Counts["needs_you"] != 1 || raw.Counts["working"] != 2 || raw.Counts["done_unread"] != 1 {
		t.Errorf("attention_counts = %v", raw.Counts)
	}
	if len(raw.Mutes) != 1 || raw.Mutes[0]["path"] != "/code/auth" || raw.Mutes[0]["name"] != "auth" || raw.Mutes[0]["until"] != "" {
		t.Errorf("space_mutes = %v", raw.Mutes)
	}
	for _, f := range []string{"attention", "space-mute"} {
		if !slices.Contains(h.srv.features(), f) {
			t.Errorf("features %v lack %s", h.srv.features(), f)
		}
	}
	// An agent with no attention (not followed by the marker) has no field.
	stub.state.Agents = nil
	var again struct {
		Agents []map[string]json.RawMessage `json:"agents"`
	}
	h.getJSON("/api/state", &again)
	if _, ok := again.Agents[0]["attention"]; ok {
		t.Error("an agent without attention got the field")
	}
}

// The whole path in mock mode (the daemon's own marker over the injector's
// panes): finish, unread, seen, mark as unread, pin, and the events.
func TestAttentionEndToEndInMockMode(t *testing.T) {
	h := newHarness(t, harnessOpts{marks: true})
	if !slices.Contains(h.srv.features(), "attention") || slices.Contains(h.srv.features(), "space-mute") {
		t.Fatalf("features = %v, want attention without space-mute (no mute store here)", h.srv.features())
	}
	post := func(path string, body map[string]any) (int, string) {
		t.Helper()
		return h.postJSON(path, body, nil)
	}
	post2 := func(path, body string, out any) {
		t.Helper()
		if code, b := h.rawPost(path, body, out); code != 200 {
			t.Fatalf("POST %s %s = %d %s", path, body, code, b)
		}
	}
	if code, b := post("/api/inject/pane", map[string]any{"pane_id": "e2e:work", "alias": "build", "kind": "codex", "state": "working", "screen": "Running...\n"}); code != 200 {
		t.Fatalf("inject = %d %s", code, b)
	}
	if code, b := post("/api/inject/state", map[string]any{"pane_id": "e2e:work", "state": "done", "screen": "All pass.\n"}); code != 200 {
		t.Fatalf("inject done = %d %s", code, b)
	}
	view := func() (att struct {
		Group  string  `json:"group"`
		Unread bool    `json:"unread"`
		Pinned bool    `json:"pinned"`
		Turn   uint64  `json:"turn"`
		Seen   *uint64 `json:"seen"`
		Shared bool    `json:"shared"`
	}, counts map[string]int) {
		t.Helper()
		var st struct {
			Agents []struct {
				PaneID    string `json:"pane_id"`
				Attention *struct {
					Group  string  `json:"group"`
					Unread bool    `json:"unread"`
					Pinned bool    `json:"pinned"`
					Turn   uint64  `json:"turn"`
					Seen   *uint64 `json:"seen"`
					Shared bool    `json:"shared"`
				} `json:"attention"`
			} `json:"agents"`
			Counts map[string]int `json:"attention_counts"`
		}
		h.getJSON("/api/state", &st)
		for _, a := range st.Agents {
			if a.PaneID == "e2e:work" && a.Attention != nil {
				return *a.Attention, st.Counts
			}
		}
		t.Fatalf("e2e:work has no attention: %+v", st)
		return
	}

	a, counts := view()
	if a.Group != "done" || !a.Unread || a.Turn != 1 || a.Seen != nil || a.Shared || counts["done_unread"] != 1 {
		t.Fatalf("after the finish: %+v counts %v", a, counts)
	}
	post2("/api/agents/e2e:work/seen", fmt.Sprintf(`{"turn": %d}`, a.Turn), nil)
	if a, counts = view(); a.Unread || a.Seen == nil || *a.Seen != 1 || counts["done_unread"] != 0 {
		t.Errorf("after seen: %+v counts %v", a, counts)
	}
	post2("/api/agents/e2e:work/seen", `{"unread": true}`, nil)
	if a, _ = view(); !a.Unread || a.Seen == nil || *a.Seen != 0 {
		t.Errorf("after mark as unread: %+v", a)
	}
	post2("/api/agents/e2e:work/pin", `{"pinned": true}`, nil)
	if a, _ = view(); !a.Pinned {
		t.Errorf("after pin: %+v", a)
	}
	if code, _ := h.rawPost("/api/agents/e2e:nope/pin", `{"pinned": true}`, nil); code != http.StatusNotFound {
		t.Errorf("pin of an unknown agent = %d, want 404", code)
	}

	// The events a phone and the panel re-read on, with who made them.
	var vias []string
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "agent.attention" {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(ev.Payload, &p)
		vias = append(vias, fmt.Sprintf("%s:%s:unread=%v:pinned=%v", ev.PaneID, p["via"], p["unread"], p["pinned"]))
	}
	want := []string{
		"e2e:work:panel:unread=false:pinned=false",
		"e2e:work:panel:unread=true:pinned=false",
		"e2e:work:panel:unread=true:pinned=true",
	}
	if !slices.Equal(vias, want) {
		t.Errorf("agent.attention events = %q, want %q", vias, want)
	}
}

// The mute store is reached through the state and GET /api/spaces alone,
// and the Live Activity's priority follows it.
func TestLiveActivityUrgencyIgnoresMutedSpaces(t *testing.T) {
	h, stub := stubHarness(t)
	trackWorking(t, h, "w1:p2", "auth", "working")
	trackWorking(t, h, "w1:p3", "docs", "working")
	for _, p := range []string{"w1:p2", "w1:p3"} {
		if code, _ := h.postJSON("/api/inject/state", map[string]any{"pane_id": p, "state": "blocked", "screen": "Which one?\n> "}, nil); code != 200 {
			t.Fatalf("block %s = %d", p, code)
		}
	}
	snap := h.srv.activityState()
	if snap.Waiting != 2 {
		t.Fatalf("waiting = %d, want 2", snap.Waiting)
	}
	if got := h.srv.loudWaiting(); got != 2 {
		t.Errorf("loud waiting with nothing muted = %d, want 2", got)
	}
	stub.muted = map[string]bool{"w1:p2": true}
	if got := h.srv.loudWaiting(); got != 1 {
		t.Errorf("loud waiting with one space muted = %d, want 1", got)
	}
	tests := []struct {
		name       string
		known      int
		loud, was  int
		had        bool
		wantUrgent bool
	}{
		{name: "a new unmuted waiting agent alerts", known: 1, loud: 2, was: 1, had: true, wantUrgent: true},
		{name: "a new waiting agent in a muted space does not", known: 1, loud: 1, was: 1, had: true, wantUrgent: false},
		{name: "fewer waiting does not", known: 2, loud: 1, was: 2, had: true, wantUrgent: false},
		{name: "the first update alerts for an unmuted one", loud: 1, had: false, wantUrgent: true},
		{name: "the first update for a muted one does not", loud: 0, had: false, wantUrgent: false},
	}
	for _, tc := range tests {
		known := snap
		known.Waiting = tc.known
		cur := snap
		cur.Waiting = tc.known + 1
		if got := h.srv.activityUrgent(cur, known, tc.loud, tc.was, tc.had); got != tc.wantUrgent {
			t.Errorf("%s: urgent = %v, want %v", tc.name, got, tc.wantUrgent)
		}
	}
}

// rawPost posts a body as it is and decodes the answer into out.
func (h *harness) rawPost(path, body string, out any) (int, string) {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.http.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, string(raw)
}
