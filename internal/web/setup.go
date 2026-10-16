package web

// The setup page (/setup) and its API (/api/setup/*): the first-run
// wizard `sheepdog setup` opens, and the page used later to change keys
// or re-pair the phone. Both are served to loopback clients only, even
// when web.bind is wider, and need no web token: whoever can open a page
// on this computer can read the env file anyway, and on a first run
// there is no token to type yet. Keys go into the env file (0600) and
// are never sent back to the page or logged.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

// Links the setup page shows. TestFlightURL is empty until the public
// TestFlight link exists; the page then says the link is coming.
const (
	TestFlightURL  = ""
	IOSRepoURL     = "https://github.com/kccarlos/herdr-sheepdog/tree/main/ios"
	GeminiKeyURL   = "https://aistudio.google.com/apikey"
	ModelsDocURL   = "https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/models.md"
	RemoteDocURL   = "https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/remote-access.md"
	setupNotLocal  = "The setup page only opens on the computer sheepdog runs on. Open http://127.0.0.1:%s/setup there, or run `sheepdog setup` in its terminal."
	phoneSeenFresh = 10 * time.Minute
)

// SetupDeps back the setup page. nil Deps.Setup: no setup page (404).
type SetupDeps struct {
	// ConfigPath and EnvPath are the files the page writes: the config
	// (only to add an OpenAI-compatible provider) and the env file that
	// holds the keys, the bind and the token.
	ConfigPath, EnvPath string
	// Config is the config the running daemon was built with.
	Config config.Config
	// Endpoints are the APIs the keys are validated against.
	Endpoints onboard.Endpoints
	// Service reports the login service; nil: not supported here.
	Service func(ctx context.Context) ServiceState
	// InstallService hands the daemon over to the login service (it
	// installs the service and this process exits once the service's
	// daemon can take the port); nil: not supported.
	InstallService func() error
	// ClearOverrides forgets the voice provider and orchestrator model
	// picked at runtime (the state file's session overrides), so the
	// choice saved here is the one the daemon starts with.
	ClearOverrides func() error
	// Demo runs the scripted demo daemon; nil: no demo offered.
	Demo DemoControl
	// SysAddrs and TailscaleCLI detect the networks (tests replace them).
	SysAddrs     func() ([]pair.IfaceAddr, error)
	TailscaleCLI func() bool
	// Boot identifies this build of the daemon: the page watches it
	// change to know a reload finished.
	Boot time.Time
}

// ServiceState is the login service as the setup page shows it.
type ServiceState struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Detail    string `json:"detail,omitempty"`
}

// DemoControl runs the scripted demo daemon for "Try it with demo
// agents": in this process, on its own port and data dir, stopped by
// Stop, by a missing page heartbeat (Ping) or after a time cap.
type DemoControl interface {
	Start(cfg config.Config) (DemoState, error)
	Stop(reason string)
	Ping()
	State() DemoState
}

// DemoState is the demo as the page shows it.
type DemoState struct {
	Running bool   `json:"running"`
	URL     string `json:"url,omitempty"`
	// Stopped says why the last demo ended ("stopped", "page closed",
	// "time limit", an error).
	Stopped string `json:"stopped,omitempty"`
	// EndsAt is when the time cap stops it.
	EndsAt *time.Time `json:"ends_at,omitempty"`
}

// phoneTracker remembers the last authorized request from another
// machine (the phone app after pairing).
type phoneTracker struct {
	mu     sync.Mutex
	at     time.Time
	remote string
}

func (p *phoneTracker) note(r *http.Request, now time.Time) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsLoopback() {
		return
	}
	p.mu.Lock()
	p.at, p.remote = now, host
	p.mu.Unlock()
}

func (p *phoneTracker) last() (time.Time, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.at, p.remote
}

// localOnly wraps h: only a loopback client reaching the daemon by a
// loopback host name gets through (the Host check stops a DNS-rebinding
// page); a request that changes something must also come from this
// origin (Origin absent or equal) as JSON, so another website open in
// the same browser cannot post to it.
func (s *Server) localOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if msg := localProblem(r); msg != "" {
			s.log.Log(logSource, "web.setup_refused", "", map[string]any{"path": r.URL.Path, "remote": r.RemoteAddr, "why": msg})
			_, port, _ := net.SplitHostPort(s.Addr())
			text := fmt.Sprintf(setupNotLocal, port)
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, http.StatusForbidden, text)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprintln(w, text)
			return
		}
		h(w, r)
	}
}

// localProblem says why r may not use the setup page ("" when it may).
func localProblem(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return "not a loopback client"
	}
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if ip := net.ParseIP(h); h != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "not a loopback host name"
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return ""
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			return "another origin"
		}
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return "not JSON"
	}
	return ""
}

func (s *Server) setupRoutes() error {
	if s.deps.Setup == nil {
		return nil
	}
	if err := s.staticWrapped("setup", s.localOnly); err != nil {
		return err
	}
	routes := map[string]http.HandlerFunc{
		"GET /api/setup/state":     s.handleSetupState,
		"POST /api/setup/validate": s.handleSetupValidate,
		"POST /api/setup/keys":     s.handleSetupKeys,
		"GET /api/setup/checks":    s.handleSetupChecks,
		"POST /api/setup/service":  s.handleSetupService,
		"POST /api/setup/phone":    s.handleSetupPhone,
		"GET /api/setup/qr.svg":    s.handleSetupQR,
		"POST /api/setup/login":    s.handleSetupLogin,
		"POST /api/setup/demo":     s.handleSetupDemo,
	}
	for pattern, h := range routes {
		s.mux.HandleFunc(pattern, s.localOnly(h))
	}
	return nil
}

// SetupState is GET /api/setup/state.
type SetupState struct {
	Boot       string `json:"boot"`
	Version    string `json:"version"`
	URL        string `json:"url"`
	EnvFile    string `json:"env_file"`
	ConfigFile string `json:"config_file"`
	// Keys says which keys are saved (never their values).
	Keys         SetupKeys         `json:"keys"`
	Voice        SetupVoice        `json:"voice"`
	Orchestrator SetupOrchestrator `json:"orchestrator"`
	Herdr        SetupHerdr        `json:"herdr"`
	Phone        SetupPhone        `json:"phone"`
	Service      ServiceState      `json:"service"`
	Demo         *DemoState        `json:"demo,omitempty"`
	Links        map[string]string `json:"links"`
}

// SetupKeys: which keys the running daemon has.
type SetupKeys struct {
	Gemini   bool `json:"gemini"`
	OpenAI   bool `json:"openai"`
	DeepSeek bool `json:"deepseek"`
}

// SetupVoice is the voice side of SetupState.
type SetupVoice struct {
	Provider    string `json:"provider"`
	Status      string `json:"status"`
	Problem     string `json:"problem,omitempty"`
	SetupNeeded bool   `json:"setup_needed"`
}

// SetupOrchestrator is the orchestrator side of SetupState.
type SetupOrchestrator struct {
	On        bool   `json:"on"`
	Provider  string `json:"provider"`
	Model     string `json:"model,omitempty"`
	OffReason string `json:"off_reason,omitempty"`
}

// SetupHerdr is herdr's reachability and the agents the daemon knows.
type SetupHerdr struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
	Agents int    `json:"agents"`
}

// SetupPhone is the phone-access step's state.
type SetupPhone struct {
	Enabled     bool           `json:"enabled"`
	Bind        string         `json:"bind"`
	HasToken    bool           `json:"has_token"`
	Addresses   []pair.Address `json:"addresses"`
	Tailscale   bool           `json:"tailscale"`
	Connected   bool           `json:"connected"`
	ConnectedAt *time.Time     `json:"connected_at,omitempty"`
	Remote      string         `json:"remote,omitempty"`
}

func (s *Server) handleSetupState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.setupState(r.Context()))
}

func (s *Server) setupState(ctx context.Context) SetupState {
	sd := s.deps.Setup
	cfg := sd.Config
	hs := s.hub.state()
	st := SetupState{
		Boot: sd.Boot.UTC().Format(time.RFC3339Nano), Version: s.deps.Version, URL: "http://" + s.Addr(),
		EnvFile: sd.EnvPath, ConfigFile: sd.ConfigPath,
		Keys:  SetupKeys{Gemini: len(cfg.Gemini.APIKeys) > 0, OpenAI: cfg.Voice.OpenAI.APIKey != "", DeepSeek: cfg.DeepSeek.APIKey != ""},
		Voice: SetupVoice{Provider: s.voiceProvider(), Status: hs.status.String()},
		Links: map[string]string{"get_key": GeminiKeyURL, "testflight": TestFlightURL, "ios_repo": IOSRepoURL, "models_doc": ModelsDocURL, "remote_doc": RemoteDocURL},
	}
	if s.deps.VoiceIssue != nil {
		st.Voice.Problem = s.deps.VoiceIssue()
	}
	if s.deps.SetupNeeded != nil {
		st.Voice.SetupNeeded = s.deps.SetupNeeded()
	}
	if b := s.brainInfo(); b != nil {
		st.Orchestrator = SetupOrchestrator{On: true, Provider: b.Provider, Model: b.Model}
	} else {
		st.Orchestrator = SetupOrchestrator{Provider: cfg.Brain.Provider, OffReason: s.deps.BrainOffReason}
	}
	h := onboard.CheckHerdr(cfg.Herdr.Socket)
	st.Herdr = SetupHerdr{OK: h.Status == onboard.OK, Detail: h.Detail, Fix: h.Fix, Agents: len(s.deps.Store.List())}
	if s.deps.HerdrMode == "mock" {
		st.Herdr = SetupHerdr{OK: true, Detail: "mock herdr (developer mode)", Agents: len(s.deps.Store.List())}
	}
	st.Phone = s.setupPhone()
	if sd.Service != nil {
		st.Service = sd.Service(ctx)
	}
	if sd.Demo != nil {
		d := sd.Demo.State()
		st.Demo = &d
	}
	return st
}

func (s *Server) setupPhone() SetupPhone {
	sd := s.deps.Setup
	cfg := sd.Config
	p := SetupPhone{Enabled: !cfg.Web.IsLoopback(), Bind: cfg.Web.Bind, HasToken: cfg.Web.Token != "", Addresses: []pair.Address{}}
	sys, _ := sd.SysAddrs()
	for _, a := range sys {
		if ip4 := a.IP.To4(); ip4 != nil && pair.Kind(ip4, a.Iface) == pair.KindTailscale {
			p.Tailscale = true
		}
	}
	if !p.Tailscale && sd.TailscaleCLI != nil {
		p.Tailscale = sd.TailscaleCLI()
	}
	if p.Enabled {
		if addrs, _, err := s.pairing(); err == nil {
			p.Addresses = addrs
		}
	}
	if at, remote := s.phones.last(); !at.IsZero() && s.deps.Now().Sub(at) < phoneSeenFresh {
		t := at.UTC()
		p.Connected, p.ConnectedAt, p.Remote = true, &t, remote
	}
	return p
}

// SetupValidateRequest is POST /api/setup/validate: one key to check
// live, before anything is saved.
type SetupValidateRequest struct {
	// Kind is gemini, openai, deepseek or compat.
	Kind string `json:"kind"`
	Key  string `json:"key"`
	// BaseURL and Name are the compat provider's.
	BaseURL string `json:"base_url,omitempty"`
	Name    string `json:"name,omitempty"`
}

// SetupCheck is one check's outcome for the page.
type SetupCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail | skip
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func checkOf(r onboard.Result) SetupCheck {
	return SetupCheck{Name: r.Name, Status: strings.ToLower(r.Status.String()), Detail: r.Detail, Fix: r.Fix}
}

// validateKey checks one key live (no key in any result).
func (s *Server) validateKey(ctx context.Context, req SetupValidateRequest) SetupCheck {
	ep := s.deps.Setup.Endpoints
	key := strings.TrimSpace(req.Key)
	switch req.Kind {
	case "gemini":
		c := checkOf(onboard.CheckGeminiKey(ctx, ep, key))
		if c.Status == "fail" && strings.Contains(c.Detail, "rejected the key") {
			c.Fix = "check that you copied the whole key, or create a new one at " + GeminiKeyURL
		}
		return c
	case "openai":
		if key == "" {
			return SetupCheck{Name: "openai voice key", Status: "fail", Detail: "no key given", Fix: "paste an OpenAI API key (https://platform.openai.com/api-keys)"}
		}
		return checkOf(onboard.CheckOpenAIVoiceKey(ctx, ep, key))
	case "deepseek":
		off := false
		return checkOf(onboard.CheckCompat(ctx, ep, onboard.Compat{Name: brain.ProviderDeepSeek, BaseURL: config.DefaultDeepSeekBaseURL, APIKey: key, KeyEnv: "DEEPSEEK_API_KEY", Thinking: &off}, false)[0])
	case "compat":
		name := compatName(req.Name)
		c := config.OpenAICompat{Name: name, BaseURL: strings.TrimSpace(req.BaseURL)}
		if c.BaseURL == "" {
			return SetupCheck{Name: name + " key", Status: "fail", Detail: "no base URL", Fix: "give the API root, e.g. https://openrouter.ai/api/v1"}
		}
		return checkOf(onboard.CheckCompat(ctx, ep, onboard.Compat{Name: name, BaseURL: c.BaseURL, APIKey: key, KeyEnv: config.DefaultCompatKeyEnv(name), NoKey: c.KeyOptional()}, false)[0])
	}
	return SetupCheck{Name: req.Kind, Status: "fail", Detail: "unknown key kind " + fmt.Sprintf("%q", req.Kind)}
}

func compatName(n string) string {
	n = strings.ToLower(strings.TrimSpace(n))
	if n == "" {
		return "openrouter"
	}
	return n
}

func (s *Server) handleSetupValidate(w http.ResponseWriter, r *http.Request) {
	var req SetupValidateRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.validateKey(r.Context(), req))
}

// SetupKeysRequest is POST /api/setup/keys: the models step. An empty
// key keeps the saved one; a key that is needed and missing, or that
// fails its live check, saves nothing.
type SetupKeysRequest struct {
	GeminiKey string `json:"gemini_key"`
	// Voice is gemini (default) or openai (GPT-Live).
	Voice     string `json:"voice"`
	OpenAIKey string `json:"openai_key"`
	// Orchestrator is gemini (default), deepseek or compat.
	Orchestrator string `json:"orchestrator"`
	DeepSeekKey  string `json:"deepseek_key"`
	Compat       struct {
		Name    string `json:"name"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		Key     string `json:"key"`
	} `json:"compat"`
}

// SetupKeysResponse answers the models step: each key's check, and
// whether the daemon restarts with the saved settings.
type SetupKeysResponse struct {
	OK         bool         `json:"ok"`
	Checks     []SetupCheck `json:"checks"`
	Error      string       `json:"error,omitempty"`
	Restarting bool         `json:"restarting"`
}

func (s *Server) handleSetupKeys(w http.ResponseWriter, r *http.Request) {
	var req SetupKeysRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	resp, status := s.saveKeys(r.Context(), req)
	writeJSON(w, status, resp)
	if resp.Restarting {
		s.restartSoon("setup: models saved")
	}
}

// saveKeys validates and writes the models step.
func (s *Server) saveKeys(ctx context.Context, req SetupKeysRequest) (SetupKeysResponse, int) {
	sd := s.deps.Setup
	cfg := sd.Config
	saved, err := onboard.ReadEnvFile(sd.EnvPath)
	if err != nil {
		return SetupKeysResponse{Error: "cannot read " + sd.EnvPath + ": " + err.Error()}, http.StatusInternalServerError
	}
	voiceP := orDefault(req.Voice, "gemini")
	orch := orDefault(req.Orchestrator, brain.ProviderGemini)
	if voiceP != "gemini" && voiceP != "openai" {
		return SetupKeysResponse{Error: "voice must be gemini or openai"}, http.StatusBadRequest
	}
	type need struct {
		kind, env string
		given     string
		have      bool
		req       SetupValidateRequest
	}
	var needs []need
	if voiceP == "gemini" || orch == brain.ProviderGemini {
		needs = append(needs, need{kind: "gemini", env: "GEMINI_API_KEY", given: req.GeminiKey, have: len(cfg.Gemini.APIKeys) > 0})
	}
	if voiceP == "openai" {
		needs = append(needs, need{kind: "openai", env: "OPENAI_API_KEY", given: req.OpenAIKey, have: cfg.Voice.OpenAI.APIKey != ""})
	}
	var compat *config.OpenAICompat
	brainProvider, brainModel := orch, ""
	switch orch {
	case brain.ProviderGemini:
	case brain.ProviderDeepSeek:
		needs = append(needs, need{kind: "deepseek", env: "DEEPSEEK_API_KEY", given: req.DeepSeekKey, have: cfg.DeepSeek.APIKey != ""})
		brainModel = brain.DefaultDeepSeekModel
	case "compat":
		c := config.OpenAICompat{Name: compatName(req.Compat.Name), BaseURL: strings.TrimSpace(req.Compat.BaseURL), Model: strings.TrimSpace(req.Compat.Model)}
		if c.BaseURL == "" || c.Model == "" {
			return SetupKeysResponse{Error: "an OpenAI-compatible orchestrator needs its base URL and a model that supports tool calling"}, http.StatusBadRequest
		}
		if c.Name == brain.ProviderGemini || c.Name == brain.ProviderDeepSeek || c.Name == brain.ProviderVertex {
			return SetupKeysResponse{Error: fmt.Sprintf("%q is a built-in provider name; pick another name", c.Name)}, http.StatusBadRequest
		}
		c.APIKeyEnv = config.DefaultCompatKeyEnv(c.Name)
		if cur, ok := cfg.Brain.CompatProvider(c.Name); ok && cur.APIKeyEnv != "" {
			c.APIKeyEnv = cur.APIKeyEnv
		}
		have := false
		if cur, ok := cfg.Brain.CompatProvider(c.Name); ok {
			have = cur.APIKey != ""
		}
		n := need{kind: "compat", env: c.APIKeyEnv, given: req.Compat.Key, have: have || c.KeyOptional(), req: SetupValidateRequest{Kind: "compat", Name: c.Name, BaseURL: c.BaseURL}}
		needs = append(needs, n)
		compat = &c
		brainProvider, brainModel = c.Name, c.Model
	default:
		return SetupKeysResponse{Error: "orchestrator must be gemini, deepseek or compat"}, http.StatusBadRequest
	}

	resp := SetupKeysResponse{Checks: []SetupCheck{}}
	set := map[string]string{}
	failed := false
	for _, n := range needs {
		v := strings.TrimSpace(n.given)
		if v == "" {
			if n.have {
				resp.Checks = append(resp.Checks, SetupCheck{Name: n.env, Status: "ok", Detail: "keeping the saved key"})
				continue
			}
			failed = true
			fix := "paste the key"
			if n.kind == "gemini" {
				fix = "get a free key at " + GeminiKeyURL + " and paste it here"
			}
			resp.Checks = append(resp.Checks, SetupCheck{Name: n.env, Status: "fail", Detail: n.env + " is needed and not saved yet", Fix: fix})
			continue
		}
		vr := n.req
		if vr.Kind == "" {
			vr.Kind = n.kind
		}
		vr.Key = v
		c := s.validateKey(ctx, vr)
		resp.Checks = append(resp.Checks, c)
		if c.Status == "fail" {
			failed = true
			continue
		}
		set[n.env] = v
	}
	if failed {
		resp.Error = "nothing was saved: fix the key marked above"
		return resp, http.StatusUnprocessableEntity
	}
	if compat != nil {
		if err := onboard.EnsureCompat(sd.ConfigPath, *compat); err != nil {
			resp.Error = "cannot add the provider to " + sd.ConfigPath + ": " + err.Error()
			return resp, http.StatusInternalServerError
		}
	}
	set["SHEEPDOG_VOICE_PROVIDER"] = voiceP
	set["SHEEPDOG_BRAIN_PROVIDER"] = brainProvider
	var del []string
	if brainModel != "" {
		set["SHEEPDOG_BRAIN_MODEL"] = brainModel
	} else if p := saved["SHEEPDOG_BRAIN_PROVIDER"]; p != "" && p != brainProvider {
		// Back to Gemini from another provider: that provider's model
		// must not stay behind.
		del = append(del, "SHEEPDOG_BRAIN_MODEL")
	}
	if err := onboard.EditEnvFile(sd.EnvPath, set, del); err != nil {
		resp.Error = "cannot write " + sd.EnvPath + ": " + err.Error()
		return resp, http.StatusInternalServerError
	}
	if sd.ClearOverrides != nil {
		if err := sd.ClearOverrides(); err != nil {
			s.log.Log(logSource, "web.setup_error", "", map[string]any{"step": "clear_overrides", "error": err.Error()})
		}
	}
	names := make([]string, 0, len(set))
	for k := range set {
		names = append(names, k)
	}
	s.log.Log(logSource, "web.setup_saved", "", map[string]any{"step": "models", "vars": names, "voice": voiceP, "orchestrator": brainProvider})
	resp.OK = true
	resp.Restarting = s.deps.Restart != nil
	return resp, http.StatusOK
}

func orDefault(v, def string) string {
	if v = strings.TrimSpace(v); v == "" {
		return def
	}
	return v
}

// restartSoon asks for the in-place restart once the response is out.
func (s *Server) restartSoon(reason string) {
	if s.deps.Restart == nil {
		return
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		s.deps.Restart(reason)
	}()
}

func (s *Server) handleSetupChecks(w http.ResponseWriter, r *http.Request) {
	sd := s.deps.Setup
	results := onboard.Doctor(r.Context(), sd.Config, onboard.DoctorOptions{Endpoints: sd.Endpoints, ConfigPath: sd.ConfigPath, EnvPath: sd.EnvPath})
	out := make([]SetupCheck, 0, len(results))
	for _, res := range results {
		out = append(out, checkOf(res))
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": out})
}

// SetupServiceRequest is POST /api/setup/service.
type SetupServiceRequest struct {
	Install bool `json:"install"`
}

func (s *Server) handleSetupService(w http.ResponseWriter, r *http.Request) {
	sd := s.deps.Setup
	var req SetupServiceRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if !req.Install {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "handoff": false})
		return
	}
	if sd.Service == nil || sd.InstallService == nil {
		writeError(w, http.StatusNotImplemented, "the login service is not supported here; run `sheepdog service install` in a terminal")
		return
	}
	if st := sd.Service(r.Context()); st.Installed {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "handoff": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "handoff": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		if err := sd.InstallService(); err != nil {
			s.log.Log(logSource, "web.setup_error", "", map[string]any{"step": "service", "error": err.Error()})
		}
	}()
}

// SetupPhoneRequest is POST /api/setup/phone. Enable sets the bind to
// every address of this computer (0.0.0.0: the LAN and Tailscale ones,
// behind the token) and creates a token if none exists; NewToken
// replaces the token (the paired phone must scan again).
type SetupPhoneRequest struct {
	Enable   bool `json:"enable"`
	NewToken bool `json:"new_token"`
}

func (s *Server) handleSetupPhone(w http.ResponseWriter, r *http.Request) {
	var req SetupPhoneRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	created, err := s.savePhone(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token_created": created, "restarting": s.deps.Restart != nil})
	s.restartSoon("setup: phone access")
}

// savePhone writes the bind and, when needed, a token; it reports
// whether a token was created.
func (s *Server) savePhone(req SetupPhoneRequest) (bool, error) {
	sd := s.deps.Setup
	created, err := onboard.SetPhoneAccess(sd.EnvPath, sd.Config, req.Enable, req.NewToken)
	if err != nil {
		return false, err
	}
	s.log.Log(logSource, "web.setup_saved", "", map[string]any{"step": "phone", "enable": req.Enable, "token_created": created})
	return created, nil
}

// handleSetupQR is the pairing code (it holds the token: never logged,
// never cached).
func (s *Server) handleSetupQR(w http.ResponseWriter, r *http.Request) {
	s.handlePairQR(w, r)
}

// handleSetupLogin sets the token cookie for this origin, so /voice and
// /debug opened from the setup page need no token typed.
func (s *Server) handleSetupLogin(w http.ResponseWriter, r *http.Request) {
	if s.deps.Token != "" {
		http.SetCookie(w, &http.Cookie{
			Name: tokenCookie, Value: s.deps.Token, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// SetupDemoRequest is POST /api/setup/demo: action start, stop or ping
// (the page's heartbeat).
type SetupDemoRequest struct {
	Action string `json:"action"`
}

func (s *Server) handleSetupDemo(w http.ResponseWriter, r *http.Request) {
	d := s.deps.Setup.Demo
	if d == nil {
		writeError(w, http.StatusNotImplemented, "the demo is not available in this build")
		return
	}
	var req SetupDemoRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	switch req.Action {
	case "start":
		st, err := d.Start(s.deps.Setup.Config)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "demo": d.State()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"demo": st})
	case "stop":
		d.Stop("stopped")
		writeJSON(w, http.StatusOK, map[string]any{"demo": d.State()})
	case "ping":
		d.Ping()
		writeJSON(w, http.StatusOK, map[string]any{"demo": d.State()})
	default:
		writeError(w, http.StatusBadRequest, "action must be start, stop or ping")
	}
}

// TailscaleInstalled reports whether the tailscale CLI or the macOS app
// is on this computer.
func TailscaleInstalled() bool {
	if _, err := exec.LookPath("tailscale"); err == nil {
		return true
	}
	_, err := os.Stat("/Applications/Tailscale.app")
	return err == nil
}
