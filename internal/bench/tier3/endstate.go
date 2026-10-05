package tier3

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

// EndStateCheck is one check of the world after the call, in the run's
// isolated herdr session and repo clones: what Done means for the
// scenario, checked against the world rather than the transcript.
type EndStateCheck struct {
	// Kind is one of the EndState kinds.
	Kind string `json:"kind"`
	// Worker is the scenario worker whose repo clone (file, git and test
	// kinds) or pane (agent kinds) is checked.
	Worker string `json:"worker,omitempty"`
	// Path is a file in the worker's clone (file kinds).
	Path string `json:"path,omitempty"`
	// Matches is a case-insensitive regexp: the file's text
	// (file_contains), the current branch (branch), or the new agent's
	// kind or alias (agent_spawned, optional).
	Matches string `json:"matches,omitempty"`
	// Argv is the test command for tests_pass (default go test ./...).
	Argv []string `json:"argv,omitempty"`
	// Why says what the check is about, for the report.
	Why string `json:"why,omitempty"`
}

// End-state kinds.
const (
	EndTestsPass    = "tests_pass"    // Argv exits 0 in the clone
	EndFileExists   = "file_exists"   // Path exists
	EndFileAbsent   = "file_absent"   // Path does not exist
	EndFileChanged  = "file_changed"  // Path differs from the clone's origin (committed or not)
	EndFileContains = "file_contains" // Path's text matches Matches
	EndBranch       = "branch"        // the clone's current branch matches Matches
	EndCommitMade   = "commit_made"   // HEAD has commits the clone's origin does not
	EndAgentClosed  = "agent_closed"  // Worker is no longer in the daemon's roster
	EndAgentPresent = "agent_present" // Worker is still in the roster
	EndAgentSpawned = "agent_spawned" // an agent that is none of the bench's workers is in the roster
)

var endKinds = map[string]bool{EndTestsPass: true, EndFileExists: true, EndFileAbsent: true, EndFileChanged: true, EndFileContains: true, EndBranch: true, EndCommitMade: true, EndAgentClosed: true, EndAgentPresent: true, EndAgentSpawned: true}

func (c EndStateCheck) validate(workers map[string]Worker) error {
	if !endKinds[c.Kind] {
		return fmt.Errorf("kind %q", c.Kind)
	}
	needWorker := c.Kind != EndAgentSpawned
	if needWorker {
		if _, ok := workers[c.Worker]; !ok {
			return fmt.Errorf("%s: unknown worker %q", c.Kind, c.Worker)
		}
	}
	switch c.Kind {
	case EndFileExists, EndFileAbsent, EndFileChanged, EndFileContains:
		if c.Path == "" || filepath.IsAbs(c.Path) || strings.HasPrefix(filepath.Clean(c.Path), "..") {
			return fmt.Errorf("%s: path %q must be relative to the clone", c.Kind, c.Path)
		}
	}
	if (c.Kind == EndFileContains || c.Kind == EndBranch) && c.Matches == "" {
		return fmt.Errorf("%s: no matches", c.Kind)
	}
	return checkRe(c.Matches)
}

func (c EndStateCheck) name() string {
	s := c.Kind
	if c.Worker != "" {
		s += " " + c.Worker
	}
	if c.Path != "" {
		s += " " + c.Path
	}
	if c.Matches != "" {
		s += " ~ " + c.Matches
	}
	return s
}

// endWorld is what the end-state checks read: each scenario worker's
// clone, the daemon's final roster, and which panes are the bench's own
// workers.
type endWorld struct {
	repo   func(worker string) string
	alias  func(worker string) string
	roster []AgentRow
	ours   map[string]bool
}

// checkEndState runs the scenario's end-state checks against the world.
func checkEndState(ctx context.Context, checks []EndStateCheck, ew endWorld) ([]northstar.EndCheck, []eval.Failure) {
	var out []northstar.EndCheck
	var fails []eval.Failure
	for _, c := range checks {
		ok, detail := runEndCheck(ctx, c, ew)
		r := northstar.EndCheck{Name: c.name(), Pass: ok, Detail: detail}
		out = append(out, r)
		if !ok {
			fails = append(fails, eval.Failure{Type: eval.FailCheck, Detail: "end state: " + r.Name + ": " + detail + whyNote(c.Why)})
		}
	}
	return out, fails
}

func runEndCheck(ctx context.Context, c EndStateCheck, ew endWorld) (bool, string) {
	repo := ""
	if c.Worker != "" && ew.repo != nil {
		repo = ew.repo(c.Worker)
	}
	path := filepath.Join(repo, c.Path)
	switch c.Kind {
	case EndTestsPass:
		argv := c.Argv
		if len(argv) == 0 {
			argv = []string{"go", "test", "./..."}
		}
		code, out := run(ctx, repo, argv...)
		return code == 0, fmt.Sprintf("%s: exit %d %s", strings.Join(argv, " "), code, oneLine(tail(out, 3), 160))
	case EndFileExists, EndFileAbsent:
		_, err := os.Stat(path)
		exists := err == nil
		if c.Kind == EndFileExists {
			return exists, fmt.Sprintf("exists: %t", exists)
		}
		return !exists, fmt.Sprintf("exists: %t", exists)
	case EndFileContains:
		b, err := os.ReadFile(path) //nolint:gosec // a path in the run's own clone
		if err != nil {
			return false, err.Error()
		}
		hit := regexp.MustCompile("(?i)" + c.Matches).Match(b)
		return hit, fmt.Sprintf("matches: %t", hit)
	case EndFileChanged:
		_, status := run(ctx, repo, "git", "status", "--porcelain", "--", c.Path)
		if strings.TrimSpace(status) != "" {
			return true, "uncommitted change: " + oneLine(status, 80)
		}
		code, _ := run(ctx, repo, "git", "diff", "--quiet", "origin/HEAD", "HEAD", "--", c.Path)
		return code == 1, fmt.Sprintf("committed change: %t", code == 1)
	case EndBranch:
		_, br := run(ctx, repo, "git", "rev-parse", "--abbrev-ref", "HEAD")
		br = strings.TrimSpace(br)
		return regexp.MustCompile("(?i)" + c.Matches).MatchString(br), "branch " + br
	case EndCommitMade:
		_, n := run(ctx, repo, "git", "rev-list", "--count", "origin/HEAD..HEAD")
		k, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return false, "git rev-list: " + oneLine(n, 80)
		}
		return k > 0, fmt.Sprintf("%d new commit(s)", k)
	case EndAgentClosed, EndAgentPresent:
		alias := c.Worker
		if ew.alias != nil {
			alias = ew.alias(c.Worker)
		}
		in := false
		for _, r := range ew.roster {
			if r.Alias == alias || r.Alias == c.Worker {
				in = true
			}
		}
		if c.Kind == EndAgentClosed {
			return !in, fmt.Sprintf("%s in the roster: %t", alias, in)
		}
		return in, fmt.Sprintf("%s in the roster: %t", alias, in)
	case EndAgentSpawned:
		var re *regexp.Regexp
		if c.Matches != "" {
			re = regexp.MustCompile("(?i)" + c.Matches)
		}
		for _, r := range ew.roster {
			if ew.ours[r.PaneID] {
				continue
			}
			if re == nil || re.MatchString(r.Kind) || re.MatchString(r.Alias) {
				return true, "new agent " + r.Alias + " (" + r.Kind + ")"
			}
		}
		return false, "no new agent in the roster"
	}
	return false, "unknown kind"
}

// run runs argv in dir (2 min cap): exit code (-1 when it did not run) and
// combined output.
func run(ctx context.Context, dir string, argv ...string) (int, string) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...) //nolint:gosec // the scenario file's own check
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return ee.ExitCode(), string(out)
	case err != nil:
		return -1, err.Error()
	}
	return 0, string(out)
}
