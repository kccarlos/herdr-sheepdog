package redact

import (
	"bytes"
	"encoding/json"
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
		"openrouter":      "orr-FAKEVALUEgggggggggggg",
		"env-only secret": "envonly-FAKEVALUEhhhhhhh",
	}
	cfg := config.Default()
	cfg.Gemini.APIKeys = []string{s["gemini pool 1"], s["gemini pool 2"]}
	cfg.Gemini.Vertex.APIKey = s["vertex"]
	cfg.DeepSeek.APIKey = s["deepseek"]
	cfg.Voice.OpenAI.APIKey = s["openai voice"]
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
	ghpTok := "gh" + "p_" + strings.Repeat("aB3", 12)
	ghoTok := "gh" + "o_" + strings.Repeat("xY9", 10)
	ghPat := "github" + "_pat_" + "11ABCDEF0_" + strings.Repeat("z9Q", 12)
	slackBot := "xox" + "b-" + "123456789012-" + "FAKEFAKEFAKEFAKE"
	slackUser := "xox" + "p-" + "987654321098-" + "FAKEFAKEFAKEFAKE"
	awsKey := "AK" + "IA" + "FAKEFAKEFAKEFAKE"
	for _, in := range []string{
		"key AIzaSyA1234567890abcdefghijklmnopqrstu in a URL",
		"Authorization: Bearer abcdefghijklmnop.qrstuv",
		"OPENROUTER_API_KEY=sk-or-v1-0123456789abcdef0123",
		"https://example.com/x?key=ZZZsecretZZZ123&alt=json",
		"GEMINI_API_KEY_2=AIzaSecondKeyFakeFake0123456",
		`{"access_token":"plainlongtokenvalue"}`,
		"Incorrect API key provided: sk-FakeK****************0000. You can",
		"-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49\n-----END PRIVATE KEY-----",
		"pushed with " + ghpTok + " to origin",
		"oauth " + ghoTok + " ok",
		"fine-grained " + ghPat + " in log",
		"slack bot " + slackBot + " and user " + slackUser,
		"aws id " + awsKey + " in output",
	} {
		out := r.String(in)
		for _, leak := range []string{
			"AIzaSyA123", "abcdefghijklmnop", "sk-or-v1", "ZZZsecretZZZ", "AIzaSecond",
			"plainlongtokenvalue", "MIGTAgEAMBMG", "sk-FakeK",
			ghpTok, ghoTok, ghPat, slackBot, slackUser, awsKey,
		} {
			if strings.Contains(out, leak) {
				t.Errorf("%q -> %q leaks %q", in, out, leak)
			}
		}
	}
	// Ordinary log text survives, including tool call IDs and short prefixes.
	for _, keep := range []string{
		"gemini.api_keys=4 web.token=set",
		"pane_id w5:p1 turn_id t-12",
		"monkey: banana-split",
		"call_00_xoxuwTT1s9a12esbdi0Z9019",
		"prefixes ghp_ github_pat_ xoxb- xoxp- AKIA alone",
	} {
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

func TestConfiguredSecretsRedactedWhenJSONEscaped(t *testing.T) {
	s1 := "my\"quoted\\secret&key<123>\nline2"
	s2 := `"leading-quote-secret`
	s3 := `trailing-slash-secret\`
	r := New(s1, s2, s3)

	marshalNoHTML := func(v any) string {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSuffix(buf.String(), "\n")
	}

	payload := map[string]string{"a": s1, "b": s2, "c": s3}
	htmlJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	rawJSON := marshalNoHTML(payload)
	doubleJSON, err := json.Marshal(map[string]string{"arguments": rawJSON})
	if err != nil {
		t.Fatal(err)
	}

	for name, in := range map[string]string{
		"json.Marshal":         string(htmlJSON),
		"SetEscapeHTML(false)": rawJSON,
		"double-encoded":       string(doubleJSON),
	} {
		out := r.String(in)
		for _, leak := range []string{"quoted", "leading-quote-secret", "trailing-slash-secret", "line2"} {
			if strings.Contains(out, leak) {
				t.Errorf("%s leaked %q:\n%s", name, leak, out)
			}
		}
		if !json.Valid([]byte(out)) {
			t.Errorf("%s became invalid JSON after redaction:\n%s", name, out)
		}
		if name == "double-encoded" {
			var outer map[string]string
			if err := json.Unmarshal([]byte(out), &outer); err != nil {
				t.Fatalf("unmarshal outer double-encoded JSON: %v", err)
			}
			if !json.Valid([]byte(outer["arguments"])) {
				t.Errorf("inner double-encoded JSON became invalid: %s", outer["arguments"])
			}
		}
	}

	section := "### assistant\n→ send_keys(" + rawJSON + ")\n\n"
	redSection := r.String(section)
	for _, leak := range []string{"quoted", "leading-quote-secret", "trailing-slash-secret", "line2"} {
		if strings.Contains(redSection, leak) {
			t.Errorf("section leaked %q:\n%s", leak, redSection)
		}
	}
}
