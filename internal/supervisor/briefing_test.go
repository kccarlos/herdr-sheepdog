package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

func agent(pane, alias string, st herdr.State) state.Agent {
	return state.Agent{PaneID: pane, Alias: alias, State: st, StateSince: t0}
}

// TestComposeBriefingShapes pins the clause for every agent shape and the
// whole-sentence rules: at most two sentences for up to three agents, a
// count plus the interesting ones beyond that, the pending question last.
func TestComposeBriefingShapes(t *testing.T) {
	now := t0.Add(10 * time.Minute)
	fresh := &state.Result{Spoken: "created hello.txt with the requested line", At: now.Add(-5 * time.Minute)}
	stale := &state.Result{Spoken: "wrote the readme", At: now.Add(-31 * time.Minute)}
	long := &state.Result{Spoken: "one two three four five six seven eight nine ten eleven twelve thirteen fourteen.", At: now}
	working := agent("w1:p1", "auth", herdr.StateWorking)
	working.CurrentTask = "run the auth tests and fix whatever fails in the login package today"
	askingQ := agent("w2:p1", "docs", herdr.StateBlocked)
	askingQ.LastBlock = &state.Block{Question: "whether it can create claude.txt", SeenAt: now}
	idleFresh := agent("w3:p1", "vps", herdr.StateIdle)
	idleFresh.LastResult = fresh
	doneStale := agent("w4:p1", "web", herdr.StateDone)
	doneStale.LastResult = stale
	doneLong := agent("w5:p1", "api", herdr.StateDone)
	doneLong.LastResult = long
	// The brain's own line names the agent; the clause says it already.
	brainLine := agent("w7:p1", "auth", herdr.StateIdle)
	brainLine.LastResult = &state.Result{Spoken: "Auth finished the rerun you asked for.", At: now}
	pending := state.PendingAction{Kind: state.PendingAnswerPrompt, PaneID: "w2:p1", Question: "whether it can create claude.txt"}

	cases := []struct {
		name    string
		agents  []state.Agent
		pending state.PendingAction
		want    string
		panes   int
	}{
		{"none", nil, state.PendingAction{}, "No agents are running.", 0},
		{"working with task", []state.Agent{working}, state.PendingAction{}, `One agent: auth is working on "run the auth tests and fix whatever fails in the…".`, 1},
		{"working unknown task", []state.Agent{agent("w1:p1", "auth", herdr.StateWorking)}, state.PendingAction{}, "One agent: auth is working on something.", 1},
		{"asking with pending", []state.Agent{askingQ}, pending, "One agent: docs is asking whether it can create claude.txt. Docs is waiting for your answer.", 1},
		{"asking no pending", []state.Agent{askingQ}, state.PendingAction{}, "One agent: docs is asking whether it can create claude.txt.", 1},
		{"blocked no question", []state.Agent{agent("w2:p1", "docs", herdr.StateBlocked)}, state.PendingAction{}, "One agent: docs is blocked at a prompt.", 1},
		{"idle fresh result", []state.Agent{idleFresh}, state.PendingAction{}, "One agent: vps is idle; it last created hello.txt with the requested line.", 1},
		{"done stale result", []state.Agent{doneStale}, state.PendingAction{}, "One agent: web is idle.", 1},
		{"done long result trimmed", []state.Agent{doneLong}, state.PendingAction{}, "One agent: api is idle; it last one two three four five six seven eight nine ten eleven twelve.", 1},
		{"result naming the agent", []state.Agent{brainLine}, state.PendingAction{}, "One agent: auth is idle; it last finished the rerun you asked for.", 1},
		{"exited", []state.Agent{agent("w1:p1", "auth", herdr.StateUnknown)}, state.PendingAction{}, "One agent: auth has exited.", 1},
		{"three agents blocked first", []state.Agent{working, idleFresh, askingQ}, pending,
			`Three agents: docs is asking whether it can create claude.txt, auth is working on "run the auth tests and fix whatever fails in the…", and vps is idle; it last created hello.txt with the requested line. Docs is waiting for your answer.`, 3},
		{"five agents count plus interesting", []state.Agent{doneStale, working, idleFresh, askingQ, agent("w6:p1", "cli", herdr.StateUnknown)}, pending,
			`Five agents: docs is asking whether it can create claude.txt, auth is working on "run the auth tests and fix whatever fails in the…", and vps is idle; it last created hello.txt with the requested line; one more is idle and one more has exited. Docs is waiting for your answer.`, 3},
		{"six agents same rest", []state.Agent{agent("w6:p1", "b1", herdr.StateWorking), agent("w6:p2", "b2", herdr.StateWorking), agent("w6:p3", "b3", herdr.StateWorking), agent("w7:p1", "a1", herdr.StateWorking), agent("w8:p1", "a2", herdr.StateWorking), agent("w9:p1", "a3", herdr.StateWorking)}, state.PendingAction{},
			`Six agents: a1 is working on something, a2 is working on something, and a3 is working on something; the other three are working.`, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, panes := composeBriefing(tc.agents, tc.pending, now)
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
			if len(panes) != tc.panes {
				t.Errorf("named panes = %v, want %d", panes, tc.panes)
			}
			if n := strings.Count(got, ". ") + 1; len(tc.agents) <= 3 && n > 2 {
				t.Errorf("%d sentences for %d agents: %q", n, len(tc.agents), got)
			}
		})
	}
	// The pending question is repeated in full when the sentence did not
	// already carry it (a question hidden in the tally).
	many := []state.Agent{askingQ}
	for i := 0; i < 4; i++ {
		b := agent("w1"+string(rune('a'+i))+":p1", "b"+string(rune('a'+i)), herdr.StateBlocked)
		b.LastBlock = &state.Block{Question: "whether to overwrite the file"}
		many = append(many, b)
	}
	got, _ := composeBriefing(many, pending, now)
	if !strings.HasSuffix(got, "Docs is waiting for your answer on whether it can create claude.txt.") {
		t.Errorf("hidden question not repeated: %q", got)
	}
}

// TestBriefingReflectsResultsAndAges: through the supervisor, a finish
// puts what was announced about it in the briefing and the result drops
// out of the sentence once it is older than 30 minutes, even without a
// state change.
func TestBriefingReflectsResultsAndAges(t *testing.T) {
	h := twoAgents(t)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-after-approval.screen.txt"))
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	want := "Two agents: auth is idle; it last finished, and docs is idle."
	if got := h.roster(); got != want {
		t.Errorf("briefing = %q, want %q", got, want)
	}
	h.clock.Advance(31 * time.Minute)
	if got := h.roster(); got != "Two agents: auth is idle and docs is idle." {
		t.Errorf("briefing after 31 min = %q", got)
	}
}

// TestBriefingRepeatNoteWithBrain covers supervisor.repeat_window_s
// (docs/history/issues-2026-09-18.md "Smaller items"): an identical roster
// delivered again within the window gets a context note the brain can
// act on ("no change since I last checked") and a
// supervisor.briefing_repeat log with the age; a worker transition, or
// falling outside the window, clears it again.
func TestBriefingRepeatNoteWithBrain(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Text: braintest.Reply("auth is working.", "")},
		braintest.Step{Text: braintest.Reply("auth is working.", "")},
		braintest.Step{Text: braintest.Reply("auth is asking something.", "")},
		braintest.Step{Text: braintest.Reply("auth is working.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	// Call.Text() is the whole growing window, so an old turn's request
	// (with its own note) never leaves it; lastText narrows to the most
	// recent "Request from the assistant" block, the only one this
	// turn's model call could have acted on.
	lastText := func() string {
		calls := m.Calls()
		text := calls[len(calls)-1].Text()
		if i := strings.LastIndex(text, "Request from the assistant (utterance)"); i >= 0 {
			return text[i:]
		}
		return text
	}

	h.say("what's going on")
	if n := h.log.count("supervisor.briefing_repeat"); n != 0 {
		t.Fatalf("first turn logged a repeat: %d", n)
	}
	if strings.Contains(lastText(), "unchanged since you last reported") {
		t.Error("first turn's request claims a repeat")
	}

	h.clock.Advance(10 * time.Second)
	h.say("still going?")
	if n := h.log.count("supervisor.briefing_repeat"); n != 1 {
		t.Fatalf("supervisor.briefing_repeat = %d, want 1", n)
	}
	if ev := h.log.ofType("supervisor.briefing_repeat"); payload(ev[0])["age_s"] != 10 {
		t.Errorf("age_s = %v, want 10", payload(ev[0])["age_s"])
	}
	if !strings.Contains(lastText(), "The roster is unchanged since you last reported it 10 s ago") {
		t.Errorf("second turn's request:\n%s", lastText())
	}

	// A worker transition invalidates the "delivered" mark: the very
	// next question, whatever it is, carries no note.
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.say("what now")
	if n := h.log.count("supervisor.briefing_repeat"); n != 1 {
		t.Errorf("supervisor.briefing_repeat after a transition = %d, want still 1", n)
	}
	if strings.Contains(lastText(), "unchanged since you last reported") {
		t.Errorf("request after a transition:\n%s", lastText())
	}

	// Past the window (default 60 s), a question about the same roster
	// carries no note either.
	h.clock.Advance(61 * time.Second)
	h.say("what now")
	if n := h.log.count("supervisor.briefing_repeat"); n != 1 {
		t.Errorf("supervisor.briefing_repeat past the window = %d, want still 1", n)
	}
	if strings.Contains(lastText(), "unchanged since you last reported") {
		t.Errorf("request past the window:\n%s", lastText())
	}
}

// TestNoBrainRepeatFallback covers the code fallback's own "no change"
// prefix, used when there is no brain to hand the note to: present
// within supervisor.repeat_window_s, and cleared by a transition even
// though the next question is unrelated to it (proving the reset drives
// this, not a coincidence of wording).
func TestNoBrainRepeatFallback(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	h.say("what's going on")
	res := h.say("still?")
	if !strings.HasPrefix(res.Spoken, "No change since I last checked: ") {
		t.Fatalf("within the window = %+v", res)
	}
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	res = h.say("now?")
	if strings.HasPrefix(res.Spoken, "No change since I last checked: ") {
		t.Fatalf("after a transition = %+v", res)
	}
}
