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
	"reflect"
	"slices"
	"strings"
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
