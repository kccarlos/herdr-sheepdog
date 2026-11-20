package flow

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Additional roles, permission modes and continue modes (design.md 9.1).
const (
	RoleRetro, RoleDesigner, RoleVerifier, ContinueFresh, ContinueResume, ContinueHandoff = "retro", "designer", "verifier", "fresh", "resume", "handoff"
	PermFull, PermWorktree, PermReadOnly                                                  = "full", "worktree", "read-only"
)

var rolePerm = map[string]string{RoleCoordinator: PermFull, RoleWorker: PermWorktree, RoleDesigner: PermWorktree, RoleReviewer: PermReadOnly, RoleRetro: PermReadOnly, RoleVerifier: PermReadOnly}

// Candidate is one entry in a role's ordered candidate list (design.md 9.1).
type Candidate struct {
	CLI    string `json:"cli"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// CLIFacts is what Resolve knows about one CLI: whether it is installed and
// signed in, and the models offered to this account (nil means unknown).
type CLIFacts struct {
	Installed, SignedIn bool
	Models              map[string]bool
}

// ResolveFacts holds the non-I/O world facts Resolve checks (design.md 9.1).
type ResolveFacts struct {
	CLIs   map[string]CLIFacts
	Worker Candidate // worker's (CLI, Model) for the reviewer/retro check
}

// Intent is the resolved launch intent for a role (design.md 9.1).
type Intent struct{ Role, CLI, Model, Effort, Perm, Continue string }

func modelRoles(m map[string][]Candidate) string {
	return strings.Join(slices.Sorted(maps.Keys(m)), ",")
}

// DefaultModels expresses today's engine defaults as ordered candidate lists,
// so existing projects with models unset behave identically (design.md 9.1).
func DefaultModels(s Settings) map[string][]Candidate {
	m := map[string][]Candidate{RoleCoordinator: {{"claude", "opus", "medium"}}, RoleWorker: {{"claude", "sonnet", "medium"}}, RoleReviewer: {{"claude", "opus", "medium"}, {"claude", "sonnet", "medium"}}}
	if s.SecondReviewer == "codex" {
		m[RoleReviewer][1] = Candidate{"codex", "gpt-6.2-sol", "medium"}
	}
	return m
}

// ValidateCandidates checks a role->candidates map (settings.models or
// task.request): known roles, known CLIs (claude, codex, agy), non-empty
// model, and effort (low, medium, high).
func ValidateCandidates(field string, m map[string][]Candidate) error {
	for _, role := range slices.Sorted(maps.Keys(m)) {
		if _, ok := rolePerm[role]; !ok {
			return fmt.Errorf("%s: unknown role %q", field, role)
		} else if len(m[role]) == 0 {
			return fmt.Errorf("%s.%s: must list at least one candidate", field, role)
		}
		for i, c := range m[role] {
			if c.CLI != "claude" && c.CLI != "codex" && c.CLI != "agy" {
				return fmt.Errorf("%s.%s[%d]: unknown cli %q (allowed claude, codex, agy)", field, role, i, c.CLI)
			} else if strings.TrimSpace(c.Model) == "" {
				return fmt.Errorf("%s.%s[%d]: model is required", field, role, i)
			} else if c.Effort != "low" && c.Effort != "medium" && c.Effort != "high" {
				return fmt.Errorf("%s.%s[%d]: unknown effort %q (allowed low, medium, high)", field, role, i, c.Effort)
			}
		}
	}
	return nil
}

// Resolve walks task.Request[role] then settings.Models[role] (falling back to
// DefaultModels when unset) and returns the first candidate that passes every
// check decidable from facts, or the first failing reason (design.md 9.1).
func Resolve(role string, task Task, settings Settings, facts ResolveFacts) (Intent, string) {
	perm, ok := rolePerm[role]
	if !ok {
		return Intent{}, fmt.Sprintf("unknown role %q", role)
	}
	proj := settings.Models[role]
	if len(proj) == 0 {
		proj = DefaultModels(settings)[role]
	}
	first := ""
	for _, c := range slices.Concat(task.Request[role], proj) {
		cf := facts.CLIs[c.CLI]
		if !cf.Installed {
			first = cmp.Or(first, c.CLI+" is not installed")
		} else if !cf.SignedIn {
			first = cmp.Or(first, c.CLI+" is not signed in")
		} else if cf.Models != nil && !cf.Models[c.Model] {
			first = cmp.Or(first, fmt.Sprintf("model %s is not offered to this account", c.Model))
		} else if (role == RoleReviewer || role == RoleRetro) && facts.Worker.CLI != "" && c.CLI == facts.Worker.CLI && c.Model == facts.Worker.Model {
			first = cmp.Or(first, "the reviewer must differ from the worker")
		} else {
			// Pool usability (design.md 7.3) and designer canvas capability (9.1)
			// are checked in slice 5b's pools PR and the design loop.
			return Intent{role, c.CLI, c.Model, c.Effort, perm, ContinueFresh}, ""
		}
	}
	return Intent{}, cmp.Or(first, fmt.Sprintf("no candidates for %s", role))
}
