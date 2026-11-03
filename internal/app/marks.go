package app

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/attention"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
)

// markFleet is the fleet the old herdr's and the mock's marks follow: the
// real herdr's panes (none for the mock) and the debug injector's synthetic
// ones.
type markFleet struct{ h *inject.Herdr }

func (f markFleet) Snapshot(ctx context.Context) (herdr.Snapshot, error) {
	return f.h.MarkSnapshot(ctx)
}

// wireMarks gives the daemon the marker for its backend (a.backend and
// a.herdr must be set): herdr-core keeps the seen and pinned marks in
// terminal labels that every Herdr client shares; the old herdr and the mock
// have none, so sheepdog keeps them in <data_dir>/agent_marks.json. With
// herdr-core the injector's synthetic panes (a debug feature) have no marks:
// they read by their state alone and cannot be seen or pinned.
func (a *App) wireMarks(cfg config.Config, warn io.Writer) {
	if core, ok := a.backend.(*herdrcore.Backend); ok {
		a.marker, a.marksRun = core, core.RunMarks
		return
	}
	src := markFleet{a.herdr}
	path := filepath.Join(cfg.DataDir, attention.FileName)
	local, err := attention.NewLocal(path, src, attention.Options{Warn: warn})
	if err != nil {
		// The marks are a convenience: an unreadable data directory keeps
		// them in memory rather than stopping the daemon.
		a.log.Log("daemon", "daemon.marks_error", "", map[string]any{"path": path, "error": err.Error()})
		local, _ = attention.NewLocal("", src, attention.Options{})
	}
	a.marker = local
	interval := time.Duration(cfg.SnapshotIntervalS) * time.Second
	a.marksRun = func(ctx context.Context) { local.Run(ctx, interval) }
	a.marksPoke = local.Poke
	// A synthetic pane moves without a Watcher transition: look at once, so
	// a route that follows the move sees it.
	a.herdr.Changed = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = local.Refresh(ctx)
	}
	// A pane sheepdog starts an agent in begins read, not unread: the
	// counterpart of herdr.seen=0 that herdr-core takes in a label.
	a.herdr.AfterStart = func(ctx context.Context, paneID string) {
		if err := local.AgentStarted(ctx, paneID); err != nil {
			a.log.Log("daemon", "daemon.marks_error", paneID, map[string]any{"error": err.Error()})
		}
	}
}

// wireMutes opens the space mute store (<data_dir>/space_mutes.json). An
// unreadable data directory keeps the mutes in memory rather than stopping
// the daemon.
func (a *App) wireMutes(cfg config.Config) {
	path := filepath.Join(cfg.DataDir, attention.MutesFileName)
	m, err := attention.OpenMutes(path, attention.MutesOptions{})
	if err != nil {
		a.log.Log("daemon", "daemon.mutes_error", "", map[string]any{"path": path, "error": err.Error()})
		m, _ = attention.OpenMutes("", attention.MutesOptions{})
	}
	a.mutes = m
}
