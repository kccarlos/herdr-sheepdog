package supervisor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// Closing an agent by voice (docs/dev/roles.md "Closing an agent"): the
// orchestrator's interrupt tool with close true. Which agent the user meant is
// the orchestrator's judgement; the daemon holds the rails, which are about
// authority only:
//
//   - the user's own recent words must be behind it, not yet spent on
//     another write (wordsRailLocked, the spawn rail's reading; one input
//     closes one agent);
//   - an agent the daemon protects — the pane the daemon itself runs in,
//     and any agent working in one of Config.ProtectedDirs (the daemon's
//     own directory, where the user's orchestrator session works) — is
//     closed only when the orchestrator says the user named that very agent
//     (protected_ok); otherwise the orchestrator is told why, to ask.
//
// The pane is closed with herdr's pane.close; herdr's pane_closed then
// reaches the roster like any agent that exits.

// Closer is a Herdr that can close a pane. A Herdr without it refuses
// close.
type Closer interface {
	ClosePane(ctx context.Context, paneID string) error
}

var _ Closer = (*herdr.Client)(nil)

// CloseAgent implements brain.Closer.
func (h brainHost) CloseAgent(ctx context.Context, req brain.CloseRequest) (string, error) {
	return h.s.closeAgent(ctx, req)
}

var _ brain.Closer = brainHost{}

func (s *Supervisor) closeAgent(ctx context.Context, req brain.CloseRequest) (string, error) {
	row, ok := s.store.Get(req.PaneID)
	if !ok {
		return "", errors.New("that agent is gone")
	}
	name := state.Say(row)
	refuse := func(reason string, err error) (string, error) {
		s.log.Log(logSource, "supervisor.close_refused", req.PaneID, map[string]any{"agent": aliasOr(row.Alias), "reason": reason, "protected_ok": req.ProtectedOK})
		return "", err
	}
	c, ok := s.herdr.(Closer)
	if !ok {
		return refuse("no herdr closer", errors.New("closing an agent is not available here"))
	}
	if why := s.protectedWhy(row); why != "" && !req.ProtectedOK {
		return refuse("protected", fmt.Errorf("%w: %s %s; close it only if the user named this very agent: ask them, then call again with protected_ok", brain.ErrNotPermitted, name, why))
	}
	s.mu.Lock()
	reason := s.wordsRailLocked("closing an agent", "started or closed an agent")
	if reason == "" {
		s.lastInputLocked().used = true
	}
	s.mu.Unlock()
	if reason != "" {
		return refuse(reason, fmt.Errorf("%w: %s", brain.ErrNotPermitted, reason))
	}
	if err := c.ClosePane(ctx, req.PaneID); err != nil {
		s.log.Log(logSource, "supervisor.close_error", req.PaneID, map[string]any{"agent": aliasOr(row.Alias), "error": err.Error()})
		if errors.Is(err, herdr.ErrPaneNotFound) {
			return "", fmt.Errorf("%s's pane is already gone", name)
		}
		return "", fmt.Errorf("could not close %s: %w", name, err)
	}
	s.mu.Lock()
	s.dropPendingFor(req.PaneID, "closed")
	s.mu.Unlock()
	s.log.Log(logSource, "supervisor.close", req.PaneID, map[string]any{"agent": aliasOr(row.Alias), "spoken": name, "kind": row.Kind, "cwd": row.Cwd, "state": string(row.State), "protected_ok": req.ProtectedOK})
	return "Closed " + name + "; its session has ended.", nil
}

// protectedWhy says why the daemon protects the agent, "" when it does
// not: it runs in the daemon's own pane, or works in a protected
// directory.
func (s *Supervisor) protectedWhy(row state.Agent) string {
	if s.cfg.OwnPane != "" && row.PaneID == s.cfg.OwnPane {
		return "runs in the pane sheepdog itself runs in"
	}
	cwd := herdr.RealDir(row.Cwd)
	if cwd == "" {
		cwd = filepath.Clean(row.Cwd)
	}
	for _, d := range s.cfg.ProtectedDirs {
		p := herdr.RealDir(d)
		if p == "" {
			p = filepath.Clean(d)
		}
		if row.Cwd != "" && cwd == p {
			return "works in sheepdog's own directory, where the user's own session supervising sheepdog usually runs"
		}
	}
	return ""
}
