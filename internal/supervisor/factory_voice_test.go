package supervisor

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
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
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
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

func (f *fakeFactory) askByID(id string) flow.Ask {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.asks {
		if a.ID == id {
			return a
		}
	}
	return flow.Ask{}
}

func (f *fakeFactory) closeAsk(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.asks {
		if a.ID == id {
			f.asks[i].Status = flow.AskStatusCancelled
			return
		}
	}
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

func startFactorySupervisor(t *testing.T, h *harness) {
	t.Helper()
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
}

func waitFactoryPush(t *testing.T, h *harness, pendingID string) (voice.EventPush, map[string]any) {
	t.Helper()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		pushes := h.pusher.all()
		for i, p := range pushes {
			tags := h.pusher.tagsOf(i)
			if tags != nil && tags[tagPendingID] == pendingID {
				return p, tags
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("VA1: expected spoken push tagged %s=%q; got %d pushes: %+v", tagPendingID, pendingID, len(h.pusher.all()), h.pusher.all())
	return voice.EventPush{}, nil
}

func deliverFactoryPush(h *harness, pendingID, text string) {
	h.sup.OnVoiceEvent(voice.EventPush{Text: text, Urgency: voice.UrgencyIdle, PendingID: pendingID})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: text, Final: true})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	h.clock.Advance(echoWindow + time.Second)
}

func newDemoFactory() *fakeFactory {
	eng := newFakeFactory()
	eng.projects["demo"] = &fakeFactoryProject{
		id:         "demo",
		folder:     "/work/demo",
		goal:       "ship o25",
		running:    true,
		activeRuns: []string{"demo-coord", "demo-T1-w1"},
		tasks:      []string{"T1", "T2"},
	}
	return eng
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

// O25 test 1 (VA1, VA2, VA4, VA7, VA8): single told ask (gate and override),
// announce format, exact label and bareOptionWords (1-3), 4+ options by label only,
// ProofInputVoice, immediate card removal, and pending.confirmed log fields.
func TestFactoryVoiceSingleToldAskGateAndOverride(t *testing.T) {
	t.Run("gate_announce_and_exact_label", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel()
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		startFactorySupervisor(t, h)

		ask := flow.Ask{
			ID:        "ask-demo-101",
			Project:   "demo",
			Kind:      flow.KindGate,
			Gated:     true,
			Question:  "Approve task T1 at abc1234?",
			Options:   []string{"Merge", "Hold"},
			Recommend: "Merge",
			Task:      "T1",
			At:        t0,
		}
		eng.openAsk(ask)

		cardID := factoryAskPrefix + ask.ID
		p, tags := waitFactoryPush(t, h, cardID)
		if p.Urgency != voice.UrgencyIdle {
			t.Fatalf("VA1 push urgency = %v, want UrgencyIdle", p.Urgency)
		}
		if needsUser, _ := tags["needs_user"].(bool); !needsUser {
			t.Fatalf("VA1 push tags = %v, want needs_user=true", tags)
		}
		// VA1 fixed order: name (T1), question, numbered options (1, 2), recommended option.
		iName := strings.Index(p.Text, "T1")
		iQ := strings.Index(p.Text, ask.Question)
		if iName < 0 || iQ < 0 || iName >= iQ {
			t.Fatalf("VA1 push text order wrong (name before question): %q", p.Text)
		}
		tail := p.Text[iQ+len(ask.Question):]
		iOpt1 := strings.Index(tail, "1")
		iLbl1 := strings.Index(tail, "Merge")
		iOpt2 := strings.Index(tail, "2")
		iLbl2 := strings.Index(tail, "Hold")
		iRec := strings.LastIndex(tail, ask.Recommend)
		if iOpt1 < 0 || iLbl1 < 0 || iOpt2 < 0 || iLbl2 < 0 ||
			iOpt1 >= iLbl1 || iLbl1 >= iOpt2 || iOpt2 >= iLbl2 || iLbl2 >= iRec {
			t.Fatalf("VA1 push text order wrong (options and recommendation after question): %q", p.Text)
		}
		// SpokenSummary stays ask.Question so pendingSignature is stable.
		qs := h.sup.FactoryQuestions()
		if len(qs) != 1 || qs[0].SpokenSummary != ask.Question {
			t.Fatalf("VA1 SpokenSummary = %+v, want %q", qs, ask.Question)
		}

		deliverFactoryPush(h, cardID, p.Text)
		res := h.say("Merge")
		if res.Spoken == "" {
			t.Fatal("VA7: expected confirmation line after voice answer")
		}
		if h.log.count("brain.turn") != 0 {
			t.Fatalf("VA7: voice answer should not invoke orchestrator, got %d turns", h.log.count("brain.turn"))
		}
		got := eng.askByID(ask.ID)
		if got.Status != flow.AskStatusAnswered || got.Answer != "Merge" || got.DeviceID != "voice-session" {
			t.Fatalf("VA7: ask = %+v, want answered Merge by voice-session", got)
		}
		proofs := eng.mintedProofs()
		if len(proofs) != 1 || proofs[0].InputKind != factory.ProofInputVoice || proofs[0].DeviceID != "voice-session" || proofs[0].AskID != ask.ID {
			t.Fatalf("VA7: minted proofs = %+v, want 1 ProofInputVoice for voice-session", proofs)
		}
		// Card removed from Session.FactoryAsks in the same call.
		if slices.Contains(h.store.Snapshot().Session.FactoryAsks, cardID) {
			t.Fatalf("VA7: %s still in Session.FactoryAsks immediately after voice answer", cardID)
		}
		// Give syncFactoryAsks a chance to process the ask_answer Change and verify no duplicate pending.confirmed.
		time.Sleep(40 * time.Millisecond)
		confirmed := h.log.ofType("pending.confirmed")
		if len(confirmed) != 1 {
			t.Fatalf("VA7/VA8: pending.confirmed count = %d, want 1 (no duplicate from syncFactoryAsks): %+v", len(confirmed), confirmed)
		}
		pl := payload(confirmed[0])
		if pl["id"] != cardID || pl["via"] != factory.ProofInputVoice || pl["heard"] != "Merge" ||
			pl["answer"] != "Merge" || pl["option"] != 1 || pl["proof_id"] != proofs[0].ID || pl["device_id"] != "voice-session" {
			t.Fatalf("VA8: pending.confirmed payload = %+v", pl)
		}
	})

	t.Run("option_number_and_ordinal_and_override", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			kind   string
			opts   []string
			rec    string
			spoken string
			want   string
			wantN  int
		}{
			{"option_1", flow.KindGate, []string{"Merge", "Hold"}, "Merge", "option 1", "Merge", 1},
			{"first_one", flow.KindGate, []string{"Merge", "Hold"}, "Merge", "first one", "Merge", 1},
			{"override_exact", flow.KindOverride, []string{flow.OverrideOptionMerge, flow.OverrideOptionCancel}, flow.OverrideOptionCancel, flow.OverrideOptionMerge, flow.OverrideOptionMerge, 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				eng := newDemoFactory()
				m := braintest.NewModel()
				h := newBrainHarness(t, m, func(c *Config) {
					c.Factory = eng
					c.bareAnswers = false
				})
				startFactorySupervisor(t, h)

				ask := flow.Ask{
					ID:        "ask-demo-" + tc.name,
					Project:   "demo",
					Kind:      tc.kind,
					Gated:     true,
					Question:  "Proceed with " + tc.name + "?",
					Options:   tc.opts,
					Recommend: tc.rec,
					Task:      "T1",
					At:        t0,
				}
				eng.openAsk(ask)
				cardID := factoryAskPrefix + ask.ID
				p, _ := waitFactoryPush(t, h, cardID)
				deliverFactoryPush(h, cardID, p.Text)

				_ = h.say(tc.spoken)
				got := eng.askByID(ask.ID)
				if got.Status != flow.AskStatusAnswered || got.Answer != tc.want || got.DeviceID != "voice-session" {
					t.Fatalf("after %q: ask = %+v, want answered %q", tc.spoken, got, tc.want)
				}
				proofs := eng.mintedProofs()
				if len(proofs) != 1 || proofs[0].InputKind != factory.ProofInputVoice {
					t.Fatalf("after %q: proofs = %+v, want 1 voice proof", tc.spoken, proofs)
				}
				confirmed := h.log.ofType("pending.confirmed")
				if len(confirmed) != 1 || payload(confirmed[0])["heard"] != tc.spoken || payload(confirmed[0])["option"] != tc.wantN {
					t.Fatalf("VA8 log = %+v, want heard=%q option=%d", confirmed, tc.spoken, tc.wantN)
				}
			})
		}
	})

	t.Run("four_plus_options_by_label_not_option_4", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Say the option label.", "")})
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		startFactorySupervisor(t, h)

		ask := flow.Ask{
			ID:        "ask-demo-4opts",
			Project:   "demo",
			Kind:      flow.KindGate,
			Gated:     true,
			Question:  "Pick release channel?",
			Options:   []string{"Alpha", "Beta", "Canary", "Stable"},
			Recommend: "Stable",
			Task:      "T1",
			At:        t0,
		}
		eng.openAsk(ask)
		cardID := factoryAskPrefix + ask.ID
		p, _ := waitFactoryPush(t, h, cardID)
		deliverFactoryPush(h, cardID, p.Text)

		// VA4: bareOptionWords covers 1-3 only; "option 4" must not answer.
		_ = h.say("option 4")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
			t.Fatalf("VA4: 'option 4' answered ask or minted proof: ask=%+v proofs=%+v", got, eng.mintedProofs())
		}
		if h.log.count("brain.turn") != 1 {
			t.Fatalf("VA4: 'option 4' should fall through to orchestrator, brain.turn=%d", h.log.count("brain.turn"))
		}

		// Exact label for option 4 answers it.
		h.clock.Advance(time.Second)
		_ = h.say("Stable")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusAnswered || got.Answer != "Stable" {
			t.Fatalf("VA4: exact label 'Stable' did not answer option 4: %+v", got)
		}
	})
}

// O25 test 2 (VA2, VA3, VA6): untold, EventPushUnheard, onToldCut, pre-told
// (in.began <= told.seq), and playback echo (saidBefore != "").
func TestFactoryVoiceUntoldUnheardCutPreToldAndEcho(t *testing.T) {
	openGate := func(eng *fakeFactory, id string) flow.Ask {
		ask := flow.Ask{
			ID:        id,
			Project:   "demo",
			Kind:      flow.KindGate,
			Gated:     true,
			Question:  "Approve task T1 at abc1234?",
			Options:   []string{"Merge", "Hold"},
			Recommend: "Merge",
			Task:      "T1",
			At:        t0,
		}
		eng.openAsk(ask)
		return ask
	}

	t.Run("before_event_push", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Orchestrator fallback.", "")})
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		startFactorySupervisor(t, h)

		ask := openGate(eng, "ask-demo-untold")
		_, _ = waitFactoryPush(t, h, factoryAskPrefix+ask.ID)
		// EventPush not delivered yet: ask is untold.
		res := h.say("Merge")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
			t.Fatalf("VA3: answered before EventPush: ask=%+v proofs=%+v", got, eng.mintedProofs())
		}
		if h.log.count("brain.turn") != 0 {
			t.Fatalf("VA6: untold matching word should read back rather than call orchestrator, brain.turn=%d", h.log.count("brain.turn"))
		}
		if res.Scheduling != voice.UrgencyInterrupt || !strings.Contains(res.Spoken, ask.Question) {
			t.Fatalf("VA6: want UrgencyInterrupt read-back containing %q, got %+v", ask.Question, res)
		}
	})

	t.Run("after_push_unheard", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Orchestrator fallback.", "")})
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		startFactorySupervisor(t, h)

		ask := openGate(eng, "ask-demo-unheard")
		cardID := factoryAskPrefix + ask.ID
		p, _ := waitFactoryPush(t, h, cardID)
		deliverFactoryPush(h, cardID, p.Text)
		h.sup.OnVoiceEvent(voice.EventPushUnheard{Push: voice.UnheardPush{Text: p.Text, Reason: voice.UnheardNotVoiced}})
		h.clock.Advance(time.Second)

		res := h.say("Merge")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
			t.Fatalf("VA2: answered after EventPushUnheard: ask=%+v proofs=%+v", got, eng.mintedProofs())
		}
		if h.log.count("brain.turn") != 0 || res.Scheduling != voice.UrgencyInterrupt || !strings.Contains(res.Spoken, ask.Question) {
			t.Fatalf("VA6: after unheard push want read-back, got brain.turn=%d res=%+v", h.log.count("brain.turn"), res)
		}
	})

	t.Run("after_told_cut", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Orchestrator fallback.", "")})
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		startFactorySupervisor(t, h)

		ask := openGate(eng, "ask-demo-cut")
		cardID := factoryAskPrefix + ask.ID
		p, _ := waitFactoryPush(t, h, cardID)
		h.sup.OnVoiceEvent(voice.EventPush{Text: p.Text, Urgency: voice.UrgencyIdle, PendingID: cardID})
		h.sup.OnVoiceEvent(voice.EventInterrupted{})
		h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "", Final: true, Cut: &voice.TranscriptCut{Full: p.Text, HeardMs: 100}})
		h.clock.Advance(time.Second)

		res := h.say("Merge")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
			t.Fatalf("VA2: answered after cut push: ask=%+v proofs=%+v", got, eng.mintedProofs())
		}
		if h.log.count("brain.turn") != 0 || res.Scheduling != voice.UrgencyInterrupt || !strings.Contains(res.Spoken, ask.Question) {
			t.Fatalf("VA6: after cut push want read-back, got brain.turn=%d res=%+v", h.log.count("brain.turn"), res)
		}
	})

	t.Run("pre_told_input", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Orchestrator fallback.", "")})
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		startFactorySupervisor(t, h)

		ask := openGate(eng, "ask-demo-pretold")
		cardID := factoryAskPrefix + ask.ID
		p, _ := waitFactoryPush(t, h, cardID)
		// User starts speaking BEFORE EventPush (in.began <= told.seq).
		h.hear("Merge")
		deliverFactoryPush(h, cardID, p.Text)
		res := h.relay("Merge")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
			t.Fatalf("VA3: pre-told input answered ask: ask=%+v proofs=%+v", got, eng.mintedProofs())
		}
		if h.log.count("brain.turn") != 0 || res.Scheduling != voice.UrgencyInterrupt || !strings.Contains(res.Spoken, ask.Question) {
			t.Fatalf("VA6: pre-told input want read-back, got brain.turn=%d res=%+v", h.log.count("brain.turn"), res)
		}
	})

	t.Run("echo_said_before", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Ignoring playback echo.", "")})
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		startFactorySupervisor(t, h)

		ask := openGate(eng, "ask-demo-echo")
		cardID := factoryAskPrefix + ask.ID
		p, _ := waitFactoryPush(t, h, cardID)
		deliverFactoryPush(h, cardID, p.Text)

		// Voice plays a line and user input arrives within echoWindow (saidBefore != "").
		speak(h, []string{"T1 asks: Approve task T1? 1 Merge, 2 Hold."})
		h.clock.Advance(1700 * time.Millisecond)
		res := h.say("Merge")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
			t.Fatalf("VA3: echo input answered ask: ask=%+v proofs=%+v", got, eng.mintedProofs())
		}
		if h.log.count("brain.turn") != 1 || res.Spoken != "Ignoring playback echo." {
			t.Fatalf("VA3: echo input must pass to orchestrator with no VA6 reply; brain.turn=%d res=%+v", h.log.count("brain.turn"), res)
		}
		if text := lastCall(t, m); !strings.Contains(text, "began while the phone was still playing") {
			t.Fatalf("VA3: orchestrator did not receive echo note:\n%s", text)
		}
	})
}

// O25 test 3 (VA4): 6.3 bare-yes rule and whole-utterance matching.
func TestFactoryVoiceBareYesAndWholeUtteranceMatch(t *testing.T) {
	cases := []struct {
		name     string
		opts     []string
		rec      string
		nonMatch []string
		exact    string
	}{
		{
			name:     "merge_hold_containment_and_bare_yes",
			opts:     []string{"Merge", "Hold"},
			rec:      "Merge",
			nonMatch: []string{"should we merge now", "merge it then", "I would not merge", "yes", "okay", "sure"},
			exact:    "Merge",
		},
		{
			name:     "tag_hold_bare_yes",
			opts:     []string{"Tag 0.1.31", "Hold"},
			rec:      "Tag 0.1.31",
			nonMatch: []string{"yes", "okay", "sure"},
			exact:    "Tag 0.1.31",
		},
		{
			name:     "yes_merge_bare_yes",
			opts:     []string{"Yes, merge", "No, hold"},
			rec:      "Yes, merge",
			nonMatch: []string{"yes", "okay", "sure"},
			exact:    "Yes, merge",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := newDemoFactory()
			m := braintest.NewModel()
			m.Default = &braintest.Step{Text: braintest.Reply("Standing by.", "")}
			h := newBrainHarness(t, m, func(c *Config) {
				c.Factory = eng
				c.bareAnswers = false
			})
			startFactorySupervisor(t, h)

			ask := flow.Ask{
				ID:        "ask-demo-" + tc.name,
				Project:   "demo",
				Kind:      flow.KindGate,
				Gated:     true,
				Question:  "Proceed with " + tc.name + "?",
				Options:   tc.opts,
				Recommend: tc.rec,
				Task:      "T1",
				At:        t0,
			}
			eng.openAsk(ask)
			cardID := factoryAskPrefix + ask.ID
			p, _ := waitFactoryPush(t, h, cardID)
			deliverFactoryPush(h, cardID, p.Text)

			for i, phrase := range tc.nonMatch {
				h.clock.Advance(time.Second)
				_ = h.say(phrase)
				if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
					t.Fatalf("VA4: %q answered ask or minted proof: ask=%+v proofs=%+v", phrase, got, eng.mintedProofs())
				}
				if turns := h.log.count("brain.turn"); turns != i+1 {
					t.Fatalf("VA4: after %q brain.turn=%d, want %d", phrase, turns, i+1)
				}
			}

			// Exact whole-utterance option label answers it.
			h.clock.Advance(time.Second)
			_ = h.say(tc.exact)
			if got := eng.askByID(ask.ID); got.Status != flow.AskStatusAnswered || got.Answer != tc.exact {
				t.Fatalf("VA4: exact %q did not answer ask: %+v", tc.exact, got)
			}
		})
	}
}

// O25 test 4 (VA1, VA6): connect-time announce for offline ask, and untold read-back marking told.
func TestFactoryVoiceConnectAnnounceAndUntoldReadBack(t *testing.T) {
	t.Run("connect_time_announce", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel()
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		h.sup.SetVoiceConnected(false)
		startFactorySupervisor(t, h)

		ask := flow.Ask{
			ID:        "ask-demo-offline",
			Project:   "demo",
			Kind:      flow.KindGate,
			Gated:     true,
			Question:  "Approve task T1 at offline1?",
			Options:   []string{"Merge", "Hold"},
			Recommend: "Merge",
			Task:      "T1",
			At:        t0,
		}
		eng.openAsk(ask)
		deadline := time.Now().Add(200 * time.Millisecond)
		for time.Now().Before(deadline) && h.log.count("pending.created") == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		if len(h.pusher.all()) != 0 {
			t.Fatalf("VA1: pushed while VoiceConnected=false: %+v", h.pusher.all())
		}

		// Connecting announces open untold asks.
		h.sup.SetVoiceConnected(true)
		cardID := factoryAskPrefix + ask.ID
		p, _ := waitFactoryPush(t, h, cardID)
		deliverFactoryPush(h, cardID, p.Text)

		_ = h.say("Merge")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusAnswered || got.Answer != "Merge" {
			t.Fatalf("VA1/VA7: offline ask not answered after connect announce: %+v", got)
		}
	})

	t.Run("untold_readback_marks_told", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel()
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		startFactorySupervisor(t, h)

		ask := flow.Ask{
			ID:        "ask-demo-readback",
			Project:   "demo",
			Kind:      flow.KindGate,
			Gated:     true,
			Question:  "Approve task T1 at readback1?",
			Options:   []string{"Merge", "Hold"},
			Recommend: "Merge",
			Task:      "T1",
			At:        t0,
		}
		eng.openAsk(ask)
		_, _ = waitFactoryPush(t, h, factoryAskPrefix+ask.ID)

		// First "Merge" while untold: no answer, no proof, VA6 read-back marks it told.
		res1 := h.say("Merge")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
			t.Fatalf("VA6: first 'Merge' on untold ask answered: ask=%+v proofs=%+v", got, eng.mintedProofs())
		}
		if res1.Scheduling != voice.UrgencyInterrupt || !strings.Contains(res1.Spoken, ask.Question) {
			t.Fatalf("VA6: want read-back containing %q, got %+v", ask.Question, res1)
		}

		// Second "Merge" after read-back answers it.
		h.clock.Advance(time.Second)
		_ = h.say("Merge")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusAnswered || got.Answer != "Merge" {
			t.Fatalf("VA6/VA7: second 'Merge' after read-back did not answer ask: %+v", got)
		}
	})
}

// O25 test 5 (VA5, VA6): multiple open asks — unnamed asks which, named answers only that one,
// unrelated speech passes to orchestrator.
func TestFactoryVoiceMultipleOpenAsks(t *testing.T) {
	eng := newDemoFactory()
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Two tasks are waiting.", "")})
	h := newBrainHarness(t, m, func(c *Config) {
		c.Factory = eng
		c.bareAnswers = false
	})
	startFactorySupervisor(t, h)

	a1 := flow.Ask{
		ID: "ask-demo-t1", Project: "demo", Kind: flow.KindGate, Gated: true,
		Question: "Approve task T1?", Options: []string{"Merge", "Hold"}, Recommend: "Merge", Task: "T1", At: t0,
	}
	a2 := flow.Ask{
		ID: "ask-demo-t2", Project: "demo", Kind: flow.KindGate, Gated: true,
		Question: "Approve task T2?", Options: []string{"Merge", "Hold"}, Recommend: "Merge", Task: "T2", At: t0.Add(time.Second),
	}
	eng.openAsk(a1)
	eng.openAsk(a2)
	p1, _ := waitFactoryPush(t, h, factoryAskPrefix+a1.ID)
	p2, _ := waitFactoryPush(t, h, factoryAskPrefix+a2.ID)
	deliverFactoryPush(h, factoryAskPrefix+a1.ID, p1.Text)
	deliverFactoryPush(h, factoryAskPrefix+a2.ID, p2.Text)

	// Unnamed "merge" answers neither, mints 0 proofs, asks which naming T1 and T2.
	res := h.say("merge")
	if eng.askByID(a1.ID).Status != flow.AskStatusOpen || eng.askByID(a2.ID).Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
		t.Fatalf("VA5/VA6: unnamed 'merge' answered with 2 asks open: a1=%+v a2=%+v proofs=%+v", eng.askByID(a1.ID), eng.askByID(a2.ID), eng.mintedProofs())
	}
	if h.log.count("brain.turn") != 0 || res.Scheduling != voice.UrgencyInterrupt || !strings.Contains(res.Spoken, "T1") || !strings.Contains(res.Spoken, "T2") {
		t.Fatalf("VA6: want UrgencyInterrupt clarifying question naming T1 and T2, got brain.turn=%d res=%+v", h.log.count("brain.turn"), res)
	}

	// Unrelated speech passes to orchestrator.
	h.clock.Advance(time.Second)
	_ = h.say("what is the status")
	if h.log.count("brain.turn") != 1 {
		t.Fatalf("VA6: unrelated speech should reach orchestrator, brain.turn=%d", h.log.count("brain.turn"))
	}

	// "T1 merge" (name at start) answers T1 only.
	h.clock.Advance(time.Second)
	_ = h.say("T1 merge")
	if got := eng.askByID(a1.ID); got.Status != flow.AskStatusAnswered || got.Answer != "Merge" {
		t.Fatalf("VA5: 'T1 merge' did not answer T1: %+v", got)
	}
	if got := eng.askByID(a2.ID); got.Status != flow.AskStatusOpen {
		t.Fatalf("VA5: 'T1 merge' touched T2: %+v", got)
	}

	// Open T3 alongside T2 and test name at end ("merge T3").
	a3 := flow.Ask{
		ID: "ask-demo-t3", Project: "demo", Kind: flow.KindGate, Gated: true,
		Question: "Approve task T3?", Options: []string{"Merge", "Hold"}, Recommend: "Merge", Task: "T3", At: t0.Add(2 * time.Second),
	}
	eng.openAsk(a3)
	p3, _ := waitFactoryPush(t, h, factoryAskPrefix+a3.ID)
	deliverFactoryPush(h, factoryAskPrefix+a3.ID, p3.Text)

	h.clock.Advance(time.Second)
	_ = h.say("merge T3")
	if got := eng.askByID(a3.ID); got.Status != flow.AskStatusAnswered || got.Answer != "Merge" {
		t.Fatalf("VA5: 'merge T3' did not answer T3: %+v", got)
	}
	if got := eng.askByID(a2.ID); got.Status != flow.AskStatusOpen {
		t.Fatalf("VA5: 'merge T3' touched T2: %+v", got)
	}
}

// O25 test 6 (VA3, supervisor half): InputTyped never mints a proof or answers a factory ask.
func TestFactoryVoiceTypedInputNeverAnswers(t *testing.T) {
	eng := newDemoFactory()
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Use tap or live voice.", "")})
	h := newBrainHarness(t, m, func(c *Config) {
		c.Factory = eng
		c.bareAnswers = false
	})
	startFactorySupervisor(t, h)

	ask := flow.Ask{
		ID: "ask-demo-typed", Project: "demo", Kind: flow.KindGate, Gated: true,
		Question: "Approve task T1?", Options: []string{"Merge", "Hold"}, Recommend: "Merge", Task: "T1", At: t0,
	}
	eng.openAsk(ask)
	p, _ := waitFactoryPush(t, h, factoryAskPrefix+ask.ID)
	deliverFactoryPush(h, factoryAskPrefix+ask.ID, p.Text)

	h.sup.ShepherdInput(InputTyped, "Merge")
	if _, err := h.sup.TypedTurn(context.Background()); err != nil {
		t.Fatalf("TypedTurn: %v", err)
	}
	if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
		t.Fatalf("VA3: InputTyped answered factory ask or minted proof: ask=%+v proofs=%+v", got, eng.mintedProofs())
	}
	if h.log.count("brain.turn") != 1 {
		t.Fatalf("VA3: InputTyped should reach orchestrator, brain.turn=%d", h.log.count("brain.turn"))
	}
}

// O25 test 7 (VA2, VA5, VA6, VA9): agent-prompt coexistence and displacement rail.
func TestFactoryVoiceAgentPromptCoexistenceAndDisplacementRail(t *testing.T) {
	t.Run("told_factory_ask_displaces_newer_untold_agent_prompt", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel()
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		startFactorySupervisor(t, h)

		ask := flow.Ask{
			ID: "ask-demo-disp", Project: "demo", Kind: flow.KindGate, Gated: true,
			Question: "Approve task T1?", Options: []string{"Merge", "Hold"}, Recommend: "Merge", Task: "T1", At: t0,
		}
		eng.openAsk(ask)
		p, _ := waitFactoryPush(t, h, factoryAskPrefix+ask.ID)
		deliverFactoryPush(h, factoryAskPrefix+ask.ID, p.Text)

		// Newer untold agent prompt (docs) blocks while factory ask is told.
		h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
		h.block(docsPane, "codex-git-prompt.screen.txt")

		m.Add(
			braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "docs", "keys": []any{"y"}})}},
			braintest.Step{Text: braintest.Reply("Docs just asked; confirm first.", "")},
		)
		h.clock.Advance(500 * time.Millisecond)
		_ = h.say("yes")
		if got := h.keysTo(docsPane); got != 0 || h.displacedBlocks() != 1 {
			t.Fatalf("VA9: told factory ask must trigger displacement rail on untold docs: keys=%d displacedBlocks=%d, want 0 and 1", got, h.displacedBlocks())
		}
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
			t.Fatalf("VA9: bare 'yes' touched factory ask: %+v", got)
		}
	})

	t.Run("coexistence_collision_and_disambiguation", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel()
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = true
		})
		startFactorySupervisor(t, h)

		// Told agent prompt (auth).
		h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
		h.block(authPane, "codex-approval-prompt.screen.txt")
		ap, ok := h.pending()
		if !ok {
			t.Fatal("expected auth pending action")
		}
		h.sup.OnVoiceEvent(voice.EventPush{Text: ap.SpokenSummary, Urgency: voice.UrgencyIdle, PendingID: ap.ID})
		h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: ap.SpokenSummary, Final: true})
		h.sup.OnVoiceEvent(voice.EventTurnComplete{})
		h.clock.Advance(time.Second)

		// Told factory ask (T1).
		ask := flow.Ask{
			ID: "ask-demo-coexist", Project: "demo", Kind: flow.KindGate, Gated: true,
			Question: "Approve task T1?", Options: []string{"Merge", "Hold"}, Recommend: "Merge", Task: "T1", At: t0,
		}
		eng.openAsk(ask)
		fp, _ := waitFactoryPush(t, h, factoryAskPrefix+ask.ID)
		deliverFactoryPush(h, factoryAskPrefix+ask.ID, fp.Text)

		// "option one" matches both auth (bareAnswer) and T1 (VA4): answer neither, ask which.
		res := h.say("option one")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
			t.Fatalf("VA5/VA6: colliding 'option one' answered factory ask: %+v", got)
		}
		if got := h.keysTo(authPane); got != 0 {
			t.Fatalf("VA5/VA6: colliding 'option one' sent keys to auth: %d", got)
		}
		if res.Scheduling != voice.UrgencyInterrupt || res.Spoken == "" {
			t.Fatalf("VA6: colliding 'option one' want UrgencyInterrupt clarifying reply, got %+v", res)
		}

		// "merge" matches only the factory ask (not auth's bareAnswer): answers T1.
		h.clock.Advance(time.Second)
		_ = h.say("merge")
		if got := eng.askByID(ask.ID); got.Status != flow.AskStatusAnswered || got.Answer != "Merge" {
			t.Fatalf("VA5: 'merge' did not answer factory ask: %+v", got)
		}
		if got := h.keysTo(authPane); got != 0 {
			t.Fatalf("VA5: 'merge' sent keys to auth: %d", got)
		}

		// Open a second told factory ask and verify bare "yes" answers auth without touching it.
		ask2 := flow.Ask{
			ID: "ask-demo-coexist2", Project: "demo", Kind: flow.KindGate, Gated: true,
			Question: "Approve task T2?", Options: []string{"Merge", "Hold"}, Recommend: "Merge", Task: "T2", At: t0.Add(time.Second),
		}
		eng.openAsk(ask2)
		fp2, _ := waitFactoryPush(t, h, factoryAskPrefix+ask2.ID)
		deliverFactoryPush(h, factoryAskPrefix+ask2.ID, fp2.Text)

		h.clock.Advance(time.Second)
		_ = h.say("yes")
		if got := h.keysTo(authPane); got != 1 {
			t.Fatalf("VA4/VA9: bare 'yes' should answer auth, keys=%d", got)
		}
		if got := eng.askByID(ask2.ID); got.Status != flow.AskStatusOpen {
			t.Fatalf("VA4: bare 'yes' touched factory ask2: %+v", got)
		}
	})
}

// O25 test 8 (VA3, VA7): concurrent close drops stale card and reports already closed;
// one voice input cannot answer two asks.
func TestFactoryVoiceConcurrentCloseAndSingleUseInput(t *testing.T) {
	t.Run("concurrent_close", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel()
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		startFactorySupervisor(t, h)

		ask := flow.Ask{
			ID: "ask-demo-closed", Project: "demo", Kind: flow.KindGate, Gated: true,
			Question: "Approve task T1?", Options: []string{"Merge", "Hold"}, Recommend: "Merge", Task: "T1", At: t0,
		}
		eng.openAsk(ask)
		cardID := factoryAskPrefix + ask.ID
		p, _ := waitFactoryPush(t, h, cardID)
		deliverFactoryPush(h, cardID, p.Text)

		// Concurrently close the ask in the engine without notifying syncFactoryAsks.
		eng.closeAsk(ask.ID)
		res := h.say("Merge")
		if h.log.count("brain.turn") != 0 {
			t.Fatalf("VA7: concurrent close should be handled by fast path, brain.turn=%d", h.log.count("brain.turn"))
		}
		if !strings.Contains(strings.ToLower(res.Spoken), "closed") {
			t.Fatalf("VA7: want already-closed reply, got %+v", res)
		}
		if slices.Contains(h.store.Snapshot().Session.FactoryAsks, cardID) || len(h.sup.FactoryQuestions()) != 0 {
			t.Fatalf("VA7: stale card %s not dropped on concurrent close", cardID)
		}
	})

	t.Run("single_use_input", func(t *testing.T) {
		eng := newDemoFactory()
		m := braintest.NewModel()
		h := newBrainHarness(t, m, func(c *Config) {
			c.Factory = eng
			c.bareAnswers = false
		})
		startFactorySupervisor(t, h)

		a1 := flow.Ask{
			ID: "ask-demo-u1", Project: "demo", Kind: flow.KindGate, Gated: true,
			Question: "Approve task T1?", Options: []string{"Merge", "Hold"}, Recommend: "Merge", Task: "T1", At: t0,
		}
		a2 := flow.Ask{
			ID: "ask-demo-u2", Project: "demo", Kind: flow.KindGate, Gated: true,
			Question: "Approve task T2?", Options: []string{"Merge", "Hold"}, Recommend: "Merge", Task: "T2", At: t0.Add(time.Second),
		}
		eng.openAsk(a1)
		eng.openAsk(a2)
		p1, _ := waitFactoryPush(t, h, factoryAskPrefix+a1.ID)
		p2, _ := waitFactoryPush(t, h, factoryAskPrefix+a2.ID)
		deliverFactoryPush(h, factoryAskPrefix+a1.ID, p1.Text)
		deliverFactoryPush(h, factoryAskPrefix+a2.ID, p2.Text)

		h.hear("T1 merge")
		_ = h.relay("T1 merge")
		if got := eng.askByID(a1.ID); got.Status != flow.AskStatusAnswered {
			t.Fatalf("VA7: T1 not answered: %+v", got)
		}
		// A second tool call without a fresh voice input cannot reuse the input to answer T2.
		_ = h.relay("T2 merge")
		if got := eng.askByID(a2.ID); got.Status != flow.AskStatusOpen {
			t.Fatalf("VA3: single voice input answered a second ask T2: %+v", got)
		}
		if len(eng.mintedProofs()) != 1 {
			t.Fatalf("VA3: expected exactly 1 minted proof, got %+v", eng.mintedProofs())
		}
	})
}

// O25 review round 1 (VA3, VA4, VA8): barge-in stop relay (relayStopRest)
// strips leading stop words and relays the remainder; that remainder is not a
// whole owner utterance and must never answer a factory ask (gate or override).
func TestFactoryVoiceStopRestNeverAnswersAsk(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		opts     []string
		rec      string
		bargeIn  string
		wantRest string
		exact    string
	}{
		{
			name:     "override_stop_rest",
			kind:     flow.KindOverride,
			opts:     []string{flow.OverrideOptionMerge, flow.OverrideOptionCancel},
			rec:      flow.OverrideOptionCancel,
			bargeIn:  "No, wait, yes, merge now",
			wantRest: "yes, merge now",
			exact:    flow.OverrideOptionMerge,
		},
		{
			name:     "gate_stop_rest",
			kind:     flow.KindGate,
			opts:     []string{"Merge", "Hold"},
			rec:      "Merge",
			bargeIn:  "Wait, hold on, merge?",
			wantRest: "merge?",
			exact:    "Merge",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := newDemoFactory()
			m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Standing by after stop.", "")})
			h := newBrainHarness(t, m, func(c *Config) {
				c.Factory = eng
				c.bareAnswers = false
			})
			startFactorySupervisor(t, h)

			ask := flow.Ask{
				ID:        "ask-demo-" + tc.name,
				Project:   "demo",
				Kind:      tc.kind,
				Gated:     true,
				Question:  "Proceed with " + tc.name + "?",
				Options:   tc.opts,
				Recommend: tc.rec,
				Task:      "T1",
				At:        t0,
			}
			eng.openAsk(ask)
			cardID := factoryAskPrefix + ask.ID
			p, _ := waitFactoryPush(t, h, cardID)
			deliverFactoryPush(h, cardID, p.Text)

			// Owner barges in with a stop lead followed by an option-like remainder;
			// voice calls stop(), which runs relayStopRest over the stripped tail.
			h.hear(tc.bargeIn)
			_ = h.call(ToolStop, nil)
			h.sup.replies.Wait()

			if ev := h.log.ofType("supervisor.stop_rest"); len(ev) != 1 || payload(ev[0])["text"] != tc.wantRest {
				t.Fatalf("supervisor.stop_rest = %+v, want 1 event with text=%q", ev, tc.wantRest)
			}
			if got := eng.askByID(ask.ID); got.Status != flow.AskStatusOpen || len(eng.mintedProofs()) != 0 {
				t.Fatalf("VA3/VA4: stop-relayed remainder from %q answered ask or minted proof: ask=%+v proofs=%+v", tc.bargeIn, got, eng.mintedProofs())
			}
			if turns := h.log.count("brain.turn"); turns != 1 {
				t.Fatalf("VA3/VA4: stop-relayed remainder should reach orchestrator, brain.turn=%d", turns)
			}

			// A whole owner utterance matching an engine option still answers the ask.
			h.clock.Advance(time.Second)
			_ = h.say(tc.exact)
			if got := eng.askByID(ask.ID); got.Status != flow.AskStatusAnswered || got.Answer != tc.exact || got.DeviceID != "voice-session" {
				t.Fatalf("VA4/VA7: whole utterance %q did not answer ask: %+v", tc.exact, got)
			}
			if proofs := eng.mintedProofs(); len(proofs) != 1 || proofs[0].InputKind != factory.ProofInputVoice {
				t.Fatalf("VA7: minted proofs = %+v, want 1 ProofInputVoice", proofs)
			}
			if confirmed := h.log.ofType("pending.confirmed"); len(confirmed) != 1 || payload(confirmed[0])["heard"] != tc.exact {
				t.Fatalf("VA8: pending.confirmed = %+v, want heard=%q", confirmed, tc.exact)
			}
		})
	}
}

// TestFactoryVoiceStartRefusedByEngineStartChecks_KB_STA_18 verifies O31 row 10 / J1a:
// voice "start the factory" (factoryControl(true)) cannot bypass Engine.Start's
// start checks on a known stopped project.
func TestFactoryVoiceStartRefusedByEngineStartChecks_KB_STA_18(t *testing.T) {
	dataDir := t.TempDir()
	repo := filepath.Join(t.TempDir(), "repo")
	if b, err := exec.Command("git", "init", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, b)
	}
	if b, err := exec.Command("git", "-C", repo, "config", "--local", "sheepdog.projectId", "demo").CombinedOutput(); err != nil {
		t.Fatalf("git config projectId: %v: %s", err, b)
	}
	paths := factory.Paths{DataDir: dataDir}
	if err := factory.SaveState(paths.State("demo"), flow.State{
		Seq:      1,
		Project:  flow.Project{ID: "demo", Folder: repo, Goal: "ship 8.1", Started: false},
		Settings: flow.DefaultSettings(),
	}); err != nil {
		t.Fatal(err)
	}

	hcSrv := hctest.New(t)
	hcSrv.OnExec = hctest.GitExec
	eng := factory.New(paths, hc.NewClient(hcSrv.Path))
	checkCalls := 0
	eng.StartCheck = func(ctx context.Context, opts factory.DoctorOptions) []onboard.Result {
		checkCalls++
		opts.HCSocket = ""
		opts.RunCommand = func(_ context.Context, _ string, _ []string, argv ...string) factory.CommandOutput {
			key := strings.Join(argv, " ")
			switch {
			case strings.HasSuffix(key, "git --version"):
				return factory.CommandOutput{Stdout: "git version 2.45.0\n"}
			case strings.HasSuffix(key, "rev-parse --show-toplevel"):
				return factory.CommandOutput{Stdout: repo}
			case strings.HasSuffix(key, "remote get-url --all origin"), strings.HasSuffix(key, "remote get-url --push --all origin"):
				return factory.CommandOutput{Stdout: "https://github.com/o/r.git"}
			case strings.HasSuffix(key, "refs/remotes/origin/HEAD"):
				return factory.CommandOutput{Stdout: "origin/main"}
			case strings.HasSuffix(key, "rev-parse --verify --quiet origin/main"):
				return factory.CommandOutput{Stdout: "abc1234\n"}
			case strings.HasSuffix(key, "fetch --prune origin"):
				return factory.CommandOutput{}
			case strings.HasSuffix(key, "claude auth status --json"):
				return factory.CommandOutput{Stdout: `{"loggedIn":false}`}
			case strings.HasSuffix(key, "ls-tree --name-only origin/main .github/workflows/"):
				return factory.CommandOutput{Stdout: ".github/workflows/ci.yml"}
			case strings.HasSuffix(key, "show origin/main:.github/workflows/ci.yml"):
				return factory.CommandOutput{Stdout: "on:\n  pull_request:\n    branches: [main]\njobs: {}\n"}
			case strings.Contains(key, "gh repo view"):
				return factory.CommandOutput{Stdout: `{"defaultBranchRef":{"name":"main"},"viewerPermission":"WRITE"}`}
			case strings.Contains(key, "gh api repos/o/r/branches/main"):
				return factory.CommandOutput{Stdout: `{"protected":false}`}
			case strings.Contains(key, "gh api repos/o/r/rules/branches/main"):
				return factory.CommandOutput{Stdout: "[]"}
			default:
				return factory.CommandOutput{Exit: 1, Stderr: "unexpected: " + key}
			}
		}
		return factory.StartChecks(ctx, opts)
	}

	h := newHarness(t, func(c *Config) { c.Factory = eng })
	res := h.say("start the factory")

	if checkCalls != 1 {
		t.Fatalf("Engine.StartCheck calls = %d, want 1", checkCalls)
	}
	if !strings.Contains(res.Spoken, "Could not update the factory") || !strings.Contains(res.Spoken, "claude auth login") {
		t.Fatalf("voice start spoken = %q, want refusal with claude auth login fix", res.Spoken)
	}
	projs := eng.Projects()
	if len(projs) != 1 || projs[0].Running {
		t.Fatalf("refused voice start left projects = %+v, want demo stopped", projs)
	}
}
