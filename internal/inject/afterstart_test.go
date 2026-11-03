package inject

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// spawnStub is an inner herdr that can start agents and records the panes
// it started them in.
type spawnStub struct {
	innerHerdr
	err     error
	started []string
}

func (s *spawnStub) StartAgent(_ context.Context, paneID, _, _ string, _ []string, _ time.Duration) error {
	s.started = append(s.started, paneID)
	return s.err
}

func (s *spawnStub) Snapshot(context.Context) (herdr.Snapshot, error) { return herdr.Snapshot{}, nil }
func (s *spawnStub) CreateWorkspace(context.Context, string, string) (herdr.PaneInfo, error) {
	return herdr.PaneInfo{}, nil
}
func (s *spawnStub) SplitPane(context.Context, string, string) (herdr.PaneInfo, error) {
	return herdr.PaneInfo{}, nil
}
func (s *spawnStub) ProcessInfo(context.Context, string) (herdr.ProcessInfo, error) {
	return herdr.ProcessInfo{}, nil
}
func (s *spawnStub) GetAgent(context.Context, string) (herdr.AgentInfo, error) {
	return herdr.AgentInfo{}, nil
}
func (s *spawnStub) AgentKinds(context.Context) ([]string, error) { return nil, nil }

func TestStartAgentRunsAfterStartOnlyOnSuccess(t *testing.T) {
	inner := &spawnStub{}
	h := NewHerdr(inner, nil)
	var hooked []string
	h.AfterStart = func(_ context.Context, paneID string) { hooked = append(hooked, paneID) }
	ctx := context.Background()

	if err := h.StartAgent(ctx, "w1:p1", "n", "claude", nil, 0); err != nil {
		t.Fatal(err)
	}
	if len(hooked) != 1 || hooked[0] != "w1:p1" {
		t.Fatalf("hook ran for %v, want [w1:p1]", hooked)
	}

	inner.err = errors.New("no such pane")
	if err := h.StartAgent(ctx, "w1:p2", "n", "claude", nil, 0); err == nil {
		t.Fatal("StartAgent hid the inner error")
	}
	if len(hooked) != 1 {
		t.Fatalf("hook ran for a start that failed: %v", hooked)
	}
	if len(inner.started) != 2 {
		t.Fatalf("inner started %v", inner.started)
	}
}

func TestStartAgentWithoutAHook(t *testing.T) {
	h := NewHerdr(&spawnStub{}, nil)
	if err := h.StartAgent(context.Background(), "w1:p1", "n", "claude", nil, 0); err != nil {
		t.Fatal(err)
	}
}
