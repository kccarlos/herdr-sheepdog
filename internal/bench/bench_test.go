package bench

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

const (
	scenarioGlob = "../../testdata/bench/scenarios/*.json"
	simGlob      = "../../testdata/eval/sim/*.json"
	fixtureDir   = "../../testdata/herdr"
)

// The dataset loads, covers every category, resolves every screen, and
// the openings manifest pins every opening.
func TestDatasetLoads(t *testing.T) {
	scs, err := LoadScenarios(scenarioGlob, simGlob)
	if err != nil {
		t.Fatal(err)
	}
	if len(scs) < 30 {
		t.Fatalf("%d scenarios, want about 30", len(scs))
	}
	counts := CategoryCounts(scs)
	for _, c := range Categories {
		if counts[c] == 0 {
			t.Errorf("no scenario in category %s", c)
		}
	}
	for _, s := range scs {
		if _, err := eval.ResolveScreens(s.SimScenario, fixtureDir); err != nil {
			t.Errorf("%s: %v", s.ID, err)
		}
		if s.Opening == "" {
			t.Errorf("%s: no opening (the first line is pinned)", s.ID)
		}
		if len(s.Script) == 0 && len(s.Agenda) == 0 {
			t.Errorf("%s: no script or agenda for the offline scripted CEO", s.ID)
		}
	}
	m, err := LoadManifest("../../testdata/bench/openings.json")
	if err != nil {
		t.Fatal(err)
	}
	if probs := m.Check(scs); len(probs) > 0 {
		t.Errorf("openings manifest: %v", probs)
	}
}

func writeScenario(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "s.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFromSimMergeAndValidation(t *testing.T) {
	p := writeScenario(t, `{"scenarios":[{"id":"x","category":"approve","from_sim":"j2-approve-en","persona":"New persona.","audio":{"style":"tired","noise":{"name":"cafe","kind":"cafe","snr_db":10}},"behavior":{"think_ms":[100,200]}}]}`)
	scs, err := LoadScenarios(p, simGlob)
	if err != nil {
		t.Fatal(err)
	}
	s := scs[0]
	if s.ID != "x" || s.Persona != "New persona." || s.Opening != "Yes, let bench-codex run the tests." || len(s.Agents) != 2 || len(s.Expect.Actions) != 1 {
		t.Fatalf("merged = %+v", s.SimScenario)
	}
	for _, bad := range []string{
		`{"scenarios":[{"id":"x","category":"nope","from_sim":"j2-approve-en"}]}`,
		`{"scenarios":[{"id":"x","category":"approve","from_sim":"missing"}]}`,
		`{"scenarios":[{"id":"x","category":"approve","from_sim":"j2-approve-en","audio":{"voice":"Nobody"}}]}`,
		`{"scenarios":[{"id":"x","category":"approve","from_sim":"j2-approve-en","audio":{"noise":{"kind":"jazz"}}}]}`,
		`{"scenarios":[{"id":"x","category":"approve","from_sim":"j2-approve-en","behavior":{"collision":{"event":5,"line":"hi"}}}]}`,
		`{"scenarios":[{"id":"x","category":"approve","from_sim":"j2-approve-en","behavior":{"barge_in":{"prob":0}}}]}`,
		`{"scenarios":[{"id":"x","category":"approve","from_sim":"j2-approve-en","notes":["n"]}]}`,
		`{"scenarios":[{"id":"x","category":"approve","from_sim":"j2-approve-en","bogus":1}]}`,
	} {
		if _, err := LoadScenarios(writeScenario(t, bad), simGlob); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestManifestCheck(t *testing.T) {
	sc := Scenario{SimScenario: eval.SimScenario{ID: "a", Opening: "Hi there"}}
	if v := VoiceFor(sc); v != VoiceFor(Scenario{SimScenario: eval.SimScenario{ID: "a"}}) {
		t.Fatal("the voice is not a function of the id")
	}
	req := OpeningRequest(sc)
	m := Manifest{Openings: []Opening{{Scenario: "a", Text: req.Text, Lang: req.Lang, Voice: req.Voice, Style: req.Style}}}
	if p := m.Check([]Scenario{sc}); len(p) != 0 {
		t.Fatalf("fresh manifest: %v", p)
	}
	sc.Opening = "Hello there"
	if p := m.Check([]Scenario{sc}); len(p) != 1 || !strings.Contains(p[0], "stale") {
		t.Fatalf("stale manifest: %v", p)
	}
	zh := Scenario{SimScenario: eval.SimScenario{ID: "b", Opening: "先不要"}}
	if OpeningRequest(zh).Lang != "zh" {
		t.Fatal("a Chinese opening is not rendered as Chinese")
	}
}

func TestVoiceChecks(t *testing.T) {
	sc := Scenario{Voice: VoiceExpect{OpeningHeard: []string{"approve|nothing", "let codex run"}, Events: []EventMatch{{Type: "brain.failover"}}, Absent: []EventMatch{{Type: "herdr.send", Payload: map[string]any{"kind": "keys"}}}}}
	run := Run{User: []UserLine{{Said: "Let codex run the tests", Heard: "Let Codex run the tests."}, {Said: "yes", Heard: ""}}}
	fs := voiceChecks(sc, run)
	if len(fs) != 1 || fs[0].Type != eval.FailCheck || !strings.Contains(fs[0].Detail, `heard nothing of "yes"`) {
		t.Fatalf("voice checks = %+v", fs)
	}
	evs := []voiceclient.Event{{Type: "herdr.send", Payload: map[string]any{"kind": "keys"}}}
	fs = voiceEventChecks(sc, evs)
	if len(fs) != 2 || !strings.Contains(fs[0].Detail, "no brain.failover") || !strings.Contains(fs[1].Detail, "unexpected herdr.send") {
		t.Fatalf("event checks = %+v", fs)
	}
	if !fuzzyContains("Uh, let the Codex worker run its tests", "let codex run") || fuzzyContains("run codex", "codex let") {
		t.Fatal("fuzzyContains")
	}
}

func TestReport(t *testing.T) {
	pass := Run{Scenario: "status-en", Category: "status", Repeat: 1, Status: eval.RunPass, User: []UserLine{{Said: "hi", Heard: "hi"}},
		Latency: []map[string]any{{"total_ms": 4000.0, "brain_ms": 3000.0, "outcome": "played"}}, Spend: Spend{TotalUSD: 0.05}, CostUSD: 0.05}
	fail := Run{Scenario: "approve-codex", Category: "approve", Repeat: 1, Status: eval.RunFail, Failures: []eval.Failure{{Type: eval.FailMissingCall, Detail: "no keys to bench-codex"}},
		Transcript: []eval.SimLine{{AtMS: 1000, Who: "ceo", Text: "yes"}}, User: []UserLine{{Said: "yes", Heard: "guess", WER: 1}}, Excerpt: []string{"1.0s voice.tool_call {}"}, Voice: VoiceStats{Unheard: 1}}
	nr := notRun(Scenario{SimScenario: eval.SimScenario{ID: "x"}, Category: "noise"}, 1, "budget")
	scs := []Scenario{{SimScenario: eval.SimScenario{ID: "status-en"}, Category: "status"}, {SimScenario: eval.SimScenario{ID: "approve-codex"}, Category: "approve", Audio: Audio{Noise: &NoiseCond{Kind: "cafe", SNRDB: 10}}}}
	rep := NewReport(time.Now(), map[string]string{"tier": "2"}, 1, []Run{pass, fail, nr}, Spend{TotalUSD: 0.07}, scs)
	if rep.Total.Pass != 1 || rep.Total.Fail != 1 || rep.Total.NotRun != 1 || len(rep.Categories) != 3 {
		t.Fatalf("totals = %+v categories %d", rep.Total, len(rep.Categories))
	}
	if rep.Voice.Unheard != 1 || rep.Voice.WERByNoise["cafe 10dB"] != 1 || rep.Voice.WERByLang["en"] != 0.5 {
		t.Fatalf("voice = %+v", rep.Voice)
	}
	var b bytes.Buffer
	rep.WriteMarkdown(&b, 5)
	md := b.String()
	for _, want := range []string{"pass rate 50%", "| approve | 1 | 0 | 1 |", "missing_function_call", "| total | 1 | 4000 |", "### approve-codex r1", `(voice model heard "yes" as "guess")`} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestBed(t *testing.T) {
	b, err := NewBed("pink", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Next(100); len(got) != 100 || got[0] != 0 {
		t.Fatal("a bed with no level is silent")
	}
	b.SetDBFS(-30)
	s := b.Next(16000)
	if r := rms(s); r < 800 || r > 1300 {
		t.Fatalf("-30 dBFS bed RMS = %.0f", r)
	}
	b.SetFloor(-75, 1)
	if _, err := NewBed("jazz", 1); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func rms(s []int16) float64 {
	var sum float64
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	return sqrt(sum / float64(len(s)))
}

func sqrt(x float64) float64 {
	z := x / 2
	for i := 0; i < 40; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}

func TestLoadRunsRebuildsTheReport(t *testing.T) {
	dir := t.TempDir()
	r := &runner{o: RunOptions{Out: dir}, sc: Scenario{SimScenario: eval.SimScenario{ID: "a"}}, run: &Run{Scenario: "a", Category: "status", Repeat: 2, Status: eval.RunPass,
		Bot: []voiceclient.BotUtterance{{Text: "[system event, not spoken by the user] hi", AudioMS: 900}, {AudioMS: 500}}, User: []UserLine{{Said: "x", Heard: "x", Source: "gemini/gemini-2.5-flash-tts"}}}}
	r.save()
	runs, err := LoadRuns(dir)
	if err != nil || len(runs) != 1 || runs[0].Repeat != 2 {
		t.Fatalf("runs = %+v %v", runs, err)
	}
	rep := NewReport(time.Now(), nil, 2, runs, Spend{}, nil)
	if rep.Voice.MarkupSpoken != 1 || rep.Voice.EmptyBot != 1 || rep.Voice.TTSSources["gemini/gemini-2.5-flash-tts"] != 1 {
		t.Fatalf("voice = %+v", rep.Voice)
	}
}
