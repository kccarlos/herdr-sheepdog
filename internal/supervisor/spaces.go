package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// Spaces (docs/dev/ws-contract.md "Spaces"): herdr's workspaces as the
// app shows them, each a folder with its agents, plus what the user does
// with them by tap: rename, close, its git worktrees, and saved wrap-up
// commands. Like the other agent actions a tap is the user's own; the
// rails that stay in code are about damage, not who asked:
//
//   - a space is closed only while none of its agents is working or
//     blocked, and never the one sheepdog itself runs in;
//   - only a linked worktree is removed (never a repository's main
//     checkout), not while an agent there is working or blocked, and not
//     with uncommitted changes unless the user forces it;
//   - running a wrap-up command is shell-equivalent: the web layer gates
//     it like the phone shell.

// ErrNoSuchSpace is a space id herdr does not have (a 404).
var ErrNoSuchSpace = errors.New("no such space")

// ErrSpaceBusy is a close or removal refused while an agent in the space
// is working or blocked (a 409).
var ErrSpaceBusy = errors.New("an agent is busy there")

// SpaceHost is a Herdr that manages workspaces and their worktrees.
type SpaceHost interface {
	RenameWorkspace(ctx context.Context, workspaceID, label string) error
	CloseWorkspace(ctx context.Context, workspaceID string) error
	ListWorktrees(ctx context.Context, workspaceID string) (herdr.WorktreeList, error)
	OpenWorktree(ctx context.Context, workspaceID, path, branch string) (herdr.PaneInfo, error)
	RemoveWorktree(ctx context.Context, workspaceID string, force bool) error
	CreateTab(ctx context.Context, workspaceID, cwd, label string) (herdr.PaneInfo, error)
	SendInput(ctx context.Context, paneID, text string, keys ...string) error
}

var _ SpaceHost = (*herdr.Client)(nil)

// Space is one herdr workspace (GET /api/spaces).
type Space struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Path is its folder: the checkout of a worktree workspace, else the
	// folder of its first pane.
	Path string `json:"path"`
	// Git is true when Path is inside a git repository; Repo is the
	// repository's folder name and Branch its checked-out branch (a short
	// commit when detached).
	Git    bool   `json:"git"`
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Worktree is true for a linked worktree (not the main checkout).
	Worktree bool         `json:"worktree"`
	Agents   []SpaceAgent `json:"agents"`
	// Shells are its panes with no agent.
	Shells   []string `json:"shells"`
	Commands []WrapUp `json:"commands"`
}

// SpaceAgent is one agent of a space.
type SpaceAgent struct {
	PaneID string      `json:"pane_id"`
	Alias  string      `json:"alias"`
	Name   string      `json:"name"`
	Kind   string      `json:"kind"`
	State  herdr.State `json:"state"`
	Title  string      `json:"title,omitempty"`
}

// Folder is a folder in a project root that has no space yet.
type Folder struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Git  bool   `json:"git"`
}

// SpacesView is GET /api/spaces.
type SpacesView struct {
	Spaces  []Space  `json:"spaces"`
	Folders []Folder `json:"folders"`
	// Kinds are the agent kinds a new agent may be (InstalledKinds).
	Kinds []string `json:"kinds"`
	// Worktrees is true when this herdr can make a fresh worktree for a
	// new agent (a space's Git says whether its folder can have one).
	Worktrees bool `json:"worktrees"`
}

// Spaces lists the spaces, the free project folders and the kinds.
func (s *Supervisor) Spaces(ctx context.Context) (SpacesView, error) {
	sp, ok := s.herdr.(Spawner)
	if !ok {
		return SpacesView{}, errors.New("spaces are not available here")
	}
	snap, err := sp.Snapshot(ctx)
	if err != nil {
		return SpacesView{}, fmt.Errorf("could not list herdr's workspaces: %w", err)
	}
	kinds, err := s.InstalledKinds(ctx)
	if err != nil {
		return SpacesView{}, err
	}
	_, wt := s.herdr.(WorktreeCreator)
	out := SpacesView{Spaces: []Space{}, Folders: []Folder{}, Kinds: kinds, Worktrees: wt}
	cmds := s.loadWrapUps()
	rows := map[string]state.Agent{}
	for _, a := range s.store.List() {
		rows[a.PaneID] = a
	}
	taken := map[string]bool{}
	for _, w := range snap.Workspaces {
		sp := spaceOf(snap, w, rows)
		sp.Commands = append([]WrapUp{}, cmds[sp.Path]...)
		if sp.Path != "" {
			taken[sp.Path] = true
		}
		out.Spaces = append(out.Spaces, sp)
	}
	for _, d := range s.spawnDirs(snap) {
		if d.Pane != "" || taken[d.Path] {
			continue
		}
		_, git := gitInfo(d.Path)
		out.Folders = append(out.Folders, Folder{Name: d.Name, Path: d.Path, Git: git})
	}
	return out, nil
}

// spaceOf builds the view of workspace w from the snapshot and the
// roster rows.
func spaceOf(snap herdr.Snapshot, w herdr.WorkspaceInfo, rows map[string]state.Agent) Space {
	sp := Space{ID: w.WorkspaceID, Name: w.Label, Agents: []SpaceAgent{}, Shells: []string{}}
	var panes []herdr.PaneInfo
	for _, p := range snap.Panes {
		if p.WorkspaceID == w.WorkspaceID {
			panes = append(panes, p)
		}
	}
	sort.Slice(panes, func(i, j int) bool { return paneLess(panes[i].PaneID, panes[j].PaneID) })
	if w.Worktree != nil && w.Worktree.CheckoutPath != "" {
		sp.Path = realDir(w.Worktree.CheckoutPath)
		sp.Worktree = w.Worktree.Linked
	}
	if sp.Path == "" && len(panes) > 0 {
		sp.Path = realDir(panes[0].Cwd)
	}
	if sp.Path != "" {
		g, ok := gitInfo(sp.Path)
		sp.Git, sp.Repo, sp.Branch = ok, g.repo, g.branch
		if w.Worktree != nil && w.Worktree.RepoName != "" {
			sp.Repo = w.Worktree.RepoName
		}
	}
	for _, p := range panes {
		row, ok := rows[p.PaneID]
		if !ok || row.Kind == "" {
			if p.Agent == "" {
				sp.Shells = append(sp.Shells, p.PaneID)
			}
			continue
		}
		sp.Agents = append(sp.Agents, SpaceAgent{PaneID: p.PaneID, Alias: row.Alias, Name: state.Say(row), Kind: row.Kind, State: row.State, Title: row.Title})
	}
	return sp
}

// paneLess orders pane ids by workspace, then pane number (w2:p10 after
// w2:p9).
func paneLess(a, b string) bool {
	na, nb := paneNumber(a), paneNumber(b)
	if na != nb && na >= 0 && nb >= 0 && strings.SplitN(a, ":", 2)[0] == strings.SplitN(b, ":", 2)[0] {
		return na < nb
	}
	return a < b
}

func paneNumber(id string) int {
	_, after, ok := strings.Cut(id, ":p")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(after)
	if err != nil {
		return -1
	}
	return n
}

// gitCheckout is what gitInfo reads.
type gitCheckout struct {
	root, repo, branch string
}

// gitInfo finds the git checkout dir is in (walking up to a .git) and
// its branch from HEAD, without running git. ok is false outside one.
func gitInfo(dir string) (gitCheckout, bool) {
	for d := dir; d != "" && d != string(filepath.Separator) && d != "."; d = filepath.Dir(d) {
		dotgit := filepath.Join(d, ".git")
		fi, err := os.Stat(dotgit)
		if err != nil {
			continue
		}
		g := gitCheckout{root: d, repo: filepath.Base(d)}
		gitdir := dotgit
		if !fi.IsDir() {
			// A linked worktree: ".git" is a file "gitdir: <path>".
			b, err := os.ReadFile(dotgit)
			if err != nil {
				return g, true
			}
			p := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
			if !filepath.IsAbs(p) {
				p = filepath.Join(d, p)
			}
			gitdir = p
		}
		if head, err := os.ReadFile(filepath.Join(gitdir, "HEAD")); err == nil {
			h := strings.TrimSpace(string(head))
			if ref, ok := strings.CutPrefix(h, "ref: refs/heads/"); ok {
				g.branch = ref
			} else if len(h) >= 7 {
				g.branch = h[:7]
			}
		}
		return g, true
	}
	return gitCheckout{}, false
}

// space finds workspace id in a fresh snapshot.
func (s *Supervisor) space(ctx context.Context, id string) (Space, error) {
	sp, ok := s.herdr.(Spawner)
	if !ok {
		return Space{}, errors.New("spaces are not available here")
	}
	snap, err := sp.Snapshot(ctx)
	if err != nil {
		return Space{}, fmt.Errorf("could not list herdr's workspaces: %w", err)
	}
	rows := map[string]state.Agent{}
	for _, a := range s.store.List() {
		rows[a.PaneID] = a
	}
	for _, w := range snap.Workspaces {
		if w.WorkspaceID == id {
			return spaceOf(snap, w, rows), nil
		}
	}
	return Space{}, ErrNoSuchSpace
}

func (s *Supervisor) spaceHost() (SpaceHost, error) {
	h, ok := s.herdr.(SpaceHost)
	if !ok {
		return nil, errors.New("spaces are not available here")
	}
	return h, nil
}

// busyIn names an agent of sp that is working or blocked ("" when none).
func busyIn(sp Space) string {
	for _, a := range sp.Agents {
		if a.State == herdr.StateWorking || a.State == herdr.StateBlocked {
			return aliasOr(a.Name)
		}
	}
	return ""
}

// RenameSpace sets a space's name (herdr's workspace label).
func (s *Supervisor) RenameSpace(ctx context.Context, id, name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || utf8.RuneCountInString(name) > 60 {
		return "", fmt.Errorf("%w: a name is 1 to 60 characters", ErrUserAction)
	}
	h, err := s.spaceHost()
	if err != nil {
		return "", err
	}
	sp, err := s.space(ctx, id)
	if err != nil {
		return "", err
	}
	if err := h.RenameWorkspace(ctx, id, name); err != nil {
		return "", fmt.Errorf("could not rename %s: %w", sp.Name, err)
	}
	s.log.Log(logSource, "supervisor.space_rename", "", map[string]any{"space": id, "from": sp.Name, "to": name, "via": "user"})
	s.tellBrain(fmt.Sprintf("the user renamed the space %s to %s from the phone", sp.Name, name))
	return "Renamed " + sp.Name + " to " + name + ".", nil
}

// CloseSpace closes a space and every pane in it, refused while one of
// its agents is working or blocked, and for the space sheepdog runs in.
func (s *Supervisor) CloseSpace(ctx context.Context, id string) (string, error) {
	h, err := s.spaceHost()
	if err != nil {
		return "", err
	}
	sp, err := s.space(ctx, id)
	if err != nil {
		return "", err
	}
	refuse := func(reason string, err error) (string, error) {
		s.log.Log(logSource, "supervisor.space_close_refused", "", map[string]any{"space": id, "reason": reason, "via": "user"})
		return "", err
	}
	if who := busyIn(sp); who != "" {
		return refuse("busy", fmt.Errorf("%w: %s is still working or waiting for an answer in %s; stop it or answer it first", ErrSpaceBusy, who, sp.Name))
	}
	if s.ownPaneIn(sp) {
		return refuse("own pane", fmt.Errorf("%w: sheepdog itself runs in %s; close it on your computer", brain.ErrNotPermitted, sp.Name))
	}
	if err := h.CloseWorkspace(ctx, id); err != nil {
		s.log.Log(logSource, "supervisor.space_close_error", "", map[string]any{"space": id, "error": err.Error()})
		return "", fmt.Errorf("could not close %s: %w", sp.Name, err)
	}
	s.mu.Lock()
	for _, a := range sp.Agents {
		s.dropPendingFor(a.PaneID, "closed")
	}
	s.mu.Unlock()
	s.log.Log(logSource, "supervisor.space_close", "", map[string]any{"space": id, "name": sp.Name, "agents": len(sp.Agents), "via": "user"})
	s.tellBrain(fmt.Sprintf("the user closed the space %s from the phone", sp.Name))
	return "Closed " + sp.Name + ".", nil
}

// ownPaneIn reports whether the pane sheepdog runs in is in sp.
func (s *Supervisor) ownPaneIn(sp Space) bool {
	if s.cfg.OwnPane == "" {
		return false
	}
	for _, a := range sp.Agents {
		if a.PaneID == s.cfg.OwnPane {
			return true
		}
	}
	for _, p := range sp.Shells {
		if p == s.cfg.OwnPane {
			return true
		}
	}
	return strings.HasPrefix(s.cfg.OwnPane, sp.ID+":")
}

// SpaceWorktrees lists the checkouts of a space's repository.
func (s *Supervisor) SpaceWorktrees(ctx context.Context, id string) (herdr.WorktreeList, error) {
	h, err := s.spaceHost()
	if err != nil {
		return herdr.WorktreeList{}, err
	}
	sp, err := s.space(ctx, id)
	if err != nil {
		return herdr.WorktreeList{}, err
	}
	if !sp.Git {
		return herdr.WorktreeList{}, fmt.Errorf("%w: %s is not a git repository", ErrUserAction, sp.Name)
	}
	list, err := h.ListWorktrees(ctx, id)
	if err != nil {
		return herdr.WorktreeList{}, fmt.Errorf("could not list the worktrees of %s: %w", sp.Name, err)
	}
	if list.Worktrees == nil {
		list.Worktrees = []herdr.Worktree{}
	}
	return list, nil
}

// OpenWorktree opens a space for an existing checkout of space id's
// repository, by path or branch, and returns its root pane.
func (s *Supervisor) OpenWorktree(ctx context.Context, id, path, branch string) (string, error) {
	h, err := s.spaceHost()
	if err != nil {
		return "", err
	}
	if (path == "") == (branch == "") {
		return "", fmt.Errorf("%w: give the worktree's path or its branch", ErrUserAction)
	}
	if branch != "" && !validBranch(branch) {
		return "", fmt.Errorf("%w: %q is not a branch name", ErrUserAction, branch)
	}
	list, err := s.SpaceWorktrees(ctx, id)
	if err != nil {
		return "", err
	}
	found := false
	for _, w := range list.Worktrees {
		if (path != "" && w.Path == path) || (branch != "" && w.Branch == branch) {
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("%w: that worktree is not one of this repository's", ErrUserAction)
	}
	pane, err := h.OpenWorktree(ctx, id, path, branch)
	if err != nil {
		return "", fmt.Errorf("could not open the worktree: %w", err)
	}
	s.log.Log(logSource, "supervisor.worktree_open", pane.PaneID, map[string]any{"space": id, "path": path, "branch": branch, "via": "user"})
	return pane.PaneID, nil
}

// RemoveWorktree removes the linked worktree space id works in and
// closes the space. It refuses a repository's main checkout, a space
// with an agent working or blocked, and a checkout with uncommitted
// changes unless force is set.
func (s *Supervisor) RemoveWorktree(ctx context.Context, id string, force bool) (string, error) {
	h, err := s.spaceHost()
	if err != nil {
		return "", err
	}
	sp, err := s.space(ctx, id)
	if err != nil {
		return "", err
	}
	refuse := func(reason string, err error) (string, error) {
		s.log.Log(logSource, "supervisor.worktree_remove_refused", "", map[string]any{"space": id, "reason": reason, "force": force, "via": "user"})
		return "", err
	}
	if !sp.Worktree {
		return refuse("not a worktree", fmt.Errorf("%w: %s is not a linked worktree; only those are removed", ErrUserAction, sp.Name))
	}
	if who := busyIn(sp); who != "" {
		return refuse("busy", fmt.Errorf("%w: %s is still working or waiting for an answer in %s", ErrSpaceBusy, who, sp.Name))
	}
	if s.ownPaneIn(sp) {
		return refuse("own pane", fmt.Errorf("%w: sheepdog itself runs in %s", brain.ErrNotPermitted, sp.Name))
	}
	if !force {
		dirty, err := gitDirty(ctx, sp.Path)
		if err != nil {
			return refuse("status failed", fmt.Errorf("could not check %s for changes: %w", sp.Name, err))
		}
		if dirty != "" {
			return refuse("dirty", fmt.Errorf("%w: %s has uncommitted changes (%s); remove it anyway with force", ErrDirtyWorktree, sp.Name, dirty))
		}
	}
	if err := h.RemoveWorktree(ctx, id, force); err != nil {
		s.log.Log(logSource, "supervisor.worktree_remove_error", "", map[string]any{"space": id, "error": err.Error()})
		return "", fmt.Errorf("could not remove %s: %w", sp.Name, err)
	}
	s.mu.Lock()
	for _, a := range sp.Agents {
		s.dropPendingFor(a.PaneID, "closed")
	}
	s.mu.Unlock()
	s.log.Log(logSource, "supervisor.worktree_remove", "", map[string]any{"space": id, "path": sp.Path, "branch": sp.Branch, "force": force, "via": "user"})
	s.tellBrain(fmt.Sprintf("the user removed the worktree %s (branch %s) from the phone", sp.Name, sp.Branch))
	return "Removed the worktree " + sp.Name + ".", nil
}

// ErrDirtyWorktree is a removal refused for uncommitted changes (a 409).
var ErrDirtyWorktree = errors.New("uncommitted changes")

// gitDirty summarises a checkout's uncommitted changes ("" when clean).
func gitDirty(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return "", nil
	}
	if len(lines) == 1 {
		return "1 file", nil
	}
	return fmt.Sprintf("%d files", len(lines)), nil
}

// Wrap-up commands: saved per space folder in <data_dir>/wrapups.json.

// WrapUp is one saved command of a space ("Run tests", `npm test`).
type WrapUp struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
}

// wrapUpMax is how many commands a space keeps.
const wrapUpMax = 20

func (s *Supervisor) wrapUpPath() string {
	if s.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.DataDir, "wrapups.json")
}

// loadWrapUps reads the commands, keyed by space folder.
func (s *Supervisor) loadWrapUps() map[string][]WrapUp {
	s.wrapMu.Lock()
	defer s.wrapMu.Unlock()
	return s.readWrapUpsLocked()
}

func (s *Supervisor) readWrapUpsLocked() map[string][]WrapUp {
	out := map[string][]WrapUp{}
	p := s.wrapUpPath()
	if p == "" {
		for k, v := range s.wrapMem {
			out[k] = append([]WrapUp(nil), v...)
		}
		return out
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return out
	}
	if err := json.Unmarshal(b, &out); err != nil {
		s.log.Log(logSource, "supervisor.wrapup_error", "", map[string]any{"error": err.Error()})
		return map[string][]WrapUp{}
	}
	return out
}

func (s *Supervisor) writeWrapUpsLocked(all map[string][]WrapUp) error {
	p := s.wrapUpPath()
	if p == "" {
		s.wrapMem = all
		return nil
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// checkWrapUp validates a command's name and line.
func checkWrapUp(name, command string) (string, string, error) {
	name = strings.Join(strings.Fields(name), " ")
	command = strings.TrimSpace(command)
	switch {
	case name == "" || utf8.RuneCountInString(name) > 40:
		return "", "", fmt.Errorf("%w: a name is 1 to 40 characters", ErrUserAction)
	case command == "" || len(command) > 1000:
		return "", "", fmt.Errorf("%w: a command is 1 to 1000 bytes", ErrUserAction)
	case strings.ContainsAny(command, "\n\r"):
		return "", "", fmt.Errorf("%w: a command is one line", ErrUserAction)
	}
	return name, command, nil
}

// SpaceCommands lists space id's saved commands.
func (s *Supervisor) SpaceCommands(ctx context.Context, id string) ([]WrapUp, error) {
	sp, err := s.space(ctx, id)
	if err != nil {
		return nil, err
	}
	return append([]WrapUp{}, s.loadWrapUps()[sp.Path]...), nil
}

// SaveCommand adds a command to space id (cmdID "") or replaces one.
func (s *Supervisor) SaveCommand(ctx context.Context, id, cmdID, name, command string) (WrapUp, error) {
	name, command, err := checkWrapUp(name, command)
	if err != nil {
		return WrapUp{}, err
	}
	sp, err := s.space(ctx, id)
	if err != nil {
		return WrapUp{}, err
	}
	if sp.Path == "" {
		return WrapUp{}, fmt.Errorf("%w: %s has no folder", ErrUserAction, sp.Name)
	}
	s.wrapMu.Lock()
	defer s.wrapMu.Unlock()
	all := s.readWrapUpsLocked()
	list := all[sp.Path]
	w := WrapUp{ID: cmdID, Name: name, Command: command}
	if cmdID == "" {
		if len(list) >= wrapUpMax {
			return WrapUp{}, fmt.Errorf("%w: a space keeps at most %d commands", ErrUserAction, wrapUpMax)
		}
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		w.ID = hex.EncodeToString(b)
		list = append(list, w)
	} else {
		i := wrapUpIndex(list, cmdID)
		if i < 0 {
			return WrapUp{}, ErrNoSuchCommand
		}
		list[i] = w
	}
	all[sp.Path] = list
	if err := s.writeWrapUpsLocked(all); err != nil {
		return WrapUp{}, fmt.Errorf("could not save the command: %w", err)
	}
	s.log.Log(logSource, "supervisor.wrapup_saved", "", map[string]any{"space": id, "id": w.ID, "name": name, "new": cmdID == ""})
	return w, nil
}

// ErrNoSuchCommand is a wrap-up command id the space does not have.
var ErrNoSuchCommand = errors.New("no such command")

func wrapUpIndex(list []WrapUp, id string) int {
	for i, w := range list {
		if w.ID == id {
			return i
		}
	}
	return -1
}

// DeleteCommand removes one of space id's commands.
func (s *Supervisor) DeleteCommand(ctx context.Context, id, cmdID string) error {
	sp, err := s.space(ctx, id)
	if err != nil {
		return err
	}
	s.wrapMu.Lock()
	defer s.wrapMu.Unlock()
	all := s.readWrapUpsLocked()
	list := all[sp.Path]
	i := wrapUpIndex(list, cmdID)
	if i < 0 {
		return ErrNoSuchCommand
	}
	list = append(list[:i], list[i+1:]...)
	if len(list) == 0 {
		delete(all, sp.Path)
	} else {
		all[sp.Path] = list
	}
	if err := s.writeWrapUpsLocked(all); err != nil {
		return fmt.Errorf("could not save: %w", err)
	}
	s.log.Log(logSource, "supervisor.wrapup_deleted", "", map[string]any{"space": id, "id": cmdID})
	return nil
}

// RunCommand runs one of space id's commands in a new tab of the space,
// in its folder (herdr's pane run: the line and Enter at the shell
// prompt), and returns the tab's pane.
func (s *Supervisor) RunCommand(ctx context.Context, id, cmdID string) (string, error) {
	h, err := s.spaceHost()
	if err != nil {
		return "", err
	}
	sp, ok := s.herdr.(Spawner)
	if !ok {
		return "", errors.New("spaces are not available here")
	}
	space, err := s.space(ctx, id)
	if err != nil {
		return "", err
	}
	list := s.loadWrapUps()[space.Path]
	i := wrapUpIndex(list, cmdID)
	if i < 0 {
		return "", ErrNoSuchCommand
	}
	w := list[i]
	pane, err := h.CreateTab(ctx, id, space.Path, w.Name)
	if err != nil {
		return "", fmt.Errorf("could not open a tab in %s: %w", space.Name, err)
	}
	s.waitShell(ctx, sp, pane.PaneID)
	if err := h.SendInput(ctx, pane.PaneID, w.Command, "enter"); err != nil {
		return "", fmt.Errorf("could not run %s: %w", w.Name, err)
	}
	s.log.Log(logSource, "supervisor.wrapup_run", pane.PaneID, map[string]any{"space": id, "id": w.ID, "name": w.Name, "via": "user"})
	s.tellBrain(fmt.Sprintf("the user ran %q (%s) in the space %s from the phone", w.Name, w.Command, space.Name))
	return pane.PaneID, nil
}
