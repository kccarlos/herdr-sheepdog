package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Starting a new agent by voice (docs/roles.md "Starting an agent"): the
// brain's prompt tool with new_agent_dir. The judgement — which project
// the CEO meant, which kind, whether to ask — is the brain's; the daemon
// gives it the candidate directories when a name does not resolve, and
// holds the rails, which are about authorisation and runaways only:
//
//   - the CEO's own recent words must be behind it, not yet spent on
//     another write (spawnRailLocked, the same fresh-input reading as the
//     approval rail; one input starts one agent);
//   - the directory must resolve, symlinks followed, inside a project
//     root (supervisor.project_roots) or be an existing workspace's
//     directory: never an arbitrary path;
//   - at most spawnMax new agents per spawnWindow.
//
// The agent is started by typing its command line into a new pane's
// shell (herdr's agent.start needs a name, and a name the daemon chose
// would be heard as the CEO's own: state.CEOName); herdr detects it like
// any agent the CEO starts by hand, so it reaches the roster with a
// spoken name and its first-run trust question becomes a normal pending
// question. The first instruction, when there is one, follows once the
// agent sits idle at its prompt.

// Spawner is a Herdr that can open workspaces and panes and type into a
// pane's shell. A Herdr without it refuses new agents.
type Spawner interface {
	Snapshot(ctx context.Context) (herdr.Snapshot, error)
	CreateWorkspace(ctx context.Context, cwd, label string) (herdr.PaneInfo, error)
	SplitPane(ctx context.Context, paneID, cwd string) (herdr.PaneInfo, error)
	SendInput(ctx context.Context, paneID, text string, keys ...string) error
}

var _ Spawner = (*herdr.Client)(nil)

// The rate cap: runaway protection, not a judgement.
const (
	spawnMax    = 3
	spawnWindow = 10 * time.Minute
)

// Timings of a new agent's start. Vars so tests can shorten them.
var (
	// spawnPoll is how often the start-up watch looks.
	spawnPoll = time.Second
	// spawnShellWait bounds the wait for a new pane's shell to print
	// its prompt before the command line is typed.
	spawnShellWait = 5 * time.Second
	// spawnReadyWait is how long herdr may take to report the new agent
	// before the brain and the CEO are told it has not started, with the
	// pane's last lines (the watch goes on). herdr sees a starting Claude
	// or Codex within seconds, even at its trust question; a command
	// line a shell prompt ate never starts (2026-10-04).
	spawnReadyWait = 20 * time.Second
	// spawnFirstWait bounds the whole wait, from the start, for the
	// agent to be recognised and sit idle (its trust question answered)
	// before the first instruction goes; past it the instruction is
	// dropped, logged and the brain told.
	spawnFirstWait = 10 * time.Minute
	// spawnGoneCheck is how often a new agent herdr has not recognised
	// is checked for a closed pane.
	spawnGoneCheck = 15 * time.Second
)

// spawnScreenLines is how many of a not-ready agent's last pane lines
// the brain is given.
const spawnScreenLines = 15

// spawnModel is the model names the command line accepts: letters,
// digits and . _ : - only, so nothing reaches the shell but a word.
var spawnModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// spawnDir is one directory a new agent may start in.
type spawnDir struct {
	Name string // its base name: what the CEO says
	Path string // absolute, symlinks resolved
	// Pane is a pane of an existing workspace in this directory ("" when
	// none): the new agent's pane is split from it.
	Pane string
}

// Spawn implements brain.Spawner.
func (h brainHost) Spawn(ctx context.Context, req brain.SpawnRequest) (string, error) {
	return h.s.spawn(ctx, req)
}

var _ brain.Spawner = brainHost{}

func (s *Supervisor) spawn(ctx context.Context, req brain.SpawnRequest) (string, error) {
	refuse := func(reason string, err error) (string, error) {
		s.log.Log(logSource, "supervisor.spawn_refused", "", map[string]any{"reason": reason, "dir": req.Dir, "kind": req.Kind})
		return "", err
	}
	sp, ok := s.herdr.(Spawner)
	if !ok {
		return refuse("no herdr spawner", errors.New("starting a new agent is not available here"))
	}
	if req.Kind != brain.SpawnClaude && req.Kind != brain.SpawnCodex {
		return refuse("bad kind", fmt.Errorf("kind %q: want %s", req.Kind, strings.Join(brain.SpawnKinds, " or ")))
	}
	if req.Model != "" && !spawnModel.MatchString(req.Model) {
		return refuse("bad model", fmt.Errorf("model %q is not a model name", req.Model))
	}
	s.mu.Lock()
	reason := s.spawnRailLocked()
	s.mu.Unlock()
	if reason != "" {
		return refuse(reason, fmt.Errorf("%w: %s", brain.ErrNotPermitted, reason))
	}
	snap, err := sp.Snapshot(ctx)
	if err != nil {
		return refuse("snapshot failed", fmt.Errorf("could not list herdr's workspaces: %w", err))
	}
	dirs := s.spawnDirs(snap)
	dir, reason, err := resolveSpawnDir(req.Dir, dirs, s.cfg.ProjectRoots)
	if err != nil {
		return refuse(reason, err)
	}
	// The words and the slot are taken before anything opens, under the
	// lock that checked them: two calls racing cannot both spend the same
	// words or the last slot. The rail is read again, since the words may
	// have answered a prompt while the directory was resolved.
	s.mu.Lock()
	reason = s.spawnRailLocked()
	if reason == "" {
		reason = s.spawnRateLocked()
	}
	if reason != "" {
		s.mu.Unlock()
		return refuse(reason, fmt.Errorf("%w: %s", brain.ErrNotPermitted, reason))
	}
	s.lastInputLocked().used = true
	s.spawns = append(s.spawns, s.cfg.Now())
	s.mu.Unlock()

	var pane herdr.PaneInfo
	if dir.Pane != "" {
		pane, err = sp.SplitPane(ctx, dir.Pane, dir.Path)
	} else {
		pane, err = sp.CreateWorkspace(ctx, dir.Path, dir.Name)
	}
	if err != nil || pane.PaneID == "" {
		if err == nil {
			err = errors.New("herdr returned no pane")
		}
		s.log.Log(logSource, "supervisor.spawn_error", "", map[string]any{"dir": dir.Path, "kind": req.Kind, "stage": "pane", "error": err.Error()})
		return "", fmt.Errorf("could not open a pane in %s: %w", dir.Name, err)
	}
	s.waitShell(ctx, pane.PaneID)
	cmd := spawnCommand(req.Kind, req.Model)
	if err := sp.SendInput(ctx, pane.PaneID, cmd, "enter"); err != nil {
		s.log.Log(logSource, "supervisor.spawn_error", pane.PaneID, map[string]any{"dir": dir.Path, "kind": req.Kind, "stage": "start", "error": err.Error()})
		return "", fmt.Errorf("could not start %s in %s: %w", state.KindWord(req.Kind), dir.Name, err)
	}
	s.log.Log(logSource, "supervisor.spawn", pane.PaneID, map[string]any{"dir": dir.Path, "kind": req.Kind, "model": req.Model, "pane": pane.PaneID, "split": dir.Pane != "", "first_instruction": req.Text != ""})
	if s.track() {
		go func() {
			defer s.replies.Done()
			s.followSpawn(pane.PaneID, req.Kind, req.Text, dir.Name)
		}()
	}
	echo := "Starting a new " + state.KindWord(req.Kind) + " in " + dir.Name + "."
	if req.Text != "" {
		// Not "told it": the instruction is held (2026-10-03 live check,
		// the brain said "and told it to run the tests" before the trust
		// question was even answered).
		echo += " It has not been told anything yet: the instruction goes to it once it is ready."
	}
	return echo, nil
}

// spawnRailLocked is the authorisation rail of a new agent: the CEO's
// newest input — said or typed, never a tap on a question — arrived
// within supervisor.authorise_window_s, has words, is not a "no" or
// only a filler, and has not already authorised a write (an answer to a
// prompt, a new agent, a close: each marks the words used). It does not
// read what the words ask for: that is the brain's judgement. Caller
// holds s.mu.
func (s *Supervisor) spawnRailLocked() string {
	return s.wordsRailLocked("a new agent", "started an agent")
}

// wordsRailLocked is spawnRailLocked's reading for any write that needs
// words of the CEO's own: what names the write ("a new agent", "closing
// an agent"), spent what the words may already have done. Caller holds
// s.mu.
func (s *Supervisor) wordsRailLocked(what, spent string) string {
	in := s.lastInputLocked()
	now := s.cfg.Now()
	switch {
	case in == nil:
		return "the CEO has said nothing that asks for " + what
	case in.kind == InputTap:
		return "the CEO's last input was a tap on a question, not a request for " + what
	case in.used:
		return "the CEO's last words already answered a prompt or " + spent + "; " + what + " needs words of their own"
	case now.Sub(in.at) > s.cfg.AuthoriseWindow:
		return fmt.Sprintf("the CEO's last words were %d s ago, past supervisor.authorise_window_s (%d s)", int(now.Sub(in.at).Seconds()), int(s.cfg.AuthoriseWindow.Seconds()))
	case strings.TrimSpace(in.text) == "":
		return "the CEO's last words were empty"
	case fillerOnly(in.text):
		return fmt.Sprintf("the CEO only said %q, which asks for nothing", strings.TrimSpace(in.text))
	case negativeInput(in.text):
		return fmt.Sprintf("the CEO said %q, which does not ask for %s", strings.TrimSpace(in.text), what)
	}
	return ""
}

// spawnRateLocked is the runaway cap: it drops starts older than
// spawnWindow and refuses when spawnMax remain. Caller holds s.mu.
func (s *Supervisor) spawnRateLocked() string {
	now := s.cfg.Now()
	kept := s.spawns[:0]
	for _, t := range s.spawns {
		if now.Sub(t) < spawnWindow {
			kept = append(kept, t)
		}
	}
	s.spawns = kept
	if len(kept) >= spawnMax {
		return fmt.Sprintf("%d new agents were started in the last %d minutes, the most allowed; wait before starting another", len(kept), int(spawnWindow.Minutes()))
	}
	return ""
}

// spawnDirs are the directories a new agent may start in: every
// existing workspace's directory, then every immediate subdirectory of a
// project root (hidden ones skipped), each once.
func (s *Supervisor) spawnDirs(snap herdr.Snapshot) []spawnDir {
	var out []spawnDir
	seen := map[string]bool{}
	for _, p := range snap.Panes {
		path := realDir(p.Cwd)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, spawnDir{Name: filepath.Base(path), Path: path, Pane: p.PaneID})
	}
	for _, root := range s.cfg.ProjectRoots {
		r := realDir(root)
		if r == "" {
			continue
		}
		entries, err := os.ReadDir(r)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			path := realDir(filepath.Join(r, e.Name()))
			if path == "" || seen[path] || !inside(path, r) {
				continue
			}
			seen[path] = true
			out = append(out, spawnDir{Name: e.Name(), Path: path})
		}
	}
	return out
}

// resolveSpawnDir turns what the brain passed into one allowed
// directory. A path (absolute or ~/…) must resolve, symlinks followed,
// to a directory inside a project root or to an existing workspace's
// directory itself. A name matches a candidate's base name (case and
// punctuation aside) when exactly one does. Otherwise the error lists
// the candidates, for the brain to ask the CEO.
func resolveSpawnDir(want string, dirs []spawnDir, roots []string) (spawnDir, string, error) {
	want = strings.TrimSpace(want)
	if strings.HasPrefix(want, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			want = filepath.Join(home, strings.TrimPrefix(want, "~"))
		}
	}
	if filepath.IsAbs(want) {
		path := realDir(want)
		if path == "" {
			return spawnDir{}, "no such directory", fmt.Errorf("%s is not a directory; the projects are %s", want, dirNames(dirs))
		}
		for _, d := range dirs {
			if d.Path == path {
				return d, "", nil
			}
		}
		// A workspace's directory counts only as itself (matched above):
		// a shell pane sitting in the home directory must not open every
		// path below it. Only a project root allows what lies inside.
		allowed := false
		for _, r := range roots {
			if rr := realDir(r); rr != "" && inside(path, rr) {
				allowed = true
			}
		}
		if !allowed {
			return spawnDir{}, "outside project roots", fmt.Errorf("%w: %s is outside the project directories (supervisor.project_roots); the projects are %s", brain.ErrNotPermitted, want, dirNames(dirs))
		}
		return spawnDir{Name: filepath.Base(path), Path: path}, "", nil
	}
	if strings.Contains(want, "..") || strings.ContainsRune(want, filepath.Separator) {
		return spawnDir{}, "bad dir name", fmt.Errorf("give the project's name or its full path, not %q; the projects are %s", want, dirNames(dirs))
	}
	var hits []spawnDir
	for _, match := range []func(spawnDir) bool{
		func(d spawnDir) bool { return strings.EqualFold(d.Name, want) },
		func(d spawnDir) bool { return state.Slug(d.Name) != "" && state.Slug(d.Name) == state.Slug(want) },
	} {
		for _, d := range dirs {
			if match(d) {
				hits = append(hits, d)
			}
		}
		if len(hits) > 0 {
			break
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], "", nil
	case 0:
		return spawnDir{}, "unknown dir", fmt.Errorf("no project called %q; the projects are %s; ask the CEO which one", want, dirNames(dirs))
	default:
		var paths []string
		for _, d := range hits {
			paths = append(paths, d.Path)
		}
		return spawnDir{}, "ambiguous dir", fmt.Errorf("%q matches several directories: %s; ask the CEO which one", want, strings.Join(paths, ", "))
	}
}

// dirNames lists the candidates' names for an error the brain reads.
func dirNames(dirs []spawnDir) string {
	if len(dirs) == 0 {
		return "none (no workspace is open and supervisor.project_roots has no subdirectories)"
	}
	names := make([]string, 0, len(dirs))
	for _, d := range dirs {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// realDir is path made absolute with every symlink resolved, or "" when
// it is not an existing directory.
func realDir(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ""
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return ""
	}
	return real
}

// inside reports whether path is strictly below root (both resolved).
func inside(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// spawnCommand is the new agent's command line: the CLI's canonical
// executable, plus its model flag. kind is one of brain.SpawnKinds and
// model matched spawnModel, so nothing else reaches the shell.
func spawnCommand(kind, model string) string {
	cmd := kind
	if model == "" {
		return cmd
	}
	if kind == brain.SpawnCodex {
		return cmd + " -m " + model
	}
	return cmd + " --model " + model
}

// waitShell waits, up to spawnShellWait, for a new pane's shell to print
// anything (its prompt), so the command line is not typed into a shell
// still starting. A pane that cannot be read is typed into anyway.
func (s *Supervisor) waitShell(ctx context.Context, paneID string) {
	deadline := time.Now().Add(spawnShellWait)
	for {
		read, err := s.herdr.Read(ctx, paneID, herdr.SourceRecentUnwrapped, 20)
		if err != nil || strings.TrimSpace(read.Text) != "" || time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(spawnPoll / 4):
		}
	}
}

// followSpawn watches a new agent start: it waits for the watcher to put
// it in the roster, then — when there is a first instruction — for it to
// sit idle at its prompt (its trust question answered by the CEO through
// the normal pending path), and sends the instruction through the
// instruction send path (sendInstruction's menu guard included).
//
// An agent herdr has not recognised after spawnReadyWait is not given up
// on (the CEO's phone session, 2026-10-04 04:59:57: the kvoice Claude
// was dropped after 60 s with its instruction, most likely still at its
// first-run question): the brain is told, with the pane's last lines,
// that the agent is not ready and the instruction is held, and the watch
// goes on until spawnFirstWait or the pane closes. What the screen means
// is the brain's to read.
func (s *Supervisor) followSpawn(paneID, kind, text, dirName string) {
	ctx := s.life
	start := time.Now()
	ready := start.Add(spawnReadyWait)
	first := start.Add(spawnFirstWait)
	seen, late := false, false
	var lastCheck time.Time
	for {
		row, ok := s.store.Get(paneID)
		if ok && row.State != herdr.StateUnknown && row.State != "" {
			if !seen {
				seen = true
				s.log.Log(logSource, "supervisor.spawn_ready", paneID, map[string]any{"kind": kind, "state": string(row.State), "agent": aliasOr(row.Alias), "waited_s": int(time.Since(start).Seconds())})
				if late {
					// The CEO was told it had not started: correct it.
					s.pushTagged(paneID, fmt.Sprintf("The new %s%s is running now, %d s after its command was typed (herdr sees it as %s).", state.KindWord(kind), inDir(dirName), int(time.Since(start).Seconds()), row.State), voice.UrgencyIdle, map[string]any{"from": "daemon", "kind": "spawn check"})
				}
				if text == "" {
					return
				}
			}
			if row.State == herdr.StateIdle || row.State == herdr.StateDone {
				_, err := brainHost{s}.Prompt(ctx, paneID, text)
				fields := map[string]any{"kind": kind, "text": text}
				if err != nil {
					fields["error"] = err.Error()
				}
				s.log.Log(logSource, "supervisor.spawn_instruction", paneID, fields)
				if late && err == nil {
					s.tellBrain(fmt.Sprintf("the new %s (%s) is ready now; its held first instruction went to it: %q", state.KindWord(kind), s.sayOf(paneID), text))
				}
				return
			}
		}
		now := time.Now()
		switch {
		case now.After(first):
			reason := "the agent never sat idle"
			if !seen {
				reason = "herdr never recognised the agent"
			}
			s.log.Log(logSource, "supervisor.spawn_instruction_dropped", paneID, map[string]any{"kind": kind, "text": text, "reason": reason, "waited_s": int(spawnFirstWait.Seconds())})
			if text != "" {
				s.tellBrain(fmt.Sprintf("the new %s's first instruction was not sent: %s within %d minutes. The instruction was: %q", state.KindWord(kind), reason, int(spawnFirstWait.Minutes()), text))
			}
			return
		case !seen && !late && now.After(ready):
			late = true
			tail, gone := s.spawnScreen(ctx, paneID)
			s.log.Log(logSource, "supervisor.spawn_not_ready", paneID, map[string]any{"kind": kind, "waited_s": int(spawnReadyWait.Seconds()), "still_waiting": !gone, "first_instruction": text != ""})
			if gone {
				s.spawnGone(paneID, kind, text)
				return
			}
			line := fmt.Sprintf("the new %s started %d s ago is not recognised by herdr as ready yet", state.KindWord(kind), int(spawnReadyWait.Seconds()))
			if text != "" {
				line += fmt.Sprintf("; its first instruction is held until it is ready (waiting up to %d minutes)", int(spawnFirstWait.Minutes()))
			}
			s.tellBrain(line + ". The last lines of its pane:\n" + tail)
			// The CEO asked for it and must not think it is running: a
			// daemon fact for the voice, with what the pane shows.
			s.pushTagged("", spawnNotStartedLine(kind, dirName, tail), voice.UrgencyIdle, map[string]any{"from": "daemon", "kind": "spawn check"})
			lastCheck = now
		case !seen && late && now.Sub(lastCheck) >= spawnGoneCheck:
			// herdr reports no agent, so only the pane says whether it
			// still exists.
			lastCheck = now
			if _, gone := s.spawnScreen(ctx, paneID); gone {
				s.spawnGone(paneID, kind, text)
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(spawnPoll):
		}
	}
}

// spawnScreen reads the last lines of a new agent's pane; gone is true
// when herdr says the pane no longer exists.
func (s *Supervisor) spawnScreen(ctx context.Context, paneID string) (tail string, gone bool) {
	read, err := s.herdr.Read(ctx, paneID, herdr.SourceRecentUnwrapped, spawnScreenLines)
	if err != nil {
		if errors.Is(err, herdr.ErrPaneNotFound) {
			return "", true
		}
		return "(could not read the pane: " + err.Error() + ")", false
	}
	text := strings.TrimSpace(read.Text)
	if text == "" {
		return "(the pane is empty)", false
	}
	return text, false
}

// spawnGone ends a start-up watch whose pane closed before the agent was
// recognised.
func (s *Supervisor) spawnGone(paneID, kind, text string) {
	s.log.Log(logSource, "supervisor.spawn_instruction_dropped", paneID, map[string]any{"kind": kind, "text": text, "reason": "the pane closed"})
	line := fmt.Sprintf("the new %s's pane was closed before it was ready", state.KindWord(kind))
	if text != "" {
		line += fmt.Sprintf("; its first instruction was not sent: %q", text)
	}
	s.tellBrain(line)
}

// tellBrain puts a fact the daemon saw into the brain's transcript, for
// its next turn (no turn is started).
func (s *Supervisor) tellBrain(line string) {
	if s.brain != nil {
		s.brain.Daemon(line)
	}
}

// spawnNotStartedLine is the daemon's fact for the CEO about a new agent
// herdr does not see: what was started where, and the pane's last
// lines, for the voice to retell.
func spawnNotStartedLine(kind, dirName, tail string) string {
	lines := strings.Split(strings.TrimSpace(tail), "\n")
	if len(lines) > spawnTellLines {
		lines = lines[len(lines)-spawnTellLines:]
	}
	return fmt.Sprintf("The new %s%s has not started: %d s after its command was typed, herdr does not see it running. The last lines of its pane: %s", state.KindWord(kind), inDir(dirName), int(spawnReadyWait.Seconds()), strings.Join(lines, " / "))
}

// inDir is " in <dir>", or "" without a directory name.
func inDir(dirName string) string {
	if dirName == "" {
		return ""
	}
	return " in " + dirName
}

// spawnTellLines is how many pane lines the CEO's fact quotes.
const spawnTellLines = 5
