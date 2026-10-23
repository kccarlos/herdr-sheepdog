package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// leakyTransport fails every request with an error that quotes the URL
// and the Authorization header: the way a key in a query string or a
// header would reach a doctor line.
type leakyTransport struct{}

func (leakyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed for " + r.URL.String() + " auth=" + r.Header.Get("Authorization") + " key=" + r.Header.Get("x-goog-api-key"))
}

// TestReportNeverContainsSecrets plants a distinct fake value for every
// kind of secret — in the env file, the config file, the runtime log
// (as an older, unredacted sheepdog would have written it), the stdio
// file and the event log — and checks that none of them appears in the
// report, printed or written to a file.
func TestReportNeverContainsSecrets(t *testing.T) {
	secrets := map[string]string{
		"GEMINI_API_KEY":     "AIzaGeminiFakeFakeFake000000001",
		"GEMINI_API_KEY_2":   "AIzaGeminiFakeFakeFake000000002",
		"VERTEX_API_KEY":     "vertex-fake-secret-000000003",
		"DEEPSEEK_API_KEY":   "sk-deepseekfake000000000004",
		"OPENAI_API_KEY":     "sk-proj-openaifake0000000005",
		"OPENROUTER_API_KEY": "sk-or-v1-openrouterfake000006",
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	cfgPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	port := freePort(t)
	cfgBody := fmt.Sprintf("data_dir = %q\n[web]\nport = %d\n[deepseek]\nbase_url = \"http://127.0.0.1:9\"\n[[brain.openai_compat]]\nname = \"openrouter\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key_env = \"OPENROUTER_API_KEY\"\nmodel = \"x/y\"\n", data, port)
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}
	var env strings.Builder
	for k, v := range secrets {
		fmt.Fprintf(&env, "%s=%s\n", k, v)
		t.Setenv(k, "") // a value already in the environment would win over the file
		_ = os.Unsetenv(k)
	}
	env.WriteString("VERTEX_PROJECT=my-project\n")
	if err := os.WriteFile(envPath, []byte(env.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	// Logs written before redaction existed: every secret in the clear.
	if err := os.MkdirAll(daemon.LogDir(data), 0o700); err != nil {
		t.Fatal(err)
	}
	var raw strings.Builder
	for k, v := range secrets {
		fmt.Fprintf(&raw, "2026-10-14T00:00:00Z %s=%s GET /ws?token=%s\n", k, v, v)
	}
	_ = os.WriteFile(daemon.LogFile(data), []byte(raw.String()), 0o600)
	_ = os.WriteFile(daemon.StdioFile(data), []byte("panic: key "+secrets["GEMINI_API_KEY"]+"\n"), 0o600)
	ev := filepath.Join(data, "events", "2026-10-14.jsonl")
	_ = os.MkdirAll(filepath.Dir(ev), 0o700)
	_ = os.WriteFile(ev, []byte(`{"ts":"2026-10-14T00:00:00Z","type":"voice.connect_error","payload":{"error":"bad key `+secrets["OPENAI_API_KEY"]+`"}}`+"\n"), 0o600)

	old := onboardEndpoints
	onboardEndpoints = func() onboard.Endpoints {
		return onboard.Endpoints{GeminiBase: "http://127.0.0.1:9", OpenAIBase: "http://127.0.0.1:9/v1", HTTP: &http.Client{Transport: leakyTransport{}}}
	}
	t.Cleanup(func() { onboardEndpoints = old })

	outFile := filepath.Join(dir, "report.md")
	code, printed, errs := runCLI("doctor", "--report", "--config", cfgPath, "--env-file", envPath)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if code, _, errs := runCLI("doctor", "--report", "--out", outFile, "--config", cfgPath, "--env-file", envPath); code != 0 {
		t.Fatalf("--out exit %d: %s", code, errs)
	}
	written, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(outFile); st.Mode().Perm() != 0o600 {
		t.Errorf("report file mode %v", st.Mode().Perm())
	}
	for _, report := range []string{printed, string(written)} {
		for k, v := range secrets {
			if strings.Contains(report, v) {
				t.Errorf("%s's value leaked into the report", k)
			}
			// No 12-character slice of a secret survives either.
			if len(v) > 16 && strings.Contains(report, v[4:16]) {
				t.Errorf("part of %s leaked into the report", k)
			}
		}
		for _, want := range []string{"## sheepdog diagnostic report", "### Versions", "### Doctor", "### Status", "### Runtime log", "names only): ", "OPENAI_API_KEY", "VERTEX_PROJECT", "[REDACTED]", "voice.connect_error"} {
			if !strings.Contains(report, want) {
				t.Errorf("report lacks %q", want)
			}
		}
	}
}

// TestReportWithABrokenConfig: the report is most needed when the config
// does not load; it still comes out, with the error and the masked file.
func TestReportWithABrokenConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(cfgPath, []byte("[web]\ntoken = \"brokenCfgTokenFAKE000000\"\nport = \"x\"\n"), 0o600)
	code, out, errs := runCLI("doctor", "--report", "--config", cfgPath)
	if code != 1 && code != 0 {
		t.Fatalf("exit %d %s", code, errs)
	}
	if !strings.Contains(out, "config did not load") || !strings.Contains(out, "line 3, key web.port") {
		t.Errorf("no config error in the report:\n%s\nstderr: %s", out, errs)
	}
	if strings.Contains(out, "brokenCfgTokenFAKE") {
		t.Error("token leaked")
	}
}

func TestUpdate(t *testing.T) {
	var status int
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/kccarlos/herdr-sheepdog/releases" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	oldAPI, oldVersion, oldExe := releaseAPI, version, osExecutable
	t.Cleanup(func() { releaseAPI, version, osExecutable = oldAPI, oldVersion, oldExe })
	releaseAPI = srv.URL
	cache := t.TempDir()
	oldDir := updateDataDir
	t.Cleanup(func() { updateDataDir = oldDir })
	updateDataDir = func() string { return cache }
	osExecutable = func() (string, error) { return "/opt/homebrew/Cellar/sheepdog/0.1.0/bin/sheepdog", nil }
	version = "v0.1.0"

	status, body = 404, `{"message":"Not Found"}`
	code, out, _ := runCLI("update")
	if code != 0 || !strings.Contains(out, "No published release found for kccarlos/herdr-sheepdog: the repository is private or has no release yet.") {
		t.Errorf("no release: exit %d %q", code, out)
	}
	status, body = 200, `[{"tag_name":"ios-v9.0.0","html_url":"https://example/ios"},{"tag_name":"v0.2.0","html_url":"https://github.com/kccarlos/herdr-sheepdog/releases/tag/v0.2.0"}]`
	code, out, _ = runCLI("update")
	if code != 0 || !strings.Contains(out, "sheepdog v0.2.0 is available (you have v0.1.0)") || !strings.Contains(out, "brew upgrade sheepdog") {
		t.Errorf("newer: exit %d %q", code, out)
	}
	version = "v0.2.0"
	if code, out, _ = runCLI("update"); code != 0 || !strings.Contains(out, "is up to date") {
		t.Errorf("same: exit %d %q", code, out)
	}
	releaseAPI = "http://127.0.0.1:9" // offline
	if code, _, errs := runCLI("update"); code != 1 || !strings.Contains(errs, "Could not check for a new release") {
		t.Errorf("offline: exit %d %q", code, errs)
	}
}

func TestDetectInstallAndUpgradeSteps(t *testing.T) {
	plugin := t.TempDir()
	_ = os.MkdirAll(filepath.Join(plugin, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(plugin, "herdr-plugin.toml"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(plugin, "bin", "sheepdog"), nil, 0o755)
	if m := detectInstall(filepath.Join(plugin, "bin", "sheepdog")); m.Kind != "herdr plugin" || upgradeSteps(m, "")[0] != "herdr plugin update kccarlos.sheepdog" {
		t.Errorf("plugin: %+v", m)
	}
	if m := detectInstall("/opt/homebrew/Cellar/sheepdog/0.1.0/bin/sheepdog"); m.Kind != "homebrew" {
		t.Errorf("brew: %+v", m)
	}
	if m := detectInstall("/usr/local/bin/sheepdog-nowhere"); m.Kind != "binary" || !strings.HasPrefix(upgradeSteps(m, "https://x/rel")[0], "download it from https://x/rel") {
		t.Errorf("binary: %+v", m)
	}
}
