package supervisor

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// threeAgents is twoAgents plus "vps", an agent the user never deals
// with unless a step says so.
func threeAgents(t *testing.T, opts ...func(*Config)) *harness {
	t.Helper()
	h := newHarness(t, opts...)
	h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "w6", "claude", herdr.StateIdle)
	h.addAgent("w7:p1", "w7", "codex", herdr.StateWorking)
	if err := h.store.SetAlias("w7:p1", "vps"); err != nil {
		t.Fatal(err)
	}
	h.sup.invalidateBriefing()
	return h
}

// pushTexts returns the spoken pushes (silent context excluded).
func (h *harness) pushTexts() []string {
	var out []string
	for _, p := range h.pusher.all() {
		if p.Urgency != voice.UrgencySilent {
			out = append(out, p.Text)
		}
	}
	return out
}

// TestChattinessLevels runs one transition sequence at every level and
// compares the pushes: questions and exits always; informational blocks,
// replacement phrasing and finishes of agents the user dealt with at
// normal; every finish and "started on" at chatty; "idle again" never.
func TestChattinessLevels(t *testing.T) {
	const (
		authQ    = `auth asks to run printf '%s\n' 'hello from codex.' > hello.txt.`
		docsQ    = "docs is asking do you want to create claude.txt."
		replaced = "Dropping the earlier question from auth; now docs is asking do you want to create claude.txt."
		docsDone = "docs finished."
		vpsDone  = "vps finished."
		started  = `vps started on "run the tests".`
		// The trust dialog is a numbered prompt like any other, so it is
		// a question at every level now that code parses it.
		trust = "vps is asking do you trust the contents of this directory."
		exit  = "auth has exited while it was busy."
	)
	want := map[Chattiness][]string{
		Quiet:  {authQ, docsQ, trust, exit},
		Normal: {authQ, replaced, docsDone, vpsDone, trust, exit},
		Chatty: {authQ, replaced, docsDone, vpsDone, started, vpsDone, trust, exit},
	}
	for _, level := range Levels {
		t.Run(string(level), func(t *testing.T) {
			h := threeAgents(t, func(c *Config) { c.Chattiness = string(level) })
			if got := h.sup.Settings().Chattiness; got != level {
				t.Fatalf("Settings().Chattiness = %s, want %s", got, level)
			}
			// 1. auth asks; 2. docs asks (replaces auth's question).
			h.block("w5:p1", "codex-approval-prompt.screen.txt")
			h.block("w6:p1", "claude-permission-prompt.screen.txt")
			// 3. answered docs, docs works and finishes: dealt with.
			h.say("yes")
			h.transition("w6:p1", herdr.StateBlocked, herdr.StateWorking)
			h.srv.SetScreen("w6:p1", fixture(t, "claude-after-approval.screen.txt"))
			h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
			// 4. vps finishes on its own: nobody dealt with it.
			h.srv.SetScreen("w7:p1", fixture(t, "codex-after-approval.screen.txt"))
			h.transition("w7:p1", herdr.StateWorking, herdr.StateDone)
			// 5. an instruction to vps is picked up, done, then idle again.
			h.send("w7:p1", "run the tests")
			h.transition("w7:p1", herdr.StateDone, herdr.StateWorking)
			h.transition("w7:p1", herdr.StateWorking, herdr.StateDone)
			h.transition("w7:p1", herdr.StateDone, herdr.StateIdle)
			// 6. vps blocked without a question; 7. auth exits while blocked.
			h.block("w7:p1", "codex-trust-dialog.screen.txt")
			h.transition("w5:p1", herdr.StateBlocked, herdr.StateUnknown)

			if got := h.pushTexts(); !reflect.DeepEqual(got, want[level]) {
				t.Errorf("pushes at %s:\n got %q\nwant %q", level, got, want[level])
			}
			// last_result is recorded at every level, announced or not.
			if row, _ := h.store.Get("w7:p1"); row.LastResult == nil || row.LastResult.Spoken != doneResult {
				t.Errorf("vps last_result at %s = %+v", level, row.LastResult)
			}
			// The suppressed pushes are visible in the log.
			quiet := h.log.count("supervisor.push_quiet")
			if wantQuiet := len(want[Chatty]) - len(want[level]); quiet != wantQuiet {
				t.Errorf("supervisor.push_quiet = %d, want %d", quiet, wantQuiet)
			}
		})
	}
}

// TestNormalDoneWindow: at normal a finish is announced only when the user
// dealt with the agent within RecentWindow; a spoken briefing counts, the
// silent periodic one does not.
func TestNormalDoneWindow(t *testing.T) {
	h := threeAgents(t)
	h.send("w7:p1", "run the tests")
	h.clock.Advance(31 * time.Minute)
	h.transition("w7:p1", herdr.StateWorking, herdr.StateDone)
	if got := h.pushTexts(); len(got) != 0 {
		t.Fatalf("finish after the window pushed %q", got)
	}
	if h.log.count("supervisor.push_quiet") != 1 {
		t.Error("no supervisor.push_quiet for the stale finish")
	}
	// Silent periodic briefing: not a mention.
	h.transition("w7:p1", herdr.StateDone, herdr.StateWorking)
	h.sup.pushBriefing()
	h.transition("w7:p1", herdr.StateWorking, herdr.StateDone)
	if got := h.pushTexts(); len(got) != 0 {
		t.Fatalf("finish after a silent briefing pushed %q", got)
	}
	// A spoken status answer names vps: its next finish is announced.
	h.transition("w7:p1", herdr.StateDone, herdr.StateWorking)
	h.say("what's going on")
	h.transition("w7:p1", herdr.StateWorking, herdr.StateDone)
	if got := h.pushTexts(); len(got) != 1 || !strings.HasPrefix(got[0], "vps finished") {
		t.Fatalf("finish after a spoken briefing pushed %q", got)
	}
	// Reading an agent's screen counts too, and the window is configurable.
	h2 := threeAgents(t, func(c *Config) { c.RecentWindow = time.Hour })
	h2.srv.SetScreen("w7:p1", "working away")
	h2.sup.touch("w7:p1")
	h2.clock.Advance(45 * time.Minute)
	h2.transition("w7:p1", herdr.StateWorking, herdr.StateDone)
	if got := h2.pushTexts(); len(got) != 1 || !strings.HasPrefix(got[0], "vps finished") {
		t.Fatalf("finish inside a 60 min window pushed %q", got)
	}
}

// TestStartedOnlyForOurInstruction: "started on" needs a send_instruction
// pending for that pane; leaving a prompt is not a start, and a done
// clears the instruction so a later working is not announced again.
func TestStartedOnlyForOurInstruction(t *testing.T) {
	h := threeAgents(t, func(c *Config) { c.Chattiness = string(Chatty) })
	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	if got := h.pushTexts(); len(got) != 0 {
		t.Fatalf("spontaneous working pushed %q", got)
	}
	h.send("w5:p1", "write the readme")
	// auth's approval prompt displaces the in-flight instruction; leaving
	// the prompt after the answer is not a start.
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.say("yes")
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
	for _, p := range h.pushTexts() {
		if strings.Contains(p, "started on") {
			t.Fatalf("leaving a prompt announced a start: %q", p)
		}
	}
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	for _, p := range h.pushTexts() {
		if strings.Contains(p, "started on") {
			t.Fatalf("working after done announced a start: %q", p)
		}
	}
}

// TestSettingsRuntimeChangePersists: PUT-style changes are validated,
// logged, persisted in the session and win over the configured initial
// value after a restart.
func TestSettingsRuntimeChangePersists(t *testing.T) {
	h := twoAgents(t)
	if err := h.sup.SetSettings(Settings{Chattiness: "loud"}); err == nil {
		t.Error("invalid level accepted")
	}
	if err := h.sup.SetSettings(Settings{Chattiness: " CHATTY "}); err != nil {
		t.Fatal(err)
	}
	if h.sup.Settings().Chattiness != Chatty {
		t.Errorf("Settings() = %+v", h.sup.Settings())
	}
	evs := h.log.ofType("supervisor.settings")
	if len(evs) != 1 {
		t.Fatalf("supervisor.settings events = %d", len(evs))
	}
	if p := evs[0].Payload.(map[string]any); p["chattiness"] != "chatty" || p["previous"] != "normal" {
		t.Errorf("payload = %v", p)
	}
	if h.store.Snapshot().Session.Chattiness != "chatty" {
		t.Error("session.chattiness not persisted")
	}
	// The uninvolved vps-style finish is now announced.
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	if got := h.pushTexts(); len(got) != 1 || !strings.HasPrefix(got[0], "auth finished") {
		t.Errorf("after switching to chatty: %q", got)
	}

	store2, err := state.Open(h.store.Path(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sup2, err := New(store2, h.client, nil, h.pusher, h.log, Config{Now: h.clock.Now, Chattiness: "quiet"})
	if err != nil {
		t.Fatal(err)
	}
	if sup2.Settings().Chattiness != Chatty {
		t.Errorf("after restart = %s, want the session's chatty over the configured quiet", sup2.Settings().Chattiness)
	}
	if _, err := New(store2, h.client, nil, h.pusher, h.log, Config{Chattiness: "loud"}); err == nil {
		t.Error("New accepted an invalid configured level")
	}
	// A fresh session takes the configured value.
	store3, err := state.Open("", state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sup3, err := New(store3, h.client, nil, h.pusher, h.log, Config{Chattiness: "quiet"})
	if err != nil {
		t.Fatal(err)
	}
	if sup3.Settings().Chattiness != Quiet {
		t.Errorf("fresh session = %s, want quiet", sup3.Settings().Chattiness)
	}
}
