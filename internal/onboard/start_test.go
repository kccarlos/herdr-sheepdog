package onboard

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

var updateFixture = flag.Bool("update", false, "rewrite web/setup/start.fixture.json")

const webSetup = "../../web/setup/"

// The page's node tests read the same table the daemon serves; this keeps
// the fixture honest (go test ./internal/onboard -run Fixture -update).
func TestStartFixtureMatchesServedTable(t *testing.T) {
	want, err := json.MarshalIndent(Start(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join(webSetup, "start.fixture.json")
	if *updateFixture {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is out of date: run go test ./internal/onboard -run Fixture -update", path)
	}
}

// The same golden summaries are checked by web/setup/start.test.mjs.
func TestSummaryCases(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(webSetup, "summary.cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Voice, VoiceModel, Preset, OrchModel, OtherName, Want string
	}
	var raw []map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, r := range raw {
		cases = append(cases, struct{ Voice, VoiceModel, Preset, OrchModel, OtherName, Want string }{r["voice"], r["voice_model"], r["preset"], r["orch_model"], r["other_name"], r["want"]})
	}
	for _, c := range cases {
		var p *Preset
		if pr, ok := PresetByID(Presets(), c.Preset); ok {
			p = &pr
		}
		got := Summary(SummaryInput{Voice: c.Voice, VoiceModel: c.VoiceModel, Preset: p, OrchModel: c.OrchModel, OtherName: c.OtherName})
		if got != c.Want {
			t.Errorf("%s/%s:\n got %q\nwant %q", c.Voice, c.Preset, got, c.Want)
		}
	}
}

func TestCardsAndInitialChoice(t *testing.T) {
	ids := []string{}
	for _, c := range StartCards() {
		ids = append(ids, c.ID)
	}
	if len(ids) != 4 || ids[0] != "recommended" || ids[3] != "custom" {
		t.Errorf("cards = %v", ids)
	}
	for _, tc := range []struct{ v, o, want string }{
		{"gemini", "deepseek", "recommended"}, {"gemini", "anthropic", "opus"}, {"gemini", "gemini", "free"},
		{"openai", "anthropic", "custom"}, {"vertex", "vertex", "custom"}, {"gemini", "ollama", "custom"},
	} {
		if got := CardFor(tc.v, tc.o); got != tc.want {
			t.Errorf("CardFor(%s,%s) = %s, want %s", tc.v, tc.o, got, tc.want)
		}
	}
	if c := InitialChoice(true, true, "gemini", "gemini"); c.Card != "recommended" || c.Orch != "deepseek" {
		t.Errorf("fresh = %+v", c)
	}
	if c := InitialChoice(false, true, "gemini", "gemini"); c.Card != "free" {
		t.Errorf("saved = %+v", c)
	}
	if c := InitialChoice(true, false, "gemini", "gemini"); c.Card != "free" {
		t.Errorf("no deepseek preset = %+v", c)
	}
	if c, ok := CardByID("cost"); !ok || c.ID != "recommended" {
		t.Errorf("the old cost card = %+v, %v", c, ok)
	}
}

func TestVoiceSetupNeeded(t *testing.T) {
	cfg := config.Default()
	if !VoiceSetupNeeded(cfg, map[string]string{}) {
		t.Error("an empty setup needs setup")
	}
	if VoiceSetupNeeded(cfg, map[string]string{"GEMINI_API_KEY": "k"}) {
		t.Error("a Gemini key is a voice")
	}
	if !VoiceSetupNeeded(cfg, map[string]string{"GEMINI_API_KEY": "k", "SHEEPDOG_VOICE_PROVIDER": "openai"}) {
		t.Error("GPT-Live without its key needs setup")
	}
}
