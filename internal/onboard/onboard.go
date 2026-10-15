// Package onboard holds the checks behind `sheepdog init` and `sheepdog
// doctor`: live, cheap validation of each API key (with the balance or
// quota where the API exposes it), herdr's reachability, the web port,
// the bind/token pairing, the push config and the voice backends. Each
// check returns a Result with a one-line fix. No check ever prints or
// returns a key.
package onboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// Status is a check's outcome.
type Status int

const (
	OK Status = iota
	Warn
	Fail
	// Skip is a check that does not apply (no key configured for an
	// optional service).
	Skip
)

func (s Status) String() string {
	switch s {
	case OK:
		return "ok"
	case Warn:
		return "WARN"
	case Fail:
		return "FAIL"
	}
	return "skip"
}

// Result is one check: what was checked, what was found and, when it is
// not OK, the fix in one line.
type Result struct {
	Name   string
	Status Status
	Detail string
	Fix    string
}

// Line renders the result as one line: status, name, detail, fix.
func (r Result) Line() string {
	s := fmt.Sprintf("%-4s  %-18s %s", r.Status, r.Name, r.Detail)
	if r.Fix != "" && r.Status != OK {
		s += " — fix: " + r.Fix
	}
	return s
}

// Endpoints are the API roots the key checks call; tests point them at
// an httptest server.
type Endpoints struct {
	// GeminiBase is the AI Studio API root
	// (https://generativelanguage.googleapis.com).
	GeminiBase string
	// OpenAIBase is the OpenAI API root (https://api.openai.com/v1).
	OpenAIBase string
	// HTTP is the client (default: a 10 s timeout).
	HTTP *http.Client
}

// DefaultEndpoints are the real API roots.
func DefaultEndpoints() Endpoints {
	return Endpoints{GeminiBase: "https://generativelanguage.googleapis.com", OpenAIBase: "https://api.openai.com/v1"}
}

func (e Endpoints) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// httpStatusError is a non-2xx answer: the status and the API's message.
type httpStatusError struct {
	Status  int
	Message string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// getJSON GETs url with the given headers and decodes a 2xx JSON body
// into out (nil: body discarded). The URL never carries a key.
func getJSON(ctx context.Context, c *http.Client, u string, header map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{Status: resp.StatusCode, Message: apiMessage(data, resp.Status)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// apiMessage extracts error.message from an API error body (Google and
// OpenAI shapes alike), else its first line.
func apiMessage(data []byte, fallback string) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil && body.Error.Message != "" {
		return body.Error.Message
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	if line == "" {
		return fallback
	}
	if len(line) > 200 {
		line = line[:200]
	}
	return line
}

// statusOf returns the HTTP status of err (an httpStatusError or a
// brain.APIError), 0 when there is none.
func statusOf(err error) (int, string) {
	var he *httpStatusError
	if errors.As(err, &he) {
		return he.Status, he.Message
	}
	var ae *brain.APIError
	if errors.As(err, &ae) {
		return ae.Status, ae.Message
	}
	return 0, ""
}

// keyFailure turns a failed key call into a Result naming the service
// and the fix: an invalid key, an exhausted quota or balance, or an
// unreachable API.
func keyFailure(name, service, keyVar, consoleURL string, err error) Result {
	status, msg := statusOf(err)
	switch brain.AccountProblemOf(status, msg) {
	case brain.ProblemInvalidKey:
		return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("%s rejected the key (HTTP %d: %s)", service, status, msg), Fix: fmt.Sprintf("put a valid key in %s (%s)", keyVar, consoleURL)}
	case brain.ProblemQuota:
		return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("%s says the quota is used up (HTTP %d: %s)", service, status, msg), Fix: fmt.Sprintf("wait for the quota to reset or raise it (%s)", consoleURL)}
	case brain.ProblemBalance:
		return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("%s says the balance is used up (HTTP %d: %s)", service, status, msg), Fix: fmt.Sprintf("top up the account (%s)", consoleURL)}
	}
	if status != 0 {
		return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("%s answered HTTP %d: %s", service, status, msg), Fix: "check the key and the service status, then run sheepdog doctor again"}
	}
	return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("%s unreachable: %v", service, err), Fix: "check the network (and base_url), then run sheepdog doctor again"}
}

// CheckGeminiKey validates an AI Studio key with a model-list call (free).
// The key goes in the x-goog-api-key header, never the URL.
func CheckGeminiKey(ctx context.Context, ep Endpoints, key string) Result {
	const name, console = "gemini key", "https://aistudio.google.com/apikey"
	if key == "" {
		return Result{Name: name, Status: Fail, Detail: "GEMINI_API_KEY is not set", Fix: "create a free key at " + console + " and run sheepdog init"}
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	u := strings.TrimSuffix(ep.GeminiBase, "/") + "/v1beta/models?pageSize=1000"
	if err := getJSON(ctx, ep.client(), u, map[string]string{"x-goog-api-key": key}, &out); err != nil {
		return keyFailure(name, "Gemini (AI Studio)", "GEMINI_API_KEY", console, err)
	}
	return Result{Name: name, Status: OK, Detail: fmt.Sprintf("valid (%d models); AI Studio does not expose the quota left — a 429 means today's free quota is used up", len(out.Models))}
}

// CheckOpenAIVoiceKey validates the GPT-Live key with a model-list call.
func CheckOpenAIVoiceKey(ctx context.Context, ep Endpoints, key string) Result {
	const name, console = "openai voice key", "https://platform.openai.com/api-keys"
	if key == "" {
		return Result{Name: name, Status: Skip, Detail: "OPENAI_API_KEY not set (GPT-Live voice unavailable; optional)"}
	}
	u := strings.TrimSuffix(ep.OpenAIBase, "/") + "/models"
	if err := getJSON(ctx, ep.client(), u, map[string]string{"Authorization": "Bearer " + key}, nil); err != nil {
		return keyFailure(name, "OpenAI", "OPENAI_API_KEY", console, err)
	}
	return Result{Name: name, Status: OK, Detail: "valid; billed per minute of open session (balance not exposed by the API)"}
}

// Compat describes one OpenAI-compatible orchestrator provider to check.
type Compat struct {
	// Name is the provider id (deepseek, or a [[brain.openai_compat]]
	// name).
	Name    string
	BaseURL string
	APIKey  string
	// KeyEnv names the key's variable, for the fix line.
	KeyEnv string
	// NoKey: a loopback server that needs no key.
	NoKey bool
	// Model is the model the probe runs ("" skips the probe).
	Model string
	// Thinking is DeepSeek's thinking field (nil for a generic provider).
	Thinking *bool
}

// probeTool is the one tool the tool-calling probe declares.
const probeTool = "report_ready"

// CheckCompat validates an OpenAI-compatible provider: the key with
// GET /models (free), the balance where the API exposes it (DeepSeek
// /user/balance, OpenRouter /key), and — when probe is set and Model is
// not empty — one tiny tool-calling turn (a few tokens), since the
// orchestrator cannot work on a model without tool calling.
func CheckCompat(ctx context.Context, ep Endpoints, c Compat, probe bool) []Result {
	name := c.Name + " key"
	if c.APIKey == "" && !c.NoKey {
		return []Result{{Name: name, Status: Fail, Detail: c.KeyEnv + " is not set", Fix: "put the key in " + c.KeyEnv + " (sheepdog init writes it)"}}
	}
	m, err := brain.NewOpenAICompatModel(brain.OpenAICompatConfig{Provider: c.Name, BaseURL: c.BaseURL, APIKey: c.APIKey, NoKey: c.NoKey, Thinking: c.Thinking, HTTPClient: ep.client()})
	if err != nil {
		return []Result{{Name: name, Status: Fail, Detail: err.Error(), Fix: "fix the provider's config (CONFIG.md [[brain.openai_compat]])"}}
	}
	console := consoleOf(c.Name, c.BaseURL)
	models, err := m.ListModels(ctx)
	if err != nil {
		return []Result{keyFailure(name, c.Name, c.KeyEnv, console, err)}
	}
	detail := fmt.Sprintf("valid (%d models)", len(models))
	if c.NoKey && c.APIKey == "" {
		detail = fmt.Sprintf("local server reachable (%d models)", len(models))
	}
	out := []Result{{Name: name, Status: OK, Detail: detail}}
	if b, ok := balanceOf(ctx, ep, c); ok {
		out = append(out, b)
	}
	if probe && c.Model != "" {
		out = append(out, probeToolCall(ctx, m, c, console))
	}
	return out
}

// probeToolCall runs one tiny turn that must call probeTool.
func probeToolCall(ctx context.Context, m *brain.OpenAICompatModel, c Compat, console string) Result {
	name := c.Name + " tools"
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText("You are a connectivity probe. Call the "+probeTool+" tool and nothing else.", genai.RoleUser),
		Tools:             []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: probeTool, Description: "Report that you are ready.", Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{}}}}}},
		ThinkingConfig:    &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelMinimal},
	}
	resp, err := m.Generate(ctx, c.Model, []*genai.Content{genai.NewContentFromText("Call the tool now.", genai.RoleUser)}, cfg)
	if err != nil {
		r := keyFailure(name, c.Name, c.KeyEnv, console, err)
		if status, _ := statusOf(err); status == http.StatusNotFound || status == http.StatusBadRequest {
			r.Fix = fmt.Sprintf("check that model %q exists on %s and supports tool calling", c.Model, c.Name)
		}
		return r
	}
	if len(resp.FunctionCalls()) == 0 {
		return Result{Name: name, Status: Warn, Detail: fmt.Sprintf("model %s answered without calling the probe tool", c.Model), Fix: "pick a model with reliable tool calling; the orchestrator acts only through tools"}
	}
	return Result{Name: name, Status: OK, Detail: fmt.Sprintf("model %s calls tools", c.Model)}
}

// consoleOf is where the user manages the provider's key.
func consoleOf(name, base string) string {
	switch {
	case name == brain.ProviderDeepSeek:
		return "https://platform.deepseek.com"
	case strings.Contains(base, "openrouter.ai"):
		return "https://openrouter.ai/settings/keys"
	case strings.Contains(base, "groq.com"):
		return "https://console.groq.com/keys"
	}
	return "the provider's console"
}

// balanceOf reads the balance or credit left where the API exposes it.
func balanceOf(ctx context.Context, ep Endpoints, c Compat) (Result, bool) {
	name := c.Name + " balance"
	auth := map[string]string{"Authorization": "Bearer " + c.APIKey}
	switch {
	case c.Name == brain.ProviderDeepSeek:
		var out struct {
			IsAvailable  bool `json:"is_available"`
			BalanceInfos []struct {
				Currency     string `json:"currency"`
				TotalBalance string `json:"total_balance"`
			} `json:"balance_infos"`
		}
		if err := getJSON(ctx, ep.client(), strings.TrimSuffix(c.BaseURL, "/")+"/user/balance", auth, &out); err != nil {
			return Result{Name: name, Status: Warn, Detail: "could not read the balance: " + err.Error()}, true
		}
		var parts []string
		for _, b := range out.BalanceInfos {
			parts = append(parts, b.TotalBalance+" "+b.Currency)
		}
		if !out.IsAvailable {
			return Result{Name: name, Status: Fail, Detail: "balance used up (" + strings.Join(parts, ", ") + ")", Fix: "top up at https://platform.deepseek.com"}, true
		}
		return Result{Name: name, Status: OK, Detail: strings.Join(parts, ", ") + " left"}, true
	case strings.Contains(c.BaseURL, "openrouter.ai"):
		var out struct {
			Data struct {
				Usage      float64  `json:"usage"`
				Limit      *float64 `json:"limit"`
				IsFreeTier bool     `json:"is_free_tier"`
			} `json:"data"`
		}
		if err := getJSON(ctx, ep.client(), strings.TrimSuffix(c.BaseURL, "/")+"/key", auth, &out); err != nil {
			return Result{Name: name, Status: Warn, Detail: "could not read the key's credit: " + err.Error()}, true
		}
		d := out.Data
		switch {
		case d.Limit != nil && d.Usage >= *d.Limit:
			return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("credit used up ($%.2f of $%.2f)", d.Usage, *d.Limit), Fix: "add credit at https://openrouter.ai/settings/credits"}, true
		case d.Limit != nil:
			return Result{Name: name, Status: OK, Detail: fmt.Sprintf("$%.2f of $%.2f used", d.Usage, *d.Limit)}, true
		case d.IsFreeTier:
			return Result{Name: name, Status: OK, Detail: "free tier (daily request limit; :free models only)"}, true
		}
		return Result{Name: name, Status: OK, Detail: fmt.Sprintf("$%.2f used, no limit set", d.Usage)}, true
	}
	return Result{}, false
}

// HerdrInstallHint is the one-line fix when herdr is missing.
const HerdrInstallHint = "install herdr from https://github.com/herdrdev/herdr/releases and start it (herdr), then run sheepdog doctor again"

// CheckHerdr reports whether herdr's socket answers and its binary is
// on PATH.
func CheckHerdr(socket string) Result {
	const name = "herdr"
	_, binErr := exec.LookPath("herdr")
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err == nil {
		_ = conn.Close()
		return Result{Name: name, Status: OK, Detail: "socket " + socket + " answers"}
	}
	if binErr != nil {
		return Result{Name: name, Status: Fail, Detail: "no herdr on PATH and no socket at " + socket, Fix: HerdrInstallHint}
	}
	return Result{Name: name, Status: Fail, Detail: "herdr is installed but its socket " + socket + " does not answer", Fix: "start herdr (or set herdr.socket / SHEEPDOG_HERDR_SOCKET to its socket)"}
}

// CheckPort reports whether the web port is free, serves this daemon, or
// is taken by another program.
func CheckPort(ctx context.Context, c *http.Client, bind string, port int) Result {
	const name = "web port"
	host := bind
	if ip := net.ParseIP(bind); bind == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return Result{Name: name, Status: OK, Detail: addr + " is free (the daemon is not running)"}
	}
	_ = conn.Close()
	if c == nil {
		c = &http.Client{Timeout: 3 * time.Second}
	}
	var hello struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := getJSON(ctx, c, "http://"+addr+"/api/hello", nil, &hello); err == nil && hello.Name == "sheepdog" {
		return Result{Name: name, Status: OK, Detail: fmt.Sprintf("the daemon is up on %s (version %s)", addr, hello.Version)}
	}
	return Result{Name: name, Status: Fail, Detail: addr + " is taken by another program", Fix: "stop that program or set web.port / SHEEPDOG_WEB_PORT to a free port"}
}

// MinTokenLen is the token length below which doctor warns.
const MinTokenLen = 24

// CheckBindToken checks the bind/token pairing.
func CheckBindToken(w config.Web) Result {
	const name = "bind/token"
	switch {
	case w.IsLoopback():
		return Result{Name: name, Status: OK, Detail: "bind " + w.Bind + ": this machine only (the phone cannot connect; set web.bind to a LAN or Tailscale address for it)"}
	case w.Token == "":
		return Result{Name: name, Status: Fail, Detail: "bind " + w.Bind + " is reachable from the network but web.token is empty (serve refuses to start)", Fix: "run sheepdog init to generate a token, or set SHEEPDOG_WEB_TOKEN"}
	case len(w.Token) < MinTokenLen:
		return Result{Name: name, Status: Warn, Detail: fmt.Sprintf("bind %s with a short token (%d characters)", w.Bind, len(w.Token)), Fix: fmt.Sprintf("use a random token of at least %d characters (re-pair the phone after changing it)", MinTokenLen)}
	}
	return Result{Name: name, Status: OK, Detail: fmt.Sprintf("bind %s with a token of %d characters", w.Bind, len(w.Token))}
}

// CheckPush checks [push.apns]: all four values or none, and the key file.
func CheckPush(a config.APNs) Result {
	const name = "push"
	set := 0
	for _, v := range []string{a.KeyPath, a.KeyID, a.TeamID, a.Topic} {
		if v != "" {
			set++
		}
	}
	switch {
	case set == 0:
		return Result{Name: name, Status: Skip, Detail: "push not configured (optional; docs/dev/push.md)"}
	case set < 4:
		return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("[push.apns] has %d of its 4 values", set), Fix: "set key_path, key_id, team_id and topic, or none (docs/dev/push.md)"}
	}
	st, err := os.Stat(a.KeyPath)
	if err != nil {
		return Result{Name: name, Status: Fail, Detail: "APNs key file: " + err.Error(), Fix: "point push.apns.key_path at the .p8 file (docs/dev/push.md)"}
	}
	if st.Mode().Perm()&0o077 != 0 {
		return Result{Name: name, Status: Warn, Detail: "APNs key file is readable by other users", Fix: "chmod 600 " + a.KeyPath}
	}
	return Result{Name: name, Status: OK, Detail: "APNs configured for " + a.Topic}
}

// CheckVoice reports which voice backends have what they need and
// whether voice.provider is one of them.
func CheckVoice(cfg config.Config) Result {
	const name = "voice"
	var have []string
	geminiOK := false
	switch cfg.Gemini.Backend {
	case "vertex":
		geminiOK = cfg.Gemini.Vertex.APIKey != "" && cfg.Gemini.Vertex.Project != ""
	default:
		geminiOK = len(cfg.Gemini.APIKeys) > 0
	}
	if geminiOK {
		have = append(have, "gemini ("+cfg.Gemini.Backend+")")
	}
	openaiOK := cfg.Voice.OpenAI.APIKey != ""
	if openaiOK {
		have = append(have, "openai")
	}
	if (cfg.Voice.Provider == "gemini" && !geminiOK) || (cfg.Voice.Provider == "openai" && !openaiOK) {
		fix := "set GEMINI_API_KEY (sheepdog init)"
		switch {
		case cfg.Voice.Provider == "openai":
			fix = "set OPENAI_API_KEY, or voice.provider = \"gemini\""
		case cfg.Gemini.Backend == "vertex":
			fix = "set VERTEX_API_KEY and VERTEX_PROJECT, or gemini.backend = \"aistudio\""
		}
		return Result{Name: name, Status: Fail, Detail: "voice.provider " + cfg.Voice.Provider + " has no credentials", Fix: fix}
	}
	return Result{Name: name, Status: OK, Detail: "starts on " + cfg.Voice.Provider + "; available: " + strings.Join(have, ", ")}
}

// CheckSecretFile warns when a file holding secrets is readable by other
// users (a missing file is fine).
func CheckSecretFile(name, path string) Result {
	st, err := os.Stat(path)
	if err != nil {
		return Result{Name: name, Status: Skip, Detail: path + " not present"}
	}
	if st.Mode().Perm()&0o077 != 0 {
		return Result{Name: name, Status: Warn, Detail: fmt.Sprintf("%s is readable by other users (%04o)", path, st.Mode().Perm()), Fix: "chmod 600 " + path}
	}
	return Result{Name: name, Status: OK, Detail: path + " is private (0600)"}
}

// IsLoopbackURL reports whether a base URL points at this machine.
func IsLoopbackURL(base string) bool {
	return config.OpenAICompat{BaseURL: base}.KeyOptional()
}
