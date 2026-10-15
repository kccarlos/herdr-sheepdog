package brain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// compatHarness is a harness whose orchestrator runs on the DeepSeek adapter
// over srv (thinking on, reasoning effort low, as configured by default).
func compatHarness(t *testing.T, srv *compatServer) *harness {
	t.Helper()
	ds := newCompat(t, srv.srv.URL, true, "low", nil)
	h := newHarness(t, braintest.NewModel(), func(c *Config) { c.Provider = ProviderDeepSeek; c.Model = "deepseek-flash" })
	if err := h.brain.AddProvider(ProviderDeepSeek, ds); err != nil {
		t.Fatal(err)
	}
	return h
}

func announce(h *harness) (Reply, error) {
	return h.brain.Turn(context.Background(), Request{Kind: KindAnnounce, Agent: "docs", Event: "done", Template: "docs finished."})
}

// An event turn is sent with thinking disabled and no reasoning effort
// whatever deepseek.thinking says, and carries no reasoning_content; a
// user turn keeps the configured thinking (docs/dev/iteration-2.md WP5).
func TestThinkingOffForEventTurnsDeepSeek(t *testing.T) {
	srv := newCompatServer(t,
		toolCalls("Look at the screen first.", [3]string{"call_a", ToolReadScreen, `{"agent":"auth"}`}),
		ok(`{"spoken":"Auth is at a prompt.","detail":""}`),
		ok(`{"spoken":"Docs finished the readme.","urgency":"idle"}`),
	)
	h := compatHarness(t, srv)
	if _, err := h.ask("what is auth doing?", "auth"); err != nil {
		t.Fatal(err)
	}
	if _, err := announce(h); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		req := srv.request(i)
		if th, _ := req["thinking"].(map[string]any); th["type"] != "enabled" || req["reasoning_effort"] != "low" {
			t.Errorf("CEO step %d: thinking %v effort %v", i+1, req["thinking"], req["reasoning_effort"])
		}
	}
	if rc := srv.request(1)["messages"].([]any)[2].(map[string]any)["reasoning_content"]; rc != "Look at the screen first." {
		t.Errorf("the current turn's reasoning is not replayed: %v", rc)
	}
	ev := srv.request(2)
	if th, _ := ev["thinking"].(map[string]any); th["type"] != "disabled" {
		t.Errorf("event turn thinking = %v", ev["thinking"])
	}
	if _, set := ev["reasoning_effort"]; set {
		t.Errorf("event turn sent reasoning_effort %v", ev["reasoning_effort"])
	}
	body, _ := json.Marshal(ev["messages"])
	if strings.Contains(string(body), "reasoning_content") {
		t.Errorf("event turn replays reasoning: %s", body)
	}
}

// On Gemini an event turn asks for the minimal thinking level, a user
// turn for the configured one.
func TestThinkingOffForEventTurnsGemini(t *testing.T) {
	model := braintest.NewModel(braintest.Step{Text: braintest.Reply("Auth is working.", "")})
	model.EventDefault = &braintest.Step{Text: `{"spoken":"Docs finished.","urgency":"idle"}`}
	h := newHarness(t, model, nil)
	if _, err := h.ask("what is auth doing?", "auth"); err != nil {
		t.Fatal(err)
	}
	if _, err := announce(h); err != nil {
		t.Fatal(err)
	}
	calls := model.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if tc := calls[0].Config.ThinkingConfig; tc == nil || tc.ThinkingLevel != genai.ThinkingLevelLow {
		t.Errorf("CEO turn thinking = %+v", tc)
	}
	if tc := calls[1].Config.ThinkingConfig; tc == nil || tc.ThinkingLevel != genai.ThinkingLevelMinimal {
		t.Errorf("event turn thinking = %+v", tc)
	}
}

// render flattens a chat completions body the way a prefix cache sees
// it: the system message, the tools, then every message in order.
func render(req map[string]any) string {
	var b strings.Builder
	msgs := req["messages"].([]any)
	sys, _ := json.Marshal(msgs[0])
	tools, _ := json.Marshal(req["tools"])
	b.Write(sys)
	b.Write(tools)
	for _, m := range msgs[1:] {
		mm := m.(map[string]any)
		content, _ := mm["content"].(string)
		b.WriteString("\x00" + mm["role"].(string) + "\x00" + content)
		if rc, _ := mm["reasoning_content"].(string); rc != "" {
			b.WriteString("\x00" + rc)
		}
		if tc, ok := mm["tool_calls"]; ok {
			j, _ := json.Marshal(tc)
			b.Write(j)
		}
	}
	return b.String()
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// Consecutive requests share a long prefix: nothing per-turn sits before
// the state block, which travels last (docs/dev/iteration-2.md WP5). Within
// a turn the next step shares everything before the previous step's
// state block — system prompt, tools, the whole conversation. From one
// turn to the next it shares everything before the previous turn: that
// turn's reasoning_content stops being replayed (only the current
// turn's is), which ends the match there by design.
func TestStablePrefix(t *testing.T) {
	srv := newCompatServer(t,
		toolCalls("Look.", [3]string{"call_a", ToolReadScreen, `{"agent":"auth"}`}),
		ok(`{"spoken":"Auth is at a prompt.","detail":""}`),
		ok(`{"spoken":"Still at the prompt.","detail":""}`),
	)
	h := compatHarness(t, srv)
	if _, err := h.ask("what is auth doing?", "auth"); err != nil {
		t.Fatal(err)
	}
	h.brain.Pushed("auth", "auth is asking something.", "idle")
	if _, err := h.ask("and now?", "auth"); err != nil {
		t.Fatal(err)
	}
	system := srv.request(0)["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.Contains(system, StateHeader) || strings.Contains(system, "## Agents") || strings.Contains(system, "## Now") {
		t.Fatalf("state in the system message")
	}
	if srv.count() != 3 {
		t.Fatalf("requests = %d", srv.count())
	}
	r0, r1, r2 := render(srv.request(0)), render(srv.request(1)), render(srv.request(2))
	for i, r := range []string{r0, r1, r2} {
		at := strings.LastIndex(r, StateHeader)
		if at < 0 || strings.Contains(r[at:], "\x00") || !strings.Contains(r[at:], "## Now") {
			t.Fatalf("request %d: the state block is not the last thing", i)
		}
	}
	// Turn 1, step 1 -> step 2: everything before the state block (the
	// state line's own separator is the one byte allowed to differ).
	if got, want := commonPrefix(r0, r1), strings.Index(r0, StateHeader)-1; got < want {
		t.Errorf("steps of one turn share %d bytes, want >= %d", got, want)
	}
	// Turn 1 -> turn 2: everything before turn 1.
	turn1 := strings.Index(r1, `utterance="what is auth doing?"`)
	if got := commonPrefix(r1, r2); turn1 < len(system) || got < turn1 {
		t.Errorf("turns share %d bytes, want >= %d (everything before the previous turn; system %d)", got, turn1, len(system))
	}
}

// A long read_screen result enters the window as its last 4 KB with the
// delimiters intact; the transcript keeps it whole.
func TestToolResultCappedInWindow(t *testing.T) {
	var lines []string
	for i := 0; i < 300; i++ {
		lines = append(lines, strings.Repeat("x", 50)+" build step output line")
	}
	model := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth", "lines": float64(300)})}},
		braintest.Step{Text: braintest.Reply("Auth is building.", "")},
	)
	h := newHarness(t, model, nil)
	h.host.SetScreen("w5:p1", strings.Join(lines, "\n")+"\nFINAL LINE")
	if _, err := h.ask("what is auth doing?", "auth"); err != nil {
		t.Fatal(err)
	}
	var full Message
	for _, m := range h.brain.Transcript().Messages() {
		if m.Kind == KindToolResult {
			full = m
		}
	}
	if len(full.Content) < 12<<10 {
		t.Fatalf("transcript result = %d bytes, want the whole read", len(full.Content))
	}
	var fr *genai.FunctionResponse
	for _, c := range model.Calls()[1].Contents {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				fr = p.FunctionResponse
			}
		}
	}
	out, _ := fr.Response["output"].(string)
	if len(out) > 4<<10 || !strings.HasPrefix(out, "<<<BEGIN_WORKER_OUTPUT") || !strings.HasSuffix(unnonce(out), "<<<END_WORKER_OUTPUT>>>") || !strings.Contains(out, "FINAL LINE") {
		t.Errorf("window result = %d bytes: %.80q … %q", len(out), out, out[max(0, len(out)-40):])
	}
}

// DeepSeek's prompt_cache_hit_tokens reaches brain.step as
// cache_hit_tokens.
func TestCacheHitTokensLogged(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"spoken":"Fine.","detail":""}`}}},
		"usage":   map[string]any{"prompt_tokens": 3000, "completion_tokens": 30, "total_tokens": 3030, "prompt_cache_hit_tokens": 2688, "prompt_cache_miss_tokens": 312},
	})
	srv := newCompatServer(t, compatReply{status: 200, body: string(body)})
	h := compatHarness(t, srv)
	if _, err := h.ask("ok?", "auth"); err != nil {
		t.Fatal(err)
	}
	steps := h.log.ofType("brain.step")
	if len(steps) != 1 || payload(steps[0])["cache_hit_tokens"] != int32(2688) {
		t.Errorf("brain.step = %v", steps)
	}
}
