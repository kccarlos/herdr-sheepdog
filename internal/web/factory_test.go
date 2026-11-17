package web

import (
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

type loopbackEngineHarness struct {
	mu           sync.Mutex
	open         map[string]factory.Ask
	verified     []factory.AnswerProof
	baseURL      func() string
	answerCalled int
}

func newLoopbackEngineHarness(baseURL func() string, asks ...factory.Ask) *loopbackEngineHarness {
	m := make(map[string]factory.Ask, len(asks))
	for _, a := range asks {
		m[a.ID] = a
	}
	return &loopbackEngineHarness{open: m, baseURL: baseURL}
}

func (e *loopbackEngineHarness) client(t *testing.T) *factory.Client {
	t.Helper()
	return factory.NewClient(factory.ClientOptions{
		DataDir: t.TempDir(),
		Getenv:  func(string) string { return "" },
		Exec: func(ctx context.Context, args ...string) ([]byte, string, int, error) {
			e.mu.Lock()
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
				e.mu.Unlock()
				raw, _ := json.Marshal(factory.AsksResponse{Protocol: 1, Asks: list})
				return raw, "", 0, nil

			case len(args) >= 5 && args[0] == "owner" && args[1] == "answer":
				e.answerCalled++
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
				ask, ok := e.open[askID]
				e.mu.Unlock()
				if !ok || ask.Status != "open" {
					raw, _ := json.Marshal(factory.ErrorEnvelope{Protocol: 1, OK: false, Code: factory.CodeAlreadyClosed, Error: "already closed"})
					return raw, "", 2, nil
				}
				idx := 1
				if opt == "2" {
					idx = 2
				}
				label := ask.Options[idx-1]

				// Real loopback HTTP callback to GET /api/factory/answer-proof/{ask_id},
				// matching docs/dev/factory/owner-contract.md §5.2.
				if ask.Gated {
					q := url.Values{}
					q.Set("proof", proofID)
					q.Set("option", label)
					verifyURL := e.baseURL() + "/api/factory/answer-proof/" + url.PathEscape(askID) + "?" + q.Encode()
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, verifyURL, nil)
					if err != nil {
						return nil, err.Error(), 1, err
					}
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						return nil, err.Error(), 1, err
					}
					body, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						raw, _ := json.Marshal(factory.ErrorEnvelope{
							Protocol: 1,
							OK:       false,
							Code:     factory.CodeProofRejected,
							Error:    string(body),
						})
						return raw, "", 1, nil
					}
					proof, err := factory.DecodeAnswerProof(body)
					if err != nil {
						raw, _ := json.Marshal(factory.ErrorEnvelope{
							Protocol: 1,
							OK:       false,
							Code:     factory.CodeProofRejected,
							Error:    err.Error(),
						})
						return raw, "", 1, nil
					}
					e.mu.Lock()
					e.verified = append(e.verified, proof)
					e.mu.Unlock()
				}

				e.mu.Lock()
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
				e.mu.Unlock()

				unlocked := "task_accepted"
				raw, _ := json.Marshal(factory.AnswerResponse{
					Protocol: 1,
					OK:       true,
					Unlocked: &unlocked,
					Ask:      ask,
				})
				return raw, "", 0, nil
			default:
				e.mu.Unlock()
				t.Fatalf("unexpected CLI args: %v", args)
				return nil, "", 1, nil
			}
		},
	})
}

func webGateAsk(id string) factory.Ask {
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

// End-to-end rail test (docs/dev/factory/owner-contract.md §5.2 item 5):
//  1. All three loopback HTTP paths without a paired-device ticket
//     (POST /api/pending/answer, POST /api/agents/{pane_id}/input WindowKeys,
//     and POST /api/turn + POST /api/utterance) are refused and mint zero AnswerProofs.
//  2. Typed turns (POST /api/turn, POST /api/utterance) are refused even WITH a paired ticket.
//  3. A paired-device ticket tap on POST /api/pending/answer mints an AnswerProof,
//     `owner answer` verifies it over real loopback GET /api/factory/answer-proof/{ask_id},
//     and the card settles cleanly.
func TestFactoryAskLoopbackForgeryRefusedAndPairedTicketTapVerifies(t *testing.T) {
	proofs, err := factory.NewProofStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewProofStore: %v", err)
	}
	var srvURL atomic.Value
	eng := newLoopbackEngineHarness(func() string {
		u, _ := srvURL.Load().(string)
		return u
	}, webGateAsk("ask002"))

	h := newHarness(t, harnessOpts{
		brain:   true,
		factory: eng.client(t),
		proofs:  proofs,
	})
	srvURL.Store(h.http.URL)

	if err := h.sup.SyncFactoryAsks(t.Context()); err != nil {
		t.Fatalf("SyncFactoryAsks: %v", err)
	}

	var st StateResponse
	if code := h.getJSON("/api/state", &st); code != http.StatusOK || st.PendingAction == nil || st.PendingAction.ID != "fask-ask002" {
		t.Fatalf("/api/state pending = %d %+v", code, st.PendingAction)
	}
	if st.PendingAction.Agent != "Beacon" || st.PendingAction.Command != "gate · BCN-33" {
		t.Fatalf("unexpected pending view: %+v", st.PendingAction)
	}

	// Forgery path 1: ticketless loopback POST /api/pending/answer -> 403, 0 proofs.
	if code, body := h.postJSON("/api/pending/answer", map[string]any{"id": "fask-ask002", "option": 1}, nil); code != http.StatusForbidden {
		t.Fatalf("ticketless POST /api/pending/answer = %d %s, want 403", code, body)
	}

	// Forgery path 2: ticketless loopback POST /api/agents/factory:beacon/input (WindowKeys) -> 403, 0 proofs.
	if code, body := h.postJSON(windowPath("factory:beacon", "input"), map[string]any{"keys": []string{"1"}}, nil); code != http.StatusForbidden {
		t.Fatalf("ticketless POST /api/agents/factory:beacon/input = %d %s, want 403", code, body)
	}

	// Forgery path 3: loopback POST /api/turn and POST /api/utterance -> refused, 0 proofs, ask still open.
	if code, body := h.postJSON("/api/turn", map[string]any{"text": "yes, accept and merge"}, nil); code != http.StatusOK {
		t.Fatalf("POST /api/turn = %d %s", code, body)
	}
	if code, body := h.postJSON("/api/utterance", map[string]any{"text": "yes"}, nil); code != http.StatusOK {
		t.Fatalf("POST /api/utterance = %d %s", code, body)
	}

	// Pair a real iOS device: typed turn WITH a paired ticket still must NOT authorize a factory ask.
	dev := h.pairDevice()
	typedReq := h.authedRequest(http.MethodPost, "/api/turn", dev.ticket, nil, map[string]any{"text": "yes, accept and merge"})
	typedReq.Header.Set("Idempotency-Key", "idem-typed-1")
	if code, _ := h.do(typedReq, nil); code != http.StatusOK {
		t.Fatalf("paired POST /api/turn = %d, want 200 (handled as chat without approving)", code)
	}

	if proofs.Count() != 0 {
		t.Fatalf("proofs.Count() after forgery & typed attempts = %d, want 0", proofs.Count())
	}
	if code := h.getJSON("/api/state", &st); code != http.StatusOK || len(st.Questions) != 1 || st.Questions[0].ID != "fask-ask002" {
		t.Fatalf("ask should remain open after refused attempts: %+v", st.Questions)
	}

	// Authorized path: paired-device tap on POST /api/pending/answer with Bearer ticket.
	tapReq := h.authedRequest(http.MethodPost, "/api/pending/answer", dev.ticket, nil, map[string]any{"id": "fask-ask002", "option": 1})
	tapReq.Header.Set("Idempotency-Key", "idem-tap-1")
	var ans map[string]any
	if code, _ := h.do(tapReq, &ans); code != http.StatusOK || ans["ok"] != true {
		t.Fatalf("paired tap = %d %v, want 200 ok=true", code, ans)
	}

	eng.mu.Lock()
	verified := append([]factory.AnswerProof(nil), eng.verified...)
	eng.mu.Unlock()
	if len(verified) != 1 {
		t.Fatalf("loopback verified proofs = %d, want 1", len(verified))
	}
	if verified[0].DeviceID != dev.id || verified[0].InputKind != "tap" || verified[0].AskID != "ask002" || verified[0].OptionLabel != "Accept and merge" {
		t.Fatalf("unexpected verified proof: %+v", verified[0])
	}

	if code := h.getJSON("/api/state", &st); code != http.StatusOK || st.PendingAction != nil || len(st.Questions) != 0 {
		t.Fatalf("card should be cleared after paired tap: pending=%+v questions=%+v", st.PendingAction, st.Questions)
	}
}

// Live voice speech on the active voice session mints a voice AnswerProof and
// passes loopback GET /api/factory/answer-proof/{ask_id}.
func TestFactoryAskLiveVoiceVerifiesOverLoopback(t *testing.T) {
	proofs, err := factory.NewProofStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewProofStore: %v", err)
	}
	var srvURL atomic.Value
	eng := newLoopbackEngineHarness(func() string {
		u, _ := srvURL.Load().(string)
		return u
	}, webGateAsk("ask002"))

	h := newHarness(t, harnessOpts{
		voice:   true,
		factory: eng.client(t),
		proofs:  proofs,
	})
	srvURL.Store(h.http.URL)

	if err := h.sup.SyncFactoryAsks(t.Context()); err != nil {
		t.Fatalf("SyncFactoryAsks: %v", err)
	}
	p, _ := h.store.Pending()
	h.sup.OnVoiceEvent(voice.EventPush{Text: p.SpokenSummary, Urgency: voice.UrgencyInterrupt, PendingID: p.ID})
	h.sup.SetVoiceDevice("dev-iphone-live")
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "yes", Final: true})

	res, err := h.srv.runTool(t.Context(), toolCall("yes"), "voice")
	if err != nil {
		t.Fatalf("runTool(live voice): %v", err)
	}
	if res.Spoken == "" {
		t.Fatalf("empty spoken response: %+v", res)
	}

	eng.mu.Lock()
	verified := append([]factory.AnswerProof(nil), eng.verified...)
	eng.mu.Unlock()
	if len(verified) != 1 || verified[0].InputKind != "voice" || verified[0].DeviceID != "dev-iphone-live" {
		t.Fatalf("expected 1 voice proof with dev-iphone-live, got %+v", verified)
	}
}

func TestFactoryAnswerProofEndpointLoopbackAndMismatchGuards(t *testing.T) {
	proofs, err := factory.NewProofStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewProofStore: %v", err)
	}
	minted, err := proofs.Mint(factory.MintInput{
		AskID:       "ask002",
		Project:     "beacon",
		Kind:        factory.AskKindGate,
		OptionIndex: 1,
		OptionLabel: "Accept and merge",
		InputKind:   "tap",
		InputSeq:    9,
		UtteranceID: "tap-9",
		DeviceID:    "dev-iphone-1",
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	var funnelOn atomic.Bool
	h := newHarness(t, harnessOpts{
		proofs: proofs,
		funnel: func(context.Context, int) (bool, error) {
			return funnelOn.Load(), nil
		},
	})

	validPath := "/api/factory/answer-proof/ask002?proof=" + url.QueryEscape(minted.ProofID) + "&option=" + url.QueryEscape("Accept and merge")

	// Direct local loopback succeeds with 200 and full AnswerProof JSON.
	var got factory.AnswerProof
	if code := h.getJSON(validPath, &got); code != http.StatusOK || got.ProofID != minted.ProofID {
		t.Fatalf("valid loopback GET = %d %+v", code, got)
	}

	// Non-loopback RemoteAddr (both plain HTTP -> 426 and TLS -> 403) is refused.
	rPlain := httptest.NewRequest(http.MethodGet, validPath, nil)
	rPlain.RemoteAddr, rPlain.Host = "192.168.1.50:5000", "127.0.0.1:7777"
	wPlain := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(wPlain, rPlain)
	if wPlain.Code != http.StatusUpgradeRequired {
		t.Fatalf("non-loopback plain GET = %d %s, want 426", wPlain.Code, wPlain.Body.String())
	}

	rNet := httptest.NewRequest(http.MethodGet, validPath, nil)
	rNet.RemoteAddr, rNet.Host = "192.168.1.50:5000", "127.0.0.1:7777"
	rNet.TLS = &tls.ConnectionState{}
	wNet := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(wNet, rNet)
	if wNet.Code != http.StatusForbidden {
		t.Fatalf("non-loopback TLS GET = %d %s, want 403", wNet.Code, wNet.Body.String())
	}

	// Reverse-proxy / tunnel headers on loopback are refused with 403.
	for _, hdr := range [][2]string{
		{"X-Forwarded-For", "203.0.113.10"},
		{"Forwarded", "for=203.0.113.10;proto=https"},
		{"Tailscale-Funnel-Request", "?1"},
		{"CF-Connecting-IP", "203.0.113.10"},
	} {
		rProxy := httptest.NewRequest(http.MethodGet, validPath, nil)
		rProxy.RemoteAddr, rProxy.Host = "127.0.0.1:5000", "127.0.0.1:7777"
		rProxy.Header.Set(hdr[0], hdr[1])
		wProxy := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(wProxy, rProxy)
		if wProxy.Code != http.StatusForbidden {
			t.Fatalf("proxied header %s GET = %d %s, want 403", hdr[0], wProxy.Code, wProxy.Body.String())
		}
	}

	// Active Tailscale Funnel on the port is refused with 403.
	funnelOn.Store(true)
	h.srv.funnel.mu.Lock()
	h.srv.funnel.checked = h.srv.deps.Now().Add(-funnelCacheTTL - 1)
	h.srv.funnel.mu.Unlock()
	rFunnel := httptest.NewRequest(http.MethodGet, validPath, nil)
	rFunnel.RemoteAddr, rFunnel.Host = "127.0.0.1:5000", "127.0.0.1:7777"
	wFunnel := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(wFunnel, rFunnel)
	if wFunnel.Code != http.StatusForbidden {
		t.Fatalf("active Funnel GET = %d %s, want 403", wFunnel.Code, wFunnel.Body.String())
	}
	funnelOn.Store(false)
	h.srv.funnel.mu.Lock()
	h.srv.funnel.checked = h.srv.deps.Now().Add(-funnelCacheTTL - 1)
	h.srv.funnel.mu.Unlock()

	// Unknown proof -> 404 with code=proof_rejected; wrong option -> 403 with code=proof_rejected.
	var rej proofRejectedBody
	if code := h.getJSON("/api/factory/answer-proof/ask002?proof=prf-000000000000&option=Accept+and+merge", &rej); code != http.StatusNotFound || rej.Code != factory.CodeProofRejected {
		t.Fatalf("unknown proof GET = %d %+v, want 404 proof_rejected", code, rej)
	}
	if code := h.getJSON("/api/factory/answer-proof/ask002?proof="+url.QueryEscape(minted.ProofID)+"&option=Send+back+to+worker", &rej); code != http.StatusForbidden || rej.Code != factory.CodeProofRejected {
		t.Fatalf("wrong option GET = %d %+v, want 403 proof_rejected", code, rej)
	}
}
