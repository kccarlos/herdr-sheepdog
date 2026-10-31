package app

import (
	"context"
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
	socket := cfg.Herdr.Socket
	if kind == config.HerdrBackendHC {
		socket = hcSocket
	}
	a.log.Log("daemon", "daemon.herdr_backend", "", map[string]any{"backend": kind, "reason": reason, "socket": socket})
	if kind == config.HerdrBackendHC {
		return core, hcTerm(core)
	}
	return old, herdrTerm(cfg, old)
}

// hcTerm is herdr-core's live terminal: one framed attach stream per
// channel, listed only while the core can attach. There is no phone
// shell (it opens a herdr workspace).
func hcTerm(core *herdrcore.Backend) *web.TermDeps {
	return &web.TermDeps{
		Check: core.TermReady,
		Off:   hcTermOff,
		Start: func(ctx context.Context, o herdr.BridgeOptions) (web.TermBridge, error) {
			b, err := core.StartBridge(ctx, o)
			if err != nil {
				return nil, err
			}
			return b, nil
		},
	}
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
	var oldUp, coreUp bool
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
	switch {
	case oldUp && coreUp:
		return config.HerdrBackendHerdr, "both herdr and herdr-core answer; herdr.backend = \"hc\" picks herdr-core"
	case coreUp:
		return config.HerdrBackendHC, "only herdr-core answers"
	case oldUp:
		return config.HerdrBackendHerdr, "only herdr answers"
	}
	return config.HerdrBackendHerdr, "neither answers yet; waiting for herdr"
}
