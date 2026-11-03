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
)

// wireMarks gives the daemon the marker for its backend (a.backend and
// a.herdr must be set): herdr-core keeps the seen and pinned marks in
// terminal labels that every Herdr client shares; the old herdr and the mock
// have none, so sheepdog keeps them in <data_dir>/agent_marks.json.
func (a *App) wireMarks(cfg config.Config, warn io.Writer) {
	if core, ok := a.backend.(*herdrcore.Backend); ok {
		a.marker, a.marksRun = core, core.RunMarks
		return
	}
	// The mock has no herdr to ask: its store only holds what a caller writes.
	var src attention.Source
	if client, ok := a.backend.(*herdr.Client); ok {
		src = client
	}
	path := filepath.Join(cfg.DataDir, attention.FileName)
	local, err := attention.NewLocal(path, src, attention.Options{Warn: warn})
	if err != nil {
		// The marks are a convenience: an unreadable data directory keeps
		// them in memory rather than stopping the daemon.
		a.log.Log("daemon", "daemon.marks_error", "", map[string]any{"path": path, "error": err.Error()})
		local, _ = attention.NewLocal("", src, attention.Options{})
	}
	a.marker = local
	if src == nil {
		return
	}
	interval := time.Duration(cfg.SnapshotIntervalS) * time.Second
	a.marksRun = func(ctx context.Context) { local.Run(ctx, interval) }
	a.marksPoke = local.Poke
	// A pane sheepdog starts an agent in begins read, not unread: the
	// counterpart of herdr.seen=0 that herdr-core takes in a label.
	a.herdr.AfterStart = func(ctx context.Context, paneID string) {
		if err := local.AgentStarted(ctx, paneID); err != nil {
			a.log.Log("daemon", "daemon.marks_error", paneID, map[string]any{"error": err.Error()})
		}
	}
}
