package factory

// PR 6.1, TDD step 1: ask records (State.Asks), the ask table A2–A5, validation
// (KB-ASK-6, KB-ASK-9), in-process AnswerProof (KB-ASK-19), Engine methods
// (Asks, Answer, Say, Changes), worker/coordinator template replay checks, and
// restart safety across every step of an ask history (KB-ASK-10).
//
// Contracts pinned by these tests (design.md 7.1, 7.2, 9.3, 12.3):
//   - Report verb "ask":
//       worker/reviewer (`sheepdog factory report ask`): {"kind":"decision","question":...,"options":[...],"recommend":...}
//       coordinator (`sheepdog factory ask`): {"kind":<kind>,"task":<optional>,"question":...,"options":[...],"recommend":...}
//   - Report verb "answer":
//       coordinator (`sheepdog factory answer`): {"ask":<id>,"option":<label|index>,"why":<optional>}
//   - Owner gated answers go through Engine.MintProof + Engine.Answer(askID, option, proof).
//   - Engine.Changes() emits one Change per accepted ask and per accepted answer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

type askRig struct {
	*restartWorld
	coordAddr string
	worktree  string
}

// newAskRig starts one project through J7 (idle coordinator), plan apply, N11
// (worktree) and N23 (idle worker that has already made a commit over base).
func newAskRig(t *testing.T) *askRig {
	t.Helper()
	w := newRestartWorld(t, "Build cache store")
	w.edit(func(s *flow.State) { s.Settings.ReviewRounds = 2 })
	w.pass() // J7 starts coordinator
	coordAddr := w.state().Project.RunID
	w.applyPlan()
	w.waitIdle(coordAddr)
	w.pass() // N11 creates attempt worktree
	w.pass() // N23 starts worker
	w.waitIdle(w.workerID)
	wt := w.state().Tasks[0].Attempts[0].Worktree
	c1 := commitFile(t, wt, "cache.go", "package demo\n// partial work\n")
	w.commits = append(w.commits, c1)
	return &askRig{restartWorld: w, coordAddr: coordAddr, worktree: wt}
}

func drainChanges(ch <-chan Change) []Change {
	var out []Change
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, c)
		default:
			return out
		}
	}
}

func workerTexts(t *testing.T, srv *hctest.Server, client *hc.Client, addr string) []string {
	t.Helper()
	var out []string
	if term, err := client.Get(context.Background(), addr); err == nil && len(term.Argv) > 0 {
		out = append(out, term.Argv[len(term.Argv)-1])
	}
	return append(out, sendsTo(t, srv.Requests(), addr)...)
}

// TestWorkerReportAskWakesCoordinatorAndDeliversAnswer verifies that a worker
// mid-task after committing can open a decision ask, State.Asks stores it, the
// coordinator is woken once with the ask details, and the coordinator's answer
// is delivered to the worker (KB-ASK-1, KB-ASK-4, KB-ASK-10; design.md 7.1 A2, A3).
func TestWorkerReportAskWakesCoordinatorAndDeliversAnswer(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	coordBefore := len(sendsTo(t, r.srv.Requests(), r.coordAddr))
	workerBefore := len(sendsTo(t, r.srv.Requests(), r.workerID))

	askPayload := `{"kind":"decision","question":"Keep cache in SQLite or Plain JSON?","options":["SQLite","Plain JSON"],"recommend":"SQLite"}`
	if err := r.eng.Report(r.workTok, "ask", json.RawMessage(askPayload)); err != nil {
		t.Fatalf("worker report ask after commit: %v", err)
	}

	st := r.state()
	if len(st.Asks) != 1 {
		t.Fatalf("State.Asks len = %d, want 1", len(st.Asks))
	}
	gotAsk := st.Asks[0]
	if gotAsk.ID == "" || gotAsk.Kind != flow.KindDecision || gotAsk.Gated || gotAsk.Status != flow.AskStatusOpen ||
		gotAsk.Task != "T1" || gotAsk.AskedBy != flow.RoleWorker || gotAsk.Question != "Keep cache in SQLite or Plain JSON?" ||
		!slices.Equal(gotAsk.Options, []string{"SQLite", "Plain JSON"}) || gotAsk.Recommend != "SQLite" {
		t.Fatalf("stored ask = %+v", gotAsk)
	}

	// Pass delivers the held ask wake once via J12; a second pass does not wake again.
	r.pass()
	r.pass()
	coordSends := sendsTo(t, r.srv.Requests(), r.coordAddr)[coordBefore:]
	if len(coordSends) != 1 {
		t.Fatalf("coordinator wakes = %d (%q), want 1", len(coordSends), coordSends)
	}
	for _, want := range []string{gotAsk.ID, "Keep cache in SQLite or Plain JSON?", "SQLite", "Plain JSON", "factory answer"} {
		if !strings.Contains(coordSends[0], want) {
			t.Errorf("coordinator wake missing %q:\n%s", want, coordSends[0])
		}
	}

	ansPayload := fmt.Sprintf(`{"ask":%q,"option":"Plain JSON","why":"fewer dependencies"}`, gotAsk.ID)
	if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(ansPayload)); err != nil {
		t.Fatalf("coordinator answer: %v", err)
	}

	st = r.state()
	if st.Asks[0].Status != flow.AskStatusAnswered || st.Asks[0].Answer != "Plain JSON" ||
		st.Asks[0].AnsweredBy != flow.RoleCoordinator || st.Asks[0].Why != "fewer dependencies" || st.Asks[0].AnsweredAt.IsZero() {
		t.Fatalf("answered ask = %+v", st.Asks[0])
	}

	workerSends := sendsTo(t, r.srv.Requests(), r.workerID)[workerBefore:]
	if len(workerSends) != 1 || !strings.Contains(workerSends[0], "Plain JSON") || !strings.Contains(workerSends[0], "fewer dependencies") {
		t.Fatalf("worker sends = %q, want 1 message with Plain JSON and reason", workerSends)
	}
}

// TestWorkerAnswerDeliveredAfterWorkerRestartBetweenAskAndAnswer verifies
// KB-ASK-10 and design.md 7.1 A3: an open question outlives the worker terminal
// that asked it, and the coordinator's answer reaches the replacement worker
// both when answered while the worker is dead and when answered after the
// replacement worker has already started.
func TestWorkerAnswerDeliveredAfterWorkerRestartBetweenAskAndAnswer(t *testing.T) {
	t.Parallel()

	t.Run("answered_while_worker_dead_then_restarted", func(t *testing.T) {
		t.Parallel()
		r := newAskRig(t)
		firstWorker := r.workerID
		askPayload := `{"kind":"decision","question":"Use sync.Mutex or channel?","options":["sync.Mutex","channel"],"recommend":"sync.Mutex"}`
		if err := r.eng.Report(r.workTok, "ask", json.RawMessage(askPayload)); err != nil {
			t.Fatalf("worker report ask: %v", err)
		}
		askID := r.state().Asks[0].ID

		// Worker dies before the coordinator answers.
		if _, err := r.client.Kill(r.ctx, firstWorker); err != nil {
			t.Fatal(err)
		}
		r.now = r.now.Add(5 * time.Second)
		r.pass() // observes worker ended (R33) and wakes coordinator with the ask

		if st := r.state(); st.Asks[0].Status != flow.AskStatusOpen {
			t.Fatalf("ask closed when worker died: %+v", st.Asks[0])
		}

		// Coordinator answers while no worker run is live: answer is held on the task.
		ansPayload := fmt.Sprintf(`{"ask":%q,"option":"sync.Mutex","why":"simpler state guard"}`, askID)
		if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(ansPayload)); err != nil {
			t.Fatalf("coordinator answer while worker dead: %v", err)
		}

		// Advance past worker restart backoff so N23 launches the replacement worker.
		r.now = r.now.Add(65 * time.Second)
		r.pass()
		if r.workerID == "" || r.workerID == firstWorker {
			t.Fatalf("worker did not restart onto a new terminal: first=%q now=%q", firstWorker, r.workerID)
		}
		r.waitIdle(r.workerID)

		texts := strings.Join(workerTexts(t, r.srv, r.client, r.workerID), "\n")
		if !strings.Contains(texts, "sync.Mutex") || !strings.Contains(texts, "simpler state guard") {
			t.Fatalf("restarted worker %s did not receive held answer:\n%s", r.workerID, texts)
		}
	})

	t.Run("worker_restarted_then_answered", func(t *testing.T) {
		t.Parallel()
		r := newAskRig(t)
		firstWorker := r.workerID
		askPayload := `{"kind":"decision","question":"Retry three times or five?","options":["Three times","Five times"],"recommend":"Three times"}`
		if err := r.eng.Report(r.workTok, "ask", json.RawMessage(askPayload)); err != nil {
			t.Fatalf("worker report ask: %v", err)
		}
		askID := r.state().Asks[0].ID

		if _, err := r.client.Kill(r.ctx, firstWorker); err != nil {
			t.Fatal(err)
		}
		r.now = r.now.Add(5 * time.Second)
		r.pass()
		r.now = r.now.Add(65 * time.Second)
		r.pass() // replacement worker starts
		if r.workerID == "" || r.workerID == firstWorker {
			t.Fatalf("replacement worker did not start: first=%q now=%q", firstWorker, r.workerID)
		}
		r.waitIdle(r.workerID)
		before := len(sendsTo(t, r.srv.Requests(), r.workerID))

		ansPayload := fmt.Sprintf(`{"ask":%q,"option":"Five times","why":"matches backoff budget"}`, askID)
		if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(ansPayload)); err != nil {
			t.Fatalf("coordinator answer after worker restart: %v", err)
		}
		got := sendsTo(t, r.srv.Requests(), r.workerID)[before:]
		if len(got) != 1 || !strings.Contains(got[0], "Five times") || !strings.Contains(got[0], "matches backoff budget") {
			t.Fatalf("replacement worker sends = %q, want answer delivered to %s", got, r.workerID)
		}
	})
}

// TestAskRolesAndOwnerAnswerProof checks design.md 7.1, 7.2 and KB-ASK-15,
// KB-ASK-17, KB-ASK-19: workers/reviewers open only decision asks and never
// answer; coordinators open any v1 kind and answer only decision asks; gated
// asks require a verified in-process AnswerProof from a paired device or live
// voice (refusing empty proof, local device, typed input, and forged/mismatched proofs).
func TestAskRolesAndOwnerAnswerProof(t *testing.T) {
	t.Parallel()

	t.Run("role_permissions", func(t *testing.T) {
		t.Parallel()
		r := newAskRig(t)
		revAddr, err := startRun(r.ctx, r.client, world.RunSpec{Name: "rev-t1-1", Cwd: r.worktree, Argv: []string{"sh"}})
		if err != nil {
			t.Fatal(err)
		}
		r.edit(func(s *flow.State) {
			s.Tasks[0].Attempts[0].Review = &flow.ReviewRun{
				RunID: revAddr, RunTokenHash: tokenHash("tok-reviewer"), Round: 1, Head: r.commits[0], Code: "code1",
			}
		})

		// Workers and reviewers cannot open any gated ask kind.
		for _, kind := range []string{flow.KindGate, flow.KindRelease, flow.KindPaidKey, flow.KindDelete, flow.KindInstall, flow.KindReset, flow.KindRule} {
			payload := fmt.Sprintf(`{"kind":%q,"question":"Proceed?","options":["Yes","No"],"recommend":"Yes"}`, kind)
			err := r.eng.Report(r.workTok, "ask", json.RawMessage(payload))
			if refusalCode(err) != "kind_not_allowed" {
				t.Fatalf("worker ask kind %q = %v, want kind_not_allowed", kind, err)
			}
		}
		if err := r.eng.Report("tok-reviewer", "ask", json.RawMessage(`{"kind":"release","question":"Tag now?","options":["Yes","No"],"recommend":"Yes"}`)); refusalCode(err) != "kind_not_allowed" {
			t.Fatalf("reviewer ask release = %v, want kind_not_allowed", err)
		}
		if len(r.state().Asks) != 0 {
			t.Fatalf("refused gated asks mutated State.Asks: %+v", r.state().Asks)
		}

		// A reviewer may open a decision ask; neither worker nor reviewer may send answer.
		if err := r.eng.Report("tok-reviewer", "ask", json.RawMessage(`{"kind":"decision","question":"Is helper exported on purpose?","options":["Yes","No"],"recommend":"Yes"}`)); err != nil {
			t.Fatalf("reviewer decision ask: %v", err)
		}
		revAskID := r.state().Asks[0].ID
		for _, tok := range []string{r.workTok, "tok-reviewer"} {
			if err := r.eng.Report(tok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Yes"}`, revAskID))); refusalCode(err) != "kind_not_allowed" {
				t.Fatalf("non-coordinator answer with %q = %v, want kind_not_allowed", tok, err)
			}
		}

		// Coordinator opens a gated release ask, and is refused proof_required if it tries to answer it.
		relPayload := `{"kind":"release","task":"T1","question":"Tag v1.2.0 and publish release?","options":["Yes, tag v1.2.0","Not now"],"recommend":"Yes, tag v1.2.0"}`
		if err := r.eng.Report(r.coordTok, "ask", json.RawMessage(relPayload)); err != nil {
			t.Fatalf("coordinator release ask: %v", err)
		}
		asks := r.state().Asks
		if len(asks) != 2 || !asks[1].Gated || asks[1].Kind != flow.KindRelease || asks[1].Task != "T1" || asks[1].Status != flow.AskStatusOpen {
			t.Fatalf("stored release ask = %+v", asks)
		}
		if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Yes, tag v1.2.0","why":"looks green"}`, asks[1].ID))); refusalCode(err) != "proof_required" {
			t.Fatalf("coordinator answering gated ask = %v, want proof_required", err)
		}
	})

	t.Run("owner_answer_proof_guard", func(t *testing.T) {
		t.Parallel()
		r := newAskRig(t)
		r.edit(func(s *flow.State) {
			s.Asks = []flow.Ask{
				{
					ID: "ask-rel-1", Project: r.id, Folder: r.repo, Kind: flow.KindRelease, Gated: true,
					Question: "Tag v1.2.0 and publish release?", Options: []string{"Yes, tag v1.2.0", "Not now"},
					Recommend: "Yes, tag v1.2.0", Task: "T1", Head: r.commits[0],
					AskedBy: flow.RoleCoordinator, RunID: r.coordAddr, At: r.now, Status: flow.AskStatusOpen,
				},
				{
					ID: "ask-gate-1", Project: r.id, Folder: r.repo, Kind: flow.KindGate, Gated: true,
					Question: "Approve T1 design?", Options: []string{"Yes, approve", "No, revise"},
					Recommend: "Yes, approve", Task: "T1", Head: r.commits[0],
					AskedBy: flow.RoleCoordinator, RunID: r.coordAddr, At: r.now, Status: flow.AskStatusOpen,
				},
			}
		})

		// Engine.Answer without proof is refused proof_required.
		if err := r.eng.Answer("ask-rel-1", "Yes, tag v1.2.0", AnswerProof{}); refusalCode(err) != "proof_required" {
			t.Fatalf("Engine.Answer empty proof = %v, want proof_required", err)
		}

		// MintProof refuses ticketless local device and typed inputs (KB-ASK-19).
		for _, bad := range []struct{ name, inputKind, device string }{
			{"empty device", ProofInputTap, ""},
			{"ticketless local tap", ProofInputTap, "local"},
			{"ticketless local voice", ProofInputVoice, "local"},
			{"typed input on paired device", "typed", "dev-phone-1"},
			{"empty input kind", "", "dev-phone-1"},
		} {
			if _, err := r.eng.MintProof("ask-rel-1", "Yes, tag v1.2.0", bad.inputKind, bad.device); refusalCode(err) != "proof_rejected" {
				t.Fatalf("MintProof(%s) = %v, want proof_rejected", bad.name, err)
			}
		}

		// Forged unminted proof, wrong ask ID, and wrong option are refused proof_rejected.
		forged := AnswerProof{ID: "prf-0000deadbeef", AskID: "ask-rel-1", Option: "Yes, tag v1.2.0", InputKind: ProofInputTap, DeviceID: "dev-phone-1", At: r.now}
		if err := r.eng.Answer("ask-rel-1", "Yes, tag v1.2.0", forged); refusalCode(err) != "proof_rejected" {
			t.Fatalf("forged proof = %v, want proof_rejected", err)
		}
		otherProof, err := r.eng.MintProof("ask-gate-1", "Yes, approve", ProofInputTap, "dev-phone-1")
		if err != nil {
			t.Fatalf("MintProof(other ask): %v", err)
		}
		if err := r.eng.Answer("ask-rel-1", "Yes, tag v1.2.0", otherProof); refusalCode(err) != "proof_rejected" {
			t.Fatalf("wrong-ask proof = %v, want proof_rejected", err)
		}
		wrongOptProof, err := r.eng.MintProof("ask-rel-1", "Not now", ProofInputTap, "dev-phone-1")
		if err != nil {
			t.Fatalf("MintProof(other option): %v", err)
		}
		if err := r.eng.Answer("ask-rel-1", "Yes, tag v1.2.0", wrongOptProof); refusalCode(err) != "proof_rejected" {
			t.Fatalf("wrong-option proof = %v, want proof_rejected", err)
		}

		// Valid paired-device proof unlocks the gated ask, records proof & device, and logs device_id.
		validProof, err := r.eng.MintProof("ask-rel-1", "Yes, tag v1.2.0", ProofInputTap, "dev-phone-1")
		if err != nil || !strings.HasPrefix(validProof.ID, "prf-") {
			t.Fatalf("MintProof valid = (%+v, %v), want prf-* proof", validProof, err)
		}
		coordBefore := len(sendsTo(t, r.srv.Requests(), r.coordAddr))
		if err := r.eng.Answer("ask-rel-1", "Yes, tag v1.2.0", validProof); err != nil {
			t.Fatalf("Engine.Answer with valid paired-device proof: %v", err)
		}
		r.pass()

		gotRel := r.state().Asks[0]
		if gotRel.Status != flow.AskStatusAnswered || gotRel.Answer != "Yes, tag v1.2.0" || gotRel.AnsweredBy != "owner" ||
			gotRel.ProofID != validProof.ID || gotRel.DeviceID != "dev-phone-1" {
			t.Fatalf("answered gated ask = %+v", gotRel)
		}
		rawLog, _ := os.ReadFile(r.paths.Log(r.id))
		if !strings.Contains(string(rawLog), "dev-phone-1") {
			t.Fatalf("log.jsonl does not name answering device dev-phone-1:\n%s", rawLog)
		}
		if coordAfter := sendsTo(t, r.srv.Requests(), r.coordAddr)[coordBefore:]; len(coordAfter) == 0 || !strings.Contains(strings.Join(coordAfter, "\n"), "Yes, tag v1.2.0") {
			t.Fatalf("coordinator did not receive gated answer notification: %q", coordAfter)
		}

		// Live voice proof also unlocks a gated ask.
		voiceProof, err := r.eng.MintProof("ask-gate-1", "Yes, approve", ProofInputVoice, "voice-session")
		if err != nil {
			t.Fatalf("MintProof voice: %v", err)
		}
		if err := r.eng.Answer("ask-gate-1", "Yes, approve", voiceProof); err != nil {
			t.Fatalf("Engine.Answer voice: %v", err)
		}
		if got := r.state().Asks[1]; got.Status != flow.AskStatusAnswered || got.AnsweredBy != "owner" || got.ProofID != voiceProof.ID {
			t.Fatalf("voice-answered gate ask = %+v", got)
		}
	})
}

// TestAskAndAnswerValidationRefusals verifies typed Refusal codes at the Engine
// acceptance boundary for empty/multi-line/overlong questions, unknown kinds,
// too few or too many choices, duplicate/parenthetical/unrecommended choices,
// answers outside the choices, and answers to already-answered or cancelled asks
// (KB-ASK-6, KB-ASK-9, KB-ASK-15; design.md 7.1 A5, 7.2).
func TestAskAndAnswerValidationRefusals(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	seq0 := r.state().Seq

	ten := `["O1","O2","O3","O4","O5","O6","O7","O8","O9","O10"]`
	askRefusals := []struct {
		name, tok, payload, want string
	}{
		{"empty question", r.workTok, `{"kind":"decision","question":"   ","options":["Yes","No"],"recommend":"Yes"}`, "invalid_ask"},
		{"multi-line question", r.workTok, `{"kind":"decision","question":"line1\nline2","options":["Yes","No"],"recommend":"Yes"}`, "invalid_ask"},
		{"unknown kind", r.coordTok, `{"kind":"deploy","question":"Deploy?","options":["Yes","No"],"recommend":"Yes"}`, "unknown_kind"},
		{"empty kind from coordinator", r.coordTok, `{"kind":"","question":"Deploy?","options":["Yes","No"],"recommend":"Yes"}`, "unknown_kind"},
		{"one choice", r.workTok, `{"kind":"decision","question":"Only one?","options":["Yes"],"recommend":"Yes"}`, "invalid_ask"},
		{"ten choices (too many)", r.workTok, `{"kind":"decision","question":"Pick one?","options":` + ten + `,"recommend":"O1"}`, "invalid_ask"},
		{"duplicate choices", r.workTok, `{"kind":"decision","question":"Pick?","options":["Yes"," Yes "],"recommend":"Yes"}`, "invalid_ask"},
		{"recommend not in choices", r.workTok, `{"kind":"decision","question":"Pick?","options":["Yes","No"],"recommend":"Maybe"}`, "invalid_ask"},
		{"parenthetical short key in choice (KB-ASK-9)", r.workTok, `{"kind":"decision","question":"Switch?","options":["Switch (v2)","Keep"],"recommend":"Keep"}`, "invalid_ask"},
		{"recommended wait without Yes choice (KB-ASK-9)", r.coordTok, `{"kind":"release","question":"Ship?","options":["Ship now","Wait for QA"],"recommend":"Wait for QA"}`, "invalid_ask"},
	}
	for _, tc := range askRefusals {
		err := r.eng.Report(tc.tok, "ask", json.RawMessage(tc.payload))
		if refusalCode(err) != tc.want {
			t.Fatalf("%s: err = %v, want refusal %q", tc.name, err, tc.want)
		}
	}
	if r.state().Seq != seq0 || len(r.state().Asks) != 0 {
		t.Fatalf("refused asks mutated state: seq=%d (want %d), asks=%+v", r.state().Seq, seq0, r.state().Asks)
	}

	// Open one decision ask and one gated ask, plus seed one cancelled ask.
	if err := r.eng.Report(r.workTok, "ask", json.RawMessage(`{"kind":"decision","question":"SQLite or JSON?","options":["SQLite","JSON"],"recommend":"SQLite"}`)); err != nil {
		t.Fatalf("open decision ask: %v", err)
	}
	if err := r.eng.Report(r.coordTok, "ask", json.RawMessage(`{"kind":"release","question":"Tag v1.0.0?","options":["Yes, tag v1.0.0","Not now"],"recommend":"Yes, tag v1.0.0"}`)); err != nil {
		t.Fatalf("open release ask: %v", err)
	}
	if err := r.eng.Report(r.workTok, "ask", json.RawMessage(`{"kind":"decision","question":"Cancel me?","options":["A","B"],"recommend":"A"}`)); err != nil {
		t.Fatalf("open third ask: %v", err)
	}
	r.edit(func(s *flow.State) { s.Asks[2].Status = flow.AskStatusCancelled })
	decID, relID, canID := r.state().Asks[0].ID, r.state().Asks[1].ID, r.state().Asks[2].ID

	// Unknown ask id -> not_found.
	if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(`{"ask":"ask-missing","option":"SQLite"}`)); refusalCode(err) != "not_found" {
		t.Fatalf("coordinator answer unknown ask = %v, want not_found", err)
	}
	if err := r.eng.Answer("ask-missing", "Yes", AnswerProof{}); refusalCode(err) != "not_found" {
		t.Fatalf("Engine.Answer unknown ask = %v, want not_found", err)
	}

	// Answer not among choices -> invalid_option.
	if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Postgres"}`, decID))); refusalCode(err) != "invalid_option" {
		t.Fatalf("coordinator answer bad option = %v, want invalid_option", err)
	}
	badOptProof, err := r.eng.MintProof(relID, "Ship tomorrow", ProofInputTap, "dev-phone-1")
	if err == nil {
		if aerr := r.eng.Answer(relID, "Ship tomorrow", badOptProof); refusalCode(aerr) != "invalid_option" {
			t.Fatalf("Engine.Answer bad option = %v, want invalid_option", aerr)
		}
	} else if refusalCode(err) != "invalid_option" {
		t.Fatalf("MintProof bad option = %v, want invalid_option (or MintProof ok + Answer invalid_option)", err)
	}

	// Answer to a cancelled ask -> already_closed (A5).
	if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"A"}`, canID))); refusalCode(err) != "already_closed" {
		t.Fatalf("answer cancelled ask = %v, want already_closed", err)
	}

	// Answer both open asks, then verify a second answer to each is refused already_closed (A5).
	if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"SQLite","why":"chosen"}`, decID))); err != nil {
		t.Fatalf("first coordinator answer: %v", err)
	}
	if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"JSON","why":"changed mind"}`, decID))); refusalCode(err) != "already_closed" {
		t.Fatalf("second coordinator answer = %v, want already_closed", err)
	}

	prf, err := r.eng.MintProof(relID, "Yes, tag v1.0.0", ProofInputTap, "dev-phone-1")
	if err != nil {
		t.Fatalf("MintProof: %v", err)
	}
	if err := r.eng.Answer(relID, "Yes, tag v1.0.0", prf); err != nil {
		t.Fatalf("first owner answer: %v", err)
	}
	prf2, _ := r.eng.MintProof(relID, "Not now", ProofInputTap, "dev-phone-1")
	if err := r.eng.Answer(relID, "Not now", prf2); refusalCode(err) != "already_closed" {
		t.Fatalf("second owner answer = %v, want already_closed", err)
	}
}

// TestEngineAsksAnswerSayAndChanges verifies design.md 12.3: Asks(open) filters
// open vs all asks in creation order, Changes() delivers one Change per ask and
// per answer to subscribers (and none on refusals), and Say delivers the
// owner's words verbatim to the project's coordinator.
func TestEngineAsksAnswerSayAndChanges(t *testing.T) {
	t.Parallel()

	t.Run("asks_filter_and_order", func(t *testing.T) {
		t.Parallel()
		r := newAskRig(t)
		r.edit(func(s *flow.State) {
			s.Asks = []flow.Ask{
				{ID: "ask-1", Project: r.id, Kind: flow.KindDecision, Question: "Q1?", Options: []string{"A", "B"}, Recommend: "A", At: r.now, Status: flow.AskStatusAnswered, Answer: "A", AnsweredBy: flow.RoleCoordinator, AnsweredAt: r.now},
				{ID: "ask-2", Project: r.id, Kind: flow.KindRelease, Gated: true, Question: "Q2?", Options: []string{"Yes", "No"}, Recommend: "Yes", At: r.now.Add(time.Second), Status: flow.AskStatusOpen},
			}
		})
		if gotOpen := r.eng.Asks(true); len(gotOpen) != 1 || gotOpen[0].ID != "ask-2" {
			t.Fatalf("Asks(true) = %+v, want [ask-2]", gotOpen)
		}
		if gotAll := r.eng.Asks(false); len(gotAll) != 2 || gotAll[0].ID != "ask-1" || gotAll[1].ID != "ask-2" {
			t.Fatalf("Asks(false) = %+v, want [ask-1, ask-2]", gotAll)
		}
	})

	t.Run("changes_stream_per_ask_and_answer", func(t *testing.T) {
		t.Parallel()
		r := newAskRig(t)
		sub := r.eng.Changes()

		if got := r.eng.Asks(true); len(got) != 0 {
			t.Fatalf("initial Asks(true) = %+v, want empty", got)
		}

		// Open ask 1 (worker decision).
		if err := r.eng.Report(r.workTok, "ask", json.RawMessage(`{"kind":"decision","question":"Format A or B?","options":["Format A","Format B"],"recommend":"Format A"}`)); err != nil {
			t.Fatalf("open ask 1: %v", err)
		}
		ev1 := drainChanges(sub)
		if len(ev1) != 1 {
			t.Fatalf("changes after ask 1 = %+v, want 1 change", ev1)
		}

		// Open ask 2 (coordinator release).
		r.now = r.now.Add(time.Second)
		if err := r.eng.Report(r.coordTok, "ask", json.RawMessage(`{"kind":"release","task":"T1","question":"Tag v0.9.0?","options":["Yes, tag v0.9.0","Hold"],"recommend":"Yes, tag v0.9.0"}`)); err != nil {
			t.Fatalf("open ask 2: %v", err)
		}
		ev2 := drainChanges(sub)
		if len(ev2) != 1 {
			t.Fatalf("changes after ask 2 = %+v, want 1 change", ev2)
		}

		openAsks := r.eng.Asks(true)
		if len(openAsks) != 2 || openAsks[0].Kind != flow.KindDecision || openAsks[1].Kind != flow.KindRelease || openAsks[0].Project != r.id {
			t.Fatalf("Asks(true) = %+v, want [decision, release] for %s", openAsks, r.id)
		}
		if ev1[0].AskID != openAsks[0].ID || ev2[0].AskID != openAsks[1].ID {
			t.Fatalf("change AskIDs = (%q, %q), want (%q, %q)", ev1[0].AskID, ev2[0].AskID, openAsks[0].ID, openAsks[1].ID)
		}

		// A refused ask and a refused answer emit zero changes.
		_ = r.eng.Report(r.workTok, "ask", json.RawMessage(`{"kind":"release","question":"Bad?","options":["Yes","No"],"recommend":"Yes"}`))
		_ = r.eng.Answer(openAsks[1].ID, "Yes, tag v0.9.0", AnswerProof{})
		if stray := drainChanges(sub); len(stray) != 0 {
			t.Fatalf("refusals emitted changes: %+v", stray)
		}

		// Answer ask 1 (coordinator) -> 1 change; Asks(true) has 1 left, Asks(false) has 2.
		if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Format B","why":"matches API"}`, openAsks[0].ID))); err != nil {
			t.Fatalf("answer ask 1: %v", err)
		}
		ev3 := drainChanges(sub)
		if len(ev3) != 1 || ev3[0].AskID != openAsks[0].ID {
			t.Fatalf("changes after answer 1 = %+v, want 1 change for %s", ev3, openAsks[0].ID)
		}
		if gotOpen, gotAll := r.eng.Asks(true), r.eng.Asks(false); len(gotOpen) != 1 || gotOpen[0].ID != openAsks[1].ID || len(gotAll) != 2 {
			t.Fatalf("after answer 1: open=%+v all=%+v", gotOpen, gotAll)
		}

		// Answer ask 2 (owner with proof) -> 1 change; Asks(true) is empty, Asks(false) has 2 answered.
		prf, err := r.eng.MintProof(openAsks[1].ID, "Yes, tag v0.9.0", ProofInputTap, "dev-browser-1")
		if err != nil {
			t.Fatalf("MintProof: %v", err)
		}
		if err := r.eng.Answer(openAsks[1].ID, "Yes, tag v0.9.0", prf); err != nil {
			t.Fatalf("answer ask 2: %v", err)
		}
		ev4 := drainChanges(sub)
		if len(ev4) != 1 || ev4[0].AskID != openAsks[1].ID {
			t.Fatalf("changes after answer 2 = %+v, want 1 change for %s", ev4, openAsks[1].ID)
		}
		if gotOpen, gotAll := r.eng.Asks(true), r.eng.Asks(false); len(gotOpen) != 0 || len(gotAll) != 2 ||
			gotAll[0].Status != flow.AskStatusAnswered || gotAll[1].Status != flow.AskStatusAnswered {
			t.Fatalf("after answer 2: open=%+v all=%+v", gotOpen, gotAll)
		}
	})

	t.Run("say_delivers_owner_words_to_coordinator", func(t *testing.T) {
		t.Parallel()
		r := newAskRig(t)
		if err := r.eng.Say("missing-project", "hello"); !errors.Is(err, ErrProjectNotFound) && refusalCode(err) != "not_found" {
			t.Fatalf("Say unknown project = %v, want ErrProjectNotFound or not_found", err)
		}
		if err := r.eng.Say(r.repo, "   "); refusalCode(err) != "why_missing" && err == nil {
			t.Fatalf("Say empty text = %v, want refusal", err)
		}
		coordBefore := len(sendsTo(t, r.srv.Requests(), r.coordAddr))
		const ownerWords = "Pause releases until the benchmark numbers are in."
		if err := r.eng.Say(r.repo, ownerWords); err != nil {
			t.Fatalf("Say valid: %v", err)
		}
		r.pass()
		coordSends := sendsTo(t, r.srv.Requests(), r.coordAddr)[coordBefore:]
		if len(coordSends) == 0 || !strings.Contains(strings.Join(coordSends, "\n"), ownerWords) {
			t.Fatalf("coordinator did not receive owner Say verbatim: %q", coordSends)
		}
	})
}

// TestTemplatesMentionReportAskAndAnswer checks prompts.md 5.1, 5.3 and 7:
// worker.md names `sheepdog factory report ask` with `--option` and `--recommend`
// (and never `AskUserQuestion`), and coordinator.md names `sheepdog factory answer`
// and `sheepdog factory ask` (KB-ASK-2, KB-COO-41).
func TestTemplatesMentionReportAskAndAnswer(t *testing.T) {
	t.Parallel()
	workerFilled := filled("worker", workerTemplate)
	coordFilled := filled("coordinator", coordinatorTemplate)

	wantSituations := []situation{
		{
			ID:    "W-ask",
			Role:  "worker",
			Run:   []string{"sheepdog factory report ask"},
			Says:  []string{"--option", "--recommend"},
			Never: []string{"AskUserQuestion"},
		},
		{
			ID:   "C-answer",
			Role: "coordinator",
			Run:  []string{"sheepdog factory answer"},
		},
		{
			ID:   "C-ask",
			Role: "coordinator",
			Run:  []string{"sheepdog factory ask"},
		},
	}
	for _, s := range wantSituations {
		prompt := map[string]string{"worker": workerFilled, "coordinator": coordFilled}[s.Role]
		if !held(s, prompt) {
			t.Errorf("%s (%s): no single template rule line holds run=%v says=%v never=%v:\n%s",
				s.ID, s.Role, s.Run, s.Says, s.Never, prompt)
		}
	}
}

type askRestartEnd struct {
	restartEnd
	Asks          []string
	CoordAskWakes int
}

func summarizeAskRestart(w *restartWorld) askRestartEnd {
	w.t.Helper()
	base := w.summarize()
	st := w.state()
	var asks []string
	for _, a := range st.Asks {
		asks = append(asks, fmt.Sprintf("%s:%s:%s:%s:%s", a.Kind, a.Task, a.Status, a.Answer, a.AnsweredBy))
	}
	askWakes := 0
	for _, r := range w.srv.Requests() {
		var p struct{ Terminal, Text string }
		if r.Method == "terminal.send" && json.Unmarshal(r.Params, &p) == nil && strings.Contains(p.Text, "Keep cache in SQLite or Plain JSON?") {
			askWakes++
		}
	}
	return askRestartEnd{restartEnd: base, Asks: asks, CoordAskWakes: askWakes}
}

func runAskRestartHistory(t *testing.T, h restartHistory, restartAfter int) askRestartEnd {
	t.Helper()
	w := h.setup(t)
	for i, step := range h.steps {
		step.run(w)
		if i == restartAfter {
			w.restart()
		}
	}
	if restartAfter == len(h.steps)-1 {
		w.pass()
	}
	return summarizeAskRestart(w)
}

// TestAskRestartAfterEveryStep replays complete worker-ask-to-merge histories
// with a daemon restart after every step, proving asks survive restarts, wakes
// are never duplicated, and a worker killed between ask and answer still
// converges to the exact same merged end state (KB-ASK-10; design.md 15, 16).
func TestAskRestartAfterEveryStep(t *testing.T) {
	t.Parallel()
	passStep := func(name string, advance time.Duration) restartStep {
		return restartStep{name, func(w *restartWorld) {
			w.now = w.now.Add(advance)
			w.pass()
		}}
	}

	histories := []restartHistory{
		{
			name:  "worker_ask_answered_to_merge",
			setup: func(t *testing.T) *restartWorld { return newRestartWorld(t, "Ask coordinator then merge") },
			steps: []restartStep{
				passStep("J7 start coord", 0),
				{"plan apply and coord idle", func(w *restartWorld) {
					cid := w.state().Project.RunID
					w.applyPlan()
					w.waitIdle(cid)
				}},
				passStep("N11 new attempt", 0),
				passStep("N23 start worker", 0),
				{"worker commits partial work and reports ask", func(w *restartWorld) {
					w.waitIdle(w.workerID)
					wt := w.state().Tasks[0].Attempts[0].Worktree
					c := commitFile(w.t, wt, "partial.go", "package demo\n")
					w.commits = append(w.commits, c)
					payload := `{"kind":"decision","question":"Keep cache in SQLite or Plain JSON?","options":["SQLite","Plain JSON"],"recommend":"SQLite"}`
					if err := w.eng.Report(w.workTok, "ask", json.RawMessage(payload)); err != nil {
						w.t.Fatalf("worker report ask: %v", err)
					}
				}},
				passStep("J12 deliver ask wake once", 15*time.Second),
				{"coordinator answers decision ask", func(w *restartWorld) {
					cid := w.state().Project.RunID
					w.srv.SetStatus(cid, &hc.Status{App: "claude-code", State: "working"})
					askID := w.state().Asks[0].ID
					if err := w.eng.Report(w.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Plain JSON","why":"keep stdlib only"}`, askID))); err != nil {
						w.t.Fatalf("coordinator answer: %v", err)
					}
					w.waitIdle(cid)
				}},
				{"worker receives answer, commits and reports ready", func(w *restartWorld) {
					w.waitIdle(w.workerID)
					w.commitAndReady("package demo\n// Plain JSON\n", greenRollup, true)
				}},
				passStep("N26 push and open PR", 0),
				passStep("N29 start round 1", 0),
				{"round 1 pass verdict", func(w *restartWorld) { w.passReviewRound() }},
				passStep("N29 start round 2", 0),
				{"round 2 pass verdict", func(w *restartWorld) { w.passReviewRound() }},
				passStep("N42 accept and merge", 0),
				{"PR merged, N2 done", func(w *restartWorld) {
					w.markPRMerged("merged-ask-1")
					w.now = w.now.Add(15 * time.Second)
					w.pass()
				}},
				passStep("N1 cleanup", 15*time.Second),
			},
		},
		{
			name: "worker_killed_after_ask_answered_on_restart_to_merge",
			setup: func(t *testing.T) *restartWorld {
				w := newRestartWorld(t, "Kill worker between ask and answer")
				w.edit(func(s *flow.State) { s.Settings.ReviewRounds = 1 })
				return w
			},
			steps: []restartStep{
				passStep("J7 start coord", 0),
				{"plan apply and coord idle", func(w *restartWorld) {
					cid := w.state().Project.RunID
					w.applyPlan()
					w.waitIdle(cid)
				}},
				passStep("N11 new attempt", 0),
				passStep("N23 start worker", 0),
				{"worker commits and reports ask then dies", func(w *restartWorld) {
					w.waitIdle(w.workerID)
					wt := w.state().Tasks[0].Attempts[0].Worktree
					c := commitFile(w.t, wt, "partial.go", "package demo\n")
					w.commits = append(w.commits, c)
					payload := `{"kind":"decision","question":"Keep cache in SQLite or Plain JSON?","options":["SQLite","Plain JSON"],"recommend":"SQLite"}`
					if err := w.eng.Report(w.workTok, "ask", json.RawMessage(payload)); err != nil {
						w.t.Fatalf("worker report ask: %v", err)
					}
					if _, err := w.client.Kill(w.ctx, w.workerID); err != nil {
						w.t.Fatal(err)
					}
				}},
				passStep("observe worker exit and J12 deliver ask wake", 5*time.Second),
				{"coordinator answers while worker is dead", func(w *restartWorld) {
					cid := w.state().Project.RunID
					w.srv.SetStatus(cid, &hc.Status{App: "claude-code", State: "working"})
					askID := w.state().Asks[0].ID
					if err := w.eng.Report(w.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Plain JSON","why":"keep stdlib only"}`, askID))); err != nil {
						w.t.Fatalf("coordinator answer: %v", err)
					}
					w.waitIdle(cid)
				}},
				passStep("N23 resume worker with held answer", 65*time.Second),
				{"restarted worker idle and ready", func(w *restartWorld) {
					w.waitIdle(w.workerID)
					w.commitAndReady("package demo\n// Plain JSON\n", greenRollup, true)
				}},
				passStep("N26 push and open PR", 0),
				passStep("N29 start review", 0),
				{"review pass verdict", func(w *restartWorld) { w.passReviewRound() }},
				passStep("N42 accept and merge", 0),
				{"PR merged, N2 done", func(w *restartWorld) {
					w.markPRMerged("merged-ask-2")
					w.now = w.now.Add(15 * time.Second)
					w.pass()
				}},
				passStep("N1 cleanup", 15*time.Second),
			},
		},
	}

	for _, h := range histories {
		t.Run(h.name, func(t *testing.T) {
			t.Parallel()
			want := runAskRestartHistory(t, h, -1)
			if want.TaskDone == "" || want.CoordAskWakes != 1 || !slices.Equal(want.Asks, []string{"decision:T1:answered:Plain JSON:coordinator"}) ||
				len(want.PRMerges) != 1 || want.PRCreates > 1 || want.LiveTaskRuns != 0 || want.LiveCoords > 1 || !want.WorktreeGone {
				t.Fatalf("baseline %s bad end state: %+v", h.name, want)
			}
			for k, step := range h.steps {
				t.Run(fmt.Sprintf("after_%02d_%s", k, strings.ReplaceAll(step.name, " ", "_")), func(t *testing.T) {
					t.Parallel()
					got := runAskRestartHistory(t, h, k)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("restart after step %d (%s) diverged:\ngot:  %+v\nwant: %+v", k, step.name, got, want)
					}
				})
			}
		})
	}
}

// TestWorkerAnswerHeldWhenWorkerDiedOrHCDownBetweenPasses verifies KB-ASK-5,
// KB-ASK-10, and design.md 2.2 / 7.1 A3: when an idle worker dies or hc becomes
// unreachable between passes, answering its decision ask succeeds, commits once,
// holds the answer on Attempt.Held (without losing the coordinator wake), and
// delivers it to the next worker run.
func TestWorkerAnswerHeldWhenWorkerDiedOrHCDownBetweenPasses(t *testing.T) {
	t.Parallel()

	t.Run("worker_died_between_passes", func(t *testing.T) {
		t.Parallel()
		r := newAskRig(t)
		firstWorker := r.workerID
		if err := r.eng.Report(r.workTok, "ask", json.RawMessage(`{"kind":"decision","question":"Use SQLite or Plain JSON?","options":["SQLite","Plain JSON"],"recommend":"SQLite"}`)); err != nil {
			t.Fatalf("worker report ask: %v", err)
		}
		askID := r.state().Asks[0].ID

		// J12 delivers ask:<id> wake to coordinator and records it in CoordWakesInFlight.
		r.pass()
		if st := r.state(); len(st.Project.Wakes) != 0 || len(st.Project.CoordWakesInFlight) != 1 {
			t.Fatalf("after J12: wakes=%+v inFlight=%+v, want 0 queued and 1 in-flight", st.Project.Wakes, st.Project.CoordWakesInFlight)
		}

		// Worker dies between passes (no pass() yet; State.Tasks[0].Attempts[0].Run.State is still PhaseIdle).
		if _, err := r.client.Kill(r.ctx, firstWorker); err != nil {
			t.Fatal(err)
		}
		seqBefore := r.state().Seq

		ansPayload := fmt.Sprintf(`{"ask":%q,"option":"Plain JSON","why":"fewer deps"}`, askID)
		if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(ansPayload)); err != nil {
			t.Fatalf("coordinator answer when worker died between passes: %v", err)
		}

		st := r.state()
		if st.Seq != seqBefore+1 {
			t.Fatalf("state seq = %d, want %d (single commit per answer input)", st.Seq, seqBefore+1)
		}
		if st.Asks[0].Status != flow.AskStatusAnswered || st.Asks[0].Answer != "Plain JSON" || st.Asks[0].Why != "fewer deps" {
			t.Fatalf("answered ask = %+v", st.Asks[0])
		}
		if held := st.Tasks[0].Attempts[0].Held; len(held) != 1 || !strings.Contains(held[0], "Plain JSON") || !strings.Contains(held[0], "fewer deps") {
			t.Fatalf("Attempt.Held = %q, want 1 held answer with Plain JSON and reason", held)
		}

		// Coordinator finishes its turn after answering; next passes observe worker exit and start replacement worker.
		r.srv.SetStatus(r.coordAddr, &hc.Status{App: "claude-code", State: "working"})
		r.srv.SetStatus(r.coordAddr, &hc.Status{App: "claude-code", State: "idle"})
		r.pass()
		r.now = r.now.Add(65 * time.Second)
		r.pass()

		if r.workerID == "" || r.workerID == firstWorker {
			t.Fatalf("replacement worker did not start: first=%q now=%q", firstWorker, r.workerID)
		}
		r.waitIdle(r.workerID)
		texts := strings.Join(workerTexts(t, r.srv, r.client, r.workerID), "\n")
		if !strings.Contains(texts, "Plain JSON") || !strings.Contains(texts, "fewer deps") {
			t.Fatalf("replacement worker %s did not receive held answer:\n%s", r.workerID, texts)
		}
		if held := r.state().Tasks[0].Attempts[0].Held; len(held) != 0 {
			t.Fatalf("Attempt.Held not cleared after delivery to replacement worker: %q", held)
		}
	})

	t.Run("hc_down_between_passes", func(t *testing.T) {
		t.Parallel()
		srv := hctest.New(t)
		var failHC atomic.Bool
		proxyPath := methodFailProxy(t, srv.Path, "", &failHC)
		client := hc.NewClient(proxyPath)
		repo, _ := initGitRepoWithRemote(t)
		githubOrigin(t, repo)
		_ = bindRepoFakeGH(t, repo)
		paths := testPaths(t)

		var mu sync.Mutex
		var coordTok, workTok, workerID string
		srv.OnRun = func(term hc.Terminal, p hc.RunParams) {
			mu.Lock()
			defer mu.Unlock()
			switch p.Labels["sheepdog.role"] {
			case flow.RoleCoordinator:
				coordTok = p.Env["SHEEPDOG_RUN_TOKEN"]
			case flow.RoleWorker:
				workTok = p.Env["SHEEPDOG_RUN_TOKEN"]
				workerID = term.Address
			}
		}

		now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
		eng := newTestEngine(t, paths, client)
		eng.Now = func() time.Time { return now }
		if err := eng.Start(repo, "HC outage during ask answer", 2); err != nil {
			t.Fatal(err)
		}
		id := eng.projectDirs()[0].Name()
		pass := func(d time.Duration) {
			now = now.Add(d)
			eng.passProject(context.Background(), id, false)
		}
		waitIdle := func(addr string) {
			srv.SetStatus(addr, &hc.Status{App: "claude-code", State: "idle"})
			pass(5 * time.Second)
			pass(25 * time.Second)
		}
		loadSt := func() flow.State {
			s, err := LoadState(paths.State(id))
			if err != nil {
				t.Fatal(err)
			}
			return s
		}

		pass(0) // J7 starts coordinator
		coordAddr := loadSt().Project.RunID
		srv.SetStatus(coordAddr, &hc.Status{App: "claude-code", State: "working"})
		body := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: demo change","brief":"Add demo.go.",` + testScopeJSON + `,"outcomes":[{"text":"demo exists","proved_by":"manual: demo.go"}]}]}`
		if err := eng.Report(coordTok, "plan apply", []byte(body)); err != nil {
			t.Fatal(err)
		}
		waitIdle(coordAddr)
		pass(0) // N11 worktree
		pass(0) // N23 starts worker
		mu.Lock()
		firstWorker, wtTok := workerID, workTok
		mu.Unlock()
		waitIdle(firstWorker)
		wt := loadSt().Tasks[0].Attempts[0].Worktree
		_ = commitFile(t, wt, "cache.go", "package demo\n// partial\n")

		if err := eng.Report(wtTok, "ask", json.RawMessage(`{"kind":"decision","question":"Keep cache in SQLite or Plain JSON?","options":["SQLite","Plain JSON"],"recommend":"SQLite"}`)); err != nil {
			t.Fatalf("worker report ask: %v", err)
		}
		askID := loadSt().Asks[0].ID
		pass(0) // J12 delivers ask wake to coordinator

		// hc goes down between passes; coordinator answers while hc is unreachable.
		failHC.Store(true)
		ansPayload := fmt.Sprintf(`{"ask":%q,"option":"Plain JSON","why":"hc down test"}`, askID)
		if err := eng.Report(coordTok, "answer", json.RawMessage(ansPayload)); err != nil {
			t.Fatalf("coordinator answer while hc down: %v", err)
		}

		st := loadSt()
		if st.Asks[0].Status != flow.AskStatusAnswered || st.Asks[0].Answer != "Plain JSON" {
			t.Fatalf("answered ask while hc down = %+v", st.Asks[0])
		}
		if held := st.Tasks[0].Attempts[0].Held; len(held) != 1 || !strings.Contains(held[0], "Plain JSON") || !strings.Contains(held[0], "hc down test") {
			t.Fatalf("Attempt.Held while hc down = %q, want held answer", held)
		}

		// Worker died during the hc outage, then hc recovers.
		srv.Finish(firstWorker, "exited")
		failHC.Store(false)
		pass(5 * time.Second)
		pass(65 * time.Second)

		mu.Lock()
		nextWorker := workerID
		mu.Unlock()
		if nextWorker == "" || nextWorker == firstWorker {
			t.Fatalf("replacement worker did not start after hc recovery: first=%q now=%q", firstWorker, nextWorker)
		}
		waitIdle(nextWorker)
		texts := strings.Join(workerTexts(t, srv, client, nextWorker), "\n")
		if !strings.Contains(texts, "Plain JSON") || !strings.Contains(texts, "hc down test") {
			t.Fatalf("replacement worker %s did not receive held answer:\n%s", nextWorker, texts)
		}
	})

	t.Run("r36_single_commit_when_worker_already_ended", func(t *testing.T) {
		t.Parallel()
		r := newAskRig(t)
		if err := r.eng.Report(r.workTok, "ask", json.RawMessage(`{"kind":"decision","question":"Use mutex or channel?","options":["Mutex","Channel"],"recommend":"Mutex"}`)); err != nil {
			t.Fatalf("worker report ask: %v", err)
		}
		askID := r.state().Asks[0].ID
		if _, err := r.client.Kill(r.ctx, r.workerID); err != nil {
			t.Fatal(err)
		}
		r.now = r.now.Add(5 * time.Second)
		r.pass() // R33 transitions worker run to PhaseEnded
		seqBefore := r.state().Seq

		if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Mutex","why":"simpler"}`, askID))); err != nil {
			t.Fatalf("answer when worker PhaseEnded: %v", err)
		}
		st := r.state()
		if st.Seq != seqBefore+1 {
			t.Fatalf("state seq after R36 answer = %d, want %d (single commit of state.json)", st.Seq, seqBefore+1)
		}
		if held := st.Tasks[0].Attempts[0].Held; len(held) != 1 || !strings.Contains(held[0], "Mutex") {
			t.Fatalf("Attempt.Held = %q, want held answer", held)
		}
	})
}

type reviewerAskRig struct {
	*askRig
	mu     sync.Mutex
	revID  string
	revTok string
}

func newReviewerAskRig(t *testing.T, reviewRounds int) *reviewerAskRig {
	t.Helper()
	r := newAskRig(t)
	if reviewRounds > 0 {
		r.edit(func(s *flow.State) { s.Settings.ReviewRounds = reviewRounds })
	}
	rr := &reviewerAskRig{askRig: r}
	prev := r.srv.OnRun
	r.srv.OnRun = func(term hc.Terminal, p hc.RunParams) {
		if prev != nil {
			prev(term, p)
		}
		if p.Labels["sheepdog.role"] == flow.RoleReviewer {
			rr.mu.Lock()
			rr.revID = term.Address
			rr.revTok = p.Env["SHEEPDOG_RUN_TOKEN"]
			rr.mu.Unlock()
		}
	}
	r.commitAndReady("package demo\n", greenRollup, true)
	r.pass() // N26 pushes and opens PR
	r.pass() // N29 starts round 1 reviewer
	if revID, revTok := rr.currentReviewer(); revID == "" || revTok == "" {
		t.Fatalf("round 1 reviewer did not start: id=%q tok=%q", revID, revTok)
	}
	return rr
}

func (rr *reviewerAskRig) currentReviewer() (string, string) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return rr.revID, rr.revTok
}

// TestReviewerDecisionAskDeliveryAndClosure verifies design.md 5 & 7.1 A3:
//   - An answer to a live reviewer's decision ask is delivered only to that
//     asking reviewer run via flow.EvDeliver (stepping PhaseIdle -> PhaseWorking
//     so N29 does not nudge it on the next pass).
//   - If that reviewer run has ended, died between passes, or been replaced by
//     a retry or later review round, the answer is closed with a reason logged
//     and never pasted to the worker (or Attempt.Held) or to another reviewer.
func TestReviewerDecisionAskDeliveryAndClosure(t *testing.T) {
	t.Parallel()

	t.Run("idle_reviewer_receives_answer_via_EvDeliver_without_N29_nudge", func(t *testing.T) {
		t.Parallel()
		rr := newReviewerAskRig(t, 1)
		rev1, revTok1 := rr.currentReviewer()
		rr.srv.SetStatus(rev1, &hc.Status{App: "claude-code", State: "working"})
		rr.pass()

		if err := rr.eng.Report(revTok1, "ask", json.RawMessage(`{"kind":"decision","question":"Is helper exported intentionally?","options":["Yes","No"],"recommend":"Yes"}`)); err != nil {
			t.Fatalf("reviewer report ask: %v", err)
		}
		askID := rr.state().Asks[0].ID

		// Put reviewer in post-R20 PhaseIdle state before N29 has nudged it.
		rr.srv.SetStatus(rev1, &hc.Status{App: "claude-code", State: "idle"})
		rr.edit(func(s *flow.State) {
			run := s.Tasks[0].Attempts[0].Review.Run
			run.State = flow.PhaseIdle
			run.Status = "idle"
			run.StatusSince = rr.now
		})

		revBefore := len(sendsTo(t, rr.srv.Requests(), rev1))
		workerBefore := len(sendsTo(t, rr.srv.Requests(), rr.workerID))

		if err := rr.eng.Report(rr.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Yes","why":"public package API"}`, askID))); err != nil {
			t.Fatalf("answer idle reviewer: %v", err)
		}

		st := rr.state()
		rev := st.Tasks[0].Attempts[0].Review
		if rev == nil || rev.Run == nil || rev.Run.State != flow.PhaseWorking {
			t.Fatalf("reviewer run state after EvDeliver = %+v, want %q", rev, flow.PhaseWorking)
		}
		revSends := sendsTo(t, rr.srv.Requests(), rev1)[revBefore:]
		if len(revSends) != 1 || !strings.Contains(revSends[0], "Yes") || !strings.Contains(revSends[0], "public package API") {
			t.Fatalf("reviewer sends = %q, want 1 message with Yes and reason", revSends)
		}

		// Next pass must not see RunIdle and must not trigger N29's nudge.
		rr.now = rr.now.Add(5 * time.Second)
		rr.pass()
		if after := sendsTo(t, rr.srv.Requests(), rev1)[revBefore:]; len(after) != 1 {
			t.Fatalf("N29 wrongly nudged/sent to reviewer after EvDeliver: %q", after)
		}
		if workerSends := sendsTo(t, rr.srv.Requests(), rr.workerID)[workerBefore:]; len(workerSends) != 0 || len(rr.state().Tasks[0].Attempts[0].Held) != 0 {
			t.Fatalf("worker received reviewer answer: sends=%q held=%q", workerSends, rr.state().Tasks[0].Attempts[0].Held)
		}
	})

	t.Run("ended_reviewer_never_pastes_to_worker_and_closes_ask", func(t *testing.T) {
		t.Parallel()
		rr := newReviewerAskRig(t, 1)
		rr.edit(func(s *flow.State) { s.Settings.ReviewerRetries = 0 })
		rev1, revTok1 := rr.currentReviewer()
		rr.srv.SetStatus(rev1, &hc.Status{App: "claude-code", State: "working"})
		rr.pass()

		if err := rr.eng.Report(revTok1, "ask", json.RawMessage(`{"kind":"decision","question":"Treat missing doc comment as major?","options":["Yes","No"],"recommend":"No"}`)); err != nil {
			t.Fatalf("reviewer report ask: %v", err)
		}
		askID := rr.state().Asks[0].ID

		if _, err := rr.client.Kill(rr.ctx, rev1); err != nil {
			t.Fatal(err)
		}
		rr.now = rr.now.Add(5 * time.Second)
		rr.pass() // observes reviewer ended (RunID cleared, Run.State == PhaseEnded)

		workerBefore := len(sendsTo(t, rr.srv.Requests(), rr.workerID))
		err := rr.eng.Report(rr.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"No","why":"optional on internal helpers"}`, askID)))
		if err != nil && refusalCode(err) != flow.RefuseClosed {
			t.Fatalf("answer after reviewer ended = %v, want nil (closed) or already_closed", err)
		}

		st := rr.state()
		if st.Asks[0].Status == flow.AskStatusOpen {
			t.Fatalf("ask stayed open after reviewer ended: %+v", st.Asks[0])
		}
		if workerSends := sendsTo(t, rr.srv.Requests(), rr.workerID)[workerBefore:]; len(workerSends) != 0 {
			t.Fatalf("reviewer answer pasted to worker: %q", workerSends)
		}
		if held := st.Tasks[0].Attempts[0].Held; len(held) != 0 {
			t.Fatalf("reviewer answer put onto worker Attempt.Held: %q", held)
		}
		rawLog, _ := os.ReadFile(rr.paths.Log(rr.id))
		if !strings.Contains(string(rawLog), askID) {
			t.Fatalf("log.jsonl missing ask closure reason for %s:\n%s", askID, rawLog)
		}
	})

	t.Run("retry_reviewer_never_receives_previous_reviewer_answer", func(t *testing.T) {
		t.Parallel()
		rr := newReviewerAskRig(t, 1)
		rev1, revTok1 := rr.currentReviewer()
		rr.srv.SetStatus(rev1, &hc.Status{App: "claude-code", State: "working"})
		rr.pass()

		if err := rr.eng.Report(revTok1, "ask", json.RawMessage(`{"kind":"decision","question":"Accept relaxed timeout?","options":["Yes","No"],"recommend":"Yes"}`)); err != nil {
			t.Fatalf("reviewer 1 report ask: %v", err)
		}
		askID := rr.state().Asks[0].ID

		// Round 1 reviewer dies and N29 starts a retry reviewer rev2.
		if _, err := rr.client.Kill(rr.ctx, rev1); err != nil {
			t.Fatal(err)
		}
		rr.now = rr.now.Add(5 * time.Second)
		rr.pass()
		rev2, _ := rr.currentReviewer()
		if rev2 == "" || rev2 == rev1 {
			t.Fatalf("retry reviewer did not start: rev1=%q rev2=%q", rev1, rev2)
		}

		rev2Before := len(sendsTo(t, rr.srv.Requests(), rev2))
		workerBefore := len(sendsTo(t, rr.srv.Requests(), rr.workerID))

		err := rr.eng.Report(rr.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Yes","why":"matches SLA"}`, askID)))
		if err != nil && refusalCode(err) != flow.RefuseClosed {
			t.Fatalf("answer old reviewer ask while retry running = %v", err)
		}

		if got := sendsTo(t, rr.srv.Requests(), rev2)[rev2Before:]; len(got) != 0 {
			t.Fatalf("retry reviewer %s wrongly received old reviewer %s's answer: %q", rev2, rev1, got)
		}
		if workerSends := sendsTo(t, rr.srv.Requests(), rr.workerID)[workerBefore:]; len(workerSends) != 0 || len(rr.state().Tasks[0].Attempts[0].Held) != 0 {
			t.Fatalf("worker wrongly received old reviewer answer: sends=%q held=%q", workerSends, rr.state().Tasks[0].Attempts[0].Held)
		}
		if st := rr.state(); st.Asks[0].Status == flow.AskStatusOpen {
			t.Fatalf("old reviewer ask stayed open after retry started: %+v", st.Asks[0])
		}
		rawLog, _ := os.ReadFile(rr.paths.Log(rr.id))
		if !strings.Contains(string(rawLog), askID) {
			t.Fatalf("log.jsonl missing closure record for %s:\n%s", askID, rawLog)
		}
	})

	t.Run("new_round_reviewer_never_receives_previous_round_answer", func(t *testing.T) {
		t.Parallel()
		rr := newReviewerAskRig(t, 2)
		rev1, revTok1 := rr.currentReviewer()
		rr.srv.SetStatus(rev1, &hc.Status{App: "claude-code", State: "working"})
		rr.pass()

		if err := rr.eng.Report(revTok1, "ask", json.RawMessage(`{"kind":"decision","question":"Allow nil config fallback?","options":["Yes","No"],"recommend":"Yes"}`)); err != nil {
			t.Fatalf("round 1 reviewer ask: %v", err)
		}
		askID := rr.state().Asks[0].ID

		// Round 1 passes and N29 starts round 2 reviewer rev2.
		rr.passReviewRound()
		rr.pass()
		rev2, _ := rr.currentReviewer()
		if rev2 == "" || rev2 == rev1 {
			t.Fatalf("round 2 reviewer did not start: rev1=%q rev2=%q", rev1, rev2)
		}

		rev2Before := len(sendsTo(t, rr.srv.Requests(), rev2))
		workerBefore := len(sendsTo(t, rr.srv.Requests(), rr.workerID))

		err := rr.eng.Report(rr.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Yes","why":"backward compatible"}`, askID)))
		if err != nil && refusalCode(err) != flow.RefuseClosed {
			t.Fatalf("answer round 1 ask while round 2 running = %v", err)
		}

		if got := sendsTo(t, rr.srv.Requests(), rev2)[rev2Before:]; len(got) != 0 {
			t.Fatalf("round 2 reviewer %s wrongly received round 1's answer: %q", rev2, got)
		}
		if workerSends := sendsTo(t, rr.srv.Requests(), rr.workerID)[workerBefore:]; len(workerSends) != 0 || len(rr.state().Tasks[0].Attempts[0].Held) != 0 {
			t.Fatalf("worker wrongly received round 1 reviewer answer: sends=%q held=%q", workerSends, rr.state().Tasks[0].Attempts[0].Held)
		}
		if st := rr.state(); st.Asks[0].Status == flow.AskStatusOpen {
			t.Fatalf("round 1 reviewer ask stayed open in round 2: %+v", st.Asks[0])
		}
	})

	t.Run("reviewer_died_between_passes_closes_without_error_or_worker_paste", func(t *testing.T) {
		t.Parallel()
		rr := newReviewerAskRig(t, 1)
		rev1, revTok1 := rr.currentReviewer()
		rr.srv.SetStatus(rev1, &hc.Status{App: "claude-code", State: "working"})
		rr.pass()

		if err := rr.eng.Report(revTok1, "ask", json.RawMessage(`{"kind":"decision","question":"Flag unused helper?","options":["Yes","No"],"recommend":"No"}`)); err != nil {
			t.Fatalf("reviewer ask: %v", err)
		}
		askID := rr.state().Asks[0].ID

		// Reviewer dies between passes (no pass() yet; att.Review.RunID still points at rev1).
		if _, err := rr.client.Kill(rr.ctx, rev1); err != nil {
			t.Fatal(err)
		}
		workerBefore := len(sendsTo(t, rr.srv.Requests(), rr.workerID))

		if err := rr.eng.Report(rr.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"No","why":"used by tests"}`, askID))); err != nil {
			t.Fatalf("answer when reviewer died between passes = %v, want nil (closed with reason logged)", err)
		}

		st := rr.state()
		if st.Asks[0].Status == flow.AskStatusOpen {
			t.Fatalf("ask stayed open after reviewer died between passes: %+v", st.Asks[0])
		}
		if workerSends := sendsTo(t, rr.srv.Requests(), rr.workerID)[workerBefore:]; len(workerSends) != 0 || len(st.Tasks[0].Attempts[0].Held) != 0 {
			t.Fatalf("worker wrongly received dead reviewer answer: sends=%q held=%q", workerSends, st.Tasks[0].Attempts[0].Held)
		}
		rawLog, _ := os.ReadFile(rr.paths.Log(rr.id))
		if !strings.Contains(string(rawLog), askID) {
			t.Fatalf("log.jsonl missing reason for closed reviewer ask %s:\n%s", askID, rawLog)
		}
	})
}

// TestProofsStoreSafetyAndPersistence checks proofs.json hygiene (KB-TST-1,
// design.md 15, 16): real-home Paths are refused, empty DataDir fails clearly
// instead of minting an unverifiable proof, missing proofs.json starts empty,
// and unreadable or malformed proofs.json returns an error from MintProof
// without wiping unexpired proofs.
func TestProofsStoreSafetyAndPersistence(t *testing.T) {
	t.Parallel()

	t.Run("no_data_dir_refuses_mint_clearly", func(t *testing.T) {
		t.Parallel()
		e := New(Paths{}, nil)
		if _, err := e.MintProof("ask-1", "Yes", ProofInputTap, "dev-phone-1"); err == nil {
			t.Fatal("MintProof with empty DataDir succeeded; want clear error instead of minting an unverifiable proof")
		}
	})

	t.Run("real_home_refused_by_guard", func(t *testing.T) {
		t.Parallel()
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip(err)
		}
		e := New(Paths{DataDir: filepath.Join(home, ".local", "share", "sheepdog")}, nil)
		if _, err := e.MintProof("ask-1", "Yes", ProofInputTap, "dev-phone-1"); err == nil || !strings.Contains(err.Error(), "outside the temp dir") {
			t.Fatalf("MintProof under real home = %v, want guardTestPath error", err)
		}
	})

	t.Run("missing_file_starts_empty_and_persists_0600", func(t *testing.T) {
		t.Parallel()
		paths := testPaths(t)
		e := New(paths, nil)
		p, err := e.MintProof("ask-1", "Yes", ProofInputTap, "dev-phone-1")
		if err != nil || p.ID == "" {
			t.Fatalf("MintProof on missing proofs.json = (%+v, %v)", p, err)
		}
		proofPath := filepath.Join(paths.DataDir, "factory", "proofs.json")
		fi, err := os.Stat(proofPath)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("proofs.json stat = (%v, %v), want mode 0600", fi, err)
		}
	})

	t.Run("malformed_proofs_file_errors_without_wiping", func(t *testing.T) {
		t.Parallel()
		paths := testPaths(t)
		e := New(paths, nil)
		proofPath := filepath.Join(paths.DataDir, "factory", "proofs.json")
		if err := os.MkdirAll(filepath.Dir(proofPath), 0o700); err != nil {
			t.Fatal(err)
		}
		const corrupt = "{malformed json"
		if err := os.WriteFile(proofPath, []byte(corrupt), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := e.MintProof("ask-2", "Yes", ProofInputTap, "dev-phone-1"); err == nil {
			t.Fatal("MintProof succeeded over malformed proofs.json; want error")
		}
		raw, err := os.ReadFile(proofPath)
		if err != nil || string(raw) != corrupt {
			t.Fatalf("malformed proofs.json was modified/wiped: err=%v contents=%q", err, raw)
		}
	})

	t.Run("unreadable_proofs_file_errors_without_wiping_unexpired_proofs", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root ignores 0000 file mode")
		}
		r := newAskRig(t)
		r.edit(func(s *flow.State) {
			s.Asks = []flow.Ask{
				{
					ID: "ask-rel-1", Project: r.id, Folder: r.repo, Kind: flow.KindRelease, Gated: true,
					Question: "Tag v1.2.0?", Options: []string{"Yes, tag v1.2.0", "Not now"},
					Recommend: "Yes, tag v1.2.0", Task: "T1", Head: r.commits[0],
					AskedBy: flow.RoleCoordinator, RunID: r.coordAddr, At: r.now, Status: flow.AskStatusOpen,
				},
				{
					ID: "ask-gate-1", Project: r.id, Folder: r.repo, Kind: flow.KindGate, Gated: true,
					Question: "Approve T1?", Options: []string{"Yes, approve", "No, revise"},
					Recommend: "Yes, approve", Task: "T1", Head: r.commits[0],
					AskedBy: flow.RoleCoordinator, RunID: r.coordAddr, At: r.now, Status: flow.AskStatusOpen,
				},
			}
		})
		p1, err := r.eng.MintProof("ask-rel-1", "Yes, tag v1.2.0", ProofInputTap, "dev-phone-1")
		if err != nil {
			t.Fatalf("MintProof p1: %v", err)
		}
		proofPath := filepath.Join(r.paths.DataDir, "factory", "proofs.json")
		if err := os.Chmod(proofPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(proofPath, 0o600) })

		if _, err := r.eng.MintProof("ask-gate-1", "Yes, approve", ProofInputTap, "dev-phone-1"); err == nil {
			t.Fatal("MintProof succeeded on unreadable proofs.json; want error")
		}

		if err := os.Chmod(proofPath, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := r.eng.Answer("ask-rel-1", "Yes, tag v1.2.0", p1); err != nil {
			t.Fatalf("unexpired proof p1 was wiped or invalidated by failed MintProof: %v", err)
		}
	})
}
