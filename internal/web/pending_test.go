package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// /api/state's pending action carries what a page needs to answer it by
// tap, and POST /api/pending/answer answers it through the same rail
// and executor as a spoken answer.
func TestPendingAnswerByTap(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	var res injectResponse
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, &res); code != http.StatusOK {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	var st StateResponse
	if code := h.getJSON("/api/state", &st); code != http.StatusOK || st.PendingAction == nil {
		t.Fatalf("/api/state = %d %+v", code, st.PendingAction)
	}
	p := st.PendingAction
	if p.Agent != "auth" || p.Question == "" || p.Summary == "" || p.Command == "" || len(p.Options) < 2 {
		t.Fatalf("pending view = %+v", p)
	}
	defaults := 0
	for i, o := range p.Options {
		if o.Index != i+1 || o.Label == "" || len(o.Keys) == 0 {
			t.Errorf("option %d = %+v", i, o)
		}
		if o.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("%d default options, want 1: %+v", defaults, p.Options)
	}
	// pending.created carries the same options.
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "pending.created" {
			continue
		}
		var pl struct {
			Agent   string `json:"agent"`
			Command string `json:"command"`
			Options []struct {
				Index int    `json:"index"`
				Label string `json:"label"`
			} `json:"options"`
		}
		if err := json.Unmarshal(ev.Payload, &pl); err != nil || pl.Agent != "auth" || pl.Command == "" || len(pl.Options) != len(p.Options) || pl.Options[0].Index != 1 {
			t.Errorf("pending.created payload = %s (%v)", ev.Payload, err)
		}
	}

	var bad map[string]string
	if code, _ := h.postJSON("/api/pending/answer", map[string]any{"id": p.ID}, &bad); code != http.StatusBadRequest {
		t.Errorf("no option: %d %v", code, bad)
	}
	if code, _ := h.postJSON("/api/pending/answer", map[string]any{"id": p.ID, "option": 9}, &bad); code != http.StatusBadRequest {
		t.Errorf("option 9: %d %v", code, bad)
	}

	var ans map[string]any
	code, body := h.postJSON("/api/pending/answer", map[string]any{"id": p.ID, "option": 1}, &ans)
	if code != http.StatusOK || ans["ok"] != true || !strings.HasPrefix(ans["spoken"].(string), "Told auth ") {
		t.Fatalf("tap = %d %s", code, body)
	}
	want := []string{"herdr.send", "pending.confirmed"}
	if got := h.types(); !subsequence(got, want) {
		t.Fatalf("tap chain %v not in %v", want, got)
	}
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "pending.confirmed" {
			var pl map[string]any
			_ = json.Unmarshal(ev.Payload, &pl)
			if pl["via"] != "tap" || pl["option"] != float64(1) {
				t.Errorf("pending.confirmed = %s", ev.Payload)
			}
		}
	}
	// The same question again: it is no longer open.
	var gone map[string]string
	if code, _ := h.postJSON("/api/pending/answer", map[string]any{"id": p.ID, "option": 1}, &gone); code != http.StatusConflict || gone["error"] != "that question is no longer open" {
		t.Errorf("stale tap = %d %v", code, gone)
	}
}

// A typed utterance through the text router is a user input: the
// bare "yes" answers; a to_supervisor call with nothing typed does not.
func TestTypedUtteranceIsAShepherdInput(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, nil); code != http.StatusOK {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	// The voice model calling to_supervisor on its own (no typed input).
	var tr ToolResponse
	res, err := h.srv.runTool(t.Context(), toolCall("yes"), "voice")
	if err != nil || !strings.HasPrefix(res.Spoken, "Nothing was handed on") {
		t.Fatalf("model-initiated relay = %+v %v", res, err)
	}
	if code, body := h.postJSON("/api/utterance", map[string]any{"text": "yes"}, &tr); code != http.StatusOK || !strings.Contains(tr.Spoken, "auth asks to run printf") {
		t.Fatalf("first typed yes (re-ask) = %d %s", code, body)
	}
	if code, body := h.postJSON("/api/utterance", map[string]any{"text": "yes"}, &tr); code != http.StatusOK || !strings.Contains(tr.Spoken, "Told auth yes") {
		t.Fatalf("typed yes = %d %s", code, body)
	}
}

// toolCall is the voice model's to_supervisor call with text.
func toolCall(text string) voice.ToolCall {
	return voice.ToolCall{ID: "v1", Name: supervisor.ToolToSupervisor, Args: map[string]any{"text": text}}
}

// /voice approval cards and 1–9 digit shortcuts both
// submit POST /api/pending/answer: every answer goes through the same auth,
// idempotency, screen-verification and user-input approval rail as spoken or
// typed answers.
func TestVoiceApprovalRail(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	sendCount := func() int {
		n := 0
		for _, ev := range h.log.Tail(0) {
			if ev.Type == "herdr.send" {
				n++
			}
		}
		return n
	}

	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, nil); code != http.StatusOK {
		t.Fatalf("inject auth = %d %s", code, body)
	}
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:2", "alias": "api", "screen": codexScreen(t)}, nil); code != http.StatusOK {
		t.Fatalf("inject api = %d %s", code, body)
	}

	var st StateResponse
	if code := h.getJSON("/api/state", &st); code != http.StatusOK || st.PendingAction == nil || len(st.Questions) != 2 {
		t.Fatalf("/api/state = %d pending=%+v questions=%d", code, st.PendingAction, len(st.Questions))
	}
	var authQ, apiQ PendingView
	for _, q := range st.Questions {
		switch q.Agent {
		case "auth":
			authQ = q
		case "api":
			apiQ = q
		}
	}
	if authQ.ID == "" || apiQ.ID == "" || st.PendingAction.ID != apiQ.ID {
		t.Fatalf("questions = %+v pending = %+v", st.Questions, st.PendingAction)
	}

	// 1. Proxied-loopback rail: a reverse-proxied request without a paired
	// device session cannot approve over loopback.
	proxied := h.authedRequest(http.MethodPost, "/api/pending/answer", "", nil, map[string]any{"id": authQ.ID, "option": 1})
	proxied.Header.Set("X-Forwarded-For", "203.0.113.9")
	if code, _ := h.do(proxied, nil); code != http.StatusUnauthorized || sendCount() != 0 {
		t.Fatalf("proxied unauthenticated tap = %d, sends = %d", code, sendCount())
	}

	// 2. Answering an open /voice card outside the voice's current slot works,
	// deduplicates Idempotency-Key retries, keeps the voice's slot intact, and
	// leaves no unclaimed user input for a model-initiated relay to piggyback on.
	tapReq := func() *http.Request {
		r := h.authedRequest(http.MethodPost, "/api/pending/answer", "", nil, map[string]any{"id": authQ.ID, "option": 1})
		r.Header.Set("Idempotency-Key", "voice-card-auth-1")
		return r
	}
	var ans map[string]any
	if code, _ := h.do(tapReq(), &ans); code != http.StatusOK || ans["ok"] != true || sendCount() != 1 {
		t.Fatalf("tap auth = %d %v, sends = %d", code, ans, sendCount())
	}
	if code, _ := h.do(tapReq(), &ans); code != http.StatusOK || ans["ok"] != true || sendCount() != 1 {
		t.Fatalf("idempotent replay = %d %v, sends = %d", code, ans, sendCount())
	}
	if res, err := h.srv.runTool(t.Context(), toolCall("yes"), "voice"); err != nil || !strings.HasPrefix(res.Spoken, "Nothing was handed on") || sendCount() != 1 {
		t.Fatalf("post-tap model relay = %+v (%v), sends = %d", res, err, sendCount())
	}

	// 3. Screen re-read rail (stillAsking): if the pane cannot be read, nothing
	// is sent and the question stays open (502); once the screen moves off the
	// prompt, the stale card is cancelled with 0 keys sent (409).
	h.herdr.Remove("fake:2")
	var errBody map[string]string
	if code, _ := h.postJSON("/api/pending/answer", map[string]any{"id": apiQ.ID, "option": 1}, &errBody); code != http.StatusBadGateway || sendCount() != 1 {
		t.Fatalf("unreadable screen tap = %d %v, sends = %d", code, errBody, sendCount())
	}
	h.herdr.SetScreen("fake:2", "codex", "done working\n", herdr.StateDone)
	if code, _ := h.postJSON("/api/pending/answer", map[string]any{"id": apiQ.ID, "option": 1}, &errBody); code != http.StatusConflict || errBody["error"] != "that question is no longer open" || sendCount() != 1 {
		t.Fatalf("moved-on screen tap = %d %v, sends = %d", code, errBody, sendCount())
	}

	// 4. User-input approval rail (railInputLocked): a negative option label can
	// never send approving keys even through AnswerTap — refused with 403 and
	// logged as brain.rail_blocked (trigger: "tap").
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:3", "alias": "billing", "screen": codexScreen(t)}, nil); code != http.StatusOK {
		t.Fatalf("inject billing = %d %s", code, body)
	}
	cur, has := h.store.Pending()
	if !has || len(cur.Options) < 3 {
		t.Fatalf("billing pending = %+v (has=%v)", cur, has)
	}
	cur.Options[2].Keys = append([]string(nil), cur.Options[0].Keys...)
	if err := h.store.SetPending(cur); err != nil {
		t.Fatalf("SetPending: %v", err)
	}
	if err := h.store.SetQuestion(cur); err != nil {
		t.Fatalf("SetQuestion: %v", err)
	}
	code, body := h.postJSON("/api/pending/answer", map[string]any{"id": cur.ID, "option": 3}, &errBody)
	if code != http.StatusForbidden || !strings.Contains(errBody["error"], "cannot approve billing's prompt") || sendCount() != 1 {
		t.Fatalf("negative-label approving tap = %d %s, sends = %d", code, body, sendCount())
	}
	blockedTap := false
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "brain.rail_blocked" {
			continue
		}
		var pl map[string]any
		_ = json.Unmarshal(ev.Payload, &pl)
		if pl["trigger"] == "tap" && pl["id"] == cur.ID {
			blockedTap = true
		}
	}
	if !blockedTap {
		t.Fatalf("expected brain.rail_blocked with trigger=tap in %v", h.types())
	}
}
