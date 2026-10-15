package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/world"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/demo"
)

func printDemoUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog demo [flags]")
	fprintln(w, "")
	fprintln(w, "Try sheepdog without herdr: a daemon with three scripted workers (docs finishes,")
	fprintln(w, "tests asks to run the test suite and then fails, refactor finishes later). Open")
	fprintln(w, "the printed /voice page, press Connect and talk: hear the news, answer the")
	fprintln(w, "approval, ask \"what's going on?\". The workers start moving once you connect.")
	fprintln(w, "")
	fprintln(w, "It needs GEMINI_API_KEY (free at https://aistudio.google.com/apikey) for the")
	fprintln(w, "voice, and uses your configured orchestrator (Gemini by default). It never")
	fprintln(w, "touches herdr or real agents and runs on its own port and data dir, so it is")
	fprintln(w, "safe next to a running daemon. Ctrl-C ends it.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --port N          port (default 7778)")
	fprintln(w, "  --config PATH     config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH   env file (default ./.env, else ~/.config/sheepdog/.env, as serve)")
	fprintln(w, "  --dir DIR         keep the demo's data here (default: a temp dir, removed at exit)")
	fprintln(w, "  --no-open         print the URL without opening the browser")
	fprintln(w, "  --start-now       start the workers at once, not when a client connects (for recordings)")
}

// demoOpen opens a URL in the browser (tests replace it).
var demoOpen = func(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, url).Start()
}

// runDemo implements `sheepdog demo`.
func runDemo(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printDemoUsage(stderr) }
	port := fs.Int("port", demo.DefaultPort, "port")
	configPath := fs.String("config", "", "config file")
	envFile := fs.String("env-file", "", "env file")
	dirFlag := fs.String("dir", "", "data dir")
	noOpen := fs.Bool("no-open", false, "do not open the browser")
	startNow := fs.Bool("start-now", false, "start the workers at once")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *port == 7777 {
		fprintln(stderr, "sheepdog demo: 7777 is the daemon's own port; pick another --port")
		return 2
	}
	if code, ok := loadServeEnvFile(*envFile, stderr); !ok {
		return code
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fprintf(stderr, "sheepdog demo: %v\n", err)
		return 1
	}
	if msg := demoKeyProblem(cfg); msg != "" {
		fprintln(stderr, "sheepdog demo: "+msg)
		return 1
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		_ = c.Close()
		fprintf(stderr, "sheepdog demo: %s is in use; pick another --port\n", addr)
		return 1
	}

	dir := *dirFlag
	if dir == "" {
		if dir, err = os.MkdirTemp("", "sheepdog-demo-"); err != nil {
			fprintf(stderr, "sheepdog demo: %v\n", err)
			return 1
		}
		defer func() { _ = os.RemoveAll(dir) }()
	}
	d, err := startDemo(ctx, cfg, *port, dir, stderr)
	if err != nil {
		fprintf(stderr, "sheepdog demo: %v\n", err)
		return 1
	}
	defer d.Close()

	url := d.URL + "/voice"
	fprintf(stdout, "sheepdog demo is running (orchestrator %s, data in %s).\n\n", cfg.Brain.Provider, dir)
	fprintf(stdout, "  Open %s, press Connect and allow the microphone.\n", url)
	fprintln(stdout, "  Three scripted workers (docs, tests, refactor) start moving once you connect:")
	fprintln(stdout, "  listen for the news, answer the approval, ask \"what's going on?\".")
	fprintf(stdout, "  %s/debug shows what the daemon, the voice and the orchestrator do.\n\n", d.URL)
	fprintln(stdout, "Ctrl-C ends the demo.")
	if !*noOpen {
		if err := demoOpen(url); err != nil {
			fprintf(stderr, "sheepdog demo: could not open the browser (%v); open %s yourself\n", err, url)
		}
	}
	if *startNow {
		fprintln(stdout, "The workers are moving.")
		d.arm()
	} else if !waitForClient(ctx, d.URL) {
		return 0
	} else {
		fprintln(stdout, "Connected: the workers are moving.")
		d.arm()
	}
	<-ctx.Done()
	return 0
}

// demoRun is a running demo: the scratch daemon and its scripted world.
type demoRun struct {
	URL   string
	d     *bench.Daemon
	w     *world.World
	armed sync.Once
}

// startDemo starts the demo daemon on port with its data in dir (the
// workers wait for arm).
func startDemo(ctx context.Context, cfg config.Config, port int, dir string, log io.Writer) (*demoRun, error) {
	sc, screens, err := demo.Scenario(dir)
	if err != nil {
		return nil, fmt.Errorf("scenario: %w", err)
	}
	d, err := bench.StartDaemon(ctx, bench.DaemonOptions{
		Config: cfg, Port: port, Dir: dir,
		BrainProvider: cfg.Brain.Provider, BrainModel: cfg.Brain.Model,
		Querier: world.Querier{Agents: sc.Agents}, Log: log,
	})
	if err != nil {
		return nil, err
	}
	w, err := world.New(sc, world.Options{BaseURL: d.URL, FixtureDir: screens})
	if err != nil {
		d.Close()
		return nil, err
	}
	if err := w.Start(ctx); err != nil {
		d.Close()
		return nil, err
	}
	return &demoRun{URL: d.URL, d: d, w: w}, nil
}

// arm starts the workers' timeline (once).
func (r *demoRun) arm() { r.armed.Do(r.w.Arm) }

// Close stops the world and the daemon.
func (r *demoRun) Close() {
	r.w.Close()
	r.d.Close()
}

// demoKeyProblem says what the demo lacks to talk, "" when it has it:
// the voice needs a Gemini key (AI Studio) unless it runs on Vertex or
// GPT-Live with their own credentials.
func demoKeyProblem(cfg config.Config) string {
	switch {
	case cfg.Voice.Provider == "openai" && cfg.Voice.OpenAI.APIKey != "":
		return ""
	case cfg.Gemini.Backend == "vertex":
		return ""
	case len(cfg.Gemini.APIKeys) > 0:
		return ""
	}
	return "no GEMINI_API_KEY: create a free key at https://aistudio.google.com/apikey, then run sheepdog init (or put GEMINI_API_KEY in ./.env)"
}

// waitForClient waits until a browser or the app is attached to /ws, so
// the scripted workers' timeline starts with the conversation. false
// when ctx ended first.
func waitForClient(ctx context.Context, base string) bool {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
		resp, err := http.Get(base + "/api/state")
		if err != nil {
			continue
		}
		var st struct {
			Voice struct {
				ClientConnected bool `json:"client_connected"`
			} `json:"voice"`
		}
		err = json.NewDecoder(resp.Body).Decode(&st)
		_ = resp.Body.Close()
		if err == nil && st.Voice.ClientConnected {
			return true
		}
	}
}
