package factory

// TDD step 1 (round 2): failing tests for the agy adapter review findings and
// the antigravity model-family pool bug.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

func startAgyReviewerRig(t *testing.T) (*agyRig, string, string, string) {
	t.Helper()
	r := newAgyRig(t, map[string][]flow.Candidate{
		flow.RoleReviewer: {
			{CLI: "agy", Model: agyReviewModel, Effort: "high"},
			{CLI: "agy", Model: agyCoordModel, Effort: "medium"},
		},
	})
	r.pass() // J7 coordinator
	coordTok := (*r.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	plan := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: agy review","brief":"Add item.go.",` + testScopeJSON + `,"outcomes":[{"text":"item exists","proved_by":"manual: item.go"}]}]}`
	if err := r.eng.Report(coordTok, "plan apply", []byte(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	r.pass() // N11 worktree
	r.pass() // N23 worker
	wt := r.state().Tasks[0].Attempts[0].Worktree
	workerTok := r.last().Env["SHEEPDOG_RUN_TOKEN"]
	if err := os.WriteFile(filepath.Join(wt, "item.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "item.go")
	gitIn(t, wt, "commit", "-m", "feat: add item")
	c1 := gitIn(t, wt, "rev-parse", "HEAD")
	writePRJSON(t, r.gh, "pr.json", c1, greenRollup)
	if err := r.eng.Report(workerTok, "ready", []byte(`{"head":"`+c1+`","proofs":[{"outcome":"item exists","artifact":"item.go"}],"touched":"item.go","skipped":""}`)); err != nil {
		t.Fatalf("ready: %v", err)
	}
	r.pass() // N26 pushes and opens PR
	writePRJSON(t, r.gh, "view.json", c1, greenRollup)
	r.pass() // N29 starts round 1 agy reviewer

	rv := r.state().Tasks[0].Attempts[0].Review
	if rv == nil || rv.RunID == "" || rv.Reviewer.CLI != "agy" {
		t.Fatalf("agy reviewer did not start: %+v", rv)
	}
	prompt, ok := strings.CutPrefix(r.last().Argv[len(r.last().Argv)-1], "-i=")
	if !ok {
		t.Fatalf("agy reviewer argv missing -i= prompt: %q", r.last().Argv)
	}
	return r, rv.RunID, c1, prompt
}

// TestAgyReviewerMissingReportNudgedOnceByN29 verifies that when an agy
// reviewer goes idle without submitting a verdict, N29 nudges it once to run
// `factory report review` (never logging invalid_report), and a subsequent
// report review command is accepted and stops the reviewer on the next pass.
func TestAgyReviewerMissingReportNudgedOnceByN29(t *testing.T) {
	r, revID, _, prompt := startAgyReviewerRig(t)

	// First turn ends idle without calling `factory report review`.
	r.srv.SetScreen(revID, "> "+prompt+"\n\n  Checked item.go; all tests pass.\n")
	r.srv.SetStatus(revID, &hc.Status{App: "antigravity", State: "done", Msg: "Checked item.go; all tests pass."})

	*r.now = r.now.Add(5 * time.Second)
	r.pass() // R9 -> working
	*r.now = r.now.Add(25 * time.Second)
	r.pass() // R20 -> idle -> N29 nudges once

	rv := r.state().Tasks[0].Attempts[0].Review
	if rv == nil || rv.RefusalSent {
		t.Fatalf("missing verdict treated as refusal instead of N29 nudge: review=%+v logs:\n%s", rv, r.logs.String())
	}
	if strings.Contains(r.logs.String(), `"invalid_report"`) {
		t.Fatalf("missing verdict logged invalid_report:\n%s", r.logs.String())
	}
	sends := sendsTo(t, r.srv.Requests(), revID)
	if len(sends) != 1 || !strings.Contains(sends[0], "factory report review") || strings.Contains(sends[0], "<report>") {
		t.Fatalf("sends to agy reviewer = %q, want 1 N29 nudge naming factory report review (no <report> tag)", sends)
	}
	if rv.Run == nil || rv.Run.State != flow.PhaseWorking {
		t.Fatalf("after N29 nudge state = %+v, want working", rv.Run)
	}

	// Reviewer submits a pass verdict via the review report command and is accepted.
	if err := submitFakeReviewerVerdict(t, r.eng, r.srv, r.paths, r.id, "T1", rv, r.reviewerToken(revID), flow.VerdictPass, 0, "Checked item.go; all tests pass.\n"); err != nil {
		t.Fatalf("report review after nudge: %v", err)
	}
	r.pass() // stops accepted reviewer silently

	a := r.state().Tasks[0].Attempts[0]
	if len(a.Rounds) != 1 || a.Rounds[0].Verdict != flow.VerdictPass || (a.Review != nil && a.Review.RunID == revID) {
		t.Fatalf("after nudge command verdict: review=%+v rounds=%+v, want round 1 pass with rev1 stopped", a.Review, a.Rounds)
	}
}

// TestAgyDoctorUnreadableStatusIsUnknownNeverSignedOut checks design.md 9.2 and
// carbon/hydrogen Major 2: when `agy models` fails with a timeout, IPC/RPC
// error, exit 2, or empty output, checkAgy reports unknown with its reason
// (never cliSignedOut), and CheckAgentCLIs never reports "no agent CLI is logged in".
func TestAgyDoctorUnreadableStatusIsUnknownNeverSignedOut(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		out        CommandOutput
		wantReason string
	}{
		{"timed out", CommandOutput{Exit: -1, TimedOut: true}, "timed out"},
		{"ipc error", CommandOutput{Exit: -1, Err: errors.New("hc exec: broken pipe")}, "broken pipe"},
		{"exit 2", CommandOutput{Exit: 2, Stderr: "ipc transport broken\n"}, "ipc transport broken"},
		{"empty exit 0", CommandOutput{Exit: 0, Stdout: ""}, "no models listed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 1. Direct checkAgy outcome is not cliSignedOut, cliReady, or cliMissing.
			opts := DoctorOptions{
				TempDir:  t.TempDir(),
				LookPath: func(bin string) (string, error) { return "/usr/local/bin/" + bin, nil },
				RunCommand: func(context.Context, string, []string, ...string) CommandOutput {
					return tc.out
				},
			}
			oc := checkAgy(ctx, opts, probeContext{})
			if oc.state == cliSignedOut || oc.state == cliReady || oc.state == cliMissing || oc.state == cliNotInHCPath {
				t.Errorf("checkAgy(%s) state = %v, want unknown state (never cliSignedOut)", tc.name, oc.state)
			}
			if oc.result.Status != onboard.Warn || !strings.Contains(oc.result.Detail, tc.wantReason) {
				t.Errorf("checkAgy(%s) result = %+v, want Warn mentioning %q", tc.name, oc.result, tc.wantReason)
			}

			// 2. Only agy installed -> CheckAgentCLIs fails gate, never claiming "no agent CLI is logged in".
			envOnly := newFakeEnv("agy")
			envOnly.handlers["agy models"] = func(string, []string, []string) CommandOutput { return tc.out }
			rs := CheckAgentCLIs(ctx, DoctorOptions{TempDir: t.TempDir(), LookPath: envOnly.lookPath, RunCommand: envOnly.run, Canary: false}, true)
			agyRes := findResult(t, rs, "agy")
			if agyRes.Status != onboard.Warn || !strings.Contains(agyRes.Detail, tc.wantReason) {
				t.Errorf("agy row = %+v, want Warn with %q", agyRes, tc.wantReason)
			}
			gate := findResult(t, rs, "agent clis")
			gateLow := strings.ToLower(gate.Detail + " " + gate.Fix)
			if gate.Status != onboard.Fail {
				t.Errorf("agent clis gate = %+v, want Fail (unknown is never fine)", gate)
			}
			if strings.Contains(gateLow, "logged in") || strings.Contains(gateLow, "signed out") || strings.Contains(gateLow, "sign in") {
				t.Errorf("agent clis gate treated unknown agy status as signed out: %+v", gate)
			}

			// 3. claude + codex signed out, agy unreadable -> gate still must not say "no agent CLI is logged in".
			envMix := newFakeEnv("claude", "codex", "agy")
			envMix.handlers["claude auth"] = func(string, []string, []string) CommandOutput {
				return CommandOutput{Stdout: `{"loggedIn":false}`, Exit: 1}
			}
			envMix.handlers["codex login"] = func(string, []string, []string) CommandOutput {
				return CommandOutput{Stdout: "Not logged in\n", Exit: 1}
			}
			envMix.handlers["agy models"] = func(string, []string, []string) CommandOutput { return tc.out }
			rsMix := CheckAgentCLIs(ctx, DoctorOptions{TempDir: t.TempDir(), LookPath: envMix.lookPath, RunCommand: envMix.run, Canary: false}, true)
			gateMix := findResult(t, rsMix, "agent clis")
			if gateMix.Status != onboard.Fail || strings.Contains(strings.ToLower(gateMix.Detail), "no agent cli is logged in") {
				t.Fatalf("agent clis gate with signed-out others + unknown agy = %+v, must not claim no agent CLI is logged in", gateMix)
			}

			// 4. claude ready + agy unreadable -> gate passes while agy warns with its reason.
			envOK := newFakeEnv("claude", "agy")
			envOK.handlers["claude auth"] = func(string, []string, []string) CommandOutput {
				return CommandOutput{Stdout: `{"loggedIn":true}`}
			}
			envOK.handlers["agy models"] = func(string, []string, []string) CommandOutput { return tc.out }
			rsOK := CheckAgentCLIs(ctx, DoctorOptions{TempDir: t.TempDir(), LookPath: envOK.lookPath, RunCommand: envOK.run, Canary: false}, true)
			if onboard.Failed(rsOK) {
				t.Fatalf("one ready CLI with unknown agy should pass gate: %+v", rsOK)
			}
			if r := findResult(t, rsOK, "agy"); r.Status != onboard.Warn || !strings.Contains(r.Detail, tc.wantReason) {
				t.Errorf("agy warning alongside ready claude = %+v, want Warn with %q", r, tc.wantReason)
			}
		})
	}
}

// TestAntigravityPoolByModelFamily verifies that the antigravity pool reads
// the real CodexBar JSON shape by model family: agy gemini-* candidates use only
// Gemini windows (extraRateWindows ids containing "gemini", or primary labelled
// "Gemini Models"), while agy Claude and GPT candidates use only Claude/GPT
// windows (ids containing "3p", or secondary). With Gemini at 12% used and
// Claude/GPT weekly at 99.6% used, Gemini is usable and Claude/GPT is held.
func TestAntigravityPoolByModelFamily(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	rawReal, err := os.ReadFile(filepath.Join("testdata", "codexbar", "antigravity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(rawReal); strings.Contains(s, "@") && !strings.Contains(s, "account@example.invalid") {
		t.Fatalf("fixture testdata/codexbar/antigravity.json holds an unredacted account address")
	}

	ctx := context.Background()
	now := time.Date(2026, 11, 22, 19, 47, 32, 0, time.UTC)
	reset3pWeekly := time.Date(2026, 11, 25, 17, 34, 18, 0, time.UTC)
	settings := flow.DefaultSettings()
	settings.PoolMinutes = 1
	settings.PoolFloorPercent = 5

	resolveWithPoolJSON := func(t *testing.T, _ string, jsonOut string, cands []flow.Candidate) (flow.Intent, flow.ResolveReason) {
		t.Helper()
		paths := testPaths(t)
		eng := newGitTestEngine(t, paths, nil)
		eng.Now = func() time.Time { return now }
		fake := newPoolFake(t)
		attachQuotaPools(eng, fake)
		fake.set("antigravity", CommandOutput{Stdout: jsonOut})
		s := settings
		s.Models = map[string][]flow.Candidate{flow.RoleWorker: cands}
		if err := SaveState(paths.State("p1"), flow.State{Seq: 1, Settings: s, Project: flow.Project{ID: "p1", Started: true}}); err != nil {
			t.Fatal(err)
		}
		pools := eng.refreshPools(ctx, "p1")
		gate := poolCheck(pools, paths.poolCooldowns(), s.PoolFloorPercent, now)
		return flow.ResolveWithReason(flow.RoleWorker, flow.Task{}, s, flow.ResolveFacts{Pool: gate})
	}

	// 1. Real CodexBar fixture: Gemini ~12.7% used (87% left) is usable; Claude/GPT weekly 99.56% used (0% left) is held.
	for _, model := range []string{"gemini-4-carbon-medium", "gemini-4-neon-medium", "gemini-3.8-flash-low"} {
		in, r := resolveWithPoolJSON(t, "real-gemini-"+model, string(rawReal), []flow.Candidate{{CLI: "agy", Model: model, Effort: "medium"}})
		if in.CLI != "agy" || in.Model != model || r.Why != "" {
			t.Errorf("real antigravity.json for %s = (%+v, %+v), want usable", model, in, r)
		}
	}
	for _, model := range []string{"claude-sonnet-5-5-medium", "claude-opus-5-5-high", "gpt-5.4-medium"} {
		in, r := resolveWithPoolJSON(t, "real-3p-"+model, string(rawReal), []flow.Candidate{{CLI: "agy", Model: model, Effort: "medium"}})
		if in != (flow.Intent{}) || r.Kind != flow.ReasonPool || !strings.Contains(strings.ToLower(r.Why), "antigravity") || !reasonHasTime(r.Why, reset3pWeekly) {
			t.Errorf("real antigravity.json for %s = (%+v, %+v), want held at floor naming antigravity and reset %s", model, in, r, reset3pWeekly.Format(time.RFC3339))
		}
	}

	// Candidate list [agy claude, agy gemini] skips floor-held Claude and picks Gemini.
	inFallback, rFallback := resolveWithPoolJSON(t, "fallback", string(rawReal), []flow.Candidate{
		{CLI: "agy", Model: "claude-sonnet-5-5-medium", Effort: "medium"},
		{CLI: "agy", Model: "gemini-4-carbon-medium", Effort: "medium"},
	})
	if inFallback.CLI != "agy" || inFallback.Model != "gemini-4-carbon-medium" || rFallback.Why != "" {
		t.Errorf("[agy claude, agy gemini] on real antigravity.json = (%+v, %+v), want gemini-4-carbon-medium", inFallback, rFallback)
	}

	// 2. Fallback shape with no extraRateWindows (only primary labelled "Gemini Models" and secondary labelled "Claude and GPT").
	labelOnly := func(geminiUsed, thirdPartyUsed float64) string {
		return fmt.Sprintf(
			`[{"provider":"antigravity","rateWindowLabels":{"primary":"Gemini Models","secondary":"Claude and GPT"},"source":"cli","usage":{"updatedAt":%q,"primary":{"usedPercent":%v,"windowMinutes":300,"resetsAt":%q},"secondary":{"usedPercent":%v,"windowMinutes":10080,"resetsAt":%q}}}]`,
			now.Format(time.RFC3339), geminiUsed, now.Add(2*time.Hour).Format(time.RFC3339), thirdPartyUsed, reset3pWeekly.Format(time.RFC3339),
		)
	}
	if in, r := resolveWithPoolJSON(t, "label-gemini-ok", labelOnly(12, 99.6), []flow.Candidate{{CLI: "agy", Model: "gemini-4-carbon-medium", Effort: "medium"}}); in.Model != "gemini-4-carbon-medium" || r.Why != "" {
		t.Errorf("label-only (12%% gemini, 99.6%% 3p) gemini = (%+v, %+v), want usable", in, r)
	}
	if in, r := resolveWithPoolJSON(t, "label-3p-held", labelOnly(12, 99.6), []flow.Candidate{{CLI: "agy", Model: "claude-sonnet-5-5-medium", Effort: "medium"}}); in != (flow.Intent{}) || r.Kind != flow.ReasonPool {
		t.Errorf("label-only (12%% gemini, 99.6%% 3p) claude = (%+v, %+v), want held", in, r)
	}
	if in, r := resolveWithPoolJSON(t, "label-gemini-held", labelOnly(97, 20), []flow.Candidate{{CLI: "agy", Model: "gemini-4-carbon-medium", Effort: "medium"}}); in != (flow.Intent{}) || r.Kind != flow.ReasonPool {
		t.Errorf("label-only (97%% gemini, 20%% 3p) gemini = (%+v, %+v), want held", in, r)
	}
	if in, r := resolveWithPoolJSON(t, "label-3p-ok", labelOnly(97, 20), []flow.Candidate{{CLI: "agy", Model: "gpt-5.4-medium", Effort: "medium"}}); in.Model != "gpt-5.4-medium" || r.Why != "" {
		t.Errorf("label-only (97%% gemini, 20%% 3p) gpt = (%+v, %+v), want usable", in, r)
	}

	// 3. Engine integration: with real antigravity.json, Claude task is held at floor while Gemini task starts.
	rig := newAgyRig(t, map[string][]flow.Candidate{
		flow.RoleWorker: {{CLI: "agy", Model: "claude-sonnet-5-5-medium", Effort: "medium"}},
	})
	*rig.now = now
	fake := newPoolFake(t)
	attachQuotaPools(rig.eng, fake)
	fake.healthy(*rig.now)
	fake.set("antigravity", CommandOutput{Stdout: string(rawReal)})

	rig.pass() // J7 coordinator
	tok := (*rig.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	plan := `{"tasks":[` +
		poolTask("T1", `,"request":{"worker":[{"cli":"agy","model":"claude-sonnet-5-5-medium","effort":"medium"}]}`) + `,` +
		poolTask("T2", `,"request":{"worker":[{"cli":"agy","model":"gemini-4-carbon-medium","effort":"medium"}]}`) +
		`]}`
	if err := rig.eng.Report(tok, "plan apply", []byte(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	for range 3 {
		rig.pass()
	}
	st := rig.state()
	if len(st.Tasks[0].Attempts) != 0 || !strings.Contains(strings.ToLower(st.Tasks[0].HoldWhy), "antigravity") || !reasonHasTime(st.Tasks[0].HoldWhy, reset3pWeekly) {
		t.Errorf("T1 (agy/claude) = attempts=%d hold=%q, want held at antigravity floor until %s", len(st.Tasks[0].Attempts), st.Tasks[0].HoldWhy, reset3pWeekly.Format(time.RFC3339))
	}
	if len(st.Tasks[1].Attempts) != 1 || st.Tasks[1].Attempts[0].Run == nil || st.Tasks[1].Attempts[0].Run.CLI != "agy" || st.Tasks[1].Attempts[0].Run.Model != "gemini-4-carbon-medium" {
		t.Errorf("T2 (agy/gemini) = %+v, want launched on agy/gemini-4-carbon-medium", st.Tasks[1].Attempts)
	}
}

// TestAgyQuotedLimitDoesNotTriggerQuotaEndOrCooldown checks O23 end-to-end:
// an agy worker quoting limit strings above agy's bottom input box transitions
// to idle after done + grace (never R19 ended(quota)) and writes no cooldown.
func TestAgyQuotedLimitDoesNotTriggerQuotaEndOrCooldown(t *testing.T) {
	r := newAgyRig(t, map[string][]flow.Candidate{
		flow.RoleWorker: {{CLI: "agy", Model: agyWorkerModel, Effort: "medium"}},
	})
	r.pass() // J7 coordinator
	tok := (*r.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	plan := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: agy quote limit","brief":"Check world.go.",` + testScopeJSON + `,"outcomes":[{"text":"checked","proved_by":"manual: world.go"}]}]}`
	if err := r.eng.Report(tok, "plan apply", []byte(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	r.pass() // N11
	r.pass() // N23 starts agy worker
	w := r.state().Tasks[0].Attempts[0].RunID

	quotedScreen := "\n      ▄▀▀▄        Antigravity CLI 1.5.14\n" +
		"     ▀▀▀▀▀▀       account@example.invalid (Google AI Ultra)\n" +
		"    ▀▀▀▀▀▀▀▀      Gemini 4 Carbon (Medium)\n" +
		"   ▄▀▀    ▀▀▄     ~/wt\n" +
		"  ▄▀▀      ▀▀▄\n" +
		"────────────────────────────────────────────────────────────\n" +
		"> Check world.go.\n\n" +
		"● Read(~/wt/world.go)\n" +
		"  You've hit your limit · resets 2pm (America/Los_Angeles)\n" +
		"  Individual quota reached · resets 3pm\n\n" +
		"  Verified the quoted limit strings in world.go.\n\n" +
		"────────────────────────────────────────────────────────────────────────────────\n" +
		"> Auto mode: edits & safe commands auto-approved (shift+tab to cycle)\n" +
		"────────────────────────────────────────────────────────────────────────────────\n" +
		"esc to cancel                                  auto · Gemini 4 Carbon · medium\n"

	r.srv.SetScreen(w, quotedScreen)
	r.srv.SetStatus(w, &hc.Status{App: "antigravity", State: "done", Msg: "Verified the quoted limit strings."})
	*r.now = r.now.Add(5 * time.Second)
	r.pass() // R9 -> working
	*r.now = r.now.Add(25 * time.Second)
	r.pass() // R20 -> idle (must NOT match R19 ended(quota))

	run := r.state().Tasks[0].Attempts[0].Run
	if run == nil || run.State != flow.PhaseIdle {
		t.Fatalf("agy worker quoting limit text -> %+v, want PhaseIdle (not ended(quota))", run)
	}
	if cds := loadCooldowns(r.paths.poolCooldowns()); len(cds) != 0 {
		t.Fatalf("quoted limit text wrote pool cooldowns: %+v", cds)
	}
}
