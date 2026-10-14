package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

const (
	initGeminiKey = "AIza-init-test-key-0123456789"
	initCompatKey = "sk-or-init-test-key"
)

// fakeKeyAPI accepts initGeminiKey on the Gemini model list and
// initCompatKey on an OpenAI-compatible /models; everything else is 401.
func fakeKeyAPI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1beta/models") && r.Header.Get("x-goog-api-key") == initGeminiKey:
			_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-3.5-flash"}]}`)
		case strings.HasSuffix(r.URL.Path, "/v1beta/models"):
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"message":"API key not valid. Please pass a valid API key."}}`)
		case strings.HasSuffix(r.URL.Path, "/models") && r.Header.Get("Authorization") == "Bearer "+initCompatKey:
			_, _ = io.WriteString(w, `{"data":[{"id":"vendor/model-a"}]}`)
		default:
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":{"message":"Authentication Fails"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	old := onboardEndpoints
	onboardEndpoints = func() onboard.Endpoints {
		return onboard.Endpoints{GeminiBase: srv.URL, OpenAIBase: srv.URL + "/v1"}
	}
	t.Cleanup(func() { onboardEndpoints = old })
	return srv
}

// noOnboardKeys clears every key variable the developer's shell may
// carry, so no test reads (or sends anywhere) a real key.
func noOnboardKeys(t *testing.T) {
	t.Helper()
	noKeys(t)
	for _, k := range []string{"GEMINI_API_KEY_4", "GEMINI_API_KEY_5", "GEMINI_API_KEY_6", "GEMINI_API_KEY_7", "GEMINI_API_KEY_8", "GEMINI_API_KEY_9", "OPENAI_API_KEY", "VERTEX_API_KEY", "VERTEX_PROJECT", "OPENROUTER_API_KEY", "SHEEPDOG_WEB_TOKEN"} {
		t.Setenv(k, "")
	}
}

func runInitTest(t *testing.T, stdin string, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := runInit(context.Background(), args, strings.NewReader(stdin), &out, &out)
	return code, out.String()
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// A new setup: the key is validated, written 0600 with a fresh long
// token, config.toml is written 0600 and loads; nothing prints the key.
func TestInitNonInteractiveNew(t *testing.T) {
	fakeKeyAPI(t)
	noOnboardKeys(t)
	t.Setenv("GEMINI_API_KEY", initGeminiKey)
	os.Unsetenv("SHEEPDOG_WEB_TOKEN")
	dir := t.TempDir()
	cfgPath, envPath := filepath.Join(dir, "cfg", "config.toml"), filepath.Join(dir, "cfg", ".env")
	code, out := runInitTest(t, "", "--non-interactive", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	if strings.Contains(out, initGeminiKey) {
		t.Error("the key was printed")
	}
	env, err := onboard.ReadEnvFile(envPath)
	if err != nil || env["GEMINI_API_KEY"] != initGeminiKey || len(env["SHEEPDOG_WEB_TOKEN"]) < 40 {
		t.Fatalf("env file = %v (%v)", len(env), err)
	}
	if strings.Contains(out, env["SHEEPDOG_WEB_TOKEN"]) {
		t.Error("the token was printed")
	}
	if mode(t, envPath) != 0o600 || mode(t, cfgPath) != 0o600 {
		t.Errorf("modes %o %o", mode(t, envPath), mode(t, cfgPath))
	}
	cfg, err := config.Load(cfgPath)
	if err != nil || cfg.Brain.Provider != "gemini" {
		t.Errorf("config: %+v %v", cfg.Brain.Provider, err)
	}
}

// An existing config and token are never changed; only new keys go to
// the env file.
func TestInitKeepsExistingTokenAndConfig(t *testing.T) {
	fakeKeyAPI(t)
	noOnboardKeys(t)
	t.Setenv("GEMINI_API_KEY", initGeminiKey)
	os.Unsetenv("SHEEPDOG_WEB_TOKEN")
	dir := t.TempDir()
	cfgPath, envPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, ".env")
	orig := "[web]\nport = 7788\n"
	if err := os.WriteFile(cfgPath, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte("SHEEPDOG_WEB_TOKEN=phone-paired-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	code, out := runInitTest(t, "", "--non-interactive", "--config", cfgPath, "--orchestrator", "compat", "--compat-name", "openrouter", "--compat-base-url", "https://example.invalid/api/v1", "--compat-model", "vendor/model-a", "--skip-validate")
	if code == 0 {
		t.Fatalf("compat without a key succeeded: %s", out)
	}
	t.Setenv("OPENROUTER_API_KEY", initCompatKey)
	code, out = runInitTest(t, "", "--non-interactive", "--config", cfgPath, "--orchestrator", "compat", "--compat-name", "openrouter", "--compat-base-url", "https://example.invalid/api/v1", "--compat-model", "vendor/model-a", "--skip-validate")
	if code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	if data, _ := os.ReadFile(cfgPath); string(data) != orig {
		t.Errorf("config changed: %q", data)
	}
	if !strings.Contains(out, `[[brain.openai_compat]]`) || !strings.Contains(out, `provider = "openrouter"`) {
		t.Errorf("no config hint: %s", out)
	}
	env, _ := onboard.ReadEnvFile(envPath)
	if env["SHEEPDOG_WEB_TOKEN"] != "phone-paired-token" || env["OPENROUTER_API_KEY"] != initCompatKey || env["GEMINI_API_KEY"] != initGeminiKey {
		t.Errorf("env = token %q, keys set %v %v", env["SHEEPDOG_WEB_TOKEN"], env["OPENROUTER_API_KEY"] != "", env["GEMINI_API_KEY"] != "")
	}
}

// A token in the environment counts as existing too: a new config does
// not get a second one.
func TestInitTokenInEnvironment(t *testing.T) {
	fakeKeyAPI(t)
	noOnboardKeys(t)
	t.Setenv("GEMINI_API_KEY", initGeminiKey)
	t.Setenv("SHEEPDOG_WEB_TOKEN", "from-the-environment")
	dir := t.TempDir()
	if code, out := runInitTest(t, "", "--non-interactive", "--config", filepath.Join(dir, "config.toml")); code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	env, _ := onboard.ReadEnvFile(filepath.Join(dir, ".env"))
	if _, ok := env["SHEEPDOG_WEB_TOKEN"]; ok {
		t.Error("a token was generated over the environment's")
	}
}

// A rejected key stops init before anything is written.
func TestInitInvalidKeyWritesNothing(t *testing.T) {
	fakeKeyAPI(t)
	noOnboardKeys(t)
	t.Setenv("GEMINI_API_KEY", "AIza-wrong")
	dir := t.TempDir()
	code, out := runInitTest(t, "", "--non-interactive", "--config", filepath.Join(dir, "config.toml"))
	if code == 0 || !strings.Contains(out, "rejected the key") {
		t.Fatalf("code %d: %s", code, out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files written: %v", entries)
	}
	if strings.Contains(out, "AIza-wrong") {
		t.Error("the key was printed")
	}
}

// Interactive: pick the compat orchestrator from the menu, answer the
// questions, retry a wrong key; the written config loads with the entry
// as the provider and its model.
func TestInitInteractiveCompat(t *testing.T) {
	fakeKeyAPI(t)
	noOnboardKeys(t)
	os.Unsetenv("SHEEPDOG_WEB_TOKEN")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	stdin := strings.Join([]string{"3", "openrouter", "", "vendor/model-a", "AIza-wrong", initGeminiKey, initCompatKey}, "\n") + "\n"
	code, out := runInitTest(t, stdin, "--config", cfgPath)
	if code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	if strings.Contains(out, initGeminiKey) || strings.Contains(out, initCompatKey) {
		t.Error("a key was printed")
	}
	t.Setenv("OPENROUTER_API_KEY", initCompatKey)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := cfg.Brain.CompatProvider("openrouter")
	if !ok || cfg.Brain.Provider != "openrouter" || cfg.Brain.Model != "vendor/model-a" || c.BaseURL != "https://openrouter.ai/api/v1" || c.APIKey != initCompatKey {
		t.Errorf("brain = %s/%s entry %+v", cfg.Brain.Provider, cfg.Brain.Model, c.BaseURL)
	}
}

// doctor prints one line per check and exits 1 on a failure (herdr is
// not running here).
func TestDoctorCommand(t *testing.T) {
	fakeKeyAPI(t)
	noOnboardKeys(t)
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("GEMINI_API_KEY="+initGeminiKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("GEMINI_API_KEY")
	t.Cleanup(func() { os.Unsetenv("GEMINI_API_KEY") })
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	cfgPath := filepath.Join(dir, "config.toml")
	body := "[herdr]\nsocket = " + strconv.Quote(filepath.Join(dir, "no.sock")) + "\n[web]\nport = " + strconv.Itoa(port) + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := runDoctor(context.Background(), []string{"--config", cfgPath, "--env-file", envPath}, &out, &out)
	if code != 1 {
		t.Errorf("code = %d", code)
	}
	s := out.String()
	for _, want := range []string{"ok    config", "FAIL  herdr", "ok    gemini key", "ok    web port", "ok    env file"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, initGeminiKey) {
		t.Error("the key was printed")
	}
}
