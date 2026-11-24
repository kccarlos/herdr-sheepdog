package factory

// Trial finding 4, TDD step 1: failing tests for alert-clear logging
// (design.md section 6, "Alerts").
//
// Every alert clear, for every kind (stopping or non-stopping), logs exactly
// one line naming the kind:
//   By: "engine", Rule: cmp.Or(a.Rule, "alert"), Asked: []string{a.Kind},
//   Decision: "alert cleared: " + a.Kind,
//   Why: a.Cause + "; lasted " + <whole minutes from a.At to e.now()>.
// It opens no ask and sends no wake.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func alertClearLogs(t *testing.T, paths Paths, folder, kind string) []LogLine {
	t.Helper()
	lines, _, err := ProjectLog(context.Background(), paths, folder, 0)
	if err != nil {
		t.Fatalf("ProjectLog: %v", err)
	}
	wantDec := "alert cleared: " + kind
	var out []LogLine
	for _, l := range lines {
		if l.By == "engine" && l.Decision == wantDec {
			out = append(out, l)
		}
	}
	return out
}

func findStoredAlert(t *testing.T, paths Paths, id, kind string) flow.Alert {
	t.Helper()
	s, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for _, a := range s.Project.Alerts {
		if a.Kind == kind {
			return a
		}
	}
	t.Fatalf("expected active %q alert in state.json; got alerts=%+v", kind, s.Project.Alerts)
	return flow.Alert{}
}

func maxSeq(lines []LogLine) int64 {
	var best int64
	for _, l := range lines {
		if l.Seq > best {
			best = l.Seq
		}
	}
	return best
}

// Trial finding 4, test 1:
// Every alert kind logs exactly one clear line when it clears:
// Decision == "alert cleared: <kind>", Rule == cmp.Or(raised.Rule, "alert"),
// Why containing the raise cause and duration, and Seq above the raise line's.
func TestTrial_EveryAlertClearLogsOneLine(t *testing.T) {
	type fixtureResult struct {
		paths    Paths
		repo     string
		kind     string
		raised   flow.Alert
		raiseSeq int64
	}
	cases := []struct {
		name string
		run  func(t *testing.T) fixtureResult
	}{
		{
			name: "no_pool",
			run: func(t *testing.T) fixtureResult {
				w := newAlertWorld(t, "Clear log no_pool", false)
				w.setPool("claude", &poolWin{2, 90 * time.Minute}, &poolWin{60, 72 * time.Hour})
				w.pass()
				raised := findStoredAlert(t, w.paths, w.id, flow.AlertNoPool)
				raiseSeq := maxSeq(alertEpisodeLogs(t, w.paths, w.repo, flow.AlertNoPool))

				w.advance(95 * time.Minute)
				w.setPool("claude", &poolWin{80, 4 * time.Hour}, &poolWin{60, 72 * time.Hour})
				w.pass()
				return fixtureResult{w.paths, w.repo, flow.AlertNoPool, raised, raiseSeq}
			},
		},
		{
			name: "disk",
			run: func(t *testing.T) fixtureResult {
				w := newCDWorld(t, "Clear log disk")
				w.setDisks(
					DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 95, Known: true},
					DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
				)
				w.pass()
				raised := findStoredAlert(t, w.paths, w.id, flow.AlertDisk)
				raiseSeq := maxSeq(alertEpisodeLogs(t, w.paths, w.repo, flow.AlertDisk))

				w.setDisks(
					DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 60, Known: true},
					DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
				)
				w.advanceMinute()
				w.pass()
				return fixtureResult{w.paths, w.repo, flow.AlertDisk, raised, raiseSeq}
			},
		},
		{
			name: "consent_prompt",
			run: func(t *testing.T) fixtureResult {
				w := newCDWorld(t, "Clear log consent_prompt")
				w.setFolderProbe(cdProbeTimeout)
				w.pass()
				w.advanceMinute()
				w.pass()
				raised := findStoredAlert(t, w.paths, w.id, flow.AlertConsentPrompt)
				raiseSeq := maxSeq(alertEpisodeLogs(t, w.paths, w.repo, flow.AlertConsentPrompt))

				w.setFolderProbe(cdProbeOK)
				w.advanceMinute()
				w.pass()
				return fixtureResult{w.paths, w.repo, flow.AlertConsentPrompt, raised, raiseSeq}
			},
		},
		{
			name: "main_red",
			run: func(t *testing.T) fixtureResult {
				w := newAlertWorld(t, "Clear log main_red", false)
				w.pass()
				w.makeCoordinatorIdle()

				baseHead1 := "deadbeef1234567890deadbeef1234567890dead"
				writeGHFile(t, w.gh, "base-ref.json", fmt.Sprintf(`{"object":{"sha":%q}}`, baseHead1))
				writeBaseRuns(t, w.gh, fmt.Sprintf(`{"workflow_runs":[{"id":101,"head_sha":%q,"status":"completed","conclusion":"failure"}]}`, baseHead1))
				mainRedDur := time.Duration(w.state().Settings.MainRedMinutes) * time.Minute
				w.pass()
				w.advance(mainRedDur + time.Minute)
				w.pass()
				raised := findStoredAlert(t, w.paths, w.id, flow.AlertMainRed)
				raiseSeq := maxSeq(alertEpisodeLogs(t, w.paths, w.repo, flow.AlertMainRed))

				baseHead2 := "cafebabe1234567890cafebabe1234567890cafe"
				writeGHFile(t, w.gh, "base-ref.json", fmt.Sprintf(`{"object":{"sha":%q}}`, baseHead2))
				writeBaseRuns(t, w.gh, fmt.Sprintf(`{"workflow_runs":[{"id":102,"head_sha":%q,"status":"completed","conclusion":"success"}]}`, baseHead2))
				w.advance(time.Minute)
				w.pass()
				return fixtureResult{w.paths, w.repo, flow.AlertMainRed, raised, raiseSeq}
			},
		},
		{
			name: "ci_stalled",
			run: func(t *testing.T) fixtureResult {
				w := newAlertWorld(t, "Clear log ci_stalled", false)
				w.pass()
				w.makeCoordinatorIdle()

				wt, err := fixtureGit(t).NewWorktree(w.ctx, w.repo, w.paths.Worktree(w.id, "T1", 1), "T1-1", "feat: ci")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(wt.Path, "a.go"), []byte("package demo\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				gitIn(t, wt.Path, "add", "a.go")
				gitIn(t, wt.Path, "commit", "-m", "feat: c1")
				c1 := gitIn(t, wt.Path, "rev-parse", "HEAD")
				gitIn(t, wt.Path, "push", "origin", c1+":refs/heads/"+wt.Branch)
				wk, err := startRun(w.ctx, w.client, world.RunSpec{Name: workerName(w.id, "T1", 1), Cwd: wt.Path, Argv: []string{"sh"}})
				if err != nil {
					t.Fatal(err)
				}
				setIdle(t, w.srv, w.client, wk, "idle")

				w.edit(func(s *flow.State) {
					s.Tasks = []flow.Task{{
						ID: "T1", Title: "feat: ci", Brief: "b", EstimateLines: 30,
						Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: a.go"}},
						Attempts: []flow.Attempt{{
							SizeCap: 100, BaseCommit: w.baseSHA, Branch: wt.Branch, Worktree: wt.Path, PR: 7,
							RunID: wk,
							Run:   &flow.RunRecord{ID: wk, Role: flow.RoleWorker, CLI: "claude", Model: "sonnet", State: flow.PhaseIdle, StartedAt: w.now.Add(-5 * time.Minute), Turn: 1},
							Ready: []flow.Ready{{Head: c1}},
							Rounds: []flow.Round{
								{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
								{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
							},
						}},
					}}
				})
				stalledDur := time.Duration(w.state().Settings.CIStalledMinutes) * time.Minute
				writePRJSON(t, w.gh, "view.json", c1, `[{"__typename":"CheckRun","name":"ci","status":"IN_PROGRESS"}]`)
				w.pass()
				w.advance(stalledDur + time.Second)
				w.pass()
				raised := findStoredAlert(t, w.paths, w.id, flow.AlertCIStalled)
				raiseSeq := maxSeq(alertEpisodeLogs(t, w.paths, w.repo, flow.AlertCIStalled))

				writePRJSON(t, w.gh, "view.json", c1, greenRollup)
				writeGHFile(t, w.gh, "compare.json", `{"behind_by":1}`)
				w.advance(time.Minute)
				w.pass()
				return fixtureResult{w.paths, w.repo, flow.AlertCIStalled, raised, raiseSeq}
			},
		},
		{
			name: "agents_down_j5",
			run: func(t *testing.T) fixtureResult {
				w := newAlertWorld(t, "Clear log agents_down J5", false)
				w.edit(func(s *flow.State) {
					s.Settings.Models = map[string][]flow.Candidate{
						flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "high"}},
					}
				})
				w.pass()
				coord1 := w.state().Project.RunID
				w.setClaudeProbe(scriptedProbe{signedIn: false})
				w.srv.SetScreen(coord1, "Not logged in · Please run /login\n")
				w.pass()
				w.pass()
				raised := findStoredAlert(t, w.paths, w.id, flow.AlertAgentsDown)
				raiseSeq := maxSeq(alertEpisodeLogs(t, w.paths, w.repo, flow.AlertAgentsDown))

				// Positive sign-in check clears J5 (the path that previously bypassed reconcileAlertLocked).
				w.setClaudeProbe(scriptedProbe{signedIn: true})
				w.advance(time.Duration(w.state().Settings.PoolMinutes)*time.Minute + time.Minute)
				w.pass()
				return fixtureResult{w.paths, w.repo, flow.AlertAgentsDown, raised, raiseSeq}
			},
		},
		{
			name: "agents_down_j10",
			run: func(t *testing.T) fixtureResult {
				w := newAlertWorld(t, "Clear log agents_down J10", false)
				w.edit(func(s *flow.State) {
					s.Tasks = []flow.Task{{
						ID: "T1", Title: "feat: parked", Brief: "b", Parked: true, EstimateLines: 10,
						Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
					}}
				})
				w.pass()
				perm := "permission"
				blockedDur := time.Duration(w.state().Settings.BlockedMinutes)*time.Minute + time.Minute

				c1 := w.state().Project.RunID
				w.srv.SetStatus(c1, &hc.Status{App: "claude-code", State: "blocked", Kind: &perm, Msg: "Allow tool?"})
				w.advance(5 * time.Second)
				w.pass()
				w.advance(blockedDur)
				w.pass()

				c2 := w.state().Project.RunID
				w.srv.SetStatus(c2, &hc.Status{App: "claude-code", State: "blocked", Kind: &perm, Msg: "Allow tool again?"})
				w.advance(5 * time.Second)
				w.pass()
				w.advance(blockedDur)
				w.pass()
				raised := findStoredAlert(t, w.paths, w.id, flow.AlertAgentsDown)
				raiseSeq := maxSeq(alertEpisodeLogs(t, w.paths, w.repo, flow.AlertAgentsDown))

				w.makeCoordinatorIdle()
				return fixtureResult{w.paths, w.repo, flow.AlertAgentsDown, raised, raiseSeq}
			},
		},
		{
			name: "github_unavailable",
			run: func(t *testing.T) fixtureResult {
				w := newAlertWorld(t, "Clear log github_unavailable", false)
				w.pass()
				w.makeCoordinatorIdle()

				w.advance(time.Minute)
				writeGHFile(t, w.gh, "fail", "")
				w.pass()
				raised := findStoredAlert(t, w.paths, w.id, flow.AlertGitHubUnavailable)
				raiseSeq := maxSeq(alertEpisodeLogs(t, w.paths, w.repo, flow.AlertGitHubUnavailable))

				_ = os.Remove(filepath.Join(w.gh, "fail"))
				writeGHFile(t, w.gh, "base-ref.json", fmt.Sprintf(`{"object":{"sha":%q}}`, w.baseSHA))
				writeBaseRuns(t, w.gh, fmt.Sprintf(`{"workflow_runs":[{"id":1,"head_sha":%q,"status":"completed","conclusion":"success"}]}`, w.baseSHA))
				w.advance(time.Minute)
				w.pass()
				return fixtureResult{w.paths, w.repo, flow.AlertGitHubUnavailable, raised, raiseSeq}
			},
		},
		{
			name: "hc_unavailable",
			run: func(t *testing.T) fixtureResult {
				w := newAlertWorld(t, "Clear log hc_unavailable", false)
				w.pass()
				w.makeCoordinatorIdle()

				w.advance(time.Minute)
				w.hcOnline.Store(false)
				w.pass()
				raised := findStoredAlert(t, w.paths, w.id, flow.AlertHCUnavailable)
				raiseSeq := maxSeq(alertEpisodeLogs(t, w.paths, w.repo, flow.AlertHCUnavailable))

				w.hcOnline.Store(true)
				w.advance(time.Minute)
				w.pass()
				return fixtureResult{w.paths, w.repo, flow.AlertHCUnavailable, raised, raiseSeq}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.run(t)
			if ts, active := statusAlert(t, res.paths, res.repo, res.kind); active {
				t.Fatalf("%s did not clear: %+v", res.kind, ts)
			}
			clears := alertClearLogs(t, res.paths, res.repo, res.kind)
			if len(clears) != 1 {
				t.Fatalf("expected exactly 1 clear log line for %s, got %d: %+v", res.kind, len(clears), clears)
			}
			cl := clears[0]
			wantRule := cmp.Or(res.raised.Rule, "alert")
			if cl.Rule != wantRule {
				t.Errorf("clear line Rule = %q, want %q (line=%+v)", cl.Rule, wantRule, cl)
			}
			wantDec := "alert cleared: " + res.kind
			if cl.Decision != wantDec {
				t.Errorf("clear line Decision = %q, want %q", cl.Decision, wantDec)
			}
			if !strings.Contains(cl.Why, res.raised.Cause) {
				t.Errorf("clear line Why = %q, want it to contain raise cause %q", cl.Why, res.raised.Cause)
			}
			if !strings.Contains(cl.Why, "lasted") {
				t.Errorf("clear line Why = %q, want duration ('lasted ...')", cl.Why)
			}
			if cl.Seq <= res.raiseSeq {
				t.Errorf("clear line Seq = %d, want > raise Seq %d", cl.Seq, res.raiseSeq)
			}
		})
	}
}

// Trial finding 4, test 2:
// Clearing an alert logs a line only: it adds no ask, no held wake, and no
// coordinator delivery.
func TestTrial_AlertClearOpensNoAskAndNoWake(t *testing.T) {
	w := newAlertWorld(t, "Alert clear no ask no wake", false)
	w.pass()
	w.makeCoordinatorIdle()

	// Establish green base first so clearing github_unavailable does not also
	// trigger a base_green wake.
	writeGHFile(t, w.gh, "base-ref.json", fmt.Sprintf(`{"object":{"sha":%q}}`, w.baseSHA))
	writeBaseRuns(t, w.gh, fmt.Sprintf(`{"workflow_runs":[{"id":1,"head_sha":%q,"status":"completed","conclusion":"success"}]}`, w.baseSHA))
	w.advance(time.Minute)
	w.pass()

	// Raise github_unavailable.
	writeGHFile(t, w.gh, "fail", "")
	w.advance(time.Minute)
	w.pass()
	if _, active := statusAlert(t, w.paths, w.repo, flow.AlertGitHubUnavailable); !active {
		t.Fatal("expected github_unavailable to be active before clear")
	}

	stBefore := w.state()
	j12Before := len(logRules(t, w.paths.Log(w.id), "J12"))
	j13Before := len(logRules(t, w.paths.Log(w.id), "J13"))

	// Clear github_unavailable.
	_ = os.Remove(filepath.Join(w.gh, "fail"))
	w.advance(time.Minute)
	w.pass()

	if ts, active := statusAlert(t, w.paths, w.repo, flow.AlertGitHubUnavailable); active {
		t.Fatalf("github_unavailable did not clear: %+v", ts)
	}
	if clears := alertClearLogs(t, w.paths, w.repo, flow.AlertGitHubUnavailable); len(clears) != 1 {
		t.Fatalf("expected 1 clear log line for github_unavailable, got %d: %+v", len(clears), clears)
	}

	stAfter := w.state()
	if len(stAfter.Asks) != len(stBefore.Asks) {
		t.Fatalf("alert clear opened an ask: before=%d after=%d (%+v)", len(stBefore.Asks), len(stAfter.Asks), stAfter.Asks)
	}
	if len(stAfter.Project.Wakes) != len(stBefore.Project.Wakes) {
		t.Fatalf("alert clear added a held wake: before=%+v after=%+v", stBefore.Project.Wakes, stAfter.Project.Wakes)
	}
	if got := len(logRules(t, w.paths.Log(w.id), "J12")); got != j12Before {
		t.Fatalf("alert clear triggered J12 coordinator delivery: before=%d after=%d", j12Before, got)
	}
	if got := len(logRules(t, w.paths.Log(w.id), "J13")); got != j13Before {
		t.Fatalf("alert clear triggered J13 heartbeat: before=%d after=%d", j13Before, got)
	}
	if !reflect.DeepEqual(stAfter.Project.CoordWakesInFlight, stBefore.Project.CoordWakesInFlight) || !stAfter.Project.CoordWakeAt.Equal(stBefore.Project.CoordWakeAt) {
		t.Fatalf("alert clear changed coordinator wake state: inFlight %v->%v, wakeAt %v->%v",
			stBefore.Project.CoordWakesInFlight, stAfter.Project.CoordWakesInFlight, stBefore.Project.CoordWakeAt, stAfter.Project.CoordWakeAt)
	}
}

// Trial finding 4, test 3:
// When appending the clear line succeeds and saving state.json fails on the
// clear pass, the alert stays active; after the next good pass there is
// exactly one clear line and no reused sequence number.
func TestTrial_AlertClearSaveFailureLogsOnce(t *testing.T) {
	w := newCDWorld(t, "Alert clear save failure")
	w.setDisks(
		DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 95, Known: true},
		DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
	)
	w.pass()
	if _, active := statusAlert(t, w.paths, w.repo, flow.AlertDisk); !active {
		t.Fatal("expected disk alert to be active")
	}

	// Inject a non-ENOSPC save failure on the clear pass while log append succeeds.
	failSave := true
	w.eng.writeState = func(path string, s flow.State) error {
		if failSave {
			return errors.New("injected save failure")
		}
		return SaveState(path, s)
	}
	w.setDisks(
		DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 60, Known: true},
		DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 40, Known: true},
	)
	w.advanceMinute()
	w.pass()

	if _, active := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertDisk); !active {
		t.Fatal("disk alert must still be shown when clear save failed")
	}
	if _, active := statusAlert(t, w.paths, w.repo, flow.AlertDisk); !active {
		t.Fatal("disk alert must remain in state.json when clear save failed")
	}
	if clears := alertClearLogs(t, w.paths, w.repo, flow.AlertDisk); len(clears) != 1 {
		t.Fatalf("expected 1 clear line appended before save failure, got %d: %+v", len(clears), clears)
	}

	// Next good pass completes the clear without logging a second clear line
	// and without reusing any sequence number.
	failSave = false
	w.advanceMinute()
	w.pass()

	if ts, active := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertDisk); active {
		t.Fatalf("disk alert did not clear on retry pass: %+v", ts)
	}
	if ts, active := statusAlert(t, w.paths, w.repo, flow.AlertDisk); active {
		t.Fatalf("disk alert remained in state.json after retry pass: %+v", ts)
	}
	if clears := alertClearLogs(t, w.paths, w.repo, flow.AlertDisk); len(clears) != 1 {
		t.Fatalf("expected exactly 1 clear line after retry, got %d: %+v", len(clears), clears)
	}
	allLines, _, err := ProjectLog(context.Background(), w.paths, w.repo, 0)
	if err != nil {
		t.Fatalf("ProjectLog: %v", err)
	}
	for i := 1; i < len(allLines); i++ {
		if allLines[i].Seq <= allLines[i-1].Seq {
			t.Fatalf("reused or non-increasing sequence number in log.jsonl: line %d seq=%d after line %d seq=%d",
				i, allLines[i].Seq, i-1, allLines[i-1].Seq)
		}
	}
}

// Trial finding 4, test 4:
// When the store refuses writes during the raise (the disk in-memory case) and
// then recovers with the cause already gone, log.jsonl gets one raise line
// followed by one clear line in order.
func TestTrial_UnloggedRaiseThenClearLogsBothInOrder(t *testing.T) {
	w := newCDWorld(t, "Unlogged raise then clear")
	w.setDisks(
		DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 40, Known: true},
		DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 95, Known: true},
	)
	w.setStoreENOSPC(true)
	w.pass()

	if _, ok := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertDisk); !ok {
		t.Fatal("expected in-memory disk alert during store ENOSPC")
	}
	if got := alertEpisodeLogs(t, w.paths, w.repo, flow.AlertDisk); len(got) != 0 {
		t.Fatalf("expected 0 log lines while store refused writes, got %+v", got)
	}

	// Store recovers and disk drops under threshold in one step.
	w.setStoreENOSPC(false)
	w.setDisks(
		DiskReading{Volume: "/Volumes/ProjectVol", UsedPercent: 40, Known: true},
		DiskReading{Volume: "/Volumes/DataVol", UsedPercent: 60, Known: true},
	)
	w.advanceMinute()
	w.pass()

	if ts, active := cdEngineStatusAlert(t, w.eng, w.repo, flow.AlertDisk); active {
		t.Fatalf("disk alert did not clear on recovery: %+v", ts)
	}
	logs := alertEpisodeLogs(t, w.paths, w.repo, flow.AlertDisk)
	if len(logs) != 2 {
		t.Fatalf("expected 2 disk episode log lines (raise then clear), got %d: %+v", len(logs), logs)
	}
	if logs[0].Decision != "alert raised: disk" {
		t.Errorf("first line Decision = %q, want %q", logs[0].Decision, "alert raised: disk")
	}
	if logs[1].Decision != "alert cleared: disk" {
		t.Errorf("second line Decision = %q, want %q", logs[1].Decision, "alert cleared: disk")
	}
	if logs[1].Seq <= logs[0].Seq {
		t.Errorf("clear Seq %d must be > raise Seq %d", logs[1].Seq, logs[0].Seq)
	}
}

// Trial finding 4, test 5:
// A cause that raises, clears, and returns logs three lines in order:
// raise, clear, raise.
func TestTrial_SecondEpisodeLogsRaiseClearRaise(t *testing.T) {
	w := newAlertWorld(t, "Second episode raise clear raise", false)

	// Episode 1 raise.
	w.setPool("claude", &poolWin{2, 90 * time.Minute}, &poolWin{60, 72 * time.Hour})
	w.pass()

	// Episode 1 clear.
	w.advance(95 * time.Minute)
	w.setPool("claude", &poolWin{80, 4 * time.Hour}, &poolWin{60, 72 * time.Hour})
	w.pass()

	// End coordinator if it started, then cause returns for Episode 2.
	if coord := w.state().Project.RunID; coord != "" {
		w.srv.FinishExit(coord, "exited", 0)
		w.pass()
	}
	w.advance(15 * time.Minute)
	w.setPool("claude", &poolWin{1, 2 * time.Hour}, &poolWin{60, 72 * time.Hour})
	w.pass()

	logs := alertEpisodeLogs(t, w.paths, w.repo, flow.AlertNoPool)
	if len(logs) != 3 {
		t.Fatalf("expected 3 no_pool episode lines (raise, clear, raise), got %d: %+v", len(logs), logs)
	}
	if logs[0].Decision == "alert cleared: no_pool" {
		t.Errorf("line 0 must be episode 1 raise, got %+v", logs[0])
	}
	if logs[1].Decision != "alert cleared: no_pool" {
		t.Errorf("line 1 Decision = %q, want %q", logs[1].Decision, "alert cleared: no_pool")
	}
	if logs[2].Decision == "alert cleared: no_pool" {
		t.Errorf("line 2 must be episode 2 raise, got %+v", logs[2])
	}
	if logs[0].Seq >= logs[1].Seq || logs[1].Seq >= logs[2].Seq {
		t.Errorf("expected strictly increasing sequence numbers, got %d, %d, %d", logs[0].Seq, logs[1].Seq, logs[2].Seq)
	}
}

// Trial finding 4, test 6:
// Restarting while an alert is active and then clearing logs exactly one clear
// line; restarting again while clear adds none.
func TestTrial_ClearAcrossRestartLogsOnce(t *testing.T) {
	w := newAlertWorld(t, "Clear across restart", false)

	// Raise no_pool.
	w.setPool("claude", &poolWin{2, 90 * time.Minute}, &poolWin{60, 72 * time.Hour})
	w.pass()
	if _, active := statusAlert(t, w.paths, w.repo, flow.AlertNoPool); !active {
		t.Fatal("expected no_pool to be active")
	}

	// Restart while active.
	w.restart()
	w.advance(time.Minute)
	w.pass()
	if _, active := statusAlert(t, w.paths, w.repo, flow.AlertNoPool); !active {
		t.Fatal("expected no_pool to survive restart")
	}
	if clears := alertClearLogs(t, w.paths, w.repo, flow.AlertNoPool); len(clears) != 0 {
		t.Fatalf("expected 0 clear lines while still active, got %+v", clears)
	}

	// Clear after restart.
	w.advance(95 * time.Minute)
	w.setPool("claude", &poolWin{80, 4 * time.Hour}, &poolWin{60, 72 * time.Hour})
	w.pass()
	if ts, active := statusAlert(t, w.paths, w.repo, flow.AlertNoPool); active {
		t.Fatalf("no_pool did not clear after restart: %+v", ts)
	}
	if clears := alertClearLogs(t, w.paths, w.repo, flow.AlertNoPool); len(clears) != 1 {
		t.Fatalf("expected exactly 1 clear line after restart clear, got %d: %+v", len(clears), clears)
	}

	// Restart again while clear: no extra clear line.
	w.restart()
	w.advance(time.Minute)
	w.pass()
	if clears := alertClearLogs(t, w.paths, w.repo, flow.AlertNoPool); len(clears) != 1 {
		t.Fatalf("restart while clear added extra clear line: got %d (%+v)", len(clears), clears)
	}
}
