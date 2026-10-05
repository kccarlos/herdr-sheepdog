package supervisor

import (
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// silentTexts returns the silent-context pushes.
func (h *harness) silentTexts() []string {
	var out []string
	for _, p := range h.pusher.all() {
		if p.Urgency == voice.UrgencySilent {
			out = append(out, p.Text)
		}
	}
	return out
}

// TestLanguageSettingRoundTrip: the language preference is validated,
// normalised, persisted next to chattiness without touching it, logged,
// survives a restart, and a change reaches the voice as one silent line;
// setting the same list again pushes nothing.
func TestLanguageSettingRoundTrip(t *testing.T) {
	h := twoAgents(t)
	if got := h.sup.Settings(); got.Languages != nil || h.sup.LanguageLine() != "" {
		t.Fatalf("default = %+v %q", got, h.sup.LanguageLine())
	}
	if err := h.sup.SetSettings(Settings{Languages: []string{"zh-hans", "klingon"}}); err == nil {
		t.Fatal("unknown code accepted")
	}
	before := len(h.silentTexts())
	if err := h.sup.SetSettings(Settings{Languages: []string{"zh-hans", "en", "ZH-HANS"}}); err != nil {
		t.Fatal(err)
	}
	got := h.sup.Settings()
	if strings.Join(got.Languages, ",") != "zh-Hans,en" || got.Chattiness != Normal {
		t.Fatalf("Settings() = %+v", got)
	}
	if strings.Join(h.store.Snapshot().Session.Languages, ",") != "zh-Hans,en" || h.store.Snapshot().Session.Chattiness != "" {
		t.Errorf("session = %+v", h.store.Snapshot().Session)
	}
	silent := h.silentTexts()[before:]
	if len(silent) != 1 || !strings.Contains(silent[0], "changed their language setting") || !strings.Contains(silent[0], "Chinese (Simplified) 简体中文, English") || !strings.Contains(silent[0], "Simplified characters") {
		t.Fatalf("silent pushes = %q", silent)
	}
	if len(h.pushTexts()) != 0 {
		t.Errorf("spoken pushes = %q", h.pushTexts())
	}
	evs := h.log.ofType("supervisor.settings")
	if len(evs) != 1 {
		t.Fatalf("supervisor.settings = %d", len(evs))
	}
	if p := evs[0].Payload.(map[string]any); p["chattiness"] != nil || p["languages"] == nil {
		t.Errorf("payload = %v", p)
	}
	if !strings.Contains(h.sup.LanguageLine(), "Simplified characters") || brainHostLine(h) != h.sup.LanguageLine() {
		t.Errorf("LanguageLine = %q, brain host %q", h.sup.LanguageLine(), brainHostLine(h))
	}

	// A chattiness-only change keeps the languages; the same list again
	// pushes nothing.
	if err := h.sup.SetSettings(Settings{Chattiness: Quiet}); err != nil {
		t.Fatal(err)
	}
	if err := h.sup.SetSettings(Settings{Languages: []string{"zh-Hans", "en"}}); err != nil {
		t.Fatal(err)
	}
	if got := h.sup.Settings(); got.Chattiness != Quiet || len(got.Languages) != 2 {
		t.Errorf("after chattiness-only = %+v", got)
	}
	if n := len(h.silentTexts()) - before; n != 1 {
		t.Errorf("silent pushes after an unchanged list = %d, want 1", n)
	}

	store2, err := state.Open(h.store.Path(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sup2, err := New(store2, h.client, nil, h.pusher, h.log, Config{Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(sup2.Languages(), ",") != "zh-Hans,en" {
		t.Errorf("after restart = %v", sup2.Languages())
	}

	// An empty list clears it, and says so silently.
	if err := h.sup.SetSettings(Settings{Languages: []string{}}); err != nil {
		t.Fatal(err)
	}
	if h.sup.Languages() != nil || h.sup.LanguageLine() != "" {
		t.Errorf("after clear = %v", h.sup.Languages())
	}
	if s := h.silentTexts(); !strings.Contains(s[len(s)-1], "cleared their language setting") {
		t.Errorf("last silent = %q", s[len(s)-1])
	}
}

func brainHostLine(h *harness) string { return brainHost{s: h.sup}.LanguageLine() }
