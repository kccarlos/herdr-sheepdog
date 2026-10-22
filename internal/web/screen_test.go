package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// screenEnds returns the payloads of the web.screen.end events, with the
// pane under "pane".
func screenEnds(t *testing.T, h *harness) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "web.screen.end" {
			continue
		}
		var pl map[string]any
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		pl["pane"] = ev.PaneID
		out = append(out, pl)
	}
	return out
}

// waitScreenEnd waits for the n-th web.screen.end and returns it.
func waitScreenEnd(t *testing.T, h *harness, n int) map[string]any {
	t.Helper()
	waitFor(t, 3*time.Second, func() bool { return len(screenEnds(t, h)) >= n }, "web.screen.end")
	return screenEnds(t, h)[n-1]
}

// readyClient dials /ws and completes the handshake; it returns the
// bot-ready features too.
func readyClient(t *testing.T, h *harness) (*wsClient, []string) {
	t.Helper()
	c := dialWSRaw(t, h)
	c.expect(wsproto.TypeStatus)
	c.send(wsproto.ClientReady(1, iosClient))
	return c, c.expect(wsproto.TypeBotReady).Features
}

// expectNoScreen fails if a screen frame arrives within d: it waits d,
// then pings and reads up to the pong (a read deadline would close the
// connection).
func (c *wsClient) expectNoScreen(d time.Duration) {
	c.t.Helper()
	time.Sleep(d)
	c.send(wsproto.Ping())
	for {
		m, _, err := c.read(3 * time.Second)
		if err != nil {
			c.t.Fatalf("waiting for pong: %v", err)
		}
		if m.Type == wsproto.TypeScreen {
			c.t.Fatalf("unexpected screen frame: %+v", m.Screen)
		}
		if m.Type == wsproto.TypePong {
			return
		}
	}
}

// injectScreen changes the tracked fake:1 pane's screen.
func injectScreen(t *testing.T, h *harness, screen string) {
	const pane = "fake:1"
	t.Helper()
	if code, body := h.postJSON("/api/inject/screen", map[string]any{"pane_id": pane, "screen": screen}, nil); code != http.StatusOK {
		t.Fatalf("inject screen = %d %s", code, body)
	}
}

// The daemon advertises the agent window over /ws; a subscription gets
// the window at once with the REST route's fields, nothing while it does
// not change, and a frame when it does.
func TestWSScreenStream(t *testing.T) {
	h := newHarness(t, harnessOpts{screenInterval: 50 * time.Millisecond, screenMinGap: 10 * time.Millisecond})
	trackWorking(t, h, "fake:1", "auth", "line one\n\x1b[1;32mgreen\x1b[0m line two")
	c, features := readyClient(t, h)
	if !slices.Contains(features, wsproto.FeatureScreen) {
		t.Fatalf("features %v lack %s", features, wsproto.FeatureScreen)
	}

	c.send(wsproto.ScreenSubscribe("fake:1", 50))
	m := c.expect(wsproto.TypeScreen)
	s := m.Screen
	if s.PaneID != "fake:1" || s.Alias != "auth" || s.Spoken == "" || s.Kind != "codex" || s.State != "working" || s.StateSince == "" || s.Lines != 50 || s.Hash == "" {
		t.Fatalf("screen = %+v", s)
	}
	if !strings.Contains(s.Text, "green line two") || strings.Contains(s.Text, "\x1b") {
		t.Fatalf("text = %q", s.Text)
	}
	// The same window as the REST route.
	var rest ScreenResponse
	h.getJSON(windowPath("fake:1", "screen")+"?lines=50", &rest)
	if rest.Hash != s.Hash || rest.Text != s.Text {
		t.Fatalf("rest %+v != ws %+v", rest, s)
	}

	// Unchanged: several reads, no frame.
	c.expectNoScreen(250 * time.Millisecond)

	injectScreen(t, h, "line three")
	m = c.expect(wsproto.TypeScreen)
	if m.Screen.Text != "line three" || m.Screen.Hash == s.Hash {
		t.Fatalf("changed screen = %+v", m.Screen)
	}

	c.send(wsproto.ScreenUnsubscribe())
	end := waitScreenEnd(t, h, 1)
	if end["reason"] != "unsubscribe" || end["pane"] != "fake:1" || end["frames"] != float64(2) {
		t.Fatalf("end = %v", end)
	}
	injectScreen(t, h, "line four")
	c.expectNoScreen(200 * time.Millisecond)
	if !subsequence(h.types(), []string{"web.screen.subscribe", "web.screen.end"}) {
		t.Fatalf("events = %v", h.types())
	}
}

// A state change the watcher reports (herdr.transition) is pushed at once,
// long before the next periodic read; reads stay at least ScreenMinGap
// apart however many transitions arrive.
func TestWSScreenTransitionKicksAndRateCap(t *testing.T) {
	h := newHarness(t, harnessOpts{screenInterval: time.Hour, screenMinGap: 150 * time.Millisecond})
	trackWorking(t, h, "fake:1", "auth", "busy")
	c, _ := readyClient(t, h)
	c.send(wsproto.ScreenSubscribe("fake:1", 0))
	if m := c.expect(wsproto.TypeScreen); m.Screen.State != "working" || m.Screen.Lines != 120 {
		t.Fatalf("first = %+v", m.Screen)
	}
	start := time.Now()
	if code, body := h.postJSON("/api/inject/state", map[string]any{"pane_id": "fake:1", "state": "idle"}, nil); code != http.StatusOK {
		t.Fatalf("inject state = %d %s", code, body)
	}
	if m := c.expect(wsproto.TypeScreen); m.Screen.State != "idle" {
		t.Fatalf("after transition = %+v", m.Screen)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("transition frame took %v", d)
	}

	// A burst of transitions: one read per min gap, not one per event.
	burst := time.Now()
	for i := 0; i < 10; i++ {
		st := "working"
		if i%2 == 1 {
			st = "idle"
		}
		h.postJSON("/api/inject/state", map[string]any{"pane_id": "fake:1", "state": st}, nil)
	}
	time.Sleep(400 * time.Millisecond)
	c.send(wsproto.ScreenUnsubscribe())
	end := waitScreenEnd(t, h, 1)
	elapsed := time.Since(burst)
	maxReads := 2 + int(elapsed/(150*time.Millisecond)) + 1
	if r := int(end["reads"].(float64)); r > maxReads || r < 3 {
		t.Fatalf("reads = %d in %v (max %d)", r, elapsed, maxReads)
	}
}

// An untracked pane, or an agent that goes away, ends the subscription
// with a screen-error the client can show.
func TestWSScreenGone(t *testing.T) {
	h := newHarness(t, harnessOpts{screenInterval: 30 * time.Millisecond})
	c, _ := readyClient(t, h)
	c.send(wsproto.ScreenSubscribe("other:9", 0))
	m := c.expect(wsproto.TypeScreenError)
	if m.PaneID != "other:9" || m.Message != "no such agent" || !m.Ended {
		t.Fatalf("screen-error = %+v", m)
	}
	if end := waitScreenEnd(t, h, 1); end["reason"] != "gone" {
		t.Fatalf("end = %v", end)
	}

	trackWorking(t, h, "fake:1", "auth", "hi")
	c.send(wsproto.ScreenSubscribe("fake:1", 0))
	c.expect(wsproto.TypeScreen)
	if code, body := h.postJSON("/api/inject/remove", map[string]any{"pane_id": "fake:1"}, nil); code != http.StatusOK {
		t.Fatalf("remove = %d %s", code, body)
	}
	if m := c.expect(wsproto.TypeScreenError); m.PaneID != "fake:1" || !m.Ended {
		t.Fatalf("screen-error = %+v", m)
	}
	if end := waitScreenEnd(t, h, 2); end["reason"] != "gone" {
		t.Fatalf("end = %v", end)
	}
}

// One subscription per client: a second subscribe replaces the first;
// the connection ending ends the subscription.
func TestWSScreenOnePerClientAndDisconnect(t *testing.T) {
	h := newHarness(t, harnessOpts{screenInterval: 30 * time.Millisecond})
	trackWorking(t, h, "fake:1", "auth", "one")
	trackWorking(t, h, "fake:2", "db", "two")
	c, _ := readyClient(t, h)
	c.send(wsproto.ScreenSubscribe("fake:1", 0))
	c.expect(wsproto.TypeScreen)
	c.send(wsproto.ScreenSubscribe("fake:2", 0))
	if end := waitScreenEnd(t, h, 1); end["reason"] != "resubscribe" || end["pane"] != "fake:1" {
		t.Fatalf("end = %v", end)
	}
	if m := c.expect(wsproto.TypeScreen); m.Screen.PaneID != "fake:2" {
		t.Fatalf("screen = %+v", m.Screen)
	}
	// Only fake:2 is read from now on.
	injectScreen(t, h, "one changed")
	c.expectNoScreen(150 * time.Millisecond)

	_ = c.conn.CloseNow()
	if end := waitScreenEnd(t, h, 2); end["reason"] != "disconnect" || end["pane"] != "fake:2" {
		t.Fatalf("end = %v", end)
	}
}

// A replacing client does not inherit the old client's subscription.
func TestWSScreenNotInheritedByReplacement(t *testing.T) {
	h := newHarness(t, harnessOpts{screenInterval: 30 * time.Millisecond})
	trackWorking(t, h, "fake:1", "auth", "one")
	a, _ := readyClient(t, h)
	a.send(wsproto.ScreenSubscribe("fake:1", 0))
	a.expect(wsproto.TypeScreen)
	b, _ := readyClient(t, h)
	if end := waitScreenEnd(t, h, 1); end["reason"] != "closed" {
		t.Fatalf("end = %v", end)
	}
	injectScreen(t, h, "changed")
	b.expectNoScreen(150 * time.Millisecond)
}

// Feature screen-ansi: ?ansi=1 and screen-subscribe {ansi:true} keep the
// colours (the app's snapshot window when the live terminal is not
// available); the plain route still strips them.
func TestScreenANSI(t *testing.T) {
	h := newHarness(t, harnessOpts{screenInterval: 50 * time.Millisecond, screenMinGap: 10 * time.Millisecond})
	trackWorking(t, h, "fake:1", "auth", "line one\n\x1b[1;32mgreen\x1b[0m line two")
	var rest ScreenResponse
	if code := h.getJSON(windowPath("fake:1", "screen")+"?ansi=1", &rest); code != http.StatusOK {
		t.Fatalf("ansi screen = %d", code)
	}
	if !rest.ANSI || !strings.Contains(rest.Text, "\x1b[1;32mgreen\x1b[0m line two") || rest.Lines != 2 {
		t.Fatalf("ansi screen = %+v", rest)
	}
	var plain ScreenResponse
	h.getJSON(windowPath("fake:1", "screen"), &plain)
	if plain.ANSI || strings.Contains(plain.Text, "\x1b") || plain.Hash == rest.Hash {
		t.Fatalf("plain screen = %+v", plain)
	}

	c, features := readyClient(t, h)
	if !slices.Contains(features, wsproto.FeatureScreenANSI) {
		t.Fatalf("features %v lack %s", features, wsproto.FeatureScreenANSI)
	}
	on := true
	m := wsproto.ScreenSubscribe("fake:1", 0)
	m.ANSI = &on
	c.send(m)
	got := c.expect(wsproto.TypeScreen).Screen
	if !got.ANSI || got.Text != rest.Text || got.Hash != rest.Hash {
		t.Fatalf("ws ansi frame = %+v", got)
	}
}
