package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

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
	if code, body := h.postJSON("/api/utterance", map[string]any{"text": "yes"}, &tr); code != http.StatusOK || !strings.Contains(tr.Spoken, "Told auth yes") {
		t.Fatalf("typed yes = %d %s", code, body)
	}
}

// toolCall is the voice model's to_supervisor call with text.
func toolCall(text string) voice.ToolCall {
	return voice.ToolCall{ID: "v1", Name: supervisor.ToolToSupervisor, Args: map[string]any{"text": text}}
}
