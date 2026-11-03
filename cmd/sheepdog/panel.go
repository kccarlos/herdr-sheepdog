package main

// The herdr-app panel the daemon ships and keeps current (docs/dev/update.md,
// "The panel after an upgrade"): setup records what it installed, the daemon
// replaces an unchanged panel at startup, and /api/state reports the rest.

import (
	"os"
	"sync"
	"time"

	herdrsheepdog "github.com/kccarlos/herdr-sheepdog"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/panelsync"
)

// panelSource is the panel and layout built into this binary.
func panelSource() panelsync.Source {
	return panelsync.Source{Panel: herdrsheepdog.Panel, Layout: herdrsheepdog.Layout, Version: version}
}

// panelHome is herdr-app's home on this computer.
func panelHome() string {
	home, _ := os.UserHomeDir()
	return panelsync.Home(os.Getenv, home)
}

// panelTracker answers /api/state's panel object. The answer reads the files
// (a few hundred KB), so it is kept for panelStateTTL.
type panelTracker struct {
	home string
	src  panelsync.Source
	auto bool

	mu   sync.Mutex
	at   time.Time
	last panelsync.State
}

const panelStateTTL = 10 * time.Second

func newPanelTracker(cfg config.Config) *panelTracker {
	return &panelTracker{home: panelHome(), src: panelSource(), auto: cfg.Panel.AutoUpdate}
}

// State is the panel object of /api/state.
func (t *panelTracker) State() panelsync.State {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.at.IsZero() || time.Since(t.at) > panelStateTTL {
		t.refresh()
	}
	return t.last
}

func (t *panelTracker) refresh() {
	t.last = panelsync.Evaluate(t.home, t.src)
	t.last.AutoUpdate = t.auto
	t.at = time.Now()
}

// Sync replaces an unchanged installed panel (panel.auto_update) and logs
// panel.updated; a failure is logged as panel.update_failed and leaves the
// panel as it was. It returns the line to print, or "".
func (t *panelTracker) Sync(log logging.Logger) string {
	defer func() {
		t.mu.Lock()
		t.refresh()
		t.mu.Unlock()
	}()
	if !t.auto {
		return ""
	}
	res, err := panelsync.Sync(t.home, t.src)
	if err != nil {
		log.Log("daemon", "panel.update_failed", "", map[string]any{"error": err.Error()})
		return "could not update the herdr-app panel: " + err.Error()
	}
	if !res.Updated && !res.LayoutUpdated {
		return ""
	}
	log.Log("daemon", "panel.updated", "", map[string]any{
		"from": res.From, "to": res.To, "files": res.Files, "layout": res.LayoutUpdated,
	})
	return "updated the herdr-app panel to " + t.src.Version
}
