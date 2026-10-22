package web

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/push"
)

// recPush wraps a real (disabled, in-memory) sender and records what the
// server asks of it.
type recPush struct {
	*push.Sender
	mu       sync.Mutex
	notified []push.Notification
	forgot   [][]string
}

func (p *recPush) Notify(n push.Notification) {
	p.mu.Lock()
	p.notified = append(p.notified, n)
	p.mu.Unlock()
}

func (p *recPush) ForgetDevices(ids []string) error {
	p.mu.Lock()
	p.forgot = append(p.forgot, ids)
	p.mu.Unlock()
	return p.Sender.ForgetDevices(ids)
}

// A registration carries the device it came from; a new device's pairing
// pushes every other device; revoking a device drops its registration.
func TestPushIsTiedToPairedDevices(t *testing.T) {
	sender, err := push.New(push.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recPush{Sender: sender}
	h := newHarness(t, harnessOpts{push: rec})
	first := h.pairDevice()
	h.asRemote()
	tok := strings.Repeat("0a", 32)
	if code, body := h.do(h.authedRequest(http.MethodPost, "/api/push/register", first.ticket, nil, map[string]string{"token": tok, "environment": "sandbox", "device": "iPhone"}), nil); code != http.StatusOK {
		t.Fatalf("register = %d %s", code, body)
	}
	if d := sender.Devices(); len(d) != 1 || d[0].DeviceID != first.id {
		t.Fatalf("registered = %+v, want device %s", d, first.id)
	}

	second := h.pairDevice()
	rec.mu.Lock()
	var paired []push.Notification
	for _, n := range rec.notified {
		if n.Kind == push.KindDevicePaired {
			paired = append(paired, n)
		}
	}
	rec.mu.Unlock()
	if len(paired) != 2 {
		t.Fatalf("device_paired pushes = %+v, want one per pairing", paired)
	}
	last := paired[1]
	if last.ExceptDevice != second.id || !last.NeedsYou || !strings.HasPrefix(last.Text, "New device paired to ") || !strings.HasSuffix(last.Text, "Not you? Open the computer's setup page to revoke.") {
		t.Errorf("push = %+v", last)
	}

	if _, err := h.srv.revokeDevices([]string{first.id}, "test"); err != nil {
		t.Fatal(err)
	}
	if d := sender.Devices(); len(d) != 0 {
		t.Errorf("after revoke: registrations = %+v", d)
	}
}

func TestDevicePairedText(t *testing.T) {
	if got := devicePairedText("Studio"); got != "New device paired to Studio. Not you? Open the computer's setup page to revoke." {
		t.Errorf("text = %q", got)
	}
	if got := devicePairedText(""); !strings.Contains(got, "your computer") {
		t.Errorf("no name: %q", got)
	}
}
