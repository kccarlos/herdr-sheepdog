package herdrcore

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/procprio"
)

// CreateWorkspace opens a new shell terminal in cwd, in a new herdr-app
// space and tab (fresh ids in its labels: herdr-app makes them on sight).
// The space has no name of its own, so label is ignored and herdr-app names
// it after the folder.
func (b *Backend) CreateWorkspace(ctx context.Context, cwd, _ string) (herdr.PaneInfo, error) {
	return b.openShell(ctx, cwd, newID(), spawnValue, nil)
}

// SplitPane opens a new shell terminal in cwd, in a new tab of the space
// paneID is in. hc has no splits. A pane that is in no space (a terminal
// herdr-app does not show) has none to join, so it gets a new space.
// When the anchor pane has known dimensions, the new terminal inherits
// its size so the new agent does not start in a wrong-sized pane.
func (b *Backend) SplitPane(ctx context.Context, paneID, cwd string) (herdr.PaneInfo, error) {
	anchor, err := b.client.Get(ctx, paneID)
	if err != nil {
		return herdr.PaneInfo{}, translate(err)
	}
	space := spaceOf(anchor)
	if space == "" {
		space = newID()
	}
	var size *hc.InitialSize
	if anchor.Cols > 0 && anchor.Rows > 0 {
		size = &hc.InitialSize{Cols: anchor.Cols, Rows: anchor.Rows}
	}
	return b.openShell(ctx, cwd, space, spawnValue, size)
}

// openShell runs the machine's own shell (no argv) in cwd, kept by the
// core, in a new tab of space. role is sheepdog's label value: spawn for an
// agent's shell, shell for the phone's plain shell. size sets initial
// terminal dimensions when non-nil.
func (b *Backend) openShell(ctx context.Context, cwd, space, role string, size *hc.InitialSize) (herdr.PaneInfo, error) {
	tab := newID()
	t, err := b.client.Run(ctx, hc.RunParams{
		Cwd: cwd,
		Env: herdr.PaneEnv,
		// herdr.seen=0: a terminal a client starts is read, not unread
		// (herdr-app docs/labels.md).
		Labels:   map[string]string{spawnLabel: role, labelSpace: space, labelTab: tab, labelSeen: "0"},
		Keep:     true,
		NoParent: true,
		Size:     size,
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
	command := b.agentCommand(kind, args)
	_, err := b.client.Send(ctx, paneID, command, false)
	b.logEvent("herdr.agent_start", paneID, map[string]any{"kind": kind, "command": command, "error": errString(err)})
	if err != nil {
		return translate(err)
	}
	b.startLaunch(paneID, kind, timeout)
	b.startedSeen(ctx, paneID)
	return nil
}

// agentCommand is the shell line that starts kind with args at utility OS
// priority so heavy worker builds do not starve realtime voice audio. Codex
// runs with --no-daemon under hc, as herdr-core's own skill says.
func (b *Backend) agentCommand(kind string, args []string) string {
	var words []string
	for _, w := range procprio.ShellPrefixForOS(b.goos(), b.lookPath()) {
		words = append(words, shellQuote(w))
	}
	words = append(words, kind)
	if kind == "codex" {
		words = append(words, "--no-daemon")
	}
	for _, arg := range args {
		words = append(words, shellQuote(arg))
	}
	return strings.Join(words, " ")
}

func (b *Backend) goos() string {
	if b.GOOS != "" {
		return b.GOOS
	}
	return runtime.GOOS
}

func (b *Backend) lookPath() func(string) (string, error) {
	if b.LookPath != nil {
		return b.LookPath
	}
	return exec.LookPath
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
