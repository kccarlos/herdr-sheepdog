package app

import (
	"context"
	"errors"
	"testing"

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

// herdr-core's live terminal is listed only while its core can attach, and
// its bridge needs no herdr binary.
func TestHCTermStartsABridgeWithoutHerdrBin(t *testing.T) {
	srv := hctest.New(t)
	srv.Add(hc.Terminal{Address: "t1", State: "live", Argv: []string{"claude"}})
	deps := hcTerm(herdrcore.New(hc.NewClient(srv.Path), nil))
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
	if deps.Bin != "" || deps.Off == "" {
		t.Errorf("deps = %+v, want no herdr binary and an off message", deps)
	}
}
