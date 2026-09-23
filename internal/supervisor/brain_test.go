package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"

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
	if len(h.srv.RequestsFor("pane.read")) != 1 {
		t.Errorf("pane reads = %d", len(h.srv.RequestsFor("pane.read")))
	}
	calls := m.Calls()
	if !strings.Contains(calls[0].Text(), `CEO said: "why did that one fail?"`) || !strings.Contains(calls[1].Text(), "source=screen>>>") {
		t.Errorf("model saw:\n%s\n---\n%s", calls[0].Text(), calls[1].Text())
	}
	// The read counted as dealing with the agent.
	if row, _ := h.store.Get("w5:p1"); row.LastInteraction == nil {
		t.Error("read did not touch the agent")
	}
	if h.log.count("brain.turn") != 1 || h.log.count("brain.tool_call") != 1 {
		t.Errorf("brain events: turn=%d tool_call=%d", h.log.count("brain.turn"), h.log.count("brain.tool_call"))
	}
}

// A brain that cannot answer leaves the daemon with the roster and
// nothing else: no second model phrases anything, and an instruction is
// never sent on a guess (docs/roles.md "Hot paths that stay code").
func TestBrainFallbackReportsStatusAndSendsNothing(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Err: braintest.ErrBoom})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))
	res := h.call(ToolAsk, map[string]any{"question": "what is it doing?", "agent": "auth"})
	if res.Spoken != "One agent: auth is working on something. "+noBrainTrailer || res.Clarify {
		t.Errorf("fallback = %+v", res)
	}
	if h.log.count("brain.fallback") != 1 {
		t.Errorf("brain.fallback = %d", h.log.count("brain.fallback"))
	}
	m.Add(braintest.Step{Err: braintest.ErrBoom})
	res = h.call(ToolPropose, map[string]any{"instruction": "tell auth to rerun the tests"})
	// The second fallback within supervisor.repeat_window_s hears the
	// same roster again, so it is flagged as unchanged.
	if res.Spoken != "No change since I last checked: One agent: auth is working on something. "+noBrainTrailer {
		t.Errorf("propose fallback = %+v", res)
	}
	if len(h.promptRequests()) != 0 || len(h.sendKeysRequests()) != 0 {
		t.Errorf("the fallback sent something: prompts=%v keys=%v", h.promptRequests(), h.sendKeysRequests())
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
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}},
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
	if got := h.sendKeysRequests(); len(got) != 1 || got[0] != [2]string{"w5:p1", "y"} {
		t.Errorf("keys = %v", got)
	}
	if _, has := h.pending(); has {
		t.Error("pending not cleared")
	}
	if h.log.count("pending.confirmed") != 1 || h.log.count("brain.rail_blocked") != 0 {
		t.Errorf("confirmed=%d rail_blocked=%d", h.log.count("pending.confirmed"), h.log.count("brain.rail_blocked"))
	}
	// Call 0 was the block's announce event turn; the CEO turn is 1–2.
	if sys := m.Calls()[1].System(); !strings.Contains(sys, "WAITING FOR AN ANSWER: would you like to run the following command") || !strings.Contains(sys, `"Yes, proceed" -> keys [y]`) {
		t.Errorf("roster lacks the prompt:\n%s", sys)
	}
	if second := m.Calls()[2].Text(); !strings.Contains(second, "Told auth yes.") {
		t.Errorf("echo missing:\n%s", second)
	}
}

func TestBrainSendKeysBlockedByTheRail(t *testing.T) {
	// A key outside the option set is refused.
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"ctrl-c"}})}},
		braintest.Step{Text: braintest.Reply("I may not send that key.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	res := h.call(ToolPropose, map[string]any{"instruction": "press control c", "agent": "auth"})
	if res.Spoken != "I may not send that key." || len(h.sendKeysRequests()) != 0 {
		t.Errorf("result = %+v sends=%v", res, h.sendKeysRequests())
	}
	if h.log.count("brain.rail_blocked") != 1 {
		t.Errorf("rail_blocked = %d", h.log.count("brain.rail_blocked"))
	}
	// Cancel drops the question, but the prompt stays on the pane's
	// screen unanswered; when it no longer does (the agent moved on),
	// there is nothing left to revive and the host's atomic re-check
	// refuses outright (a host that refuses is covered in internal/brain).
	h.call(ToolCancel, nil)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))
	m.Add(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}},
		braintest.Step{Text: braintest.Reply("Nothing is waiting.", "")},
	)
	res = h.call(ToolPropose, map[string]any{"instruction": "yes", "agent": "auth"})
	if res.Spoken != "Nothing is waiting." || len(h.sendKeysRequests()) != 0 {
		t.Errorf("result = %+v sends=%v", res, h.sendKeysRequests())
	}
	if h.log.count("brain.rail_blocked") != 2 {
		t.Errorf("rail_blocked = %d", h.log.count("brain.rail_blocked"))
	}
	if last := m.Calls()[len(m.Calls())-1].Text(); !strings.Contains(last, "has no pending approval prompt now") {
		t.Errorf("host refusal not seen:\n%s", last)
	}
}

// Once a pending answer_prompt expires (or is otherwise dropped) while
// the prompt is still on the pane's screen, a later send_keys revives it
// from the screen and the rail's normal checks apply to what is actually
// there (docs/issues-2026-09-18.md "an expired approval prompt can never
// be approved").
func TestBrainSendKeysRevivesAnExpiredPrompt(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}},
		braintest.Step{Text: braintest.Reply("Told auth yes.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.clock.Advance(121 * time.Second)
	h.sup.ExpirePending()
	if _, has := h.pending(); has {
		t.Fatal("pending did not expire")
	}
	h.hear("please approve it")
	res := h.call(ToolPropose, map[string]any{"instruction": "please approve it", "agent": "auth"})
	if res.Spoken != "Told auth yes." {
		t.Errorf("result = %+v", res)
	}
	if got := h.sendKeysRequests(); len(got) != 1 || got[0] != [2]string{"w5:p1", "y"} {
		t.Errorf("keys = %v", got)
	}
	if h.log.count("brain.rail_blocked") != 0 {
		t.Errorf("rail_blocked = %d, want 0 (an authorised, revived answer)", h.log.count("brain.rail_blocked"))
	}
	revived := h.log.ofType("pending.revived")
	if len(revived) != 1 {
		t.Fatalf("pending.revived = %d", len(revived))
	}
	if p := payload(revived[0]); p["source"] != "send_keys" {
		t.Errorf("pending.revived source = %v, want send_keys", p["source"])
	}
}

// A key sequence that mixes more than one option's keys is refused even
// though every key is individually valid: "y" is this prompt's yes, "p"
// its "don't ask again" — sending both must never risk confirming
// whatever the pane asks next.
func TestBrainSendKeysRejectsMixedOptionKeys(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y", "p"}})}},
		braintest.Step{Text: braintest.Reply("I may not send that.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	res := h.call(ToolPropose, map[string]any{"instruction": "yes and don't ask again", "agent": "auth"})
	if res.Spoken != "I may not send that." {
		t.Errorf("result = %+v", res)
	}
	if got := h.sendKeysRequests(); len(got) != 0 {
		t.Errorf("keys sent = %v, want none", got)
	}
	if h.log.count("brain.rail_blocked") != 1 {
		t.Errorf("rail_blocked = %d", h.log.count("brain.rail_blocked"))
	}
	if blocked := h.log.ofType("brain.rail_blocked"); len(blocked) == 1 {
		if reason, _ := payload(blocked[0])["reason"].(string); !strings.Contains(reason, "mix more than one option") {
			t.Errorf("reason = %q", reason)
		}
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

// run_cli is the only way to the CLI query tool now: the brain asks,
// the supervisor runs it in the agent's workspace and wraps the output
// as worker data.
func TestBrainRunCLIUsesTheQuerier(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolRunCLI, map[string]any{"agent": "auth", "question": "why did the login test fail?"})}},
		braintest.Step{Text: braintest.Reply("The login test fails on a stale fixture.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	q := &fakeQuerier{}
	h.sup.querier = q
	res := h.call(ToolAsk, map[string]any{"question": "why did the login test fail?", "agent": "auth"})
	if res.Spoken != "The login test fails on a stale fixture." {
		t.Errorf("ask = %+v", res)
	}
	if len(q.calls) != 1 || q.calls[0] != "why did the login test fail?" || q.dirs[0] != "/work/w5:p1" {
		t.Errorf("querier calls=%v dirs=%v", q.calls, q.dirs)
	}
	if out := m.Calls()[1].Text(); !strings.Contains(out, "BEGIN_WORKER_OUTPUT agent=auth source=cli") || !strings.Contains(out, "QUERY:why did the login test fail?") {
		t.Errorf("the CLI output did not reach the brain:\n%s", out)
	}
	// With no CLI tool the brain is told so rather than left waiting.
	h.sup.querier = nil
	m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolRunCLI, map[string]any{"agent": "auth", "question": "why?"})}})
	m.Add(braintest.Step{Text: braintest.Reply("I cannot look into the repository here.", "")})
	if res := h.call(ToolAsk, map[string]any{"question": "why?", "agent": "auth"}); res.Spoken != "I cannot look into the repository here." {
		t.Errorf("without a querier = %+v", res)
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
	// The block runs its announce turn (request, the screen, the reply)
	// before anything is said: the line it decides on is the only one,
	// and the "nothing to add" reply means nothing is pushed at all.
	want := "worker_event worker_event request event_screen reply assistant_said note"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("kinds = %s\nwant %s\n%v", got, want, contents)
	}
	if contents[1] != "auth: working -> blocked" || !strings.Contains(contents[2], `event="blocked"`) || contents[5] != "Auth is asking whether it can create hello.txt." || contents[6] != "keep hello.txt small" {
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
