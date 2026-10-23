package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
)

// Without an orchestrator or a voice context log the context endpoints say so;
// the timeline still answers, with the reasons.
func TestContextEndpointsOff(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	var e map[string]string
	if code := h.getJSON("/api/context/brain", &e); code != http.StatusServiceUnavailable || !strings.Contains(e["error"], "--brain=off") {
		t.Errorf("brain = %d %v", code, e)
	}
	if code := h.getJSON("/api/context/voice", &e); code != http.StatusServiceUnavailable {
		t.Errorf("voice = %d %v", code, e)
	}
	var tl TimelineResponse
	if code := h.getJSON("/api/context/timeline", &tl); code != http.StatusOK || tl.BrainOff != "--brain=off" || tl.VoiceOff == "" || len(tl.Brain) != 0 || len(tl.Voice) != 0 {
		t.Errorf("timeline = %d %+v", code, tl)
	}
}

// linksOf collects every link id of the sections, with the labels that
// carry it.
func linksOf(secs []ctxcap.Section) map[string][]string {
	out := map[string][]string{}
	for _, s := range secs {
		for _, l := range s.Links {
			out[l] = append(out[l], s.Label)
		}
	}
	return out
}

func hasLabel(labels []string, prefix string) bool {
	for _, l := range labels {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// The orchestrator and voice context endpoints over the fake orchestrator and the text
// router, and the links between the panes for one round trip: a worker
// event, its announce turn and push, then a typed utterance relayed by
// the router, its request and reply in the orchestrator, its tool call and
// response on the voice side.
func TestContextEndpointsRoundTrip(t *testing.T) {
	h := newHarness(t, harnessOpts{brain: true, voiceCtx: true})
	var e map[string]string
	if code := h.getJSON("/api/context/brain", &e); code != http.StatusNotFound {
		t.Errorf("brain before any call = %d %v", code, e)
	}
	if code := h.getJSON("/api/context/voice", &e); code != http.StatusNotFound {
		t.Errorf("voice before anything = %d %v", code, e)
	}
	var res injectResponse
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, &res); code != http.StatusOK {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	var tr ToolResponse
	if code, body := h.postJSON("/api/utterance", map[string]any{"text": "what did auth say"}, &tr); code != http.StatusOK {
		t.Fatalf("utterance = %d %s", code, body)
	}
	h.srv.deps.Brain.Quiesce()

	var b BrainContextResponse
	if code := h.getJSON("/api/context/brain?step=latest", &b); code != http.StatusOK {
		t.Fatalf("brain latest = %d", code)
	}
	n := h.srv.deps.Brain.Requests().Len()
	if b.Step.Step != n || b.Source != "exact request" || b.Step.Kind != "utterance" || len(b.Body) == 0 || b.Diff.Prev != n-1 || len(b.Diff.Added) == 0 {
		t.Errorf("latest = step %d/%d source %q kind %s diff %+v", b.Step.Step, n, b.Source, b.Step.Kind, b.Diff)
	}
	total := 0
	for i, s := range b.Sections {
		total += s.Tokens
		if s.Text == "" || (i > 0 && s.Start != b.Sections[i-1].End) {
			t.Errorf("section %d (%s) text %q start %d", i, s.Label, s.Text, s.Start)
		}
	}
	if b.Tokens["total"] != total || b.Step.Tokens != total || b.Step.WindowTokens == 0 {
		t.Errorf("tokens %v, sum %d, step %d/%d", b.Tokens, total, b.Step.Tokens, b.Step.WindowTokens)
	}
	var first BrainContextResponse
	if code := h.getJSON("/api/context/brain?step=1", &first); code != http.StatusOK || first.Step.Step != 1 || first.Diff.Prev != 0 {
		t.Errorf("step 1 = %d %+v", code, first.Diff)
	}
	if code := h.getJSON("/api/context/brain?step=999", &e); code != http.StatusNotFound {
		t.Errorf("step 999 = %d", code)
	}
	if code := h.getJSON("/api/context/brain?step=abc", &e); code != http.StatusBadRequest {
		t.Errorf("step abc = %d", code)
	}

	var v VoiceContextResponse
	if code := h.getJSON("/api/context/voice", &v); code != http.StatusOK || v.Source != "reconstructed from what the daemon sent" || v.Turn.Session != "text-router#1" {
		t.Fatalf("voice latest = %d %+v", code, v.Turn)
	}
	if code := h.getJSON("/api/context/voice?turn=0x", &e); code != http.StatusBadRequest {
		t.Errorf("turn 0x = %d", code)
	}

	// The links.
	bl, vl := linksOf(b.Sections), linksOf(v.Sections)
	var call, push, evt string
	for l := range vl {
		switch {
		case strings.HasPrefix(l, "call:"):
			call = l
		case strings.HasPrefix(l, "push:"):
			push = l
		case strings.HasPrefix(l, "evt:"):
			evt = l
		}
	}
	if call == "" || push == "" || evt == "" {
		t.Fatalf("voice links = %v", vl)
	}
	checks := []struct {
		pane   map[string][]string
		link   string
		labels []string
	}{
		{vl, call, []string{"user typed", "tool call to_supervisor", "tool response to_supervisor"}},
		{bl, call, []string{"current request", "orchestrator tool call", "orchestrator tool result"}},
		{vl, push, []string{"push ("}},
		{bl, push, []string{"push"}},
		{vl, evt, []string{"push ("}},
		{bl, evt, []string{"worker event", "request", "orchestrator reply"}},
	}
	for _, c := range checks {
		for _, want := range c.labels {
			if !hasLabel(c.pane[c.link], want) {
				t.Errorf("%s: no %q among %v", c.link, want, c.pane[c.link])
			}
		}
	}
	// The utterance turn's reply is only in the next step's window: the
	// step that produced it holds its request, not its answer.
	h.postJSON("/api/utterance", map[string]any{"text": "and now?"}, &tr)
	h.srv.deps.Brain.Quiesce()
	h.getJSON("/api/context/brain", &b)
	if !hasLabel(linksOf(b.Sections)[call], "orchestrator reply") {
		t.Errorf("%s: no brain reply in the next turn's window: %v", call, linksOf(b.Sections)[call])
	}

	var tl TimelineResponse
	if code := h.getJSON("/api/context/timeline", &tl); code != http.StatusOK || len(tl.Brain) != h.srv.deps.Brain.Requests().Len() || len(tl.Voice) == 0 || tl.WindowTokens == 0 || tl.Brain[0].Text != "" {
		t.Errorf("timeline = %d brain %d voice %d", code, len(tl.Brain), len(tl.Voice))
	}
	steps := 0
	for _, typ := range h.types() {
		if typ == "context.step" {
			steps++
		}
	}
	if steps < len(tl.Brain)+len(tl.Voice) {
		t.Errorf("context.step events = %d, want at least %d", steps, len(tl.Brain)+len(tl.Voice))
	}
}
