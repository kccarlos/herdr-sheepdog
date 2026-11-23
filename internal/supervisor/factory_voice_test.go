package supervisor

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

type fakeFactoryProject struct {
	id         string
	folder     string
	goal       string
	running    bool
	activeRuns []string
	tasks      []string
}

type fakeFactory struct {
	mu       sync.Mutex
	projects map[string]*fakeFactoryProject
	asks     []flow.Ask
	proofs   map[string]factory.AnswerProof
	changes  chan factory.Change
	seq      int64
	proofSeq int
	stopped  []string
	started  [][2]string
}

func newFakeFactory() *fakeFactory {
	return &fakeFactory{
		projects: map[string]*fakeFactoryProject{},
		proofs:   map[string]factory.AnswerProof{},
		changes:  make(chan factory.Change, 32),
	}
}

func (f *fakeFactory) Projects() []factory.ProjectView {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]factory.ProjectView, 0, len(f.projects))
	for _, p := range f.projects {
		out = append(out, factory.ProjectView{
			ID:      p.id,
			Folder:  p.folder,
			Goal:    p.goal,
			Running: p.running,
			Counts:  map[string]int{"planned": len(p.tasks)},
		})
	}
	return out
}

func (f *fakeFactory) Asks(open bool) []flow.Ask {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []flow.Ask
	for _, a := range f.asks {
		if !open || a.Status == flow.AskStatusOpen {
			a.Options = slices.Clone(a.Options)
			out = append(out, a)
		}
	}
	return out
}

func (f *fakeFactory) MintProof(askID, option, inputKind, deviceID string) (factory.AnswerProof, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if (inputKind != factory.ProofInputTap && inputKind != factory.ProofInputVoice) || deviceID == "" || deviceID == "local" {
		return factory.AnswerProof{}, &factory.Refusal{Code: "proof_rejected", Why: "invalid proof source"}
	}
	f.proofSeq++
	p := factory.AnswerProof{
		ID:        fmt.Sprintf("prf-%012x", f.proofSeq),
		AskID:     askID,
		Option:    option,
		InputKind: inputKind,
		DeviceID:  deviceID,
		At:        t0,
	}
	f.proofs[p.ID] = p
	return p, nil
}

func (f *fakeFactory) Answer(askID, option string, proof factory.AnswerProof) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := slices.IndexFunc(f.asks, func(a flow.Ask) bool { return a.ID == askID })
	if idx < 0 {
		return &factory.Refusal{Code: "not_found", Why: "ask not found"}
	}
	a := f.asks[idx]
	if a.Status != flow.AskStatusOpen {
		return &factory.Refusal{Code: flow.RefuseClosed, Why: "already closed"}
	}
	resolved, ok := flow.ResolveAskOption(a.Options, option)
	if a.Gated {
		if strings.TrimSpace(proof.ID) == "" {
			return &factory.Refusal{Code: "proof_required", Why: "proof required"}
		}
		minted, exists := f.proofs[proof.ID]
		if !exists || minted.AskID != askID || (minted.Option != resolved && minted.Option != option) ||
			minted.InputKind != proof.InputKind || minted.DeviceID != proof.DeviceID ||
			(minted.InputKind != factory.ProofInputTap && minted.InputKind != factory.ProofInputVoice) ||
			minted.DeviceID == "" || minted.DeviceID == "local" {
			return &factory.Refusal{Code: "proof_rejected", Why: "proof rejected"}
		}
	}
	if !ok {
		return &factory.Refusal{Code: "invalid_option", Why: "invalid option"}
	}
	a.Status = flow.AskStatusAnswered
	a.Answer = resolved
	a.AnsweredBy = "owner"
	a.ProofID = proof.ID
	a.DeviceID = proof.DeviceID
	f.asks[idx] = a
	f.seq++
	f.changes <- factory.Change{Project: a.Project, Kind: "ask_answer", AskID: a.ID, Task: a.Task, Seq: f.seq}
	return nil
}

func (f *fakeFactory) Start(folder, goal string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, [2]string{folder, goal})
	for _, p := range f.projects {
		if p.folder == folder || p.id == folder {
			p.running = true
			if goal != "" {
				p.goal = goal
			}
			p.activeRuns = []string{p.id + "-coord"}
			return nil
		}
	}
	return factory.ErrProjectNotFound
}

func (f *fakeFactory) Stop(project string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[project]
	if !ok {
		return factory.ErrProjectNotFound
	}
	p.running = false
	p.activeRuns = nil
	f.stopped = append(f.stopped, project)
	return nil
}

func (f *fakeFactory) Changes() <-chan factory.Change { return f.changes }

func (f *fakeFactory) openAsk(a flow.Ask) {
	f.mu.Lock()
	a.Status = flow.AskStatusOpen
	f.asks = append(f.asks, a)
	f.seq++
	c := factory.Change{Project: a.Project, Kind: flow.EvAskOpen, AskID: a.ID, Task: a.Task, Seq: f.seq}
	f.mu.Unlock()
	f.changes <- c
}

func (f *fakeFactory) firstAsk() flow.Ask {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.asks) == 0 {
		return flow.Ask{}
	}
	return f.asks[0]
}

func (f *fakeFactory) mintedProofs() []factory.AnswerProof {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]factory.AnswerProof, 0, len(f.proofs))
	for _, p := range f.proofs {
		out = append(out, p)
	}
	return out
}

func TestFactoryVoiceBareWordsNeverAnswerAskAndDisplacementRailHolds(t *testing.T) {
	eng := newFakeFactory()
	eng.projects["demo"] = &fakeFactoryProject{
		id:         "demo",
		folder:     "/work/demo",
		goal:       "ship slice 6",
		running:    true,
		activeRuns: []string{"demo-coord", "demo-T1-w1"},
		tasks:      []string{"T1", "T2"},
	}

	m := braintest.NewModel(
		braintest.Step{Text: braintest.Reply("Nothing is waiting for yes.", "")},
		braintest.Step{Text: braintest.Reply("Okay, standing by.", "")},
		braintest.Step{Text: braintest.Reply("Sure, standing by.", "")},
	)
	h := newBrainHarness(t, m, func(c *Config) {
		c.Factory = eng
		c.bareAnswers = false
	})

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.sup.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	eng.openAsk(flow.Ask{
		ID:        "ask-demo-20",
		Project:   "demo",
		Kind:      flow.KindRelease,
		Gated:     true,
		Question:  "Cut release v1.2 now?",
		Options:   []string{"Tag 0.1.31", "Hold"},
		Recommend: "Tag 0.1.31",
		Task:      "T1",
		At:        t0,
	})

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && h.log.count("pending.created") == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	// 1. Saying "yes", "okay" or "sure" never answers a factory ask or mints proof,
	// and each utterance goes through the normal orchestrator path.
	for i, word := range []string{"yes", "okay", "sure"} {
		h.clock.Advance(time.Second)
		res := h.say(word)
		if a := eng.firstAsk(); a.Status != flow.AskStatusOpen {
			t.Fatalf("saying %q answered factory ask: %+v", word, a)
		}
		if proofs := eng.mintedProofs(); len(proofs) != 0 {
			t.Fatalf("saying %q minted factory proof(s): %+v", word, proofs)
		}
		if gotTurns := h.log.count("brain.turn"); gotTurns != i+1 {
			t.Fatalf("after %q: brain.turn = %d, want %d (utterance bypassed orchestrator)", word, gotTurns, i+1)
		}
		if res.Spoken == "" {
			t.Fatalf("after %q: empty orchestrator reply", word)
		}
	}

	// 2. Another agent's displacement-rail question still gets its answer via the
	// orchestrator while a silent background factory ask is open (even when an
	// instruction on docs replaced the single PendingAction slot, and when docs
	// blocks unannounced after auth's question was told).
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	ap, ok := h.pending()
	if !ok || ap.SpokenSummary == "" {
		t.Fatalf("auth pending = %+v, want non-empty spoken summary", ap)
	}
	h.sup.OnVoiceEvent(voice.EventPush{Text: ap.SpokenSummary, Urgency: voice.UrgencyIdle, PendingID: ap.ID})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: ap.SpokenSummary, Final: true})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	h.clock.Advance(time.Second)

	// docs blocks unannounced (displacing the pending slot while auth remains told in s.told),
	// and the user says "yes" meant for auth.
	h.block(docsPane, "codex-git-prompt.screen.txt")
	m.Add(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}},
		braintest.Step{Text: braintest.Reply("Told auth yes.", "")},
	)
	h.clock.Advance(500 * time.Millisecond)
	res := h.say("yes")
	if res.Spoken != "Told auth yes." {
		t.Fatalf("orchestrator reply for auth's told question = %+v, want Told auth yes.", res)
	}
	if got := h.keysTo(authPane); got != 1 {
		t.Fatalf("keys sent to auth = %d, want 1", got)
	}
	if got := h.keysTo(docsPane); got != 0 {
		t.Fatalf("keys sent to unannounced docs = %d, want 0", got)
	}
	if a := eng.firstAsk(); a.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
		t.Fatalf("factory ask changed during worker answer: ask=%+v proofs=%+v", a, eng.mintedProofs())
	}

	// Also verify displacement-rail refusal still blocks unannounced docs when user says "okay".
	h.srv.SetScreen(authPane, "running tests\n")
	h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)
	m.Add(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}},
		braintest.Step{Text: braintest.Reply("Docs just asked a new question; confirm first.", "")},
	)
	h.clock.Advance(500 * time.Millisecond)
	_ = h.say("okay")
	if got := h.keysTo(docsPane); got != 0 || h.displacedBlocks() != 1 {
		t.Fatalf("displacement rail on docs: keys=%d displacedBlocks=%d, want 0 and 1", got, h.displacedBlocks())
	}
	if a := eng.firstAsk(); a.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
		t.Fatalf("factory ask changed on okay: ask=%+v proofs=%+v", a, eng.mintedProofs())
	}

	// 3. Voice "stop the factory" and "start the factory" still work.
	h.clock.Advance(time.Second)
	stopRes := h.say("stop the factory")
	if stopRes.Spoken == "" {
		t.Fatal("stop the factory returned empty spoken reply")
	}
	eng.mu.Lock()
	stopped := slices.Contains(eng.stopped, "demo") && !eng.projects["demo"].running
	eng.mu.Unlock()
	if !stopped {
		t.Fatalf("stop the factory did not stop demo")
	}

	h.clock.Advance(time.Second)
	startRes := h.say("start the factory")
	if startRes.Spoken == "" {
		t.Fatal("start the factory returned empty spoken reply")
	}
	eng.mu.Lock()
	restarted := len(eng.started) == 1 && eng.projects["demo"].running
	eng.mu.Unlock()
	if !restarted {
		t.Fatalf("start the factory did not restart demo")
	}
}

func TestFactoryVoiceStopAndStartKeepsPlan(t *testing.T) {
	eng := newFakeFactory()
	eng.projects["demo"] = &fakeFactoryProject{
		id:         "demo",
		folder:     "/work/demo",
		goal:       "ship slice 6",
		running:    true,
		activeRuns: []string{"demo-coord", "demo-T1-w1"},
		tasks:      []string{"T1", "T2", "T3"},
	}
	h := newHarness(t, func(c *Config) { c.Factory = eng })

	res := h.say("stop the factory")
	if res.Spoken == "" {
		t.Fatal("stop the factory returned empty spoken reply")
	}
	eng.mu.Lock()
	p := eng.projects["demo"]
	stopped := slices.Clone(eng.stopped)
	runningAfterStop := p.running
	runsAfterStop := slices.Clone(p.activeRuns)
	tasksAfterStop := slices.Clone(p.tasks)
	eng.mu.Unlock()

	if !slices.Contains(stopped, "demo") || runningAfterStop || len(runsAfterStop) != 0 {
		t.Fatalf("after stop: stopped=%v running=%v activeRuns=%v, want stopped and 0 runs", stopped, runningAfterStop, runsAfterStop)
	}
	if !slices.Equal(tasksAfterStop, []string{"T1", "T2", "T3"}) {
		t.Fatalf("plan tasks lost on stop: got %v, want [T1 T2 T3]", tasksAfterStop)
	}

	h.clock.Advance(time.Second)
	res = h.say("start the factory")
	if res.Spoken == "" {
		t.Fatal("start the factory returned empty spoken reply")
	}
	eng.mu.Lock()
	started := slices.Clone(eng.started)
	runningAfterStart := p.running
	tasksAfterStart := slices.Clone(p.tasks)
	eng.mu.Unlock()

	if len(started) == 0 || started[0][0] != "/work/demo" || started[0][1] != "ship slice 6" || !runningAfterStart {
		t.Fatalf("after start: started=%v running=%v, want started with /work/demo and goal", started, runningAfterStart)
	}
	if !slices.Equal(tasksAfterStart, []string{"T1", "T2", "T3"}) {
		t.Fatalf("plan tasks lost on start: got %v, want [T1 T2 T3]", tasksAfterStart)
	}
}
