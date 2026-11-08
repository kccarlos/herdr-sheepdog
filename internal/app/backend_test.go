package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
)

// fakePinger answers Ping, or fails when down.
type fakePinger struct{ down bool }

func (p fakePinger) Ping(context.Context) (herdr.Pong, error) {
	if p.down {
		return herdr.Pong{}, errors.New("no socket")
	}
	return herdr.Pong{Version: "1"}, nil
}

func TestChooseBackend(t *testing.T) {
	up, down := fakePinger{}, fakePinger{down: true}
	for _, tc := range []struct {
		name      string
		want      string
		old, core fakePinger
		kind      string
	}{
		{"auto, only herdr", config.HerdrBackendAuto, up, down, config.HerdrBackendHerdr},
		{"auto, only hc", config.HerdrBackendAuto, down, up, config.HerdrBackendHC},
		{"auto, both", config.HerdrBackendAuto, up, up, config.HerdrBackendHerdr},
		{"auto, neither", config.HerdrBackendAuto, down, down, config.HerdrBackendHerdr},
		{"forced hc, down", config.HerdrBackendHC, up, down, config.HerdrBackendHC},
		{"forced herdr, down", config.HerdrBackendHerdr, down, up, config.HerdrBackendHerdr},
	} {
		kind, reason := chooseBackend(context.Background(), tc.want, tc.old, tc.core)
		if kind != tc.kind || reason == "" {
			t.Errorf("%s: chooseBackend = %q (%q), want %q", tc.name, kind, reason, tc.kind)
		}
	}
}

// TestAwaitFirstHerdr: with neither herdr running at startup, auto takes
// whichever answers first: herdr-core restarts the daemon (so auto picks
// it), herdr-tui (or both) ends the wait on the backend it already uses.
func TestAwaitFirstHerdr(t *testing.T) {
	up, down := fakePinger{}, fakePinger{down: true}
	for _, tc := range []struct {
		name        string
		old, core   fakePinger
		wantRestart bool
	}{
		{"herdr-core first", down, up, true},
		{"herdr-tui first", up, down, false},
		{"both", up, up, false},
	} {
		var restarted []string
		done := make(chan struct{})
		go func() {
			awaitFirstHerdr(context.Background(), tc.old, tc.core, time.Millisecond, func(r string) { restarted = append(restarted, r) })
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: did not return", tc.name)
		}
		if (len(restarted) == 1) != tc.wantRestart {
			t.Errorf("%s: restarts = %q, want restart %v", tc.name, restarted, tc.wantRestart)
		}
	}
	// Neither: it keeps asking until the context ends, without a restart.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	awaitFirstHerdr(ctx, down, down, time.Millisecond, func(string) { t.Error("restarted with neither running") })
}

// herdr-core's live terminal is listed only while its core can attach, and
// its bridge needs no herdr binary.
func TestHCTermStartsABridgeWithoutHerdrBin(t *testing.T) {
	srv := hctest.New(t)
	srv.Add(hc.Terminal{Address: "t1", State: "live", Argv: []string{"claude"}})
	roots := []string{"/work/projects"}
	deps := hcTerm(herdrcore.New(hc.NewClient(srv.Path), nil), roots)
	ctx := context.Background()
	if err := deps.Check(ctx); err != nil {
		t.Fatalf("Check against a core with terminal.attach = %v", err)
	}
	b, err := deps.Start(ctx, herdr.BridgeOptions{Pane: "t1", Cols: 58, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	b.Release()
	select {
	case <-b.Done():
	default:
		t.Error("bridge not done after Release")
	}
	if deps.Bin != "" || deps.Off == "" || deps.Shells == nil || len(deps.ProjectRoots) != 1 || deps.ProjectRoots[0] != roots[0] {
		t.Errorf("deps = %+v, want its shell wire, project roots, no herdr binary and an off message", deps)
	}
}

func TestStateReportsChosenHerdrBackend(t *testing.T) {
	srv := hctest.New(t)
	cfg := testConfig(t)
	cfg.Herdr.Backend = config.HerdrBackendAuto
	cfg.Herdr.HCSocket = srv.Path
	a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrSocket, CLI: CLIOff, Brain: BrainOff, Drain: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	var st map[string]any
	getJSON(t, a.URL()+"/api/state", &st)
	if st["herdr"] != HerdrSocket || st["herdr_backend"] != config.HerdrBackendHC {
		t.Errorf("/api/state = %v, want herdr=socket herdr_backend=hc", st)
	}
}
