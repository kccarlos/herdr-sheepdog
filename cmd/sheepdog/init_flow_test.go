package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// Fake keys of the terminal-flow tests. No real key is ever used: the
// package's TestMain clears the shell's, and every call goes to a fake.
const (
	flowGemini    = "AIza-flow-gemini-0123456789"
	flowOpenAI    = "sk-flow-openai-0123456789"
	flowAnthropic = "sk-ant-flow-0123456789"
	flowDeepSeek  = "sk-flow-deepseek-0123456789"
	flowRouter    = "sk-or-flow-0123456789"
	flowVertex    = "AQ.flow-vertex-0123456789"
)

var allFlowKeys = []string{flowGemini, flowOpenAI, flowAnthropic, flowDeepSeek, flowRouter, flowVertex}

// hostRewrite sends a request for another host (a preset's real API root)
// to the fake server under /h/<host>/..., and refuses the *.down hosts
// like a server that is not running.
type hostRewrite struct{ target *url.URL }

func (h hostRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Hostname(), ".down") {
		return nil, fmt.Errorf("dial tcp %s: connection refused", r.URL.Host)
	}
	if r.URL.Host == h.target.Host {
		return http.DefaultTransport.RoundTrip(r)
	}
	r2 := r.Clone(r.Context())
	r2.URL.Scheme, r2.URL.Host = h.target.Scheme, h.target.Host
	r2.URL.Path = "/h/" + r.URL.Host + r.URL.Path
	r2.Host = h.target.Host
	return http.DefaultTransport.RoundTrip(r2)
}

func jsonOK(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

// fakeFlowAPI is every API the flow calls: Gemini (model list), OpenAI,
// Anthropic, Vertex (countTokens), DeepSeek, OpenRouter and a local
// Ollama. It records whether each one saw a request.
func fakeFlowAPI(t *testing.T) (calls *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		p := r.URL.Path
		switch {
		case p == "/v1beta/models":
			if r.Header.Get("x-goog-api-key") != flowGemini {
				w.WriteHeader(400)
				jsonOK(w, `{"error":{"message":"API key not valid. Please pass a valid API key."}}`)
				return
			}
			jsonOK(w, `{"models":[
				{"name":"models/gemini-3.8-live","supportedGenerationMethods":["bidiGenerateContent"]},
				{"name":"models/gemini-3.5-live","supportedGenerationMethods":["bidiGenerateContent"]},
				{"name":"models/gemini-3.5-flash","supportedGenerationMethods":["generateContent"]},
				{"name":"models/gemini-3.8-flash","supportedGenerationMethods":["generateContent"]}]}`)
		case p == "/v1/models" && r.Header.Get("x-api-key") != "":
			if r.Header.Get("x-api-key") != flowAnthropic {
				w.WriteHeader(401)
				jsonOK(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
				return
			}
			jsonOK(w, `{"data":[{"id":"claude-opus-5-5","display_name":"Claude Opus 5.5","type":"model","created_at":"2026-09-01T00:00:00Z"},{"id":"claude-sonnet-5-5","display_name":"Claude Sonnet 5.5","type":"model","created_at":"2026-09-01T00:00:00Z"}],"has_more":false}`)
		case p == "/v1/models", p == "/h/api.openai.com/v1/models":
			if bearer != flowOpenAI {
				w.WriteHeader(401)
				jsonOK(w, `{"error":{"message":"Incorrect API key"}}`)
				return
			}
			jsonOK(w, `{"data":[{"id":"gpt-live-1"},{"id":"gpt-live-2"},{"id":"gpt-5-mini"},{"id":"text-embedding-3-small"}]}`)
		case strings.Contains(p, ":countTokens"):
			if r.Header.Get("x-goog-api-key") != flowVertex {
				w.WriteHeader(400)
				jsonOK(w, `{"error":{"code":400,"message":"API key not valid.","status":"INVALID_ARGUMENT","details":[{"reason":"API_KEY_INVALID"}]}}`)
				return
			}
			jsonOK(w, `{"totalTokens":1}`)
		case p == "/h/api.deepseek.com/models":
			if bearer != flowDeepSeek {
				w.WriteHeader(401)
				jsonOK(w, `{"error":{"message":"Authentication Fails"}}`)
				return
			}
			jsonOK(w, `{"data":[{"id":"deepseek-flash"}]}`)
		case p == "/h/openrouter.ai/api/v1/models":
			if bearer != flowRouter {
				w.WriteHeader(401)
				jsonOK(w, `{"error":{"message":"No auth credentials found"}}`)
				return
			}
			jsonOK(w, `{"data":[
				{"id":"vendor/no-tools","supported_parameters":["temperature"]},
				{"id":"vendor/with-tools","supported_parameters":["temperature","tools"]},
				{"id":"vendor/also-tools","supported_parameters":["tools"]}]}`)
		case p == "/h/ollama.test/v1/models":
			jsonOK(w, `{"data":[{"id":"nomic-embed-text"},{"id":"llama3.2"}]}`)
		case strings.HasSuffix(p, "/chat/completions"):
			jsonOK(w, `{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"report_ready","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	oldEp, oldPresets := onboardEndpoints, initPresets
	onboardEndpoints = func() onboard.Endpoints {
		return onboard.Endpoints{GeminiBase: srv.URL, OpenAIBase: srv.URL + "/v1", VertexBase: srv.URL, AnthropicBase: srv.URL,
			HTTP: &http.Client{Transport: hostRewrite{target}}}
	}
	initPresets = func() []onboard.Preset {
		ps := onboard.Presets()
		for i := range ps {
			switch ps[i].ID {
			case "ollama":
				ps[i].BaseURL = "http://ollama.test/v1"
			case "lmstudio":
				ps[i].BaseURL = "http://lmstudio.down/v1"
			}
		}
		return ps
	}
	t.Cleanup(func() { onboardEndpoints, initPresets = oldEp, oldPresets })
	return &seen
}

// keyPrompts records the label of every hidden prompt for the test.
func keyPrompts(t *testing.T) *[]string {
	t.Helper()
	var labels []string
	secretPromptHook = func(l string) { labels = append(labels, l) }
	t.Cleanup(func() { secretPromptHook = nil })
	return &labels
}

type flowRun struct {
	code           int
	out            string
	cfgPath, envPa string
	env            map[string]string
	cfg            string
}

// runFlow runs init in a fresh directory with the scripted answers.
func runFlow(t *testing.T, stdin string, args ...string) flowRun {
	t.Helper()
	dir := t.TempDir()
	r := flowRun{cfgPath: filepath.Join(dir, "config.toml"), envPa: filepath.Join(dir, ".env")}
	fakeService(t)
	var out bytes.Buffer
	r.code = runInit(context.Background(), append([]string{"--config", r.cfgPath}, args...), strings.NewReader(stdin), &out, &out)
	r.out = out.String()
	r.env, _ = onboard.ReadEnvFile(r.envPa)
	if b, err := os.ReadFile(r.cfgPath); err == nil {
		r.cfg = string(b)
	}
	for _, k := range allFlowKeys {
		if strings.Contains(r.out, k) {
			t.Errorf("a key reached the output")
		}
	}
	if tok := r.env["SHEEPDOG_WEB_TOKEN"]; tok != "" && strings.Contains(r.out, tok) {
		t.Error("the web token reached the output")
	}
	return r
}

// webSave is what the setup page's save writes for the same choices: the
// shared resolve and save, on a fresh directory.
func webSave(t *testing.T, in onboard.ModelsInput, keys map[string]string) (env map[string]string, cfg string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath, envPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, ".env")
	ch, msg := onboard.ResolveModels(config.Default(), initPresets(), in)
	if msg != "" {
		t.Fatal(msg)
	}
	if _, err := onboard.SaveModels(cfgPath, envPath, map[string]string{}, ch, keys); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		if err := onboard.WriteConfig(cfgPath, "gemini", nil); err != nil {
			t.Fatal(err)
		}
	}
	env, _ = onboard.ReadEnvFile(envPath)
	b, _ := os.ReadFile(cfgPath)
	return env, string(b)
}

// sameSaved compares a terminal run with the page's save (the web token
// is the terminal's own).
func sameSaved(t *testing.T, r flowRun, wantEnv map[string]string, wantCfg string) {
	t.Helper()
	got := map[string]string{}
	for k, v := range r.env {
		if k != "SHEEPDOG_WEB_TOKEN" {
			got[k] = v
		}
	}
	if len(got) != len(wantEnv) {
		t.Errorf("env names: terminal %v, page %v", names(got), names(wantEnv))
	}
	for k, v := range wantEnv {
		if got[k] != v {
			t.Errorf("env %s: terminal %q, page %q", k, got[k], v)
		}
	}
	if r.cfg != wantCfg {
		t.Errorf("config differs from the page's:\n%s\n---\n%s", r.cfg, wantCfg)
	}
}

func names(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func answers(a ...string) string { return strings.Join(a, "\n") + "\n" }

func TestFlowCards(t *testing.T) {
	fakeFlowAPI(t)
	noOnboardKeys(t)
	prompts := keyPrompts(t)
	cases := []struct {
		name    string
		stdin   string
		summary string
		keys    map[string]string
		input   onboard.ModelsInput
		labels  int
	}{
		{
			// 1 Recommended: Gemini key; voice model and voice kept; the
			// Anthropic key and model kept; no tool test.
			name:    "recommended",
			stdin:   answers("1", flowGemini, "", "", flowAnthropic, "", "n", "n"),
			summary: "Summary: You talk to gemini-3.8-live (Google AI Studio). Your agents are handled by claude-opus-5-5 (Anthropic). Cost: free with daily limits · $4/$20 per million tokens",
			keys:    map[string]string{"GEMINI_API_KEY": flowGemini, "ANTHROPIC_API_KEY": flowAnthropic},
			input:   onboard.ModelsInput{Voice: "gemini", Orchestrator: "anthropic"},
			labels:  2,
		},
		{
			// 2 Cost-effective: DeepSeek, and the second Live model and a voice.
			name:    "cost",
			stdin:   answers("2", flowGemini, "2", "kore", flowDeepSeek, "n"),
			summary: "Summary: You talk to gemini-3.5-live (Google AI Studio). Your agents are handled by deepseek-flash (DeepSeek). Cost: free with daily limits · pay per use",
			keys:    map[string]string{"GEMINI_API_KEY": flowGemini, "DEEPSEEK_API_KEY": flowDeepSeek},
			input:   onboard.ModelsInput{Voice: "gemini", Orchestrator: "deepseek", VoiceModel: "gemini-3.5-live", VoiceName: "Kore"},
			labels:  2,
		},
		{
			// 3 Free quick start: one key for both; the orchestrator
			// model is the second one listed (the default is first).
			name:    "free",
			stdin:   answers("3", flowGemini, "", "", "2", "n"),
			summary: "Summary: You talk to gemini-3.8-live (Google AI Studio). Your agents are handled by gemini-3.8-flash (Gemini). Cost: free with daily limits · free with daily limits",
			keys:    map[string]string{"GEMINI_API_KEY": flowGemini},
			input:   onboard.ModelsInput{Voice: "gemini", Orchestrator: "gemini", GeminiModel: "gemini-3.8-flash"},
			labels:  1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			*prompts = nil
			r := runFlow(t, c.stdin)
			if r.code != 0 {
				t.Fatalf("code %d\n%s", r.code, r.out)
			}
			if !strings.Contains(r.out, c.summary) {
				t.Errorf("summary missing:\n%s", r.out)
			}
			if len(*prompts) != c.labels {
				t.Errorf("hidden key prompts: %v", *prompts)
			}
			for env, v := range c.keys {
				if r.env[env] != v {
					t.Errorf("%s not saved", env)
				}
			}
			wantEnv, wantCfg := webSave(t, c.input, nil)
			for k, v := range c.keys {
				wantEnv[k] = v
			}
			sameSaved(t, r, wantEnv, wantCfg)
			if mode(t, r.envPa) != 0o600 || mode(t, r.cfgPath) != 0o600 {
				t.Error("files are not 0600")
			}
		})
	}
}

func TestFlowDefaultCardIsRecommendedOnAFreshInstall(t *testing.T) {
	fakeFlowAPI(t)
	noOnboardKeys(t)
	// Enter at the first question takes the default card.
	r := runFlow(t, answers("", flowGemini, "", "", flowAnthropic, "", "n", "n"))
	if r.code != 0 || !strings.Contains(r.out, "choice [1]") || !strings.Contains(r.out, "Starting point: Recommended") || r.env["SHEEPDOG_BRAIN_PROVIDER"] != "anthropic" {
		t.Fatalf("code %d\n%s", r.code, r.out)
	}
}

func TestFlowCustomOllama(t *testing.T) {
	fakeFlowAPI(t)
	noOnboardKeys(t)
	prompts := keyPrompts(t)
	// Custom; voice Gemini and its key (model and voice kept); the
	// orchestrator Ollama (7): found running, the embedding model is not
	// the suggestion, pick llama3.2 by Enter; no tool test.
	r := runFlow(t, answers("4", "1", flowGemini, "", "", "7", "", "n", "n"))
	if r.code != 0 {
		t.Fatalf("code %d\n%s", r.code, r.out)
	}
	if !strings.Contains(r.out, "Ollama is running (2 models)") || !strings.Contains(r.out, "Summary: You talk to gemini-3.8-live (Google AI Studio). Your agents are handled by llama3.2 (Ollama). Cost: free with daily limits · free on this computer") {
		t.Errorf("output:\n%s", r.out)
	}
	if len(*prompts) != 1 {
		t.Errorf("key prompts %v: Ollama needs no key", *prompts)
	}
	for _, k := range allFlowKeys[1:] {
		for _, v := range r.env {
			if v == k {
				t.Error("another key was saved")
			}
		}
	}
	in := onboard.ModelsInput{Voice: "gemini", Orchestrator: "ollama"}
	in.Compat.Model = "llama3.2"
	wantEnv, wantCfg := webSave(t, in, nil)
	wantEnv["GEMINI_API_KEY"] = flowGemini
	sameSaved(t, r, wantEnv, wantCfg)
	if !strings.Contains(r.cfg, `name = "ollama"`) || !strings.Contains(r.cfg, `base_url = "http://localhost:11434/v1"`) && !strings.Contains(r.cfg, "ollama.test") {
		t.Errorf("config:\n%s", r.cfg)
	}
}

func TestFlowLocalServerNotRunning(t *testing.T) {
	fakeFlowAPI(t)
	noOnboardKeys(t)
	// LM Studio (8) is down: it is reported with the fix, "s" types the
	// model name instead.
	r := runFlow(t, answers("4", "1", flowGemini, "", "", "8", "s", "my-model", "n", "n"))
	if r.code != 0 || !strings.Contains(r.out, "LM Studio is not running. Start LM Studio, then press Check again.") || r.env["SHEEPDOG_BRAIN_MODEL"] != "my-model" {
		t.Fatalf("code %d\n%s", r.code, r.out)
	}
}

func TestFlowOpenRouterListsToolModelsFirst(t *testing.T) {
	fakeFlowAPI(t)
	noOnboardKeys(t)
	prompts := keyPrompts(t)
	// OpenRouter (5): the key is hidden; models with tool calling come
	// first and are marked; "1" picks the first of them.
	r := runFlow(t, answers("4", "1", flowGemini, "", "", "5", flowRouter, "1", "n", "n"))
	if r.code != 0 {
		t.Fatalf("code %d\n%s", r.code, r.out)
	}
	i, j, k := strings.Index(r.out, "1) vendor/with-tools (default, tool calling)"), strings.Index(r.out, "2) vendor/also-tools (tool calling)"), strings.Index(r.out, "3) vendor/no-tools (no tool calling)")
	if i < 0 || j < i || k < j {
		t.Errorf("list order:\n%s", r.out)
	}
	if r.env["SHEEPDOG_BRAIN_PROVIDER"] != "openrouter" || r.env["SHEEPDOG_BRAIN_MODEL"] != "vendor/with-tools" || r.env["OPENROUTER_API_KEY"] != flowRouter {
		t.Errorf("env %v", names(r.env))
	}
	if len(*prompts) != 2 || !strings.Contains((*prompts)[1], "openrouter API key") {
		t.Errorf("prompts %v", *prompts)
	}
}

func TestFlowGPTLiveAndOpenAIOrchestratorReusingTheKey(t *testing.T) {
	fakeFlowAPI(t)
	noOnboardKeys(t)
	prompts := keyPrompts(t)
	// Custom; voice GPT-Live (3) + its key, model 2, voice kept; the
	// orchestrator OpenAI (6): reuse the voice key (y); model: the newest
	// small chat model is suggested (Enter); test tool calling (y).
	r := runFlow(t, answers("4", "3", flowOpenAI, "2", "", "6", "y", "", "y", "n"))
	if r.code != 0 {
		t.Fatalf("code %d\n%s", r.code, r.out)
	}
	if len(*prompts) != 1 {
		t.Errorf("key prompts %v: the voice key is reused", *prompts)
	}
	if r.env["SHEEPDOG_VOICE_PROVIDER"] != "openai" || r.env["SHEEPDOG_VOICE_OPENAI_MODEL"] != "gpt-live-2" || r.env["OPENAI_API_KEY"] != flowOpenAI {
		t.Errorf("voice env %v", names(r.env))
	}
	if _, ok := r.env["OPENAI_ORCH_API_KEY"]; ok {
		t.Error("the voice key was copied to the orchestrator's entry")
	}
	if r.env["SHEEPDOG_BRAIN_PROVIDER"] != "openai" || r.env["SHEEPDOG_BRAIN_MODEL"] != "gpt-5-mini" || !strings.Contains(r.cfg, `api_key_env = "OPENAI_API_KEY"`) {
		t.Errorf("brain %q %q\n%s", r.env["SHEEPDOG_BRAIN_PROVIDER"], r.env["SHEEPDOG_BRAIN_MODEL"], r.cfg)
	}
	if !strings.Contains(r.out, "openai tools") || !strings.Contains(r.out, "Cost: $0.05/min of open session · pay per use") {
		t.Errorf("output:\n%s", r.out)
	}
	in := onboard.ModelsInput{Voice: "openai", Orchestrator: "openai", VoiceModel: "gpt-live-2"}
	in.Compat.Model, in.Compat.ReuseKey = "gpt-5-mini", true
	wantEnv, wantCfg := webSave(t, in, nil)
	wantEnv["OPENAI_API_KEY"] = flowOpenAI
	sameSaved(t, r, wantEnv, wantCfg)
}

func TestFlowVertexVoiceAndOrchestrator(t *testing.T) {
	fakeFlowAPI(t)
	noOnboardKeys(t)
	prompts := keyPrompts(t)
	// Custom; voice Vertex (2): project, location kept, key; Live model
	// and voice kept; orchestrator Vertex (4): its model kept.
	r := runFlow(t, answers("4", "2", "my-project", "", flowVertex, "", "", "4", ""))
	if r.code != 0 {
		t.Fatalf("code %d\n%s", r.code, r.out)
	}
	if len(*prompts) != 1 {
		t.Errorf("key prompts %v: the project and key are asked once", *prompts)
	}
	if r.env["VERTEX_API_KEY"] != flowVertex || r.env["SHEEPDOG_GEMINI_BACKEND"] != "vertex" || r.env["SHEEPDOG_BRAIN_PROVIDER"] != "vertex" {
		t.Errorf("env %v", names(r.env))
	}
	if !strings.Contains(r.cfg, `project = "my-project"`) || !strings.Contains(r.cfg, `location = "us-central1"`) {
		t.Errorf("config:\n%s", r.cfg)
	}
	if !strings.Contains(r.out, "Google Vertex AI") || !strings.Contains(r.out, "billed to your Google Cloud project · billed to your Google Cloud project") {
		t.Errorf("summary:\n%s", r.out)
	}
	in := onboard.ModelsInput{Voice: "vertex", Orchestrator: "vertex"}
	in.Vertex.Project = "my-project"
	wantEnv, wantCfg := webSave(t, in, nil)
	wantEnv["VERTEX_API_KEY"] = flowVertex
	sameSaved(t, r, wantEnv, wantCfg)
}

func TestFlowWrongKeyIsRetriedAndNeverPrinted(t *testing.T) {
	fakeFlowAPI(t)
	noOnboardKeys(t)
	r := runFlow(t, answers("3", "AIza-flow-wrong-key", flowGemini, "", "", "", "n"))
	if r.code != 0 || !strings.Contains(r.out, "rejected the key") || strings.Contains(r.out, "AIza-flow-wrong-key") {
		t.Fatalf("code %d\n%s", r.code, r.out)
	}
}

func TestFlowNonInteractiveFlags(t *testing.T) {
	fakeFlowAPI(t)
	noOnboardKeys(t)
	t.Setenv("GEMINI_API_KEY", flowGemini)
	t.Setenv("OPENROUTER_API_KEY", flowRouter)
	// A card.
	t.Setenv("DEEPSEEK_API_KEY", flowDeepSeek)
	r := runFlow(t, "", "--non-interactive", "--card", "cost", "--voice-name", "Puck")
	if r.code != 0 || r.env["SHEEPDOG_BRAIN_PROVIDER"] != "deepseek" || r.env["DEEPSEEK_API_KEY"] != flowDeepSeek || !strings.Contains(r.out, "Starting point: Cost-effective") {
		t.Fatalf("cost: code %d\n%s", r.code, r.out)
	}
	// Custom with flags: a preset, a model, a Live model.
	r = runFlow(t, "", "--non-interactive", "--orchestrator", "openrouter", "--model", "vendor/with-tools", "--voice-model", "gemini-3.5-live")
	if r.code != 0 || r.env["SHEEPDOG_BRAIN_PROVIDER"] != "openrouter" || r.env["SHEEPDOG_BRAIN_MODEL"] != "vendor/with-tools" || r.env["SHEEPDOG_GEMINI_MODEL"] != "gemini-3.5-live" {
		t.Fatalf("openrouter: code %d\n%s", r.code, r.out)
	}
	// A local preset picks its suggested model; the key checks and model
	// list go to the fake.
	r = runFlow(t, "", "--non-interactive", "--orchestrator", "ollama")
	if r.code != 0 || r.env["SHEEPDOG_BRAIN_MODEL"] != "llama3.2" {
		t.Fatalf("ollama: code %d\n%s", r.code, r.out)
	}
	// A missing key or an unknown choice is an error and writes nothing.
	t.Setenv("ANTHROPIC_API_KEY", "")
	r = runFlow(t, "", "--non-interactive", "--card", "recommended")
	if r.code == 0 || !strings.Contains(r.out, "ANTHROPIC_API_KEY") || r.env != nil && len(r.env) > 0 {
		t.Errorf("recommended without a key: code %d\n%s", r.code, r.out)
	}
	if r := runFlow(t, "", "--non-interactive", "--card", "nope"); r.code == 0 || !strings.Contains(r.out, "--card") {
		t.Errorf("unknown card: %d %s", r.code, r.out)
	}
	if r := runFlow(t, "", "--non-interactive", "--orchestrator", "nope"); r.code == 0 {
		t.Errorf("unknown orchestrator: %d %s", r.code, r.out)
	}
}

func TestFlowNonInteractiveVertexAndToolTest(t *testing.T) {
	fakeFlowAPI(t)
	noOnboardKeys(t)
	t.Setenv("VERTEX_API_KEY", flowVertex)
	t.Setenv("ANTHROPIC_API_KEY", flowAnthropic)
	r := runFlow(t, "", "--non-interactive", "--voice", "vertex", "--vertex-project", "p-1", "--vertex-location", "us-central1", "--orchestrator", "anthropic", "--model", "claude-sonnet-5-5")
	if r.code != 0 || r.env["VERTEX_API_KEY"] != flowVertex || r.env["SHEEPDOG_BRAIN_MODEL"] != "claude-sonnet-5-5" || !strings.Contains(r.cfg, `project = "p-1"`) {
		t.Fatalf("code %d\n%s", r.code, r.out)
	}
	if !strings.Contains(r.out, "$2/$10 per million tokens") {
		t.Errorf("summary:\n%s", r.out)
	}
}

func TestFlowLegacyFlagsStillWork(t *testing.T) {
	fakeFlowAPI(t)
	noOnboardKeys(t)
	t.Setenv("GEMINI_API_KEY", flowGemini)
	t.Setenv("MY_API_KEY", "sk-flow-mine")
	r := runFlow(t, "", "--non-interactive", "--orchestrator", "compat", "--compat-name", "mine", "--compat-base-url", "http://mine.down/v1", "--compat-model", "m1", "--compat-key-env", "MY_API_KEY", "--skip-validate")
	if r.code != 0 || r.env["MY_API_KEY"] != "sk-flow-mine" || r.env["SHEEPDOG_BRAIN_PROVIDER"] != "mine" || !strings.Contains(r.cfg, `api_key_env = "MY_API_KEY"`) {
		t.Fatalf("code %d\n%s\n%s", r.code, r.out, r.cfg)
	}
}

// The state the page loads names the same cards and summary the terminal
// printed.
func TestStartTableIsJSONServedToThePage(t *testing.T) {
	b, err := json.Marshal(onboard.Start())
	if err != nil || !strings.Contains(string(b), `"id":"recommended"`) || !strings.Contains(string(b), `"template":"You talk to {voice_model}`) {
		t.Errorf("%v %s", err, b)
	}
}
