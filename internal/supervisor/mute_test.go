package supervisor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/attention"
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// muteWorld puts auth's pane in a real folder, so the space has a path, and
// returns the mute store (empty) with that path.
type muteWorld struct {
	*harness
	mutes *attention.Mutes
	path  string
	fn    *fakeNotifier
}

// newMuteWorld builds a harness (with the orchestrator model m when given,
// else without one) whose auth agent works in the space "auth" (workspace id
// auth) in a temp folder. Nothing is muted yet.
func newMuteWorld(t *testing.T, m *braintest.Model, level Chattiness) *muteWorld {
	t.Helper()
	// The mute store reads the harness clock, so a test can let a mute end.
	var h *harness
	mutes, err := attention.OpenMutes("", attention.MutesOptions{Now: func() time.Time { return h.clock.Now() }})
	if err != nil {
		t.Fatal(err)
	}
	fn := &fakeNotifier{}
	opts := []func(*Config){withNotifier(fn), func(c *Config) { c.Chattiness = string(level); c.Mutes = mutes }}
	if m != nil {
		h = newBrainHarness(t, m, opts...)
	} else {
		h = newHarness(t, opts...)
	}
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.srv.Handle("server.agent_manifests", func(json.RawMessage) (any, error) {
		return map[string]any{"type": "agent_manifest_status", "manifests": []map[string]any{}}, nil
	})
	dir := t.TempDir()
	h.srv.SetPane(herdr.PaneInfo{PaneID: "w5:p1", WorkspaceID: "auth", Cwd: dir})
	return &muteWorld{harness: h, mutes: mutes, path: herdr.RealDir(dir), fn: fn}
}

func (w *muteWorld) mute(t *testing.T) {
	t.Helper()
	if _, err := w.mutes.Set(w.path, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
}

func TestMutedPromptToolKeepsQuestionHeld(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.mute(t)
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	prompt, ok := herdr.ParsePrompt(fixture(t, "codex-approval-prompt.screen.txt"))
	if !ok {
		t.Fatal("prompt did not parse")
	}
	row, _ := w.store.Get("w5:p1")
	w.sup.pendingFromPrompt(row, prompt)
	if p, ok := w.store.Pending(); ok {
		t.Fatalf("muted prompt became pending: %+v", p)
	}
	qs := w.store.Questions()
	if len(qs) != 1 || !w.sup.heldQuestion(qs[0].ID) {
		t.Fatalf("question is not held: %+v", qs)
	}
}

func TestMutedQuestionExcludedFromDecisions(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.mute(t)
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	if got := w.sup.openDecisions(); len(got) != 0 {
		t.Fatalf("muted decision announced: %v", got)
	}
}

func TestWindowAnswerFindsHeldQuestion(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.mute(t)
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	qs := w.store.Questions()
	if len(qs) != 1 {
		t.Fatal("missing question")
	}
	q := qs[0]
	id, option, ok := w.sup.windowAnswer("w5:p1", q.Options[0].Keys)
	if !ok || id != q.ID || option != 1 {
		t.Fatalf("window answer = %q, %d, %v", id, option, ok)
	}
}

func TestMutedRepromptIsRecognized(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.mute(t)
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	prompt, ok := herdr.ParsePrompt(fixture(t, "codex-approval-prompt.screen.txt"))
	if !ok {
		t.Fatal("prompt did not parse")
	}
	row, _ := w.store.Get("w5:p1")
	if !w.sup.raiseReprompt(context.Background(), row, prompt, "test", 0) {
		t.Fatal("held reprompt was not recognized")
	}
}

func TestReconciledMutedQuestionRemainsHeld(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.mute(t)
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	q := w.store.Questions()[0]
	w.srv.SetScreen(q.PaneID, fixture(t, "claude-permission-prompt.screen.txt"))
	row, _ := w.store.Get(q.PaneID)
	w.sup.checkQuestion(context.Background(), row, q)
	qs := w.store.Questions()
	if len(qs) != 1 || qs[0].ID == q.ID || !w.sup.heldQuestion(qs[0].ID) {
		t.Fatalf("replacement not held: %+v", qs)
	}
	if w.log.count("pending.cancelled") != 1 || w.log.count("pending.created") != 2 {
		t.Fatal("replacement events missing")
	}
}

func TestMuteLookupRefreshesRecentIndexOnNewPane(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.mute(t)
	w.sup.WarmFolders(context.Background())
	w.addAgent("w6:p1", "new", "codex", herdr.StateWorking)
	w.srv.SetPane(herdr.PaneInfo{PaneID: "w6:p1", WorkspaceID: "new", Cwd: w.path})
	if !w.sup.paneMuted(context.Background(), "w6:p1") {
		t.Fatal("new pane in muted folder was not recognized")
	}
}

func TestHeldQuestionSurvivesRestart(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.mute(t)
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	store, err := state.Open(w.store.Path(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sup, err := New(store, w.client, nil, w.pusher, w.log, Config{Now: w.clock.Now, Mutes: w.mutes})
	if err != nil {
		t.Fatal(err)
	}
	qs := store.Questions()
	if len(qs) != 1 || !sup.heldQuestion(qs[0].ID) {
		t.Fatalf("held question lost on restart: %+v", qs)
	}
}

// What the mute silences, channel by channel, for a block and for a finish,
// with the two exceptions (a kept promise, the finish of the user's own
// instruction), with and without an orchestrator. The question card and the
// worker event are never silenced.
func TestSpaceMuteDecisions(t *testing.T) {
	const approvalScreen = "codex-approval-prompt.screen.txt"
	tests := []struct {
		name  string
		event string // block | finish
		// setup is run before the event: the exception, if any.
		exception string // "" | promise | owed
		muted     bool
		// Chatty hears a plain finish; Quiet hears questions only.
		level     Chattiness
		wantVoice int // spoken pushes (not silent context)
		wantNote  int // phone notifications
		wantQuiet bool
	}{
		{name: "block unmuted", event: "block", level: Quiet, wantVoice: 1, wantNote: 1},
		{name: "block muted", event: "block", level: Quiet, muted: true, wantVoice: 0, wantNote: 0, wantQuiet: true},
		{name: "block muted but a promise is kept", event: "block", exception: "promise", level: Quiet, muted: true, wantVoice: 1, wantNote: 1},
		{name: "finish unmuted", event: "finish", level: Chatty, wantVoice: 1, wantNote: 0},
		{name: "finish muted", event: "finish", level: Chatty, muted: true, wantVoice: 0, wantNote: 0, wantQuiet: true},
		{name: "finish muted but a promise is kept", event: "finish", exception: "promise", level: Chatty, muted: true, wantVoice: 1, wantNote: 1},
		{name: "finish unmuted promise", event: "finish", exception: "promise", level: Chatty, wantVoice: 1, wantNote: 1},
		{name: "finish muted but the user's own instruction ended", event: "finish", exception: "owed", level: Quiet, muted: true, wantVoice: 1, wantNote: 0},
		{name: "finish unmuted own instruction", event: "finish", exception: "owed", level: Quiet, wantVoice: 1, wantNote: 0},
	}
	for _, withBrain := range []bool{false, true} {
		for _, tc := range tests {
			name := tc.name + " (no orchestrator)"
			var model *braintest.Model
			if withBrain {
				name = tc.name + " (orchestrator)"
				model = braintest.NewModel()
				model.EventDefault = announceReply("auth has news.", "idle", "", "", false)
			}
			t.Run(name, func(t *testing.T) {
				w := newMuteWorld(t, model, tc.level)
				if tc.muted {
					w.mute(t)
				}
				switch tc.exception {
				case "promise":
					if _, err := w.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: map[string]string{"block": "blocked", "finish": "done"}[tc.event]}); err != nil {
						t.Fatal(err)
					}
				case "owed":
					w.send("w5:p1", "run the tests")
				}
				turnsBefore := 0
				if model != nil {
					turnsBefore = len(model.Calls())
				}
				switch tc.event {
				case "block":
					w.block("w5:p1", approvalScreen)
				case "finish":
					doneAuth(w.harness)
				}

				if got := len(w.pushTexts()); got != tc.wantVoice {
					t.Errorf("spoken pushes = %v, want %d", w.pushTexts(), tc.wantVoice)
				}
				if got := len(w.fn.all()); got != tc.wantNote {
					t.Errorf("phone notifications = %+v, want %d", w.fn.all(), tc.wantNote)
				}
				quiet := false
				for _, e := range w.log.ofType("supervisor.push_quiet") {
					if payload(e)["reason"] == "space_muted" {
						quiet = true
					}
				}
				if quiet != tc.wantQuiet {
					t.Errorf("push_quiet reason space_muted logged = %v, want %v", quiet, tc.wantQuiet)
				}
				if tc.event == "block" {
					if qs := w.store.Questions(); len(qs) != 1 {
						t.Errorf("the question card is gone: %+v", qs)
					}
				}
				if model != nil && tc.wantQuiet {
					if n := len(model.Calls()) - turnsBefore; n != 0 {
						t.Errorf("a muted event ran %d orchestrator turn(s), want none", n)
					}
				}
				if model != nil && !tc.muted {
					if n := len(model.Calls()) - turnsBefore; n == 0 {
						t.Error("an unmuted event ran no orchestrator turn")
					}
				}
			})
		}
	}
}

// A mute counts only while it lasts, and only for its own folder.
func TestSpaceMuteOnlyAppliesToItsFolderWhileItLasts(t *testing.T) {
	w := newMuteWorld(t, nil, Chatty)
	until := w.clock.Now().Add(time.Hour)
	if _, err := w.mutes.Set(filepath.Join(w.path, "elsewhere"), nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	doneAuth(w.harness)
	if got := w.pushTexts(); len(got) != 1 {
		t.Fatalf("pushes with another folder muted = %v, want the finish", got)
	}

	if _, err := w.mutes.Set(w.path, &until, ViaPhone); err != nil {
		t.Fatal(err)
	}
	w.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	doneAuth(w.harness)
	if got := w.pushTexts(); len(got) != 1 {
		t.Fatalf("pushes while the space is muted = %v, want still only the first finish", got)
	}

	w.clock.Advance(2 * time.Hour) // the mute ended
	w.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	doneAuth(w.harness)
	if got := w.pushTexts(); len(got) != 2 {
		t.Errorf("pushes after the mute ended = %v, want the finish spoken again", got)
	}
}

// With nothing muted the decisions are what they were: the mute check
// answers before it looks at herdr.
func TestNoMutesNeverAsksHerdr(t *testing.T) {
	w := newMuteWorld(t, nil, Chatty)
	before := len(w.srv.RequestsFor("session.snapshot")) + len(w.srv.RequestsFor("workspace.list")) + len(w.srv.RequestsFor("pane.list"))
	if w.sup.paneMuted(context.Background(), "w5:p1") {
		t.Error("muted with nothing muted")
	}
	doneAuth(w.harness)
	after := len(w.srv.RequestsFor("session.snapshot")) + len(w.srv.RequestsFor("workspace.list")) + len(w.srv.RequestsFor("pane.list"))
	if after != before {
		t.Errorf("the mute check read herdr %d time(s) with nothing muted", after-before)
	}
}

// An agent whose space cannot be resolved is never muted.
func TestUnresolvableSpaceIsNeverMuted(t *testing.T) {
	w := newMuteWorld(t, nil, Chatty)
	w.mute(t)
	w.addAgent("w9:p1", "ghost", "codex", herdr.StateWorking) // no pane with a folder
	w.srv.SetPane(herdr.PaneInfo{PaneID: "w9:p1", WorkspaceID: "ghost", Cwd: "/nonexistent/ghost"})
	if w.sup.paneMuted(context.Background(), "w9:p1") {
		t.Error("an agent with no folder was muted")
	}
	if w.sup.paneMuted(context.Background(), "no-such-pane") {
		t.Error("an unknown pane was muted")
	}
}

// The folder a mute is stored under is the path GET /api/spaces reports.
func TestSpaceMuteKeyIsTheSpacesPath(t *testing.T) {
	w := newMuteWorld(t, nil, Chatty)
	v, err := w.sup.Spaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var space Space
	for _, sp := range v.Spaces {
		if sp.ID == "auth" {
			space = sp
		}
	}
	if space.ID == "" || space.Path != w.path {
		t.Fatalf("space = %+v, want path %q", space, w.path)
	}
	idx, err := w.sup.readFolders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.ws["auth"]; got != space.Path {
		t.Errorf("folder index = %q, GET /api/spaces path = %q: the mute key must be the same", got, space.Path)
	}

	res, err := w.sup.SetSpaceMute(context.Background(), "auth", true, nil, ViaPhone)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != space.Path || !w.mutes.Muted(space.Path) {
		t.Errorf("SetSpaceMute = %+v, muted(%q) = %v", res, space.Path, w.mutes.Muted(space.Path))
	}
	v, _ = w.sup.Spaces(context.Background())
	if !v.Spaces[0].Muted || v.Spaces[0].Until != nil {
		t.Errorf("GET /api/spaces after the mute: %+v", v.Spaces[0])
	}
	if ev := w.log.ofType("space.muted"); len(ev) != 1 || payload(ev[0])["muted"] != true || payload(ev[0])["via"] != ViaPhone || payload(ev[0])["path"] != space.Path {
		t.Errorf("space.muted = %+v", ev)
	}
	if _, err := w.sup.SetSpaceMute(context.Background(), "auth", false, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	if !w.mutes.Empty() {
		t.Error("unmute left the folder muted")
	}
}

func TestSetSpaceMuteErrors(t *testing.T) {
	w := newMuteWorld(t, nil, Chatty)
	ctx := context.Background()
	if _, err := w.sup.SetSpaceMute(ctx, "w404", true, nil, ViaPhone); err == nil || !strings.Contains(err.Error(), ErrNoSuchSpace.Error()) {
		t.Errorf("unknown space: %v, want ErrNoSuchSpace", err)
	}
	past := w.clock.Now().Add(-time.Hour)
	if _, err := w.sup.SetSpaceMute(ctx, "auth", true, &past, ViaPhone); err == nil {
		t.Error("a mute that ended already was accepted")
	}
	if !w.mutes.Empty() {
		t.Error("a refused mute was stored")
	}
	none := newHarness(t)
	if _, err := none.sup.SetSpaceMute(ctx, "auth", true, nil, ViaPhone); err == nil {
		t.Error("a daemon without a mute store accepted a mute")
	}
}

// When a mute ends the supervisor logs space.muted (the timer itself is
// the attention package's; here the clock is the harness's, so the test
// lets the mute end and asks for the expiry pass the timer would make).
func TestSpaceMuteExpiryIsLogged(t *testing.T) {
	w := newMuteWorld(t, nil, Chatty)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.sup.RunAttention(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	until := w.clock.Now().Add(time.Hour)
	if _, err := w.mutes.Set(w.path, &until, ViaPhone); err != nil {
		t.Fatal(err)
	}
	w.clock.Advance(2 * time.Hour)
	w.mutes.Expire()
	evs := w.log.ofType("space.muted")
	if len(evs) != 1 {
		t.Fatalf("space.muted events = %+v, want one", evs)
	}
	if p := payload(evs[0]); p["muted"] != false || p["via"] != ViaExpired || p["path"] != w.path {
		t.Errorf("space.muted = %+v", p)
	}
	// The mute is gone: the finish is heard again.
	doneAuth(w.harness)
	if got := w.pushTexts(); len(got) != 1 {
		t.Errorf("pushes after the mute ended = %v", got)
	}
}

// A promise kept in a muted space still reaches the phone as a push.
func TestMutedKeptPromiseStillPushesToThePhone(t *testing.T) {
	w := newMuteWorld(t, nil, Chatty)
	w.mute(t)
	if _, err := w.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
		t.Fatal(err)
	}
	doneAuth(w.harness)
	got := w.fn.all()
	if len(got) != 1 || got[0].Kind != push.KindDone || !strings.HasPrefix(got[0].Text, "As you asked:") {
		t.Errorf("notifications = %+v", got)
	}
}
