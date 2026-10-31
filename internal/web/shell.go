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
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/termproto"
)

// The phone shell (docs/dev/term-contract.md "The plain shell"): a plain
// shell in a project folder, opened over /term as a herdr tab, one per
// folder and reused. The old herdr puts it in the "phone shell" workspace;
// herdr-core joins the folder's existing space. One switch per computer
// gates it, web.shell (on by default, set on the setup page), for every
// paired device on every network.

// ShellWorkspace is the old herdr workspace label phone shells open in.
const ShellWorkspace = herdr.PhoneShellWorkspace

// ShellHost is what the phone shell uses from a backend (tests pass a fake).
// A backend supplies its own layout: the old herdr has one phone-shell
// workspace, while herdr-core joins the existing space for the folder.
type ShellHost interface {
	Snapshot(ctx context.Context) (herdr.Snapshot, error)
	ShellWorkspace(snap herdr.Snapshot, cwd string) string
	IsShellPane(p herdr.PaneInfo, workspaceID string) bool
	OpenShell(ctx context.Context, workspaceID, cwd string) (herdr.PaneInfo, error)
	CloseShell(ctx context.Context, paneID string) error
}

var _ ShellHost = (*herdr.Client)(nil)

// ShellBook is the daemon's live phone-shell registry. A shell is keyed by
// pane and folder; Open reaps closed panes, adopts shells left by
// a daemon restart, and opens one only when the folder has none.
type ShellBook struct {
	mu    sync.Mutex
	panes map[string]string
}

// NewShellBook returns an empty phone-shell registry.
func NewShellBook() *ShellBook { return &ShellBook{panes: map[string]string{}} }

// errOutsideProjects: the shell's folder is not inside supervisor.project_roots
// nor a tracked agent's folder.
var errOutsideProjects = errors.New("that folder is outside the project directories")

// shellAllowed is the gate: the live terminal and shell host work, and the
// computer's shell switch is on.
func (t *termServer) shellAllowed() bool {
	if t == nil || t.deps.Shells == nil {
		return false
	}
	return t.s.shellOn.Load() && t.available()
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
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	return herdr.RealDir(path)
}

// inside reports whether path is root or below it.
func inside(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// shellPane returns the phone shell for dir, opening one when there is
// none. The backend chooses the old herdr's shared workspace or herdr-core's
// folder space. Opens are serialised, so two quick taps make one shell.
func (t *termServer) shellPane(ctx context.Context, dir string) (pane string, reused bool, err error) {
	p, reused, err := t.shells.Open(ctx, t.deps.Shells, dir)
	return p.PaneID, reused, err
}

// Open returns the folder's phone shell, opening one when needed. Opens are
// serialised, so two quick taps make one shell.
func (b *ShellBook) Open(ctx context.Context, h ShellHost, dir string) (herdr.PaneInfo, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return herdr.PaneInfo{}, false, err
	}
	ws := h.ShellWorkspace(snap, dir)
	alive := map[string]herdr.PaneInfo{}
	for _, p := range snap.Panes {
		if h.IsShellPane(p, ws) {
			alive[p.PaneID] = p
		}
	}
	// Forget shells herdr no longer has; adopt ones a restart forgot.
	for p := range b.panes {
		if _, ok := alive[p]; !ok {
			delete(b.panes, p)
		}
	}
	for id, p := range alive {
		if _, ok := b.panes[id]; !ok && p.Cwd != "" {
			b.panes[id] = realDir(p.Cwd)
		}
	}
	for id, d := range b.panes {
		if d == dir {
			return alive[id], true, nil
		}
	}
	p, err := h.OpenShell(ctx, ws, dir)
	if err == nil && p.PaneID == "" {
		err = errors.New("herdr returned no pane")
	}
	if err != nil {
		return herdr.PaneInfo{}, false, err
	}
	b.panes[p.PaneID] = dir
	return p, false, nil
}

// Has reports whether pane is a live phone shell known to the registry.
func (b *ShellBook) Has(pane string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.panes[pane]
	return ok
}

// Forget drops pane from the registry.
func (b *ShellBook) Forget(pane string) {
	b.mu.Lock()
	delete(b.panes, pane)
	b.mu.Unlock()
}

// Close ends a phone shell and forgets it. CloseShell may keep an old
// backend shell that already vanished as ErrPaneNotFound.
func (b *ShellBook) Close(ctx context.Context, h ShellHost, pane string) error {
	err := h.CloseShell(ctx, pane)
	if err == nil || errors.Is(err, herdr.ErrPaneNotFound) {
		b.Forget(pane)
	}
	return err
}

// isShell reports whether pane is a phone shell the daemon opened.
func (t *termServer) isShell(pane string) bool {
	if t == nil {
		return false
	}
	return t.shells.Has(pane)
}

func (t *termServer) forgetShell(pane string) {
	t.shells.Forget(pane)
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
	if !t.shellAllowed() {
		writeError(w, http.StatusForbidden, "the shell is off on this computer (the setup page switches it)")
		return
	}
	if !t.isShell(pane) {
		writeError(w, http.StatusNotFound, "not a phone shell")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	err := t.shells.Close(ctx, t.deps.Shells, pane)
	if err != nil && !errors.Is(err, herdr.ErrPaneNotFound) {
		s.log.Log(logSource, "web.shell.close", pane, map[string]any{"pane_id": pane, "remote_kind": kind, "error": err.Error()})
		writeError(w, http.StatusBadGateway, "could not close the shell: "+err.Error())
		return
	}
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

// handleSetupShell is POST /api/setup/shell {"enable"} (loopback only):
// the setup page's "Shell from your phone" switch (web.shell). It is
// written to the env file and applies at once, with no restart.
func (s *Server) handleSetupShell(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enable bool `json:"enable"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if err := onboard.UpsertEnvFile(s.deps.Setup.EnvPath, map[string]string{"SHEEPDOG_WEB_SHELL": strconv.FormatBool(req.Enable)}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.shellOn.Store(req.Enable)
	s.log.Log(logSource, "web.setup_saved", "", map[string]any{"step": "shell", "enable": req.Enable})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "shell": req.Enable})
}
