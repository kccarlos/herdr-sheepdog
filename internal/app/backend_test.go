package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
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

func TestClaudeHookAcceptsWatchedSocketOnly(t *testing.T) {
	srv := hctest.New(t)
	cfg := testConfig(t)
	cfg.Herdr.Backend = config.HerdrBackendHC
	cfg.Herdr.HCSocket = srv.Path

	a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrSocket, CLI: CLIOff, Brain: BrainOff})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.elog.Close() }()
	if err := a.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		_ = a.web.Serve(ctx, time.Second)
	}()
	t.Cleanup(func() {
		cancel()
		<-srvDone
	})

	// Build a complete 50-line Claude Bash prompt (>40-line ParsePrompt window).
	var cmdLines, screenLines []string
	screenLines = append(screenLines,
		"────────────────────────────────────────────────────────────────────────────────",
		" Bash command",
		" Echo 50 lines",
		"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	)
	for i := 1; i <= 50; i++ {
		l := fmt.Sprintf("echo line-%02d", i)
		cmdLines = append(cmdLines, l)
		screenLines = append(screenLines, " │ "+l)
	}
	screenLines = append(screenLines,
		"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. No",
	)
	fullCmd := strings.Join(cmdLines, "\n")
	fullScreen := strings.Join(screenLines, "\n")
	hookBody, _ := json.Marshal(map[string]any{
		"hook_event_name": "PermissionRequest",
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": fullCmd},
	})

	postHook := func(sock string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, a.URL()+"/api/hooks/claude-permission", bytes.NewReader(hookBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Sheepdog-Pane", "t1")
		if sock != "" {
			req.Header.Set("X-Sheepdog-Socket", sock)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	injectBlocked := func() string {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"pane_id": "t1", "alias": "auth", "screen": fullScreen})
		resp, err := http.Post(a.URL()+"/api/inject/blocked", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		var st struct {
			Questions []struct {
				Command string `json:"command"`
			} `json:"questions"`
		}
		getJSON(t, a.URL()+"/api/state", &st)
		if len(st.Questions) != 1 {
			t.Fatalf("questions = %+v, want 1", st.Questions)
		}
		b2, _ := json.Marshal(map[string]any{"pane_id": "t1", "state": "working"})
		r2, err := http.Post(a.URL()+"/api/inject/state", "application/json", bytes.NewReader(b2))
		if err != nil {
			t.Fatal(err)
		}
		_ = r2.Body.Close()
		return st.Questions[0].Command
	}

	// Missing or mismatched socket header must be ignored.
	postHook("")
	if got := injectBlocked(); got != "" {
		t.Fatalf("missing socket header attached command: %q", got)
	}
	postHook(filepath.Join(filepath.Dir(srv.Path), "other.sock"))
	if got := injectBlocked(); got != "" {
		t.Fatalf("wrong socket header attached command: %q", got)
	}

	// Matching socket (even via symlinked /tmp vs /private/tmp) attaches the command.
	resolvedSock, err := filepath.EvalSymlinks(srv.Path)
	if err != nil {
		resolvedSock = srv.Path
	}
	postHook(resolvedSock)
	if got := injectBlocked(); got != fullCmd {
		t.Fatalf("matching socket command = %q, want fullCmd", got)
	}
}
