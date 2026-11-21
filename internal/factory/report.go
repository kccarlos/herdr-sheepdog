package factory

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

// Refusal is a report or command the acceptance table turned down
// (design.md 7.2): a code and one plain sentence. Nothing changed.
type Refusal struct{ Code, Why string }

func (r *Refusal) Error() string { return r.Code + ": " + r.Why }

// owner marks the verbs only the owner may send, from a terminal with no
// run's token.
const owner = "owner"

// verbRoles is the acceptance table: who may send each verb.
var verbRoles = map[string]string{
	"start": owner, "stop": owner,
	"plan apply": flow.RoleCoordinator, "recap decide": flow.RoleCoordinator,
	"plan amend": flow.RoleCoordinator, "task": flow.RoleCoordinator,
	"ready": flow.RoleWorker,
}

// Report is the acceptance step for everything a run's terminal sends: the
// token names the run, and through it the role and the project.
func (e *Engine) Report(token, verb string, args json.RawMessage) error {
	var facts flow.ResolveFacts
	if clis := e.planProbeCLIs(token, verb, args); len(clis) > 0 {
		facts = planResolveFacts(context.Background(), clis)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	want, ok := verbRoles[verb]
	if !ok {
		return e.refuse("", "kind_not_allowed", fmt.Sprintf("%q is not a report the factory knows.", verb))
	}
	id, s, role, ti := e.runByTokenLocked(token)
	if want == owner {
		return e.refuse(id, "kind_not_allowed", fmt.Sprintf("A run's terminal cannot %s the factory; the owner runs it from a terminal with no run's token.", verb))
	}
	if id == "" {
		return e.refuse("", "invalid_token", "The token matches no run the factory started.")
	}
	if role != want {
		return e.refuse(id, "kind_not_allowed", fmt.Sprintf("A %s may not send %s; only the %s may.", role, verb, want))
	}
	if ti >= 0 && flow.Ended(s.Tasks[ti]) {
		return e.refuse(id, "already_closed", fmt.Sprintf("Task %q has ended; it takes no more reports.", s.Tasks[ti].ID))
	}
	if verb == "ready" {
		return e.acceptReadyLocked(id, &s, role, ti, args)
	}
	if verb == "recap decide" {
		return e.acceptRecapDecideLocked(id, &s, role, args, facts)
	}
	if verb == "task" {
		return e.acceptTaskLocked(id, &s, role, args)
	}
	if verb == "plan amend" {
		return e.acceptAmendLocked(id, &s, role, args, facts)
	}
	if len(s.Tasks) > 0 {
		return e.refuse(id, "plan_exists", "A plan already exists; change it with plan amend.")
	}
	tasks, fails := checkPlanWith(args, nil, "", s.Settings, facts)
	if len(fails) > 0 {
		return e.refuse(id, "plan_invalid", strings.Join(fails, "; ")+".")
	}
	s.Tasks = tasks
	titles := make([]string, len(tasks))
	for i, t := range tasks {
		titles[i] = t.ID + " " + t.Title
	}
	return e.commitLocked(id, &s, LogLine{By: role, Input: map[string]any{"verb": verb, "tasks": tasks}, Rule: "plan apply",
		Decision: "plan applied", Why: fmt.Sprintf("%d task(s): %s", len(tasks), strings.Join(titles, ", "))})
}

func (e *Engine) acceptReadyLocked(id string, s *flow.State, role string, ti int, args json.RawMessage) error {
	var req struct {
		Head    string       `json:"head"`
		Reject  *string      `json:"reject"`
		Nothing *string      `json:"nothing_to_change"`
		Proofs  []flow.Proof `json:"proofs"`
		Touched *string      `json:"touched"`
		Skipped *string      `json:"skipped"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if len(bytes.TrimSpace(args)) == 0 || dec.Decode(&req) != nil || strings.TrimSpace(req.Head) == "" {
		return e.refuse(id, "invalid_report", "A ready report must carry the worktree head commit.")
	}
	t := &s.Tasks[ti]
	a := &t.Attempts[len(t.Attempts)-1]
	h := flow.H(*a)
	ctx, cancel := context.WithTimeout(world.GitRepository(context.Background(), s.Project.Folder), lockGitTimeout)
	defer cancel()
	tip, rel, clean, err := e.git.InspectWorktree(ctx, a.Worktree, h)
	if err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(req.Head) != tip:
		return e.refuse(id, "invalid_report", "Reported head does not match the task worktree tip; run report ready from the worktree.")
	case !clean:
		return e.refuse(id, "dirty_tree", "The worktree has uncommitted changes; commit or discard them before reporting ready.")
	case req.Reject != nil:
		why, err := e.cleanWhy(id, *req.Reject, "reject report", "--reject")
		if err != nil {
			return err
		}
		r := flow.RejectableRound(*a, h)
		if rel != flow.RelSame || r == nil || flow.Ladder(flow.In{Task: *t, Settings: s.Settings}) != flow.Fix {
			if rel == flow.RelSame && flow.Ladder(flow.In{Task: *t, Settings: s.Settings}) == flow.RecapAnswer {
				return e.refuse(id, "nothing_to_reject", "Nothing to reject: a recap is open and the coordinator decides; there is nothing for you to do.")
			}
			return e.refuse(id, "nothing_to_reject", "Nothing to reject: the newest round covering the current head must be an unrejected reviewer changes verdict with no new commit (after a commit, run report ready without --reject).")
		}
		a.Rejects = append(a.Rejects, flow.Reject{Head: h, Round: r.N, Why: why})
		return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "ready", "head": h, "round": r.N, "reject": why},
			Rule: "ready --reject", Decision: "rejected", Why: fmt.Sprintf("round %d at %s: %s", r.N, h, why)})
	case req.Nothing != nil:
		why, err := e.cleanWhy(id, *req.Nothing, "nothing-to-change report", "--why")
		if err != nil {
			return err
		}
		if tip != a.BaseCommit || rel != flow.RelSame { // at the base, and no ready moved H past it
			return e.refuse(id, "nothing_new", "The worktree has commits over the base; run report ready without --nothing-to-change.")
		}
		a.NothingToChange = why
		return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "ready", "nothing_to_change": why},
			Rule: "ready --nothing-to-change", Decision: "nothing to change", Why: why})
	case rel == flow.RelBehind || rel == flow.RelDiverged:
		return e.refuse(id, "pull_first", "The attempt head moved past the worktree tip; pull first.")
	case rel == flow.RelSame:
		return e.refuse(id, "nothing_new", "The worktree has no commit ahead of the current head.")
	}
	var touched string
	var skipped *string
	if req.Touched != nil {
		if touched = flow.CleanText(*req.Touched); utf8.RuneCountInString(touched) > maxRejectWhyRunes {
			return e.refuse(id, "invalid_report", fmt.Sprintf("--touched is longer than %d characters.", maxRejectWhyRunes))
		}
	}
	if req.Skipped != nil {
		s := flow.CleanText(*req.Skipped)
		if utf8.RuneCountInString(s) > maxRejectWhyRunes {
			return e.refuse(id, "invalid_report", fmt.Sprintf("--skipped is longer than %d characters.", maxRejectWhyRunes))
		}
		skipped = &s
	}
	var proofs []flow.Proof
	if needsProofs(t.Outcomes) || len(req.Proofs) > 0 {
		var code, why string
		proofs, code, why, err = e.checkProofs(ctx, t.Outcomes, req.Proofs, a.Worktree)
		if err != nil {
			return err
		}
		if code != "" {
			return e.refuse(id, code, why)
		}
		if touched == "" {
			return e.refuse(id, "invalid_report", `A ready report with proofs must list what the change touched (--touched "<places>").`)
		}
		if req.Skipped == nil {
			return e.refuse(id, "invalid_report", `A ready report with proofs must state what was skipped or not checked (--skipped "<what was left out>", or --skipped "" when nothing was skipped).`)
		}
	}
	mctx, mcancel := context.WithTimeout(world.GitRepository(context.Background(), s.Project.Folder), lockGitTimeout)
	defer mcancel()
	if mo, err := e.git.MergeOnly(mctx, a.Worktree, h, tip); err != nil {
		return err
	} else if mo { // a ready at a merge-only tip is N20's move too: no round
		a.MergeOnly = append(a.MergeOnly, flow.Advance{From: h, To: tip})
	}
	a.Ready, a.NothingToChange = append(a.Ready, flow.Ready{Head: tip, Proofs: proofs, Touched: touched, Skipped: skipped}), ""
	return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "ready", "head": tip, "proofs": proofs, "touched": touched, "skipped": skipped}, Rule: "ready", Decision: "ready", Why: tip})
}

func needsProofs(outcomes []flow.Outcome) bool {
	return slices.ContainsFunc(outcomes, func(o flow.Outcome) bool { return o.ProvedBy != "" })
}

func (e *Engine) checkProofs(ctx context.Context, outcomes []flow.Outcome, raw []flow.Proof, worktree string) ([]flow.Proof, string, string, error) {
	want, seen, out := make(map[string]flow.Outcome, len(outcomes)), make(map[string]bool, len(outcomes)), make([]flow.Proof, 0, len(raw))
	for _, o := range outcomes {
		want[o.Text] = o
	}
	var failed, missing []string
	for _, p := range raw {
		name := flow.CleanText(p.Outcome)
		o, ok := want[name]
		p.Outcome, p.Command, p.Artifact, p.OutputPath = name, strings.TrimSpace(p.Command), strings.TrimSpace(p.Artifact), strings.TrimSpace(p.OutputPath)
		manual := strings.HasPrefix(o.ProvedBy, "manual:")
		switch {
		case !ok:
			return nil, "invalid_report", fmt.Sprintf("Proof names unknown outcome %q.", name), nil
		case seen[name]:
			return nil, "invalid_report", fmt.Sprintf("Duplicate proof for outcome %q.", name), nil
		case (p.Command == "") == (p.Artifact == ""):
			return nil, "invalid_report", fmt.Sprintf("Proof for %q must carry either a command or an artifact.", name), nil
		case utf8.RuneCountInString(p.Command) > maxRejectWhyRunes || utf8.RuneCountInString(p.OutputPath) > maxRejectWhyRunes || utf8.RuneCountInString(p.Artifact) > maxRejectWhyRunes:
			return nil, "invalid_report", fmt.Sprintf("Proof for %q has a field longer than %d characters.", name, maxRejectWhyRunes), nil
		case (p.Artifact != "") != manual:
			return nil, "invalid_report", fmt.Sprintf("Outcome %q proof kind does not match proved_by (%s).", name, o.ProvedBy), nil
		case p.Command != "" && p.OutputPath == "":
			return nil, "invalid_report", fmt.Sprintf("Proof for %q has no output path.", name), nil
		}
		seen[name] = true
		if p.Artifact != "" {
			path := p.Artifact
			if !filepath.IsAbs(path) {
				path = filepath.Join(worktree, path)
			}
			exists, err := e.git.PathExists(ctx, worktree, path)
			if err != nil {
				return nil, "", "", err
			}
			if !exists {
				failed = append(failed, fmt.Sprintf("%q: artifact %q does not exist", name, p.Artifact))
			}
		} else if p.Exit != 0 {
			output, err := e.proofHead(ctx, p.OutputPath, worktree)
			if err != nil {
				return nil, "", "", err
			}
			failed = append(failed, fmt.Sprintf("%q (%s, exit %d): %s", name, flow.CleanText(p.Command), p.Exit, output))
		}
		out = append(out, p)
	}
	for _, o := range outcomes {
		if !seen[o.Text] {
			missing = append(missing, fmt.Sprintf("%q", o.Text))
		}
	}
	if len(missing) > 0 {
		return nil, "outcome_missing", "Missing proof for outcome(s): " + strings.Join(missing, ", ") + ".", nil
	}
	if len(failed) > 0 {
		return nil, "proof_failed", "Proof(s) failed: " + strings.Join(failed, "; ") + ".", nil
	}
	return out, "", "", nil
}

func (e *Engine) proofHead(ctx context.Context, path, worktree string) (string, error) {
	buf, err := e.git.ProofOutput(ctx, worktree, path)
	if errors.Is(err, world.ErrProofOutputRejected) {
		return "(output unreadable)", nil
	}
	if err != nil {
		return "", err
	}
	var lines []string
	for l := range strings.Lines(string(buf)) {
		if s := flow.CleanText(l); s != "" && len(lines) < 5 {
			lines = append(lines, s)
		}
	}
	return flow.CapRunes(cmp.Or(strings.Join(lines, " | "), "(no output)"), maxRejectWhyRunes), nil
}

const maxRejectWhyRunes = 500

// cleanWhy cleans a free-text reason and refuses it when empty or over the cap.
func (e *Engine) cleanWhy(id, raw, what, flag string) (string, error) {
	why := flow.CleanText(raw)
	if why == "" {
		return "", e.refuse(id, "why_missing", fmt.Sprintf("A %s must carry a non-empty reason (%s \"<why>\").", what, flag))
	}
	if utf8.RuneCountInString(why) > maxRejectWhyRunes {
		return "", e.refuse(id, "invalid_report", "the reason is longer than 500 characters; put details in a file and name it")
	}
	return why, nil
}

type replyLines struct {
	majors    int
	misses    int
	hasMisses bool
	badMisses bool
	class     string
	deletable string
	notRun    string
}

func unwrapReply(s string) string {
	for {
		t := strings.TrimSpace(s)
		switch {
		case len(t) >= 4 && strings.HasPrefix(t, "**") && strings.HasSuffix(t, "**"):
			t = t[2 : len(t)-2]
		case len(t) >= 2 && strings.HasPrefix(t, "`") && strings.HasSuffix(t, "`"):
			t = t[1 : len(t)-1]
		case strings.HasSuffix(t, "."):
			t = t[:len(t)-1]
		default:
			return t
		}
		s = t
	}
}

func parseReplyLine(l string) (key, val string, ok bool) {
	s := strings.TrimSpace(l)
	for {
		prev := s
		s = strings.TrimLeft(s, " \t⏺•◦▪‣·-")
		if rest, cut := strings.CutPrefix(s, "*"); cut && strings.HasPrefix(rest, " ") {
			s = strings.TrimLeft(rest, " \t")
		}
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i > 0 && i < len(s) && s[i] == '.' {
			s = strings.TrimLeft(s[i+1:], " \t")
		}
		if s == prev {
			break
		}
	}
	s = unwrapReply(s)
	k, v, ok := strings.Cut(s, ":")
	if !ok {
		return "", "", false
	}
	if strings.HasPrefix(k, "**") {
		v = strings.TrimPrefix(v, "**")
	} else if strings.HasPrefix(k, "`") {
		v = strings.TrimPrefix(v, "`")
	}
	key = strings.ToLower(strings.Trim(k, "*` \t"))
	val = unwrapReply(flow.CleanText(v))
	if strings.Count(val, "`")%2 == 1 && strings.HasSuffix(val, "`") {
		val = unwrapReply(val[:len(val)-1])
	}
	return key, val, key != ""
}

func parseMisses(val string) (int, bool) {
	low := strings.ToLower(val)
	if low == "none" || strings.HasPrefix(low, "none ") || strings.HasPrefix(low, "none(") {
		return 0, true
	}
	i := 0
	for i < len(val) && val[i] >= '0' && val[i] <= '9' {
		i++
	}
	if i == 0 || (i < len(val) && (unicode.IsLetter(rune(val[i])) || val[i] == '_')) {
		return 0, false
	}
	n, err := strconv.Atoi(val[:i])
	return n, err == nil
}

func parseReplyLines(body string) replyLines {
	var rl replyLines
	inTool, afterCall := false, false
	for l := range strings.Lines(body) {
		trimmed := strings.TrimLeft(l, " \t")
		top, blank := l == trimmed, strings.TrimSpace(l) == ""
		// ⎿ (Claude) opens a tool block; "└ " (Codex) only under a tool call
		// line and its wrapped or │ continuation lines. A table's "└─" never does.
		if strings.HasPrefix(trimmed, "⎿") || (afterCall && strings.HasPrefix(trimmed, "└ ")) {
			inTool, afterCall = true, false
			continue
		}
		// Only an unindented ⏺ or • is a tool call line; one inside output is output.
		afterCall = (top && (strings.HasPrefix(l, "⏺") || strings.HasPrefix(l, "•"))) || (afterCall && !top && !blank)
		if inTool {
			if blank || top {
				inTool = false
			} else {
				continue
			}
		}
		key, val, ok := parseReplyLine(l)
		if !ok {
			continue
		}
		switch key {
		case "major":
			rl.majors++
		case "self-check misses":
			rl.hasMisses = true
			n, valid := parseMisses(val)
			rl.badMisses = !valid
			if valid {
				rl.misses = n
			}
		case "class":
			rl.class = flow.CapRunes(val, maxRejectWhyRunes)
		case "deletable":
			rl.deletable = flow.CapRunes(val, maxRejectWhyRunes)
		case "not run":
			rl.notRun = flow.CapRunes(val, maxRejectWhyRunes)
		}
	}
	return rl
}

// acceptVerdictLocked checks a reviewer's terminal <report> tag against the
// acceptance table (design.md 7.2). Valid: writes findings to reports/, stops
// the reviewer, and records the round. Invalid: refuses with invalid_report,
// logged once per distinct output on that run, leaving the round unused.
func (e *Engine) acceptVerdictLocked(ctx context.Context, id string, s *flow.State, ti int, rd world.ReportRead) error {
	t := &s.Tasks[ti]
	a := &t.Attempts[len(t.Attempts)-1]
	rev := a.Review
	if rev == nil {
		return e.refuse(id, "invalid_report", "No review round is active for this task.")
	}
	rep, why := rd.Report, ""
	rl := parseReplyLines(rd.Body)
	switch {
	case rd.Status != world.ReportFound || rep.Code == "" || rep.Code != rev.Code:
		why = "The report tag is malformed or does not carry this round's one-time code."
	case rep.Round != rev.Round || rep.Head != rev.Head || !flow.Covers(*a, rev.Head, flow.H(*a)) || rev.Round != len(a.Rounds)+1:
		why = "The report tag does not match this review run's round and commit."
	case (rep.Verdict != flow.VerdictPass || rep.Majors != 0) && (rep.Verdict != flow.VerdictChanges || rep.Majors < 1):
		why = "Verdict must be pass with 0 majors or changes with at least 1 major."
	case rep.Majors != rl.majors:
		why = fmt.Sprintf("The tag says %d major(s) but %d findings line(s) start with major:.", rep.Majors, rl.majors)
	case !rl.hasMisses:
		why = "The reply must include a self-check misses: <n> line."
	case rl.badMisses:
		why = "The self-check misses: line must carry a non-negative integer."
	case rl.deletable == "":
		why = "The reply must include a deletable: <text or none> line."
	}
	errKey := "verdict " + id + " " + t.ID + " " + rev.RunID
	if why != "" {
		if sig := rd.Raw + "\x00" + why; rev.RunID == "" || e.errs[errKey] != sig {
			if rev.RunID != "" {
				if e.errs == nil {
					e.errs = map[string]string{}
				}
				e.errs[errKey] = sig
			}
			return e.refuse(id, "invalid_report", why)
		}
		return &Refusal{Code: "invalid_report", Why: why}
	}
	delete(e.errs, errKey)
	path := e.Paths.Report(id, t.ID, rev.Round)
	body := fmt.Sprintf("# Review round %d: %s (%d major(s))\n\nTask: %s — %s\nCommit: %s\n\n## Findings\n\n%s\n",
		rev.Round, rep.Verdict, rep.Majors, t.ID, t.Title, rev.Head, cmp.Or(rd.Body, "(none)"))
	if err := writeAtomic(path, []byte(body)); err != nil {
		return err
	}
	if err := world.StopRun(ctx, e.HC, rev.RunID); err != nil {
		return err
	}
	misses := rl.misses
	rnd := flow.Round{
		N: rev.Round, Kind: cmp.Or(rev.Kind, flow.RoundFull), Head: rev.Head, Reviewer: rev.Reviewer,
		Verdict: rep.Verdict, Majors: rep.Majors, SelfCheckMisses: &misses,
		Class: rl.class, Deletable: rl.deletable, NotRun: rl.notRun, ReportPath: path,
	}
	a.Rounds, a.Review = append(a.Rounds, rnd), nil
	return e.commitLocked(id, s, LogLine{
		By: flow.RoleReviewer, Task: t.ID,
		Input: map[string]any{
			"verb": "verdict", "round": rnd.N, "kind": rnd.Kind, "model": rnd.Reviewer.Model,
			"head": rnd.Head, "verdict": rnd.Verdict, "majors": rnd.Majors,
			"self_check_misses": misses,
		},
		Rule: "verdict", Decision: rnd.Verdict,
		Why: fmt.Sprintf("round %d (%s, %s) at %s: %s (%d major(s), %d self-check miss(es))", rnd.N, rnd.Kind, rnd.Reviewer.Model, rnd.Head, rnd.Verdict, rnd.Majors, misses),
	})
}

func (e *Engine) refuse(project, code, why string) error {
	e.logEvent("factory.refused", map[string]string{"project": project, "code": code, "why": why})
	return &Refusal{Code: code, Why: why}
}

// tokenHash is all state.json keeps of a run token: its SHA-256, hex.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// runByTokenLocked finds the run holding token: a project's coordinator or a
// task's open attempt worker.
func (e *Engine) runByTokenLocked(token string) (string, flow.State, string, int) {
	h := tokenHash(token)
	same := func(t string) bool {
		return token != "" && t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(h)) == 1
	}
	for _, ent := range e.projectDirs() {
		s, err := LoadState(e.Paths.State(ent.Name()))
		if err != nil {
			continue
		}
		if same(s.Project.RunTokenHash) {
			return ent.Name(), s, flow.RoleCoordinator, -1
		}
		for ti, t := range s.Tasks {
			if n := len(t.Attempts); n > 0 {
				if a := t.Attempts[n-1]; same(a.RunTokenHash) {
					return ent.Name(), s, flow.RoleWorker, ti
				} else if a.Review != nil && same(a.Review.RunTokenHash) {
					return ent.Name(), s, flow.RoleReviewer, ti
				}
			}
		}
	}
	return "", flow.State{}, "", -1
}

type planOutcome flow.Outcome

func (o *planOutcome) UnmarshalJSON(data []byte) error {
	*o = planOutcome{}
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &o.Text)
	}
	type alias planOutcome
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode((*alias)(o))
}

// planTask is the part of design.md 3's plan record that slice 3 accepts.
type planTask struct {
	ID        string                      `json:"id"`
	Title     string                      `json:"title"`
	Brief     string                      `json:"brief"`
	ForWhom   string                      `json:"for_whom"`
	IfSkipped string                      `json:"if_skipped"`
	Smallest  string                      `json:"smallest"`
	NotDoing  string                      `json:"not_doing"`
	Outcomes  []planOutcome               `json:"outcomes"`
	After     []string                    `json:"after"`
	Priority  string                      `json:"priority"`
	Estimate  int                         `json:"estimate_lines"`
	Request   map[string][]flow.Candidate `json:"request"`
}

var conventionalTitleRE = regexp.MustCompile(`^(feat|fix|docs|refactor|test|chore|perf|ci|build|style|revert)(\([A-Za-z0-9._/-]+\))?!?: \S.*$`)

// checkTitle is design.md 11.7's Title check: a Conventional Commit subject.
func checkTitle(id, raw string) []string {
	title := flow.CleanText(raw)
	if title == "" {
		return []string{fmt.Sprintf("shape: task %q has no title", id)}
	}
	if !conventionalTitleRE.MatchString(title) {
		return []string{fmt.Sprintf("title: task %q title %q must be a Conventional Commit subject (type(scope)?: subject)", id, title)}
	}
	return nil
}

// checkScope is design.md 11.7's Scope check (prompts.md 3.1): all four
// scope fields must be non-empty after cleaning.
func checkScope(t planTask) []string {
	var fails []string
	for _, f := range []struct{ name, val string }{
		{"for_whom", t.ForWhom},
		{"if_skipped", t.IfSkipped},
		{"smallest", t.Smallest},
		{"not_doing", t.NotDoing},
	} {
		if flow.CleanText(f.val) == "" {
			fails = append(fails, fmt.Sprintf("scope: task %q needs %s", t.ID, f.name))
		}
	}
	return fails
}

// checkOutcomes is design.md 11.7's Outcomes check (prompts.md 3.1): at least
// one outcome per task, each with non-empty unique text and a proved_by of kind
// cmd:, test:, or manual:, both capped at maxRejectWhyRunes.
func checkOutcomes(id string, raw []planOutcome) ([]flow.Outcome, []string) {
	if len(raw) == 0 {
		return nil, []string{fmt.Sprintf("outcomes: task %q needs at least one outcome", id)}
	}
	out := make([]flow.Outcome, len(raw))
	seen := make(map[string]bool, len(raw))
	var fails []string
	for i, o := range raw {
		text, pb := flow.CleanText(o.Text), flow.CleanText(o.ProvedBy)
		label := fmt.Sprintf("outcome %q", text)
		if text == "" {
			label = fmt.Sprintf("outcome %d", i+1)
			fails = append(fails, fmt.Sprintf("outcomes: task %q %s needs text", id, label))
		} else if strings.ContainsAny(o.Text, "=\n\r") {
			fails = append(fails, fmt.Sprintf("outcomes: task %q %s text cannot contain '=' or a newline", id, label))
		} else if utf8.RuneCountInString(text) > maxRejectWhyRunes {
			label = fmt.Sprintf("outcome %d", i+1)
			fails = append(fails, fmt.Sprintf("outcomes: task %q %s text is longer than %d characters", id, label, maxRejectWhyRunes))
		} else if seen[text] {
			fails = append(fails, fmt.Sprintf("outcomes: task %q has duplicate outcome %q", id, text))
		} else {
			seen[text] = true
		}
		if pb == "" {
			fails = append(fails, fmt.Sprintf("outcomes: task %q %s needs proved_by (cmd:, test:, or manual:)", id, label))
		} else if utf8.RuneCountInString(pb) > maxRejectWhyRunes {
			fails = append(fails, fmt.Sprintf("outcomes: task %q %s proved_by is longer than %d characters", id, label, maxRejectWhyRunes))
		} else if kind, rest, ok := strings.Cut(pb, ":"); !ok || (kind != "cmd" && kind != "test" && kind != "manual") {
			fails = append(fails, fmt.Sprintf("outcomes: task %q %s has unknown proved_by %q (use cmd:, test:, or manual:)", id, label, pb))
		} else if rest = strings.TrimSpace(rest); rest == "" {
			fails = append(fails, fmt.Sprintf("outcomes: task %q %s proved_by %q needs a value after %s:", id, label, pb, kind))
		} else {
			pb = kind + ": " + rest
		}
		out[i] = flow.Outcome{Text: text, ProvedBy: pb}
	}
	return out, fails
}

type planFile struct {
	Tasks []planTask `json:"tasks"`
}

// checkPriority validates the optional plan priority field (design.md 3, 4.10).
func checkPriority(id, raw string) (string, []string) {
	p := strings.TrimSpace(raw)
	if p != "" && p != flow.PriorityUrgent {
		return "", []string{fmt.Sprintf("shape: task %q has unknown priority %q (use %q or omit)", id, p, flow.PriorityUrgent)}
	}
	return p, nil
}

// checkEstimate is design.md 11.5's plan check: estimate_lines is required,
// and a task over task_size_limit is split before it exists.
func checkEstimate(id string, est, limit int) []string {
	if est <= 0 {
		return []string{fmt.Sprintf("shape: task %q needs estimate_lines above 0", id)}
	}
	if est > limit {
		return []string{fmt.Sprintf("size: task %q estimates %d lines, over task_size_limit %d: split it before it exists", id, est, limit)}
	}
	return nil
}

var validTaskID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

func checkPlanWith(raw json.RawMessage, existing []flow.Task, parentID string, s flow.Settings, facts flow.ResolveFacts) ([]flow.Task, []string) {
	var p planFile
	var fails []string
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		fails = append(fails, "shape: "+strings.TrimPrefix(err.Error(), "json: "))
		p = planFile{} // the strict decode may have stopped half way
		if json.Unmarshal(raw, &p) != nil {
			return nil, fails
		}
	}
	if len(p.Tasks) == 0 {
		fails = append(fails, "shape: the plan has no tasks")
	}
	existingIDs := map[string]bool{}
	for _, t := range existing {
		existingIDs[t.ID] = true
	}
	ids := map[string]bool{}
	var newIDs []string
	outcomes := make([][]flow.Outcome, len(p.Tasks))
	for i := range p.Tasks {
		t := &p.Tasks[i]
		t.ID = strings.TrimSpace(t.ID)
		for j := range t.After {
			t.After[j] = strings.TrimSpace(t.After[j])
		}
		if t.ID == "" || ids[t.ID] || existingIDs[t.ID] {
			fails = append(fails, fmt.Sprintf("shape: task %d needs an id no other task has", i+1))
		} else if !validTaskID.MatchString(t.ID) {
			fails = append(fails, fmt.Sprintf("shape: task %q id must be 1-32 letters, digits, -, _ or .", t.ID))
		} else {
			ids[t.ID], newIDs = true, append(newIDs, t.ID)
		}
		fails = append(fails, checkTitle(t.ID, t.Title)...)
		fails = append(fails, checkScope(*t)...)
		var oFails, pFails []string
		outcomes[i], oFails = checkOutcomes(t.ID, t.Outcomes)
		fails = append(fails, oFails...)
		t.Priority, pFails = checkPriority(t.ID, t.Priority)
		fails = append(fails, pFails...)
		fails = append(fails, checkEstimate(t.ID, t.Estimate, s.TaskSizeLimit)...)
		if err := flow.ValidateCandidates("request", t.Request); err != nil {
			fails = append(fails, fmt.Sprintf("shape: task %q %s", t.ID, err))
		}
	}
	after := map[string][]string{}
	for _, et := range existing {
		if et.ID != parentID {
			after[et.ID] = replaceDep(et.After, parentID, newIDs)
		}
	}
	tasks := make([]flow.Task, len(p.Tasks))
	for i, t := range p.Tasks {
		for _, a := range t.After {
			if parentID != "" && a == parentID {
				fails = append(fails, fmt.Sprintf("shape: task %q is after %q, which is being split", t.ID, a))
			} else if !ids[a] && !existingIDs[a] {
				fails = append(fails, fmt.Sprintf("shape: task %q is after %q, which is not in the plan", t.ID, a))
			}
		}
		after[t.ID] = t.After
		tasks[i] = flow.Task{
			ID: t.ID, Title: flow.CleanText(t.Title), Brief: t.Brief,
			ForWhom: flow.CleanText(t.ForWhom), IfSkipped: flow.CleanText(t.IfSkipped),
			Smallest: flow.CleanText(t.Smallest), NotDoing: flow.CleanText(t.NotDoing),
			Outcomes: outcomes[i], After: t.After, Priority: t.Priority, EstimateLines: t.Estimate, Request: t.Request, SplitFrom: parentID, Attempts: []flow.Attempt{},
		}
	}
	if id := cycleAt(newIDs, after); id != "" {
		fails = append(fails, fmt.Sprintf("shape: the after links make a cycle through %q", id))
	}
	if len(fails) == 0 && len(tasks) > 0 {
		fails = checkTasksLaunch(tasks, s, facts)
	}
	return tasks, fails
}

func replaceDep(after []string, old string, repl []string) []string {
	if old == "" || !slices.Contains(after, old) {
		return after
	}
	var out []string
	add := func(id string) {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	for _, a := range after {
		if a == old {
			for _, r := range repl {
				add(r)
			}
		} else {
			add(a)
		}
	}
	return out
}

func (e *Engine) acceptRecapDecideLocked(id string, s *flow.State, role string, args json.RawMessage, facts flow.ResolveFacts) error {
	var req struct {
		Task     string            `json:"task"`
		Choice   string            `json:"choice"`
		Why      string            `json:"why"`
		Children []json.RawMessage `json:"children,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if len(bytes.TrimSpace(args)) == 0 || dec.Decode(&req) != nil || strings.TrimSpace(req.Task) == "" || strings.TrimSpace(req.Choice) == "" {
		return e.refuse(id, "invalid_report", "A recap decision must carry task and choice.")
	}
	taskID, choice := strings.TrimSpace(req.Task), strings.TrimSpace(req.Choice)
	ti := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return t.ID == taskID })
	if ti < 0 || flow.OpenRecap(s.Tasks[ti]) == nil {
		return e.refuse(id, "no_recap", fmt.Sprintf("Task %q has no open recap.", taskID))
	}
	t := &s.Tasks[ti]
	if flow.Ended(*t) {
		return e.refuse(id, "already_closed", fmt.Sprintf("Task %q is already ended; its recap no longer needs a decision.", taskID))
	}
	rc := flow.OpenRecap(*t)
	if !slices.Contains(rc.Allowed, choice) {
		return e.refuse(id, "not_allowed", fmt.Sprintf("Choice %q is not allowed for this recap; allowed: %s.", choice, strings.Join(rc.Allowed, ", ")))
	}
	why, err := e.cleanWhy(id, req.Why, "recap decision", "--why")
	if err != nil {
		return err
	}
	now := e.now()
	if choice == "drop" {
		if len(req.Children) > 0 {
			return e.refuse(id, "invalid_report", "drop does not take --child tasks.")
		}
		rc.Decided, t.Dropped = &flow.RecapDecision{Choice: choice, Why: why, At: now}, &flow.Dropped{Why: why}
		return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "recap decide", "choice": choice, "why": why},
			Rule: "recap decide", Decision: "dropped", Why: why})
	}
	if choice == flow.ChoiceContinue {
		a := flow.OpenAttempt(*t)
		if a == nil {
			return e.refuse(id, "not_allowed", "continue needs an open attempt.")
		}
		newCap, note, msg := a.SizeCap, "", fmt.Sprintf("The coordinator lets task %s go on: %s. Continue the task.", t.ID, why)
		if rc.Trigger == flow.TriggerSize {
			ctx, cancel := context.WithTimeout(world.GitRepository(context.Background(), s.Project.Folder), lockGitTimeout)
			defer cancel()
			lines, err := e.git.ChangedLines(ctx, a.Worktree, "HEAD") // the same measure as N12's
			base := lines
			if err != nil {
				e.logError("factory.git_error", id, err)
				base, note = a.SizeCap, " (changed-lines read failed; raised from current cap)"
				if c, ok := e.tips[a.Worktree]; ok && c.lines > base {
					base, note = c.lines, " (changed-lines read failed; raised from last good reading)"
				}
			}
			newCap = base + s.Settings.ContinueLines
			cur := fmt.Sprintf(" (%d now)", lines)
			if err != nil {
				cur = ""
			}
			msg = fmt.Sprintf("The coordinator lets task %s go on: %s. Your size cap is now %d changed lines%s. Continue the task.", t.ID, why, newCap, cur)
		}
		if a.RunID != "" || a.Run != nil && a.Run.State == flow.PhaseEnded {
			sctx, scancel := context.WithTimeout(context.Background(), lockGitTimeout)
			defer scancel()
			if err := e.deliverWorkerLocked(sctx, id, t.ID, s, a, msg, false); err != nil {
				e.logError("factory.exec_error", id, err)
				return err
			}
		}
		dec := "continue"
		if rc.Trigger == flow.TriggerSize {
			a.SizeCap, dec = newCap, fmt.Sprintf("continue; size cap %d%s", newCap, note)
		}
		a.Working = 0
		rc.Decided = &flow.RecapDecision{Choice: choice, Why: why, At: now}
		return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "recap decide", "choice": choice, "why": why},
			Rule: "recap decide", Decision: dec, Why: why})
	}
	if choice == "new" {
		if !rc.Amended {
			return e.refuse(id, "not_allowed", "new is allowed only after plan amend changed the task's brief or outcomes since the recap opened.")
		}
		note := fmt.Sprintf("Closed by sheepdog factory: task %s starts a new attempt (%s).", t.ID, why)
		if a := flow.OpenAttempt(*t); a != nil {
			ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
			defer cancel()
			if _, err := e.closeOpenPR(ctx, s.Project.Folder, *a, note); err != nil {
				return err // before any run is stopped: the task is as it was
			}
			if err := e.stopRuns(ctx, taskRuns(id, *t)); err != nil {
				return err
			}
			stopTaskRuns(t, s.Settings, now)
			a.Closed = note
		}
		rc.Decided = &flow.RecapDecision{Choice: choice, Why: why, At: now}
		return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "recap decide", "choice": choice, "why": why},
			Rule: "recap decide", Decision: "attempt closed; a new one starts", Why: why})
	}
	raw, _ := json.Marshal(struct {
		Tasks []json.RawMessage `json:"tasks"`
	}{Tasks: req.Children})
	children, fails := checkPlanWith(raw, s.Tasks, t.ID, s.Settings, facts)
	if len(fails) > 0 {
		return e.refuse(id, "plan_invalid", strings.Join(fails, "; ")+".")
	}
	childIDs := make([]string, len(children))
	for i, c := range children {
		childIDs[i] = c.ID
	}
	rc.Decided, t.Split = &flow.RecapDecision{Choice: choice, Why: why, At: now}, &flow.Split{Children: childIDs}
	for i := range s.Tasks {
		if i != ti {
			s.Tasks[i].After = replaceDep(s.Tasks[i].After, t.ID, childIDs)
		}
	}
	s.Tasks = append(s.Tasks, children...)
	return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "recap decide", "choice": choice, "why": why, "children": childIDs},
		Rule: "recap decide", Decision: "split into " + strings.Join(childIDs, ", "), Why: why})
}

// acceptTaskLocked is the coordinator's park, unpark, drop, close and
// send-back (design.md 4.9, 7.2). The rows then act on the record.
func (e *Engine) acceptTaskLocked(id string, s *flow.State, role string, args json.RawMessage) error {
	var req struct{ Action, Task, Why string }
	if json.Unmarshal(args, &req) != nil {
		return e.refuse(id, "invalid_report", "A task command must carry action, task and why.")
	}
	ti := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return t.ID == strings.TrimSpace(req.Task) })
	switch {
	case ti < 0:
		return e.refuse(id, "invalid_report", fmt.Sprintf("No task %q in the plan.", req.Task))
	case flow.Ended(s.Tasks[ti]):
		return e.refuse(id, "already_closed", fmt.Sprintf("Task %q has ended; it cannot be changed.", req.Task))
	}
	why, err := e.cleanWhy(id, req.Why, "task command", "--why")
	if err != nil {
		return err
	}
	t, verb := &s.Tasks[ti], "task "+req.Action
	a := flow.OpenAttempt(*t)
	line := LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": verb, "why": why}, Rule: verb, Decision: req.Action, Why: why}
	switch req.Action {
	case "park", "unpark":
		t.Parked = req.Action == "park"
	case "drop":
		t.Dropped = &flow.Dropped{Why: why}
		var waiting []string // nothing is unblocked silently: they keep waiting on the dropped id
		for _, o := range s.Tasks {
			if slices.Contains(o.After, t.ID) && !flow.Ended(o) {
				waiting = append(waiting, o.ID)
			}
		}
		if len(waiting) > 0 {
			line.Decision += "; still waiting on it: " + strings.Join(waiting, ", ")
		}
	case "close":
		if a == nil || a.NothingToChange == "" {
			return e.refuse(id, "not_allowed", "close is allowed only after the worker reported nothing to change.")
		}
		ctx, cancel := context.WithTimeout(world.GitRepository(context.Background(), s.Project.Folder), lockGitTimeout)
		defer cancel()
		if tip, _, _, err := e.git.InspectWorktree(ctx, a.Worktree, flow.H(*a)); err != nil {
			return err
		} else if tip != a.BaseCommit {
			return e.refuse(id, "nothing_new", "The worker committed since it reported nothing to change; send it back or wait for its ready.")
		}
		t.Closed = &flow.Closed{Why: why}
	case "send-back":
		if a == nil {
			return e.refuse(id, "invalid_report", fmt.Sprintf("Task %q has no attempt to send back.", t.ID))
		}
		n := len(a.Rounds) + 1
		a.Rounds = append(a.Rounds, flow.Round{N: n, Kind: flow.RoundSendBack, Head: flow.H(*a), Verdict: flow.VerdictChanges, Why: why})
		a.NothingToChange, line.Decision = "", fmt.Sprintf("sent back (round %d)", n)
	case "retry":
		acts := &t.Acts
		if a != nil {
			acts = &a.Acts
		}
		now, recent := e.now(), flow.SinceRetry(*acts)
		hasWake := func(prefix string) bool {
			return slices.ContainsFunc(recent, func(x flow.Act) bool {
				return x.Result == "done" && strings.HasPrefix(x.Key, flow.AskWake+"@"+prefix)
			})
		}
		isN4 := a != nil && (t.LastRow == "N4" || t.LastRow == "N26" && hasWake("pr_other_base@"))
		isN7 := a != nil && t.LastRow == "N7" && (hasWake("diverged@") || hasWake("merge_only_unknown@"))
		isN5 := (t.LastRow == "N5" && flow.HasExhausted(*acts, s.Settings)) ||
			(t.LastRow == "N3" && flow.Exhausted(*acts, flow.AskStopRuns, s.Settings)) ||
			(a != nil && (flow.Done(recent, flow.AskWake+"@worker_failed") || hasWake("reviewer_failed@")))
		if !isN4 && !isN5 && !isN7 {
			return e.refuse(id, "nothing_to_retry", fmt.Sprintf("Task %q has nothing to retry (it is not at N4, N5, N7 or a PR on another base).", t.ID))
		}
		if isN4 {
			reason := map[bool]string{false: "PR closed", true: "PR on another base"}[t.LastRow == "N26"]
			note := fmt.Sprintf("Closed by sheepdog factory: retried after %s (%s).", reason, why)
			ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
			defer cancel()
			if _, err := e.closeOpenPR(ctx, s.Project.Folder, *a, note); err != nil {
				return err // before any run is stopped: the task is as it was
			}
			if err := e.stopRuns(ctx, taskRuns(id, *t)); err != nil {
				return err
			}
			stopTaskRuns(t, s.Settings, now)
			*acts = append(*acts, flow.Act{Key: flow.AskRetry, Result: "done", At: now})
			a.Closed = note
			line.Decision = "retried; attempt closed for a new one"
			if n := len(a.Held); n > 0 {
				line.Decision += fmt.Sprintf("; dropped %d held message(s)", n)
			}
		} else {
			*acts = append(*acts, flow.Act{Key: flow.AskRetry, Result: "done", At: now})
			if a != nil {
				a.Restarts = 0
				if a.Run != nil && a.Run.State == flow.PhaseEnded {
					a.Run.Result = flow.EndStopped
				}
				if a.Review != nil {
					a.Review.Retries, a.Review.FailedModels = 0, nil
					if a.Review.Run != nil && a.Review.Run.State == flow.PhaseEnded {
						a.Review.Run.Result = flow.EndStopped
					}
				}
			}
			line.Decision = "retried"
		}
	case "say":
		if a == nil {
			return e.refuse(id, "no_worker", fmt.Sprintf("Task %q has no open attempt to message.", t.ID))
		}
		ctx, cancel := context.WithTimeout(context.Background(), lockGitTimeout)
		defer cancel()
		if err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, "Message from the coordinator: "+why, false); errors.Is(err, errNoWorkerRun) {
			return e.refuse(id, "no_worker", fmt.Sprintf("Task %q has no worker run to message.", t.ID))
		} else if err != nil {
			return err
		}
		line.Decision = "said to worker"
	default:
		return e.refuse(id, "invalid_report", fmt.Sprintf("%q is not a task command; use park, unpark, drop, close, send-back, retry or say.", req.Action))
	}
	return e.commitLocked(id, s, line)
}

// amendTask is one entry of plan amend for an existing task: only the
// fields named change.
type amendTask struct {
	ID       string                       `json:"id"`
	Title    *string                      `json:"title"`
	Brief    *string                      `json:"brief"`
	Outcomes *[]planOutcome               `json:"outcomes"`
	Estimate *int                         `json:"estimate_lines"`
	After    *[]string                    `json:"after"`
	Priority *string                      `json:"priority"`
	Request  *map[string][]flow.Candidate `json:"request"`
}

// acceptAmendLocked is plan amend (design.md 3.1, 4.9, 7.2): changes to
// existing tasks' title, brief and outcomes, and new tasks, with a why. An
// ended task is not changed. A changed brief or outcomes is a boundary:
// earlier rounds stop covering, an in-flight review is stopped, and an open
// recap may then decide new. A live worker is told once what changed.
func (e *Engine) acceptAmendLocked(id string, s *flow.State, role string, args json.RawMessage, facts flow.ResolveFacts) error {
	var req struct {
		Why   string            `json:"why"`
		Tasks []json.RawMessage `json:"tasks"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return e.refuse(id, "plan_invalid", "shape: "+strings.TrimPrefix(err.Error(), "json: ")+".")
	}
	why, err := e.cleanWhy(id, req.Why, "plan amend", `"why"`)
	if err != nil {
		return err
	}
	var fails []string
	if len(req.Tasks) == 0 {
		fails = append(fails, "shape: the amend has no tasks")
	}
	type change struct {
		ti       int
		c        amendTask
		outcomes []flow.Outcome
		after    []string
		priority string
		request  map[string][]flow.Candidate
	}
	var changes []change
	var fresh []json.RawMessage
	var reqTasks []flow.Task
	for _, raw := range req.Tasks {
		var c amendTask
		_ = json.Unmarshal(raw, &c) // the id decides: an existing task or a new one
		ti := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return t.ID == strings.TrimSpace(c.ID) })
		if ti < 0 {
			fresh = append(fresh, raw)
			continue
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&c); err != nil {
			fails = append(fails, fmt.Sprintf("shape: task %q: %s (only title, brief, outcomes, estimate_lines, after, priority and request change)", s.Tasks[ti].ID, strings.TrimPrefix(err.Error(), "json: ")))
			continue
		}
		if flow.Ended(s.Tasks[ti]) {
			fails = append(fails, fmt.Sprintf("task %q has ended and is not changed", s.Tasks[ti].ID))
			continue
		}
		before, outs, after, prio, reqMap := len(fails), []flow.Outcome(nil), []string(nil), "", map[string][]flow.Candidate(nil)
		if c.Title != nil {
			fails = append(fails, checkTitle(s.Tasks[ti].ID, *c.Title)...)
		}
		if c.Outcomes != nil {
			var oFails []string
			outs, oFails = checkOutcomes(s.Tasks[ti].ID, *c.Outcomes)
			fails = append(fails, oFails...)
		}
		if c.Estimate != nil {
			fails = append(fails, checkEstimate(s.Tasks[ti].ID, *c.Estimate, s.Settings.TaskSizeLimit)...)
		}
		if (c.After != nil || c.Priority != nil) && len(s.Tasks[ti].Attempts) > 0 {
			fails = append(fails, fmt.Sprintf("task %q already has an attempt; after and priority cannot be changed", s.Tasks[ti].ID))
		}
		if c.After != nil {
			for _, dep := range *c.After {
				after = append(after, strings.TrimSpace(dep))
			}
		}
		if c.Priority != nil {
			var pFails []string
			prio, pFails = checkPriority(s.Tasks[ti].ID, *c.Priority)
			fails = append(fails, pFails...)
		}
		if c.Request != nil {
			if err := flow.ValidateCandidates("request", *c.Request); err != nil {
				fails = append(fails, fmt.Sprintf("shape: task %q %s", s.Tasks[ti].ID, err))
			} else {
				if len(*c.Request) > 0 {
					reqMap = make(map[string][]flow.Candidate, len(s.Tasks[ti].Request)+len(*c.Request))
					maps.Copy(reqMap, s.Tasks[ti].Request)
					maps.Copy(reqMap, *c.Request)
				}
				merged := s.Tasks[ti]
				merged.Request = reqMap
				if !slices.Equal(s.Tasks[ti].Request[flow.RoleWorker], reqMap[flow.RoleWorker]) {
					merged.Attempts = nil // preflight the worker the amended request will launch
				}
				reqTasks = append(reqTasks, merged)
			}
		}
		if len(fails) == before {
			changes = append(changes, change{ti, c, outs, after, prio, reqMap})
		}
	}
	var added []flow.Task
	if len(fresh) > 0 {
		raw, _ := json.Marshal(map[string]any{"tasks": fresh})
		var f []string
		added, f = checkPlanWith(raw, s.Tasks, "", s.Settings, facts)
		fails = append(fails, f...)
	}
	if slices.ContainsFunc(changes, func(ch change) bool { return ch.c.After != nil }) {
		afterMap, checkIDs := make(map[string][]string, len(s.Tasks)+len(added)), make([]string, 0, len(changes)+len(added))
		for _, t := range s.Tasks {
			afterMap[t.ID] = t.After
		}
		for _, t := range added {
			afterMap[t.ID], checkIDs = t.After, append(checkIDs, t.ID)
		}
		for _, ch := range changes {
			if ch.c.After != nil {
				tid := s.Tasks[ch.ti].ID
				afterMap[tid], checkIDs = ch.after, append(checkIDs, tid)
				for _, dep := range ch.after {
					if _, ok := afterMap[dep]; !ok {
						fails = append(fails, fmt.Sprintf("shape: task %q is after %q, which is not in the plan", tid, dep))
					}
				}
			}
		}
		if cid := cycleAt(checkIDs, afterMap); cid != "" {
			fails = append(fails, fmt.Sprintf("shape: the after links make a cycle through %q", cid))
		}
	}
	if len(fails) == 0 && len(reqTasks) > 0 {
		fails = checkTasksLaunch(reqTasks, s.Settings, facts)
	}
	if len(fails) > 0 {
		return e.refuse(id, "plan_invalid", strings.Join(fails, "; ")+".")
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockGitTimeout)
	defer cancel()
	var done []string
	for _, ch := range changes {
		t, c, what := &s.Tasks[ch.ti], ch.c, []string{}
		if c.Title != nil && flow.CleanText(*c.Title) != t.Title {
			t.Title, what = flow.CleanText(*c.Title), append(what, "title")
		}
		if c.Brief != nil && *c.Brief != t.Brief {
			t.Brief, what = *c.Brief, append(what, "brief")
		}
		if c.Outcomes != nil && !slices.Equal(ch.outcomes, t.Outcomes) {
			t.Outcomes, what = ch.outcomes, append(what, "outcomes")
		}
		if c.After != nil && !slices.Equal(ch.after, t.After) {
			t.After, what = ch.after, append(what, "after")
		}
		if c.Priority != nil && ch.priority != t.Priority {
			t.Priority, what = ch.priority, append(what, "priority")
		}
		var oldReq map[string][]flow.Candidate
		if c.Request != nil && !reflect.DeepEqual(ch.request, t.Request) {
			oldReq, t.Request, what = t.Request, ch.request, append(what, "request")
		}
		a := flow.OpenAttempt(*t)
		var capNote string
		if c.Estimate != nil && *c.Estimate != t.EstimateLines {
			t.EstimateLines, what = *c.Estimate, append(what, "estimate_lines")
			if a != nil {
				// design.md 4.9: may lower an open attempt's size_cap (never raise it, and never change it while a size recap is open).
				want := s.Settings.SizeCapFactor * t.EstimateLines
				if flow.OpenRecap(*t) != nil && flow.OpenRecap(*t).Trigger == flow.TriggerSize {
					capNote = fmt.Sprintf("; size cap unchanged at %d while size recap is open (applies to next attempt)", a.SizeCap)
				} else if want > a.SizeCap {
					capNote = fmt.Sprintf("; size cap unchanged at %d (amend cannot raise a started task's cap; applies to next attempt)", a.SizeCap)
				} else {
					a.SizeCap = want
				}
			}
		}
		if slices.Contains(what, "brief") || slices.Contains(what, "outcomes") {
			if rc := flow.OpenRecap(*t); rc != nil {
				rc.Amended = true
			}
			if a != nil {
				a.Boundary = len(a.Rounds)
				if a.Review != nil { // it was given the old brief
					if err := world.StopRun(ctx, e.HC, a.Review.RunID); err != nil {
						return err
					}
					a.Review.Retries, a.Review.FailedModels, a.Review.Run = 0, nil, nil
					stopReviewRec(a.Review, s.Settings, e.now())
				}
			}
		}
		if slices.Contains(what, "request") && a != nil {
			if !slices.Equal(oldReq[flow.RoleWorker], t.Request[flow.RoleWorker]) && (a.RunID != "" || a.Run != nil && a.Run.State != flow.PhaseEnded) {
				if err := world.StopRun(ctx, e.HC, a.RunID); err != nil {
					return err
				}
				stopWorkerRec(a, s.Settings, e.now(), false)
			}
			if !slices.Equal(oldReq[flow.RoleReviewer], t.Request[flow.RoleReviewer]) && a.Review != nil && a.Review.RunID != "" {
				if err := world.StopRun(ctx, e.HC, a.Review.RunID); err != nil {
					return err
				}
				stopReviewRec(a.Review, s.Settings, e.now())
			}
		}
		if len(what) == 0 {
			continue
		}
		done = append(done, t.ID+" ("+strings.Join(what, ", ")+")"+capNote)
		workerWhat := slices.DeleteFunc(slices.Clone(what), func(f string) bool { return f == "request" })
		if a != nil && a.RunID != "" && len(workerWhat) > 0 {
			rc := readyCmd(cmp.Or(e.Bin, "sheepdog"), t.Outcomes)
			msg := fmt.Sprintf("The plan changed for task %s (%s): %s\nTitle: %s\nBrief:\n%s\nOutcomes:\n%s\nEarlier review rounds no longer count. If your work must change, commit and run %s again; otherwise wait.",
				t.ID, strings.Join(workerWhat, ", "), why, t.Title, cmp.Or(strings.TrimSpace(t.Brief), "(none)"), bullets(t.Outcomes), rc)
			if err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false); err != nil { // a restarted worker reads the new plan anyway
				e.logError("factory.exec_error", id, err)
			}
		}
	}
	for _, t := range added {
		done = append(done, t.ID+" (new)")
	}
	s.Tasks = append(s.Tasks, added...)
	return e.commitLocked(id, s, LogLine{By: role, Input: map[string]any{"verb": "plan amend", "why": why, "tasks": done},
		Rule: "plan amend", Decision: "amended " + cmp.Or(strings.Join(done, ", "), "nothing"), Why: why})
}

// cycleAt returns a task on a cycle of after links, or "" when there is none.
func cycleAt(ids []string, after map[string][]string) string {
	seen := map[string]int{} // 1: on the current path, 2: finished
	var visit func(id string) string
	visit = func(id string) string {
		switch seen[id] {
		case 1:
			return id
		case 2:
			return ""
		}
		seen[id] = 1
		for _, a := range after[id] {
			if c := visit(a); c != "" {
				return c
			}
		}
		seen[id] = 2
		return ""
	}
	for _, id := range ids {
		if c := visit(id); c != "" {
			return c
		}
	}
	return ""
}

var cliProbeArgv = map[string][]string{
	"claude": {"claude", "auth", "status", "--json"},
	"codex":  {"codex", "login", "status"},
	"agy":    {"agy", "models"},
}

// planResolveFacts supplies non-quota CLI facts for plan launch checks
// (design.md 9.1, 11.7). Tests override it so no real CLI or network is called.
var planResolveFacts = func(ctx context.Context, clis []string) flow.ResolveFacts {
	return probeResolveFacts(ctx, clis, DoctorOptions{})
}

func (e *Engine) planProbeCLIs(token, verb string, args json.RawMessage) []string {
	if verb != "plan apply" && verb != "plan amend" && verb != "recap decide" {
		return nil
	}
	e.mu.Lock()
	id, s, role, ti := e.runByTokenLocked(token)
	e.mu.Unlock()
	var req struct {
		Choice          string
		Tasks, Children []amendTask
	}
	if id == "" || role != flow.RoleCoordinator || (ti >= 0 && flow.Ended(s.Tasks[ti])) || json.Unmarshal(args, &req) != nil {
		return nil
	}
	if verb == "recap decide" && strings.TrimSpace(req.Choice) == "split" {
		req.Tasks = req.Children
	} else if verb == "recap decide" || (verb == "plan apply" && len(s.Tasks) > 0) {
		return nil
	}
	seen, hasReq := map[string]bool{}, len(s.Settings.Models) > 0
	add := func(m map[string][]flow.Candidate) {
		for _, cs := range m {
			for _, c := range cs {
				if cli := strings.TrimSpace(c.CLI); cliProbeArgv[cli] != nil {
					seen[cli] = true
				}
			}
		}
	}
	for _, c := range req.Tasks {
		if c.Request == nil {
			continue
		}
		hasReq = true
		add(*c.Request)
		if idx := slices.IndexFunc(s.Tasks, func(x flow.Task) bool { return x.ID == strings.TrimSpace(c.ID) }); verb == "plan amend" && idx >= 0 {
			add(s.Tasks[idx].Request)
		}
	}
	if !hasReq || len(req.Tasks) == 0 {
		return nil
	}
	add(s.Settings.Models)
	for r, cs := range flow.DefaultModels(s.Settings) {
		if r != flow.RoleCoordinator && len(s.Settings.Models[r]) == 0 {
			add(map[string][]flow.Candidate{r: cs})
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

func checkTasksLaunch(tasks []flow.Task, s flow.Settings, facts flow.ResolveFacts) []string {
	if len(facts.CLIs) == 0 {
		return nil
	}
	facts.CLIs = maps.Clone(facts.CLIs)
	for cli := range cliProbeArgv {
		if _, ok := facts.CLIs[cli]; !ok {
			facts.CLIs[cli] = flow.CLIFacts{Unknown: true}
		}
	}
	var fails []string
	for _, t := range tasks {
		f, roles := facts, []string{flow.RoleReviewer}
		f.Worker = storedWorkerModel(t)
		for role := range t.Request {
			if role != flow.RoleWorker && role != flow.RoleReviewer {
				roles = append(roles, role)
			}
		}
		slices.Sort(roles)
		for _, role := range append([]string{flow.RoleWorker}, roles...) {
			if in, why := flow.Resolve(role, t, s, f); why != "" {
				fails = append(fails, fmt.Sprintf("launch: task %q %s: %s", t.ID, role, why))
			} else if role == flow.RoleWorker && f.Worker.Model == "" {
				f.Worker = flow.Candidate{CLI: in.CLI, Model: in.Model, Effort: in.Effort}
			}
		}
	}
	return fails
}

func probeResolveFacts(ctx context.Context, clis []string, opts DoctorOptions) flow.ResolveFacts {
	o := opts.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out := make(map[string]flow.CLIFacts, len(clis))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, cli := range clis {
		if argv := cliProbeArgv[cli]; len(argv) > 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var env []string
				if cli == "claude" {
					env = []string{"DISABLE_AUTOUPDATER=1"}
				}
				f := classifyProbe(cli, o.RunCommand(ctx, o.TempDir, env, argv...))
				mu.Lock()
				out[cli] = f
				mu.Unlock()
			}()
		}
	}
	wg.Wait()
	return flow.ResolveFacts{CLIs: out}
}

func classifyProbe(cli string, c CommandOutput) flow.CLIFacts {
	if c.Exit == 127 || errors.Is(c.Err, exec.ErrNotFound) || errors.Is(c.Err, os.ErrNotExist) {
		return flow.CLIFacts{}
	}
	if c.TimedOut || c.Err != nil {
		return flow.CLIFacts{Unknown: true}
	}
	out := strings.ToLower(c.Stdout + "\n" + c.Stderr)
	switch cli {
	case "claude":
		var st struct {
			LoggedIn *bool `json:"loggedIn"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(c.Stdout)), &st) == nil && st.LoggedIn != nil {
			return flow.CLIFacts{Installed: true, SignedIn: *st.LoggedIn}
		}
	case "codex":
		if strings.Contains(out, "not logged in") {
			return flow.CLIFacts{Installed: true}
		}
	case "agy":
		if ms := parseAgyModels(c.Stdout); c.Exit == 0 && len(ms) > 0 {
			models := make(map[string]bool, len(ms))
			for _, m := range ms {
				models[m] = true
			}
			return flow.CLIFacts{Installed: true, SignedIn: true, Models: models}
		}
		if c.Exit != 0 && (strings.Contains(out, "sign in") || strings.Contains(out, "logged in") || strings.Contains(out, "unauthenticated")) {
			return flow.CLIFacts{Installed: true}
		}
		return flow.CLIFacts{Unknown: true}
	}
	return flow.CLIFacts{Installed: c.Exit == 0, SignedIn: c.Exit == 0, Unknown: c.Exit != 0}
}
