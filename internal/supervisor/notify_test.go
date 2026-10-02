package supervisor

import (
	"strings"
	"sync"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
)

// fakeNotifier records notifications.
type fakeNotifier struct {
	mu sync.Mutex
	ns []push.Notification
}

func (f *fakeNotifier) Notify(n push.Notification) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ns = append(f.ns, n)
}

func (f *fakeNotifier) all() []push.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]push.Notification(nil), f.ns...)
}

func withNotifier(n Notifier) func(*Config) { return func(c *Config) { c.Notifier = n } }

// A block with an approval prompt notifies once, with the spoken
// sentence, the pending id and its options — at quiet, and with no voice
// session at all (nil pusher: the app is the only listener).
func TestNotifyApprovalWithoutVoice(t *testing.T) {
	fn := &fakeNotifier{}
	h := newHarness(t, withNotifier(fn), func(c *Config) { c.Chattiness = string(Quiet) })
	h.sup.pusher = nil
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	got := fn.all()
	if len(got) != 1 {
		t.Fatalf("notifications = %+v", got)
	}
	n := got[0]
	p, ok := h.pending()
	if !ok || n.Kind != push.KindApproval || n.PendingID != p.ID || !n.NeedsYou || n.PaneID != "w5:p1" || n.Agent != "auth" {
		t.Errorf("notification = %+v (pending %+v)", n, p)
	}
	if !strings.HasPrefix(n.Text, "auth asks to run") {
		t.Errorf("text = %q, want the spoken sentence", n.Text)
	}
	if len(n.Options) == 0 || len(n.Options) != len(p.Options) || n.Options[0].Index != 1 || n.Options[0].Label != p.Options[0].Label {
		t.Errorf("options = %+v, pending options %+v", n.Options, p.Options)
	}
	if h.log.count("supervisor.push_skipped") != 1 {
		t.Error("the voice line should still be logged as skipped")
	}
}

// A block with nothing answerable notifies as blocked; a plain finish
// nobody asked about does not notify.
func TestNotifyBlockedNotPlainFinish(t *testing.T) {
	fn := &fakeNotifier{}
	h := newHarness(t, withNotifier(fn), func(c *Config) { c.Chattiness = string(Chatty) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	doneAuth(h)
	if got := fn.all(); len(got) != 0 {
		t.Fatalf("plain finish notified: %+v", got)
	}
	h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	h.block("w5:p1", "codex-after-ctrl-c.screen.txt")
	got := fn.all()
	if len(got) != 1 || got[0].Kind != push.KindBlocked || got[0].PendingID != "" || !got[0].NeedsYou || len(got[0].Options) != 0 {
		t.Errorf("notifications = %+v", got)
	}
}

// A kept promise ("tell me when it's done") notifies with its line, not
// as needs-you.
func TestNotifyKeptPromise(t *testing.T) {
	fn := &fakeNotifier{}
	h := newHarness(t, withNotifier(fn), func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
		t.Fatal(err)
	}
	doneAuth(h)
	got := fn.all()
	if len(got) != 1 || got[0].Kind != push.KindDone || got[0].NeedsYou || got[0].Text != "As you asked: "+doneSentence {
		t.Errorf("notifications = %+v", got)
	}
}

// With the brain, the notification carries its line and the event id
// (the sender's dedupe key); a brain that skips a finish sends nothing.
func TestNotifyBrainLineCarriesEventID(t *testing.T) {
	fn := &fakeNotifier{}
	m := braintest.NewModel()
	m.EventDefault = announceReply("Auth wants to run the tests. Yes or no?", "interrupt", "", "", true)
	h := newBrainHarness(t, m, withNotifier(fn))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	got := fn.all()
	if len(got) != 1 || got[0].Text != "Auth wants to run the tests. Yes or no?" || got[0].EventID == "" || got[0].Kind != push.KindApproval {
		t.Fatalf("notifications = %+v", got)
	}
}
