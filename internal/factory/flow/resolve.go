package flow

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Additional roles, permission modes and continue modes (design.md 9.1).
const (
	RoleRetro                                      = "retro"
	RoleDesigner                                   = "designer"
	RoleVerifier                                   = "verifier"
	ContinueFresh, ContinueResume, ContinueHandoff = "fresh", "resume", "handoff"
	PermFull                                       = "full"
	PermReadOnly                                   = "read-only"
)

var rolePerm = map[string]string{
	RoleCoordinator: PermFull,
	RoleWorker:      PermFull,
	RoleDesigner:    PermFull,
	RoleReviewer:    PermReadOnly,
	RoleRetro:       PermReadOnly,
	RoleVerifier:    PermReadOnly,
}

// Candidate is one entry in a role's ordered candidate list (design.md 9.1).
type Candidate struct {
	CLI    string `json:"cli"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// CLIFacts is what Resolve knows about one CLI: whether it is installed and
// signed in and the models offered to this account (nil means unknown).
// An inconclusive probe is absent from ResolveFacts.CLIs.
type CLIFacts struct {
	Installed, SignedIn bool
	Models              map[string]bool
}

// ReasonKind classifies why Resolve skipped a candidate.
type ReasonKind int

const (
	ReasonOther ReasonKind = iota
	ReasonReviewerDiffers
	ReasonPool
)

// ResolveReason is a typed rejection from a Resolve check.
type ResolveReason struct {
	Kind  ReasonKind
	Why   string
	Reset time.Time // earliest blocking reset when Kind == ReasonPool
}

// ReviewerDiffers is the typed reason when a reviewer candidate matches the worker.
var ReviewerDiffers = ResolveReason{Kind: ReasonReviewerDiffers, Why: "the reviewer must differ from the worker"}

// ResolveFacts holds the non-I/O world facts Resolve checks (design.md 9.1).
type ResolveFacts struct {
	CLIs   map[string]CLIFacts
	Worker Candidate                     // worker's (CLI, Model) for the reviewer/retro check
	Check  func(Candidate) ResolveReason // additional role capability or launch checks
	Pool   func(Candidate) ResolveReason // pool usability check (design.md 7.3, 9.1)
	Avoid  []Candidate                   // models already used in this round sequence
}

var knownCLIs = []string{"claude", "codex", "agy"}

// Intent is the resolved launch intent for a role (design.md 9.1).
type Intent struct{ Role, CLI, Model, Effort, Perm, Continue string }

// DefaultModels expresses today's engine defaults as ordered candidate lists,
// so existing projects with models unset behave identically (design.md 9.1).
func DefaultModels(s Settings) map[string][]Candidate {
	m := map[string][]Candidate{
		RoleCoordinator: {{"claude", "opus", "medium"}},
		RoleWorker:      {{"claude", "sonnet", "medium"}},
		RoleReviewer:    {{"claude", "opus", "medium"}, {"claude", "sonnet", "medium"}},
	}
	if s.SecondReviewer == "codex" {
		m[RoleReviewer][1] = Candidate{"codex", "gpt-6.2-sol", "medium"}
	}
	return m
}

// ValidateCandidates trims and checks a role->candidates map (settings.models
// or task.request): known roles (no coordinator on task.request), known CLIs
// (claude, codex, agy), non-empty model, and effort (low, medium, high).
func ValidateCandidates(field string, m map[string][]Candidate) error {
	for _, role := range slices.Sorted(maps.Keys(m)) {
		if _, ok := rolePerm[role]; !ok {
			return fmt.Errorf("%s: unknown role %q", field, role)
		} else if field == "request" && role == RoleCoordinator {
			return fmt.Errorf("%s: role %q is not allowed on a task", field, role)
		} else if len(m[role]) == 0 {
			return fmt.Errorf("%s.%s: must list at least one candidate", field, role)
		}
		for i := range m[role] {
			c := &m[role][i]
			c.CLI, c.Model, c.Effort = strings.TrimSpace(c.CLI), strings.TrimSpace(c.Model), strings.TrimSpace(c.Effort)
			if !slices.Contains(knownCLIs, c.CLI) {
				return fmt.Errorf("%s.%s[%d]: unknown cli %q (allowed %s)", field, role, i, c.CLI, strings.Join(knownCLIs, ", "))
			} else if c.Model == "" {
				return fmt.Errorf("%s.%s[%d]: model is required", field, role, i)
			} else if c.Effort != "low" && c.Effort != "medium" && c.Effort != "high" {
				return fmt.Errorf("%s.%s[%d]: unknown effort %q (allowed low, medium, high)", field, role, i, c.Effort)
			}
		}
	}
	return nil
}

// SameModel identifies a model by its CLI and model name, independent of effort.
func (c Candidate) SameModel(other Candidate) bool {
	return c.CLI != "" && c.CLI == other.CLI && c.Model == other.Model
}

// Candidates returns the effective order, with legacy defaults for unset roles.
func Candidates(role string, task Task, settings Settings) []Candidate {
	project := settings.Models[role]
	if len(project) == 0 {
		project = DefaultModels(settings)[role]
	}
	return slices.Concat(task.Request[role], project)
}

// Resolve walks the effective list and returns the first passing candidate or
// the highest-priority failing reason (design.md 9.1).
func Resolve(role string, task Task, settings Settings, facts ResolveFacts) (Intent, string) {
	in, r := ResolveWithReason(role, task, settings, facts)
	return in, r.Why
}

// ResolveWithReason is Resolve returning the typed rejection reason.
func ResolveWithReason(role string, task Task, settings Settings, facts ResolveFacts) (Intent, ResolveReason) {
	perm, ok := rolePerm[role]
	if !ok {
		return Intent{}, ResolveReason{Why: fmt.Sprintf("unknown role %q", role)}
	}
	var pool, first, fallback ResolveReason
	setFirst := func(dst *ResolveReason, r ResolveReason) {
		if dst.Why == "" {
			*dst = r
		}
	}
	for _, c := range Candidates(role, task, settings) {
		if slices.ContainsFunc(facts.Avoid, c.SameModel) {
			continue
		}
		cf, probed := facts.CLIs[c.CLI]
		var r ResolveReason
		if !slices.Contains(knownCLIs, c.CLI) {
			r = ResolveReason{Why: fmt.Sprintf("unknown cli %q", c.CLI)}
		} else if facts.Check != nil {
			r = facts.Check(c)
		}
		switch {
		case r.Why != "" && r.Kind == ReasonReviewerDiffers:
			setFirst(&fallback, r)
		case r.Why != "":
			setFirst(&first, r)
		case probed && !cf.Installed:
			setFirst(&first, ResolveReason{Why: c.CLI + " is not installed"})
		case probed && !cf.SignedIn:
			setFirst(&first, ResolveReason{Why: c.CLI + " is not signed in"})
		case cf.Models != nil && !cf.Models[c.Model]:
			setFirst(&first, ResolveReason{Why: fmt.Sprintf("model %s is not offered to this account", c.Model)})
		case (role == RoleReviewer || role == RoleRetro) && c.SameModel(facts.Worker):
			setFirst(&fallback, ReviewerDiffers)
		default:
			if facts.Pool != nil {
				if pr := facts.Pool(c); pr.Why != "" {
					if pool.Why == "" || (!pr.Reset.IsZero() && (pool.Reset.IsZero() || pr.Reset.Before(pool.Reset))) {
						pool = pr
					}
					continue
				}
			}
			return Intent{role, c.CLI, c.Model, c.Effort, perm, ContinueFresh}, ResolveReason{}
		}
	}
	if pool.Why != "" && first.Why != "" {
		return Intent{}, ResolveReason{Kind: ReasonOther, Why: pool.Why + "; " + first.Why, Reset: pool.Reset}
	}
	for _, r := range []ResolveReason{pool, first, fallback} {
		if r.Why != "" {
			return Intent{}, r
		}
	}
	return Intent{}, ResolveReason{Why: fmt.Sprintf("no candidates for %s", role)}
}
