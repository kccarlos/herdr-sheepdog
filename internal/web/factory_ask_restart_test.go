package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/identity"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

type fakeAPNsRequest struct {
	path string
	body map[string]any
}

type fakeAPNsServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []fakeAPNsRequest
}

func newFakeAPNsServer(t *testing.T) *fakeAPNsServer {
	t.Helper()
	f := &fakeAPNsServer{}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		f.mu.Lock()
		f.reqs = append(f.reqs, fakeAPNsRequest{path: r.URL.Path, body: body})
		f.mu.Unlock()
		w.Header().Set("apns-id", "test-id")
		w.WriteHeader(http.StatusOK)
	}))
	f.srv.EnableHTTP2 = true
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPNsServer) requests() []fakeAPNsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeAPNsRequest(nil), f.reqs...)
}

func writeTestAPNsKey(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey_TEST.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type restartRun struct {
	*harness
	push *push.Sender
	stop func()
}

func startFactoryRestartRun(t *testing.T, dir, keyPath string, apns *fakeAPNsServer, eng supervisor.Factory) *restartRun {
	t.Helper()
	log, err := eventlog.Open(dir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(dir, "state.json"), state.Options{})
	if err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	sender, err := push.New(push.Config{
		Mode:          push.ModeAPNs,
		KeyPath:       keyPath,
		KeyID:         "KEYID12345",
		TeamID:        "TEAM123456",
		Topic:         "com.example.app",
		DataDir:       dir,
		ProductionURL: apns.srv.URL,
		Client:        apns.srv.Client(),
	}, log)
	if err != nil {
		_ = store.Close()
		_ = log.Close()
		t.Fatal(err)
	}
	h := inject.NewHerdr(nil, log)
	sup, err := supervisor.New(store, h, nil, inject.SkippedPusher{Log: log}, log, supervisor.Config{
		DataDir:  dir,
		Notifier: sender,
		Factory:  eng,
	})
	if err != nil {
		_ = store.Close()
		_ = log.Close()
		t.Fatal(err)
	}
	sup.SetVoiceConnected(true)

	daemonID, err := identity.DaemonID(dir)
	if err != nil {
		_ = store.Close()
		_ = log.Close()
		t.Fatal(err)
	}
	tlsID, err := LoadTLSIdentity(dir, nil)
	if err != nil {
		_ = store.Close()
		_ = log.Close()
		t.Fatal(err)
	}
	srv, err := New(Deps{
		Log:        log,
		Logger:     log,
		Store:      store,
		Supervisor: sup,
		Injector:   inject.NewInjector(store, h, sup, log, nil),
		Router:     &inject.Router{},
		Version:    "test",
		Computer:   identity.Computer{ID: daemonID, Name: "Test Mac"},
		Heartbeat:  200 * time.Millisecond,
		OutQueue:   8,
		Push:       sender,
		DataDir:    dir,
		TLS:        tlsID,
	})
	if err != nil {
		_ = store.Close()
		_ = log.Close()
		t.Fatal(err)
	}

	log.Log("daemon", "daemon.start", "", map[string]any{"version": "test"})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		sup.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		srv.Run(ctx)
	}()
	hs := httptest.NewServer(srv.Handler())

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			wg.Wait()
			sender.Wait()
			hs.Close()
			_ = store.Close()
			_ = log.Close()
		})
	}
	t.Cleanup(stop)

	return &restartRun{
		harness: &harness{t: t, log: log, store: store, herdr: h, sup: sup, srv: srv, http: hs, cancel: cancel},
		push:    sender,
		stop:    stop,
	}
}

func countPendingCreatedInLog(t *testing.T, log *eventlog.Log, pendingID string) int {
	t.Helper()
	if err := log.Flush(); err != nil {
		t.Fatal(err)
	}
	days, err := log.Days()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, day := range days {
		evs, err := log.ReadDay(day)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if ev.Type != "pending.created" {
				continue
			}
			var pl struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(ev.Payload, &pl) == nil && pl.ID == pendingID {
				count++
			}
		}
	}
	return count
}

func TestFactoryAskBridgeRestartDoesNotRelogOrRepush(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeTestAPNsKey(t)
	apns := newFakeAPNsServer(t)
	eng := newWebFakeFactory()

	const (
		askID  = "ask-demo-1"
		cardID = "fask-" + askID
		devTok = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa123456"
	)

	// Run 1: open a gated factory ask, verify the card is shown, pending.created
	// is logged once, and one phone push is sent.
	r1 := startFactoryRestartRun(t, dir, keyPath, apns, eng)
	if err := r1.push.Register(devTok, push.EnvProduction, "Owner iPhone"); err != nil {
		t.Fatal(err)
	}
	eng.openAsk(flow.Ask{
		ID:        askID,
		Project:   "demo",
		Kind:      flow.KindRelease,
		Gated:     true,
		Question:  "Cut release v1.2 now?",
		Options:   []string{"Yes, cut v1.2", "Hold"},
		Recommend: "Yes, cut v1.2",
		Task:      "T1",
		At:        time.Now().UTC(),
	})

	waitFor(t, time.Second, func() bool {
		var st StateResponse
		if r1.getJSON("/api/state", &st) != http.StatusOK {
			return false
		}
		_, ok := findQuestion(st, cardID)
		return ok && st.PendingAction != nil && st.PendingAction.ID == cardID
	}, "run 1 card "+cardID+" in /api/state")

	waitFor(t, time.Second, func() bool {
		return len(apns.requests()) == 1
	}, "run 1 APNs push for "+cardID)
	r1.push.Wait()

	if got := countPendingCreatedInLog(t, r1.log, cardID); got != 1 {
		t.Fatalf("run 1 pending.created count = %d, want 1", got)
	}
	r1.stop()

	// Run 2: restart the daemon on the same DataDir while ask-demo-1 is still open.
	r2 := startFactoryRestartRun(t, dir, keyPath, apns, eng)
	if got := r2.push.Status().Devices; got != 1 {
		t.Fatalf("run 2 reloaded push devices = %d, want 1", got)
	}

	var st2 StateResponse
	waitFor(t, time.Second, func() bool {
		if r2.getJSON("/api/state", &st2) != http.StatusOK {
			return false
		}
		_, ok := findQuestion(st2, cardID)
		return ok
	}, "run 2 card "+cardID+" still shown in /api/state after restart")

	if st2.PendingAction == nil || st2.PendingAction.ID != cardID {
		t.Fatalf("run 2 pending_action = %+v, want %s", st2.PendingAction, cardID)
	}

	r2.push.Wait()
	if got := countPendingCreatedInLog(t, r2.log, cardID); got != 1 {
		t.Errorf("total pending.created count after restart = %d, want 1 (restart re-logged pending.created)", got)
	}

	// GET /api/conversation must show only one approval item for the open ask.
	var convo ConversationResponse
	if code := r2.getJSON("/api/conversation", &convo); code != http.StatusOK {
		t.Fatalf("run 2 /api/conversation = %d", code)
	}
	approvals := 0
	for _, it := range convo.Items {
		if it.Kind == "approval" && it.PendingID == cardID {
			approvals++
			if it.Resolution != "open" {
				t.Errorf("run 2 conversation item resolution = %q, want open", it.Resolution)
			}
		}
	}
	if approvals != 1 {
		t.Errorf("run 2 conversation approval items for %s = %d, want 1", cardID, approvals)
	}

	if reqs := apns.requests(); len(reqs) != 1 {
		t.Errorf("APNs push requests after restart = %d, want 1 (restart re-sent phone push)", len(reqs))
	}

	// Answering the still-open ask after restart still resolves the card cleanly.
	dev := r2.pairDevice()
	req := r2.authedRequest(http.MethodPost, "/api/pending/answer", dev.ticket, nil, map[string]any{"id": cardID, "option": 1})
	var okBody map[string]any
	code, _ := r2.do(req, &okBody)
	if code != http.StatusOK || okBody["ok"] != true {
		t.Fatalf("post-restart paired tap = %d %v, want 200 ok", code, okBody)
	}
	if a := eng.firstAsk(); a.Status != flow.AskStatusAnswered || a.Answer != "Yes, cut v1.2" {
		t.Fatalf("ask after post-restart tap = %+v, want answered", a)
	}
	waitFor(t, time.Second, func() bool {
		var st StateResponse
		if r2.getJSON("/api/state", &st) != http.StatusOK {
			return false
		}
		_, still := findQuestion(st, cardID)
		return !still && (st.PendingAction == nil || st.PendingAction.ID != cardID)
	}, "answered ask "+cardID+" to disappear after post-restart tap")
}
