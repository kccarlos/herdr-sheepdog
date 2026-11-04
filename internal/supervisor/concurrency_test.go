package supervisor

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

type interceptHerdr struct {
	Herdr
	onPrompt    func(ctx context.Context, paneID, text string) (herdr.AgentInfo, error)
	onSendKeys  func(ctx context.Context, paneID string, keys ...string) error
	onRead      func(ctx context.Context, paneID string, source herdr.ReadSource, lines int) (herdr.ReadResult, error)
	onInterrupt func(ctx context.Context, paneID string) error
}

func (h *interceptHerdr) Prompt(ctx context.Context, paneID, text string) (herdr.AgentInfo, error) {
	if h.onPrompt != nil {
		return h.onPrompt(ctx, paneID, text)
	}
	return h.Herdr.Prompt(ctx, paneID, text)
}

func (h *interceptHerdr) SendKeys(ctx context.Context, paneID string, keys ...string) error {
	if h.onSendKeys != nil {
		return h.onSendKeys(ctx, paneID, keys...)
	}
	return h.Herdr.SendKeys(ctx, paneID, keys...)
}

func (h *interceptHerdr) Read(ctx context.Context, paneID string, source herdr.ReadSource, lines int) (herdr.ReadResult, error) {
	if h.onRead != nil {
		return h.onRead(ctx, paneID, source, lines)
	}
	return h.Herdr.Read(ctx, paneID, source, lines)
}

func (h *interceptHerdr) Interrupt(ctx context.Context, paneID string) error {
	if h.onInterrupt != nil {
		return h.onInterrupt(ctx, paneID)
	}
	return h.Herdr.Interrupt(ctx, paneID)
}

func TestPromptDoesNotBlockStopOrVoiceEvents(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "deploy production"})}},
		braintest.Step{Text: braintest.Reply("Sent to auth.", "")},
	)
	h := newBrainHarness(t, m, func(c *Config) { c.ReplyWait = -1 })
	h.addAgent("w5:p1", "auth", "claude", herdr.StateWorking)

	inPrompt := make(chan struct{})
	promptCtxCanceled := make(chan struct{})
	unblock := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	})

	base := h.sup.herdr
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onPrompt: func(ctx context.Context, paneID, text string) (herdr.AgentInfo, error) {
			close(inPrompt)
			select {
			case <-ctx.Done():
				close(promptCtxCanceled)
				return herdr.AgentInfo{}, ctx.Err()
			case <-unblock:
				return herdr.AgentInfo{}, errors.New("prompt timed out")
			}
		},
	}

	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		h.say("tell auth to deploy production")
	}()

	select {
	case <-inPrompt:
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt was not called")
	}

	// While Prompt is blocked on socket I/O, OnVoiceEvent must not stall on s.mu.
	evDone := make(chan struct{})
	go func() {
		h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Stop!", Final: true})
		close(evDone)
	}()
	select {
	case <-evDone:
	case <-time.After(150 * time.Millisecond):
		close(unblock)
		t.Fatal("OnVoiceEvent blocked on s.mu while herdr.Prompt was in flight")
	}

	// Stop must immediately cancel the in-flight utterance context without blocking on s.mu,
	// and must not send Ctrl-C to the already-working agent since Prompt failed/aborted.
	stopDone := make(chan voice.ToolResult, 1)
	go func() {
		res, _ := h.sup.Stop(context.Background(), StopTriggerTool)
		stopDone <- res
	}()

	var stopRes voice.ToolResult
	select {
	case stopRes = <-stopDone:
	case <-time.After(150 * time.Millisecond):
		close(unblock)
		t.Fatal("Stop blocked on s.mu while herdr.Prompt was in flight")
	}

	select {
	case <-promptCtxCanceled:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("Stop did not cancel in-flight Prompt context")
	}

	<-turnDone

	if !strings.Contains(stopRes.Spoken, "withdrawn") {
		t.Errorf("stop result = %q, want withdrawn", stopRes.Spoken)
	}
	if got := len(h.srv.RequestsFor("pane.send_keys")); got != 0 {
		t.Errorf("interrupts sent = %d, want 0 since Prompt was aborted before sending", got)
	}
	if p, ok := h.pending(); ok {
		t.Errorf("unexpected leftover pending action: %+v", p)
	}
}

func TestSendKeysAndReviveReadDoNotHoldMu(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}},
		braintest.Step{Text: braintest.Reply("Approved auth.", "")},
	)
	h := newBrainHarness(t, m, func(c *Config) {
		c.ReplyWait = -1
		c.bareAnswers = false
	})
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.block("w6:p1", "codex-approval-prompt.screen.txt")
	// Clear the pending slot so AnswerPrompt goes through revivePrompt (Read) + answerKeys (SendKeys).
	if err := h.store.ClearPending(); err != nil {
		t.Fatal(err)
	}

	inReviveRead := make(chan struct{})
	releaseRead := make(chan struct{})
	inSendKeys := make(chan struct{})
	releaseKeys := make(chan struct{})

	base := h.sup.herdr
	var sawRevive bool
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onRead: func(ctx context.Context, paneID string, source herdr.ReadSource, lines int) (herdr.ReadResult, error) {
			if paneID == "w5:p1" && !sawRevive {
				sawRevive = true
				close(inReviveRead)
				<-releaseRead
			}
			return base.Read(ctx, paneID, source, lines)
		},
		onSendKeys: func(ctx context.Context, paneID string, keys ...string) error {
			if paneID == "w5:p1" {
				close(inSendKeys)
				<-releaseKeys
			}
			return base.SendKeys(ctx, paneID, keys...)
		},
	}

	turnDone := make(chan voice.ToolResult, 1)
	go func() {
		turnDone <- h.say("yes")
	}()

	select {
	case <-inReviveRead:
	case <-time.After(2 * time.Second):
		t.Fatal("revivePrompt Read was not called")
	}

	ev1 := make(chan struct{})
	go func() {
		h.sup.OnVoiceEvent(voice.EventTurnComplete{})
		close(ev1)
	}()
	select {
	case <-ev1:
	case <-time.After(150 * time.Millisecond):
		close(releaseRead)
		close(releaseKeys)
		t.Fatal("OnVoiceEvent blocked on s.mu during revivePrompt Read")
	}
	close(releaseRead)

	select {
	case <-inSendKeys:
	case <-time.After(2 * time.Second):
		close(releaseKeys)
		t.Fatal("answerKeys SendKeys was not called")
	}

	ev2 := make(chan struct{})
	go func() {
		h.sup.OnVoiceEvent(voice.EventTurnComplete{})
		close(ev2)
	}()
	select {
	case <-ev2:
	case <-time.After(150 * time.Millisecond):
		close(releaseKeys)
		t.Fatal("OnVoiceEvent blocked on s.mu during answerKeys SendKeys")
	}

	// While SendKeys is in flight on w5:p1 without s.mu, the approval input must already be marked
	// used so a concurrent AnswerPrompt on w6:p1 cannot spend the same "yes".
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), "w6:p1", []string{"y"}); !errors.Is(err, brain.ErrNotPermitted) {
		t.Errorf("concurrent AnswerPrompt on second pane = %v, want ErrNotPermitted", err)
	}
	close(releaseKeys)

	res := <-turnDone
	if !strings.Contains(res.Spoken, "Approved") {
		t.Errorf("spoken = %q", res.Spoken)
	}
}

func TestRelayStopRestAfterShutdown(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Auth is working.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Stop. What is auth doing?", Final: true})

	// App.Run shuts down Supervisor (and closes Brain) before voice.Close finishes draining tool calls.
	h.sup.shutdown()
	if err := h.sup.Brain().Close(); err != nil {
		t.Fatal(err)
	}

	_, err := h.sup.Stop(context.Background(), StopTriggerTool)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Wait briefly to catch any untracked/post-shutdown goroutine spawned by relayStopRest.
	time.Sleep(50 * time.Millisecond)
	if got := h.log.count("supervisor.stop_rest"); got != 0 {
		t.Fatalf("relayStopRest spawned %d turn(s) after shutdown, want 0", got)
	}
}

func TestAnnounceHeldTrackedByShutdown(t *testing.T) {
	inAnnounce := make(chan struct{})
	announceCanceled := make(chan struct{})
	unblock := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	})

	m := braintest.NewModel()
	w := newMuteWorld(t, m, Chatty)
	until := w.clock.Now().Add(time.Hour)
	if _, err := w.mutes.Set(w.path, &until, ViaPhone); err != nil {
		t.Fatal(err)
	}
	w.sup.WarmFolders(context.Background())
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	if got := len(w.sup.heldQuestions()); got != 1 {
		t.Fatalf("held questions = %d, want 1", got)
	}

	var turnStillRunning atomic.Bool
	m.EventDefault = &braintest.Step{
		FnCtx: func(ctx context.Context, _ braintest.Call) (*genai.GenerateContentResponse, error) {
			turnStillRunning.Store(true)
			defer turnStillRunning.Store(false)
			close(inAnnounce)
			select {
			case <-ctx.Done():
				close(announceCanceled)
				return nil, ctx.Err()
			case <-unblock:
				return nil, errors.New("unblocked by test")
			}
		},
	}

	if _, err := w.sup.SetSpaceMute(context.Background(), "auth", false, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}

	select {
	case <-inAnnounce:
	case <-time.After(2 * time.Second):
		t.Fatal("announceHeld did not start announce turn")
	}

	shutDone := make(chan struct{})
	go func() {
		w.sup.shutdown()
		close(shutDone)
	}()

	select {
	case <-announceCanceled:
	case <-time.After(150 * time.Millisecond):
		close(unblock)
		<-shutDone
		t.Fatal("shutdown did not cancel announceHeld turn context via s.life")
	}

	select {
	case <-shutDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("shutdown did not finish after announceHeld cancelled")
	}

	if turnStillRunning.Load() {
		t.Fatal("shutdown returned while announceHeld turn was still running")
	}
}

func TestPollRepromptStopsOnShutdown(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.RepromptPoll = 2 * time.Second
		c.RepromptPollInterval = 15 * time.Millisecond
	})
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")

	sawPollRead := make(chan struct{})
	var shutReturned atomic.Bool
	var postShutdownReads atomic.Int32

	base := h.sup.herdr
	var pollReads atomic.Int32
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onRead: func(ctx context.Context, paneID string, source herdr.ReadSource, lines int) (herdr.ReadResult, error) {
			if shutReturned.Load() {
				postShutdownReads.Add(1)
			}
			if pollReads.Add(1) == 1 {
				close(sawPollRead)
			}
			return base.Read(ctx, paneID, source, lines)
		},
	}

	h.say("yes")

	select {
	case <-sawPollRead:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pollReprompt did not read pane")
	}

	h.sup.shutdown()
	shutReturned.Store(true)

	time.Sleep(60 * time.Millisecond)
	if got := postShutdownReads.Load(); got != 0 {
		t.Fatalf("pollReprompt performed %d pane read(s) after shutdown returned, want 0", got)
	}
}


