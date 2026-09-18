package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// The brain endpoints answer 503 with the reason when no brain is
// attached (the Supervisor tab's "Brain off" state).
func TestBrainEndpointsOff(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	for _, path := range []string{"/api/brain/transcript", "/api/brain/context", "/api/brain/system_prompt", "/api/brain/models"} {
		var body map[string]string
		if code := h.getJSON(path, &body); code != http.StatusServiceUnavailable || !strings.Contains(body["error"], "brain is off (--brain=off)") {
			t.Errorf("%s = %d %v", path, code, body)
		}
	}
	var body map[string]string
	if code, _ := h.putModel(map[string]any{"provider": "gemini", "model": "gemini-3.8-flash"}, &body); code != http.StatusServiceUnavailable || !strings.Contains(body["error"], "brain is off") {
		t.Errorf("PUT /api/brain/model = %d %v", code, body)
	}
	var st StateResponse
	if h.getJSON("/api/state", &st); st.Brain != nil {
		t.Errorf("state.brain with the brain off = %+v", st.Brain)
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
	// worker_event (blocked), push (the question), the block's summarize
	// event turn (request, the screen the supervisor read, the reflex
	// model's "nothing to add" reply), then the ask turn: the text
	// router names no agent, so the reflex model lists the agents and
	// reads the one the question names.
	if got := strings.Join(kinds, " "); got != "worker_event push request event_screen reply request brain_tool_call brain_tool_result brain_tool_call brain_tool_result reply" {
		t.Fatalf("kinds = %s", got)
	}
	if msgs[6].ToolCall == nil || msgs[6].ToolCall.Name != brain.ToolListAgents || msgs[8].ToolCall == nil || msgs[8].ToolCall.Name != brain.ToolReadScreen || msgs[9].ToolResult == nil || !strings.Contains(msgs[9].Content, "BEGIN_WORKER_OUTPUT agent=auth source=screen") || msgs[9].Turn != 2 {
		t.Errorf("tool messages = %+v / %+v / %+v", msgs[6], msgs[8], msgs[9])
	}
	if msgs[3].Kind != brain.KindEventScreen || msgs[3].Turn != 1 || msgs[3].ToolResult == nil || !strings.Contains(msgs[3].Content, "BEGIN_WORKER_OUTPUT agent=auth source=screen") {
		t.Errorf("event screen = %+v", msgs[3])
	}
	var tail []TranscriptMessage
	if h.getJSON("/api/brain/transcript?since=9", &tail); len(tail) != 2 || tail[0].Seq != 10 {
		t.Errorf("since=9 → %+v", tail)
	}
	if h.getJSON("/api/brain/transcript?n=2", &tail); len(tail) != 2 || tail[0].Seq != 10 {
		t.Errorf("n=2 → %+v", tail)
	}

	var cx ContextResponse
	if code := h.getJSON("/api/brain/context", &cx); code != http.StatusOK {
		t.Fatalf("/api/brain/context = %d", code)
	}
	if cx.WindowTokens != brain.DefaultWindowTokens || cx.EstimatedTokens <= 0 || len(cx.Window) != 11 || cx.Turns != 2 || cx.TranscriptLen != 11 || cx.LastCompaction != nil || cx.Model != brain.DefaultModel {
		t.Errorf("context = %+v", cx)
	}
	sum := 0
	for _, w := range cx.Window {
		sum += w.Tokens
		// Turn 1's event screen is already a stub after turn 2; nothing
		// of turn 2 is.
		if w.Pruned != (w.Kind == brain.KindEventScreen) {
			t.Errorf("pruning after turn 2: %+v", w)
		}
	}
	if sum != cx.EstimatedTokens {
		t.Errorf("window sum %d != estimated %d", sum, cx.EstimatedTokens)
	}
	if n := len(cx.SystemPrompt.Sections); n != 12 || cx.SystemPrompt.Sections[0].Source != "embedded" || cx.SystemPrompt.Sections[n-1].Source != "dynamic" || cx.SystemPrompt.TotalChars == 0 || cx.SystemPrompt.Sections[0].Text != "" {
		t.Errorf("system prompt summary = %+v", cx.SystemPrompt)
	}

	// The next turn prunes the earlier turns' screens (the tool result
	// and the event screen) to stubs in the window; the transcript API
	// still serves the full result.
	if code, body := h.postJSON("/api/inject/utterance", map[string]any{"text": "what did auth say"}, &tr); code != http.StatusOK {
		t.Fatalf("utterance 2 = %d %s", code, body)
	}
	h.srv.deps.Brain.Quiesce()
	if h.getJSON("/api/brain/context", &cx); len(cx.Window) != 17 || !cx.Window[9].Pruned || !strings.HasPrefix(cx.Window[9].Stub, "[screen of auth, ") || cx.Window[9].Seq != 10 || !cx.Window[7].Pruned || !cx.Window[3].Pruned || !strings.HasPrefix(cx.Window[3].Stub, "[screen of auth at blocked, ") || cx.Window[15].Pruned {
		t.Errorf("window after turn 3 = %+v", cx.Window)
	}
	var full []TranscriptMessage
	if h.getJSON("/api/brain/transcript?since=9", &full); len(full) != 8 || full[0].Seq != 10 || !strings.Contains(full[0].Content, "BEGIN_WORKER_OUTPUT") || full[7].Kind != brain.KindReply || full[7].ToolResult != nil {
		t.Errorf("transcript lost the full result: %+v", full)
	}

	// A promise made through the text router ("let me know when …" is a
	// propose; the reflex records it) is served with its record.
	if code, body := h.postJSON("/api/inject/utterance", map[string]any{"text": "let me know when auth is done"}, &tr); code != http.StatusOK || tr.Tool != "propose" || tr.Spoken != "I will tell you when auth finishes." {
		t.Fatalf("promise utterance = %d %s (%+v)", code, body, tr)
	}
	h.srv.deps.Brain.Quiesce()
	var withPromise []TranscriptMessage
	h.getJSON("/api/brain/transcript?since=17", &withPromise)
	var pm *TranscriptMessage
	for i := range withPromise {
		if withPromise[i].Kind == brain.KindPromise {
			pm = &withPromise[i]
		}
	}
	if pm == nil || pm.Promise == nil || pm.Promise.Status != brain.PromiseOpen || pm.Promise.Agent != "auth" || pm.Promise.Event != brain.PromiseEventDone || pm.Turn != 4 {
		t.Errorf("promise message = %+v", pm)
	}

	var sp PromptInfo
	if code := h.getJSON("/api/brain/system_prompt", &sp); code != http.StatusOK || len(sp.Sections) != 12 || !strings.Contains(sp.Sections[0].Text, "SVP") || !strings.Contains(sp.Text, "## Agents") || !strings.Contains(sp.Text, "auth") {
		t.Errorf("/api/brain/system_prompt = %d %+v", code, sp)
	}

	// The SSE stream carried a brain.transcript per message.
	got := 0
	deadline := time.After(3 * time.Second)
	for got < 22 {
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

// The model picker's endpoints: GET lists the discovered catalog with the
// current model (recommended first; ?refresh=1 fetches again; a failed
// fetch keeps the last list and reports the error, never a 5xx), PUT
// switches the brain's model (validated; gemini is the only provider;
// a well-formed name the catalog does not list is accepted), persists
// it in the state file's session and logs brain.model_changed; /api/state
// and /api/brain/context report the live model.
func TestBrainModelEndpoints(t *testing.T) {
	h := newHarness(t, harnessOpts{brain: true})
	var got BrainModelsResponse
	if code := h.getJSON("/api/brain/models", &got); code != http.StatusOK {
		t.Fatalf("GET = %d", code)
	}
	if got.Provider != "gemini" || len(got.Providers) != 1 || got.Providers[0].ID != "gemini" || got.Model != brain.DefaultModel || got.Error != "" || got.Stale || got.FetchedAt == nil {
		t.Fatalf("GET body = %+v", got)
	}
	if len(got.Models) != len(braintest.Catalog()) || got.Models[0].Name != "gemini-3.8-flash" || !got.Models[0].Recommended || got.Models[len(got.Models)-1].Name != braintest.FakeModel || got.Models[len(got.Models)-1].Recommended {
		t.Fatalf("GET models = %+v", got.Models)
	}
	var st StateResponse
	if code := h.getJSON("/api/state", &st); code != http.StatusOK || st.Brain == nil || st.Brain.Provider != "gemini" || st.Brain.Model != brain.DefaultModel || st.Session.BrainModel != "" {
		t.Fatalf("state before = %d %+v", code, st.Brain)
	}

	// Set: a listed model.
	var out BrainModelsResponse
	if code, body := h.putModel(map[string]any{"provider": "gemini", "model": "gemini-3.8-flash"}, &out); code != http.StatusOK || out.Model != "gemini-3.8-flash" || len(out.Models) != len(braintest.Catalog()) || out.Provider != "gemini" {
		t.Fatalf("PUT = %d %s", code, body)
	}
	if h.srv.deps.Brain.Model() != "gemini-3.8-flash" || h.store.Snapshot().Session.BrainModel != "gemini-3.8-flash" {
		t.Errorf("brain = %q session = %q", h.srv.deps.Brain.Model(), h.store.Snapshot().Session.BrainModel)
	}
	if h.getJSON("/api/state", &st); st.Brain == nil || st.Brain.Model != "gemini-3.8-flash" || st.Session.BrainModel != "gemini-3.8-flash" {
		t.Errorf("state after = %+v / %q", st.Brain, st.Session.BrainModel)
	}
	var cx ContextResponse
	if h.getJSON("/api/brain/context", &cx); cx.Model != "gemini-3.8-flash" {
		t.Errorf("context.model = %q", cx.Model)
	}
	var msgs []TranscriptMessage
	if h.getJSON("/api/brain/transcript", &msgs); len(msgs) != 1 || msgs[0].Kind != brain.KindSystem || msgs[0].Content != "model switched from "+brain.DefaultModel+" to gemini-3.8-flash" {
		t.Errorf("transcript marker = %+v", msgs)
	}
	// Unknown but well-formed (the catalog may be stale): accepted.
	if code, body := h.putModel(map[string]any{"model": "models/gemini-4.0-pro-preview"}, &out); code != http.StatusOK || out.Model != "gemini-4.0-pro-preview" {
		t.Fatalf("PUT unknown = %d %s", code, body)
	}
	// Bad requests: empty, odd characters, another provider, garbage.
	for _, body := range []map[string]any{{"model": ""}, {"model": "gemini flash"}, {"provider": "openai", "model": "gpt-5"}} {
		var e map[string]string
		if code, raw := h.putModel(body, &e); code != http.StatusBadRequest || e["error"] == "" {
			t.Errorf("PUT %v = %d %s", body, code, raw)
		}
		if body["provider"] == "openai" && !strings.Contains(e["error"], "only gemini") {
			t.Errorf("provider error = %q", e["error"])
		}
	}
	var e map[string]string
	if code, _ := h.putModel("garbage", &e); code != http.StatusBadRequest {
		t.Errorf("PUT garbage = %d", code)
	}
	if h.srv.deps.Brain.Model() != "gemini-4.0-pro-preview" || h.store.Snapshot().Session.BrainModel != "gemini-4.0-pro-preview" {
		t.Errorf("bad requests changed the model: %q / %q", h.srv.deps.Brain.Model(), h.store.Snapshot().Session.BrainModel)
	}
	var changed []string
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "brain.model_changed" {
			var p struct{ From, To, Via string }
			_ = json.Unmarshal(ev.Payload, &p)
			changed = append(changed, p.From+">"+p.To+"/"+p.Via)
		}
	}
	if strings.Join(changed, " ") != brain.DefaultModel+">gemini-3.8-flash/debug gemini-3.8-flash>gemini-4.0-pro-preview/debug" {
		t.Errorf("brain.model_changed = %v", changed)
	}

	// Discovery failure: refresh keeps the last list and says why.
	h.model.ListErr = errors.New("catalog down")
	if code := h.getJSON("/api/brain/models?refresh=1", &got); code != http.StatusOK || got.Error != "catalog down" || !got.Stale || len(got.Models) != len(braintest.Catalog()) || got.Model != "gemini-4.0-pro-preview" {
		t.Errorf("failed refresh = %d %+v", code, got)
	}
	// Without a refresh the cache is served, no error.
	var cached BrainModelsResponse
	if code := h.getJSON("/api/brain/models", &cached); code != http.StatusOK || cached.Error != "" || cached.Stale || len(cached.Models) != len(braintest.Catalog()) {
		t.Errorf("cached = %d %+v", code, cached)
	}
}

// With no catalog fetched yet and discovery failing, GET is still a 200
// with an empty list and the error, so the picker can show the current
// model.
func TestBrainModelsNoCache(t *testing.T) {
	h := newHarness(t, harnessOpts{brain: true})
	h.model.ListErr = errors.New("no network")
	var got BrainModelsResponse
	if code := h.getJSON("/api/brain/models", &got); code != http.StatusOK || got.Error != "no network" || got.Stale || len(got.Models) != 0 || got.Models == nil || got.Model != brain.DefaultModel || got.FetchedAt != nil {
		t.Errorf("GET = %d %+v", code, got)
	}
}
