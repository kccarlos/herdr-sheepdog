package app

import (
	"context"
	"errors"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
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
