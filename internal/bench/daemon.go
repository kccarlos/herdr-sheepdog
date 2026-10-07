// Package bench is the voice benchmark (tier 2, `sheepdog bench`):
// synthetic speech (internal/bench/speech) from a phone emulator
// (internal/bench/voiceclient) into a scratch daemon with the real voice
// model and the real orchestrator over a mock herdr whose workers follow the
// eval sim's scripted worlds (internal/bench/world); an LLM plays the
// user, hearing the bot through the voice model's output transcription,
// and the eval judge grades the call (docs/bench/README.md).
package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// DefaultPorts are the scratch daemons' ports (one per parallel worker);
// never the user's own daemon (7777).
var DefaultPorts = []int{7790, 7791, 7792}

// DaemonOptions configure one scratch daemon.
type DaemonOptions struct {
	// Config is the base configuration (the user's file and .env: the
	// Vertex and DeepSeek credentials, gemini.backend); everything that
	// could touch the real daemon is overridden below.
	Config config.Config
	Port   int
	// Dir is the scratch directory (data dir and state file inside).
	Dir string
	// Voice is app.VoiceGemini (default) or app.VoiceOff.
	Voice string
	// Brain is app.BrainOn (default) or app.BrainOff (calibration).
	Brain string
	// BrainProvider and BrainModel pick the orchestrator (default deepseek,
	// deepseek-flash).
	BrainProvider, BrainModel string
	// DeepSeekBaseURL, when set, sends the DeepSeek orchestrator through this
	// root (the fault proxy).
	DeepSeekBaseURL string
	// Querier answers run_cli and investigations (the scenario's
	// scripted findings); nil leaves them unavailable (--cli=off).
	Querier supervisor.Querier
	// Log receives the daemon's one-line warnings.
	Log io.Writer
}

// Daemon is a running scratch daemon.
type Daemon struct {
	URL    string
	Dir    string
	app    *app.App
	cancel context.CancelFunc
	done   chan error
}

// ScratchConfig is o.Config made safe for a scratch daemon: loopback
// only, no token, its own data dir and state file, no phone
// notifications, the bench's orchestrator.
func ScratchConfig(o DaemonOptions) config.Config {
	cfg := o.Config
	cfg.DataDir = filepath.Join(o.Dir, "data")
	cfg.State.Path = filepath.Join(o.Dir, "state.json")
	cfg.Web.Bind = "127.0.0.1"
	cfg.Web.Port = o.Port
	cfg.Web.Token = ""
	cfg.Web.Dev = false
	cfg.Push.APNs.KeyPath = "" // never APNs the user's phone
	cfg.Herdr.Socket = filepath.Join(o.Dir, "no-herdr.sock")
	if cfg.Gemini.Backend == "vertex" {
		// The voice runs on Vertex; the AI Studio key pool would only
		// register the free-tier gemini orchestrator provider as a failover
		// target (the eval sim leaves it out unless asked, too).
		cfg.Gemini.APIKeys = nil
	}
	cfg.Brain.Enabled = true
	cfg.Brain.Provider = orStr(o.BrainProvider, brain.ProviderDeepSeek)
	cfg.Brain.Model = orStr(o.BrainModel, "deepseek-flash")
	if o.DeepSeekBaseURL != "" {
		cfg.DeepSeek.BaseURL = o.DeepSeekBaseURL
	}
	// Never the real codex/claude, whatever the querier.
	cfg.CLI.CodexBin = filepath.Join(o.Dir, "no-codex")
	cfg.CLI.ClaudeBin = filepath.Join(o.Dir, "no-claude")
	return cfg
}

// StartDaemon builds and runs a scratch daemon in process and waits
// until it answers /api/hello.
func StartDaemon(ctx context.Context, o DaemonOptions) (*Daemon, error) {
	if o.Port == 7777 {
		return nil, errors.New("bench: port 7777 is the user's daemon")
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, err
	}
	cfg := ScratchConfig(o)
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	stderr := o.Log
	if stderr == nil {
		stderr = io.Discard
	}
	opts := app.Options{
		Voice: orStr(o.Voice, app.VoiceGemini), Herdr: app.HerdrMock, Brain: orStr(o.Brain, app.BrainOn),
		CLI: app.CLIOff, Querier: o.Querier, Port: o.Port, Version: "bench", Stderr: stderr,
	}
	a, err := app.Build(cfg, opts)
	if err != nil {
		return nil, err
	}
	if err := a.Listen(); err != nil {
		return nil, err
	}
	rctx, cancel := context.WithCancel(context.Background())
	d := &Daemon{URL: a.URL(), Dir: o.Dir, app: a, cancel: cancel, done: make(chan error, 1)}
	go func() { d.done <- a.Run(rctx) }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(d.URL + "/api/hello")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return d, nil
			}
		}
		select {
		case err := <-d.done:
			cancel()
			return nil, fmt.Errorf("bench: daemon exited: %v", err)
		case <-ctx.Done():
			d.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			d.Close()
			return nil, errors.New("bench: daemon did not answer /api/hello in 15 s")
		}
	}
}

// RecycleVoice forces one Gemini resume (the voice session reconnects
// with its handle); false when the voice provider cannot.
func (d *Daemon) RecycleVoice(reason string) bool { return d.app.RecycleVoice(reason) }

// Close stops the daemon (and waits for it, at most 15 s).
func (d *Daemon) Close() {
	if d == nil {
		return
	}
	d.cancel()
	select {
	case <-d.done:
	case <-time.After(15 * time.Second):
	}
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
