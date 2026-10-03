package supervisor

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
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

	res := h.say("why did that one fail?")
	if res.Spoken != "Auth is asking whether it can create hello.txt. See a file." || !strings.Contains(res.Detail, "a file") || strings.Contains(res.Detail, "/Users") {
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

// A brain that cannot answer leaves the daemon with one honest line and
// nothing else: no second model phrases anything, no roster answers a
// question that may not have been about status, and an instruction is
// never sent on a guess (docs/roles.md "Hot paths that stay code").
func TestBrainFallbackSaysSoAndSendsNothing(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Err: braintest.ErrBoom})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))
	res := h.say("what is auth doing?")
	if res.Spoken != brainFailedLine || res.Clarify || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("fallback = %+v", res)
	}
	if h.log.count("brain.fallback") != 1 {
		t.Errorf("brain.fallback = %d", h.log.count("brain.fallback"))
	}
	m.Add(braintest.Step{Err: braintest.ErrBoom})
	res = h.say("tell auth to rerun the tests")
	// A second failure within supervisor.repeat_window_s is the same
	// line: nothing was delivered, so nothing is "unchanged".
	if res.Spoken != brainFailedLine {
		t.Errorf("instruction fallback = %+v", res)
	}
	if len(h.promptRequests()) != 0 || len(h.sendKeysRequests()) != 0 {
		t.Errorf("the fallback sent something: prompts=%v keys=%v", h.promptRequests(), h.sendKeysRequests())
	}
}

// An instruction the brain sends with its prompt tool is echoed, becomes
// the in-flight send_instruction and can still be stopped (the stop
// procedure's interrupt phase).
func TestBrainProposeSendsPromptAndCancelInterrupts(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "Rerun the flaky test in the auth package."})}},
		braintest.Step{Text: braintest.Reply("Telling auth to rerun the flaky test. Sent.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)
	res := h.say("tell auth to rerun the flaky test")
	// An utterance is not classified, so its reply is idle unless the
	// shepherd must act (the old propose tool always interrupted).
	if res.Spoken != "Telling auth to rerun the flaky test. Sent." || res.Scheduling != voice.UrgencyIdle {
		t.Errorf("result = %+v", res)
	}
	if got := h.promptRequests(); len(got) != 1 || got[0] != [2]string{"w5:p1", withSignOff("Rerun the flaky test in the auth package.")} {
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
	// "Stop" still interrupts the in-flight instruction once herdr
	// reports the agent working on it.
	h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)
	res = h.say("stop")
	if res.Spoken == "" || len(h.srv.RequestsFor("pane.send_keys")) != 1 {
		t.Errorf("stop = %+v sends=%d", res, len(h.srv.RequestsFor("pane.send_keys")))
	}
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "ctrl+c"} {
		t.Errorf("send_keys = %v", keys)
	}
	if _, has := h.pending(); has {
		t.Error("pending survived the stop")
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
	// Not a bare answer (that is the fast path's): the brain answers it.
	res := h.say("tell auth yes go ahead")
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
	if sys := m.Calls()[1].System(); !strings.Contains(sys, "WAITING FOR AN ANSWER (asked ") || !strings.Contains(sys, "ago): would you like to run the following command") || !strings.Contains(sys, `"Yes, proceed" -> keys [y]`) {
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
	res := h.say("press control c on auth")
	if res.Spoken != "I may not send that key." || len(h.sendKeysRequests()) != 0 {
		t.Errorf("result = %+v sends=%v", res, h.sendKeysRequests())
	}
	if h.log.count("brain.rail_blocked") != 1 {
		t.Errorf("rail_blocked = %d", h.log.count("brain.rail_blocked"))
	}
	// The question is dropped when the agent moves on (answered at the
	// keyboard); the prompt is no longer on screen, so there is nothing
	// left to revive and the host's atomic re-check refuses outright (a
	// host that refuses is covered in internal/brain).
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
	m.Add(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}},
		braintest.Step{Text: braintest.Reply("Nothing is waiting.", "")},
	)
	res = h.say("tell auth yes")
	if res.Spoken != "Nothing is waiting." || len(h.sendKeysRequests()) != 0 {
		t.Errorf("result = %+v sends=%v", res, h.sendKeysRequests())
	}
	if h.log.count("brain.rail_blocked") != 2 {
		t.Errorf("rail_blocked = %d", h.log.count("brain.rail_blocked"))
	}
	if last := m.Calls()[len(m.Calls())-1].Text(); !strings.Contains(last, "error: not permitted: auth has no") {
		t.Errorf("host refusal not seen:\n%s", last)
	}
}

// Once a pending answer_prompt is dropped unanswered while the prompt is
// still on the pane's screen (another pane's block or an instruction took
// the one slot), a later send_keys revives it from the screen and the
// rail's normal checks apply to what is actually there
// (docs/history/issues-2026-09-18.md "an expired approval prompt can never
// be approved"). Here it is dropped while the brain's turn runs.
func TestBrainSendKeysRevivesADroppedPrompt(t *testing.T) {
	var h *harness
	m := braintest.NewModel(
		braintest.Step{Fn: func(braintest.Call) (*genai.GenerateContentResponse, error) {
			h.clock.Advance(11 * time.Second)
			h.sup.mu.Lock()
			if p, has := h.store.Pending(); has {
				h.sup.clearPending(p, "replaced")
			}
			h.sup.mu.Unlock()
			return braintest.Response(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}}), nil
		}},
		braintest.Step{Text: braintest.Reply("Told auth yes.", "")},
	)
	h = newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	res := h.say("please approve it")
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
	res := h.say("tell auth yes and don't ask again")
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
	res := h.say("stop auth")
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
	res := h.say("why did the login test fail?")
	if res.Spoken != "The login test fails on a stale fixture." {
		t.Errorf("reply = %+v", res)
	}
	if len(q.calls) != 1 || q.calls[0] != "why did the login test fail?" || q.dirs[0] != "/work/w5:p1" {
		t.Errorf("querier calls=%v dirs=%v", q.calls, q.dirs)
	}
	if out := m.Calls()[1].Text(); !strings.Contains(out, "agent=auth source=cli") || !strings.Contains(out, "QUERY:why did the login test fail?") {
		t.Errorf("the CLI output did not reach the brain:\n%s", out)
	}
	// With no CLI tool the brain is told so rather than left waiting.
	h.sup.querier = nil
	m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolRunCLI, map[string]any{"agent": "auth", "question": "why?"})}})
	m.Add(braintest.Step{Text: braintest.Reply("I cannot look into the repository here.", "")})
	if res := h.say("why?"); res.Spoken != "I cannot look into the repository here." {
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
	var kinds []string
	var contents []string
	for _, msg := range h.sup.Brain().Transcript().Messages() {
		kinds = append(kinds, string(msg.Kind))
		contents = append(contents, msg.Content)
	}
	// The block runs its announce turn (request, the screen, the reply)
	// before anything is said: the line it decides on is the only one,
	// and the "nothing to add" reply means nothing is pushed at all.
	want := "worker_event worker_event request event_screen reply assistant_said"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("kinds = %s\nwant %s\n%v", got, want, contents)
	}
	if contents[1] != "auth: working -> blocked" || !strings.Contains(contents[2], `event="blocked"`) || contents[5] != "Auth is asking whether it can create hello.txt." {
		t.Errorf("contents = %v", contents)
	}
	if d := h.log.ofType("brain.push_decision"); len(d) != 1 || payload(d[0])["urgency_requested"] != "skip" || payload(d[0])["urgency_applied"] != "skip" || payload(d[0])["reason"] != "brain" {
		t.Errorf("push_decision = %v", d)
	}
}

// Without a brain the transcript hooks are no-ops and a request gets the
// roster sentence with no detail.
func TestNoBrainIsInert(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "hi", Final: true})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	if h.sup.Brain() != nil {
		t.Error("brain attached")
	}
	if res := h.say("what is auth doing?"); res.Spoken != "One agent: auth is working on something. "+noBrainTrailer || res.Detail != "" {
		t.Errorf("a request without a brain = %+v", res)
	}
}

func TestToolResultFromReply(t *testing.T) {
	// Short lines are not cut (one high runaway cap, bench #13).
	res := toolResultFrom(brain.Reply{Spoken: "One. Two. Three.", Detail: "See /a/b/c.go and abc1234def5678", Options: []string{"keep /x/y/z", ""}, NeedsUser: true}, prose.EarRunaway)
	if res.Spoken != "One. Two. Three." || res.Detail != "See a file and" || len(res.Options) != 1 || res.Options[0] != "keep a file" || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("result = %+v", res)
	}
	if res := toolResultFrom(brain.Reply{Spoken: "fine"}, prose.EarRunaway); res.Spoken != "fine." || res.Scheduling != voice.UrgencyIdle {
		t.Errorf("result = %+v", res)
	}
	if res := toolResultFrom(brain.Reply{Spoken: "Sent.", Clarify: true}, prose.EarRunaway); !res.Clarify || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("result = %+v", res)
	}
}

// The ear contract (docs/iteration-2.md WP1): the 2026-09-24 line the
// shepherd heard (101 words, dashes, two PR numbers), said twice over so it
// is a runaway, is cut at the runaway cap, the ask is heard last, and the
// overflow lands in detail after the plain explanation.
func TestToolResultFromEarContract(t *testing.T) {
	const heard = "Both landed: the Context tab is merged as daemon #49 — a new /debug tab beside Supervisor showing the brain's exact request and the voice model's context side by side, linked, with a scrubber and diffs against the previous step — and the first app refactor went in as #12, dead code out and duplicated state merged, behaviour unchanged with 245 tests green. It's asking whether to carry on with the next two in parallel — the daemon's dead-code and docs cleanup then removing the legacy tool surface, and the app's testable SessionCore — and I'd say yes."
	const plain = "A tab is a page of the debug screen."
	res := toolResultFrom(brain.Reply{Spoken: heard + " " + heard, Ask: "Shall it carry on with both?", Detail: plain, NeedsUser: true}, prose.EarRunaway)
	head, ok := strings.CutSuffix(res.Spoken, " Shall it carry on with both?")
	if !ok {
		t.Fatalf("the ask is not the last sentence: %q", res.Spoken)
	}
	if n := len(strings.Fields(head)); n == 0 || n > prose.EarWords {
		t.Errorf("headline is %d words: %q", n, head)
	}
	for _, bad := range []string{"—", "#49", "#12", ";"} {
		if strings.Contains(res.Spoken, bad) {
			t.Errorf("spoken contains %q: %q", bad, res.Spoken)
		}
	}
	if !strings.HasPrefix(res.Detail, plain+" ") || !strings.Contains(res.Detail, "SessionCore") {
		t.Errorf("overflow did not follow the plain detail: %q", res.Detail)
	}
	// An old-shape reply (no ask) keeps its own question.
	if res := toolResultFrom(brain.Reply{Spoken: "Auth finished. Want the details?"}, prose.EarRunaway); res.Spoken != "Auth finished. Want the details?" || res.Detail != "" {
		t.Errorf("old shape = %+v", res)
	}
	// With an ask, a question in the headline moves to detail: one
	// question per reply, and it is the ask.
	if res := toolResultFrom(brain.Reply{Spoken: "Auth finished. Want the details?", Ask: "Run the tests?"}, prose.EarRunaway); res.Spoken != "Auth finished. Run the tests?" || res.Detail != "Want the details?" {
		t.Errorf("two questions = %+v", res)
	}
}
