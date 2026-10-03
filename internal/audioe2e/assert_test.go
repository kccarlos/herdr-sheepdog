package audioe2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaxLatencyBounds(t *testing.T) {
	r := &Result{EOSToFirstIn: 300, EOSToTool: 2500, EOSToFirstAudio: -1, EOSToFirstOut: 900}
	e := Expect{MaxEOSToFirstInMS: 500, MaxEOSToToolMS: 2000, MaxEOSToFirstAudioMS: 4000, MaxEOSToFirstOutMS: 1000}
	fails := check(e, r, "gemini")
	if len(fails) != 2 {
		t.Fatalf("fails = %q", fails)
	}
	if !strings.Contains(fails[0], "tool call: 2500 ms, max 2000 ms") || !strings.Contains(fails[1], "first reply audio: never happened") {
		t.Errorf("fails = %q", fails)
	}
	if f := check(Expect{}, r, "gemini"); len(f) != 0 {
		t.Errorf("unchecked bounds failed: %q", f)
	}
}

// absent catches a duplicate announcement in its window, and only there.
func TestAbsentCountsInWindow(t *testing.T) {
	r := &Result{
		EOSMS: 10000,
		Events: []Event{
			{AtMS: -1, Type: "voice.push", Payload: map[string]any{"text": "auth is waiting"}}, // seen while settling
			{AtMS: 4000, Type: "voice.push", Payload: map[string]any{"text": "auth asks to run printf"}},
			{AtMS: 6500, Type: "voice.push", Payload: map[string]any{"text": "auth wants approval to run printf. Allow?"}},
			{AtMS: 12000, Type: "voice.push", Payload: map[string]any{"text": "docs finished"}},
			{AtMS: 12500, Type: "herdr.send", Payload: map[string]any{"kind": "keys"}},
		},
		Timeline: []Timed{{AtMS: 11000, Kind: "flush"}},
	}
	dup := Absent{Type: "voice.push", Contains: "AUTH", FromMS: -7000, MaxCount: 1, Note: "announced once"}
	fails := check(Expect{Absent: []Absent{dup}}, r, "gemini")
	if len(fails) != 1 || !strings.Contains(fails[0], "seen 2 times") || !strings.Contains(fails[0], "announced once") {
		t.Fatalf("duplicate: %q", fails)
	}
	dup.FromMS = -5000 // only the second announcement is inside
	if f := check(Expect{Absent: []Absent{dup}}, r, "gemini"); len(f) != 0 {
		t.Errorf("window: %q", f)
	}
	keys := Absent{Type: "herdr.send", Payload: map[string]any{"kind": "keys"}, ToMS: 2000}
	if f := check(Expect{Absent: []Absent{keys}}, r, "gemini"); len(f) != 0 {
		t.Errorf("keys outside the window counted: %q", f)
	}
	keys.ToMS = 0
	if f := check(Expect{Absent: []Absent{keys}}, r, "gemini"); len(f) != 1 {
		t.Errorf("keys inside the window: %q", f)
	}
	flush := Absent{Timeline: "flush", Providers: []string{"openai"}}
	if f := check(Expect{Absent: []Absent{flush}}, r, "gemini"); len(f) != 0 {
		t.Errorf("provider filter: %q", f)
	}
	if f := check(Expect{Absent: []Absent{flush}}, r, "openai"); len(f) != 1 {
		t.Errorf("timeline flush: %q", f)
	}
}

// Each turn is checked on its own window, with latencies from its own
// end of speech.
func TestCheckTurnsSlicesWindows(t *testing.T) {
	sc := Scenario{Name: "two", Turns: []Turn{
		{Name: "approve", Expect: Expect{Tool: "to_supervisor", KeysSent: true, MaxEOSToToolMS: 1500, Reply: true}},
		{Name: "ask", Expect: Expect{Tool: "to_supervisor", Reply: true, InContains: []string{"approve"}, Absent: []Absent{{Type: "herdr.send", Payload: map[string]any{"kind": "keys"}}}}},
	}}
	r := &Result{
		Timeline: []Timed{
			{AtMS: 6300, Kind: "in", Text: "Yes."},
			{AtMS: 7600, Kind: "audio"},
			{AtMS: 7700, Kind: "out", Text: "Approved."},
			{AtMS: 13000, Kind: "in", Text: "What did I just approve?"},
			{AtMS: 16000, Kind: "audio"},
			{AtMS: 16100, Kind: "out", Text: "You let auth write hello.txt."},
		},
		Events: []Event{
			{AtMS: 6900, Type: "voice.tool_call", Payload: map[string]any{"name": "to_supervisor", "args": map[string]any{"text": "yes"}}},
			{AtMS: 7000, Type: "herdr.send", Payload: map[string]any{"kind": "keys"}},
			{AtMS: 14500, Type: "voice.tool_call", Payload: map[string]any{"name": "to_supervisor", "args": map[string]any{"text": "What did I just approve?"}}},
			{AtMS: 14800, Type: "herdr.send", Payload: map[string]any{"kind": "keys"}},
		},
	}
	windows := []turnWindow{
		{start: 0, end: 12000, speechStart: 6000, eos: 6400, spoke: true},
		{start: 12000, end: 0, speechStart: 12800, eos: 13600, spoke: true},
	}
	turns, fails := checkTurns(sc, r, windows, "gemini")
	if len(turns) != 2 {
		t.Fatalf("turns = %+v", turns)
	}
	a, b := turns[0], turns[1]
	if !a.Pass || a.EOSToTool != 500 || a.EOSToFirstAudio != 1200 || a.InTranscript != "Yes." || len(a.Tools) != 1 {
		t.Errorf("approve = %+v", a)
	}
	if b.Pass || b.EOSToTool != 900 || b.OutTranscript != "You let auth write hello.txt." {
		t.Errorf("ask = %+v", b)
	}
	if len(fails) != 1 || !strings.HasPrefix(fails[0], "ask: herdr.send seen 1 times") {
		t.Errorf("fails = %q", fails)
	}
	// A turn the run never reached is a failure, not a pass.
	turns, fails = checkTurns(sc, r, windows[:1], "gemini")
	if len(turns) != 2 || turns[1].Pass || len(fails) != 1 || fails[0] != "ask: not run" {
		t.Errorf("not run: %+v %q", turns, fails)
	}
}

func TestLoadValidatesTurnsAndAbsent(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"both":    `{"scenarios":[{"name":"x","segments":[{"say":"a"}],"turns":[{"segments":[{"say":"b"}]}]}]}`,
		"empty":   `{"scenarios":[{"name":"x","turns":[{"segments":[]}]}]}`,
		"absent0": `{"scenarios":[{"name":"x","segments":[{"say":"a"}],"expect":{"absent":[{"contains":"auth"}]}}]}`,
		"absent2": `{"scenarios":[{"name":"x","turns":[{"segments":[{"say":"a"}],"expect":{"absent":[{"type":"voice.push","timeline":"out"}]}}]}]}`,
	} {
		p := filepath.Join(dir, name+".json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	p := filepath.Join(dir, "ok.json")
	_ = os.WriteFile(p, []byte(`{"scenarios":[{"name":"x","turns":[{"segments":[{"say":"a"}]},{"segments":[{"say":"b"}]}]}]}`), 0o644)
	f, err := Load(p)
	if err != nil || len(f.Scenarios[0].turns()) != 2 {
		t.Fatalf("ok: %v", err)
	}
}
