package redact

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// fakeSecrets are one distinct, recognisable fake value per secret kind
// the config holds.
func fakeSecrets() (config.Config, map[string]string) {
	s := map[string]string{
		"gemini pool 1":   "gem1-FAKEVALUEaaaaaaaaaaaa",
		"gemini pool 2":   "gem2-FAKEVALUEbbbbbbbbbbbb",
		"vertex":          "vtx-FAKEVALUEcccccccccccc",
		"deepseek":        "dsk-FAKEVALUEdddddddddddd",
		"openai voice":    "oai-FAKEVALUEeeeeeeeeeeee",
		"web token":       "tok-FAKEVALUEffffffffffff",
		"openrouter":      "orr-FAKEVALUEgggggggggggg",
		"env-only secret": "envonly-FAKEVALUEhhhhhhh",
	}
	cfg := config.Default()
	cfg.Gemini.APIKeys = []string{s["gemini pool 1"], s["gemini pool 2"]}
	cfg.Gemini.Vertex.APIKey = s["vertex"]
	cfg.DeepSeek.APIKey = s["deepseek"]
	cfg.Voice.OpenAI.APIKey = s["openai voice"]
	cfg.Web.Token = s["web token"]
	cfg.Brain.OpenAICompat = []config.OpenAICompat{{Name: "openrouter", APIKey: s["openrouter"]}}
	return cfg, s
}

func TestEveryConfigSecretIsRedacted(t *testing.T) {
	cfg, secrets := fakeSecrets()
	r := New(append(ConfigSecrets(cfg), EnvSecrets([]string{"MY_SERVICE_TOKEN=" + secrets["env-only secret"], "PATH=/usr/bin"})...)...)
	var text strings.Builder
	for name, v := range secrets {
		text.WriteString(name + " is " + v + " (in a sentence)\n")
		text.WriteString(`{"payload":{"detail":"` + v + `"}}` + "\n")
		text.WriteString("GET /ws?u=http://x&t=" + v + "\n")
	}
	out := r.String(text.String())
	for name, v := range secrets {
		if strings.Contains(out, v) {
			t.Errorf("%s leaked:\n%s", name, out)
		}
	}
	if !strings.Contains(out, Mask) {
		t.Error("no mask in the output")
	}
	if !strings.Contains(r.String("PATH=/usr/bin"), "/usr/bin") {
		t.Error("PATH is not a secret")
	}
}

func TestShapesAreRedactedWithoutConfig(t *testing.T) {
	var r *Redactor
	for _, in := range []string{
		"key AIzaSyA1234567890abcdefghijklmnopqrstu in a URL",
		"Authorization: Bearer abcdefghijklmnop.qrstuv",
		"OPENROUTER_API_KEY=sk-or-v1-0123456789abcdef0123",
		"https://example.com/x?key=ZZZsecretZZZ123&alt=json",
		"GEMINI_API_KEY_2=AIzaSecondKeyFakeFake0123456",
		`{"token":"plainlongtokenvalue"}`,
		"-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49\n-----END PRIVATE KEY-----",
	} {
		out := r.String(in)
		for _, leak := range []string{"AIzaSyA123", "abcdefghijklmnop", "sk-or-v1", "ZZZsecretZZZ", "AIzaSecond", "plainlongtokenvalue", "MIGTAgEAMBMG"} {
			if strings.Contains(out, leak) {
				t.Errorf("%q -> %q leaks %q", in, out, leak)
			}
		}
	}
	// Ordinary log text survives.
	for _, keep := range []string{"gemini.api_keys=4 web.token=set", "pane_id w5:p1 turn_id t-12", "monkey: banana-split"} {
		if got := r.String(keep); got != keep {
			t.Errorf("%q was changed to %q", keep, got)
		}
	}
}

func TestWriterRedactsSecretSplitAcrossWrites(t *testing.T) {
	var buf bytes.Buffer
	w := New("SPLITSECRET123456").Writer(&buf)
	_, _ = w.Write([]byte("value SPLITSEC"))
	_, _ = w.Write([]byte("RET123456 end\npartial"))
	w.Flush()
	if strings.Contains(buf.String(), "SPLITSECRET") || !strings.Contains(buf.String(), "value [REDACTED] end\npartial") {
		t.Errorf("got %q", buf.String())
	}
}
