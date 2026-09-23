package audioe2e

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

func TestScenariosFileLoads(t *testing.T) {
	f, err := Load(filepath.Join("..", "..", "testdata", "audio-e2e", "scenarios.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Scenarios) < 8 {
		t.Fatalf("%d scenarios, want at least 8", len(f.Scenarios))
	}
	seen := map[string]bool{}
	for _, s := range f.Scenarios {
		if seen[s.Name] {
			t.Errorf("duplicate scenario %s", s.Name)
		}
		seen[s.Name] = true
		for _, seg := range s.Segments {
			if seg.Say == "" && seg.SilenceMS == 0 && seg.NoiseMS == 0 {
				t.Errorf("%s: empty segment", s.Name)
			}
		}
	}
}

func TestNoiseIsUnitRMSAndDeterministic(t *testing.T) {
	for _, kind := range []string{"white", "pink", "brown", "cafe"} {
		a, err := NoiseSamples(kind, 16000, 7)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := NoiseSamples(kind, 16000, 7)
		var sum float64
		for i, v := range a {
			sum += v * v
			if v != b[i] {
				t.Fatalf("%s: not deterministic", kind)
			}
		}
		if rms := math.Sqrt(sum / float64(len(a))); math.Abs(rms-1) > 1e-6 {
			t.Errorf("%s: rms %f", kind, rms)
		}
	}
	if _, err := NoiseSamples("jazz", 10, 1); err == nil {
		t.Error("unknown kind accepted")
	}
}

func TestMixAtSNR(t *testing.T) {
	speech := make([]int16, 16000)
	for i := range speech {
		speech[i] = int16(8000 * math.Sin(float64(i)*0.05))
	}
	noise, _ := NoiseSamples("white", len(speech), 1)
	level := VoicedRMS(speech) / math.Pow(10, 10.0/20)
	mixed := Mix(make([]int16, len(speech)), noise, level)
	snr := 20 * math.Log10(VoicedRMS(speech)/RMS(mixed))
	if math.Abs(snr-10) > 0.3 {
		t.Errorf("snr %.2f dB, want 10", snr)
	}
}

func TestGateMatchesTheApps(t *testing.T) {
	var msgs []string
	frames := 0
	g := &gate{
		send:  func(m wsproto.Message) error { msgs = append(msgs, m.Type+":"+boolStr(*m.On)); return nil },
		write: func([]byte) error { frames++; return nil },
	}
	loud := make([]int16, FrameSamples)
	for i := range loud {
		loud[i] = 1000
	}
	quiet := make([]int16, FrameSamples)
	for i := 0; i < 3; i++ {
		_ = g.push(loud)
	}
	// 300 ms of silence closes the gate: 7 frames sent, the 8th closes.
	for i := 0; i < 10; i++ {
		_ = g.push(quiet)
	}
	if len(msgs) != 1 || msgs[0] != "silence:true" {
		t.Fatalf("msgs %v", msgs)
	}
	sent := frames
	_ = g.push(loud)
	if len(msgs) != 2 || msgs[1] != "silence:false" {
		t.Fatalf("msgs %v", msgs)
	}
	// The withheld preroll (the last 40 ms quiet frame) and the loud one go out.
	if frames-sent != 2 {
		t.Errorf("reopen sent %d frames, want 2", frames-sent)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestFuzzyContains(t *testing.T) {
	for _, c := range []struct {
		text, group string
		want        bool
	}{
		{" What's going on?", "what is going on", true},
		{"What is going on with my agents", "going on|status", true},
		{"yes", "yes|yeah|yep", true},
		{"Yeah.", "yes|yeah", true},
		{"Stop.", "stop", true},
		{"fishing", "stop", false},
		{"Tell the aggents to run tests", "agents", true}, // one edit
		{"", "stop", false},
	} {
		if got := FuzzyContains(c.text, c.group); got != c.want {
			t.Errorf("FuzzyContains(%q, %q) = %v", c.text, c.group, got)
		}
	}
}

func TestCheck(t *testing.T) {
	r := &Result{InTranscript: "Stop.", Tools: []string{"stop"}, AudioOutBytes: 10, OutTranscript: "ok", Flushes: 1,
		Events: []Event{{Type: "voice.session_close", Payload: map[string]any{"reason": "idle"}}}}
	e := Expect{InContains: []string{"stop"}, Tool: "stop", Reply: true, Flush: true,
		Events: []EventMatch{{Type: "voice.session_close", Payload: map[string]any{"reason": "idle"}, Providers: []string{"openai"}}}}
	if f := check(e, r, "openai"); len(f) != 0 {
		t.Errorf("failures %v", f)
	}
	r.Events = nil
	if f := check(e, r, "openai"); len(f) != 1 || !strings.Contains(f[0], "voice.session_close") {
		t.Errorf("failures %v", f)
	}
	if f := check(e, r, "gemini"); len(f) != 0 {
		t.Errorf("provider-scoped event checked on gemini: %v", f)
	}
	if f := check(Expect{NoInTranscript: true, KeysSent: true}, &Result{InTranscript: "fishing"}, "gemini"); len(f) != 2 {
		t.Errorf("failures %v", f)
	}
}
