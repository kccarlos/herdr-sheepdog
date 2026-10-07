package tier3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DaemonOptions start a scratch daemon.
type DaemonOptions struct {
	// Bin is the sheepdog executable (the running binary by default).
	Bin string
	// Port is the web port (127.0.0.1 only).
	Port int
	// Dir holds the daemon's config, data and state.
	Dir string
	// Socket is the bench herdr session's API socket.
	Socket string
	// EnvFile is loaded by the daemon (--env-file): the orchestrator's keys.
	EnvFile string
	// BrainModel is brain.model (default gemini-3.8-flash, the daemon's
	// own setting on this machine).
	BrainModel string
	// Voice runs the daemon with the real voice model (--voice=gemini, on
	// Vertex) for a phone emulator, instead of --voice=off with the text
	// router and --heard-echo.
	Voice bool
	// ProjectRoots is supervisor.project_roots for the scratch daemon:
	// where the orchestrator may start a new agent by voice. It defaults to the
	// run's own repos directory, never the config default (~/Documents),
	// so a bench orchestrator can never start an agent in the user's real projects.
	ProjectRoots string
	// Log receives the daemon's stderr.
	Log io.Writer
}

// Daemon is a running scratch daemon.
type Daemon struct {
	opts DaemonOptions
	cmd  *exec.Cmd
	done chan struct{}
	http *http.Client

	mu     sync.Mutex
	lastID int64
}

// configTOML is the scratch daemon's whole config: nothing is read from
// ~/.config/sheepdog (no APNs, no aliases, no LAN bind). The orchestrator is
// the user's daemon's model on Vertex (its primary there is the AI
// Studio key, whose small quota the bench leaves alone); investigations
// use Claude (Haiku), never the scarce Codex quota.
const configTOML = `# tier-3 bench scratch daemon (generated)
[brain]
provider = "vertex"
model = %q

[gemini]
backend = "vertex"

[cli]
prefer = "claude"

[web]
bind = "127.0.0.1"
`

// StartDaemon starts `sheepdog serve` bound to the bench session and
// waits until it answers.
func StartDaemon(ctx context.Context, o DaemonOptions) (*Daemon, error) {
	if o.Bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		o.Bin = exe
	}
	if o.BrainModel == "" {
		o.BrainModel = "gemini-3.8-flash"
	}
	data := filepath.Join(o.Dir, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(o.Dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(configTOML, o.BrainModel)), 0o600); err != nil {
		return nil, err
	}
	args := serveArgs(o, cfgPath, data)
	if o.EnvFile != "" {
		env, err := filterEnvFile(o.EnvFile, filepath.Join(o.Dir, "daemon.env"))
		if err != nil {
			return nil, err
		}
		args = append(args, "--env-file", env)
	}
	cmd := exec.Command(o.Bin, args...)
	cmd.Dir = o.Dir
	cmd.Env = append(scrubEnv(os.Environ()),
		"SHEEPDOG_HERDR_SOCKET="+o.Socket,
		"SHEEPDOG_DATA_DIR="+data,
		"SHEEPDOG_STATE_PATH="+filepath.Join(o.Dir, "state.json"),
		"SHEEPDOG_WEB_BIND=127.0.0.1",
		"SHEEPDOG_CLI_PREFER=claude",
		"SHEEPDOG_SUPERVISOR_PROJECT_ROOTS="+projectRoots(o),
	)
	cmd.Stdout, cmd.Stderr = o.Log, o.Log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("tier3: start daemon: %w", err)
	}
	d := &Daemon{opts: o, cmd: cmd, done: make(chan struct{}), http: &http.Client{Timeout: 120 * time.Second}}
	go func() { _ = cmd.Wait(); close(d.done) }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-d.done:
			return nil, fmt.Errorf("tier3: the daemon exited at start (see its log)")
		case <-ctx.Done():
			d.Stop()
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		var hello map[string]any
		if err := d.get(ctx, "/api/hello", &hello); err == nil {
			return d, nil
		}
	}
	d.Stop()
	return nil, fmt.Errorf("tier3: the daemon did not answer on port %d", o.Port)
}

// projectRoots is the scratch daemon's supervisor.project_roots: the
// option, or the run's repos directory next to the daemon's.
func projectRoots(o DaemonOptions) string {
	if o.ProjectRoots != "" {
		return o.ProjectRoots
	}
	return filepath.Join(filepath.Dir(o.Dir), "repos")
}

// serveArgs are the scratch daemon's flags: the text router with the
// heard echo, or (Voice) the real voice model for the phone emulator.
func serveArgs(o DaemonOptions, cfgPath, data string) []string {
	voice := []string{"--voice=off"}
	if o.Voice {
		voice = []string{"--voice=gemini"}
	}
	args := append([]string{"serve"}, voice...)
	args = append(args, "--herdr=socket", "--cli=on", "--brain=on", "--dev")
	if !o.Voice {
		args = append(args, "--heard-echo")
	}
	return append(args, "--port", fmt.Sprint(o.Port), "--config", cfgPath, "--data-dir", data)
}

// BaseURL is the daemon's web root (the phone emulator's BaseURL).
func (d *Daemon) BaseURL() string { return d.url("") }

// daemonEnvKeys are the env-file keys the scratch daemon gets: the
// Vertex orchestrator and the DeepSeek failover. GEMINI_API_KEY (the AI Studio
// free tier, a small quota the user's own daemon needs) and the voice
// keys stay out.
var daemonEnvKeys = map[string]bool{"VERTEX_API_KEY": true, "VERTEX_PROJECT": true, "VERTEX_LOCATION": true, "DEEPSEEK_API_KEY": true}

// filterEnvFile copies the daemonEnvKeys lines of src to dst (0600);
// values are never logged.
func filterEnvFile(src, dst string) (string, error) {
	b, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		k, _, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export ")), "=")
		if ok && daemonEnvKeys[strings.TrimSpace(k)] {
			out = append(out, strings.TrimSpace(line))
		}
	}
	if err := os.WriteFile(dst, []byte(strings.Join(out, "\n")+"\n"), 0o600); err != nil {
		return "", err
	}
	return dst, nil
}

// scrubEnv drops inherited SHEEPDOG_* settings, so only the bench's own
// apply (a shell that points at the user's daemon must not leak in).
func scrubEnv(env []string) []string {
	out := env[:0:0]
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(e, "SHEEPDOG_") || k == "GEMINI_API_KEY" || strings.HasPrefix(k, "GEMINI_API_KEY_") || k == "OPENAI_API_KEY" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Stop ends the daemon (SIGTERM to its process group, then SIGKILL).
func (d *Daemon) Stop() {
	if d == nil || d.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-d.done:
	case <-time.After(8 * time.Second):
		_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGKILL)
		<-d.done
	}
}

// DataDir is where the daemon writes its event log.
func (d *Daemon) DataDir() string { return filepath.Join(d.opts.Dir, "data") }

func (d *Daemon) url(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", d.opts.Port, path)
}

func (d *Daemon) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url(path), nil)
	if err != nil {
		return err
	}
	return d.do(req, v)
}

func (d *Daemon) post(ctx context.Context, path string, body, v any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url(path), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return d.do(req, v)
}

func (d *Daemon) do(req *http.Request, v any) error {
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %d %s", req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if v == nil {
		return nil
	}
	return json.Unmarshal(b, v)
}

// Reply is the daemon's answer to an utterance.
type Reply struct {
	Spoken  string   `json:"spoken"`
	Options []string `json:"options"`
	Pending bool     `json:"pending"`
	Tool    string   `json:"tool"`
}

// Say sends the user's words through the text router.
func (d *Daemon) Say(ctx context.Context, text string) (Reply, error) {
	var r Reply
	err := d.post(ctx, "/api/inject/utterance", map[string]string{"text": text}, &r)
	return r, err
}

// AgentRow is an /api/state agent.
type AgentRow struct {
	PaneID         string `json:"pane_id"`
	Alias          string `json:"alias"`
	Kind           string `json:"kind"`
	WorkspaceLabel string `json:"workspace_label"`
	State          string `json:"state"`
	Spoken         string `json:"spoken"`
	Title          string `json:"title"`
}

// Agents lists the daemon's roster.
func (d *Daemon) Agents(ctx context.Context) ([]AgentRow, json.RawMessage, error) {
	var st struct {
		Agents json.RawMessage `json:"agents"`
	}
	if err := d.get(ctx, "/api/state", &st); err != nil {
		return nil, nil, err
	}
	var rows []AgentRow
	if err := json.Unmarshal(st.Agents, &rows); err != nil {
		return nil, st.Agents, err
	}
	return rows, st.Agents, nil
}

// Event is a daemon event.
type Event struct {
	ID      int64           `json:"id"`
	TS      time.Time       `json:"ts"`
	Source  string          `json:"source"`
	Type    string          `json:"type"`
	PaneID  string          `json:"pane_id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// NewEvents returns the events logged since the last call (from the
// in-memory tail; the full log is read from disk at the end).
func (d *Daemon) NewEvents(ctx context.Context) ([]Event, error) {
	var evs []Event
	if err := d.get(ctx, "/api/events?n=2000", &evs); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Event
	for _, e := range evs {
		if e.ID > d.lastID {
			out = append(out, e)
		}
	}
	if len(out) > 0 {
		d.lastID = out[len(out)-1].ID
	}
	return out, nil
}

// AllEvents reads the daemon's whole event log from its data dir.
func AllEvents(dataDir string) ([]Event, error) {
	files, err := filepath.Glob(filepath.Join(dataDir, "events", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, line := range bytes.Split(b, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var e Event
			if json.Unmarshal(line, &e) == nil {
				out = append(out, e)
			}
		}
	}
	return out, nil
}
