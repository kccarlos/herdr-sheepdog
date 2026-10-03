package voiceclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/pcm"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// fakeDaemon is a /ws + /events server that records what the client
// sends and lets the test push frames.
type fakeDaemon struct {
	t    *testing.T
	srv  *httptest.Server
	mu   sync.Mutex
	got  []wsproto.Message
	bin  int // binary frames received
	conn *websocket.Conn
	up   chan struct{}
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	f := &fakeDaemon{t: t, up: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "id: 1\nevent: log\ndata: {\"id\":1,\"ts\":%q,\"type\":\"daemon.start\",\"payload\":{}}\n\n", time.Now().UTC().Format(time.RFC3339Nano))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(1 << 22)
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		f.send(wsproto.Status("connecting", false, ""))
		close(f.up)
		for {
			typ, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			f.mu.Lock()
			if typ == websocket.MessageBinary {
				f.bin++
			} else if m, err := wsproto.Parse(data); err == nil {
				f.got = append(f.got, m)
				if m.Type == wsproto.TypeClientReady {
					go func() {
						f.send(wsproto.BotReady(1, 1, "test", []string{"playback", "speaking"}))
						f.send(wsproto.Status("connected", false, ""))
					}()
				}
			}
			f.mu.Unlock()
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDaemon) send(m wsproto.Message) {
	b, _ := m.Encode()
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	_ = c.Write(context.Background(), websocket.MessageText, b)
}

func (f *fakeDaemon) audio(d time.Duration) {
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	for sent := time.Duration(0); sent < d; sent += 100 * time.Millisecond {
		s := make([]int16, OutRate/10)
		for i := range s {
			s[i] = 3000
		}
		_ = c.Write(context.Background(), websocket.MessageBinary, pcm.ToBytes(s))
	}
}

func (f *fakeDaemon) messages(typ string) []wsproto.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []wsproto.Message
	for _, m := range f.got {
		if m.Type == typ {
			out = append(out, m)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestClientHandshakePlaybackAndBargeIn(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time client test")
	}
	f := newFakeDaemon(t)
	ctx := context.Background()
	c, err := Dial(ctx, Options{BaseURL: f.srv.URL, LocalBargeIn: true, Grace: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WaitConnected(ctx, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	<-f.up
	f.mu.Lock()
	first := f.got[0]
	f.mu.Unlock()
	if first.Type != wsproto.TypeClientReady || first.Client == nil || first.Client.Name != "sheepdog-bench" {
		t.Fatalf("first frame = %+v, want client-ready", first)
	}

	// A 500 ms reply plays through: started, then drained with ≈500 ms.
	f.send(wsproto.Transcript(wsproto.DirOut, "All tests pass.", false))
	f.audio(500 * time.Millisecond)
	u, ok := c.NextUtterance(ctx, 3*time.Second)
	if !ok || u.End != EndDrained || u.Heard != "All tests pass." || u.AudioMS != 500 {
		t.Fatalf("utterance = %+v %v", u, ok)
	}
	pb := f.messages(wsproto.TypePlayback)
	if len(pb) != 2 || pb[0].State != "started" || pb[1].State != "drained" || *pb[1].PlayedMs != 500 {
		t.Fatalf("playback reports = %+v", pb)
	}

	// The shepherd speaks: speaking on, ~10 frames, speaking off.
	speech := make([]int16, InRate*4/10)
	for i := range speech {
		speech[i] = 4000
	}
	if err := c.Say(ctx, "status please", speech); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "speaking off", 2*time.Second, func() bool { return len(f.messages(wsproto.TypeSpeaking)) == 2 })
	sp := f.messages(wsproto.TypeSpeaking)
	if !*sp[0].On || *sp[1].On {
		t.Fatalf("speaking = %+v", sp)
	}
	f.mu.Lock()
	bin := f.bin
	f.mu.Unlock()
	if bin < 10 {
		t.Fatalf("%d audio frames sent for 400 ms of speech", bin)
	}

	// Barge-in: a long reply starts playing, the shepherd talks over it,
	// the player flushes locally and reports what was heard.
	f.send(wsproto.Transcript(wsproto.DirOut, "Here is a long answer that goes on and on.", false))
	f.audio(3 * time.Second)
	waitFor(t, "playing", 2*time.Second, func() bool { return c.PlayingFor() > 300*time.Millisecond })
	if _, err := c.SayAsync("stop", speech); err != nil {
		t.Fatal(err)
	}
	u, ok = c.NextUtterance(ctx, 2*time.Second)
	if !ok || u.End != EndLocalFlush || u.PlayedMS < 250 || u.PlayedMS > 1200 || u.Heard == u.Text {
		t.Fatalf("barged utterance = %+v", u)
	}
	// The daemon's own flush still gets its flushed (played_ms 0).
	f.send(wsproto.Flush())
	waitFor(t, "two flushed reports", 2*time.Second, func() bool {
		n := 0
		for _, m := range f.messages(wsproto.TypePlayback) {
			if m.State == "flushed" {
				n++
			}
		}
		return n == 2
	})
	rec := c.Close()
	if len(rec.User) != 2 || !rec.User[1].BargeIn || len(rec.Events) == 0 || rec.Events[0].Type != "daemon.start" {
		t.Fatalf("recording: user %+v events %+v", rec.User, rec.Events)
	}
	if len(rec.BotAudio) != OutRate*35/10 {
		t.Fatalf("bot audio = %d samples", len(rec.BotAudio))
	}
}
