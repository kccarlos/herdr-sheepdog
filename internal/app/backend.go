package app

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
)

// backendProbe bounds each socket check of herdr.backend = auto.
const backendProbe = time.Second

// hcTermOff is /term's answer when herdr-core is not running or is too old
// to attach (the app shows the screen).
const hcTermOff = "the live terminal needs a running herdr-core that has terminal.attach"

// newBackend builds the backend herdr.backend picks (auto: asks both
// sockets once, docs/dev/design/herdr-core-backend.md "Choosing the
// backend") and logs the choice as daemon.herdr_backend, with the live
// terminal deps of that backend.
func (a *App) newBackend(cfg config.Config) (b herdr.Backend, term *web.TermDeps) {
	old := herdr.NewClient(cfg.Herdr.Socket)
	old.Log = a.log
	hcSocket := cfg.Herdr.HCSocketPath()
	core := herdrcore.New(hc.NewClient(hcSocket), a.log)

	kind, reason := chooseBackend(context.Background(), cfg.Herdr.Backend, old, core)
	a.herdrBackend = kind
	socket := cfg.Herdr.Socket
	if kind == config.HerdrBackendHC {
		socket = hcSocket
	}
	a.log.Log("daemon", "daemon.herdr_backend", "", map[string]any{"backend": kind, "reason": reason, "socket": socket})
	if kind == config.HerdrBackendHC {
		return core, hcTerm(core, cfg.Supervisor.ProjectRoots)
	}
	return old, herdrTerm(cfg, old)
}

// hcTerm is herdr-core's live terminal: one framed attach stream per
// channel, listed only while the core can attach. Its phone shells use the
// backend's existing space for the folder, or a new one.
func hcTerm(core *herdrcore.Backend, roots []string) *web.TermDeps {
	return &web.TermDeps{
		Check:        core.TermReady,
		Off:          hcTermOff,
		Shells:       shellHost(core),
		ProjectRoots: roots,
		Start: func(ctx context.Context, o herdr.BridgeOptions) (web.TermBridge, error) {
			b, err := core.StartBridge(ctx, o)
			if err != nil {
				return nil, err
			}
			return b, nil
		},
	}
}

// shellHost finds the phone-shell half of a backend the same way for both
// implementations.
func shellHost(backend herdr.Backend) web.ShellHost {
	h, _ := backend.(web.ShellHost)
	return h
}

// pinger is the liveness check chooseBackend asks each backend.
type pinger interface {
	Ping(ctx context.Context) (herdr.Pong, error)
}

// chooseBackend resolves herdr.backend to herdr or hc, with the reason.
// auto takes the one whose socket answers; the old herdr when both do
// (its users' behaviour is unchanged) or neither does (the daemon then
// waits for herdr, as before herdr-core existed).
func chooseBackend(ctx context.Context, want string, old, core pinger) (kind, reason string) {
	switch want {
	case config.HerdrBackendHerdr, config.HerdrBackendHC:
		return want, "herdr.backend=" + want
	}
	oldUp, coreUp := probeBackends(ctx, old, core)
	return ResolveBackend(want, oldUp, coreUp)
}

func probeBackends(ctx context.Context, old, core pinger) (oldUp, coreUp bool) {
	var wg sync.WaitGroup
	probe := func(p pinger, up *bool) {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(ctx, backendProbe)
		defer cancel()
		_, err := p.Ping(ctx)
		*up = err == nil
	}
	wg.Add(2)
	go probe(old, &oldUp)
	go probe(core, &coreUp)
	wg.Wait()
	return oldUp, coreUp
}

func (a *App) herdrBackends(ctx context.Context) []string {
	if a.opts.Herdr != HerdrSocket {
		return nil
	}
	oldUp, coreUp := probeBackends(ctx, herdr.NewClient(a.cfg.Herdr.Socket), herdrcore.New(hc.NewClient(a.cfg.Herdr.HCSocketPath()), nil))
	var out []string
	if oldUp {
		out = append(out, config.HerdrBackendHerdr)
	}
	if coreUp {
		out = append(out, config.HerdrBackendHC)
	}
	return out
}

func (a *App) herdrOverride() string {
	if a.opts.Herdr != HerdrSocket {
		return "set by --herdr=" + a.opts.Herdr
	}
	if v, ok := os.LookupEnv("SHEEPDOG_HERDR_BACKEND"); ok && strings.TrimSpace(v) != "" && !slices.Contains(a.opts.EnvFileNames, "SHEEPDOG_HERDR_BACKEND") {
		return "set by SHEEPDOG_HERDR_BACKEND in the environment"
	}
	return ""
}

// ResolveBackend resolves herdr.backend ("herdr", "hc" or "auto") from
// which sockets answer, returning the backend kind and reason.
func ResolveBackend(want string, oldUp, coreUp bool) (kind, reason string) {
	switch want {
	case config.HerdrBackendHerdr, config.HerdrBackendHC:
		return want, "pinned"
	}
	switch {
	case oldUp && coreUp:
		return config.HerdrBackendHerdr, "auto: both herdr-tui and herdr-core are running"
	case coreUp:
		return config.HerdrBackendHC, "auto: only herdr-core is running"
	case oldUp:
		return config.HerdrBackendHerdr, "auto: only herdr-tui is running"
	}
	return config.HerdrBackendHerdr, "auto: neither answers, waiting for herdr-tui"
}
