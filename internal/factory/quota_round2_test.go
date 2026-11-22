package factory

// PR 5b.7a round 2, TDD step 1: failing tests for the review findings (A-K).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// hcRPCIntercept intercepts one JSON-RPC call in front of an hctest.Server.
// forward sends the raw request line to the real hctest server and returns its
// raw response line and whether herdr-core accepted the call (error == nil).
type hcRPCIntercept func(method string, params json.RawMessage, forward func() ([]byte, bool, error)) ([]byte, bool)

type hcInterceptProxy struct {
	Path string

	mu       sync.Mutex
	hook     hcRPCIntercept
	accepted []hctest.Request
}

func newHCInterceptProxy(t *testing.T, upstream string) *hcInterceptProxy {
	t.Helper()
	dir, err := os.MkdirTemp("", "hcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := &hcInterceptProxy{Path: filepath.Join(dir, "hc.sock")}
	ln, err := net.Listen("unix", p.Path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer func() { _ = c.Close() }()
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil && len(line) == 0 {
					return
				}
				var req struct {
					ID     uint64          `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				_ = json.Unmarshal(line, &req)
				forward := func() ([]byte, bool, error) {
					up, err := net.Dial("unix", upstream)
					if err != nil {
						return nil, false, err
					}
					defer func() { _ = up.Close() }()
					if _, err := up.Write(line); err != nil {
						return nil, false, err
					}
					resp, err := bufio.NewReader(up).ReadBytes('\n')
					if err != nil {
						return nil, false, err
					}
					var wire struct {
						Error json.RawMessage `json:"error"`
					}
					ok := json.Unmarshal(resp, &wire) == nil && (len(wire.Error) == 0 || string(wire.Error) == "null")
					if ok {
						p.mu.Lock()
						p.accepted = append(p.accepted, hctest.Request{ID: req.ID, Method: req.Method, Params: slices.Clone(req.Params)})
						p.mu.Unlock()
					}
					return resp, ok, nil
				}
				p.mu.Lock()
				h := p.hook
				p.mu.Unlock()
				if h != nil {
					resp, drop := h(req.Method, req.Params, forward)
					if drop {
						return
					}
					if len(resp) > 0 {
						_, _ = c.Write(resp)
					}
					return
				}
				resp, _, err := forward()
				if err == nil {
					_, _ = c.Write(resp)
				}
			}(conn)
		}
	}()
	return p
}

func (p *hcInterceptProxy) setHook(h hcRPCIntercept) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hook = h
}

func (p *hcInterceptProxy) acceptedHandoffs(t *testing.T, address string) int {
	t.Helper()
	p.mu.Lock()
	reqs := slices.Clone(p.accepted)
	p.mu.Unlock()
	n := 0
	for _, s := range sendsTo(t, reqs, address) {
		if strings.Contains(strings.ToLower(s), quotaHandoffRequest) {
			n++
		}
	}
	return n
}

func isHandoffSendTo(method string, params json.RawMessage, address string) bool {
	if method != "terminal.send" {
		return false
	}
	var p struct {
		Terminal string `json:"terminal"`
		Text     string `json:"text"`
	}
	return json.Unmarshal(params, &p) == nil && p.Terminal == address && strings.Contains(strings.ToLower(p.Text), quotaHandoffRequest)
}

// Decision A: the handoff marker and deadline are saved before the send. A
// crash or a lost reply after the send never sends it again, while the
// deadline still stops the run; only a send that herdr-core refused before
// accepting it clears the marker.
func TestQuotaFloorHandoffAtMostOnceAcrossCrashAndLostReply(t *testing.T) {
	newProxiedHistory := func(t *testing.T) (*quotaHistory, *hcInterceptProxy) {
		t.Helper()
		q := newQuotaHistory(t, nil)
		proxy := newHCInterceptProxy(t, q.srv.Path)
		q.cl = hc.NewClient(proxy.Path)
		q.restart()
		return q, proxy
	}

	t.Run("crash after hc accepts send before post-send commit", func(t *testing.T) {
		q, proxy := newProxiedHistory(t)
		q.startWorkers(poolTask("T1", ""))
		w := q.attempt(0).RunID
		q.status(w, "working")
		q.pass()

		const handoffMinutes = 3
		q.setHandoffMinutes(handoffMinutes)
		*q.now = q.now.Add(time.Minute)
		deadline := q.now.Add(handoffMinutes * time.Minute)
		q.atFloor("claude", q.now.Add(2*time.Hour))

		var savedState, savedLog []byte
		var snapped bool
		proxy.setHook(func(method string, params json.RawMessage, forward func() ([]byte, bool, error)) ([]byte, bool) {
			resp, _, _ := forward()
			if !snapped && isHandoffSendTo(method, params, w) {
				snapped = true
				savedState, _ = os.ReadFile(q.paths.State(q.id))
				savedLog, _ = os.ReadFile(q.paths.Log(q.id))
			}
			return resp, false
		})
		q.pass()
		proxy.setHook(nil)
		if !snapped || proxy.acceptedHandoffs(t, w) != 1 {
			t.Fatalf("fixture did not intercept accepted handoff send: snapped=%v accepted=%d", snapped, proxy.acceptedHandoffs(t, w))
		}

		// Simulate a crash right after herdr-core accepted terminal.send and
		// before any post-send state commit reached disk.
		if err := os.WriteFile(q.paths.State(q.id), savedState, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(q.paths.Log(q.id), savedLog, 0o600); err != nil {
			t.Fatal(err)
		}
		q.restart()
		q.pass()
		if got := proxy.acceptedHandoffs(t, w); got != 1 {
			t.Errorf("after crash at send/commit boundary: %d accepted handoff sends, want 1", got)
		}

		*q.now = deadline.Add(-time.Second)
		q.pass()
		q.assertLive(w)

		*q.now = deadline
		q.pass()
		a := q.attempt(0)
		if a.Run.Result != flow.EndQuota || !a.Run.EndedAt.Equal(deadline) || !q.stopped(w) || proxy.acceptedHandoffs(t, w) != 1 {
			t.Errorf("at deadline after crash: result=%q endedAt=%v stopped=%v handoffs=%d, want quota at %v and 1 handoff",
				a.Run.Result, a.Run.EndedAt, q.stopped(w), proxy.acceptedHandoffs(t, w), deadline)
		}
	})

	t.Run("lost reply after hc accepts send", func(t *testing.T) {
		q, proxy := newProxiedHistory(t)
		q.startWorkers(poolTask("T1", ""))
		w := q.attempt(0).RunID
		q.status(w, "working")
		q.pass()

		const handoffMinutes = 3
		q.setHandoffMinutes(handoffMinutes)
		*q.now = q.now.Add(time.Minute)
		deadline := q.now.Add(handoffMinutes * time.Minute)
		q.atFloor("claude", q.now.Add(2*time.Hour))

		dropped := false
		proxy.setHook(func(method string, params json.RawMessage, forward func() ([]byte, bool, error)) ([]byte, bool) {
			resp, _, _ := forward()
			if !dropped && isHandoffSendTo(method, params, w) {
				dropped = true
				return nil, true // hc accepted the send, then the reply connection dropped
			}
			return resp, false
		})
		q.pass()
		proxy.setHook(nil)
		if !dropped || proxy.acceptedHandoffs(t, w) != 1 {
			t.Fatalf("fixture did not drop reply after accepted send: dropped=%v accepted=%d", dropped, proxy.acceptedHandoffs(t, w))
		}

		q.pass()
		*q.now = q.now.Add(time.Minute)
		q.pass()
		q.restart()
		if got := proxy.acceptedHandoffs(t, w); got != 1 {
			t.Errorf("after lost reply: %d accepted handoff sends, want 1", got)
		}

		*q.now = deadline.Add(-time.Second)
		q.pass()
		q.assertLive(w)

		*q.now = deadline
		q.pass()
		a := q.attempt(0)
		if a.Run.Result != flow.EndQuota || !a.Run.EndedAt.Equal(deadline) || !q.stopped(w) || proxy.acceptedHandoffs(t, w) != 1 {
			t.Errorf("at deadline after lost reply: result=%q endedAt=%v stopped=%v handoffs=%d, want quota at %v and 1 handoff",
				a.Run.Result, a.Run.EndedAt, q.stopped(w), proxy.acceptedHandoffs(t, w), deadline)
		}
	})

	t.Run("send refused before accept clears marker", func(t *testing.T) {
		q, proxy := newProxiedHistory(t)
		q.startWorkers(poolTask("T1", ""))
		w := q.attempt(0).RunID
		q.status(w, "working")
		q.pass()

		const handoffMinutes = 3
		q.setHandoffMinutes(handoffMinutes)
		*q.now = q.now.Add(time.Minute)
		q.atFloor("claude", q.now.Add(2*time.Hour))

		refusedOnce := false
		proxy.setHook(func(method string, params json.RawMessage, forward func() ([]byte, bool, error)) ([]byte, bool) {
			if !refusedOnce && isHandoffSendTo(method, params, w) {
				refusedOnce = true
				// Worker hits a permission prompt after ObserveRun: herdr-core
				// refuses non-forced terminal.send with ErrBlocked before accepting.
				q.status(w, "blocked")
			}
			resp, _, _ := forward()
			return resp, false
		})
		q.pass()
		proxy.setHook(nil)
		if !refusedOnce || proxy.acceptedHandoffs(t, w) != 0 {
			t.Fatalf("fixture send was not refused before accept: refusedOnce=%v accepted=%d", refusedOnce, proxy.acceptedHandoffs(t, w))
		}

		// Once the worker is working again before the deadline, the cleared
		// marker allows the one handoff to be delivered.
		q.status(w, "working")
		*q.now = q.now.Add(15 * time.Second)
		q.pass()
		q.pass()
		if got := proxy.acceptedHandoffs(t, w); got != 1 {
			t.Errorf("after herdr-core refused the first send and worker resumed: %d accepted handoffs, want 1", got)
		}
	})
}

// Decision B: a replacement run in the same attempt on a different pool gets
// its own handoff when that pool reaches its floor; the same pool never gets
// one twice in one attempt.
func TestQuotaFloorHandoffOncePerAttemptAndPool(t *testing.T) {
	q := newQuotaHistory(t, quotaWorkerCandidates)
	q.srv.OnExec = hctest.GitExec
	q.startWorkers(poolTask("T1", ""))

	// 1. First worker in attempt 1 runs on Claude and gets one handoff at floor.
	w1 := q.attempt(0).RunID
	q.status(w1, "working")
	q.pass()
	*q.now = q.now.Add(2 * time.Minute)
	q.atFloor("claude", q.now.Add(30*time.Minute))
	q.pools.set("codex", poolOut("codex", *q.now, nil, &poolWin{70, 72 * time.Hour}, nil))
	q.pass()
	if got := q.handoffs(w1); got != 1 {
		t.Fatalf("first claude run in attempt 1: %d handoffs, want 1", got)
	}
	q.status(w1, "idle")
	q.turnEnds()
	q.pass()

	// 2. Replacement worker in the same attempt 1 runs on Codex; when Codex
	// reaches its floor, Codex gets its own handoff once.
	a2 := q.attempt(0)
	if n := len(q.state().Tasks[0].Attempts); n != 1 || a2.Run == nil || a2.Run.CLI != "codex" || a2.RunID == "" || a2.RunID == w1 {
		t.Fatalf("replacement 1 = %d attempts, run %+v id %q, want 1 open attempt on codex", n, a2.Run, a2.RunID)
	}
	w2 := a2.RunID
	q.srv.SetScreen(w2, codexWorkingScreen("Continue from Claude handoff"))
	q.pass()
	if q.attempt(0).Run.State != flow.PhaseWorking {
		t.Fatalf("codex replacement state = %q, want working", q.attempt(0).Run.State)
	}

	*q.now = q.now.Add(2 * time.Minute)
	q.pools.set("claude", poolOut("claude", *q.now, &poolWin{80, 3 * time.Hour}, &poolWin{60, 72 * time.Hour}, nil))
	q.atFloor("codex", q.now.Add(30*time.Minute))
	q.pass()
	q.restart()
	q.pass()
	if got := q.handoffs(w2); got != 1 {
		t.Errorf("codex replacement on a different pool in attempt 1: %d handoffs, want 1", got)
	}

	q.srv.SetScreen(w2, codexIdleScreen("Wrote Codex handoff", 14))
	q.turnEnds()
	q.pass()

	// 3. Replacement worker back on Claude in the same attempt 1: when Claude
	// hits its floor again, Claude must NOT get a second handoff in attempt 1,
	// while the floor deadline still ends the run with quota.
	a3 := q.attempt(0)
	if n := len(q.state().Tasks[0].Attempts); n != 1 || a3.Run == nil || a3.Run.CLI != "claude" || a3.RunID == "" || a3.RunID == w1 || a3.RunID == w2 {
		t.Fatalf("replacement 2 = %d attempts, run %+v id %q, want 1 open attempt back on claude", n, a3.Run, a3.RunID)
	}
	w3 := a3.RunID
	q.status(w3, "working")
	q.pass()
	const handoffMinutes = 3
	q.setHandoffMinutes(handoffMinutes)
	*q.now = q.now.Add(2 * time.Minute)
	deadline := q.now.Add(handoffMinutes * time.Minute)
	q.atFloor("claude", q.now.Add(30*time.Minute))
	q.atFloor("codex", q.now.Add(2*time.Hour))
	q.pass()
	q.restart()
	*q.now = deadline.Add(-time.Second)
	q.pass()
	if got := q.handoffs(w3); got != 0 {
		t.Errorf("second claude run in same attempt 1: %d handoffs, want 0 (never twice on the same pool in one attempt)", got)
	}
	q.assertLive(w3)

	*q.now = deadline
	q.pass()
	end := q.attempt(0)
	if got := q.handoffs(w3); got != 0 {
		t.Errorf("second claude run at deadline: %d handoffs, want 0", got)
	}
	if end.Run.Result != flow.EndQuota || !q.stopped(w3) {
		t.Errorf("second claude run at deadline: state=%q result=%q stopped=%v, want ended(quota)", end.Run.State, end.Run.Result, q.stopped(w3))
	}
}

// Decision C: on a pass where the floor is seen, the handoff deadline is set
// before timeouts are judged. While a handoff deadline is pending, R29
// (blocked too long), starting and quiet timeouts do not end the run; it ends
// with quota at the deadline or on the next idle.
func TestQuotaFloorDeadlinePrecedesAndHoldsTimeouts(t *testing.T) {
	t.Run("R29 blocked timeout yields to floor deadline", func(t *testing.T) {
		q := newQuotaHistory(t, nil)
		q.edit(func(s *flow.State) { s.Settings.BlockedMinutes = 2 })
		q.setHandoffMinutes(5)
		q.startWorkers(poolTask("T1", ""))
		w := q.attempt(0).RunID
		q.status(w, "blocked")
		q.pass()
		if q.attempt(0).Run.State != flow.PhaseBlocked {
			t.Fatal("fixture worker must be blocked")
		}

		// BlockedMinutes elapses on the exact pass the floor is first seen.
		*q.now = q.now.Add(2*time.Minute + 5*time.Second)
		deadline := q.now.Add(5 * time.Minute)
		q.atFloor("claude", q.now.Add(2*time.Hour))
		q.pass()
		if a := q.attempt(0); a.Run.State == flow.PhaseEnded || q.stopped(w) {
			t.Fatalf("floor pass ended blocked worker via R29 instead of setting handoff deadline: state=%q result=%q", a.Run.State, a.Run.Result)
		}

		// Past further BlockedMinutes ticks while the handoff deadline is pending.
		*q.now = deadline.Add(-time.Second)
		q.pass()
		q.assertLive(w)

		*q.now = deadline
		q.pass()
		if a := q.attempt(0); a.Run.Result != flow.EndQuota || !q.stopped(w) {
			t.Errorf("blocked worker at handoff deadline: state=%q result=%q stopped=%v, want ended(quota)", a.Run.State, a.Run.Result, q.stopped(w))
		}
	})

	t.Run("R11 starting timeout yields to floor deadline", func(t *testing.T) {
		q := newQuotaHistory(t, nil)
		q.edit(func(s *flow.State) { s.Settings.StartMinutes = 2 })
		q.setHandoffMinutes(5)
		q.startWorkers(poolTask("T1", ""))
		w := q.attempt(0).RunID
		if q.attempt(0).Run.State != flow.PhaseStarting {
			t.Fatal("fixture worker must stay starting")
		}

		// StartMinutes elapses on the exact pass the floor is first seen.
		*q.now = q.now.Add(2*time.Minute + 5*time.Second)
		deadline := q.now.Add(5 * time.Minute)
		q.atFloor("claude", q.now.Add(2*time.Hour))
		q.pass()
		if a := q.attempt(0); a.Run.State == flow.PhaseEnded || q.stopped(w) {
			t.Fatalf("floor pass ended starting worker via R11 instead of holding until handoff deadline: state=%q result=%q", a.Run.State, a.Run.Result)
		}

		*q.now = deadline.Add(-time.Second)
		q.pass()
		q.assertLive(w)

		*q.now = deadline
		q.pass()
		if a := q.attempt(0); a.Run.Result != flow.EndQuota || !q.stopped(w) {
			t.Errorf("starting worker at handoff deadline: state=%q result=%q stopped=%v, want ended(quota)", a.Run.State, a.Run.Result, q.stopped(w))
		}
	})

	t.Run("R21 and R22 quiet timeouts yield to floor deadline and next idle", func(t *testing.T) {
		for _, endBy := range []string{"deadline", "next idle"} {
			t.Run(endBy, func(t *testing.T) {
				q := newQuotaHistory(t, nil)
				q.edit(func(s *flow.State) {
					s.Settings.QuietMinutes = 2
					s.Settings.InterruptSeconds = 20
				})
				q.setHandoffMinutes(6)
				q.startWorkers(poolTask("T1", ""))
				w := q.attempt(0).RunID
				q.srv.SetScreen(w, "⏺ Investigating tests...\n")
				q.status(w, "working")
				q.pass()

				// Trigger R21 interrupt before the floor is reached, then reach
				// R22's quiet-kill threshold on the exact pass the floor is seen.
				*q.now = q.now.Add(2*time.Minute + 5*time.Second)
				q.pass() // R21
				*q.now = q.now.Add(25 * time.Second)
				deadline := q.now.Add(6 * time.Minute)
				q.atFloor("claude", q.now.Add(2*time.Hour))
				q.pass()
				if a := q.attempt(0); a.Run.State == flow.PhaseEnded || q.stopped(w) || q.handoffs(w) != 1 {
					t.Fatalf("floor pass on quiet worker: state=%q result=%q stopped=%v handoffs=%d, want live with 1 handoff",
						a.Run.State, a.Run.Result, q.stopped(w), q.handoffs(w))
				}

				// Stay completely quiet past QuietMinutes + InterruptSeconds
				// while the handoff deadline is still pending.
				*q.now = q.now.Add(3 * time.Minute)
				q.pass()
				*q.now = q.now.Add(30 * time.Second)
				q.pass()
				if a := q.attempt(0); a.Run.State == flow.PhaseEnded || q.stopped(w) {
					t.Fatalf("quiet timeout ended worker while handoff deadline was pending: state=%q result=%q", a.Run.State, a.Run.Result)
				}

				if endBy == "next idle" {
					q.status(w, "idle")
					q.turnEnds()
					if a := q.attempt(0); a.Run.Result != flow.EndQuota || !q.stopped(w) {
						t.Errorf("on next idle with pending handoff: state=%q result=%q stopped=%v, want ended(quota)", a.Run.State, a.Run.Result, q.stopped(w))
					}
					return
				}

				*q.now = deadline.Add(-time.Second)
				q.pass()
				q.assertLive(w)

				*q.now = deadline
				q.pass()
				if a := q.attempt(0); a.Run.Result != flow.EndQuota || !q.stopped(w) {
					t.Errorf("at handoff deadline after quiet period: state=%q result=%q stopped=%v, want ended(quota)", a.Run.State, a.Run.Result, q.stopped(w))
				}
			})
		}
	})
}

// Decision D (O23): R5/R19 classification and the reset time come only from
// the adapter's own UI area (Claude: the bottom area at or below the input
// box; Codex: the footer and composer area), never from agent output above it.
// If several genuine messages show, the bottom-most counts. A quote, above or
// below in the agent's text, never ends a run or sets a time.
func TestQuotaLimitUsesOnlyAdapterUIArea(t *testing.T) {
	rawClaudeIdle, err := os.ReadFile("../../testdata/herdr/claude-idle.screen.txt")
	if err != nil {
		t.Fatal(err)
	}
	claudeIdleUI := string(rawClaudeIdle)

	const codexHeader = "\n\n  >_ OpenAI Codex (v0.171.0)\n" +
		"     /private/tmp/demo/worktrees/T1-1\n" +
		"  permissions: YOLO mode\n\n" +
		"  Tip: Maximize usage with GPT-6.1 Sol. Try it on complex work for near-Astra performance at a lower cost.\n\n" +
		"⚠ Heads up, you have less than 10% of your weekly limit left. Run /status for a breakdown.\n\n"
	const codexIdleFooter = "\n› Ask Codex to do anything\n\n" +
		"  GPT-6.2-Sol medium · /private/tmp/demo/worktrees/T1-1 · Add Nonatriaconta\n" +
		"  ← for agents · ? for shortcuts                                                                ⚠ 1 warning · f2 to view\n"

	// 1. Quotes in agent output (with no genuine limit message in the bottom UI
	// area) and quotes below an earlier turn's genuine scrollback banner must
	// never end a starting (R5) or idle (R19) run or record a cooldown.
	noLimitCases := []struct {
		name, cli, screen string
	}{
		{
			name: "claude quote in agent reply with no genuine message",
			cli:  "claude",
			screen: "❯ Show the limit examples from workerlog.\n\n" +
				"● Read(internal/workerlog/errors.go) (ctrl+o to expand)\n" +
				"  ⎿  You've hit your limit · resets 11pm (America/Los_Angeles)\n\n" +
				"⏺ You've hit your limit · resets 11pm (America/Los_Angeles)\n" +
				"  Claude AI usage limit reached · resets 11pm (America/Los_Angeles)\n\n" +
				"✻ Worked for 12s\n\n" + claudeIdleUI,
		},
		{
			name: "claude quote below earlier scrollback banner with healthy idle box",
			cli:  "claude",
			screen: "⎿  You've hit your limit · resets 1pm (America/Los_Angeles)\n\n" +
				"❯ What did that banner say?\n\n" +
				"⏺ You've hit your limit · resets 9pm (America/Los_Angeles)\n\n" + claudeIdleUI,
		},
		{
			name: "codex quote in agent reply with no genuine message",
			cli:  "codex",
			screen: codexHeader +
				"› Show the limit examples from workerlog.\n\n" +
				"• The CLI banner looks like:\n" +
				"  You've hit your usage limit · try again at 11:00 PM (America/Los_Angeles)\n" +
				"• You've hit your usage limit · try again at 11:00 PM (America/Los_Angeles)\n\n" +
				"  Worked for 12s • 8:44 PM\n" + codexIdleFooter,
		},
		{
			name: "codex quote below earlier scrollback banner with healthy composer",
			cli:  "codex",
			screen: "⚠ You've hit your usage limit · try again at 1:00 PM (America/Los_Angeles)\n\n" +
				"› What did that warning say?\n\n" +
				"• You've hit your usage limit · try again at 9:00 PM (America/Los_Angeles)\n\n" +
				"  Worked for 6s • 8:46 PM\n" + codexIdleFooter,
		},
	}

	for _, tc := range noLimitCases {
		for _, phase := range []string{"R5 starting", "R19 idle"} {
			t.Run(tc.name+"/"+phase, func(t *testing.T) {
				models := codexCoordOnly
				if tc.cli == "codex" {
					models = map[string][]flow.Candidate{
						flow.RoleCoordinator: {{CLI: "codex", Model: codexModel, Effort: "high"}},
						flow.RoleWorker:      {{CLI: "codex", Model: codexModel, Effort: "medium"}},
					}
				}
				q := newQuotaHistory(t, models)
				q.startWorkers(poolTask("T1", ""))
				w := q.attempt(0).RunID
				if phase == "R19 idle" {
					if tc.cli == "claude" {
						q.status(w, "working")
						q.pass()
						q.status(w, "idle")
						q.srv.SetScreen(w, tc.screen)
						q.turnEnds()
					} else {
						q.srv.SetScreen(w, codexWorkingScreen("Check workerlog"))
						q.pass()
						q.srv.SetScreen(w, codexIdleScreen("Check workerlog", 12))
						q.pass()
						q.srv.SetScreen(w, tc.screen)
						*q.now = q.now.Add(25 * time.Second)
						q.pass()
						q.pass()
					}
				} else {
					q.srv.SetScreen(w, tc.screen)
					q.pass()
				}

				a := q.attempt(0)
				if a.Run.State == flow.PhaseEnded || a.Run.Result == flow.EndQuota || q.stopped(w) {
					t.Errorf("quoted output ended %s (%s): state=%q result=%q, want live run", tc.cli, phase, a.Run.State, a.Run.Result)
				}
				// Schedule a probe task on the same CLI to verify no cooldown was set.
				tok := (*q.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
				req := ""
				if tc.cli == "codex" {
					req = `,"request":{"worker":[{"cli":"codex","model":"` + codexModel + `","effort":"medium"}]}`
				}
				if err := q.eng.Report(tok, "plan amend", []byte(`{"why":"probe cooldown","tasks":[`+poolTask("T2", req)+`]}`)); err != nil {
					t.Fatal(err)
				}
				q.pass()
				q.pass()
				if probe := q.state().Tasks[1]; len(probe.Attempts) != 1 || probe.HoldWhy != "" {
					t.Errorf("quoted output set a pool hold on probe task: attempts=%d hold=%q", len(probe.Attempts), probe.HoldWhy)
				}
			})
		}
	}

	// 2. Genuine limit messages in the bottom UI area: a quote above (or below
	// an earlier scrollback banner) never overrides the genuine message's time,
	// and when several genuine messages show in the bottom UI area, the
	// bottom-most counts.
	const poolMinutes = 3
	reset2PM := time.Date(2026, 11, 20, 22, 0, 0, 0, time.UTC) // 2:00 PM PST on codexRolesT0 (2026-11-20)
	reset3PM := time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC) // 3:00 PM PST on codexRolesT0

	genuineCases := []struct {
		name, cli, screen string
		wantReset         time.Time // zero means now + poolMinutes
	}{
		{
			name: "claude quote above genuine footer limit uses genuine time",
			cli:  "claude",
			screen: "⏺ You've hit your limit · resets 9pm (America/Los_Angeles)\n\n" +
				"────────────────────────────────────────\n❯ \n────────────────────────────────────────\n" +
				"  You've hit your limit · resets 2pm (America/Los_Angeles)\n",
			wantReset: reset2PM,
		},
		{
			name: "claude quote below scrollback banner and above genuine unprinted limit uses pool_minutes",
			cli:  "claude",
			screen: "⎿  You've hit your limit · resets 1pm (America/Los_Angeles)\n" +
				"❯ Explain that line.\n" +
				"⏺ You've hit your limit · resets 9pm (America/Los_Angeles)\n\n" +
				quotaClaudeScreen("You've hit your limit"),
			wantReset: time.Time{},
		},
		{
			name: "claude two genuine messages in bottom UI area uses bottom-most",
			cli:  "claude",
			screen: "⏺ You've hit your limit · resets 9pm (America/Los_Angeles)\n\n" +
				"⎿  You've hit your limit · resets 2pm (America/Los_Angeles)\n" +
				"────────────────────────────────────────\n❯ \n────────────────────────────────────────\n" +
				"  You've hit your limit · resets 3pm (America/Los_Angeles)\n",
			wantReset: reset3PM,
		},
		{
			name: "codex quote above genuine footer limit uses genuine time",
			cli:  "codex",
			screen: codexHeader +
				"• You've hit your usage limit · try again at 9:00 PM (America/Los_Angeles)\n\n" +
				"› Ask Codex to do anything\n\n" +
				"  You've hit your usage limit · try again at 2:00 PM (America/Los_Angeles)\n" +
				"  GPT-6.2-Sol medium · /private/tmp/demo/worktrees/T1-1\n",
			wantReset: reset2PM,
		},
		{
			name: "codex two genuine messages in composer-footer area uses bottom-most",
			cli:  "codex",
			screen: codexHeader +
				"• You've hit your usage limit · try again at 9:00 PM (America/Los_Angeles)\n\n" +
				"⚠ You've hit your usage limit · try again at 2:00 PM (America/Los_Angeles)\n\n" +
				"› Ask Codex to do anything\n\n" +
				"  You've hit your usage limit · try again at 3:00 PM (America/Los_Angeles)\n",
			wantReset: reset3PM,
		},
	}

	for _, tc := range genuineCases {
		t.Run(tc.name, func(t *testing.T) {
			models := codexCoordOnly
			req := ""
			if tc.cli == "codex" {
				models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "medium"}},
					flow.RoleWorker:      {{CLI: "codex", Model: codexModel, Effort: "medium"}},
				}
				req = `,"request":{"worker":[{"cli":"codex","model":"` + codexModel + `","effort":"medium"}]}`
			}
			q := newQuotaHistory(t, models)
			q.edit(func(s *flow.State) { s.Settings.PoolMinutes = poolMinutes })
			q.startWorkers(poolTask("T1", req))
			w := q.attempt(0).RunID
			if tc.cli == "claude" {
				q.status(w, "working")
				q.pass()
				q.status(w, "idle")
				q.pass()
				*q.now = q.now.Add(25 * time.Second)
				q.srv.SetScreen(w, tc.screen)
				q.pass()
			} else {
				q.srv.SetScreen(w, codexWorkingScreen("Working"))
				q.pass()
				q.srv.SetScreen(w, codexIdleScreen("Working", 12))
				q.pass()
				q.srv.SetScreen(w, tc.screen)
				*q.now = q.now.Add(25 * time.Second)
				q.pass()
			}

			if a := q.attempt(0); a.Run.Result != flow.EndQuota {
				t.Fatalf("genuine bottom UI limit did not end %s run with quota: state=%q result=%q", tc.cli, a.Run.State, a.Run.Result)
			}
			want := tc.wantReset
			if want.IsZero() {
				want = q.now.Add(poolMinutes * time.Minute)
			}

			tok := (*q.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
			if err := q.eng.Report(tok, "plan amend", []byte(`{"why":"probe reset","tasks":[`+poolTask("T2", req)+`]}`)); err != nil {
				t.Fatal(err)
			}
			*q.now = want.Add(-time.Second)
			q.pools.healthy(*q.now)
			q.pass()
			if probe := q.state().Tasks[1]; len(probe.Attempts) != 0 || !reasonHasTime(probe.HoldWhy, want) {
				t.Errorf("one second before %s: probe attempts=%d hold=%q, want held until %s",
					want.Format(time.RFC3339), len(probe.Attempts), probe.HoldWhy, want.Format(time.RFC3339))
			}
			*q.now = want
			q.pools.healthy(*q.now)
			q.pass()
			q.pass()
			if probe := q.state().Tasks[1]; len(probe.Attempts) != 1 || probe.HoldWhy != "" {
				t.Errorf("at %s: probe attempts=%d hold=%q, want probe started (quoted 9pm must not extend cooldown)",
					want.Format(time.RFC3339), len(probe.Attempts), probe.HoldWhy)
			}
		})
	}
}

// Decision E: a printed reset time already in the past (it passed during the
// idle grace or tick) gives now + pool_minutes, never tomorrow or next year.
// Text in brackets before the time zone does not stop the time from being read.
func TestQuotaResetParsingPastTimeAndBrackets(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	const poolMinutes = 3

	// 10:59:45 AM PST on Nov 21, 2026 -> R19 fires 25s later at 11:00:10 AM PST,
	// 10 seconds after the printed 11:00 AM reset.
	justBefore11AM := time.Date(2026, 11, 21, 10, 59, 45, 0, la).UTC()
	at11AMPlus10s := justBefore11AM.Add(25 * time.Second)
	wantPastFallback := at11AMPlus10s.Add(poolMinutes * time.Minute)

	noonBase := time.Date(2026, 11, 21, 12, 0, 0, 0, la).UTC()
	reset2PM := time.Date(2026, 11, 21, 14, 0, 0, 0, la).UTC()
	reset230PM := time.Date(2026, 11, 21, 14, 30, 0, 0, la).UTC()
	reset3PMUTC := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		startAt   time.Time
		cli       string
		screen    string
		wantReset time.Time
	}{
		{
			name:      "clock-only reset already passed during idle grace gives now plus pool_minutes",
			startAt:   justBefore11AM,
			cli:       "claude",
			screen:    quotaClaudeScreen("You've hit your limit · resets 11am (America/Los_Angeles)"),
			wantReset: wantPastFallback,
		},
		{
			name:      "dated reset already passed during idle grace gives now plus pool_minutes not next year",
			startAt:   justBefore11AM,
			cli:       "claude",
			screen:    quotaClaudeScreen("You've hit your limit · resets Nov 21 at 11:00 AM (America/Los_Angeles)"),
			wantReset: wantPastFallback,
		},
		{
			name:      "parenthesised window before time zone still reads reset",
			startAt:   noonBase,
			cli:       "claude",
			screen:    quotaClaudeScreen("You've hit your limit (5-hour) · resets 2pm (America/Los_Angeles)"),
			wantReset: reset2PM,
		},
		{
			name:      "square and round brackets before time zone still read reset",
			startAt:   noonBase,
			cli:       "claude",
			screen:    quotaClaudeScreen("You've hit your limit [5-hour] (Max) · resets 2:30pm (America/Los_Angeles)"),
			wantReset: reset230PM,
		},
		{
			name:      "parenthesised plan name with no time zone still reads clock time",
			startAt:   time.Date(2026, 11, 21, 13, 0, 0, 0, time.UTC),
			cli:       "codex",
			screen:    quotaCodexScreen("You've hit your usage limit (Plus) · try again at 3:00 PM"),
			wantReset: reset3PMUTC,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			models := codexCoordOnly
			req := ""
			if tc.cli == "codex" {
				models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "medium"}},
					flow.RoleWorker:      {{CLI: "codex", Model: codexModel, Effort: "medium"}},
				}
				req = `,"request":{"worker":[{"cli":"codex","model":"` + codexModel + `","effort":"medium"}]}`
			}
			q := newQuotaHistory(t, models)
			q.edit(func(s *flow.State) { s.Settings.PoolMinutes = poolMinutes })
			*q.now = tc.startAt.Add(-time.Minute)
			q.pools.healthy(*q.now)
			q.startWorkers(poolTask("T1", req))
			w := q.attempt(0).RunID

			*q.now = tc.startAt
			if tc.cli == "claude" {
				q.status(w, "working")
				q.pass()
				q.srv.SetScreen(w, tc.screen)
				q.status(w, "idle")
				q.pass()
				*q.now = tc.startAt.Add(25 * time.Second)
				q.pass()
			} else {
				q.srv.SetScreen(w, codexWorkingScreen("Working"))
				q.pass()
				q.srv.SetScreen(w, codexIdleScreen("Working", 12))
				q.pass()
				q.srv.SetScreen(w, tc.screen)
				*q.now = tc.startAt.Add(25 * time.Second)
				q.pass()
			}
			if a := q.attempt(0); a.Run.Result != flow.EndQuota {
				t.Fatalf("fixture did not end with quota via R19: state=%q result=%q", a.Run.State, a.Run.Result)
			}

			tok := (*q.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
			if err := q.eng.Report(tok, "plan amend", []byte(`{"why":"probe parsed cooldown","tasks":[`+poolTask("T2", req)+`]}`)); err != nil {
				t.Fatal(err)
			}

			*q.now = tc.wantReset.Add(-time.Second)
			q.pools.healthy(*q.now)
			q.pass()
			if probe := q.state().Tasks[1]; len(probe.Attempts) != 0 || !reasonHasTime(probe.HoldWhy, tc.wantReset) {
				t.Errorf("one second before %s: probe attempts=%d hold=%q, want held until %s",
					tc.wantReset.Format(time.RFC3339), len(probe.Attempts), probe.HoldWhy, tc.wantReset.Format(time.RFC3339))
			}

			*q.now = tc.wantReset
			q.pools.healthy(*q.now)
			q.pass()
			q.pass()
			if probe := q.state().Tasks[1]; len(probe.Attempts) != 1 || probe.HoldWhy != "" {
				t.Errorf("at %s: probe attempts=%d hold=%q, want started (never tomorrow or next year)",
					tc.wantReset.Format(time.RFC3339), len(probe.Attempts), probe.HoldWhy)
			}
		})
	}
}

// Decision F: a cooldown recorded in one project blocks that (CLI, pool) in
// every project, also after a restart; a later quota end never shortens an
// unexpired cooldown.
func TestQuotaCooldownBlocksAcrossProjectsAndNeverShortens(t *testing.T) {
	ctx := context.Background()
	q := newQuotaHistory(t, codexCoordOnly)
	q.edit(func(s *flow.State) { s.Settings.PoolMinutes = 3 })

	// Start a second project on the same Engine and Paths.
	repoB, _ := initGitRepoWithRemote(t)
	githubOrigin(t, repoB)
	_ = bindRepoFakeGH(t, repoB)
	if err := q.eng.Start(repoB, "Second project sharing pools", 2); err != nil {
		t.Fatal(err)
	}
	var idB string
	for _, d := range q.eng.projectDirs() {
		if d.Name() != q.id {
			idB = d.Name()
		}
	}
	if idB == "" {
		t.Fatal("second project directory not created")
	}
	stB, err := LoadState(q.paths.State(idB))
	if err != nil {
		t.Fatal(err)
	}
	stB.Settings.Models = codexCoordOnly
	stB.Settings.PoolMinutes = 3
	if err := SaveState(q.paths.State(idB), stB); err != nil {
		t.Fatal(err)
	}

	// Project A starts two Claude workers (T1, T2). Project B starts its Codex
	// coordinator and plans Claude task B1 before launching it.
	q.startWorkers(poolTask("T1", ""), poolTask("T2", ""))
	wA1, wA2 := q.attempt(0).RunID, q.attempt(1).RunID
	q.status(wA2, "working")
	q.pass()

	runsBeforeB := len(*q.runs)
	q.eng.passProject(ctx, idB, false) // J7 for Project B (Codex coordinator)
	coordBTok := (*q.runs)[runsBeforeB].Env["SHEEPDOG_RUN_TOKEN"]
	if err := q.eng.Report(coordBTok, "plan apply", []byte(`{"tasks":[`+poolTask("B1", "")+`]}`)); err != nil {
		t.Fatal(err)
	}

	// Worker A1 in Project A hits R5 with an 11 AM Pacific reset (19:00 UTC).
	reset11AM := time.Date(2026, 11, 20, 19, 0, 0, 0, time.UTC)
	q.srv.SetScreen(wA1, quotaClaudeScreen("You've hit your limit · resets 11am (America/Los_Angeles)"))
	q.pass()
	if a := q.attempt(0); a.Run.Result != flow.EndQuota {
		t.Fatalf("Project A worker A1 did not end with quota: %+v", a.Run)
	}

	// Project B must immediately see the Claude cooldown recorded in Project A,
	// even when CodexBar reports Claude healthy.
	*q.now = q.now.Add(4 * time.Minute)
	q.pools.healthy(*q.now)
	q.eng.passProject(ctx, idB, false)
	q.eng.passProject(ctx, idB, false)
	stB, _ = LoadState(q.paths.State(idB))
	if len(stB.Tasks[0].Attempts) != 0 || !strings.Contains(strings.ToLower(stB.Tasks[0].HoldWhy), "quota") || !reasonHasTime(stB.Tasks[0].HoldWhy, reset11AM) {
		t.Errorf("Project B task B1 after Project A quota end: attempts=%d hold=%q, want held on shared claude cooldown until %s",
			len(stB.Tasks[0].Attempts), stB.Tasks[0].HoldWhy, reset11AM.Format(time.RFC3339))
	}

	// Worker A2 in Project A hits R19 with no printed reset (fallback now+3m)
	// while the 11 AM cooldown is still unexpired: must NOT shorten it.
	q.srv.SetScreen(wA2, quotaClaudeScreen("You've hit your limit"))
	q.status(wA2, "idle")
	q.pass()
	*q.now = q.now.Add(25 * time.Second)
	q.pass()
	if a := q.attempt(1); a.Run.Result != flow.EndQuota {
		t.Fatalf("Project A worker A2 did not end with quota: %+v", a.Run)
	}

	// Jump past the fallback (now+3m) to one second before 11 AM Pacific,
	// verify both projects stay held, then restart the daemon and verify again.
	*q.now = reset11AM.Add(-time.Second)
	q.pools.healthy(*q.now)
	q.pass()
	q.eng.passProject(ctx, idB, false)

	checkBothHeld := func(stage string) {
		t.Helper()
		sA := q.state()
		sB, err := LoadState(q.paths.State(idB))
		if err != nil {
			t.Fatal(err)
		}
		if !reasonHasTime(sA.Tasks[0].HoldWhy, reset11AM) {
			t.Errorf("%s: Project A hold = %q, want unexpired %s preserved", stage, sA.Tasks[0].HoldWhy, reset11AM.Format(time.RFC3339))
		}
		if len(sB.Tasks[0].Attempts) != 0 || !reasonHasTime(sB.Tasks[0].HoldWhy, reset11AM) {
			t.Errorf("%s: Project B attempts=%d hold=%q, want held until %s", stage, len(sB.Tasks[0].Attempts), sB.Tasks[0].HoldWhy, reset11AM.Format(time.RFC3339))
		}
	}
	checkBothHeld("before restart")

	q.restart()
	q.eng.passProject(ctx, idB, true)
	q.pass()
	q.eng.passProject(ctx, idB, false)
	checkBothHeld("after restart")

	// At 11 AM Pacific, both projects unblock and launch Claude workers.
	*q.now = reset11AM
	q.pools.healthy(*q.now)
	q.pass()
	q.eng.passProject(ctx, idB, false)
	q.eng.passProject(ctx, idB, false)
	sB, _ := LoadState(q.paths.State(idB))
	if len(sB.Tasks[0].Attempts) != 1 || sB.Tasks[0].HoldWhy != "" {
		t.Errorf("at 11 AM reset: Project B attempts=%d hold=%q, want B1 started", len(sB.Tasks[0].Attempts), sB.Tasks[0].HoldWhy)
	}
}

// Decision G: the replacement's handoff includes the outgoing run's last chat
// lines (from its last screen read through herdr-core). A git read that fails
// shows "unknown (<reason>)", never "none".
func TestQuotaHandoffCarriesOutgoingChatAndUnknownGitReads(t *testing.T) {
	t.Run("carries outgoing worker last chat lines across restart", func(t *testing.T) {
		q := newQuotaHistory(t, quotaWorkerCandidates)
		q.srv.OnExec = hctest.GitExec
		q.startWorkers(poolTask("T1", ""))
		w1 := q.attempt(0).RunID
		q.status(w1, "working")
		q.pass()

		*q.now = q.now.Add(time.Minute)
		// Hold Codex at floor briefly so the outgoing terminal is stopped and
		// removed, and the daemon restarts, before the replacement launches.
		soon := q.now.Add(2 * time.Minute)
		q.atFloor("claude", q.now.Add(2*time.Hour))
		q.atFloor("codex", soon)
		q.pass()

		const chatLine1 = "Handoff summary: wired token parser in internal/demo/parse.go."
		const chatLine2 = "Next step: handle empty slice in parseToken before running ready."
		q.srv.SetScreen(w1, "❯ "+quotaHandoffRequest+"\n\n"+
			"⏺ "+chatLine1+"\n  "+chatLine2+"\n\n"+
			"────────────────────────────────────────\n❯ \n────────────────────────────────────────\n  ⏵⏵ bypass permissions on\n")
		q.status(w1, "idle")
		q.turnEnds()
		if !q.stopped(w1) || q.attempt(0).Run.Result != flow.EndQuota {
			t.Fatalf("outgoing worker did not stop with quota: stopped=%v result=%q", q.stopped(w1), q.attempt(0).Run.Result)
		}

		q.restart()
		*q.now = soon.Add(time.Minute)
		q.pools.set("codex", poolOut("codex", *q.now, nil, &poolWin{70, 72 * time.Hour}, nil))
		q.pass()
		q.pass()

		rep := q.attempt(0)
		if rep.Run == nil || rep.Run.CLI != "codex" || rep.RunID == w1 {
			t.Fatalf("replacement did not launch on codex: %+v", rep.Run)
		}
		prompt := q.last().Argv[len(q.last().Argv)-1]
		handoff := strings.TrimSpace(strings.TrimSuffix(prompt, workerPrompt(workerTemplate, q.state().Tasks[0], rep, "sheepdog")))
		for _, line := range []string{chatLine1, chatLine2} {
			if !strings.Contains(handoff, line) {
				t.Errorf("replacement handoff is missing outgoing chat line %q:\n%s", line, handoff)
			}
		}
	})

	t.Run("failed git reads show unknown with reason never none", func(t *testing.T) {
		q := newQuotaHistory(t, quotaWorkerCandidates)
		q.startWorkers(poolTask("T1", ""))
		a := q.attempt(0)
		w1 := a.RunID
		q.status(w1, "working")
		q.pass()

		*q.now = q.now.Add(time.Minute)
		q.atFloor("claude", q.now.Add(2*time.Hour))
		q.pools.set("codex", poolOut("codex", *q.now, nil, &poolWin{70, 72 * time.Hour}, nil))
		q.pass()
		q.status(w1, "idle")
		q.pass()
		*q.now = q.now.Add(25 * time.Second)

		gitServer := hctest.New(t)
		const gitReason = "worktree git index is unreadable"
		var afterInspect sync.Map
		gitServer.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
			if p.Cwd == a.Worktree && slices.Contains(p.Argv, "merge-base") {
				afterInspect.Store(a.Worktree, true)
			}
			if _, ok := afterInspect.Load(a.Worktree); ok && p.Cwd == a.Worktree && (slices.Contains(p.Argv, "log") || slices.Contains(p.Argv, "status")) {
				return hc.ExecResult{}, errors.New(gitReason)
			}
			return hctest.GitExec(p)
		}
		q.eng.git = world.NewGit(hc.NewClient(gitServer.Path), q.eng.now, nil)

		q.pass()
		rep := q.attempt(0)
		if rep.Run == nil || rep.Run.CLI != "codex" {
			t.Fatalf("replacement did not launch on codex: %+v", rep.Run)
		}
		prompt := q.last().Argv[len(q.last().Argv)-1]
		handoff := strings.TrimSpace(strings.TrimSuffix(prompt, workerPrompt(workerTemplate, q.state().Tasks[0], rep, "sheepdog")))
		for _, field := range []string{"- Branch commits: ", "- Uncommitted files: "} {
			if strings.Contains(handoff, field+"none") {
				t.Errorf("handoff rendered %q on failed git read:\n%s", field+"none", handoff)
			}
			wantPrefix := field + "unknown ("
			if !strings.Contains(handoff, wantPrefix) || !strings.Contains(handoff, gitReason) {
				t.Errorf("handoff missing %q with reason %q:\n%s", wantPrefix, gitReason, handoff)
			}
		}
	})
}

// Decision H: a launch failure while starting the replacement does not
// overwrite the ended run's quota result and reason.
func TestQuotaRecoveryKeepsResultAcrossLaunchFailure(t *testing.T) {
	t.Run("worker replacement launch failure preserves quota result and floor reason", func(t *testing.T) {
		q := newQuotaHistory(t, quotaWorkerCandidates)
		proxy := newHCInterceptProxy(t, q.srv.Path)
		q.cl = hc.NewClient(proxy.Path)
		q.restart()
		q.srv.OnExec = hctest.GitExec
		// Zero ordinary restarts: overwriting EndQuota with EndFailed would
		// immediately exhaust restarts and wake worker_failed instead of retrying.
		q.edit(func(s *flow.State) { s.Settings.WorkerRestarts = 0 })

		q.startWorkers(poolTask("T1", ""))
		w1 := q.attempt(0).RunID
		q.status(w1, "working")
		q.pass()

		// Hold Codex briefly while the outgoing Claude worker stops with EndQuota
		// so its ended record is observed before replacement launch is attempted.
		*q.now = q.now.Add(time.Minute)
		q.atFloor("claude", q.now.Add(2*time.Hour))
		q.atFloor("codex", q.now.Add(10*time.Minute))
		q.pass()
		q.status(w1, "idle")
		q.pass()
		*q.now = q.now.Add(25 * time.Second)
		q.pass() // R31 ends w1 with EndQuota and floor reason; N23 waits on pools
		if a := q.attempt(0); a.Run.Result != flow.EndQuota || !strings.Contains(a.Run.Reason, "the claude pool reached its floor") {
			t.Fatalf("outgoing worker before replacement = %+v, want quota with floor reason", a.Run)
		}

		// Codex recovers; fail the replacement terminal.run once with a transient drop.
		*q.now = q.now.Add(2 * time.Minute)
		q.pools.set("codex", poolOut("codex", *q.now, nil, &poolWin{70, 72 * time.Hour}, nil))
		failedOnce := false
		proxy.setHook(func(method string, _ json.RawMessage, forward func() ([]byte, bool, error)) ([]byte, bool) {
			if !failedOnce && method == "terminal.run" {
				failedOnce = true
				return nil, true
			}
			resp, _, _ := forward()
			return resp, false
		})
		q.pass()
		proxy.setHook(nil)
		if !failedOnce {
			t.Fatal("fixture did not fail replacement terminal.run")
		}

		a := q.attempt(0)
		if a.Run == nil || a.Run.Result != flow.EndQuota || !strings.Contains(a.Run.Reason, "the claude pool reached its floor") {
			t.Errorf("after failed replacement launch: run = %+v, want Result=%q and floor Reason preserved", a.Run, flow.EndQuota)
		}

		// After ensure back-off, replacement launches without waking worker_failed
		// and still receives the outgoing floor reason in its handoff.
		*q.now = q.now.Add(3 * time.Minute)
		q.pass()
		q.pass()
		st := q.state()
		rep := st.Tasks[0].Attempts[0]
		if slices.ContainsFunc(st.Project.Wakes, func(w flow.Wake) bool { return strings.Contains(w.Key, "worker_failed") }) {
			t.Errorf("transient launch failure after quota end queued worker_failed: %+v", st.Project.Wakes)
		}
		if rep.Run == nil || rep.Run.CLI != "codex" || rep.Restarts != 0 {
			t.Fatalf("after backoff: run=%+v restarts=%d, want codex replacement with 0 restarts spent", rep.Run, rep.Restarts)
		}
		prompt := q.last().Argv[len(q.last().Argv)-1]
		if !strings.Contains(prompt, "the claude pool reached its floor") {
			t.Errorf("replacement prompt lost outgoing floor reason:\n%s", prompt)
		}
	})

	t.Run("reviewer replacement launch failure preserves quota result and retry budget", func(t *testing.T) {
		isolateQuotaFixtureGit(t)
		ctx := context.Background()
		srv, eng, paths, id, head, _, runs, _ := codexSetup(t)
		proxy := newHCInterceptProxy(t, srv.Path)
		eng.HC = hc.NewClient(proxy.Path)
		now := time.Now().Add(time.Second).Truncate(time.Second)
		eng.Now = func() time.Time { return now }
		pools := newPoolFake(t)
		pools.healthy(now)
		attachQuotaPools(eng, pools)

		st, _ := LoadState(paths.State(id))
		st.Settings.ReviewerRetries = 0
		st.Settings.PoolMinutes = 1
		if err := SaveState(paths.State(id), st); err != nil {
			t.Fatal(err)
		}

		eng.passProject(ctx, id, false) // N26
		eng.passProject(ctx, id, false) // N29 starts Codex round 2 reviewer
		st, _ = LoadState(paths.State(id))
		rev := st.Tasks[0].Attempts[0].Review
		if rev == nil || rev.Reviewer.CLI != "codex" || rev.RunID == "" {
			t.Fatalf("fixture did not start Codex round 2 reviewer: %+v", rev)
		}
		revID := rev.RunID
		before := roleRuns(*runs, flow.RoleReviewer)

		// Hold Claude briefly while Codex reviewer hits R5 so the ended quota
		// state is observed cleanly before N29 launches the backup reviewer.
		srv.SetScreen(revID, quotaCodexScreen("You've hit your usage limit."))
		now = now.Add(2 * time.Minute)
		pools.set("claude", poolOut("claude", now, &poolWin{2, 10 * time.Minute}, &poolWin{60, 72 * time.Hour}, nil))
		eng.passProject(ctx, id, false)
		st, _ = LoadState(paths.State(id))
		if r := st.Tasks[0].Attempts[0].Review; r == nil || r.Run == nil || r.Run.Result != flow.EndQuota {
			t.Fatalf("reviewer did not end with quota: %+v", r)
		}

		// Now Claude recovers and N29 tries to launch the replacement reviewer,
		// hitting a transient terminal.run failure.
		now = now.Add(2 * time.Minute)
		pools.healthy(now)
		failedOnce := false
		proxy.setHook(func(method string, _ json.RawMessage, forward func() ([]byte, bool, error)) ([]byte, bool) {
			if !failedOnce && method == "terminal.run" {
				failedOnce = true
				return nil, true
			}
			resp, _, _ := forward()
			return resp, false
		})
		eng.passProject(ctx, id, false)
		proxy.setHook(nil)
		if !failedOnce {
			t.Fatal("fixture did not fail reviewer replacement terminal.run")
		}

		st, _ = LoadState(paths.State(id))
		if r := st.Tasks[0].Attempts[0].Review; r == nil || r.Run == nil || r.Run.Result != flow.EndQuota {
			t.Errorf("after failed reviewer replacement launch: review=%+v, want Run.Result=%q preserved", r, flow.EndQuota)
		}

		now = now.Add(3 * time.Minute)
		pools.healthy(now)
		eng.passProject(ctx, id, false)
		st, _ = LoadState(paths.State(id))
		r := st.Tasks[0].Attempts[0].Review
		if failedWake(st) || r == nil || r.Round != 2 || r.Head != head || r.Retries != 0 || roleRuns(*runs, flow.RoleReviewer) != before+1 {
			t.Errorf("after reviewer backoff: review=%+v launches=%d (was %d) failedWake=%v, want round 2 started with 0 retries spent",
				r, roleRuns(*runs, flow.RoleReviewer), before, failedWake(st))
		}
	})
}

// Decision I: the default Claude coordinator (R19 quota end while working)
// requeues its in-flight wake, the same as Codex (R32).
func TestQuotaDefaultClaudeCoordinatorRequeuesWake(t *testing.T) {
	q := newQuotaHistory(t, nil) // default models: claude/opus coordinator
	q.edit(func(s *flow.State) { s.Settings.CoordinatorGapMinutes = 1 })
	q.pass() // J7 starts default Claude coordinator
	p0 := q.state().Project
	if p0.Run == nil || p0.Run.CLI != "claude" || p0.RunID == "" {
		t.Fatalf("default coordinator = %+v, want claude", p0.Run)
	}
	coord := p0.RunID

	// Finish initial planning turn so the coordinator is PhaseIdle.
	q.status(coord, "working")
	q.pass()
	q.status(coord, "idle")
	q.turnEnds()
	if q.state().Project.Run.State != flow.PhaseIdle {
		t.Fatalf("coordinator state = %q, want idle", q.state().Project.Run.State)
	}

	// Queue a wake and let J12 deliver it to start a coordinator turn.
	const key = "worker_failed"
	q.edit(func(s *flow.State) {
		s.Project.Wakes = []flow.Wake{{Task: "T1", Key: key, Why: "worker needs coordinator decision", At: *q.now}}
	})
	q.pass()
	if p := q.state().Project; len(p.CoordWakesInFlight) != 1 || len(p.Wakes) != 0 {
		t.Fatalf("J12 did not put wake in flight: queued=%+v inFlight=%+v", p.Wakes, p.CoordWakesInFlight)
	}

	// Coordinator works on the wake, then hits Claude's usage limit and leaves
	// working -> idle (which increments herdr-core's Turn counter during grace).
	q.status(coord, "working")
	q.pass()
	*q.now = q.now.Add(time.Minute)
	q.pools.healthy(*q.now)
	q.srv.SetScreen(coord, quotaClaudeScreen("You've hit your limit."))
	q.status(coord, "idle")
	q.pass() // observed during idle_grace_seconds

	*q.now = q.now.Add(25 * time.Second)
	reset := q.now.Add(time.Minute)
	q.pass() // R19 ends coordinator with quota; J7 held by cooldown
	if !q.stopped(coord) || len(logRules(t, q.paths.Log(q.id), "R19")) != 1 {
		t.Fatal("default Claude coordinator did not take R19 quota end")
	}

	p := q.state().Project
	if len(p.Wakes) != 1 || p.Wakes[0].Key != key || len(p.CoordWakesInFlight) != 0 {
		t.Errorf("after Claude coordinator R19: queued=%+v inFlight=%+v, want %q requeued once", p.Wakes, p.CoordWakesInFlight, key)
	}

	q.restart()
	if n := roleRuns(*q.runs, flow.RoleCoordinator); n != 1 {
		t.Errorf("coordinator launches during cooldown = %d, want 1", n)
	}

	*q.now = reset
	q.pools.healthy(*q.now)
	q.pass()
	rep := q.state().Project.RunID
	if rep == "" || rep == coord {
		t.Fatalf("no replacement Claude coordinator at reset: %q", rep)
	}
	q.status(rep, "working")
	q.pass()
	q.status(rep, "idle")
	q.turnEnds()
	sends := sendsTo(t, q.srv.Requests(), rep)
	if len(sends) != 1 || !strings.Contains(sends[0], key) {
		t.Errorf("replacement Claude coordinator sends = %q, want requeued wake %q once", sends, key)
	}
}

// Decision J: a quota wait (task, reviewer, coordinator J7) is logged once per
// episode, independent of the displayed time (the day disappearing at 24 h does
// not log again) and across a restart.
func TestQuotaWaitLoggedOncePerEpisode(t *testing.T) {
	t.Run("task", func(t *testing.T) {
		q := newQuotaHistory(t, nil)
		poolPlan(q.codexRoles, poolTask("T1", ""))

		*q.now = q.now.Add(2 * time.Minute)
		reset := q.now.Add(26 * time.Hour) // > 24h: weekday shown
		q.atFloor("claude", reset)
		q.pass()
		q.pass()

		*q.now = q.now.Add(30 * time.Minute)
		q.atFloor("claude", reset)
		q.restart()
		q.pass()

		// Cross inside 24h for the same reset episode: weekday disappears.
		*q.now = reset.Add(-23 * time.Hour)
		q.atFloor("claude", reset)
		q.pass()
		q.restart()
		q.pass()

		if waits := q.quotaWaits(); len(waits) != 1 {
			t.Errorf("task quota wait logged %d times across 24h boundary and restarts, want 1: %+v", len(waits), waits)
		}
	})

	t.Run("reviewer", func(t *testing.T) {
		isolateQuotaFixtureGit(t)
		ctx := context.Background()
		srv, eng, paths, id, _, _, _, _ := codexSetup(t)
		now := codexRolesT0
		eng.Now = func() time.Time { return now }
		pools := newPoolFake(t)
		attachQuotaPools(eng, pools)
		st, _ := LoadState(paths.State(id))
		st.Settings.PoolMinutes = 1
		if err := SaveState(paths.State(id), st); err != nil {
			t.Fatal(err)
		}

		reset := now.Add(26 * time.Hour) // > 24h: weekday shown
		setBothAtFloor := func(at time.Time) {
			rem := reset.Sub(at)
			pools.set("claude", poolOut("claude", at, &poolWin{2, rem}, &poolWin{60, 168 * time.Hour}, nil))
			pools.set("codex", poolOut("codex", at, nil, &poolWin{2, rem}, nil))
		}
		setBothAtFloor(now)
		eng.passProject(ctx, id, false) // N26
		eng.passProject(ctx, id, false) // N29 waits

		restartReviewerEng := func() {
			eng = newGitTestEngine(t, paths, hc.NewClient(srv.Path))
			eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
			eng.Now = func() time.Time { return now }
			attachQuotaPools(eng, pools)
			eng.passProject(ctx, id, true)
		}

		now = now.Add(30 * time.Minute)
		setBothAtFloor(now)
		restartReviewerEng()
		eng.passProject(ctx, id, false)

		// Cross inside 24h for the same reset episode: weekday disappears.
		now = reset.Add(-23 * time.Hour)
		setBothAtFloor(now)
		eng.passProject(ctx, id, false)
		restartReviewerEng()
		eng.passProject(ctx, id, false)

		var waits []LogLine
		for _, l := range logRules(t, paths.Log(id), "N29") {
			if l.Decision == "waiting" {
				waits = append(waits, l)
			}
		}
		if len(waits) != 1 {
			t.Errorf("reviewer quota wait logged %d times across 24h boundary and restarts, want 1: %+v", len(waits), waits)
		}
	})

	t.Run("coordinator J7", func(t *testing.T) {
		q := newQuotaHistory(t, nil)
		reset := q.now.Add(26 * time.Hour) // > 24h: weekday shown
		q.atFloor("claude", reset)
		q.pass()
		q.pass()

		*q.now = q.now.Add(30 * time.Minute)
		q.atFloor("claude", reset)
		q.restart()
		q.pass()

		// Cross inside 24h for the same reset episode: weekday disappears.
		*q.now = reset.Add(-23 * time.Hour)
		q.atFloor("claude", reset)
		q.pass()
		q.restart()
		q.pass()

		var waits []LogLine
		for _, l := range logRules(t, q.paths.Log(q.id), "J7") {
			if l.Decision == "waiting" {
				waits = append(waits, l)
			}
		}
		if len(waits) != 1 {
			t.Errorf("coordinator J7 quota wait logged %d times across 24h boundary and restarts, want 1: %+v", len(waits), waits)
		}
	})
}

// Decision K: "All reviewers are out of quota" only when every otherwise
// eligible reviewer is quota-blocked; a mixed set (one out of quota, one signed
// out) shows each reason.
func TestQuotaReviewerWordingMixedVsAllOutOfQuota(t *testing.T) {
	for _, order := range []string{"claude then codex", "codex then claude"} {
		t.Run(order, func(t *testing.T) {
			isolateQuotaFixtureGit(t)
			ctx := context.Background()
			_, eng, paths, id, _, _, runs, _ := codexSetup(t)
			now := codexRolesT0
			eng.Now = func() time.Time { return now }
			pools := newPoolFake(t)
			attachQuotaPools(eng, pools)

			// Sign Codex out by pointing CODEX_HOME to an empty directory and
			// removing any cached link in the engine's scoped codex-home.
			emptyCodexHome := t.TempDir()
			t.Setenv("CODEX_HOME", emptyCodexHome)
			_ = os.RemoveAll(filepath.Join(paths.DataDir, "factory", "codex-home"))

			// Include claude/sonnet as a third candidate that is ineligible
			// because the worker already used claude/sonnet (ReviewerDiffers).
			candidates := []flow.Candidate{
				{CLI: "claude", Model: "opus", Effort: "medium"},
				{CLI: "codex", Model: codexModel, Effort: "medium"},
				{CLI: "claude", Model: "sonnet", Effort: "medium"},
			}
			if order == "codex then claude" {
				candidates[0], candidates[1] = candidates[1], candidates[0]
			}
			st, _ := LoadState(paths.State(id))
			st.Settings.PoolMinutes = 1
			st.Settings.Models = map[string][]flow.Candidate{flow.RoleReviewer: candidates}
			if err := SaveState(paths.State(id), st); err != nil {
				t.Fatal(err)
			}

			// 1. Mixed set: Claude is out of quota, Codex is signed out.
			claudeReset := now.Add(2 * time.Hour)
			pools.set("claude", poolOut("claude", now, &poolWin{2, 2 * time.Hour}, &poolWin{60, 72 * time.Hour}, nil))
			pools.set("codex", poolOut("codex", now, nil, &poolWin{70, 72 * time.Hour}, nil))
			before := roleRuns(*runs, flow.RoleReviewer)
			eng.passProject(ctx, id, false) // N26
			eng.passProject(ctx, id, false) // N29 waits

			st, _ = LoadState(paths.State(id))
			if n := roleRuns(*runs, flow.RoleReviewer); n != before {
				t.Fatalf("reviewer started under mixed blockers: %d new runs", n-before)
			}
			why := st.Tasks[0].HoldWhy
			low := strings.ToLower(why)
			if strings.Contains(low, "all reviewers are out of quota") {
				t.Errorf("mixed reviewer blockers used all-out-of-quota wording: %q", why)
			}
			if !strings.Contains(low, "quota") || !reasonHasTime(why, claudeReset) {
				t.Errorf("mixed reviewer hold %q missing Claude quota reason naming %s", why, claudeReset.Format(time.RFC3339))
			}
			if !strings.Contains(low, "codex") || !strings.Contains(low, "not signed in") {
				t.Errorf("mixed reviewer hold %q missing Codex signed-out reason", why)
			}

			// 2. Sign Codex in and put Codex at its floor too (while ineligible
			// claude/sonnet remains in the list): now every otherwise eligible
			// reviewer is quota-blocked, so "All reviewers are out of quota" applies.
			if err := os.WriteFile(filepath.Join(emptyCodexHome, "auth.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			now = now.Add(2 * time.Minute)
			soonest := now.Add(45 * time.Minute)
			pools.set("claude", poolOut("claude", now, &poolWin{2, claudeReset.Sub(now)}, &poolWin{60, 72 * time.Hour}, nil))
			pools.set("codex", poolOut("codex", now, nil, &poolWin{2, 45 * time.Minute}, nil))
			eng.passProject(ctx, id, false)

			st, _ = LoadState(paths.State(id))
			whyAll := st.Tasks[0].HoldWhy
			if !strings.HasPrefix(whyAll, "All reviewers are out of quota; retries at ") || !reasonHasTime(whyAll, soonest) || reasonHasTime(whyAll, claudeReset) {
				t.Errorf("when all eligible reviewers are quota-blocked: hold = %q, want 'All reviewers are out of quota; retries at ...' naming soonest %s",
					whyAll, soonest.Format(time.RFC3339))
			}
		})
	}
}
