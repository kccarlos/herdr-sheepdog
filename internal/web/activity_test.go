package web

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// actPush records the Live Activity updates the server asks for.
type actPush struct {
	*push.Sender
	mu      sync.Mutex
	updates []push.ActivityUpdate
}

func (p *actPush) UpdateActivity(_ context.Context, _ string, u push.ActivityUpdate) error {
	p.mu.Lock()
	p.updates = append(p.updates, u)
	p.mu.Unlock()
	return nil
}

func (p *actPush) sent() []push.ActivityUpdate {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]push.ActivityUpdate(nil), p.updates...)
}

func addAgent(t *testing.T, h *harness, pane, kind string, to herdr.State, at time.Time) {
	t.Helper()
	if _, _, err := h.srv.deps.Store.Apply(herdr.Transition{PaneID: pane, Agent: kind, To: to, At: at}); err != nil {
		t.Fatal(err)
	}
}

// The banner's content with the app away: the waiting agents in the
// questions' order, then by how long they waited, their questions, and
// the counts.
func TestActivityState(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	t0 := time.Now().Add(-time.Hour)
	addAgent(t, h, "w1:p1", "claude", herdr.StateBlocked, t0.Add(3*time.Minute))
	addAgent(t, h, "w2:p1", "codex", herdr.StateBlocked, t0.Add(1*time.Minute))
	addAgent(t, h, "w3:p1", "claude", herdr.StateBlocked, t0.Add(2*time.Minute))
	addAgent(t, h, "w4:p1", "claude", herdr.StateWorking, t0)
	addAgent(t, h, "w5:p1", "claude", herdr.StateDone, t0)
	addAgent(t, h, "w6:p1", "claude", herdr.StateIdle, t0)
	if err := h.store.SetQuestion(state.PendingAction{ID: "q1", PaneID: "w1:p1", Kind: "answer_prompt", Question: "Run   npm test\nin webapp?", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Update("w2:p1", func(a *state.Agent) { a.LastBlock = &state.Block{Question: "Which branch?"} }); err != nil {
		t.Fatal(err)
	}
	st := h.srv.activityState()
	if st.Phase != push.PhaseAway || st.Waiting != 3 || st.Working != 1 || st.Done != 1 {
		t.Fatalf("state = %+v", st)
	}
	var panes, questions []string
	for _, r := range st.Rows {
		panes = append(panes, r.Pane)
		questions = append(questions, r.Question)
		if r.Agent == "" {
			t.Errorf("row without a name: %+v", r)
		}
	}
	if strings.Join(panes, ",") != "w1:p1,w2:p1,w3:p1" {
		t.Errorf("order = %v", panes)
	}
	if strings.Join(questions, "|") != "Run npm test in webapp?|Which branch?|Needs you" {
		t.Errorf("questions = %q", questions)
	}
}

func TestClipLine(t *testing.T) {
	if got := clipLine("  a \n b  ", 10); got != "a b" {
		t.Errorf("fold = %q", got)
	}
	got := clipLine(strings.Repeat("é", 100), activityQuestionLimit)
	if n := len([]rune(got)); n != activityQuestionLimit || !strings.HasSuffix(got, "…") {
		t.Errorf("cut = %d runes %q", n, got)
	}
}

// The daemon pushes only for a device that has been away past the grace
// time, and only when something changed; a new waiting agent goes urgent.
func TestActivityStepPushesChangesWhileAway(t *testing.T) {
	sender, err := push.New(push.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &actPush{Sender: sender}
	h := newHarness(t, harnessOpts{push: rec})
	dev := h.pairDevice()
	h.asRemote()
	tok := strings.Repeat("0b", 32)
	if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/activity", dev.ticket, nil, map[string]string{"token": tok, "environment": "sandbox"}), nil); code != http.StatusOK {
		t.Fatalf("register = %d", code)
	}
	acts := sender.Activities()
	if len(acts) != 1 || acts[0].DeviceID != dev.id {
		t.Fatalf("activities = %+v", acts)
	}
	now := acts[0].At
	h.srv.deps.Now = func() time.Time { return now }
	ctx := context.Background()
	addAgent(t, h, "w1:p1", "claude", herdr.StateWorking, now)

	// Within the grace time: nothing.
	h.srv.activityStep(ctx)
	if n := len(rec.sent()); n != 0 {
		t.Fatalf("pushed within grace: %d", n)
	}
	// Away past it: the first push.
	now = now.Add(activityGrace + time.Second)
	h.srv.activityStep(ctx)
	if s := rec.sent(); len(s) != 1 || s[0].State.Working != 1 || s[0].Urgent || s[0].Stale.IsZero() {
		t.Fatalf("first push = %+v", s)
	}
	// Nothing changed: no push.
	now = now.Add(activityTick)
	h.srv.activityStep(ctx)
	if n := len(rec.sent()); n != 1 {
		t.Fatalf("pushed without a change: %d", n)
	}
	// An agent starts waiting: an urgent push.
	addAgent(t, h, "w2:p1", "codex", herdr.StateBlocked, now)
	h.srv.activityStep(ctx)
	if s := rec.sent(); len(s) != 2 || !s[1].Urgent || s[1].State.Waiting != 1 || len(s[1].State.Rows) != 1 {
		t.Fatalf("second push = %+v", s)
	}
	// Ending the activity forgets it.
	if code, _ := h.do(h.authedRequest(http.MethodDelete, "/api/push/activity", dev.ticket, nil, nil), nil); code != http.StatusOK {
		t.Fatalf("end = %d", code)
	}
	if len(sender.Activities()) != 0 {
		t.Error("activity kept after DELETE")
	}
}

// An activity iOS has surely ended (8 hours) is forgotten.
func TestActivityStepForgetsOldActivities(t *testing.T) {
	sender, _ := push.New(push.Config{}, nil)
	rec := &actPush{Sender: sender}
	h := newHarness(t, harnessOpts{push: rec})
	if err := sender.RegisterActivity("dev-x", strings.Repeat("0c", 32), "production"); err != nil {
		t.Fatal(err)
	}
	at := sender.Activities()[0].At
	h.srv.deps.Now = func() time.Time { return at.Add(push.ActivityLifetime + time.Minute) }
	h.srv.activityStep(context.Background())
	if len(sender.Activities()) != 0 || len(rec.sent()) != 0 {
		t.Errorf("activities = %+v, sent = %d", sender.Activities(), len(rec.sent()))
	}
}

// A Live Activity needs a paired device: loopback has none.
func TestPushActivityNeedsADevice(t *testing.T) {
	sender, _ := push.New(push.Config{}, nil)
	h := newHarness(t, harnessOpts{push: &actPush{Sender: sender}})
	code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/activity", "", nil, map[string]string{"token": strings.Repeat("0d", 32), "environment": "sandbox"}), nil)
	if code != http.StatusBadRequest {
		t.Errorf("loopback register = %d", code)
	}
}
