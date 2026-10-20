package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/pair"
	"github.com/kccarlos/herdr-sheepdog/internal/termproto"
)

// The phone shell (docs/dev/term-contract.md "The plain shell"): a plain
// shell in a project folder, opened over /term as a herdr tab in the
// "phone shell" workspace, one per folder and reused. It is gated by the
// address the request came from: loopback and Tailscale get it; a plain
// LAN client only when web.shell_lan is on (set on the setup page),
// because plain-LAN HTTP carries every keystroke in clear text.

// ShellWorkspace is the label of the herdr workspace phone shells open in.
const ShellWorkspace = "phone shell"

// ShellHost is the part of *herdr.Client the phone shell uses (tests pass
// a fake).
type ShellHost interface {
	Snapshot(ctx context.Context) (herdr.Snapshot, error)
	CreateWorkspace(ctx context.Context, cwd, label string) (herdr.PaneInfo, error)
	CreateTab(ctx context.Context, workspaceID, cwd, label string) (herdr.PaneInfo, error)
	ClosePane(ctx context.Context, paneID string) error
}

var _ ShellHost = (*herdr.Client)(nil)

// errOutsideProjects: the shell's folder is not inside supervisor.project_roots
// nor a tracked agent's folder.
var errOutsideProjects = errors.New("that folder is outside the project directories")

// shellAllowed is the gate for a client of the given remote kind.
func (t *termServer) shellAllowed(kind string) bool {
	if t == nil || t.deps.Shells == nil {
		return false
	}
	switch kind {
	case "loopback", pair.KindTailscale:
		return true
	}
	return t.s.shellLAN.Load()
}

// shellDir resolves the folder a shell asks for: empty is the first
// project root; otherwise the real path must lie inside a project root or
// be a tracked agent's own folder (a worktree herdr put elsewhere).
func (t *termServer) shellDir(want string) (string, error) {
	roots := t.deps.ProjectRoots
	want = strings.TrimSpace(want)
	if want == "" {
		if len(roots) == 0 {
			return "", errOutsideProjects
		}
		want = roots[0]
	}
	path := realDir(want)
	if path == "" {
		return "", fmt.Errorf("%s is not a folder", want)
	}
	for _, r := range roots {
		if rr := realDir(r); rr != "" && inside(path, rr) {
			return path, nil
		}
	}
	for _, a := range t.s.deps.Store.List() {
		if a.Cwd != "" && realDir(a.Cwd) == path {
			return path, nil
		}
	}
	return "", errOutsideProjects
}

// realDir expands ~ and resolves symlinks; "" when path is not a folder.
func realDir(path string) string {
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	if !filepath.IsAbs(path) {
		return ""
	}
	p, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return ""
	}
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		return ""
	}
	return p
}

// inside reports whether path is root or below it.
func inside(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// shellPane returns the phone shell for dir, opening one when there is
// none: a tab in the "phone shell" workspace (the workspace itself the
// first time). Opens are serialised, so two quick taps make one shell.
func (t *termServer) shellPane(ctx context.Context, dir string) (pane string, reused bool, err error) {
	t.shellMu.Lock()
	defer t.shellMu.Unlock()
	h := t.deps.Shells
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return "", false, err
	}
	ws := ""
	for _, w := range snap.Workspaces {
		if w.Label == ShellWorkspace {
			ws = w.WorkspaceID
			break
		}
	}
	alive := map[string]herdr.PaneInfo{}
	for _, p := range snap.Panes {
		if ws != "" && p.WorkspaceID == ws {
			alive[p.PaneID] = p
		}
	}
	t.mu.Lock()
	// Forget shells herdr no longer has; adopt ones a restart forgot.
	for p := range t.shells {
		if _, ok := alive[p]; !ok {
			delete(t.shells, p)
		}
	}
	for id, p := range alive {
		if _, ok := t.shells[id]; !ok && p.Cwd != "" {
			t.shells[id] = realDir(p.Cwd)
		}
	}
	for id, d := range t.shells {
		if d == dir {
			t.mu.Unlock()
			return id, true, nil
		}
	}
	t.mu.Unlock()
	var p herdr.PaneInfo
	if ws == "" {
		p, err = h.CreateWorkspace(ctx, dir, ShellWorkspace)
	} else {
		p, err = h.CreateTab(ctx, ws, dir, filepath.Base(dir))
	}
	if err == nil && p.PaneID == "" {
		err = errors.New("herdr returned no pane")
	}
	if err != nil {
		return "", false, err
	}
	t.mu.Lock()
	t.shells[p.PaneID] = dir
	t.mu.Unlock()
	return p.PaneID, false, nil
}

// isShell reports whether pane is a phone shell the daemon opened.
func (t *termServer) isShell(pane string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.shells[pane]
	return ok
}

func (t *termServer) forgetShell(pane string) {
	t.mu.Lock()
	delete(t.shells, pane)
	t.mu.Unlock()
}

// openShell is term-open {target:{shell}} after the gate: find or open the
// folder's shell, then open the channel on it like an agent's window.
func (c *termConn) openShell(m termproto.Message) {
	dir, err := c.t.shellDir(m.Target.Shell.Cwd)
	if err != nil {
		c.refuse(m.Ch, "", termproto.RefusedOutsideProjects)
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	pane, reused, err := c.t.shellPane(ctx, dir)
	if err != nil {
		c.t.s.log.Log(logSource, "web.shell.error", "", map[string]any{"client_id": c.id, "cwd": dir, "error": err.Error()})
		c.refuse(m.Ch, "", termproto.ReasonHerdr)
		return
	}
	c.t.s.log.Log(logSource, "web.shell.open", pane, map[string]any{"client_id": c.id, "pane_id": pane, "cwd": dir, "remote_kind": c.kind, "reused": reused})
	c.openChan(m, pane, "shell")
}

// handleShellClose is POST /api/shells/{pane_id}/close: end a phone
// shell (the app confirmed it). Only a phone shell, behind the same gate.
func (s *Server) handleShellClose(w http.ResponseWriter, r *http.Request) {
	t := s.term
	pane := r.PathValue("pane_id")
	kind := s.kindOf(r.RemoteAddr)
	if !t.shellAllowed(kind) {
		writeError(w, http.StatusForbidden, "the shell is off on this network")
		return
	}
	if !t.isShell(pane) {
		writeError(w, http.StatusNotFound, "not a phone shell")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	err := t.deps.Shells.ClosePane(ctx, pane)
	if err != nil && !errors.Is(err, herdr.ErrPaneNotFound) {
		s.log.Log(logSource, "web.shell.close", pane, map[string]any{"pane_id": pane, "remote_kind": kind, "error": err.Error()})
		writeError(w, http.StatusBadGateway, "could not close the shell: "+err.Error())
		return
	}
	t.forgetShell(pane)
	s.log.Log(logSource, "web.shell.close", pane, map[string]any{"pane_id": pane, "remote_kind": kind})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// kindOf classifies a request's peer (tests replace it to stand in for a
// LAN or Tailscale client).
func (s *Server) kindOf(remote string) string {
	if s.kindHook != nil {
		return s.kindHook(remote)
	}
	return remoteKind(remote)
}

// handleSetupShellLAN is POST /api/setup/shell-lan {"enable"} (loopback
// only): the setup page's "Shell on this network" switch
// (web.shell_lan). It is written to the env file and applies at once,
// with no restart.
func (s *Server) handleSetupShellLAN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enable bool `json:"enable"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if err := onboard.UpsertEnvFile(s.deps.Setup.EnvPath, map[string]string{"SHEEPDOG_WEB_SHELL_LAN": strconv.FormatBool(req.Enable)}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.shellLAN.Store(req.Enable)
	s.log.Log(logSource, "web.setup_saved", "", map[string]any{"step": "shell_lan", "enable": req.Enable})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "shell_lan": req.Enable})
}
