package supervisor

import (
	"strings"
	"testing"
	"time"

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
	fresh := &state.Result{Spoken: "created hello.txt with the requested line", Outcome: "success", At: now.Add(-5 * time.Minute)}
	stale := &state.Result{Spoken: "wrote the readme", Outcome: "success", At: now.Add(-31 * time.Minute)}
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
// puts its result in the briefing (no model call) and the result drops
// out of the sentence once it is older than 30 minutes, even without a
// state change.
func TestBriefingReflectsResultsAndAges(t *testing.T) {
	h := twoAgents(t)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-after-approval.screen.txt"))
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	calls := h.llm.count("summarize_done")
	want := "Two agents: auth is idle; it last created hello.txt with the requested line, and docs is idle."
	if got := h.call(ToolBriefing, nil).Spoken; got != want {
		t.Errorf("briefing = %q, want %q", got, want)
	}
	h.clock.Advance(31 * time.Minute)
	if got := h.call(ToolBriefing, nil).Spoken; got != "Two agents: auth is idle and docs is idle." {
		t.Errorf("briefing after 31 min = %q", got)
	}
	if h.llm.count("summarize_done") != calls || h.llm.count("spoken_answer") != 0 {
		t.Error("briefing called the model")
	}
}
