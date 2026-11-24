package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/attention"
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// Blocking probe (NeonH §3.5, Carbon §3.6): startInvestigation must refuse
// remote panes BEFORE planInvestigation so Source:"worker" does not run
// workerFork / WorkerLog.Resolve / pane.process_info against local files and
// does not return a misleading "not_reported; investigate without source" error.
func TestInvestigateRefusesRemoteBeforePlanInvestigation(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "secret.txt"), []byte("local-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := &fakeInvestigator{answer: "should never run"}
	logDir := t.TempDir()
	var workerLogProbed bool
	h := newInvestigationHarness(t, f, func(c *Config) {
		c.WorkerLog = workerlog.New(workerlog.Options{
			ClaudeProjects: filepath.Join(logDir, "projects"),
			CodexSessions:  filepath.Join(logDir, "sessions"),
			TreePIDs: func(roots workerlog.ProcessRoots) []int {
				workerLogProbed = true
				return roots.Direct()
			},
			OpenFiles: func([]int) []string {
				workerLogProbed = true
				return nil
			},
		})
	})

	h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "s-remote", Label: "remote"})
	h.srv.SetAgent(herdr.AgentInfo{PaneID: "box~t4", WorkspaceID: "s-remote", TabID: "t1", Agent: "claude", Status: herdr.StateIdle, Cwd: localDir})
	h.sup.HandleTransition(context.Background(), herdr.Transition{
		PaneID: "box~t4", To: herdr.StateIdle, At: h.clock.Now(),
		Agent: "claude", WorkspaceID: "s-remote", WorkspaceLabel: "remote", Cwd: localDir,
	})

	// Default (cli) source: refused with "another machine" and 0 investigator runs.
	if _, err := h.sup.startInvestigation(context.Background(), "box~t4", brain.InvestigationRequest{
		Question: "what is in secret.txt?",
		Turn:     1,
	}); err == nil || !strings.Contains(err.Error(), "another machine") {
		t.Errorf("default source err = %v, want remote refusal ('another machine')", err)
	}

	// Worker source: must refuse BEFORE planInvestigation -> workerFork.
	_, err := h.sup.startInvestigation(context.Background(), "box~t4", brain.InvestigationRequest{
		Question: "why did you fail?",
		Source:   brain.InvestigationSourceWorker,
		Turn:     1,
	})
	if err == nil || !strings.Contains(err.Error(), "another machine") {
		t.Fatalf("worker source err = %v, want remote refusal ('another machine') before planInvestigation", err)
	}
	if strings.Contains(err.Error(), "investigate without source") {
		t.Errorf("worker source returned misleading workerFork error before remote check: %v", err)
	}
	if workerLogProbed {
		t.Error("startInvestigation touched local WorkerLog for a remote pane")
	}
	if len(f.all()) != 0 {
		t.Fatalf("investigator ran %d queries on remote pane's local cwd", len(f.all()))
	}
}

// Blocking probe (NeonH §3.3, Carbon Probe 6): brainHost.RunCLI must refuse a
// remote pane and never invoke the local CLI Querier in the remote pane's cwd.
func TestBrainHostRunCLIRefusesRemotePane(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "secret.txt"), []byte("local-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t)
	fq := &fakeQuerier{}
	h.sup.querier = fq

	h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "s-remote", Label: "remote"})
	h.srv.SetAgent(herdr.AgentInfo{PaneID: "box~t4", WorkspaceID: "s-remote", TabID: "t1", Agent: "codex", Status: herdr.StateIdle, Cwd: localDir})
	h.sup.HandleTransition(context.Background(), herdr.Transition{
		PaneID: "box~t4", To: herdr.StateIdle, At: h.clock.Now(),
		Agent: "codex", WorkspaceID: "s-remote", WorkspaceLabel: "remote", Cwd: localDir,
	})

	out, err := (brainHost{h.sup}).RunCLI(context.Background(), "box~t4", "read secret.txt")
	if err == nil {
		t.Fatalf("RunCLI(box~t4) succeeded with %q, want refusal for remote pane", out)
	}
	if len(fq.calls) != 0 || len(fq.dirs) != 0 {
		t.Fatalf("RunCLI invoked local Querier on remote pane cwd: dirs=%v calls=%v", fq.dirs, fq.calls)
	}
}

// Blocking probe (NeonH §3.1/§3.6, Carbon §3.2/§3.6): spaceFolder, spaceOf,
// Supervisor.SpaceFolder and Supervisor.Spaces must skip local RealDir,
// herdr.GitInfo and fsaccess probes for a remote space whose panes' cwd matches
// real local directories.
func TestSpaceOfAndSpaceFolderSkipLocalProbesForRemoteSpace(t *testing.T) {
	base := t.TempDir()
	localRepo := filepath.Join(base, "secret-local-repo")
	if err := os.MkdirAll(filepath.Join(localRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localRepo, ".git", "HEAD"), []byte("ref: refs/heads/secret-branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localRepo, "secret.txt"), []byte("local-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Unit probes on spaceFolder and spaceOf with a recording readable callback.
	wRemote := herdr.WorkspaceInfo{WorkspaceID: "s-remote", Label: "remote"}
	remotePane := herdr.PaneInfo{PaneID: "box~t4", WorkspaceID: "s-remote", Cwd: localRepo, Agent: "claude"}
	snap := herdr.Snapshot{
		Workspaces: []herdr.WorkspaceInfo{wRemote},
		Panes:      []herdr.PaneInfo{remotePane},
	}

	var readableCalls []string
	spyReadable := func(p string) bool {
		readableCalls = append(readableCalls, p)
		return true
	}

	path, unreadable, _ := spaceFolder(wRemote, []herdr.PaneInfo{remotePane}, spyReadable)
	if path != "" || unreadable {
		t.Errorf("spaceFolder(remote) = (%q, unreadable=%v), want (\"\", false)", path, unreadable)
	}
	if len(readableCalls) != 0 {
		t.Errorf("spaceFolder probed local fsaccess on remote cwd: %v", readableCalls)
	}

	readableCalls = nil
	sp := spaceOf(snap, wRemote, map[string]state.Agent{}, spyReadable)
	if sp.Path != "" || sp.Git || sp.Repo != "" || sp.Branch != "" {
		t.Errorf("spaceOf(remote) probed local repo/dir: %+v", sp)
	}
	if len(readableCalls) != 0 {
		t.Errorf("spaceOf probed local fsaccess on remote cwd: %v", readableCalls)
	}

	// End-to-end Supervisor.SpaceFolder and Supervisor.Spaces (including an
	// unreadable local dir that must NOT surface in NeedsAccess).
	w := newSpawnWorld(t)
	w.srv.SetWorkspace(wRemote)
	w.srv.SetPane(remotePane)

	if dir, err := w.sup.SpaceFolder(context.Background(), "s-remote"); err != nil || dir != "" {
		t.Errorf("Supervisor.SpaceFolder(s-remote) = (%q, %v), want (\"\", nil)", dir, err)
	}

	var deniedDir string
	if os.Geteuid() != 0 {
		deniedDir = filepath.Join(base, "denied-local")
		if err := os.MkdirAll(deniedDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(deniedDir, "private.txt"), []byte("private\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(deniedDir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(deniedDir, 0o755) })
		w.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "s-denied", Label: "remote-denied"})
		w.srv.SetPane(herdr.PaneInfo{PaneID: "box~t5", WorkspaceID: "s-denied", Cwd: deniedDir, Agent: "claude"})
	}

	view, err := w.sup.Spaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range view.Spaces {
		if s.ID == "s-remote" && (s.Path != "" || s.Git || s.Branch == "secret-branch" || s.Repo == "secret-local-repo") {
			t.Errorf("Spaces() returned local path/git for remote space: %+v", s)
		}
	}
	if deniedDir != "" {
		for _, blocked := range view.NeedsAccess {
			if blocked.Path == deniedDir {
				t.Errorf("Spaces() ran local fsaccess probe on remote cwd and reported NeedsAccess: %+v", view.NeedsAccess)
			}
		}
	}
}

// Blocking probe (NeonH §3.6, Carbon §3.6): readFolders and space mutes in
// internal/supervisor/attention.go must not resolve a remote pane's Cwd against
// local directories or cross-mute local and remote spaces sharing a path string.
func TestAttentionReadFoldersAndMuteIgnoreRemoteCwd(t *testing.T) {
	sharedDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sharedDir, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realShared := herdr.RealDir(sharedDir)

	var h *harness
	mutes, err := attention.OpenMutes("", attention.MutesOptions{Now: func() time.Time { return h.clock.Now() }})
	if err != nil {
		t.Fatal(err)
	}
	h = newHarness(t, func(c *Config) { c.Mutes = mutes })
	ctx := context.Background()

	// Local space and remote space happen to report the exact same cwd string.
	h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w-local", Label: "local"})
	h.srv.SetPane(herdr.PaneInfo{PaneID: "w1:p1", WorkspaceID: "w-local", Cwd: sharedDir, Agent: "claude"})
	h.srv.SetAgent(herdr.AgentInfo{PaneID: "w1:p1", WorkspaceID: "w-local", Agent: "claude", Status: herdr.StateIdle, Cwd: sharedDir})
	h.sup.HandleTransition(ctx, herdr.Transition{PaneID: "w1:p1", To: herdr.StateIdle, At: h.clock.Now(), Agent: "claude", WorkspaceID: "w-local", WorkspaceLabel: "local", Cwd: sharedDir})

	h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "s-remote", Label: "remote"})
	h.srv.SetPane(herdr.PaneInfo{PaneID: "box~t4", WorkspaceID: "s-remote", Cwd: sharedDir, Agent: "claude"})
	h.srv.SetAgent(herdr.AgentInfo{PaneID: "box~t4", WorkspaceID: "s-remote", Agent: "claude", Status: herdr.StateIdle, Cwd: sharedDir})
	h.sup.HandleTransition(ctx, herdr.Transition{PaneID: "box~t4", To: herdr.StateIdle, At: h.clock.Now(), Agent: "claude", WorkspaceID: "s-remote", WorkspaceLabel: "remote", Cwd: sharedDir})

	idx, err := h.sup.readFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if idx.ws["w-local"] != realShared {
		t.Fatalf("idx.ws[w-local] = %q, want %q", idx.ws["w-local"], realShared)
	}
	if idx.ws["s-remote"] != "" {
		t.Errorf("readFolders indexed remote space cwd on local fs: idx.ws[s-remote] = %q", idx.ws["s-remote"])
	}

	// Muting the local space's folder must not mute the remote pane.
	if _, err := mutes.Set(realShared, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	if !h.sup.paneMuted(ctx, "w1:p1") {
		t.Error("local pane w1:p1 was not muted")
	}
	if h.sup.paneMuted(ctx, "box~t4") {
		t.Error("muting local folder cross-muted remote pane box~t4 sharing the same path string")
	}

	// Muting the remote space must not mute the local folder either.
	if _, err := mutes.Clear(realShared); err != nil {
		t.Fatal(err)
	}
	_, _ = h.sup.SetSpaceMute(ctx, "s-remote", true, nil, ViaPhone)
	if mutes.Muted(realShared) || h.sup.paneMuted(ctx, "w1:p1") {
		t.Error("SetSpaceMute on remote space muted the local space at the same path")
	}
}

// Blocking probe (NeonH §3.4, Carbon §3.3): spawnDirsIn / resolveSpawnDir /
// spaceDir must ignore remote panes so a remote pane's Cwd cannot bypass
// supervisor.project_roots or serve as a local spawn target.
func TestSpawnAndSpaceDirIgnoreRemotePaneCwd(t *testing.T) {
	w := newSpawnWorld(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside-project-roots\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	w.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "s-remote", Label: "remote"})
	w.srv.SetPane(herdr.PaneInfo{PaneID: "box~t4", WorkspaceID: "s-remote", Cwd: outside, Agent: "claude"})

	ctx := context.Background()
	snap, err := w.client.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	dirs := w.sup.spawnDirs(snap)
	if _, _, err := resolveSpawnDir(outside, dirs, []string{w.root}); !errors.Is(err, brain.ErrNotPermitted) {
		t.Errorf("resolveSpawnDir(%q) err = %v, want ErrNotPermitted (remote pane bypassed project_roots)", outside, err)
	}
	if d, err := spaceDir(snap, "s-remote"); err == nil {
		t.Errorf("spaceDir(s-remote) = %+v, nil; want refusal for remote space", d)
	}
	if _, err := w.sup.UserNewAgent(ctx, NewAgentRequest{Kind: "codex", Dir: outside}); err == nil {
		t.Error("UserNewAgent started a local agent outside project_roots via remote pane cwd")
	}
	if _, _, err := w.sup.NewSpace(ctx, outside, "", ""); err == nil {
		t.Error("NewSpace opened a local space outside project_roots via remote pane cwd")
	}
	if len(w.params("workspace.create")) != 0 || len(w.params("pane.split")) != 0 {
		t.Errorf("remote cwd caused local spawn RPCs: create=%v split=%v", w.params("workspace.create"), w.params("pane.split"))
	}
}

// Blocking probe (Carbon §3.6): protectedWhy (internal/supervisor/close.go)
// must not compare a remote pane's Cwd against local ProtectedDirs.
func TestProtectedWhyIgnoresRemoteCwd(t *testing.T) {
	protDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(protDir, "secret.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(c *Config) { c.ProtectedDirs = []string{protDir} })

	if why := h.sup.protectedWhy(state.Agent{PaneID: "box~t4", Cwd: protDir}); why != "" {
		t.Fatalf("protectedWhy(remote box~t4) = %q, want empty (must not match remote cwd against local ProtectedDirs)", why)
	}
}

// Round-2 blocking probe (NeonH §2.1): spaceDir must skip remote panes when a
// space also contains a local pane, instead of picking "box~t4" < "t1" and
// rejecting the whole space.
func TestSpaceDirSkipsRemotePaneInMixedSpace(t *testing.T) {
	localDir := t.TempDir()
	snap := herdr.Snapshot{
		Workspaces: []herdr.WorkspaceInfo{{WorkspaceID: "s1", Label: "app"}},
		Panes: []herdr.PaneInfo{
			{PaneID: "box~t4", WorkspaceID: "s1", Cwd: "/remote/app", Agent: "claude"},
			{PaneID: "t1", WorkspaceID: "s1", Cwd: localDir, Agent: "claude"},
		},
	}
	got, err := spaceDir(snap, "s1")
	if err != nil {
		t.Fatalf("spaceDir(s1) error = %v, want local pane t1 in %s", err, localDir)
	}
	if got.Pane != "t1" {
		t.Fatalf("spaceDir(s1).Pane = %q, want t1", got.Pane)
	}
}

// Round-2 blocking probe (Carbon Bug 2, NeonH §2): workerSessionWithRoots and
// brainHost.ReadSession must never pass remote Session* fields into local
// WorkerLog or read a local transcript file for a remote pane.
func TestReadSessionAndJournalNeverReadLocalTranscriptForRemotePane(t *testing.T) {
	logDir := t.TempDir()
	localTranscript := filepath.Join(logDir, "projects", "-local-secret", "11111111-1111-1111-1111-111111111111.jsonl")
	if err := os.MkdirAll(filepath.Dir(localTranscript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localTranscript, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"LOCAL SECRET TRANSCRIPT"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, func(c *Config) {
		c.WorkerLog = workerlog.New(workerlog.Options{
			ClaudeProjects: filepath.Join(logDir, "projects"),
			CodexSessions:  filepath.Join(logDir, "sessions"),
		})
	})
	ctx := context.Background()
	h.sup.HandleTransition(ctx, herdr.Transition{
		PaneID: "box~t4", To: herdr.StateDone, At: h.clock.Now(),
		Agent: "claude",
		Session: &herdr.AgentSession{
			Source: "herdr:claude", Kind: herdr.SessionKindPath, Value: localTranscript,
		},
	})

	sess := workerSessionWithRoots(state.Agent{
		PaneID:        "box~t4",
		Kind:          "claude",
		SessionSource: "herdr:claude",
		SessionKind:   herdr.SessionKindPath,
		SessionValue:  localTranscript,
		Cwd:           logDir,
	}, workerlog.ProcessRoots{})
	if sess.Source != "" || sess.Kind != "" || sess.Value != "" || sess.Cwd != "" {
		t.Errorf("workerSessionWithRoots(remote box~t4) = %+v, want empty session fields", sess)
	}

	sr, err := (brainHost{h.sup}).ReadSession(ctx, "box~t4")
	if err == nil || !strings.Contains(err.Error(), "another machine") {
		t.Fatalf("ReadSession(box~t4) = (%+v, %v), want remote refusal ('another machine')", sr, err)
	}
}

// Round-2 blocking probe (Carbon Bug 3): Supervisor.Spaces (GET /api/spaces)
// must omit remote-only workspaces whose panes all run on another machine.
func TestSpacesOmitsRemoteOnlyWorkspace(t *testing.T) {
	w := newSpawnWorld(t)
	w.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "s-remote", Label: "remote"})
	w.srv.SetPane(herdr.PaneInfo{PaneID: "box~t4", WorkspaceID: "s-remote", Cwd: "/remote/app", Agent: "claude"})

	view, err := w.sup.Spaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Spaces) != 0 {
		t.Fatalf("Spaces() listed remote-only space: %+v", view.Spaces)
	}
}

// Round-2 probe (Carbon §3): spaceFolder must not clobber an unreadable local
// worktree CheckoutPath when iterating remote panes in the same workspace.
func TestSpaceFolderPreservesUnreadableWorktreeWhenOnlyRemotePanes(t *testing.T) {
	wtPath := "/unreadable/worktree"
	w := herdr.WorkspaceInfo{
		WorkspaceID: "s-wt",
		Label:       "wt",
		Worktree:    &herdr.WorkspaceWorktree{CheckoutPath: wtPath, Linked: true},
	}
	panes := []herdr.PaneInfo{
		{PaneID: "box~t4", WorkspaceID: "s-wt", Cwd: "/remote/app", Agent: "claude"},
	}

	path, unreadable, linked := spaceFolder(w, panes, func(string) bool { return false })
	if path != wtPath || !unreadable || !linked {
		t.Fatalf("spaceFolder = (%q, unreadable=%v, linked=%v), want (%q, true, true)", path, unreadable, linked, wtPath)
	}
}
