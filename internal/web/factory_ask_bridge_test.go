package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

type webFakeFactory struct {
	mu       sync.Mutex
	asks     []flow.Ask
	proofs   map[string]factory.AnswerProof
	changes  chan factory.Change
	seq      int64
	proofSeq int
}

func newWebFakeFactory() *webFakeFactory {
	return &webFakeFactory{
		proofs:  map[string]factory.AnswerProof{},
		changes: make(chan factory.Change, 32),
	}
}

func (f *webFakeFactory) Projects() []factory.ProjectView { return nil }

func (f *webFakeFactory) Asks(open bool) []flow.Ask {
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

func (f *webFakeFactory) MintProof(askID, option, inputKind, deviceID string) (factory.AnswerProof, error) {
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
		At:        time.Now().UTC(),
	}
	f.proofs[p.ID] = p
	return p, nil
}

func (f *webFakeFactory) Answer(askID, option string, proof factory.AnswerProof) error {
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

func (f *webFakeFactory) Start(string, string, int) error { return nil }
func (f *webFakeFactory) Stop(string) error               { return nil }
func (f *webFakeFactory) Changes() <-chan factory.Change  { return f.changes }

func (f *webFakeFactory) openAsk(a flow.Ask) {
	f.mu.Lock()
	a.Status = flow.AskStatusOpen
	f.asks = append(f.asks, a)
	f.seq++
	c := factory.Change{Project: a.Project, Kind: flow.EvAskOpen, AskID: a.ID, Task: a.Task, Seq: f.seq}
	f.mu.Unlock()
	f.changes <- c
}

func (f *webFakeFactory) cancelAsk(askID string) {
	f.mu.Lock()
	idx := slices.IndexFunc(f.asks, func(a flow.Ask) bool { return a.ID == askID })
	if idx < 0 {
		f.mu.Unlock()
		return
	}
	f.asks[idx].Status = flow.AskStatusCancelled
	a := f.asks[idx]
	f.seq++
	c := factory.Change{Project: a.Project, Kind: flow.EvAskCancel, AskID: a.ID, Task: a.Task, Seq: f.seq}
	f.mu.Unlock()
	f.changes <- c
}

func (f *webFakeFactory) firstAsk() flow.Ask {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.asks) == 0 {
		return flow.Ask{}
	}
	return f.asks[0]
}

func (f *webFakeFactory) mintedProofs() []factory.AnswerProof {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]factory.AnswerProof, 0, len(f.proofs))
	for _, p := range f.proofs {
		out = append(out, p)
	}
	return out
}

func startSupervisorRun(t *testing.T, h *harness) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.sup.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func findQuestion(st StateResponse, id string) (PendingView, bool) {
	for _, q := range st.Questions {
		if q.ID == id {
			return q, true
		}
	}
	return PendingView{}, false
}

func openSSEStream(t *testing.T, url string) (<-chan eventlog.Event, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	ch := make(chan eventlog.Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var ev eventlog.Event
			if json.Unmarshal([]byte(data), &ev) == nil {
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	cleanup := func() {
		cancel()
		_ = resp.Body.Close()
		<-done
	}
	t.Cleanup(cleanup)
	return ch, cleanup
}

func TestFactoryAskBridgeAppearsAndDisappears(t *testing.T) {
	eng := newWebFakeFactory()
	h := newHarness(t, harnessOpts{factory: eng})
	startSupervisorRun(t, h)

	sse, _ := openSSEStream(t, h.http.URL+"/events")

	// Non-gated worker decision asks must not surface to the owner.
	eng.openAsk(flow.Ask{
		ID:        "ask-demo-decision",
		Project:   "demo",
		Kind:      flow.KindDecision,
		Gated:     false,
		Question:  "Use table-driven helper?",
		Options:   []string{"Yes", "No"},
		Recommend: "Yes",
		Task:      "T1",
		At:        time.Now().UTC(),
	})

	// Open owner ask from Engine.Changes must appear in /api/state and /events.
	eng.openAsk(flow.Ask{
		ID:        "ask-demo-1",
		Project:   "demo",
		Kind:      flow.KindRelease,
		Gated:     true,
		Question:  "Cut release v1.2 now?",
		Options:   []string{"Yes, cut v1.2", "Wait for next batch"},
		Recommend: "Yes, cut v1.2",
		Task:      "T1",
		At:        time.Now().UTC(),
	})

	const wantID = "fask-ask-demo-1"
	var got PendingView
	waitFor(t, 500*time.Millisecond, func() bool {
		var st StateResponse
		if h.getJSON("/api/state", &st) != http.StatusOK {
			return false
		}
		if _, foundDecision := findQuestion(st, "fask-ask-demo-decision"); foundDecision {
			t.Errorf("non-gated decision ask leaked into /api/state questions")
		}
		q, ok := findQuestion(st, wantID)
		if ok {
			got = q
		}
		return ok
	}, "open owner ask "+wantID+" to appear in /api/state")

	if got.Kind != state.PendingAnswerPrompt || got.ID != wantID || got.PaneID != "factory:demo" || got.Question != "Cut release v1.2 now?" {
		t.Fatalf("PendingView = %+v", got)
	}
	if len(got.Options) != 2 ||
		got.Options[0].Index != 1 || got.Options[0].Label != "Yes, cut v1.2" || !got.Options[0].Recommended ||
		got.Options[1].Index != 2 || got.Options[1].Label != "Wait for next batch" {
		t.Fatalf("PendingView options = %+v", got.Options)
	}

	// Verify /events emitted pending.created for fask-ask-demo-1.
	sawCreated := false
	deadline := time.After(500 * time.Millisecond)
	for !sawCreated {
		select {
		case ev := <-sse:
			if ev.Type != "pending.created" || ev.PaneID != "factory:demo" {
				continue
			}
			var pl struct {
				ID       string `json:"id"`
				Kind     string `json:"kind"`
				Question string `json:"question"`
				Options  []struct {
					Index int    `json:"index"`
					Label string `json:"label"`
				} `json:"options"`
			}
			if json.Unmarshal(ev.Payload, &pl) == nil && pl.ID == wantID && pl.Kind == string(state.PendingAnswerPrompt) && pl.Question == "Cut release v1.2 now?" && len(pl.Options) == 2 {
				sawCreated = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for pending.created SSE event for %s", wantID)
		}
	}

	// Cancelling the ask removes it from /api/state and emits pending.cancelled on /events.
	eng.cancelAsk("ask-demo-1")
	waitFor(t, 500*time.Millisecond, func() bool {
		var st StateResponse
		if h.getJSON("/api/state", &st) != http.StatusOK {
			return false
		}
		_, still := findQuestion(st, wantID)
		return !still && (st.PendingAction == nil || st.PendingAction.ID != wantID)
	}, "cancelled ask "+wantID+" to disappear from /api/state")

	sawCancelled := false
	deadline = time.After(500 * time.Millisecond)
	for !sawCancelled {
		select {
		case ev := <-sse:
			if ev.Type != "pending.cancelled" {
				continue
			}
			var pl struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(ev.Payload, &pl) == nil && pl.ID == wantID {
				sawCancelled = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for pending.cancelled SSE event for %s", wantID)
		}
	}

	// Answering a second ask through the engine also removes it from /api/state.
	eng.openAsk(flow.Ask{
		ID:        "ask-demo-2",
		Project:   "demo",
		Kind:      flow.KindGate,
		Gated:     true,
		Question:  "Merge PR #42?",
		Options:   []string{"Yes, merge", "No, hold"},
		Recommend: "Yes, merge",
		Task:      "T2",
		Head:      "abc1234",
		At:        time.Now().UTC(),
	})
	const wantID2 = "fask-ask-demo-2"
	waitFor(t, 500*time.Millisecond, func() bool {
		var st StateResponse
		if h.getJSON("/api/state", &st) != http.StatusOK {
			return false
		}
		_, ok := findQuestion(st, wantID2)
		return ok
	}, "second ask "+wantID2+" to appear in /api/state")

	proof, err := eng.MintProof("ask-demo-2", "Yes, merge", factory.ProofInputTap, "dev-owner-1")
	if err != nil {
		t.Fatalf("MintProof: %v", err)
	}
	if err := eng.Answer("ask-demo-2", "Yes, merge", proof); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitFor(t, 500*time.Millisecond, func() bool {
		var st StateResponse
		if h.getJSON("/api/state", &st) != http.StatusOK {
			return false
		}
		_, still := findQuestion(st, wantID2)
		return !still && (st.PendingAction == nil || st.PendingAction.ID != wantID2)
	}, "answered ask "+wantID2+" to disappear from /api/state")
}

func TestFactoryAskTapOwnerProof(t *testing.T) {
	eng := newWebFakeFactory()
	h := newHarness(t, harnessOpts{factory: eng})
	startSupervisorRun(t, h)

	eng.openAsk(flow.Ask{
		ID:        "ask-demo-10",
		Project:   "demo",
		Kind:      flow.KindGate,
		Gated:     true,
		Question:  "Approve owner gate for T1?",
		Options:   []string{"Yes, merge", "No, hold"},
		Recommend: "Yes, merge",
		Task:      "T1",
		Head:      "def5678",
		At:        time.Now().UTC(),
	})

	const cardID = "fask-ask-demo-10"
	waitFor(t, 500*time.Millisecond, func() bool {
		var st StateResponse
		if h.getJSON("/api/state", &st) != http.StatusOK {
			return false
		}
		_, ok := findQuestion(st, cardID)
		return ok
	}, "open ask "+cardID+" to appear in /api/state")

	// 1. Ticketless loopback POST /api/pending/answer for a fask- card is refused 403 proof_required (KB-ASK-19).
	var errBody map[string]any
	code, raw := h.postJSON("/api/pending/answer", map[string]any{"id": cardID, "option": 1}, &errBody)
	if code != http.StatusForbidden || !strings.Contains(raw, "proof_required") {
		t.Fatalf("loopback ticketless tap = %d %s, want 403 proof_required", code, raw)
	}
	if a := eng.firstAsk(); a.Status != flow.AskStatusOpen {
		t.Fatalf("ticketless loopback tap answered ask: %+v", a)
	}

	// 2. Paired device tap with a session ticket answers the ask and unblocks it.
	dev := h.pairDevice()
	req := h.authedRequest(http.MethodPost, "/api/pending/answer", dev.ticket, nil, map[string]any{"id": cardID, "option": 1})
	var okBody map[string]any
	code, _ = h.do(req, &okBody)
	if code != http.StatusOK || okBody["ok"] != true {
		t.Fatalf("paired device tap = %d %v, want 200 ok", code, okBody)
	}

	a := eng.firstAsk()
	if a.Status != flow.AskStatusAnswered || a.Answer != "Yes, merge" || a.DeviceID != dev.id || a.ProofID == "" {
		t.Fatalf("ask after paired device tap = %+v, want answered by %s with proof", a, dev.id)
	}

	waitFor(t, 500*time.Millisecond, func() bool {
		var st StateResponse
		if h.getJSON("/api/state", &st) != http.StatusOK {
			return false
		}
		_, still := findQuestion(st, cardID)
		return !still && (st.PendingAction == nil || st.PendingAction.ID != cardID)
	}, "answered ask "+cardID+" to disappear from /api/state")
}

func TestFactoryWSNoProofPathPairedTapAnswers(t *testing.T) {
	eng := newWebFakeFactory()
	h := newHarness(t, harnessOpts{voice: true, factory: eng})
	startSupervisorRun(t, h)

	eng.openAsk(flow.Ask{
		ID:        "ask-demo-ws",
		Project:   "demo",
		Kind:      flow.KindRelease,
		Gated:     true,
		Question:  "Cut release v1.3 now?",
		Options:   []string{"Yes, cut v1.3", "Hold"},
		Recommend: "Yes, cut v1.3",
		Task:      "T1",
		At:        time.Now().UTC(),
	})

	const cardID = "fask-ask-demo-ws"
	waitFor(t, 500*time.Millisecond, func() bool {
		var st StateResponse
		if h.getJSON("/api/state", &st) != http.StatusOK {
			return false
		}
		_, ok := findQuestion(st, cardID)
		return ok
	}, "open ask "+cardID+" to appear in /api/state")

	// 1. A ticketless loopback /ws client streaming audio and speaking "yes"
	// cannot mint any AnswerProof or answer a fask- ask (no proof path from /ws).
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	c.sendAudio([]byte{1, 2, 3, 4})
	waitFor(t, time.Second, func() bool { return h.session.sentFrames() == 1 }, "loopback /ws audio reached session")

	h.session.emit(voice.EventTranscript{Dir: voice.DirIn, Text: "yes", Final: true})
	c.expect(wsproto.TypeTranscript)
	if _, err := h.sup.Handler()(context.Background(), voice.ToolCall{
		ID:   "ws-loopback-yes",
		Name: supervisor.ToolToSupervisor,
		Args: map[string]any{"text": "yes"},
	}); err != nil {
		t.Fatalf("to_supervisor from loopback /ws speech: %v", err)
	}
	if proofs := eng.mintedProofs(); len(proofs) != 0 {
		t.Fatalf("ticketless loopback /ws minted AnswerProof(s): %+v, want 0 proofs", proofs)
	}
	if a := eng.firstAsk(); a.Status != flow.AskStatusOpen {
		t.Fatalf("ticketless loopback /ws answered ask: %+v", a)
	}

	// Text sent over ticketless loopback /ws also never mints proof or answers.
	c.send(wsproto.SimulateText("yes"))
	waitFor(t, time.Second, func() bool { return len(h.session.sentTexts()) == 1 }, "loopback /ws text reached session")
	if _, err := h.sup.Handler()(context.Background(), voice.ToolCall{
		ID:   "ws-loopback-text",
		Name: supervisor.ToolToSupervisor,
		Args: map[string]any{"text": "yes"},
	}); err != nil {
		t.Fatalf("to_supervisor from loopback /ws text: %v", err)
	}
	if proofs := eng.mintedProofs(); len(proofs) != 0 || eng.firstAsk().Status != flow.AskStatusOpen {
		t.Fatalf("ticketless loopback /ws text minted proof or answered ask: proofs=%+v ask=%+v", eng.mintedProofs(), eng.firstAsk())
	}
	_ = c.conn.CloseNow()

	// Even a paired /ws client has no proof path in 6.3 (voice does not answer factory asks).
	dev := h.pairDevice()
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	wsURL := "ws" + strings.TrimPrefix(h.http.URL, "http") + "/ws"
	pairedConn, _, err := websocket.Dial(wsCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + ticketPrefix + dev.ticket}},
	})
	if err != nil {
		t.Fatalf("paired /ws dial: %v", err)
	}
	defer func() { _ = pairedConn.CloseNow() }()
	pairedWS := &wsClient{t: t, conn: pairedConn, ctx: wsCtx, skipBotReady: true}
	pairedWS.send(wsproto.ClientReady(wsproto.ProtocolVersion, testClient))
	pairedWS.expect(wsproto.TypeStatus)
	pairedWS.sendAudio([]byte{5, 6, 7, 8})
	waitFor(t, time.Second, func() bool { return h.session.sentFrames() >= 2 }, "paired /ws audio reached session")
	h.session.emit(voice.EventTranscript{Dir: voice.DirIn, Text: "yes", Final: true})
	pairedWS.expect(wsproto.TypeTranscript)
	if _, err := h.sup.Handler()(context.Background(), voice.ToolCall{
		ID:   "ws-paired-yes",
		Name: supervisor.ToolToSupervisor,
		Args: map[string]any{"text": "yes"},
	}); err != nil {
		t.Fatalf("to_supervisor from paired /ws speech: %v", err)
	}
	if proofs := eng.mintedProofs(); len(proofs) != 0 || eng.firstAsk().Status != flow.AskStatusOpen {
		t.Fatalf("/ws minted proof or answered ask when /ws must have no proof path: proofs=%+v ask=%+v", proofs, eng.firstAsk())
	}

	// 2. A paired device's HTTP tap with its session ticket still mints a tap proof and answers the ask.
	req := h.authedRequest(http.MethodPost, "/api/pending/answer", dev.ticket, nil, map[string]any{"id": cardID, "option": 1})
	var okBody map[string]any
	code, _ := h.do(req, &okBody)
	if code != http.StatusOK || okBody["ok"] != true {
		t.Fatalf("paired device tap = %d %v, want 200 ok", code, okBody)
	}
	proofs := eng.mintedProofs()
	if len(proofs) != 1 || proofs[0].InputKind != factory.ProofInputTap || proofs[0].DeviceID != dev.id {
		t.Fatalf("minted proofs after paired tap = %+v, want 1 tap proof for %s", proofs, dev.id)
	}
	a := eng.firstAsk()
	if a.Status != flow.AskStatusAnswered || a.Answer != "Yes, cut v1.3" || a.DeviceID != dev.id || a.ProofID != proofs[0].ID {
		t.Fatalf("ask after paired device tap = %+v, want answered by %s with proof %s", a, dev.id, proofs[0].ID)
	}
}
