package supervisor

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

type stubFactoryEngine struct {
	mu      sync.Mutex
	open    map[string]factory.Ask
	answers []factory.AnswerRequest
	failErr error
}

func newStubFactoryEngine(asks ...factory.Ask) *stubFactoryEngine {
	m := make(map[string]factory.Ask, len(asks))
	for _, a := range asks {
		m[a.ID] = a
	}
	return &stubFactoryEngine{open: m}
}

func (e *stubFactoryEngine) client(t *testing.T, proofs *factory.ProofStore) *factory.Client {
	t.Helper()
	return factory.NewClient(factory.ClientOptions{
		DataDir: t.TempDir(),
		Getenv:  func(string) string { return "" },
		Exec: func(_ context.Context, args ...string) ([]byte, string, int, error) {
			e.mu.Lock()
			defer e.mu.Unlock()

			switch {
			case reflect.DeepEqual(args, []string{"owner", "asks", "--open", "--json"}):
				list := make([]factory.Ask, 0, len(e.open))
				for _, a := range e.open {
					if a.Status == "open" {
						list = append(list, a)
					}
				}
				slices.SortFunc(list, func(a, b factory.Ask) int {
					if c := cmp.Compare(a.At, b.At); c != 0 {
						return c
					}
					return cmp.Compare(a.ID, b.ID)
				})
				raw, _ := json.Marshal(factory.AsksResponse{Protocol: 1, Asks: list})
				return raw, "", 0, nil

			case len(args) >= 5 && args[0] == "owner" && args[1] == "answer":
				askID := args[2]
				var opt, proofID string
				for i := 3; i < len(args); i++ {
					switch args[i] {
					case "--option":
						if i+1 < len(args) {
							opt = args[i+1]
							i++
						}
					case "--proof":
						if i+1 < len(args) {
							proofID = args[i+1]
							i++
						}
					}
				}
				if e.failErr != nil {
					return nil, e.failErr.Error(), 1, e.failErr
				}
				ask, ok := e.open[askID]
				if !ok || ask.Status != "open" {
					raw, _ := json.Marshal(factory.ErrorEnvelope{Protocol: 1, OK: false, Code: factory.CodeAlreadyClosed, Error: "already closed"})
					return raw, "", 2, nil
				}
				idx := 1
				if opt == "2" {
					idx = 2
				}
				label := ask.Options[idx-1]
				if ask.Gated {
					if _, _, _, err := proofs.Verify(askID, proofID, label); err != nil {
						raw, _ := json.Marshal(factory.ErrorEnvelope{Protocol: 1, OK: false, Code: factory.CodeProofRejected, Error: err.Error()})
						return raw, "", 1, nil
					}
				}
				e.answers = append(e.answers, factory.AnswerRequest{AskID: askID, Option: opt, Proof: proofID})
				by := "owner"
				nowMS := ask.At + 1000
				ask.Status = "answered"
				ask.Answer = &label
				ask.AnsweredAt = &nowMS
				ask.AnsweredBy = &by
				if proofID != "" {
					ask.ProofID = &proofID
				}
				e.open[askID] = ask
				unlocked := "task_accepted"
				raw, _ := json.Marshal(factory.AnswerResponse{
					Protocol: 1,
					OK:       true,
					Unlocked: &unlocked,
					Ask:      ask,
				})
				return raw, "", 0, nil
			default:
				t.Fatalf("unexpected engine args: %v", args)
				return nil, "", 1, nil
			}
		},
	})
}

func sampleGateAsk(id string) factory.Ask {
	folder := "/Users/me/projects/beacon"
	task := "BCN-33"
	ms := "M2"
	return factory.Ask{
		ID:        id,
		Project:   "beacon",
		Name:      "Beacon",
		Folder:    &folder,
		Kind:      factory.AskKindGate,
		Gated:     true,
		Question:  "BCN-33 is out of review rounds after rebase. Accept and merge it?",
		Options:   []string{"Accept and merge", "Send back to worker"},
		Recommend: "Accept and merge",
		Task:      &task,
		Milestone: &ms,
		Subject:   map[string]any{"target": "task", "ref": "BCN-33"},
		At:        1763301000000,
		Status:    "open",
	}
}

type factoryTestNotifier struct {
	mu    sync.Mutex
	notes []push.Notification
}

func (c *factoryTestNotifier) Notify(n push.Notification) {
	c.mu.Lock()
	c.notes = append(c.notes, n)
	c.mu.Unlock()
}

func TestFactoryAsksSyncAsCardsNotificationsAndSettleIndependently(t *testing.T) {
	ask1 := sampleGateAsk("ask002")
	ask2 := sampleGateAsk("ask004")
	ask2.Kind = factory.AskKindPaidKey
	ask2.Question = "Allow Beacon tasks to use stored secret DEEPSEEK_API_KEY?"
	ask2.Options = []string{"Allow for Beacon", "Deny"}
	ask2.Recommend = "Allow for Beacon"
	ask2.At = 1763303000000

	notes := &factoryTestNotifier{}
	var proofs *factory.ProofStore
	eng := newStubFactoryEngine(ask1, ask2)

	h := newHarness(t, func(cfg *Config) {
		var err error
		proofs, err = factory.NewProofStore(t.TempDir(), cfg.Now)
		if err != nil {
			t.Fatal(err)
		}
		cfg.FactoryProofs = proofs
		cfg.Factory = eng.client(t, proofs)
		cfg.Notifier = notes
	})

	if err := h.sup.SyncFactoryAsks(context.Background()); err != nil {
		t.Fatalf("SyncFactoryAsks: %v", err)
	}

	qs := h.store.Questions()
	if len(qs) != 2 {
		t.Fatalf("Questions = %d, want 2: %+v", len(qs), qs)
	}
	if qs[0].ID != "fask-ask002" || qs[1].ID != "fask-ask004" {
		t.Fatalf("question IDs = [%s, %s], want [fask-ask002, fask-ask004]", qs[0].ID, qs[1].ID)
	}
	if qs[0].Agent != "Beacon" || qs[0].Command != "gate · BCN-33" || !qs[0].Options[0].Recommended {
		t.Fatalf("unexpected card fields on qs[0]: %+v", qs[0])
	}
	if len(notes.notes) != 2 || notes.notes[0].Kind != push.KindApproval || notes.notes[0].PendingID != "fask-ask002" {
		t.Fatalf("APNs notifications = %+v", notes.notes)
	}

	// Answering ask002 from a paired phone settles fask-ask002 while keeping fask-ask004 open.
	h.clock.Advance(time.Second)
	res, err := h.sup.AnswerTap(context.Background(), "fask-ask002", 1, "dev-iphone-1")
	if err != nil {
		t.Fatalf("AnswerTap(paired): %v", err)
	}
	if !strings.Contains(res.Spoken, "Beacon") {
		t.Fatalf("Spoken = %q, want Beacon named", res.Spoken)
	}
	qs = h.store.Questions()
	if len(qs) != 1 || qs[0].ID != "fask-ask004" {
		t.Fatalf("remaining questions = %+v, want only fask-ask004", qs)
	}
	if proofs.Count() != 1 {
		t.Fatalf("proofs.Count() = %d, want 1", proofs.Count())
	}

	// External cancellation of ask004 clears fask-ask004 on the next sync.
	eng.mu.Lock()
	a4 := eng.open["ask004"]
	a4.Status = "cancelled"
	eng.open["ask004"] = a4
	eng.mu.Unlock()

	if err := h.sup.SyncFactoryAsks(context.Background()); err != nil {
		t.Fatalf("second SyncFactoryAsks: %v", err)
	}
	if len(h.store.Questions()) != 0 {
		t.Fatalf("expected 0 open questions after cancel, got %+v", h.store.Questions())
	}
}

func TestFactoryUnknownAskKindRefusedWithoutCreatingCardOrProof(t *testing.T) {
	h := newHarness(t)
	bad := sampleGateAsk("bad001")
	bad.Kind = factory.AskKind("deploy")

	err := h.sup.IngestFactoryAsks([]factory.Ask{bad})
	if !errors.Is(err, factory.ErrUnknownAskKind) {
		t.Fatalf("IngestFactoryAsks err = %v, want ErrUnknownAskKind", err)
	}
	if _, has := h.pending(); has || len(h.store.Questions()) != 0 {
		t.Fatalf("unknown ask kind created pending/question state: %+v", h.store.Questions())
	}
}

func TestFactoryBadSiblingAskDoesNotHideValidAsks(t *testing.T) {
	good := sampleGateAsk("ask002")
	bad := sampleGateAsk("bad009")
	bad.Kind = factory.AskKind("deploy")
	bad.At = 1763302000000

	var proofs *factory.ProofStore
	eng := newStubFactoryEngine(good, bad)

	h := newHarness(t, func(cfg *Config) {
		var err error
		proofs, err = factory.NewProofStore(t.TempDir(), cfg.Now)
		if err != nil {
			t.Fatal(err)
		}
		cfg.FactoryProofs = proofs
		cfg.Factory = eng.client(t, proofs)
	})

	err := h.sup.SyncFactoryAsks(context.Background())
	if !errors.Is(err, factory.ErrUnknownAskKind) {
		t.Fatalf("SyncFactoryAsks err = %v, want ErrUnknownAskKind", err)
	}
	qs := h.store.Questions()
	if len(qs) != 1 || qs[0].ID != "fask-ask002" {
		t.Fatalf("Questions = %+v, want [fask-ask002] despite bad sibling ask", qs)
	}
	if n := h.log.count("factory.ask_rejected"); n != 1 {
		t.Fatalf("factory.ask_rejected = %d, want 1", n)
	}

	// Answering the valid ask works even while the bad sibling ask sits in the store.
	h.clock.Advance(time.Second)
	if _, err := h.sup.AnswerTap(context.Background(), "fask-ask002", 1, "dev-iphone-1"); err != nil {
		t.Fatalf("AnswerTap(valid ask alongside bad sibling): %v", err)
	}
	if len(h.store.Questions()) != 0 {
		t.Fatalf("Questions after answering valid ask = %+v, want empty", h.store.Questions())
	}
}

func TestFactoryRedactedOptionLabelStillVerifiesAnswerProof(t *testing.T) {
	ask := sampleGateAsk("ask007")
	ask.Options = []string{"Rotate password: demoValue9", "Cancel"}
	ask.Recommend = ask.Options[0]

	var proofs *factory.ProofStore
	eng := newStubFactoryEngine(ask)

	h := newHarness(t, func(cfg *Config) {
		var err error
		proofs, err = factory.NewProofStore(t.TempDir(), cfg.Now)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Redactor = redact.New("demoValue9")
		cfg.FactoryProofs = proofs
		cfg.Factory = eng.client(t, proofs)
	})

	if err := h.sup.SyncFactoryAsks(context.Background()); err != nil {
		t.Fatalf("SyncFactoryAsks: %v", err)
	}
	p, ok := h.pending()
	if !ok || strings.Contains(p.Options[0].Label, "demoValue9") {
		t.Fatalf("card option label was not redacted: %+v", p)
	}

	h.clock.Advance(time.Second)
	if _, err := h.sup.AnswerTap(context.Background(), "fask-ask007", 1, "dev-iphone-1"); err != nil {
		t.Fatalf("AnswerTap on redacted option label: %v", err)
	}
	if len(eng.answers) != 1 || proofs.Count() != 1 {
		t.Fatalf("answers=%d proofs=%d, want 1 each", len(eng.answers), proofs.Count())
	}
	for _, ev := range h.log.ofType("pending.confirmed") {
		raw, _ := json.Marshal(ev.Payload)
		if strings.Contains(string(raw), "demoValue9") {
			t.Fatalf("pending.confirmed leaked unredacted value: %s", raw)
		}
	}
}

func TestFactoryAskRefusesLocalTapWindowKeysTypedInputAndNegativeSpeech(t *testing.T) {
	ask := sampleGateAsk("ask008")
	ask.Kind = factory.AskKindRule
	ask.Question = "Typed rule, this project (Beacon): designs wait for you. Save?"
	ask.Options = []string{"Save rule", "Cancel"}
	ask.Recommend = "Save rule"

	var proofs *factory.ProofStore
	eng := newStubFactoryEngine(ask)
	m := braintest.NewModel()
	h := newBrainHarness(t, m, func(cfg *Config) {
		var err error
		proofs, err = factory.NewProofStore(t.TempDir(), cfg.Now)
		if err != nil {
			t.Fatal(err)
		}
		cfg.FactoryProofs = proofs
		cfg.Factory = eng.client(t, proofs)
	})
	if err := h.sup.SyncFactoryAsks(context.Background()); err != nil {
		t.Fatalf("SyncFactoryAsks: %v", err)
	}
	// Mark announcement pushed so displacement rail knows the user heard it.
	p, _ := h.pending()
	h.sup.OnVoiceEvent(voice.EventPush{Text: p.SpokenSummary, Urgency: voice.UrgencyInterrupt, PendingID: p.ID})
	h.clock.Advance(time.Second)

	// 1. Ticketless local tap ("", "local") refused before addInputLocked.
	for _, dev := range []string{"", "local"} {
		_, err := h.sup.AnswerTap(context.Background(), "fask-ask008", 1, dev)
		if !errors.Is(err, factory.ErrProofRejected) {
			t.Fatalf("AnswerTap(dev=%q) err = %v, want ErrProofRejected", dev, err)
		}
	}

	// 2. WindowKeys on factory:beacon with local device refused.
	if _, err := h.sup.WindowKeys(context.Background(), "factory:beacon", []string{"1"}, "local"); !errors.Is(err, ErrWindowRefused) {
		t.Fatalf("WindowKeys(local) err = %v, want ErrWindowRefused", err)
	}

	// 3. Typed input refused on brainHost.AnswerPrompt.
	h.sup.ShepherdInput(InputTyped, "yes, save rule")
	if _, err := (brainHost{s: h.sup}).AnswerPrompt(context.Background(), "factory:beacon", []string{"1"}); !errors.Is(err, brain.ErrNotPermitted) {
		t.Fatalf("AnswerPrompt(InputTyped) err = %v, want brain.ErrNotPermitted", err)
	}

	// 4. Negative live voice speech ("no, don't save it") refused for approving key ["1"].
	h.hear("no, don't save it")
	if _, err := (brainHost{s: h.sup}).AnswerPrompt(context.Background(), "factory:beacon", []string{"1"}); !errors.Is(err, brain.ErrNotPermitted) {
		t.Fatalf("AnswerPrompt(negative speech) err = %v, want brain.ErrNotPermitted", err)
	}

	if proofs.Count() != 0 || len(eng.answers) != 0 {
		t.Fatalf("forgery/negative attempts minted %d proofs or sent %d answers", proofs.Count(), len(eng.answers))
	}

	// 5. Real live voice approval succeeds, mints proof with voice device, and settles card.
	h.sup.SetVoiceDevice("dev-iphone-voice")
	h.clock.Advance(time.Second)
	h.hear("yes, save rule")
	echo, err := (brainHost{s: h.sup}).AnswerPrompt(context.Background(), "factory:beacon", []string{"1"})
	if err != nil {
		t.Fatalf("AnswerPrompt(InputVoice): %v", err)
	}
	if !strings.Contains(echo, "Save rule") {
		t.Fatalf("AnswerPrompt echo = %q, want Save rule", echo)
	}
	if proofs.Count() != 1 || len(eng.answers) != 1 {
		t.Fatalf("expected 1 proof & 1 answer, got %d proofs, %d answers", proofs.Count(), len(eng.answers))
	}
	verified, _, _, err := proofs.Verify("ask008", eng.answers[0].Proof, "Save rule")
	if err != nil || verified.InputKind != "voice" || verified.DeviceID != "dev-iphone-voice" {
		t.Fatalf("Verify voice proof: %+v, err=%v", verified, err)
	}
}

func TestFactoryAnswerTransportFailureRollsBackProofAndInput(t *testing.T) {
	ask := sampleGateAsk("ask002")
	var proofs *factory.ProofStore
	eng := newStubFactoryEngine(ask)
	eng.failErr = errors.New("engine busy")

	h := newHarness(t, func(cfg *Config) {
		var err error
		proofs, err = factory.NewProofStore(t.TempDir(), cfg.Now)
		if err != nil {
			t.Fatal(err)
		}
		cfg.FactoryProofs = proofs
		cfg.Factory = eng.client(t, proofs)
	})
	if err := h.sup.SyncFactoryAsks(context.Background()); err != nil {
		t.Fatalf("SyncFactoryAsks: %v", err)
	}
	h.clock.Advance(time.Second)

	if _, err := h.sup.AnswerTap(context.Background(), "fask-ask002", 1, "dev-iphone-1"); !errors.Is(err, ErrNotSent) {
		t.Fatalf("AnswerTap(engine failure) err = %v, want ErrNotSent", err)
	}
	if proofs.Count() != 0 {
		t.Fatalf("failed answer left %d unconsumed proofs in store, want 0", proofs.Count())
	}
	if len(h.store.Questions()) != 1 {
		t.Fatal("failed answer dropped open question")
	}

	// Clearing the engine error allows immediate retry.
	eng.mu.Lock()
	eng.failErr = nil
	eng.mu.Unlock()
	if _, err := h.sup.AnswerTap(context.Background(), "fask-ask002", 1, "dev-iphone-1"); err != nil {
		t.Fatalf("retry AnswerTap: %v", err)
	}
	if proofs.Count() != 1 || len(h.store.Questions()) != 0 {
		t.Fatalf("after retry: proofs=%d questions=%d", proofs.Count(), len(h.store.Questions()))
	}
}

func TestFactorySlowUnlockPastActionTimeoutStillConfirmsRecordedAnswer(t *testing.T) {
	ask := sampleGateAsk("ask002")
	var proofs *factory.ProofStore
	eng := newStubFactoryEngine(ask)
	unlockDone := make(chan struct{})

	h := newHarness(t, func(cfg *Config) {
		var err error
		proofs, err = factory.NewProofStore(t.TempDir(), cfg.Now)
		if err != nil {
			t.Fatal(err)
		}
		baseClient := eng.client(t, proofs)
		cfg.FactoryProofs = proofs
		cfg.Factory = factory.NewClient(factory.ClientOptions{
			DataDir:       t.TempDir(),
			Getenv:        func(string) string { return "" },
			ActionTimeout: 40 * time.Millisecond,
			Exec: func(ctx context.Context, args ...string) ([]byte, string, int, error) {
				if reflect.DeepEqual(args, []string{"owner", "asks", "--open", "--json"}) {
					resp, err := baseClient.Asks(ctx, factory.AsksFilter{Open: true})
					if err != nil {
						return nil, err.Error(), 1, err
					}
					raw, _ := json.Marshal(resp)
					return raw, "", 0, nil
				}
				// Record the ask answered immediately (as owner answer does before
				// running a slow merge/tag unlock), then outlive ActionTimeout.
				eng.mu.Lock()
				a := eng.open["ask002"]
				ans := a.Options[0]
				by := "owner"
				at := a.At + 1000
				a.Status, a.Answer, a.AnsweredBy, a.AnsweredAt = "answered", &ans, &by, &at
				eng.open["ask002"] = a
				eng.mu.Unlock()

				go func() {
					time.Sleep(80 * time.Millisecond)
					close(unlockDone)
				}()
				<-ctx.Done()
				return nil, "", -1, ctx.Err()
			},
		})
	})

	if err := h.sup.SyncFactoryAsks(context.Background()); err != nil {
		t.Fatalf("SyncFactoryAsks: %v", err)
	}
	h.clock.Advance(time.Second)

	res, err := h.sup.AnswerTap(context.Background(), "fask-ask002", 1, "dev-iphone-1")
	if err != nil {
		t.Fatalf("AnswerTap(slow unlock) err = %v, want nil once ask is recorded answered", err)
	}
	if !strings.Contains(res.Spoken, "Beacon") {
		t.Fatalf("Spoken = %q, want Beacon confirmation", res.Spoken)
	}
	if len(h.store.Questions()) != 0 {
		t.Fatalf("Questions = %+v, want settled", h.store.Questions())
	}
	if proofs.Count() != 1 {
		t.Fatalf("proofs.Count() = %d, want proof retained for async unlock", proofs.Count())
	}
	<-unlockDone
}

func TestFactoryDecisionAskWithTaskSurvivesColdCacheLookup(t *testing.T) {
	folder := "/Users/me/projects/beacon"
	task := "BCN-10"
	ms := "M2"
	ask := factory.Ask{
		ID:        "ask001",
		Project:   "beacon",
		Name:      "Beacon",
		Folder:    &folder,
		Kind:      factory.AskKindDecision,
		Gated:     false,
		Question:  "Keep the offline cache in SQLite or plain JSON files?",
		Options:   []string{"SQLite", "Plain JSON"},
		Recommend: "SQLite",
		Task:      &task,
		Milestone: &ms,
		Subject:   nil,
		At:        1763300000000,
		Status:    "open",
	}

	var proofs *factory.ProofStore
	eng := newStubFactoryEngine(ask)
	m := braintest.NewModel()
	h := newBrainHarness(t, m, func(cfg *Config) {
		var err error
		proofs, err = factory.NewProofStore(t.TempDir(), cfg.Now)
		if err != nil {
			t.Fatal(err)
		}
		cfg.FactoryProofs = proofs
		cfg.Factory = eng.client(t, proofs)
	})

	if err := h.sup.SyncFactoryAsks(context.Background()); err != nil {
		t.Fatalf("SyncFactoryAsks: %v", err)
	}
	p, _ := h.pending()
	if p.Command != "BCN-10" {
		t.Fatalf("decision ask Command = %q, want BCN-10", p.Command)
	}
	h.sup.OnVoiceEvent(voice.EventPush{Text: p.SpokenSummary, Urgency: voice.UrgencyInterrupt, PendingID: p.ID})

	// Simulate daemon restart with cold in-memory factoryAsks map.
	h.sup.mu.Lock()
	clear(h.sup.factoryAsks)
	h.sup.mu.Unlock()

	h.clock.Advance(time.Second)
	h.hear("option one")
	echo, err := (brainHost{s: h.sup}).AnswerPrompt(context.Background(), "factory:beacon", []string{"1"})
	if err != nil {
		t.Fatalf("AnswerPrompt(decision ask with cold cache): %v", err)
	}
	if !strings.Contains(echo, "SQLite") {
		t.Fatalf("echo = %q, want SQLite", echo)
	}
}
