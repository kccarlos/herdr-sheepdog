package brain

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

// voiceLineFixturePath is a real transcript segment (2026-09-24,
// contents and reasoning trimmed): in turn 6 the voice said "On it."
// while read_screen ran, so the transcript holds call → assistant_said →
// result, and every later DeepSeek call of the segment failed with HTTP
// 400 ("An assistant message with 'tool_calls' must be followed by tool
// messages responding to each 'tool_call_id'").
const voiceLineFixturePath = "../../testdata/brain/voice-line-mid-step.jsonl"

// validChat enforces the OpenAI-compatible rule DeepSeek applies: an
// assistant message with tool_calls is immediately followed by one tool
// message per call id, and no tool message stands anywhere else.
func validChat(msgs []chatMessage) error {
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role == "tool" {
			return fmt.Errorf("message %d: tool message %q with no assistant tool_calls before it", i, m.ToolCallID)
		}
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		want := map[string]bool{}
		for _, tc := range m.ToolCalls {
			want[tc.ID] = true
		}
		for len(want) > 0 {
			i++
			if i >= len(msgs) || msgs[i].Role != "tool" {
				return fmt.Errorf("message %d: assistant tool_calls not followed by a tool message for each id (missing %v)", i, keys(want))
			}
			if !want[msgs[i].ToolCallID] {
				return fmt.Errorf("message %d: tool message %q answers no pending call", i, msgs[i].ToolCallID)
			}
			delete(want, msgs[i].ToolCallID)
		}
	}
	return nil
}

// validContents is the same rule for Gemini: a model content with
// function calls is immediately followed by a user content holding one
// function response per call, and no function response stands elsewhere.
func validContents(cs []*genai.Content) error {
	for i := 0; i < len(cs); i++ {
		calls := 0
		for _, p := range cs[i].Parts {
			if p.FunctionResponse != nil {
				return fmt.Errorf("content %d: function response with no function call before it", i)
			}
			if p.FunctionCall != nil {
				calls++
			}
		}
		if calls == 0 {
			continue
		}
		i++
		if i >= len(cs) {
			return fmt.Errorf("content %d: function calls at the end of the request", i-1)
		}
		responses := 0
		for _, p := range cs[i].Parts {
			if p.FunctionResponse == nil {
				return fmt.Errorf("content %d: function calls not followed by their responses (got %q)", i, p.Text)
			}
			responses++
		}
		if responses != calls {
			return fmt.Errorf("content %d: %d function responses for %d calls", i, responses, calls)
		}
	}
	return nil
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func testCompat(t *testing.T) *OpenAICompatModel {
	t.Helper()
	m, err := NewOpenAICompatModel(OpenAICompatConfig{Provider: ProviderDeepSeek, BaseURL: "http://127.0.0.1:1/", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// render runs msgs through both renderers and checks both requests are
// valid; it returns the chat messages for further checks.
func render(t *testing.T, msgs []Message) []chatMessage {
	t.Helper()
	chat := testCompat(t).messagesOf(msgs, "deepseek-flash")
	if err := validChat(chat); err != nil {
		t.Errorf("openai-compatible request: %v\n%s", err, chatShape(chat))
	}
	for _, provider := range []string{ProviderGemini, ProviderDeepSeek} {
		// Rebuilt from the neutral fields (another origin's Raw) and, for
		// the Gemini-origin case, replayed from Raw when it has one.
		if err := validContents(toContents(msgs, provider, "deepseek-flash")); err != nil {
			t.Errorf("gemini request (%s): %v", provider, err)
		}
	}
	return chat
}

// chatShape is a compact rendering of a chat request for failures:
// role, tool call ids, tool_call_id and the first words of user text.
func chatShape(chat []chatMessage) string {
	var parts []string
	for _, m := range chat {
		s := m.Role
		for _, tc := range m.ToolCalls {
			s += "+" + tc.ID
		}
		if m.ToolCallID != "" {
			s += "@" + m.ToolCallID
		}
		if m.Role == "user" {
			s += fmt.Sprintf("(%.30q)", m.Content)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

var orderTS = time.Date(2026, 9, 24, 18, 16, 43, 0, time.UTC)

func ctxMsg(kind MessageKind, text string) Message {
	return Message{TS: orderTS, Role: RoleUser, Kind: kind, Content: text}
}

func callMsg(turn int, id, name string, raw string) Message {
	m := Message{TS: orderTS, Role: RoleAssistant, Kind: KindToolCall, Turn: turn, Provider: ProviderDeepSeek, Model: "deepseek-flash", ToolCall: &ToolCall{ID: id, Name: name, Args: map[string]any{"agent": "auth"}}}
	if raw != "" {
		m.Raw = []byte(raw)
	}
	return m
}

func resultMsg(turn int, id, name, text string) Message {
	return Message{TS: orderTS, Role: RoleTool, Kind: KindToolResult, Turn: turn, Content: text, ToolResult: &ToolResult{ID: id, Name: name}}
}

func requestMsg(turn int, text string) Message {
	return Message{TS: orderTS, Role: RoleUser, Kind: KindRequest, Turn: turn, Content: text}
}

// TestModelOrderVoiceLineMidStep is turn 6 of 2026-09-24 built from the
// message kinds: the assistant's "On it." was ingested between the
// read_screen call and its result. Both renderers keep the result
// adjacent to its call and move the line to just after it; the rest of
// the window keeps its order.
func TestModelOrderVoiceLineMidStep(t *testing.T) {
	raw := `{"role":"assistant","content":"","reasoning_content":"read more","tool_calls":[{"id":"call_00_Bk","type":"function","function":{"name":"read_screen","arguments":"{\"agent\":\"auth\"}"}}]}`
	msgs := []Message{
		ctxMsg(KindCEOUtterance, "Um, I want to know the detail of the glitches"),
		requestMsg(6, `Request from the assistant (utterance): utterance="Um, I want to know the detail of the glitches"`),
		callMsg(6, "call_00_Bk", ToolReadScreen, raw),
		ctxMsg(KindAssistantSaid, "On it."),
		resultMsg(6, "call_00_Bk", ToolReadScreen, "<<<BEGIN_WORKER_OUTPUT agent=auth source=screen>>>\nglitches\n<<<END_WORKER_OUTPUT>>>"),
		ctxMsg(KindPush, "(idle) Four agents are idle."),
		requestMsg(7, `Request from the assistant (stop): stopped="nothing was in flight"`),
	}
	if got, want := fmt.Sprint(modelOrder(msgs)), "[0 1 2 4 3 5 6]"; got != want {
		t.Errorf("modelOrder = %s, want %s", got, want)
	}
	chat := render(t, msgs)
	if got, want := chatShape(chat), `user("[18:16:43] User said: \"Um, I w") assistant+call_00_Bk tool@call_00_Bk user("[18:16:43] Assistant said: \"On")`; got != want {
		t.Errorf("request = %s\nwant      %s", got, want)
	}
	if last := chat[len(chat)-1].Content; !strings.Contains(last, "Pushed to the assistant") || !strings.Contains(last, "(stop)") {
		t.Errorf("the moved line, the push and the next request should merge into one user message after the result: %q", last)
	}
	// Gemini: model(call) → user(response) → user(text), the text after.
	cs := toContents(msgs, ProviderGemini, "gemini-3.5-flash")
	if len(cs) != 4 || cs[1].Parts[0].FunctionCall == nil || cs[2].Parts[0].FunctionResponse == nil || !strings.Contains(cs[3].Parts[0].Text, "Assistant said") {
		t.Errorf("gemini contents = %d: %+v", len(cs), cs)
	}
}

// TestModelOrderMultiCallStep: a step of two calls with speech between
// the calls and between the results, a push among them; relative order
// of the moved lines is kept.
func TestModelOrderMultiCallStep(t *testing.T) {
	raw := `{"role":"assistant","content":"","tool_calls":[{"id":"a","type":"function","function":{"name":"read_screen","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"list_agents","arguments":"{}"}}]}`
	msgs := []Message{
		requestMsg(3, "Request from the assistant (utterance): utterance=\"read both\""),
		callMsg(3, "a", ToolReadScreen, raw),
		ctxMsg(KindAssistantSaid, "Checking."),
		callMsg(3, "b", ToolListAgents, ""),
		resultMsg(3, "a", ToolReadScreen, "screen"),
		ctxMsg(KindWorkerEvent, "docs: idle -> working"),
		ctxMsg(KindCEOUtterance, "Hello."),
		resultMsg(3, "b", ToolListAgents, "[]"),
		{TS: orderTS, Role: RoleAssistant, Kind: KindReply, Turn: 3, Content: `{"spoken":"ok"}`},
	}
	if got, want := fmt.Sprint(modelOrder(msgs)), "[0 1 3 4 7 2 5 6 8]"; got != want {
		t.Errorf("modelOrder = %s, want %s", got, want)
	}
	chat := render(t, msgs)
	if got, want := chatShape(chat), `user("[18:16:43] Request from the as") assistant+a+b tool@a tool@b user("[18:16:43] Assistant said: \"Ch") assistant`; got != want {
		t.Errorf("request = %s\nwant      %s", got, want)
	}
	if u := chat[4].Content; strings.Index(u, "Checking.") > strings.Index(u, "docs: idle") || strings.Index(u, "docs: idle") > strings.Index(u, "Hello.") {
		t.Errorf("moved lines out of order: %q", u)
	}
}

// TestModelOrderOrphans: a step whose result never arrived (a transcript
// cut short mid-step) is dropped whole, its context kept; a result whose
// call is not in the window (cut off by an older compaction) is dropped.
func TestModelOrderOrphans(t *testing.T) {
	raw := `{"role":"assistant","content":"","tool_calls":[{"id":"x","type":"function","function":{"name":"read_screen","arguments":"{}"}},{"id":"y","type":"function","function":{"name":"recall","arguments":"{}"}}]}`
	msgs := []Message{
		resultMsg(1, "gone", ToolRecall, "an answer whose call was compacted away"),
		ctxMsg(KindCompaction, "summary"),
		requestMsg(2, "Request from the assistant (utterance): utterance=\"what now\""),
		callMsg(2, "x", ToolReadScreen, raw),
		callMsg(2, "y", ToolRecall, ""),
		ctxMsg(KindAssistantSaid, "On it."),
		resultMsg(2, "x", ToolReadScreen, "screen"),
		// The daemon died here: y never got its result.
		ctxMsg(KindSystem, "model switched"),
		requestMsg(3, "Request from the assistant (utterance): utterance=\"hello\""),
		callMsg(3, "z", ToolListAgents, ""),
		// …and here, at the end of the window.
	}
	if got, want := fmt.Sprint(modelOrder(msgs)), "[1 2 5 7 8]"; got != want {
		t.Errorf("modelOrder = %s, want %s", got, want)
	}
	chat := render(t, msgs)
	for _, m := range chat {
		if len(m.ToolCalls) > 0 || m.Role == "tool" {
			t.Errorf("an orphaned step or result was rendered: %s", chatShape(chat))
			break
		}
	}
}

// TestModelOrderWellFormedUnchanged: a window already in order is
// rendered exactly as before (the golden request covers the bytes).
func TestModelOrderWellFormedUnchanged(t *testing.T) {
	msgs := []Message{
		ctxMsg(KindCEOUtterance, "status?"),
		requestMsg(1, "r"),
		callMsg(1, "a", ToolReadScreen, ""),
		resultMsg(1, "a", ToolReadScreen, "s"),
		callMsg(1, "b", ToolRecall, ""),
		resultMsg(1, "b", ToolRecall, "t"),
		{TS: orderTS, Role: RoleAssistant, Kind: KindReply, Turn: 1, Content: `{"spoken":"ok"}`},
		ctxMsg(KindAssistantSaid, "ok"),
	}
	if got, want := fmt.Sprint(modelOrder(msgs)), "[0 1 2 3 4 5 6 7]"; got != want {
		t.Errorf("modelOrder = %s, want %s", got, want)
	}
	render(t, msgs)
}

// TestVoiceLineFixtureHeals replays the real transcript segment of
// 2026-09-24 (turns 1–8) through both renderers at every point the loop
// assembled a request — before each step of every turn — and requires a
// valid request each time: the transcript recorded before the fix
// assembles without the fix touching it.
func TestVoiceLineFixtureHeals(t *testing.T) {
	msgs, err := ReadTranscript(voiceLineFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	sawMidStep := false
	for i := range msgs {
		if i > 0 && msgs[i].Kind == KindAssistantSaid && msgs[i-1].Kind == KindToolCall && i+1 < len(msgs) && msgs[i+1].Kind == KindToolResult {
			sawMidStep = true
		}
	}
	if !sawMidStep {
		t.Fatal("the fixture no longer holds call → assistant_said → result")
	}
	calls := 0
	for i := range msgs {
		// A model call is assembled after a request or a step's last
		// result (what the loop does before each step).
		if next := i + 1; next < len(msgs) && msgs[next].Kind != KindToolResult && (msgs[i].Kind == KindRequest || msgs[i].Kind == KindToolResult) {
			calls++
			window := msgs[:i+1]
			t.Run(fmt.Sprintf("seq%d", i+1), func(t *testing.T) { render(t, window) })
		}
	}
	chat := render(t, msgs)
	// Turn 6's request: the tool message right after its call, the
	// assistant's "On it." after the tool message.
	for i, m := range chat {
		if len(m.ToolCalls) == 1 && m.ToolCalls[0].ID == "call_00_BkLqg3cwp4kTUSVeNkgi3934" {
			if chat[i+1].ToolCallID != "call_00_BkLqg3cwp4kTUSVeNkgi3934" || !strings.HasPrefix(chat[i+2].Content, `[11:16:43] Assistant said: "On it."`) {
				t.Errorf("turn 6 = %s", chatShape(chat[i:min(i+3, len(chat))]))
			}
		}
	}
	if calls < 8 {
		t.Errorf("replayed %d request points, want at least one per turn", calls)
	}
}

// TestCutPointKeepsStepWhole: the compaction cut never falls on a
// context line that sits inside a step (between a call and its result),
// which would leave the result in the window without its call.
func TestCutPointKeepsStepWhole(t *testing.T) {
	big := strings.Repeat("x", 400)
	window := []entry{
		{msg: requestMsg(1, big)},
		{msg: callMsg(1, "a", ToolReadScreen, "")},
		{msg: ctxMsg(KindAssistantSaid, "On it.")},
		{msg: resultMsg(1, "a", ToolReadScreen, "s")},
		{msg: ctxMsg(KindCEOUtterance, "next")},
		{msg: requestMsg(2, big)},
	}
	for i := range window {
		window[i].msg.Tokens = EstimateTokens(window[i].msg.Content)
	}
	if got := cutPoint(window, 100); got != 4 {
		t.Errorf("cutPoint = %d, want 4 (after the step's result, not at the line inside it)", got)
	}
}
