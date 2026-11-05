package cli

import (
	"slices"
	"testing"
)

// The agents get the user's environment to run (PATH, HOME, proxies, the
// tools' own login), except daemon settings and names loaded from its env file.
func TestScrubEnvStripsDaemonKeys(t *testing.T) {
	environ := []string{
		"PATH=/bin", "HOME=/h", "HTTPS_PROXY=http://p", "LANG=C", "FAKE_EXIT=0",
		"HERDR_PANE_ID=w1:p1", "CLAUDECODE=1", "CLAUDE_CODE_FOO=bar",
		"SHEEPDOG_WEB_TOKEN=s", "SHEEPDOG_APNS_KEY_ID=k",
		"GEMINI_API_KEY=g", "GEMINI_API_KEY_2=g2",
		"VERTEX_API_KEY=v", "VERTEX_PROJECT=p",
		"DEEPSEEK_API_KEY=d", "OPENAI_API_KEY=o", "OPENAI_ORCH_API_KEY=oo",
		"ANTHROPIC_API_KEY=a", "OPENROUTER_API_KEY=r",
		"MY_GROQ_KEY=q",
	}
	got := scrubEnv(environ, "MY_GROQ_KEY", "OPENROUTER_API_KEY")
	want := []string{"PATH=/bin", "HOME=/h", "HTTPS_PROXY=http://p", "LANG=C", "FAKE_EXIT=0",
		"OPENAI_API_KEY=o", "ANTHROPIC_API_KEY=a"}
	if !slices.Equal(got, want) {
		t.Errorf("scrubEnv = %v, want %v", got, want)
	}
}
