package inject

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// Herdr serves synthetic panes from memory and delegates everything else
// to the inner client (nil inner = mock herdr: unknown panes are
// ErrPaneNotFound). It is safe for concurrent use.
type Herdr struct {
	inner supervisor.Herdr
	log   logging.Logger

	mu    sync.Mutex
	panes map[string]*synthetic
}

type synthetic struct {
	screen string
	agent  string
	status herdr.State
	cwd    string
	sends  int
}

var _ supervisor.Herdr = (*Herdr)(nil)
var _ supervisor.Renamer = (*Herdr)(nil)
var _ supervisor.Closer = (*Herdr)(nil)
var _ supervisor.Spawner = (*Herdr)(nil)

// NewHerdr wraps inner (may be nil). log may be nil.
func NewHerdr(inner supervisor.Herdr, log logging.Logger) *Herdr {
	if log == nil {
		log = logging.NopLogger{}
	}
	return &Herdr{inner: inner, log: log, panes: map[string]*synthetic{}}
}

// Mock reports whether there is no real herdr behind this decorator.
func (h *Herdr) Mock() bool { return h.inner == nil }

// SetScreen registers or updates a synthetic pane: what Read returns, the
// agent kind label and the status ListAgents reports.
func (h *Herdr) SetScreen(paneID, agent, screen string, status herdr.State) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.panes[paneID]
	if !ok {
		p = &synthetic{}
		h.panes[paneID] = p
	}
	p.screen, p.status = screen, status
	if agent != "" {
		p.agent = agent
	}
}

// SetStatus changes the status ListAgents reports for a synthetic pane.
func (h *Herdr) SetStatus(paneID string, status herdr.State) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.panes[paneID]; ok {
		p.status = status
	}
}

// Remove forgets a synthetic pane.
func (h *Herdr) Remove(paneID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.panes, paneID)
}

// IsSynthetic reports whether paneID is served from memory.
func (h *Herdr) IsSynthetic(paneID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.panes[paneID]
	return ok
}

func (h *Herdr) get(paneID string) (synthetic, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.panes[paneID]
	if !ok {
		return synthetic{}, false
	}
	return *p, true
}

// Read implements supervisor.Herdr.
func (h *Herdr) Read(ctx context.Context, paneID string, source herdr.ReadSource, lines int) (herdr.ReadResult, error) {
	p, ok := h.get(paneID)
	if !ok {
		if h.inner == nil {
			return herdr.ReadResult{}, fmt.Errorf("%w: %s", herdr.ErrPaneNotFound, paneID)
		}
		return h.inner.Read(ctx, paneID, source, lines)
	}
	text := tailLines(p.screen, lines)
	h.log.Log("herdr", "herdr.read", paneID, map[string]any{
		"source": string(source), "lines": lines, "bytes": len(text), "truncated": len(text) < len(p.screen), "dur_ms": 0, "synthetic": true,
	})
	return herdr.ReadResult{PaneID: paneID, Source: source, Text: text, Truncated: len(text) < len(p.screen)}, nil
}

// ReadANSI implements supervisor.ANSIReader: a synthetic pane's screen
// has no colours, so it is Read; a real one goes to the inner client
// when it can keep them.
func (h *Herdr) ReadANSI(ctx context.Context, paneID string, source herdr.ReadSource, lines int) (herdr.ReadResult, error) {
	if _, ok := h.get(paneID); !ok && h.inner != nil {
		if r, ok := h.inner.(interface {
			ReadANSI(context.Context, string, herdr.ReadSource, int) (herdr.ReadResult, error)
		}); ok {
			return r.ReadANSI(ctx, paneID, source, lines)
		}
	}
	return h.Read(ctx, paneID, source, lines)
}

// Prompt implements supervisor.Herdr: for a synthetic pane it records the
// send and appends the text to the screen so a later Read shows it.
func (h *Herdr) Prompt(ctx context.Context, paneID, text string) (herdr.AgentInfo, error) {
	if !h.IsSynthetic(paneID) {
		if h.inner == nil {
			return herdr.AgentInfo{}, fmt.Errorf("%w: %s", herdr.ErrPaneNotFound, paneID)
		}
		return h.inner.Prompt(ctx, paneID, text)
	}
	h.mu.Lock()
	p := h.panes[paneID]
	p.sends++
	p.screen += "\n› " + text + "\n"
	info := herdr.AgentInfo{PaneID: paneID, Agent: p.agent, Status: p.status, Cwd: p.cwd}
	h.mu.Unlock()
	h.log.Log("herdr", "herdr.send", paneID, map[string]any{"kind": "prompt", "text": text, "synthetic": true})
	return info, nil
}

// SendKeys implements supervisor.Herdr: recorded, never delivered.
func (h *Herdr) SendKeys(ctx context.Context, paneID string, keys ...string) error {
	if len(keys) == 0 {
		return fmt.Errorf("herdr: SendKeys: no keys")
	}
	if !h.IsSynthetic(paneID) {
		if h.inner == nil {
			return fmt.Errorf("%w: %s", herdr.ErrPaneNotFound, paneID)
		}
		return h.inner.SendKeys(ctx, paneID, keys...)
	}
	h.mu.Lock()
	h.panes[paneID].sends++
	h.mu.Unlock()
	h.log.Log("herdr", "herdr.send", paneID, map[string]any{"kind": "keys", "keys": keys, "synthetic": true})
	return nil
}

// Rename implements supervisor.Renamer: a synthetic pane's rename is
// only logged; a real one goes to the inner client when it can rename.
func (h *Herdr) Rename(ctx context.Context, paneID, name string) error {
	if !h.IsSynthetic(paneID) {
		r, ok := h.inner.(supervisor.Renamer)
		if !ok {
			return fmt.Errorf("herdr: cannot rename %s here", paneID)
		}
		return r.Rename(ctx, paneID, name)
	}
	h.log.Log("herdr", "herdr.rename", paneID, map[string]any{"name": name, "synthetic": true})
	return nil
}

// ClosePane implements supervisor.Closer: a synthetic pane has nothing
// to close and is refused; a real one goes to the inner client.
func (h *Herdr) ClosePane(ctx context.Context, paneID string) error {
	if h.IsSynthetic(paneID) {
		return fmt.Errorf("herdr: %s is not a real pane", paneID)
	}
	c, ok := h.inner.(supervisor.Closer)
	if !ok {
		return fmt.Errorf("herdr: cannot close %s here", paneID)
	}
	return c.ClosePane(ctx, paneID)
}

// spawner is the inner client's spawn half, or an error saying there is
// none (mock herdr): a new agent is always a real one.
func (h *Herdr) spawner() (supervisor.Spawner, error) {
	sp, ok := h.inner.(supervisor.Spawner)
	if !ok {
		return nil, fmt.Errorf("herdr: cannot start agents here")
	}
	return sp, nil
}

// Snapshot implements supervisor.Spawner (the inner client's snapshot;
// synthetic panes have no workspace).
func (h *Herdr) Snapshot(ctx context.Context) (herdr.Snapshot, error) {
	sp, err := h.spawner()
	if err != nil {
		return herdr.Snapshot{}, err
	}
	return sp.Snapshot(ctx)
}

// CreateWorkspace implements supervisor.Spawner.
func (h *Herdr) CreateWorkspace(ctx context.Context, cwd, label string) (herdr.PaneInfo, error) {
	sp, err := h.spawner()
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	return sp.CreateWorkspace(ctx, cwd, label)
}

// CreateWorktree implements supervisor.WorktreeCreator (the inner
// client's; mock herdr has none).
func (h *Herdr) CreateWorktree(ctx context.Context, cwd, branch, label string) (herdr.PaneInfo, error) {
	wt, ok := h.inner.(supervisor.WorktreeCreator)
	if !ok {
		return herdr.PaneInfo{}, fmt.Errorf("herdr: cannot make worktrees here")
	}
	return wt.CreateWorktree(ctx, cwd, branch, label)
}

// SplitPane implements supervisor.Spawner.
func (h *Herdr) SplitPane(ctx context.Context, paneID, cwd string) (herdr.PaneInfo, error) {
	sp, err := h.spawner()
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	return sp.SplitPane(ctx, paneID, cwd)
}

// SendInput implements supervisor.Spawner.
func (h *Herdr) SendInput(ctx context.Context, paneID, text string, keys ...string) error {
	sp, err := h.spawner()
	if err != nil {
		return err
	}
	return sp.SendInput(ctx, paneID, text, keys...)
}

// ProcessInfo implements supervisor.Spawner.
func (h *Herdr) ProcessInfo(ctx context.Context, paneID string) (herdr.ProcessInfo, error) {
	sp, err := h.spawner()
	if err != nil {
		return herdr.ProcessInfo{}, err
	}
	return sp.ProcessInfo(ctx, paneID)
}

// Interrupt implements supervisor.Herdr.
func (h *Herdr) Interrupt(ctx context.Context, paneID string) error {
	return h.SendKeys(ctx, paneID, "ctrl+c")
}

// ListAgents implements supervisor.Herdr: the inner list (when there is
// one and it answers) plus every synthetic pane, sorted by pane id.
func (h *Herdr) ListAgents(ctx context.Context) ([]herdr.AgentInfo, error) {
	var out []herdr.AgentInfo
	if h.inner != nil {
		real, err := h.inner.ListAgents(ctx)
		if err != nil {
			if !h.anySynthetic() {
				return nil, err
			}
			h.log.Log("herdr", "herdr.list_error", "", map[string]any{"error": err.Error(), "synthetic_only": true})
		} else {
			out = append(out, real...)
		}
	}
	h.mu.Lock()
	for id, p := range h.panes {
		out = append(out, herdr.AgentInfo{PaneID: id, Agent: p.agent, Status: p.status, Cwd: p.cwd})
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].PaneID < out[j].PaneID })
	return out, nil
}

func (h *Herdr) anySynthetic() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.panes) > 0
}

// tailLines returns the last n lines of s (all when n <= 0).
func tailLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	end := len(s)
	if end > 0 && s[end-1] == '\n' {
		end--
	}
	count := 0
	for i := end - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count == n {
				return s[i+1:]
			}
		}
	}
	return s
}
