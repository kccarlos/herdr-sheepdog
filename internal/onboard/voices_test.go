package onboard

import (
	"strings"
	"testing"
)

func TestVoiceNamesAndValidation(t *testing.T) {
	if v, ok := CanonicalVoice(VoiceGemini, "puck", ""); !ok || v != "Puck" {
		t.Errorf("puck = %q %v", v, ok)
	}
	if v, ok := CanonicalVoice(VoiceVertex, "KORE", ""); !ok || v != "Kore" {
		t.Errorf("vertex KORE = %q %v", v, ok)
	}
	if v, ok := CanonicalVoice(VoiceOpenAI, "Marin", ""); !ok || v != "marin" {
		t.Errorf("Marin = %q %v", v, ok)
	}
	if _, ok := CanonicalVoice(VoiceOpenAI, "Puck", ""); ok {
		t.Error("a Gemini voice was accepted for GPT-Live")
	}
	if _, ok := CanonicalVoice(VoiceGemini, "marin", ""); ok {
		t.Error("a GPT-Live voice was accepted for Gemini")
	}
	// A voice set by hand in the config stays offered and accepted.
	if v, ok := CanonicalVoice(VoiceGemini, "fenrir", "Fenrir"); !ok || v != "Fenrir" {
		t.Errorf("hand-set voice = %q %v", v, ok)
	}
	if got := strings.Join(VoiceNames(VoiceGemini, "Kore"), ","); got != "Puck,Kore,Charon,Aoede" {
		t.Errorf("gemini voices = %s", got)
	}
	for m, want := range map[string]bool{
		"gemini-3.8-live": true, "gpt-live-1": true, "models/gemini-x_1.2": true,
		"": false, "a model": false, "x\nSHEEPDOG_WEB_BIND=0.0.0.0": false, `x"y`: false, "-x": false, strings.Repeat("a", 101): false,
	} {
		if ValidModelName(m) != want {
			t.Errorf("ValidModelName(%q) = %v", m, !want)
		}
	}
}
