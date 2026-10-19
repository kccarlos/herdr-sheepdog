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
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
)

// Links the setup page shows. TestFlightURL is the public TestFlight
// link (empty would make the page say the link is coming).
const (
	TestFlightURL  = "https://testflight.apple.com/join/bjCaFYgK"
	IOSRepoURL     = "https://github.com/kccarlos/herdr-sheepdog/tree/main/ios"
	GeminiKeyURL   = onboard.GeminiKeyURL
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
	// Presets are the orchestrator presets (nil: onboard.Presets(); tests
	// point the local ones at fake servers).
	Presets []onboard.Preset
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
		"GET /api/setup/state":             s.handleSetupState,
		"POST /api/setup/validate":         s.handleSetupValidate,
		"POST /api/setup/keys":             s.handleSetupKeys,
		"POST /api/setup/models":           s.handleSetupModels,
		"POST /api/setup/probe":            s.handleSetupProbe,
		"POST /api/setup/gemini-models":    s.handleSetupGeminiModels,
		"POST /api/setup/anthropic-models": s.handleSetupAnthropicModels,
		"POST /api/setup/anthropic-probe":  s.handleSetupAnthropicProbe,
		"POST /api/setup/voice-models":     s.handleSetupVoiceModels,
		"GET /api/setup/checks":            s.handleSetupChecks,
		"POST /api/setup/service":          s.handleSetupService,
		"POST /api/setup/phone":            s.handleSetupPhone,
		"GET /api/setup/qr.svg":            s.handleSetupQR,
		"POST /api/setup/login":            s.handleSetupLogin,
		"POST /api/setup/demo":             s.handleSetupDemo,
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
	Vertex       SetupVertex       `json:"vertex"`
	Orchestrator SetupOrchestrator `json:"orchestrator"`
	// Presets are the orchestrator choices, in the order the page shows
	// them, with whether each one's key is saved.
	Presets []SetupPreset `json:"presets"`
	// Start is the starting-point cards, the summary template and the
	// cost lines (onboard.Start): the terminal setup prints the same.
	Start   onboard.StartInfo `json:"start"`
	Herdr   SetupHerdr        `json:"herdr"`
	Phone   SetupPhone        `json:"phone"`
	Service ServiceState      `json:"service"`
	Demo    *DemoState        `json:"demo,omitempty"`
	Links   map[string]string `json:"links"`
}

// SetupKeys: which keys the running daemon has.
type SetupKeys struct {
	Gemini   bool `json:"gemini"`
	OpenAI   bool `json:"openai"`
	DeepSeek bool `json:"deepseek"`
	// Vertex: a Vertex AI key is saved (for Vertex.Project only).
	Vertex    bool `json:"vertex"`
	Anthropic bool `json:"anthropic"`
}

// SetupVertex is the Vertex AI block's state: the saved project and
// location (not secrets), the Live model and voice the Vertex voice uses,
// and the orchestrator models known to answer on Vertex (its model
// listing refuses an API key, so the list is brain.VertexModels).
type SetupVertex struct {
	Project         string `json:"project"`
	Location        string `json:"location"`
	DefaultLocation string `json:"default_location"`
	LiveModel       string `json:"live_model"`
	// LiveModels are the Live models the Vertex voice offers: the ones
	// checked on Vertex, and the configured one when the voice is on
	// Vertex already.
	LiveModels   []string `json:"live_models"`
	Voice        string   `json:"voice"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"default_model"`
	APIURL       string   `json:"api_url"`
}

// SetupVoice is the voice side of SetupState.
type SetupVoice struct {
	Provider string `json:"provider"`
	// Backend is the Gemini Live backend: aistudio or vertex.
	Backend     string `json:"backend"`
	Status      string `json:"status"`
	Problem     string `json:"problem,omitempty"`
	SetupNeeded bool   `json:"setup_needed"`
	// Gemini is the Gemini Live model and voice (AI Studio and Vertex
	// share them), OpenAI GPT-Live's; OpenAIPrice is GPT-Live's price.
	Gemini      SetupVoicePick `json:"gemini"`
	OpenAI      SetupVoicePick `json:"openai"`
	OpenAIPrice string         `json:"openai_price"`
}

// SetupOrchestrator is the orchestrator side of SetupState.
type SetupOrchestrator struct {
	On        bool   `json:"on"`
	Provider  string `json:"provider"`
	Model     string `json:"model,omitempty"`
	OffReason string `json:"off_reason,omitempty"`
	// Preset is the preset the saved provider matches (other: an
	// OpenAI-compatible entry that is no preset, or one whose base URL
	// was changed by hand); BaseURL is that entry's base URL.
	Preset  string `json:"preset"`
	BaseURL string `json:"base_url,omitempty"`
}

// SetupPreset is one orchestrator preset as the page shows it. KeySaved:
// a key is saved under its KeyEnv. ReuseOffered: the voice's OpenAI key
// is saved, so the OpenAI preset may offer to reuse it; ReuseActive: the
// saved OpenAI entry uses it (api_key_env = OPENAI_API_KEY).
type SetupPreset struct {
	onboard.Preset
	KeySaved     bool `json:"key_saved"`
	ReuseOffered bool `json:"reuse_offered,omitempty"`
	ReuseActive  bool `json:"reuse_active,omitempty"`
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
	Enabled   bool           `json:"enabled"`
	Bind      string         `json:"bind"`
	HasToken  bool           `json:"has_token"`
	Addresses []pair.Address `json:"addresses"`
	Tailscale bool           `json:"tailscale"`
	// Explain says which address the QR leads with (pair.Explain).
	Explain     string     `json:"explain,omitempty"`
	Connected   bool       `json:"connected"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	Remote      string     `json:"remote,omitempty"`
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
		Keys:   SetupKeys{Gemini: len(cfg.Gemini.APIKeys) > 0, OpenAI: cfg.Voice.OpenAI.APIKey != "", DeepSeek: cfg.DeepSeek.APIKey != "", Vertex: cfg.Gemini.Vertex.APIKey != "", Anthropic: s.savedKey("ANTHROPIC_API_KEY") != ""},
		Voice:  SetupVoice{Provider: s.voiceProvider(), Backend: cfg.Gemini.Backend, Status: hs.status.String()},
		Vertex: setupVertex(cfg),
		Links:  map[string]string{"get_key": GeminiKeyURL, "testflight": TestFlightURL, "ios_repo": IOSRepoURL, "models_doc": ModelsDocURL, "remote_doc": RemoteDocURL},
	}
	st.Voice.Gemini, st.Voice.OpenAI = s.setupVoicePicks()
	st.Voice.OpenAIPrice = onboard.GPTLivePrice
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
	st.Orchestrator.Preset, st.Orchestrator.BaseURL = s.presetOf(st.Orchestrator.Provider)
	st.Presets = s.setupPresets()
	st.Start = onboard.Start()
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

// presets are the orchestrator presets the page offers.
func (s *Server) presets() []onboard.Preset {
	if s.deps.Setup.Presets != nil {
		return s.deps.Setup.Presets
	}
	return onboard.Presets()
}

// compatPreset is the compat preset named id.
func (s *Server) compatPreset(id string) (onboard.Preset, bool) {
	p, ok := onboard.PresetByID(s.presets(), id)
	return p, ok && p.Kind == onboard.PresetCompat
}

func normURL(u string) string { return onboard.NormURL(u) }

// presetOf is the preset a saved orchestrator provider matches, and the
// base URL of its OpenAI-compatible entry.
func (s *Server) presetOf(provider string) (string, string) {
	return onboard.PresetOf(s.presets(), s.deps.Setup.Config, provider)
}

// savedKey is the key saved under the env-file entry env ("" when none):
// the env file first (it may be newer than this boot), then what the
// daemon loaded. Never sent to the page.
func (s *Server) savedKey(env string) string {
	sd := s.deps.Setup
	if env == "" {
		return ""
	}
	if sd.EnvPath != "" {
		if saved, err := onboard.ReadEnvFile(sd.EnvPath); err == nil {
			if v := strings.TrimSpace(saved[env]); v != "" {
				return v
			}
		}
	}
	cfg := sd.Config
	switch env {
	case "OPENAI_API_KEY":
		return cfg.Voice.OpenAI.APIKey
	case "DEEPSEEK_API_KEY":
		return cfg.DeepSeek.APIKey
	case "ANTHROPIC_API_KEY":
		return cfg.Anthropic.APIKey
	case "VERTEX_API_KEY":
		return cfg.Gemini.Vertex.APIKey
	case "GEMINI_API_KEY":
		if len(cfg.Gemini.APIKeys) > 0 {
			return cfg.Gemini.APIKeys[0]
		}
	}
	for _, o := range cfg.Brain.OpenAICompat {
		if o.APIKeyEnv == env && o.APIKey != "" {
			return o.APIKey
		}
	}
	return ""
}

// compatKeyEnv is where the key of the OpenAI-compatible provider name
// lives (see onboard.CompatKeyEnv).
func (s *Server) compatKeyEnv(name string, reuse bool) string {
	return onboard.CompatKeyEnv(s.deps.Setup.Config, name, reuse)
}

// setupPresets is the preset list with what is saved for each.
func (s *Server) setupPresets() []SetupPreset {
	cfg := s.deps.Setup.Config
	out := []SetupPreset{}
	for _, p := range s.presets() {
		sp := SetupPreset{Preset: p}
		switch p.Kind {
		case onboard.PresetNative, onboard.PresetCompat:
			env := p.KeyEnv
			if p.Kind == onboard.PresetCompat {
				env = s.compatKeyEnv(p.ID, false)
			}
			sp.KeySaved = s.savedKey(env) != ""
		}
		if p.ReuseEnv != "" {
			sp.ReuseOffered = s.savedKey(p.ReuseEnv) != ""
			c, ok := cfg.Brain.CompatProvider(p.ID)
			sp.ReuseActive = ok && c.APIKeyEnv == p.ReuseEnv
		}
		out = append(out, sp)
	}
	return out
}

// DefaultVertexLocation is the location the setup page offers for the
// Vertex voice.
const DefaultVertexLocation = onboard.DefaultVertexLocation

func setupVertex(cfg config.Config) SetupVertex {
	v := SetupVertex{
		Project: cfg.Gemini.Vertex.Project, Location: cfg.Gemini.Vertex.Location, DefaultLocation: DefaultVertexLocation,
		LiveModel: cfg.Gemini.Model, Voice: cfg.Gemini.Voice, DefaultModel: brain.DefaultModel, APIURL: onboard.VertexAPIURL,
		Models: []string{}, LiveModels: append([]string(nil), gemini.VertexModels...),
	}
	// The configured model counts as known on Vertex only when the voice
	// already runs there; an AI Studio pick may not be served on Vertex.
	if cfg.Gemini.Backend == "vertex" {
		v.LiveModels = withFirst(gemini.VertexModels, cfg.Gemini.Model)
	}
	for _, m := range brain.VertexModels {
		v.Models = append(v.Models, m.Name)
	}
	return v
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
			p.Explain = pair.Explain(addrs)
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
type SetupValidateRequest = onboard.ValidateRequest

// SetupCheck is one check's outcome for the page.
type SetupCheck = onboard.Check

func checkOf(r onboard.Result) SetupCheck { return onboard.CheckOf(r) }

// validateKey checks one key live (no key in any result).
func (s *Server) validateKey(ctx context.Context, req SetupValidateRequest) SetupCheck {
	return onboard.ValidateKey(ctx, s.deps.Setup.Endpoints, s.deps.Setup.Config, s.presets(), req)
}

// savedVertexKey is the saved Vertex key, only for the project it was
// saved for.
func (s *Server) savedVertexKey(project string) string {
	return onboard.SavedVertexKey(s.deps.Setup.Config, project)
}

// compatTarget is the provider name and base URL a request means.
func (s *Server) compatTarget(preset, name, base string) (string, string) {
	return onboard.CompatTarget(s.presets(), preset, name, base)
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
type SetupKeysRequest = onboard.ModelsInput

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
	ch, msg := onboard.ResolveModels(cfg, s.presets(), req)
	if msg != "" {
		return SetupKeysResponse{Error: msg}, http.StatusBadRequest
	}
	voiceP, orch := ch.Voice, ch.Orch
	type need struct {
		kind, env string
		given     string
		have      bool
		req       SetupValidateRequest
		// recheck: validate the saved key when none is given.
		recheck bool
		// missing replaces the "not saved yet" detail, fix its fix.
		missing, fix string
		// kept replaces the "keeping the saved key" detail.
		kept string
	}
	var needs []need
	if vertexProject := ch.VertexProject; vertexProject != "" {
		n := need{kind: "vertex", env: "VERTEX_API_KEY", given: req.Vertex.Key, have: s.savedVertexKey(vertexProject) != "",
			req: SetupValidateRequest{Kind: "vertex", Project: vertexProject}}
		if voiceP == "vertex" {
			n.req.Location, n.req.Voice, n.req.LiveModel = ch.VertexLocation, true, ch.VoiceSel.Model
			// A new location or Live model for the voice is checked with
			// the saved key: the model must be served there.
			n.recheck = ch.VertexLocation != cfg.Gemini.Vertex.Location || (ch.VoiceSel.Model != "" && ch.VoiceSel.Model != cfg.Gemini.Model)
		}
		if cur := cfg.Gemini.Vertex; cur.APIKey != "" && cur.Project != vertexProject {
			n.missing = fmt.Sprintf("the saved Vertex key is for project %s; paste the key for project %s", cur.Project, vertexProject)
		}
		needs = append(needs, n)
	}
	if voiceP == "gemini" || orch == brain.ProviderGemini {
		needs = append(needs, need{kind: "gemini", env: "GEMINI_API_KEY", given: req.GeminiKey, have: len(cfg.Gemini.APIKeys) > 0})
	}
	if voiceP == "openai" {
		needs = append(needs, need{kind: "openai", env: "OPENAI_API_KEY", given: req.OpenAIKey, have: cfg.Voice.OpenAI.APIKey != ""})
	}
	switch orch {
	case brain.ProviderDeepSeek:
		needs = append(needs, need{kind: "deepseek", env: "DEEPSEEK_API_KEY", given: req.DeepSeekKey, have: cfg.DeepSeek.APIKey != ""})
	case brain.ProviderAnthropic:
		needs = append(needs, need{kind: "anthropic", env: "ANTHROPIC_API_KEY", given: req.AnthropicKey, have: s.savedKey("ANTHROPIC_API_KEY") != ""})
	}
	if c := ch.Compat; c != nil {
		if ch.Reuse {
			// The voice's key, on the user's explicit yes: never written
			// from here, only pointed at.
			have := s.savedKey(c.APIKeyEnv) != "" || (voiceP == "openai" && strings.TrimSpace(req.OpenAIKey) != "")
			needs = append(needs, need{kind: "reuse", env: c.APIKeyEnv, have: have, kept: "using the same OpenAI key as the voice",
				missing: "no OpenAI voice key is saved to reuse", fix: "untick \"Use the same OpenAI key\" and paste a key for the orchestrator"})
		} else {
			// A keyed preset always needs its key; a typed loopback URL
			// (a local server) may run without one.
			keyOptional := c.KeyOptional() && (!ch.IsPreset || !ch.Preset.KeyNeeded)
			needs = append(needs, need{kind: "compat", env: c.APIKeyEnv, given: req.Compat.Key, have: s.savedKey(c.APIKeyEnv) != "" || keyOptional,
				req: SetupValidateRequest{Kind: "compat", Name: c.Name, BaseURL: c.BaseURL}})
		}
	}
	resp := SetupKeysResponse{Checks: []SetupCheck{}}
	set := map[string]string{}
	failed := false
	for _, n := range needs {
		v := strings.TrimSpace(n.given)
		if v == "" && n.have && n.recheck {
			vr := n.req
			c := s.validateKey(ctx, vr)
			resp.Checks = append(resp.Checks, withName(c, n.env))
			if c.Status == "fail" {
				failed = true
			}
			continue
		}
		if v == "" {
			if n.have {
				kept := "keeping the saved key"
				if n.kept != "" {
					kept = n.kept
				}
				resp.Checks = append(resp.Checks, SetupCheck{Name: n.env, Status: "ok", Detail: kept})
				continue
			}
			failed = true
			fix := "paste the key"
			if n.kind == "gemini" {
				fix = "get a free key at " + GeminiKeyURL + " and paste it here"
			}
			if n.kind == "anthropic" {
				fix = "create a key at " + brain.ServiceOf(brain.ProviderAnthropic).Console + " and paste it here"
			}
			if n.kind == "vertex" {
				fix = "paste a Vertex AI API key bound to a service account in that project (" + onboard.VertexKeyDocURL + ")"
			}
			detail := n.env + " is needed and not saved yet"
			if n.missing != "" {
				detail = n.missing
			}
			if n.fix != "" {
				fix = n.fix
			}
			resp.Checks = append(resp.Checks, SetupCheck{Name: n.env, Status: "fail", Detail: detail, Fix: fix})
			continue
		}
		vr := n.req
		if vr.Kind == "" {
			vr.Kind = n.kind
		}
		vr.Key = v
		c := s.validateKey(ctx, vr)
		if n.kind == "vertex" {
			c = withName(c, n.env)
		}
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
	names, err := onboard.SaveModels(sd.ConfigPath, sd.EnvPath, saved, ch, set)
	if err != nil {
		resp.Error = err.Error()
		return resp, http.StatusInternalServerError
	}
	if sd.ClearOverrides != nil {
		if err := sd.ClearOverrides(); err != nil {
			s.log.Log(logSource, "web.setup_error", "", map[string]any{"step": "clear_overrides", "error": err.Error()})
		}
	}
	s.log.Log(logSource, "web.setup_saved", "", map[string]any{"step": "models", "vars": names, "voice": voiceP, "orchestrator": ch.Provider})
	resp.OK = true
	resp.Restarting = s.deps.Restart != nil
	return resp, http.StatusOK
}

// withName is c named name (the env variable the page maps results by).
func withName(c SetupCheck, name string) SetupCheck {
	c.Name = name
	return c
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
