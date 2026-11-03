package app

import (
	"strings"
	"testing"
	"time"
)

// --voice=fake is a developer mode: it needs --dev, and then the voice is
// open at once with no key and no network.
func TestVoiceFakeNeedsDevAndRunsOpen(t *testing.T) {
	cfg := testConfig(t)
	cfg.Brain.Enabled = false
	if _, err := Build(cfg, Options{Voice: VoiceFake, Herdr: HerdrMock}); err == nil || !strings.Contains(err.Error(), "--dev") {
		t.Fatalf("--voice=fake without --dev: %v", err)
	}
	a, err := Build(cfg, Options{Voice: VoiceFake, Herdr: HerdrMock, Dev: true, Drain: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stop := runApp(t, a)
	deadline := time.Now().Add(10 * time.Second)
	v := stateVoice(t, a.URL())
	for v.Status != "connected" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		v = stateVoice(t, a.URL())
	}
	if v.Status != "connected" || v.Problem != "" || v.SetupNeeded {
		t.Fatalf("voice = %+v, want connected with no problem", v)
	}
	if got := a.VoiceProvider(); got != VoiceFake {
		t.Fatalf("provider = %q", got)
	}
	if err := stop(); err != nil {
		t.Errorf("Run: %v", err)
	}
}
