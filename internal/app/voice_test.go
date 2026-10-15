package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
)

// TestVoiceProviderSwitchPersists: with only an OpenAI key the daemon
// starts on GPT-Live (voice.provider = openai) without dialing anything
// (the session is lazy), lists Gemini as unavailable, refuses a switch to
// it, and a runtime pick kept in the state file wins over voice.provider
// on the next start. No network: the GPT-Live session never opens
// because no /ws client is attached.
func TestVoiceProviderSwitchPersists(t *testing.T) {
	cfg := testConfig(t)
	cfg.Voice.OpenAI.APIKey = "sk-test-not-used"
	cfg.Voice.Provider = "openai"
	cfg.Brain.Enabled = false

	if _, err := Build(cfg, Options{Voice: VoiceGemini, Herdr: HerdrMock}); err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Fatalf("--voice=gemini without a key: %v", err)
	}
	// Without the provider's key the daemon still builds: setup mode.
	noOpenAI := cfg
	noOpenAI.Voice.OpenAI.APIKey = ""
	if a, err := Build(noOpenAI, Options{Herdr: HerdrMock}); err != nil || !a.SetupNeeded() {
		t.Fatalf("openai without a key: %v (setup needed: %v)", err, err == nil && a.SetupNeeded())
	}

	run := func(check func(url string)) {
		t.Helper()
		a, err := Build(cfg, Options{Herdr: HerdrMock, Drain: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Listen(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		waitFor(t, 5*time.Second, func() bool {
			resp, err := http.Get(a.URL() + "/api/hello")
			if err == nil {
				_ = resp.Body.Close()
			}
			return err == nil
		}, "daemon up")
		check(a.URL())
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return")
		}
	}

	run(func(url string) {
		var got web.VoiceProvidersResponse
		getJSON(t, url+"/api/voice/providers", &got)
		if got.Provider != "openai" || got.Model != "gpt-live-1" || len(got.Providers) != 2 {
			t.Fatalf("providers = %+v", got)
		}
		if g := got.Providers[0]; g.ID != "gemini" || g.Available || g.Reason != "no GEMINI_API_KEY" {
			t.Errorf("gemini = %+v", g)
		}
		if o := got.Providers[1]; o.ID != "openai" || !o.Available || o.Billing != "per_minute" || o.Label != "GPT-Live" {
			t.Errorf("openai = %+v", o)
		}
		req, _ := http.NewRequest(http.MethodPut, url+"/api/voice/provider", strings.NewReader(`{"provider":"gemini"}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("PUT gemini = %d", resp.StatusCode)
		}
		var hello web.HelloResponse
		getJSON(t, url+"/api/hello", &hello)
		if hello.VoiceProvider != "openai" || hello.VoiceModel != "gpt-live-1" {
			t.Errorf("hello = %+v", hello)
		}
	})

	// The state file's pick wins over voice.provider: configure gemini
	// (now with a key) and a stored "openai".
	cfg.Gemini.APIKeys = []string{"AIza-test-not-used"}
	cfg.Voice.Provider = "gemini"
	a, err := Build(cfg, Options{Herdr: HerdrMock, Voice: VoiceOff})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.UpdateSession(func(ss *state.Session) { ss.VoiceProvider = "openai" }); err != nil {
		t.Fatal(err)
	}
	_ = a.store.Close()
	_ = a.elog.Close()
	b, err := Build(cfg, Options{Herdr: HerdrMock})
	if err != nil {
		t.Fatal(err)
	}
	b.restoreVoiceProvider()
	if b.VoiceProvider() != "openai" {
		t.Errorf("restored provider = %q, want openai", b.VoiceProvider())
	}
	_ = b.store.Close()
	_ = b.elog.Close()
	// --voice=gemini overrides the stored pick for one run.
	c, err := Build(cfg, Options{Herdr: HerdrMock, Voice: VoiceGemini})
	if err != nil {
		t.Fatal(err)
	}
	c.restoreVoiceProvider()
	if c.VoiceProvider() != "gemini" {
		t.Errorf("--voice=gemini provider = %q", c.VoiceProvider())
	}
	_ = c.store.Close()
	_ = c.elog.Close()
}

// brain.reply_wait_s reaches the supervisor as configured, except with
// --voice=off, where the reply is always the tool result (the /debug
// text router shows it; no voice said "on it").
func TestReplyWaitWiring(t *testing.T) {
	cfg := testConfig(t)
	cfg.Brain.ReplyWaitS = 3
	if got := replyWait(cfg, Options{Voice: VoiceOn}); got != 3*time.Second {
		t.Errorf("voice on: %v, want 3s", got)
	}
	if got := replyWait(cfg, Options{Voice: VoiceOff}); got >= 0 {
		t.Errorf("--voice=off: %v, want negative (synchronous)", got)
	}
}
