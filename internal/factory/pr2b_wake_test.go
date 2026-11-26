package factory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// lastSendTo returns the text of the most recent terminal.send to term.
func lastSendTo(t *testing.T, reqs []hctest.Request, term string) string {
	t.Helper()
	sends := sendsTo(t, reqs, term)
	if len(sends) == 0 {
		t.Fatalf("no terminal.send recorded for %s", term)
	}
	return sends[len(sends)-1]
}

// formatPromptEcho renders a multi-line submitted prompt using the CLI's real
// capture layout: prompt glyph on line 1 and two-space indent on continuations.
func formatPromptEcho(glyph, msg string) string {
	lines := strings.Split(strings.TrimRight(msg, "\n"), "\n")
	var b strings.Builder
	for i, ln := range lines {
		if i == 0 {
			b.WriteString(glyph + " " + ln + "\n")
		} else {
			b.WriteString("  " + ln + "\n")
		}
	}
	return b.String()
}

// completedWakeScreen builds a completed wake turn screen from the actual
// recorded terminal.send, following real captures:
//   - Claude: testdata/herdr/claude-after-approval.screen.txt & claude-idle.screen.txt
//   - Codex:  internal/factory/testdata/codex-idle.screen.txt
//   - agy:    internal/factory/world/testdata/agy-esc-active.screen.txt & agy-esc-handled.screen.txt
func completedWakeScreen(cli, sent, reply string) string {
	switch cli {
	case world.CodexCLI:
		return formatPromptEcho("›", sent) +
			"\n• " + reply + "\n\n  Worked for 17s • 8:44 PM\n\n› Ask Codex to do anything\n\n  gpt-6.2-sol high · 96% left\n"
	case world.AgyCLI:
		return "────────────────────────────────────────────────────────────\n" +
			formatPromptEcho(">", sent) + "\n  " + reply + "\n" + agyIdleBox
	default:
		return formatPromptEcho("❯", sent) +
			"\n⏺ " + reply + "\n\n✻ Cogitated for 4s · done 4:59 PM\n" +
			"────────────────────────────────────────────────────────────────────────────────\n❯ \n────────────────────────────────────────────────────────────────────────────────\n"
	}
}

// bindAgyCaptureWake replaces the first captured `> Wake: ...` header line in a
// real agy screen capture with the actual delivered terminal.send header line,
// preserving the rest of the captured screen verbatim.
func bindAgyCaptureWake(capture, sent string) string {
	header, _, _ := strings.Cut(strings.TrimRight(sent, "\n"), "\n")
	return strings.Replace(capture, "> Wake: 1 task wake(s) wait on you.", "> "+header, 1)
}

func wakeHeaderSeq(t *testing.T, msg string) int64 {
	t.Helper()
	line, _, _ := strings.Cut(msg, "\n")
	rest, ok := strings.CutPrefix(line, flow.CoordWakePromptPrefix+"#")
	if !ok {
		t.Fatalf("wake header %q missing numbered prefix %q", line, flow.CoordWakePromptPrefix+"#")
	}
	num, _, ok := strings.Cut(rest, " ")
	if !ok || num == "" {
		t.Fatalf("wake header %q missing sequence space separator", line)
	}
	seq, err := strconv.ParseInt(num, 10, 64)
	if err != nil || seq <= 0 {
		t.Fatalf("wake header %q has invalid sequence %q: %v", line, num, err)
	}
	return seq
}

func readHerdrFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// hcReadProxy wraps an hctest.Server Unix socket to simulate real herdr-core
// read behavior:
//   - viewportLines > 0: terminal.read with lines == 0 returns only the bottom
//     viewportLines lines, while lines > 0 passes through to the server's
//     scrollback slicing.
//   - failReads / failReadLines: return an RPC error on matching terminal.read.
//   - failGet: return an RPC error on terminal.get (herdr-core outage).
type hcReadProxy struct {
	path          string
	viewportLines int
	failReads     atomic.Bool
	failReadLines atomic.Int32 // when > 0, fail only terminal.read with this lines count
	failGet       atomic.Bool

	mu        sync.Mutex
	readLines []int
}

func newHCReadProxy(t *testing.T, target string, viewportLines int) *hcReadProxy {
	t.Helper()
	dir, err := os.MkdirTemp("", "hrp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	p := &hcReadProxy{path: ln.Addr().String(), viewportLines: viewportLines}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.handle(c, target)
		}
	}()
	return p
}

func (p *hcReadProxy) recordedReads() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.readLines)
}

func (p *hcReadProxy) handle(c net.Conn, target string) {
	defer func() { _ = c.Close() }()
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return
	}
	var req struct {
		ID     uint64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(line, &req)
	if req.Method == "terminal.get" && p.failGet.Load() {
		_, _ = fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"hc unreachable"}}`+"\n", req.ID)
		return
	}
	var readParams struct {
		Lines int `json:"lines"`
	}
	isRead := req.Method == "terminal.read"
	if isRead {
		_ = json.Unmarshal(req.Params, &readParams)
		p.mu.Lock()
		p.readLines = append(p.readLines, readParams.Lines)
		p.mu.Unlock()
		if p.failReads.Load() || (p.failReadLines.Load() > 0 && readParams.Lines == int(p.failReadLines.Load())) {
			_, _ = fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"read failed"}}`+"\n", req.ID)
			return
		}
	}
	up, err := net.Dial("unix", target)
	if err != nil {
		return
	}
	defer func() { _ = up.Close() }()
	if _, err := up.Write(line); err != nil {
		return
	}
	if !isRead || p.viewportLines <= 0 || readParams.Lines != 0 {
		_, _ = io.Copy(c, up)
		return
	}
	respLine, err := bufio.NewReader(up).ReadBytes('\n')
	if err != nil {
		return
	}
	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      uint64 `json:"id"`
		Result  *struct {
			Text     string `json:"text"`
			Complete bool   `json:"complete"`
			How      string `json:"how"`
		} `json:"result,omitempty"`
		Error json.RawMessage `json:"error,omitempty"`
	}
	if json.Unmarshal(respLine, &resp) == nil && resp.Result != nil {
		parts := strings.Split(strings.TrimSuffix(resp.Result.Text, "\n"), "\n")
		if len(parts) > p.viewportLines {
			resp.Result.Text = strings.Join(parts[len(parts)-p.viewportLines:], "\n")
		}
		if out, merr := json.Marshal(resp); merr == nil {
			_, _ = c.Write(append(out, '\n'))
			return
		}
	}
	_, _ = c.Write(respLine)
}

func wakeSendsWith(t *testing.T, reqs []hctest.Request, needle string) (out []string) {
	t.Helper()
	for _, r := range reqs {
		var p struct {
			Text string `json:"text"`
		}
		if r.Method == "terminal.send" && json.Unmarshal(r.Params, &p) == nil && strings.Contains(p.Text, needle) {
			out = append(out, p.Text)
		}
	}
	return out
}

func hasJ12LogFor(logs []LogLine, key, decisionSub string) bool {
	for _, l := range logs {
		if l.Decision != "" && l.Why != "" && slices.Contains(l.Asked, key) &&
			(decisionSub == "" || strings.Contains(strings.ToLower(l.Decision), decisionSub)) {
			return true
		}
	}
	return false
}

// startPR2bDeliveredWake starts a Claude coordinator with one parked task T1
// (so N5 does not inject a stray failed@new_attempt wake) and delivers one
// T1:worker_failed wake via J12.
func startPR2bDeliveredWake(t *testing.T, goal string) (*projectHarness, string) {
	t.Helper()
	h := newProjectHarness(t, goal)
	h.pass() // J7 starts coord1
	coord1 := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open", Parked: true}}
		s.Project.Wakes = []flow.Wake{{Key: "worker_failed", Task: "T1", Why: "N23", Facts: "title x", At: h.now}}
	})
	h.coordIdleWithStatus(coord1, "idle") // J12 delivers wake
	if got := workerFailedWakeSends(t, h.srv.Requests()); got != 1 {
		t.Fatalf("initial wake sends = %d, want 1", got)
	}
	return h, coord1
}

// TestPR2bDeliveryMarkerAndFormat pins the delivery header format (`Wake: #<seq> ...`),
// restart-safe sequence numbering, standalone watch reset on J12 delivery,
// pre-upgrade zero-sequence recovery, and removal of CoordWakeActed.
func TestPR2bDeliveryMarkerAndFormat(t *testing.T) {
	t.Run("numbered_header_uses_commit_seq_and_increments_across_restart", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Numbered wake header")
		send1 := lastSendTo(t, h.srv.Requests(), coord1)
		seq1 := wakeHeaderSeq(t, send1)
		j12 := logRules(t, h.paths.Log(h.id), "J12")
		if len(j12) != 1 || j12[0].Seq != seq1 {
			t.Fatalf("first J12 log seq = %+v, want single line with seq %d matching header %q", j12, seq1, strings.SplitN(send1, "\n", 2)[0])
		}
		if !strings.HasPrefix(send1, fmt.Sprintf("Wake: #%d 1 task wake(s) wait on you.\n", seq1)) {
			t.Fatalf("first wake header = %q, want %q", strings.SplitN(send1, "\n", 2)[0], fmt.Sprintf("Wake: #%d 1 task wake(s) wait on you.", seq1))
		}

		// Simulate daemon restart (fresh Engine on same state dir) before interrupted retry.
		eng2 := newGitTestEngine(t, h.paths, h.client)
		eng2.Now = func() time.Time { return h.now }
		h.eng = eng2

		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, formatPromptEcho("❯", send1)+"\n  ⎿  Interrupted · What should Claude do instead?\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")

		sends := sendsTo(t, h.srv.Requests(), coord1)
		if len(sends) != 2 {
			t.Fatalf("after restart + Esc: sends = %d, want 2", len(sends))
		}
		seq2 := wakeHeaderSeq(t, sends[1])
		if seq2 <= seq1 {
			t.Fatalf("retry sequence #%d did not advance past #%d", seq2, seq1)
		}
		j12After := logRules(t, h.paths.Log(h.id), "J12")
		if j12After[len(j12After)-1].Seq != seq2 {
			t.Fatalf("retry J12 log seq = %d, want %d", j12After[len(j12After)-1].Seq, seq2)
		}
	})

	t.Run("j12_records_own_watch_baseline_over_earlier_standalone_watch", func(t *testing.T) {
		h := newProjectHarness(t, "J12 resets standalone watch")
		h.pass() // J7 starts coord1
		coord1 := h.state().Project.RunID
		h.coordIdleWithStatus(coord1, "idle")

		// Arm an old standalone watch (as from an earlier J13 heartbeat or J8 nudge) 25m ago.
		staleWatch := h.now.Add(-25 * time.Minute)
		h.edit(func(s *flow.State) {
			s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open", Parked: true}}
			s.Project.CoordWakeAt = staleWatch
			s.Project.CoordWakeTurn = s.Project.Run.Turn
			s.Project.Wakes = []flow.Wake{{Key: "worker_failed", Task: "T1", Why: "N23", Facts: "title x", At: h.now}}
		})
		deliverAt := h.now.Add(15 * time.Second)
		h.now = deliverAt
		h.pass() // J12 delivers wake

		st := h.state()
		if !st.Project.CoordWakeAt.Equal(deliverAt) {
			t.Fatalf("CoordWakeAt after J12 = %v, want J12 delivery time %v (stale standalone watch %v leaked)",
				st.Project.CoordWakeAt, deliverAt, staleWatch)
		}
		// 10 minutes later (35m after staleWatch, only 10m after J12): J9 must NOT stop coord1 yet.
		h.now = h.now.Add(10 * time.Minute)
		h.pass()
		if h.state().Project.RunID != coord1 || len(logRules(t, h.paths.Log(h.id), "J9")) != 0 {
			t.Fatalf("J9 fired early from stale standalone watch: runID=%q j9=%+v", h.state().Project.RunID, logRules(t, h.paths.Log(h.id), "J9"))
		}
	})

	t.Run("pre_upgrade_zero_sequence_takes_unconfirmed_recovery", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Pre-upgrade zero seq")
		// Simulate pre-upgrade state.json where CoordWakesInFlight has no sequence (CoordWakeSeq == 0).
		raw, err := os.ReadFile(h.paths.State(h.id))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		proj := doc["project"].(map[string]any)
		delete(proj, "coord_wake_seq")
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(h.paths.State(h.id), append(out, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}

		// Even with an unnumbered echo + reply on screen, a zero sequence cannot confirm -> retries once.
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, "❯ Wake: 1 task wake(s) wait on you.\n  Settings that differ from their default: none.\n\n⏺ Handled.\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")

		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("pre-upgrade zero-seq batch sends = %d, want 2 (must take unconfirmed recovery)", got)
		}
		send2 := lastSendTo(t, h.srv.Requests(), coord1)
		_ = wakeHeaderSeq(t, send2)
	})

	t.Run("unrelated_coordinator_record_does_not_confirm_wake", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Unrelated coord commit")
		h.setCoordWorking(coord1)
		// Owner typed a command causing the coordinator to commit an unrelated record
		// while T1:worker_failed is in flight; screen only shows the owner turn.
		h.eng.mu.Lock()
		st := h.state()
		if !h.eng.commitLogLocked(h.id, &st, LogLine{By: flow.RoleCoordinator, Task: "T2", Decision: "parked T2", Why: "owner asked in live window"}) {
			h.eng.mu.Unlock()
			t.Fatal("commitLogLocked failed")
		}
		h.eng.mu.Unlock()
		h.srv.SetScreen(coord1, "❯ park T2 please\n\n⏺ Parked T2.\n\n✻ Cogitated for 2s\n────────────────────────────────────────────────────────────────────────────────\n❯ \n────────────────────────────────────────────────────────────────────────────────\n")
		h.coordIdleWithStatus(coord1, "idle")

		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("unrelated coordinator record falsely confirmed wake: sends = %d, want 2", got)
		}
	})
}

// TestPR2bRow1DeliveryTurnHolds pins State Table Row 1:
// while working, blocked, or idle on the delivery turn (Turn <= CoordWakeTurn),
// the batch and its retry bits stay intact, J12/J13/J4 wait, J9 still fires
// after coordinator_silent_minutes, and non-Codex working clears CoordWakeAt early.
func TestPR2bRow1DeliveryTurnHolds(t *testing.T) {
	t.Run("idle_on_delivery_turn_holds_J12_J13_and_J4_until_J9", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Row 1 holds")
		// Queue a 2nd wake, set heartbeat_minutes < coordinator_silent_minutes, and end all tasks.
		h.edit(func(s *flow.State) {
			s.Settings.HeartbeatMinutes = 10
			s.Settings.CoordinatorSilentMinutes = 30
			s.Tasks[0].Done = &flow.Merged{MergedCommit: "m1"}
			s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: "main_red", Why: "base red", Facts: "red", At: h.now})
		})

		// Return to PhaseIdle on the SAME delivery turn (status never went working, so Turn == CoordWakeTurn),
		// then advance 15m (past heartbeat_minutes=10, before coordinator_silent_minutes=30).
		h.coordIdleWithStatus(coord1, "idle")
		if got := len(sendsTo(t, h.srv.Requests(), coord1)); got != 1 {
			t.Fatalf("same delivery turn fired extra send (J12/J13/J4 did not hold): sends=%q", sendsTo(t, h.srv.Requests(), coord1))
		}
		if pre := h.state(); pre.Project.Run.State != flow.PhaseIdle || pre.Project.Run.Turn > pre.Project.CoordWakeTurn {
			t.Fatalf("expected coordinator to remain PhaseIdle on delivery turn: state=%s turn=%d wakeTurn=%d",
				pre.Project.Run.State, pre.Project.Run.Turn, pre.Project.CoordWakeTurn)
		}
		h.now = h.now.Add(15 * time.Minute)
		h.pass()

		st := h.state()
		if got := len(sendsTo(t, h.srv.Requests(), coord1)); got != 1 {
			t.Fatalf("same delivery turn fired extra send (J12/J13/J4 did not hold): sends=%q", sendsTo(t, h.srv.Requests(), coord1))
		}
		if len(st.Project.CoordWakesInFlight) != 1 || len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Key != "main_red" {
			t.Fatalf("same delivery turn mutated wake lists: inFlight=%+v wakes=%+v", st.Project.CoordWakesInFlight, st.Project.Wakes)
		}
		if len(logRules(t, h.paths.Log(h.id), "J13")) != 0 || len(logRules(t, h.paths.Log(h.id), "J4")) != 0 {
			t.Fatalf("J13 or J4 fired during delivery turn: j13=%+v j4=%+v", logRules(t, h.paths.Log(h.id), "J13"), logRules(t, h.paths.Log(h.id), "J4"))
		}

		// Advance past coordinator_silent_minutes=30 on the same turn -> J9 stops coord1 and preserves both wakes.
		h.now = h.now.Add(20 * time.Minute)
		h.pass()
		if len(logRules(t, h.paths.Log(h.id), "J9")) != 1 {
			t.Fatalf("expected J9 stop after silent_minutes on delivery turn: j9=%+v", logRules(t, h.paths.Log(h.id), "J9"))
		}
		stAfter := h.state()
		if len(stAfter.Project.Wakes) != 2 || stAfter.Project.Wakes[0].Key != "worker_failed" || stAfter.Project.Wakes[1].Key != "main_red" {
			t.Fatalf("after J9 stop: wakes = %+v, want [worker_failed, main_red] preserving FIFO order", stAfter.Project.Wakes)
		}
	})

	t.Run("non_codex_working_clears_watch_early_but_keeps_batch", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Row 1 early watch clear")
		h.setCoordWorking(coord1)
		st := h.state()
		if !st.Project.CoordWakeAt.IsZero() {
			t.Fatalf("non-Codex working did not clear CoordWakeAt early: %v", st.Project.CoordWakeAt)
		}
		if len(st.Project.CoordWakesInFlight) != 1 {
			t.Fatalf("non-Codex working dropped CoordWakesInFlight: %+v", st.Project.CoordWakesInFlight)
		}
	})
}

// TestPR2bRow2ConfirmedWakeAcrossCLIs pins State Table Row 2 across real Claude,
// Codex, and agy captures, including Codex's separate 200-line scrollback read,
// later owner-turn Esc immunity, agy reply markers vs queued-prompt trap, and
// Codex indented echo bullets.
func TestPR2bRow2ConfirmedWakeAcrossCLIs(t *testing.T) {
	t.Run("claude_completed_wake_immune_to_later_owner_turn_esc", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Claude later owner Esc")
		send1 := lastSendTo(t, h.srv.Requests(), coord1)
		h.edit(func(s *flow.State) { s.Project.CoordSilentStops = 1 })

		// Build from real claude-after-approval + claude-bash-prompt-58col interrupt marker:
		// completed wake turn followed by a later owner prompt interrupted by Esc.
		h.setCoordWorking(coord1)
		screen := formatPromptEcho("❯", send1) +
			"\n⏺ Retried T1 after reviewing the logs.\n\n✻ Cogitated for 4s · done 4:59 PM\n\n" +
			"❯ Use the Bash tool to run exactly this command: date > stamp-58.txt\n\n" +
			"⏺ Bash(date > stamp-58.txt)\n" +
			"  ⎿ \u00a0Interrupted · What should Claude do instead?\n\n" +
			"✻ Worked for 1s · done 10:08 AM\n" +
			"────────────────────────────────────────────────────────────────────────────────\n❯ \n────────────────────────────────────────────────────────────────────────────────\n"
		h.srv.SetScreen(coord1, screen)
		h.coordIdleWithStatus(coord1, "idle")
		h.now = h.now.Add(time.Minute)
		h.pass()

		st := h.state()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 1 {
			t.Fatalf("Esc on later owner turn falsely retried earlier completed wake: sends=%d, want 1", got)
		}
		if len(st.Project.CoordWakesInFlight) != 0 || !st.Project.CoordWakeAt.IsZero() || st.Project.CoordSilentStops != 0 {
			t.Fatalf("confirmed wake left tracking behind: inFlight=%d wakeAt=%v silentStops=%d",
				len(st.Project.CoordWakesInFlight), st.Project.CoordWakeAt, st.Project.CoordSilentStops)
		}
	})

	t.Run("codex_inferred_reply_above_viewport_confirmed_via_200_line_read", func(t *testing.T) {
		c := newCodexRoles(t, map[string][]flow.Candidate{
			flow.RoleCoordinator: {{CLI: world.CodexCLI, Model: codexModel, Effort: "medium"}},
		}, true)
		// Wrap hctest.Server with a 24-line viewport proxy so lines=0 truly returns only 24 lines.
		proxy := newHCReadProxy(t, c.srv.Path, 24)
		c.cl = hc.NewClient(proxy.path)
		c.eng.HC = c.cl

		c.pass() // J7 starts Codex coordinator
		co := c.state().Project.RunID
		c.edit(func(s *flow.State) {
			s.Project.Wakes = []flow.Wake{{Task: "T1", Key: "worker_failed", Why: "worker needs help", At: *c.now}}
		})
		c.srv.SetScreen(co, readFixture(t, "codex-idle.screen.txt"))
		c.turnEnds()
		send1 := lastSendTo(t, c.srv.Requests(), co)
		if !strings.Contains(send1, "worker_failed") {
			t.Fatalf("first send = %q, want worker_failed", send1)
		}

		// Working screen with real codex-working capture, then a normal 40-line reply
		// that pushes `› Wake: #<seq> ...` above the 24-line viewport while keeping it
		// well inside the 200-line scrollback read.
		c.srv.SetScreen(co, readFixture(t, "codex-working.screen.txt"))
		c.pass()

		var b strings.Builder
		b.WriteString(formatPromptEcho("›", send1))
		b.WriteString("\n• Explored\n  └ Read internal/factory/engine.go\n\n")
		for i := range 32 {
			fmt.Fprintf(&b, "• Checked worker log line %d and confirmed retry is safe.\n", i+1)
		}
		b.WriteString("\n  Worked for 17s • 8:44 PM\n\n› Ask Codex to do anything\n\n  gpt-6.2-sol high · 96% left\n")
		c.srv.SetScreen(co, b.String())
		c.turnEnds()
		*c.now = c.now.Add(time.Minute)
		c.pass()

		if got := workerFailedWakeSends(t, c.srv.Requests()); got != 1 {
			t.Fatalf("Codex normal 40-line reply above 24-line viewport double-delivered: sends=%d, want 1", got)
		}
		if st := c.state(); len(st.Project.CoordWakesInFlight) != 0 {
			t.Fatalf("Codex reply in 200-line scrollback did not retire batch: inFlight=%+v", st.Project.CoordWakesInFlight)
		}
		reads := proxy.recordedReads()
		if !slices.Contains(reads, 0) || !slices.Contains(reads, 200) {
			t.Fatalf("Codex inferred reads = %v, want both lines=0 (viewport) and lines=200 (evidence)", reads)
		}
	})

	t.Run("codex_indented_echo_bullets_without_column0_reply_retry_once", func(t *testing.T) {
		c := newCodexRoles(t, map[string][]flow.Candidate{
			flow.RoleCoordinator: {{CLI: world.CodexCLI, Model: codexModel, Effort: "medium"}},
		}, true)
		c.pass()
		co := c.state().Project.RunID
		c.edit(func(s *flow.State) {
			s.Project.Wakes = []flow.Wake{{Task: "T1", Key: "worker_failed", Why: "worker needs help", At: *c.now}}
		})
		c.srv.SetScreen(co, readFixture(t, "codex-idle.screen.txt"))
		c.turnEnds()
		send1 := lastSendTo(t, c.srv.Requests(), co)
		header, rest, _ := strings.Cut(strings.TrimRight(send1, "\n"), "\n")

		// Codex renders markdown `- ` bullets inside the echoed prompt as 2-space-indented `  • `
		// (as seen in internal/factory/testdata/codex-working.screen.txt:20). No column-0 `• ` reply follows.
		c.srv.SetScreen(co, readFixture(t, "codex-working.screen.txt"))
		c.pass()
		echoed := "› " + header + "\n"
		for ln := range strings.Lines(rest) {
			ln = strings.TrimRight(ln, "\n")
			if strings.HasPrefix(ln, "- ") {
				echoed += "  • " + strings.TrimPrefix(ln, "- ") + "\n"
			} else {
				echoed += "  " + ln + "\n"
			}
		}
		c.srv.SetScreen(co, echoed+"\n› Ask Codex to do anything\n\n  gpt-6.2-sol high · 96% left\n")
		c.turnEnds()

		if got := workerFailedWakeSends(t, c.srv.Requests()); got != 2 {
			t.Fatalf("Codex echoed prompt with only indented bullets falsely confirmed: sends=%d, want 2", got)
		}
	})

	t.Run("agy_active_queued_prompt_not_confirmed_while_thought_tool_and_plain_confirm", func(t *testing.T) {
		activeCapture := readWorldScreenFixture(t, "agy-esc-active.screen.txt")
		handledCapture := readWorldScreenFixture(t, "agy-esc-handled.screen.txt")
		// Extract the real `▸ Thought for 7s, 535 tokens` line from the live 829-live-r3 capture.
		if !strings.Contains(handledCapture, "▸ Thought for 7s, 535 tokens") {
			t.Fatal("agy-esc-handled.screen.txt missing real Thought line")
		}

		// Subcase A: real agy-esc-active capture (spinner + queued `▸ ... (alt+m to send immediately)`).
		// Must NOT confirm on an idle tick -> retries once.
		t.Run("queued_prompt_and_spinner_do_not_confirm", func(t *testing.T) {
			r, coord := startAgyDeliveredWake(t)
			send1 := lastSendTo(t, r.srv.Requests(), coord)
			agyStepWorking(r, coord, bindAgyCaptureWake(activeCapture, send1))
			agyStepIdle(r, coord, "idle", bindAgyCaptureWake(activeCapture, send1))
			if got := prClosedWakeSends(t, r.srv.Requests()); got != 2 {
				t.Fatalf("agy active screen with queued prompt `▸ ... (alt+m to send immediately)` falsely confirmed: sends=%d, want 2", got)
			}
		})

		// Subcase B, C, D: real agy reply markers confirm without retry.
		for _, tc := range []struct {
			name, body string
		}{
			{"tool_line", "\n● Bash(sheepdog factory task retry T1 --why \"closed PR\")\n"},
			{"thought_line", "\n▸ Thought for 7s, 535 tokens\n"},
			{"blank_line_plain_reply", "\n  Waiting for your go-ahead before acting on T1's pr_closed wake.\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r, coord := startAgyDeliveredWake(t)
				send1 := lastSendTo(t, r.srv.Requests(), coord)
				agyStepWorking(r, coord, bindAgyCaptureWake(activeCapture, send1))
				screen := "────────────────────────────────────────────────────────────\n" +
					formatPromptEcho(">", send1) + tc.body + agyIdleBox
				agyStepIdle(r, coord, "done", screen)
				*r.now = r.now.Add(time.Minute)
				r.pass()
				if got := prClosedWakeSends(t, r.srv.Requests()); got != 1 {
					t.Fatalf("agy %s did not confirm wake: sends=%d, want 1", tc.name, got)
				}
				if st := r.state(); len(st.Project.CoordWakesInFlight) != 0 {
					t.Fatalf("agy %s left inFlight=%+v", tc.name, st.Project.CoordWakesInFlight)
				}
			})
		}
	})
}

// TestPR2bRow3BlankUnreadableAndOutage pins State Table Row 3:
//   - Blank or unreadable evidence waits until LastTurnEnd + idle_grace_seconds,
//     closing CoordWakeAt and resetting CoordSilentStops during grace, while
//     holding J12/J13/J4.
//   - Recovery before deadline confirms with 1 send.
//   - Persistent blank or failed read reconciles at/after deadline.
//   - Zero LastTurnEnd reconciles immediately on a real EvTick.
//   - A herdr-core outage (no EvTick) never expires grace.
func TestPR2bRow3BlankUnreadableAndOutage(t *testing.T) {
	t.Run("transient_blank_holds_during_grace_resets_streak_then_confirms", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Row 3 transient blank")
		send1 := lastSendTo(t, h.srv.Requests(), coord1)
		h.edit(func(s *flow.State) {
			s.Settings.IdleGraceSeconds = 20
			s.Settings.HeartbeatMinutes = 10
			s.Project.CoordSilentStops = 1
			s.Project.Wakes = []flow.Wake{{Key: "main_red", Why: "base red", Facts: "red", At: h.now}}
		})

		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, "")
		h.coordIdleWithStatus(coord1, "idle") // R20 enters PhaseIdle, sets LastTurnEnd = h.now

		// Within idle_grace_seconds: batch stays in flight, CoordWakeAt is closed,
		// CoordSilentStops is reset to 0, and J12/J13 wait.
		h.now = h.now.Add(10 * time.Second)
		h.pass()
		st := h.state()
		if len(st.Project.CoordWakesInFlight) != 1 || !st.Project.CoordWakeAt.IsZero() || st.Project.CoordSilentStops != 0 {
			t.Fatalf("during blank-screen grace: inFlight=%d wakeAt=%v silentStops=%d, want 1 / zero / 0",
				len(st.Project.CoordWakesInFlight), st.Project.CoordWakeAt, st.Project.CoordSilentStops)
		}
		if got := len(sendsTo(t, h.srv.Requests(), coord1)); got != 1 {
			t.Fatalf("J12 sent batch 2 during blank-screen grace: sends=%q", sendsTo(t, h.srv.Requests(), coord1))
		}

		// Screen becomes readable before deadline with confirmed wake -> retires cleanly, then J12 sends main_red.
		h.srv.SetScreen(coord1, completedWakeScreen("claude", send1, "Retried T1."))
		h.now = h.now.Add(5 * time.Second)
		h.pass()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 1 {
			t.Fatalf("transient blank screen followed by confirmed wake retried: sends=%d, want 1", got)
		}
		if sends := sendsTo(t, h.srv.Requests(), coord1); len(sends) != 2 || !strings.Contains(sends[1], "main_red") {
			t.Fatalf("after confirmed wake cleared grace, expected main_red delivery: sends=%q", sends)
		}
	})

	t.Run("persistent_blank_screen_reconciles_at_idle_grace_deadline", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Row 3 persistent blank")
		h.edit(func(s *flow.State) { s.Settings.IdleGraceSeconds = 20 })
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, "")
		h.coordIdleWithStatus(coord1, "idle")

		// Still inside grace at +10s: no retry yet.
		h.now = h.now.Add(10 * time.Second)
		h.pass()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 1 {
			t.Fatalf("reconciled before idle_grace_seconds deadline: sends=%d, want 1", got)
		}

		// At +20s (LastTurnEnd + idle_grace_seconds): reconciles as unconfirmed, logs J12 retry, redelivers once.
		h.now = h.now.Add(10 * time.Second)
		h.pass()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("persistent blank screen past idle_grace_seconds did not reconcile: sends=%d, want 2", got)
		}
		st := h.state()
		if len(st.Project.CoordWakesInFlight) != 1 || !st.Project.CoordWakesInFlight[0].Retried {
			t.Fatalf("after blank-screen retry: inFlight=%+v, want 1 retried wake", st.Project.CoordWakesInFlight)
		}
		if !hasJ12LogFor(logRules(t, h.paths.Log(h.id), "J12"), "T1:worker_failed", "") {
			t.Fatalf("missing J12 reconciliation log: %+v", logRules(t, h.paths.Log(h.id), "J12"))
		}
	})

	t.Run("failed_terminal_read_waits_grace_then_reconciles", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Row 3 failed read")
		proxy := newHCReadProxy(t, h.srv.Path, 0)
		h.client = hc.NewClient(proxy.path)
		h.eng.HC = h.client
		h.edit(func(s *flow.State) { s.Settings.IdleGraceSeconds = 20 })

		h.setCoordWorking(coord1)
		proxy.failReads.Store(true)
		h.coordIdleWithStatus(coord1, "idle")

		// Inside grace: held.
		h.now = h.now.Add(10 * time.Second)
		h.pass()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 1 {
			t.Fatalf("failed read reconciled before grace deadline: sends=%d, want 1", got)
		}

		// At deadline: reconciles as unconfirmed and retries once.
		h.now = h.now.Add(15 * time.Second)
		h.pass()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("failed read past grace deadline did not reconcile: sends=%d, want 2", got)
		}
	})

	t.Run("zero_last_turn_end_reconciles_immediately_on_blank_screen", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Row 3 zero LastTurnEnd")
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, "")
		h.coordIdleWithStatus(coord1, "idle")
		// Simulate legacy/synthetic idle record with zero LastTurnEnd.
		h.edit(func(s *flow.State) { s.Project.Run.LastTurnEnd = time.Time{} })

		h.now = h.now.Add(1 * time.Second)
		h.pass()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("zero LastTurnEnd on blank screen did not reconcile immediately: sends=%d, want 2", got)
		}
	})

	t.Run("hc_outage_no_tick_does_not_expire_grace", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Row 3 hc outage")
		proxy := newHCReadProxy(t, h.srv.Path, 0)
		h.client = hc.NewClient(proxy.path)
		h.eng.HC = h.client
		h.edit(func(s *flow.State) { s.Settings.IdleGraceSeconds = 20 })

		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, "")
		h.coordIdleWithStatus(coord1, "idle")

		// Simulate herdr-core outage past idle_grace_seconds: observeCoordLocked gets err != nil
		// and calls reconcileCoordWakes with an empty event (no EvTick).
		proxy.failGet.Store(true)
		h.now = h.now.Add(2 * time.Minute)
		h.pass()

		st := h.state()
		if len(st.Project.CoordWakesInFlight) != 1 || st.Project.CoordWakesInFlight[0].Retried || len(st.Project.Wakes) != 0 {
			t.Fatalf("herdr-core outage falsely expired grace without EvTick: inFlight=%+v wakes=%+v",
				st.Project.CoordWakesInFlight, st.Project.Wakes)
		}
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 1 {
			t.Fatalf("herdr-core outage caused send=%d, want 1", got)
		}
	})
}

// TestPR2bRow4UnconfirmedVariantsAndReconciliation pins State Table Row 4:
//   - Interruption after partial output invalidates slice (Claude, Codex, agy).
//   - Stale delivery marker from an earlier batch retries batch 2 once.
//   - Readable dialog redraw without wake echo retries once.
//   - J8 error reconciles immediately even on a blank screen and survives J8 stop.
//   - Exhausted 2nd unconfirmed logs keys & reason, resets streak, no J9.
//   - Mixed retry bits in one batch retry unused and log/release spent.
//   - Newer held duplicate wins with its own Facts/Retried and omits superseded copy from logs.
//   - Failed reconciliation commit preserves CoordWakesInFlight until store recovers.
func TestPR2bRow4UnconfirmedVariantsAndReconciliation(t *testing.T) {
	t.Run("partial_output_then_interruption_invalidates_all_clis", func(t *testing.T) {
		t.Run("claude", func(t *testing.T) {
			h, coord1 := startPR2bDeliveredWake(t, "Claude partial then Esc")
			send1 := lastSendTo(t, h.srv.Requests(), coord1)
			// Matches testdata/herdr/claude-bash-prompt-58col.screen.txt: ⏺ Bash(...) then Interrupted.
			h.setCoordWorking(coord1)
			screen := formatPromptEcho("❯", send1) +
				"\n⏺ Bash(date > stamp-58.txt)\n  ⎿ \u00a0Interrupted · What should Claude do instead?\n\n✻ Worked for 1s · done 10:08 AM\n" +
				"────────────────────────────────────────────────────────────────────────────────\n❯ \n────────────────────────────────────────────────────────────────────────────────\n"
			h.srv.SetScreen(coord1, screen)
			h.coordIdleWithStatus(coord1, "idle")
			if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
				t.Fatalf("Claude partial output + Interrupted falsely confirmed: sends=%d, want 2", got)
			}
		})

		t.Run("codex", func(t *testing.T) {
			c := newCodexRoles(t, map[string][]flow.Candidate{
				flow.RoleCoordinator: {{CLI: world.CodexCLI, Model: codexModel, Effort: "medium"}},
			}, true)
			c.pass()
			co := c.state().Project.RunID
			c.edit(func(s *flow.State) {
				s.Project.Wakes = []flow.Wake{{Task: "T1", Key: "worker_failed", Why: "worker needs help", At: *c.now}}
			})
			c.srv.SetScreen(co, readFixture(t, "codex-idle.screen.txt"))
			c.turnEnds()
			send1 := lastSendTo(t, c.srv.Requests(), co)

			// Matches testdata/herdr/codex-after-ctrl-c.screen.txt: • partial output then ■ Conversation interrupted.
			ctrlC := readHerdrFixture(t, "codex-after-ctrl-c.screen.txt")
			bodyStart := strings.Index(ctrlC, "\n• ")
			if bodyStart < 0 {
				t.Fatal("codex-after-ctrl-c.screen.txt missing partial output bullet")
			}
			c.srv.SetScreen(co, readFixture(t, "codex-working.screen.txt"))
			c.pass()
			c.srv.SetScreen(co, formatPromptEcho("›", send1)+ctrlC[bodyStart:])
			c.turnEnds()
			if got := workerFailedWakeSends(t, c.srv.Requests()); got != 2 {
				t.Fatalf("Codex partial output + Conversation interrupted falsely confirmed: sends=%d, want 2", got)
			}
		})

		t.Run("agy", func(t *testing.T) {
			r, coord := startAgyDeliveredWake(t)
			send1 := lastSendTo(t, r.srv.Requests(), coord)
			activeCapture := readWorldScreenFixture(t, "agy-esc-active.screen.txt")
			agyStepWorking(r, coord, bindAgyCaptureWake(activeCapture, send1))
			// Partial ● Bash tool call before real agy Interrupted marker.
			screen := "────────────────────────────────────────────────────────────\n" +
				formatPromptEcho(">", send1) +
				"\n● Bash(sheepdog factory status)\n\n  ⎿  Interrupted · What should Antigravity CLI do instead?\n" +
				agyIdleBox
			agyStepIdle(r, coord, "idle", screen)
			if got := prClosedWakeSends(t, r.srv.Requests()); got != 2 {
				t.Fatalf("agy partial tool output + Interrupted falsely confirmed: sends=%d, want 2", got)
			}
		})
	})

	t.Run("stale_delivery_marker_from_batch1_does_not_confirm_batch2", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Stale delivery marker")
		send1 := lastSendTo(t, h.srv.Requests(), coord1)

		// Batch 1 completes cleanly; its echo + reply remain in scrollback.
		h.setCoordWorking(coord1)
		batch1Done := completedWakeScreen("claude", send1, "Retried T1.")
		h.srv.SetScreen(coord1, batch1Done)
		h.coordIdleWithStatus(coord1, "idle")
		if len(h.state().Project.CoordWakesInFlight) != 0 {
			t.Fatalf("batch 1 did not confirm: inFlight=%+v", h.state().Project.CoordWakesInFlight)
		}

		// Batch 2 arrives and is delivered with a newer #<seq2>.
		h.edit(func(s *flow.State) {
			s.Project.Wakes = []flow.Wake{{Key: "main_red", Why: "N38: base red", Facts: "red", At: h.now}}
		})
		h.now = h.now.Add(15 * time.Second)
		h.pass()
		if got := len(wakeSendsWith(t, h.srv.Requests(), "main_red")); got != 1 {
			t.Fatalf("batch 2 sends = %d, want 1", got)
		}

		// Batch 2's turn advances, but screen still only shows Batch 1's older completed echo (stale marker).
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, batch1Done)
		h.coordIdleWithStatus(coord1, "idle")

		if got := len(wakeSendsWith(t, h.srv.Requests(), "main_red")); got != 2 {
			t.Fatalf("stale batch 1 marker falsely confirmed batch 2: main_red sends=%d, want 2", got)
		}
	})

	t.Run("readable_dialog_redraw_without_wake_retries_once", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Dialog redraw")
		h.setCoordWorking(coord1)
		// Real Claude idle composer redraw after dialog dismissal with no wake echo.
		h.srv.SetScreen(coord1, readHerdrFixture(t, "claude-idle.screen.txt"))
		h.coordIdleWithStatus(coord1, "idle")
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("readable dialog redraw without wake did not retry: sends=%d, want 2", got)
		}
	})

	t.Run("j8_error_requeues_immediately_on_blank_screen_and_survives_j8_stop", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Row 4 J8 error")
		h.edit(func(s *flow.State) { s.Settings.IdleGraceSeconds = 20 })

		// Wake turn ends in error on a BLANK screen: must requeue immediately (no waiting
		// for idle_grace_seconds), ahead of J8's go-on prompt.
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, "")
		h.coordIdleWithStatus(coord1, "error")

		st := h.state()
		if len(st.Project.CoordWakesInFlight) != 0 {
			t.Fatalf("error turn left CoordWakesInFlight=%+v, want empty (requeued immediately)", st.Project.CoordWakesInFlight)
		}
		if len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Key != "worker_failed" || !st.Project.Wakes[0].Retried {
			t.Fatalf("after 1st error turn: wakes=%+v, want [worker_failed Retried=true] held while J8 nudges", st.Project.Wakes)
		}
		if j8 := logRules(t, h.paths.Log(h.id), "J8"); len(j8) != 1 {
			t.Fatalf("expected J8 go-on on 1st error ahead of J12: j8=%+v", j8)
		}

		// Coordinator errors a 2nd time within quiet_minutes -> J8 stops coord1,
		// J7 starts coord2, and coord2 receives the requeued wake.
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, "")
		h.coordIdleWithStatus(coord1, "error")
		if j8 := logRules(t, h.paths.Log(h.id), "J8"); len(j8) != 2 {
			t.Fatalf("expected J8 stop on 2nd error: j8=%+v", j8)
		}
		h.now = h.now.Add(time.Duration(h.state().Settings.CoordinatorGapMinutes+1) * time.Minute)
		h.pass() // J7 starts coord2
		coord2 := h.state().Project.RunID
		if coord2 == "" || coord2 == coord1 {
			t.Fatalf("replacement coord2 did not start after J8 stop: runID=%q", coord2)
		}
		h.coordIdleWithStatus(coord2, "idle")
		if sends := sendsTo(t, h.srv.Requests(), coord2); len(sends) != 1 || !strings.Contains(sends[0], "worker_failed") {
			t.Fatalf("coord2 sends after J8 stop = %q, want requeued worker_failed", sends)
		}
	})

	t.Run("exhausted_second_unconfirmed_logs_keys_resets_streak_and_no_j9", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Row 4 exhaustion")
		h.edit(func(s *flow.State) { s.Project.CoordSilentStops = 1 })

		// 1st unconfirmed turn -> retry #1.
		send1 := lastSendTo(t, h.srv.Requests(), coord1)
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, formatPromptEcho("❯", send1)+"\n  ⎿ \u00a0Interrupted · What should Claude do instead?\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("after 1st Esc: sends=%d, want 2", got)
		}

		// 2nd unconfirmed turn -> exhausted, released, logged via J12, streak reset, no J9.
		send2 := lastSendTo(t, h.srv.Requests(), coord1)
		h.edit(func(s *flow.State) { s.Project.CoordSilentStops = 1 })
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, formatPromptEcho("❯", send2)+"\n  ⎿ \u00a0Interrupted · What should Claude do instead?\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")

		st := h.state()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("2nd Esc sent a 3rd time: sends=%d, want 2", got)
		}
		if len(st.Project.CoordWakesInFlight) != 0 || len(st.Project.Wakes) != 0 || !st.Project.CoordWakeAt.IsZero() || st.Project.CoordSilentStops != 0 {
			t.Fatalf("after exhaustion: inFlight=%d wakes=%d wakeAt=%v silentStops=%d, want all clear/0",
				len(st.Project.CoordWakesInFlight), len(st.Project.Wakes), st.Project.CoordWakeAt, st.Project.CoordSilentStops)
		}
		j12 := logRules(t, h.paths.Log(h.id), "J12")
		last := j12[len(j12)-1]
		if !slices.Contains(last.Asked, "T1:worker_failed") || last.Decision == "" || last.Why == "" || strings.Contains(last.Decision, "delivered") {
			t.Fatalf("last J12 log = %+v, want exhaustion log naming T1:worker_failed with non-empty Decision and Why", last)
		}
		// Advancing past coordinator_silent_minutes must NOT trigger J9 replacement.
		h.now = h.now.Add(time.Duration(st.Settings.CoordinatorSilentMinutes+5) * time.Minute)
		h.pass()
		if h.state().Project.RunID != coord1 || len(logRules(t, h.paths.Log(h.id), "J9")) != 0 {
			t.Fatalf("J9 fired after exhausted idle turn: runID=%q j9=%+v", h.state().Project.RunID, logRules(t, h.paths.Log(h.id), "J9"))
		}
	})

	t.Run("mixed_retry_bits_in_one_batch", func(t *testing.T) {
		h := newProjectHarness(t, "Mixed retry bits")
		h.pass()
		coord1 := h.state().Project.RunID
		h.edit(func(s *flow.State) {
			s.Tasks = []flow.Task{{ID: "T1", Title: "feat: one", Parked: true}, {ID: "T2", Title: "feat: two", Parked: true}}
			s.Project.Wakes = []flow.Wake{
				{Key: "worker_failed", Task: "T1", Why: "N23", Facts: "t1", At: h.now, Retried: true},
				{Key: "pr_closed", Task: "T2", Why: "N4", Facts: "t2", At: h.now, Retried: false},
			}
		})
		h.coordIdleWithStatus(coord1, "idle") // J12 delivers mixed batch [T1(Retried=true), T2(Retried=false)]

		// Turn ends unconfirmed -> T1 is exhausted & logged, T2 is requeued with Retried=true and redelivered.
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, readHerdrFixture(t, "claude-idle.screen.txt"))
		h.coordIdleWithStatus(coord1, "idle")

		if got := len(wakeSendsWith(t, h.srv.Requests(), "worker_failed")); got != 1 {
			t.Fatalf("already-retried T1:worker_failed was redelivered: sends=%d, want 1", got)
		}
		if got := len(wakeSendsWith(t, h.srv.Requests(), "pr_closed")); got != 2 {
			t.Fatalf("fresh T2:pr_closed was not retried once: sends=%d, want 2", got)
		}
		st := h.state()
		if len(st.Project.CoordWakesInFlight) != 1 || st.Project.CoordWakesInFlight[0].Key != "pr_closed" || !st.Project.CoordWakesInFlight[0].Retried {
			t.Fatalf("inFlight after mixed batch retry = %+v, want only T2:pr_closed(Retried=true)", st.Project.CoordWakesInFlight)
		}
		// One J12 reconciliation log precedes the retry delivery log and names both keys.
		j12 := logRules(t, h.paths.Log(h.id), "J12")
		found := false
		for _, l := range j12 {
			if !strings.Contains(l.Decision, "delivered") && l.Decision != "" && l.Why != "" &&
				slices.Contains(l.Asked, "T1:worker_failed") && slices.Contains(l.Asked, "T2:pr_closed") {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing single J12 reconciliation log naming both mixed keys: %+v", j12)
		}
	})

	t.Run("newer_held_duplicate_wins_and_omitted_from_logs", func(t *testing.T) {
		for _, inFlightRetried := range []bool{false, true} {
			name := map[bool]string{false: "in_flight_fresh", true: "in_flight_spent"}[inFlightRetried]
			t.Run(name, func(t *testing.T) {
				h := newProjectHarness(t, "Newer held duplicate")
				h.pass()
				coord1 := h.state().Project.RunID
				h.edit(func(s *flow.State) {
					s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open", Parked: true}}
					s.Project.Wakes = []flow.Wake{
						{Key: "worker_failed", Task: "T1", Why: "N23", Facts: "t1", At: h.now, Retried: inFlightRetried},
						{Key: "new_goal", Why: "old goal", Facts: "Goal OLD", At: h.now, Retried: inFlightRetried},
					}
				})
				h.coordIdleWithStatus(coord1, "idle") // J12 delivers [T1:worker_failed, project:new_goal(OLD)]

				// Newer episode of project:new_goal arrives while Goal OLD is in flight.
				newerAt := h.now.Add(10 * time.Second)
				h.edit(func(s *flow.State) {
					s.Project.Wakes = []flow.Wake{{Key: "new_goal", Why: "new goal", Facts: "Goal NEW", At: newerAt, Retried: false}}
				})
				j12Before := len(logRules(t, h.paths.Log(h.id), "J12"))

				// Unconfirmed idle turn: queued Goal NEW must win (keeping Facts="Goal NEW", Retried=false),
				// and the J12 reconciliation log must include T1:worker_failed while omitting superseded project:new_goal.
				h.setCoordWorking(coord1)
				h.srv.SetScreen(coord1, readHerdrFixture(t, "claude-idle.screen.txt"))
				h.coordIdleWithStatus(coord1, "idle")

				st := h.state()
				idx := slices.IndexFunc(st.Project.CoordWakesInFlight, func(w flow.Wake) bool { return w.Key == "new_goal" })
				if idx < 0 || st.Project.CoordWakesInFlight[idx].Facts != "Goal NEW" || st.Project.CoordWakesInFlight[idx].Retried {
					t.Fatalf("after superseded reconciliation + delivery: inFlight=%+v, want Goal NEW with Retried=false", st.Project.CoordWakesInFlight)
				}
				if sends := sendsTo(t, h.srv.Requests(), coord1); len(sends) != 2 || !strings.Contains(sends[1], "Goal NEW") || strings.Contains(sends[1], "Goal OLD") {
					t.Fatalf("second send = %q, want only Goal NEW", sends)
				}
				reconcileLogged := false
				for _, l := range logRules(t, h.paths.Log(h.id), "J12")[j12Before:] {
					if !strings.Contains(l.Decision, "delivered") && l.Decision != "" && l.Why != "" {
						reconcileLogged = true
						if !slices.Contains(l.Asked, "T1:worker_failed") {
							t.Fatalf("J12 reconciliation log missing non-superseded T1:worker_failed: %+v", l)
						}
						if slices.Contains(l.Asked, "project:new_goal") {
							t.Fatalf("superseded in-flight copy was logged as retried/exhausted: %+v", l)
						}
					}
				}
				if !reconcileLogged {
					t.Fatalf("missing J12 reconciliation log for non-superseded T1:worker_failed: %+v", logRules(t, h.paths.Log(h.id), "J12")[j12Before:])
				}
			})
		}
	})

	t.Run("failed_reconciliation_commit_preserves_in_flight_batch", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Failed reconciliation commit")
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, readHerdrFixture(t, "claude-idle.screen.txt"))
		// Advance to idle status (R17) first, then make log.jsonl read-only before R20 reconciles.
		h.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "idle"})
		h.now = h.now.Add(5 * time.Second)
		h.pass()

		if err := os.Chmod(h.paths.Log(h.id), 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(h.paths.Log(h.id), 0o600) })

		h.now = h.now.Add(25 * time.Second)
		h.pass() // R20 tries to reconcile -> commitLogLocked fails

		// Active batch must stay preserved on disk (not dropped) while log is unwritable.
		st := h.state()
		if len(st.Project.CoordWakesInFlight) != 1 || st.Project.CoordWakesInFlight[0].Key != "worker_failed" {
			t.Fatalf("failed reconciliation commit dropped in-flight batch: inFlight=%+v wakes=%+v",
				st.Project.CoordWakesInFlight, st.Project.Wakes)
		}

		// Restore writable log -> next pass reconciles cleanly and delivers retry once.
		if err := os.Chmod(h.paths.Log(h.id), 0o600); err != nil {
			t.Fatal(err)
		}
		h.now = h.now.Add(15 * time.Second)
		h.pass()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("after store recovery: sends=%d, want 2", got)
		}
	})
}

// TestPR2bRow5PhaseEndedPreservesBatch pins State Table Row 5:
// whenever the coordinator reaches PhaseEnded with a batch still in flight—
// even after Turn already advanced across a dialog or during idle_grace_seconds
// before R20 reached PhaseIdle—the remaining batch is prepended to the front of
// Wakes preserving each Retried bit (R33 exit/loss, R5/R19 quota, R34 Engine.Stop).
func TestPR2bRow5PhaseEndedPreservesBatch(t *testing.T) {
	t.Run("exit_after_turn_advance_before_idle_prepends_preserving_retried", func(t *testing.T) {
		for _, alreadyRetried := range []bool{false, true} {
			name := map[bool]string{false: "retried_false", true: "retried_true"}[alreadyRetried]
			t.Run(name, func(t *testing.T) {
				h, coord1 := startPR2bDeliveredWake(t, "Exit after turn advance")
				h.edit(func(s *flow.State) {
					s.Project.CoordWakesInFlight[0].Retried = alreadyRetried
					s.Project.Wakes = []flow.Wake{{Key: "main_red", Why: "base red", Facts: "red", At: h.now}}
				})

				// Working -> idle starts idle_grace_seconds and increments Turn (Turn > CoordWakeTurn)
				// while Run.State is still PhaseWorking (before R20 reaches PhaseIdle).
				h.setCoordWorking(coord1)
				h.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "idle"})
				h.now = h.now.Add(5 * time.Second)
				h.pass()
				if pre := h.state(); pre.Project.Run.State != flow.PhaseWorking || pre.Project.Run.Turn <= pre.Project.CoordWakeTurn {
					t.Fatalf("precondition failed: state=%s turn=%d wakeTurn=%d",
						pre.Project.Run.State, pre.Project.Run.Turn, pre.Project.CoordWakeTurn)
				}

				// Coordinator exits mid-grace (R33).
				h.srv.Finish(coord1, "killed")
				h.now = h.now.Add(5 * time.Second)
				h.pass()

				st := h.state()
				if len(st.Project.CoordWakesInFlight) != 0 {
					t.Fatalf("inFlight not cleared on exit: %+v", st.Project.CoordWakesInFlight)
				}
				if len(st.Project.Wakes) != 2 || st.Project.Wakes[0].Key != "worker_failed" || st.Project.Wakes[1].Key != "main_red" {
					t.Fatalf("after exit with advanced Turn: wakes=%+v, want [worker_failed, main_red] prepended", st.Project.Wakes)
				}
				if st.Project.Wakes[0].Retried != alreadyRetried {
					t.Fatalf("exit altered Retried bit: got %v, want %v", st.Project.Wakes[0].Retried, alreadyRetried)
				}
			})
		}
	})

	t.Run("quota_end_r19_prepends_ahead_of_newer_wakes_preserving_retried", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Quota R19 prepend")
		h.edit(func(s *flow.State) {
			s.Project.CoordWakesInFlight[0].Retried = true
			s.Project.Wakes = []flow.Wake{{Key: "main_red", Why: "base red", Facts: "red", At: h.now}}
		})

		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, "You've hit your limit · resets 5pm\n")
		h.coordIdleWithStatus(coord1, "idle") // R19 -> PhaseEnded(EndQuota)

		st := h.state()
		if st.Project.Run == nil || st.Project.Run.State != flow.PhaseEnded || st.Project.Run.Result != flow.EndQuota {
			t.Fatalf("expected R19 EndQuota, got run=%+v", st.Project.Run)
		}
		if len(st.Project.Wakes) != 2 || st.Project.Wakes[0].Key != "worker_failed" || st.Project.Wakes[1].Key != "main_red" {
			t.Fatalf("after R19 quota end: wakes=%+v, want [worker_failed, main_red] prepended", st.Project.Wakes)
		}
		if !st.Project.Wakes[0].Retried {
			t.Fatalf("R19 quota end cleared Retried bit on worker_failed: %+v", st.Project.Wakes[0])
		}
	})

	t.Run("owner_stop_and_restart_preserves_batch_even_after_turn_advance", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Owner stop preserves wake")
		h.edit(func(s *flow.State) {
			s.Project.Wakes = []flow.Wake{{Key: "main_red", Why: "base red", Facts: "red", At: h.now}}
		})

		// Advance Turn across a permission dialog (working -> blocked -> working):
		// R27 advances rec.Turn > CoordWakeTurn while State stays PhaseWorking.
		h.setCoordWorking(coord1)
		perm := "permission"
		h.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "blocked", Kind: &perm, Msg: "Allow?"})
		h.now = h.now.Add(5 * time.Second)
		h.pass()
		h.setCoordWorking(coord1)
		if pre := h.state(); pre.Project.Run.State != flow.PhaseWorking || pre.Project.Run.Turn <= pre.Project.CoordWakeTurn {
			t.Fatalf("precondition failed: state=%s turn=%d wakeTurn=%d",
				pre.Project.Run.State, pre.Project.Run.Turn, pre.Project.CoordWakeTurn)
		}

		if err := h.eng.Stop(h.repo); err != nil {
			t.Fatalf("Engine.Stop: %v", err)
		}
		st := h.state()
		if st.Project.Started || len(st.Project.CoordWakesInFlight) != 0 {
			t.Fatalf("after Engine.Stop: started=%v inFlight=%+v", st.Project.Started, st.Project.CoordWakesInFlight)
		}
		if len(st.Project.Wakes) != 2 || st.Project.Wakes[0].Key != "worker_failed" || st.Project.Wakes[1].Key != "main_red" || st.Project.Wakes[0].Retried {
			t.Fatalf("after Engine.Stop with advanced Turn: wakes=%+v, want [worker_failed(Retried=false), main_red]", st.Project.Wakes)
		}

		// Owner restarts -> replacement coordinator receives both wakes in order.
		if err := h.eng.Start(h.repo, "Owner stop preserves wake", 2); err != nil {
			t.Fatalf("Engine.Start: %v", err)
		}
		h.now = h.now.Add(15 * time.Second)
		h.pass()
		coord2 := h.state().Project.RunID
		if coord2 == "" || coord2 == coord1 {
			t.Fatalf("replacement coord2 did not start after restart: runID=%q", coord2)
		}
		h.coordIdleWithStatus(coord2, "idle")
		sends := sendsTo(t, h.srv.Requests(), coord2)
		if len(sends) != 1 || !strings.Contains(sends[0], "worker_failed") || !strings.Contains(sends[0], "main_red") {
			t.Fatalf("coord2 sends after restart = %q, want both held wakes", sends)
		}
	})
}

// TestPR2bRow6UnconfirmedBeforeFinish pins State Table Row 6 (and app-factory.md §10):
// unconfirmed wakes before finish get one bounded reconciliation retry before J4
// asks for the final journal (sent once) or stops the coordinator; a second
// unconfirmed turn exhausts and logs the wake so finish never deadlocks.
func TestPR2bRow6UnconfirmedBeforeFinish(t *testing.T) {
	t.Run("unconfirmed_quota_replacement_turn_retries_once_then_finishes", func(t *testing.T) {
		h := newProjectHarness(t, "Unconfirmed quota before finish")
		h.pass() // J7 starts coord1
		coord1 := h.state().Project.RunID
		h.edit(func(s *flow.State) {
			s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open", Parked: true}}
			s.Project.Wakes = []flow.Wake{{Key: "worker_note", Task: "T1", Why: "review note", Facts: "note body", At: h.now}}
		})
		h.coordIdleWithStatus(coord1, "idle") // J12 delivers worker_note to coord1

		h.srv.Finish(coord1, "exited")
		h.edit(func(s *flow.State) {
			s.Project.Run.State = flow.PhaseEnded
			s.Project.Run.Result = flow.EndQuota
		})
		h.now = h.now.Add(15 * time.Second)
		h.pass()

		h.edit(func(s *flow.State) {
			s.Tasks[0].Done = &flow.Merged{MergedCommit: "m1"}
		})
		h.now = h.now.Add(time.Duration(h.state().Settings.CoordinatorGapMinutes+1) * time.Minute)
		h.pass() // J7 starts coord2
		coord2 := h.state().Project.RunID

		// J12 delivers worker_note to coord2, but coord2's turn ends unconfirmed (no echo/reply).
		h.coordIdleWithStatus(coord2, "idle")
		h.setCoordWorking(coord2)
		h.srv.SetScreen(coord2, readHerdrFixture(t, "claude-idle.screen.txt"))
		h.coordIdleWithStatus(coord2, "idle")

		// J4 must NOT have asked final_journal yet; J12 retried worker_note once.
		if j4 := logRules(t, h.paths.Log(h.id), "J4"); len(j4) != 0 {
			t.Fatalf("J4 ran before unconfirmed wake retried: %+v", j4)
		}
		sends := sendsTo(t, h.srv.Requests(), coord2)
		if len(sends) != 2 || !strings.Contains(sends[0], "note body") || !strings.Contains(sends[1], "note body") {
			t.Fatalf("coord2 sends after unconfirmed turn = %q, want 2 worker_note deliveries", sends)
		}

		// Now coord2 completes the retry -> J4 asks final_journal once, then stops coord2.
		h.completeWakeTurn(coord2)
		h.finishThroughJ4(coord2)

		sends = sendsTo(t, h.srv.Requests(), coord2)
		if len(sends) != 3 || !strings.Contains(strings.ToLower(sends[2]), "final journal") {
			t.Fatalf("coord2 final sends = %q, want 2 wakes then 1 final journal", sends)
		}
		if h.state().Project.RunID != "" || !h.state().Project.Finished {
			t.Fatalf("project did not finish after retry confirmed: runID=%q finished=%v", h.state().Project.RunID, h.state().Project.Finished)
		}
	})

	t.Run("unconfirmed_say_during_final_journal_retries_once_and_journal_stays_single", func(t *testing.T) {
		h := newProjectHarness(t, "Unconfirmed Say before finish")
		h.pass() // J7 starts coord1
		coord1 := h.state().Project.RunID
		h.edit(func(s *flow.State) {
			s.Tasks = []flow.Task{{ID: "T1", Title: "feat: done", Done: &flow.Merged{MergedCommit: "m1"}}}
		})

		h.coordIdleWithStatus(coord1, "idle") // J4 asks final journal (once)
		h.setCoordWorking(coord1)

		const ownerWords = "Also mention the follow-up PR."
		if err := h.eng.Say(h.repo, ownerWords); err != nil {
			t.Fatalf("Say during final journal: %v", err)
		}

		// Final journal turn ends -> J12 delivers Say (not J4 stop).
		h.coordIdleWithStatus(coord1, "idle")

		// Say turn ends unconfirmed (readable redraw without echo) -> J4 holds, J12 retries Say once.
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, readHerdrFixture(t, "claude-idle.screen.txt"))
		h.coordIdleWithStatus(coord1, "idle")

		if h.state().Project.RunID != coord1 {
			t.Fatalf("J4 stopped coordinator before unconfirmed Say retried: runID=%q", h.state().Project.RunID)
		}
		if got := len(wakeSendsWith(t, h.srv.Requests(), ownerWords)); got != 2 {
			t.Fatalf("unconfirmed Say sends = %d, want 2", got)
		}

		// Second Say turn also ends unconfirmed -> exhausted & logged in J12, J4 stops coordinator without deadlock.
		saySend2 := lastSendTo(t, h.srv.Requests(), coord1)
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, formatPromptEcho("❯", saySend2)+"\n  ⎿ \u00a0Interrupted · What should Claude do instead?\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")
		h.now = h.now.Add(15 * time.Second)
		h.pass()

		if h.state().Project.RunID != "" || !h.state().Project.Finished {
			t.Fatalf("project wedged after exhausted Say before finish: runID=%q finished=%v", h.state().Project.RunID, h.state().Project.Finished)
		}
		j12 := logRules(t, h.paths.Log(h.id), "J12")
		if len(j12) == 0 || !hasJ12LogFor(j12[len(j12)-1:], "project:owner_say", "") || strings.Contains(j12[len(j12)-1].Decision, "delivered") {
			t.Fatalf("second unconfirmed Say missing J12 exhaustion log: %+v", j12)
		}
		// Final journal request must stay single.
		journalSends := 0
		for _, s := range sendsTo(t, h.srv.Requests(), coord1) {
			if strings.Contains(strings.ToLower(s), "final journal") {
				journalSends++
			}
		}
		if journalSends != 1 {
			t.Fatalf("final journal sends = %d, want 1", journalSends)
		}
	})
}
