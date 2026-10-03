package tier3

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/bench"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/pcm"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/speech"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// toneSynth renders every line as 500 ms of tone.
type toneSynth struct {
	mu   sync.Mutex
	reqs []speech.Request
}

func (s *toneSynth) ID() string { return "tone" }
func (s *toneSynth) Synthesize(_ context.Context, r speech.Request) (speech.Clip, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, r)
	s.mu.Unlock()
	n := speech.Rate / 2
	smp := make([]int16, n)
	for i := range smp {
		smp[i] = 5000
		if i%2 == 0 {
			smp[i] = -5000
		}
	}
	return speech.Clip{Samples: smp, Source: "say", Voice: r.Voice}, nil
}

// fakePhoneDaemon speaks the /ws and /events sides of the daemon: when
// the shepherd stops speaking it "hears" heard and answers with reply.
type fakePhoneDaemon struct {
	srv    *httptest.Server
	heard  string
	reply  string
	push   string
	mu     sync.Mutex
	conn   *websocket.Conn
	gotHdr bool
}

func newFakePhoneDaemon(t *testing.T, heard, reply string) *fakePhoneDaemon {
	f := &fakePhoneDaemon{heard: heard, reply: reply}
	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "id: 1\nevent: log\ndata: {\"id\":1,\"ts\":%q,\"type\":\"daemon.start\",\"payload\":{}}\n\n", time.Now().UTC().Format(time.RFC3339Nano))
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
		for {
			typ, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				continue
			}
			m, err := wsproto.Parse(data)
			if err != nil {
				continue
			}
			switch {
			case m.Type == wsproto.TypeClientReady:
				f.mu.Lock()
				f.gotHdr = true
				f.mu.Unlock()
				go func() {
					f.send(wsproto.BotReady(1, 1, "test", []string{"playback", "speaking"}))
					f.send(wsproto.Status("connected", false, ""))
					if f.push != "" {
						time.Sleep(300 * time.Millisecond)
						f.say(f.push)
					}
				}()
			case m.Type == wsproto.TypeSpeaking && m.On != nil && !*m.On:
				go func() {
					f.send(wsproto.Transcript(wsproto.DirIn, f.heard, true))
					time.Sleep(200 * time.Millisecond)
					f.say(f.reply)
				}()
			}
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePhoneDaemon) send(m wsproto.Message) {
	b, _ := m.Encode()
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	_ = c.Write(context.Background(), websocket.MessageText, b)
}

func (f *fakePhoneDaemon) say(text string) {
	f.send(wsproto.Transcript(wsproto.DirOut, text, false))
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	for range 5 {
		_ = c.Write(context.Background(), websocket.MessageBinary, pcm.ToBytes(make([]int16, voiceclient.OutRate/10)))
	}
}

func voiceScenario() Scenario {
	return Scenario{ID: "v1", Goal: "g", Language: "en", Opening: "how is the build going",
		Workers: []Worker{{Alias: "a", Workspace: "w", Kind: KindClaude}}}
}

func TestVoiceCallSpeaksAndHears(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time phone test")
	}
	f := newFakePhoneDaemon(t, "how is the build going", "The build is green.")
	f.push = "Heads up, the tests finished."
	syn := &toneSynth{}
	meter := bench.NewSpendMeter("")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	start := time.Now().Add(-5 * time.Second)
	vc, err := dialVoice(ctx, voiceScenario(), VoiceOptions{Synth: syn}, f.srv.URL, 1, eval.ScriptedSimUser{}, meter, start, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.close()
	if vc.off < 4000 {
		t.Fatalf("clock offset %d ms, want about 5000 (the call started before the phone dialled)", vc.off)
	}
	// The push of the flock already running.
	arrived := vc.arrive(ctx)
	if len(arrived) != 1 || !strings.Contains(arrived[0].Text, "tests finished") {
		t.Fatalf("arrive = %+v", arrived)
	}
	var rec Record
	lines, answered, err := vc.say(ctx, "how is the build going", true, &rec)
	if err != nil || !answered {
		t.Fatalf("say: %v answered=%v", err, answered)
	}
	if len(lines) != 2 || lines[0].Who != "ceo" || lines[1].Who != "sheepdog" || !strings.Contains(lines[1].Text, "build is green") {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[0].AtMS < 4000 {
		t.Fatalf("CEO line at %d ms: not on the run's clock", lines[0].AtMS)
	}
	vc.tail(ctx)
	fails := vc.finish(&rec)
	if len(fails) != 0 || rec.Voice == nil || len(rec.Voice.User) != 1 {
		t.Fatalf("finish: fails %+v voice %+v", fails, rec.Voice)
	}
	if u := rec.Voice.User[0]; u.Heard != "how is the build going" || u.WER != 0 {
		t.Fatalf("user line = %+v", u)
	}
	if len(rec.Voice.Bot) != 2 || rec.Voice.TTS != "tone" {
		t.Fatalf("voice record = %+v", rec.Voice)
	}
	if len(syn.reqs) != 1 || syn.reqs[0].Voice == "" || syn.reqs[0].Lang != "en" {
		t.Fatalf("tts requests = %+v", syn.reqs)
	}
}

func TestVoiceCallFlagsALineNotHeard(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time phone test")
	}
	f := newFakePhoneDaemon(t, "", "Sorry?")
	meter := bench.NewSpendMeter("")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	vc, err := dialVoice(ctx, voiceScenario(), VoiceOptions{Synth: &toneSynth{}}, f.srv.URL, 1, eval.ScriptedSimUser{}, meter, time.Now(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.close()
	var rec Record
	if _, _, err := vc.say(ctx, "status please", true, &rec); err != nil {
		t.Fatal(err)
	}
	fails := vc.finish(&rec)
	if len(fails) != 1 || fails[0].Type != eval.FailCheck || !strings.Contains(fails[0].Detail, "heard nothing") || rec.Voice.NotHeard != 1 {
		t.Fatalf("fails = %+v", fails)
	}
}

func TestDialVoiceFailsWhenTheDaemonIsNotThere(t *testing.T) {
	dial := func(context.Context, voiceclient.Options) (*voiceclient.Client, error) {
		return nil, fmt.Errorf("refused")
	}
	_, err := dialVoice(context.Background(), voiceScenario(), VoiceOptions{Synth: &toneSynth{}, Dial: dial}, "http://127.0.0.1:1", 1, eval.ScriptedSimUser{}, bench.NewSpendMeter(""), time.Now(), t.Logf)
	if err == nil || !strings.Contains(err.Error(), "phone: refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestServeArgsByMode(t *testing.T) {
	text := serveArgs(DaemonOptions{Port: 7795}, "c.toml", "d")
	if !slices.Contains(text, "--voice=off") || !slices.Contains(text, "--heard-echo") || slices.Contains(text, "--voice=gemini") {
		t.Fatalf("text args = %v", text)
	}
	v := serveArgs(DaemonOptions{Port: 7795, Voice: true}, "c.toml", "d")
	if !slices.Contains(v, "--voice=gemini") || slices.Contains(v, "--heard-echo") || slices.Contains(v, "--voice=off") {
		t.Fatalf("voice args = %v", v)
	}
	for _, a := range [][]string{text, v} {
		if !slices.Contains(a, "--herdr=socket") || !slices.Contains(a, "7795") {
			t.Fatalf("args = %v", a)
		}
	}
}

func TestVoiceScenarioFieldsLoadAndValidate(t *testing.T) {
	s := voiceScenario()
	s.Audio = bench.Audio{Voice: "Kore", Style: "mixed", Disfluency: 0.2}
	s.Behavior = bench.Behavior{BargeIn: &bench.BargeIn{Prob: 1, Line: "stop"}}
	if err := s.Validate(); err != nil {
		t.Fatalf("voice scenario: %v", err)
	}
	s.Behavior.Reconnect = &bench.Reconnect{AfterTurn: 1, AwayMS: 1000}
	if err := s.Validate(); err == nil {
		t.Fatal("a reconnect behavior is tier 2 only")
	}
}
