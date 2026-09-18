package supervisor

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// withBrain attaches a brain over the scripted model to the harness.
func withBrain(t *testing.T, m *braintest.Model, log *recLog, dir string) func(*Config) {
	t.Helper()
	b, err := brain.New(brain.Config{DataDir: dir, RunID: "t"}, m, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return func(c *Config) { c.Brain = b }
}

// newBrainHarness builds a harness with the brain attached (the log and
// dir are shared so withBrain can be a Config option).
func newBrainHarness(t *testing.T, m *braintest.Model, opts ...func(*Config)) *harness {
	t.Helper()
	log := &recLog{}
	dir := t.TempDir()
	// Event turns (the announce turn a block or finish triggers) answer
	// "nothing to add" unless a test scripts them (or the model is the
	// reflex, which answers them itself).
	if m.EventDefault == nil && m.Default == nil {
		m.EventDefault = braintest.NothingToAdd()
	}
	h := newHarness(t, append([]func(*Config){withBrain(t, m, log, dir), func(c *Config) { c.DataDir = dir }}, opts...)...)
	h.log = log // the brain logs there; the supervisor's own logger is the harness one
	h.sup.log = log
	h.client.Log = log
	return h
}

func TestBrainAnswersAskWithTools(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolReadScreen, map[string]any{"agent": "auth"})}},
		braintest.Step{Text: braintest.Reply("Auth is asking whether it can create hello.txt; see /Users/kc/x/y.go.", "It ran `go test` in /Users/kc/repo/auth and stopped at the approval prompt.")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-approval-prompt.screen.txt"))
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "why did that one fail?", Final: true})

	res := h.call(ToolAsk, map[string]any{"question": "why did it fail?"})
	if res.Spoken != "Auth is asking whether it can create hello.txt; see a file." || !strings.Contains(res.Detail, "a file") || strings.Contains(res.Detail, "/Users") {
		t.Errorf("result = %+v", res)
	}
	if res.Scheduling != voice.UrgencyIdle {
		t.Errorf("scheduling = %v", res.Scheduling)
	}
	if h.llm.count("spoken_answer") != 0 {
		t.Error("template path ran although the brain answered")
	}
	if len(h.srv.RequestsFor("pane.read")) != 1 {
		t.Errorf("pane reads = %d", len(h.srv.RequestsFor("pane.read")))
	}
	calls := m.Calls()
	if !strings.Contains(calls[0].Text(), `CEO said: "why did that one fail?"`) || !strings.Contains(calls[1].Text(), "source=screen>>>") {
		t.Errorf("model saw:\n%s\n---\n%s", calls[0].Text(), calls[1].Text())
	}
	// The read made auth the subject for the fallback path and counted as
	// dealing with it.
	if h.sup.lastSubject != "w5:p1" {
		t.Errorf("lastSubject = %q", h.sup.lastSubject)
	}
	if row, _ := h.store.Get("w5:p1"); row.LastInteraction == nil {
		t.Error("read did not touch the agent")
	}
	if h.log.count("brain.turn") != 1 || h.log.count("brain.tool_call") != 1 {
		t.Errorf("brain events: turn=%d tool_call=%d", h.log.count("brain.turn"), h.log.count("brain.tool_call"))
	}
}

func TestBrainFallsBackToTemplatePath(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Err: braintest.ErrBoom})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))
	res := h.call(ToolAsk, map[string]any{"question": "what is it doing?", "agent": "auth"})
	if !strings.Contains(res.Spoken, "sleep command") {
		t.Errorf("template path did not answer: %+v", res)
	}
	if h.llm.count("spoken_answer") != 1 || h.log.count("brain.fallback") != 1 {
		t.Errorf("spoken_answer=%d brain.fallback=%d", h.llm.count("spoken_answer"), h.log.count("brain.fallback"))
	}
	// propose falls back to resolve_instruction the same way.
	m.Add(braintest.Step{Err: braintest.ErrBoom})
	res = h.call(ToolPropose, map[string]any{"instruction": "tell auth to rerun the tests"})
	if res.Spoken != "Telling auth to rerun the tests. Sent." || h.llm.count("resolve_instruction") != 1 {
		t.Errorf("propose fallback = %+v (resolve calls %d)", res, h.llm.count("resolve_instruction"))
	}
}

func TestBrainProposeSendsPromptAndCancelInterrupts(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "Rerun the flaky test in the auth package."})}},
		braintest.Step{Text: braintest.Reply("Telling auth to rerun the flaky test. Sent.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)
	res := h.call(ToolPropose, map[string]any{"instruction": "tell auth to rerun the flaky test", "agent": "auth"})
	if res.Spoken != "Telling auth to rerun the flaky test. Sent." || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("result = %+v", res)
	}
	if got := h.promptRequests(); len(got) != 1 || got[0] != [2]string{"w5:p1", "Rerun the flaky test in the auth package."} {
		t.Errorf("prompts = %v", got)
	}
	p, has := h.pending()
	if !has || p.Kind != state.PendingSendInstruction || p.PaneID != "w5:p1" || p.SpokenSummary != "Telling auth to rerun the flaky test in the auth package." {
		t.Errorf("pending = %+v %v", p, has)
	}
	if row, _ := h.store.Get("w5:p1"); row.CurrentTask != "Rerun the flaky test in the auth package." {
		t.Errorf("current task = %q", row.CurrentTask)
	}
	if second := m.Calls()[1].Text(); !strings.Contains(second, "Telling auth to rerun the flaky test in the auth package. Sent.") {
		t.Errorf("model did not see the echo:\n%s", second)
	}
	// cancel still interrupts the in-flight instruction.
	res = h.call(ToolCancel, nil)
	if res.Spoken != "Cancelled." || len(h.srv.RequestsFor("pane.send_keys")) != 1 {
		t.Errorf("cancel = %+v sends=%d", res, len(h.srv.RequestsFor("pane.send_keys")))
	}
	if _, has := h.pending(); has {
		t.Error("pending survived cancel")
	}
}

func TestBrainSendKeysPassesTheRail(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"enter"}})}},
		braintest.Step{Text: braintest.Reply("Told auth yes.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if _, has := h.pending(); !has {
		t.Fatal("no pending prompt")
	}
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "tell auth yes go ahead", Final: true})
	res := h.call(ToolPropose, map[string]any{"instruction": "yes go ahead", "agent": "auth"})
	if res.Spoken != "Told auth yes." {
		t.Errorf("result = %+v", res)
	}
	if got := h.sendKeysRequests(); len(got) != 1 || got[0] != [2]string{"w5:p1", "enter"} {
		t.Errorf("keys = %v", got)
	}
	if _, has := h.pending(); has {
		t.Error("pending not cleared")
	}
	if h.log.count("pending.confirmed") != 1 || h.log.count("brain.rail_blocked") != 0 {
		t.Errorf("confirmed=%d rail_blocked=%d", h.log.count("pending.confirmed"), h.log.count("brain.rail_blocked"))
	}
	// Call 0 was the block's announce event turn; the CEO turn is 1–2.
	if sys := m.Calls()[1].System(); !strings.Contains(sys, "WAITING FOR AN ANSWER: whether it can create hello.txt") || !strings.Contains(sys, `"Yes, proceed" -> keys [enter]`) {
		t.Errorf("roster lacks the prompt:\n%s", sys)
	}
	if second := m.Calls()[2].Text(); !strings.Contains(second, "Told auth yes.") {
		t.Errorf("echo missing:\n%s", second)
	}
}

func TestBrainSendKeysBlockedByTheRail(t *testing.T) {
	// A key outside the option set is refused; once cancel dropped the
	// question there is nothing to answer (the host's atomic re-check is
	// covered in internal/brain with a host that refuses).
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"ctrl-c"}})}},
		braintest.Step{Text: braintest.Reply("I may not send that key.", "")},
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"enter"}})}},
		braintest.Step{Text: braintest.Reply("Nothing is waiting.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	res := h.call(ToolPropose, map[string]any{"instruction": "press control c", "agent": "auth"})
	if res.Spoken != "I may not send that key." || len(h.sendKeysRequests()) != 0 {
		t.Errorf("result = %+v sends=%v", res, h.sendKeysRequests())
	}
	h.call(ToolCancel, nil) // drops the question
	res = h.call(ToolPropose, map[string]any{"instruction": "yes", "agent": "auth"})
	if res.Spoken != "Nothing is waiting." || len(h.sendKeysRequests()) != 0 {
		t.Errorf("result = %+v sends=%v", res, h.sendKeysRequests())
	}
	if h.log.count("brain.rail_blocked") != 2 {
		t.Errorf("rail_blocked = %d", h.log.count("brain.rail_blocked"))
	}
	if fourth := m.Calls()[4].Text(); !strings.Contains(fourth, "has no recorded approval prompt") {
		t.Errorf("host refusal not seen:\n%s", fourth)
	}
}

func TestBrainInterruptSuppressesFinished(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolInterrupt, map[string]any{"agent": "auth"})}},
		braintest.Step{Text: braintest.Reply("Interrupted auth.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	res := h.call(ToolPropose, map[string]any{"instruction": "stop auth", "agent": "auth"})
	if res.Spoken != "Interrupted auth." || len(h.sendKeysRequests()) != 1 {
		t.Errorf("result = %+v sends=%v", res, h.sendKeysRequests())
	}
	h.srv.SetScreen("w5:p1", fixture(t, "codex-after-ctrl-c.screen.txt"))
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	if h.log.count("supervisor.finished_suppressed") != 1 {
		t.Error("the interrupted turn's done was announced")
	}
}

func TestBrainIngestsSupervisorEvents(t *testing.T) {
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt") // a push, a transition
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "Auth is asking ", Final: false})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "whether it can create hello.txt.", Final: true})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	h.call(ToolNote, map[string]any{"text": "keep hello.txt small"})
	var kinds []string
	var contents []string
	for _, msg := range h.sup.Brain().Transcript().Messages() {
		kinds = append(kinds, string(msg.Kind))
		contents = append(contents, msg.Content)
	}
	// The block also ran an announce event turn (request, the screen,
	// the "nothing to add" reply).
	want := "worker_event worker_event push request event_screen reply assistant_said note"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("kinds = %s\nwant %s\n%v", got, want, contents)
	}
	if contents[1] != "auth: working -> blocked" || !strings.Contains(contents[2], "(idle) auth is asking") || contents[6] != "Auth is asking whether it can create hello.txt." || contents[7] != "keep hello.txt small" {
		t.Errorf("contents = %v", contents)
	}
	if d := h.log.ofType("brain.push_decision"); len(d) != 1 || payload(d[0])["urgency_requested"] != "skip" || payload(d[0])["urgency_applied"] != "skip" || payload(d[0])["reason"] != "brain" {
		t.Errorf("push_decision = %v", d)
	}
}

// Without a brain the transcript hooks are no-ops and the tool results
// carry no detail.
func TestNoBrainIsInert(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "hi", Final: true})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	if h.sup.Brain() != nil {
		t.Error("brain attached")
	}
	if res, ok := h.sup.brainTurn(context.Background(), brain.KindAsk, "q", ""); ok || res.Spoken != "" {
		t.Errorf("brainTurn without a brain = %+v %v", res, ok)
	}
}

func TestToolResultFromReply(t *testing.T) {
	res := toolResultFrom(brain.KindAsk, brain.Reply{Spoken: "One. Two. Three.", Detail: "See /a/b/c.go and abc1234def5678", Options: []string{"keep /x/y/z", ""}, NeedsUser: true})
	if res.Spoken != "One. Two." || res.Detail != "See a file and" || len(res.Options) != 1 || res.Options[0] != "keep a file" || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("result = %+v", res)
	}
	if res := toolResultFrom(brain.KindAsk, brain.Reply{Spoken: "fine"}); res.Spoken != "fine." || res.Scheduling != voice.UrgencyIdle {
		t.Errorf("result = %+v", res)
	}
	if res := toolResultFrom(brain.KindPropose, brain.Reply{Spoken: "Sent.", Clarify: true}); !res.Clarify || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("result = %+v", res)
	}
}
