package herdrcore

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// CreateWorkspace opens a new shell terminal in cwd, in a new herdr-app
// space and tab (fresh ids in its labels: herdr-app makes them on sight).
// The space has no name of its own, so label is ignored and herdr-app names
// it after the folder.
func (b *Backend) CreateWorkspace(ctx context.Context, cwd, _ string) (herdr.PaneInfo, error) {
	return b.openShell(ctx, cwd, newID(), spawnValue)
}

// SplitPane opens a new shell terminal in cwd, in a new tab of the space
// paneID is in. hc has no splits. A pane that is in no space (a terminal
// herdr-app does not show) has none to join, so it gets a new space.
func (b *Backend) SplitPane(ctx context.Context, paneID, cwd string) (herdr.PaneInfo, error) {
	anchor, err := b.client.Get(ctx, paneID)
	if err != nil {
		return herdr.PaneInfo{}, translate(err)
	}
	space := spaceOf(anchor)
	if space == "" {
		space = newID()
	}
	return b.openShell(ctx, cwd, space, spawnValue)
}

// openShell runs the machine's own shell (no argv) in cwd, kept by the
// core, in a new tab of space. role is sheepdog's label value: spawn for an
// agent's shell, shell for the phone's plain shell.
func (b *Backend) openShell(ctx context.Context, cwd, space, role string) (herdr.PaneInfo, error) {
	tab := newID()
	t, err := b.client.Run(ctx, hc.RunParams{
		Cwd:      cwd,
		Env:      herdr.PaneEnv,
		Labels:   map[string]string{spawnLabel: role, labelSpace: space, labelTab: tab},
		Keep:     true,
		NoParent: true,
	})
	b.logEvent("herdr.open_shell", t.Address, map[string]any{"cwd": cwd, "space": space, "tab": tab, "role": role, "error": errString(err)})
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	return paneInfo(t, mapState(t.Status)), nil
}

// ProcessInfo reports the terminal's shell and foreground program.
func (b *Backend) ProcessInfo(ctx context.Context, paneID string) (herdr.ProcessInfo, error) {
	t, err := b.client.Get(ctx, paneID)
	if err != nil {
		return herdr.ProcessInfo{}, translate(err)
	}
	info := herdr.ProcessInfo{PaneID: t.Address}
	if t.PID != nil {
		info.ShellPID = pid32(*t.PID)
	}
	if fg := t.Foreground; fg != nil {
		info.ForegroundProcessGroup = pid32(fg.PID)
		info.ForegroundProcesses = []herdr.ProcessItem{{PID: pid32(fg.PID), Name: fg.Name, Argv: fg.Argv}}
	}
	return info, nil
}

func pid32(pid int) uint32 {
	if pid < 0 || int64(pid) > math.MaxUint32 {
		return 0
	}
	return uint32(pid)
}

// StartAgent types the kind's command into the terminal's shell and returns
// at once, like herdr's agent.start: until the program shows, GetAgent
// reports the launch pending, and after timeout (zero: DefaultLaunchWait)
// not found. The caller polls GetAgent until the agent reports, shows a
// question or gives up. name is a placeholder (hc cannot rename).
func (b *Backend) StartAgent(ctx context.Context, paneID, _, kind string, args []string, timeout time.Duration) error {
	if !knownKind(kind) {
		return fmt.Errorf("herdrcore: unknown agent kind %q", kind)
	}
	command := agentCommand(kind, args)
	_, err := b.client.Send(ctx, paneID, command, false)
	b.logEvent("herdr.agent_start", paneID, map[string]any{"kind": kind, "command": command, "error": errString(err)})
	if err != nil {
		return translate(err)
	}
	b.startLaunch(paneID, kind, timeout)
	return nil
}

// agentCommand is the shell line that starts kind with args. Codex runs
// with --no-daemon under hc, as herdr-core's own skill says.
func agentCommand(kind string, args []string) string {
	words := []string{kind}
	if kind == "codex" {
		words = append(words, "--no-daemon")
	}
	for _, arg := range args {
		words = append(words, shellQuote(arg))
	}
	return strings.Join(words, " ")
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote quotes s as one POSIX shell word.
func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// AgentKinds lists the known agent kinds whose program is on PATH.
func (b *Backend) AgentKinds(context.Context) ([]string, error) {
	var kinds []string
	for _, kind := range knownKinds {
		if _, err := b.LookPath(kind); err == nil {
			kinds = append(kinds, kind)
		}
	}
	return kinds, nil
}
