package supervisor

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
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

func TestFactoryAskVoiceAnswerRequiresLiveVoice(t *testing.T) {
	eng := newFakeFactory()
	h := newHarness(t, func(c *Config) { c.Factory = eng })
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
		Options:   []string{"Yes, ship it", "Hold"},
		Recommend: "Yes, ship it",
		Task:      "T1",
		At:        t0,
	})

	// Give the ask bridge a moment to consume Engine.Changes.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && h.log.count("pending.created") == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	// 1. Model-initiated relay without user speech must not answer.
	h.clock.Advance(time.Second)
	h.relay("yes")
	if a := eng.firstAsk(); a.Status != flow.AskStatusOpen {
		t.Fatalf("model-initiated relay answered ask: %+v", a)
	}

	// 2. Typed input (not live voice) must not answer a gated ask.
	h.clock.Advance(time.Second)
	h.sup.ShepherdInput(InputTyped, "yes")
	h.relay("yes")
	if a := eng.firstAsk(); a.Status != flow.AskStatusOpen {
		t.Fatalf("typed input answered gated ask: %+v", a)
	}

	// 3. Disconnected voice must not answer a gated ask.
	h.clock.Advance(time.Second)
	h.sup.SetVoiceConnected(false)
	h.hear("yes")
	h.relay("yes")
	if a := eng.firstAsk(); a.Status != flow.AskStatusOpen {
		t.Fatalf("disconnected voice answered gated ask: %+v", a)
	}

	// 4. Live voice answers the gated ask with a minted voice proof.
	h.clock.Advance(time.Second)
	h.sup.SetVoiceConnected(true)
	h.hear("yes")
	h.relay("yes")
	a := eng.firstAsk()
	if a.Status != flow.AskStatusAnswered || a.Answer != "Yes, ship it" {
		t.Fatalf("live voice did not answer ask: %+v", a)
	}
	if a.ProofID == "" || a.DeviceID == "" || a.DeviceID == "local" {
		t.Fatalf("live voice answer missing valid proof/device: %+v", a)
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
