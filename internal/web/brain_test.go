package web

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// The brain endpoints answer 503 with the reason when no brain is
// attached (the Supervisor tab's "Brain off" state).
func TestBrainEndpointsOff(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	for _, path := range []string{"/api/brain/transcript", "/api/brain/context", "/api/brain/system_prompt"} {
		var body map[string]string
		if code := h.getJSON(path, &body); code != http.StatusServiceUnavailable || !strings.Contains(body["error"], "brain is off (--brain=off)") {
			t.Errorf("%s = %d %v", path, code, body)
		}
	}
}

// With a brain (the reflex fake model) the inject chain feeds the
// transcript, an ask runs a real turn, and the three endpoints report it:
// the transcript with seqs (incrementally), the window with a pruned
// stub after the next turn, and the prompt sections with their source.
func TestBrainEndpointsWithFakeBrain(t *testing.T) {
	h := newHarness(t, harnessOpts{brain: true})
	// Subscribe to SSE first so brain.transcript events are observed live.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.http.URL+"/events?type=brain.transcript", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	seen := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: ") {
				seen <- line
			}
		}
	}()

	var res injectResponse
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, &res); code != http.StatusOK {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	var tr ToolResponse
	if code, body := h.postJSON("/api/inject/utterance", map[string]any{"text": "what did auth say"}, &tr); code != http.StatusOK || tr.Tool != "ask" || !strings.HasPrefix(tr.Spoken, "The screen ends with:") {
		t.Fatalf("utterance = %d %s", code, body)
	}
	h.srv.deps.Brain.Quiesce()

	var msgs []TranscriptMessage
	if code := h.getJSON("/api/brain/transcript", &msgs); code != http.StatusOK {
		t.Fatalf("/api/brain/transcript = %d", code)
	}
	var kinds []string
	for i, m := range msgs {
		kinds = append(kinds, string(m.Kind))
		if m.Seq != i+1 {
			t.Errorf("msgs[%d].seq = %d", i, m.Seq)
		}
	}
	// worker_event (blocked), push (the question), then the ask turn:
	// the text router names no agent, so the reflex model lists the
	// agents and reads the one the question names.
	if got := strings.Join(kinds, " "); got != "worker_event push request brain_tool_call brain_tool_result brain_tool_call brain_tool_result reply" {
		t.Fatalf("kinds = %s", got)
	}
	if msgs[3].ToolCall == nil || msgs[3].ToolCall.Name != brain.ToolListAgents || msgs[5].ToolCall == nil || msgs[5].ToolCall.Name != brain.ToolReadScreen || msgs[6].ToolResult == nil || !strings.Contains(msgs[6].Content, "BEGIN_WORKER_OUTPUT agent=auth source=screen") || msgs[6].Turn != 1 {
		t.Errorf("tool messages = %+v / %+v / %+v", msgs[3], msgs[5], msgs[6])
	}
	var tail []TranscriptMessage
	if h.getJSON("/api/brain/transcript?since=6", &tail); len(tail) != 2 || tail[0].Seq != 7 {
		t.Errorf("since=6 → %+v", tail)
	}
	if h.getJSON("/api/brain/transcript?n=2", &tail); len(tail) != 2 || tail[0].Seq != 7 {
		t.Errorf("n=2 → %+v", tail)
	}

	var cx ContextResponse
	if code := h.getJSON("/api/brain/context", &cx); code != http.StatusOK {
		t.Fatalf("/api/brain/context = %d", code)
	}
	if cx.WindowTokens != brain.DefaultWindowTokens || cx.EstimatedTokens <= 0 || len(cx.Window) != 8 || cx.Turns != 1 || cx.TranscriptLen != 8 || cx.LastCompaction != nil || cx.Model != brain.DefaultModel {
		t.Errorf("context = %+v", cx)
	}
	sum := 0
	for _, w := range cx.Window {
		sum += w.Tokens
		if w.Pruned {
			t.Errorf("pruned before the next turn: %+v", w)
		}
	}
	if sum != cx.EstimatedTokens {
		t.Errorf("window sum %d != estimated %d", sum, cx.EstimatedTokens)
	}
	if n := len(cx.SystemPrompt.Sections); n != 11 || cx.SystemPrompt.Sections[0].Source != "embedded" || cx.SystemPrompt.Sections[n-1].Source != "dynamic" || cx.SystemPrompt.TotalChars == 0 || cx.SystemPrompt.Sections[0].Text != "" {
		t.Errorf("system prompt summary = %+v", cx.SystemPrompt)
	}

	// A second turn prunes the first turn's screen to a stub in the
	// window; the transcript API still serves the full result.
	if code, body := h.postJSON("/api/inject/utterance", map[string]any{"text": "what did auth say"}, &tr); code != http.StatusOK {
		t.Fatalf("utterance 2 = %d %s", code, body)
	}
	h.srv.deps.Brain.Quiesce()
	if h.getJSON("/api/brain/context", &cx); len(cx.Window) != 14 || !cx.Window[6].Pruned || !strings.HasPrefix(cx.Window[6].Stub, "[screen of auth, ") || cx.Window[6].Seq != 7 || !cx.Window[4].Pruned || cx.Window[12].Pruned {
		t.Errorf("window after turn 2 = %+v", cx.Window)
	}
	var full []TranscriptMessage
	if h.getJSON("/api/brain/transcript?since=6", &full); len(full) != 8 || full[0].Seq != 7 || !strings.Contains(full[0].Content, "BEGIN_WORKER_OUTPUT") || full[7].Kind != brain.KindReply || full[7].ToolResult != nil {
		t.Errorf("transcript lost the full result: %+v", full)
	}

	var sp PromptInfo
	if code := h.getJSON("/api/brain/system_prompt", &sp); code != http.StatusOK || len(sp.Sections) != 11 || !strings.Contains(sp.Sections[0].Text, "SVP") || !strings.Contains(sp.Text, "## Agents") || !strings.Contains(sp.Text, "auth") {
		t.Errorf("/api/brain/system_prompt = %d %+v", code, sp)
	}

	// The SSE stream carried a brain.transcript per message.
	got := 0
	deadline := time.After(3 * time.Second)
	for got < 14 {
		select {
		case line := <-seen:
			if !strings.Contains(line, `"type":"brain.transcript"`) || !strings.Contains(line, `"seq":`) {
				t.Errorf("unexpected line %s", line)
			}
			got++
		case <-deadline:
			t.Fatalf("brain.transcript events on SSE = %d", got)
		}
	}
}
