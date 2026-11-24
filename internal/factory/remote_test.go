package factory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// Blocking probe (NeonH §2.2, Carbon §2.1): coordTerminalsLocked must ignore
// remote terminals even when their labels match this machine's project ID and
// coordinator role, so a local factory never adopts or kills a remote terminal.
func TestCoordTerminalsLockedIgnoresRemoteTerminals(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "secret.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := hctest.New(t)
	srv.Add(hc.Terminal{
		Address:   "box/t4",
		State:     "live",
		CreatedMS: 100,
		Cwd:       localDir,
		Labels: map[string]string{
			"sheepdog.project": "p1",
			"sheepdog.role":    flow.RoleCoordinator,
			"sheepdog.run":     "remote-run-1",
		},
	})
	eng := &Engine{HC: hc.NewClient(srv.Path)}

	owned, err := eng.coordTerminalsLocked(context.Background(), "p1", nil)
	if err != nil {
		t.Fatalf("coordTerminalsLocked error = %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("coordTerminalsLocked adopted remote terminal(s): %+v", owned)
	}
}
