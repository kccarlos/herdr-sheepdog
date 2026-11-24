package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/termproto"
)

// Untested guard from PR #623 (NeonH §5, Carbon §5): GET /api/agents/{pane}/files
// and /files/{path} must refuse a remote pane even when its cwd is a real local
// directory containing files.
func TestFilesRefusesRemoteAgentPane(t *testing.T) {
	localDir := t.TempDir()
	writeT(t, filepath.Join(localDir, "secret.txt"), "local-only\n")

	h := newHarness(t, harnessOpts{})
	trackIn(t, h, "box~t4", localDir)

	for _, path := range []string{
		windowPath("box~t4", "files"),
		windowPath("box~t4", "files/secret.txt"),
	} {
		resp, err := http.Get(h.http.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		if resp.StatusCode != http.StatusNotFound || body["reason"] != "no_cwd" {
			t.Errorf("GET %s = %d %s, want 404 no_cwd", path, resp.StatusCode, raw)
		}
		if strings.Contains(string(raw), "secret.txt") || strings.Contains(string(raw), "local-only") {
			t.Errorf("GET %s leaked local file: %s", path, raw)
		}
	}
}

// Blocking probe (NeonH §3.1, Carbon Probe 3): GET /api/spaces/{id}/files and
// /files/{path} must refuse a space whose panes are remote, even when the
// remote pane's cwd matches a real local directory with a file in it.
func TestFilesRefusesRemoteSpace(t *testing.T) {
	localDir := t.TempDir()
	writeT(t, filepath.Join(localDir, "secret.txt"), "local-only\n")

	hcSrv := hctest.New(t)
	hcSrv.Add(hc.Terminal{
		Address:  "box/t4",
		State:    "live",
		Argv:     []string{"claude"},
		Cwd:      localDir,
		Reported: true,
		Status:   &hc.Status{State: "idle", App: "claude-code"},
		Labels:   map[string]string{"herdr.space": "s-remote", "herdr.tab": "t-1"},
	})
	backend := herdrcore.New(hc.NewClient(hcSrv.Path), nil)

	log, err := eventlog.Open("", eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	st, err := state.Open("", state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	realSup, err := supervisor.New(st, backend, nil, inject.SkippedPusher{Log: log}, log, supervisor.Config{})
	if err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, harnessOpts{wrap: func(*supervisor.Supervisor) Supervisor { return realSup }})

	for _, path := range []string{
		"/api/spaces/s-remote/files",
		"/api/spaces/s-remote/files/secret.txt",
	} {
		resp, err := http.Get(h.http.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s served local directory for remote space: status=%d body=%s", path, resp.StatusCode, raw)
		}
		if strings.Contains(string(raw), "secret.txt") || strings.Contains(string(raw), "local-only") {
			t.Errorf("GET %s leaked local file: %s", path, raw)
		}
	}
}

// Blocking probe (NeonH §3.2, Carbon Probe 4): GET /api/agents/{pane}/changes
// and /changes/{path} must refuse a remote pane whose cwd points to a real
// local git repository, without running local git or reading local files.
func TestChangesRefusesRemotePane(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}

	repo := t.TempDir()
	gitT(t, repo, "init", "-q")
	writeT(t, filepath.Join(repo, "a.go"), "package a\n\nfunc A() {}\n")
	gitT(t, repo, "add", "a.go")
	gitT(t, repo, "commit", "-qm", "init")
	writeT(t, filepath.Join(repo, "a.go"), "package a\n\nfunc A() { localSecret() }\n")
	writeT(t, filepath.Join(repo, "untracked.txt"), "local-secret-line\n")

	h := newHarness(t, harnessOpts{})
	trackIn(t, h, "box~t4", repo)

	// Changes list must not run local git or list local modified/untracked files.
	resp, err := http.Get(h.http.URL + windowPath("box~t4", "changes"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		var got ChangesResponse
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode changes: %v (%s)", err, raw)
		}
		if got.Repo || len(got.Files) != 0 || got.Reason != "no_cwd" {
			t.Errorf("GET changes on remote pane ran local git: %+v", got)
		}
	} else if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET changes on remote pane = %d %s, want 200 no_cwd or 404/403", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "a.go") || strings.Contains(string(raw), "untracked.txt") {
		t.Errorf("GET changes leaked local repo files: %s", raw)
	}

	// Individual file diffs must not read or diff local tracked or untracked files.
	for _, p := range []string{"changes/a.go", "changes/untracked.txt"} {
		resp, err := http.Get(h.http.URL + windowPath("box~t4", p))
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		diffRaw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s on remote pane served local diff: %s", p, diffRaw)
		}
		if strings.Contains(string(diffRaw), "localSecret") || strings.Contains(string(diffRaw), "local-secret-line") {
			t.Errorf("GET %s leaked local file contents: %s", p, diffRaw)
		}
	}
}

// Blocking probe (NeonH §3.4, Carbon Probe 5): a remote agent's Cwd must never
// whitelist a local directory outside supervisor.project_roots in shellDir or
// /term shell-open.
func TestShellDirIgnoresRemoteAgentCwd(t *testing.T) {
	h, fs, _ := newShellHarness(t, true)
	outside := t.TempDir()
	writeT(t, filepath.Join(outside, "secret.txt"), "outside-project-roots\n")
	realOutside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	trackIn(t, h, "box~t4", realOutside)

	if dir, err := h.srv.term.shellDir(outside); err == nil {
		t.Errorf("shellDir(%q) = %q, nil; remote agent cwd bypassed project_roots", outside, dir)
	}

	c, _ := helloReady(t, h)
	c.send(shellOpen(1, outside))
	if m := c.expect(termproto.TypeClosed); m.Reason != termproto.RefusedOutsideProjects {
		t.Errorf("term-open shell in remote agent cwd = %+v, want refused outside_projects", m)
	}
	if calls := fs.createCalls(); len(calls) != 0 {
		t.Errorf("shell opened outside project_roots via remote agent cwd: %v", calls)
	}
}

// Blocking probe (NeonH §3.7, Carbon §3.7): POST /api/agents/{pane}/attachments
// must refuse remote panes instead of writing dead local files on this host.
func TestAttachmentRefusesRemotePane(t *testing.T) {
	data := t.TempDir()
	localDir := t.TempDir()
	writeT(t, filepath.Join(localDir, "secret.txt"), "local\n")

	h := newHarness(t, harnessOpts{dataDir: data})
	trackIn(t, h, "box~t4", localDir)

	code, _, raw := upload(t, h, "box~t4", "note.txt", []byte("hello\n"))
	if code == http.StatusCreated || code < 400 {
		t.Fatalf("POST attachments for remote pane = %d %s, want 4xx refusal", code, raw)
	}
	if entries, err := os.ReadDir(filepath.Join(data, "attachments", "box_t4")); err == nil && len(entries) != 0 {
		t.Fatalf("remote attachment wrote local files: %v", entries)
	}
}

// Sanity check that SpaceFolder itself returns "" via the real Supervisor when
// wired to herdrcore with a remote-only space.
func TestSpaceFolderViaHerdrcoreReturnsEmptyForRemoteSpace(t *testing.T) {
	localDir := t.TempDir()
	writeT(t, filepath.Join(localDir, "secret.txt"), "local-only\n")

	hcSrv := hctest.New(t)
	hcSrv.Add(hc.Terminal{
		Address: "box/t4",
		State:   "live",
		Argv:    []string{"claude"},
		Cwd:     localDir,
		Labels:  map[string]string{"herdr.space": "s-remote", "herdr.tab": "t-1"},
	})
	backend := herdrcore.New(hc.NewClient(hcSrv.Path), nil)
	log, err := eventlog.Open("", eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	st, err := state.Open("", state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sup, err := supervisor.New(st, backend, nil, inject.SkippedPusher{Log: log}, log, supervisor.Config{})
	if err != nil {
		t.Fatal(err)
	}

	dir, err := sup.SpaceFolder(context.Background(), "s-remote")
	if err != nil {
		t.Fatalf("SpaceFolder(s-remote) error = %v", err)
	}
	if dir != "" {
		t.Fatalf("SpaceFolder(s-remote) = %q, want empty string for remote-only space", dir)
	}
}

// Round-2 blocking probe (Carbon Bug 1): state.Store.Apply and GET /api/state
// must never expose a remote agent's Cwd as a local path, so clients do not
// offer "Open shell here" targeting this Mac.
func TestStateEndpointOmitsRemoteAgentCwd(t *testing.T) {
	localDir := t.TempDir()
	h := newHarness(t, harnessOpts{})

	if _, _, err := h.store.Apply(herdr.Transition{
		PaneID:         "box~t4",
		To:             herdr.StateIdle,
		Agent:          "claude",
		WorkspaceID:    "s-remote",
		WorkspaceLabel: "remote",
		Cwd:            localDir,
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(h.http.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/state = %d %s", resp.StatusCode, raw)
	}
	var got StateResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode /api/state: %v (%s)", err, raw)
	}
	var found bool
	for _, a := range got.Agents {
		if a.PaneID == "box~t4" {
			found = true
			if a.Cwd != "" {
				t.Fatalf("GET /api/state exposed remote agent cwd = %q, want empty", a.Cwd)
			}
		}
	}
	if !found {
		t.Fatalf("GET /api/state missing remote agent box~t4: %+v", got.Agents)
	}
}
