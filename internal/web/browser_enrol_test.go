package web

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/identity"
	"github.com/kccarlos/herdr-sheepdog/internal/pair"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

type browserEnrolFixture struct {
	t     *testing.T
	dir   string
	now   time.Time
	log   *eventlog.Log
	eng   *factory.Engine
	paths factory.Paths
	srv   *Server
}

func newBrowserEnrolFixture(t *testing.T, asks []flow.Ask) *browserEnrolFixture {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	elog, err := eventlog.Open(dir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = elog.Close() })

	st, err := state.Open(filepath.Join(dir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	paths := factory.Paths{DataDir: dir}
	if len(asks) > 0 {
		fs := flow.State{
			Seq:      10,
			Settings: flow.DefaultSettings(),
			Project:  flow.Project{ID: "proj-demo", Folder: filepath.Join(dir, "repo"), Goal: "demo goal", Started: true},
			Tasks:    []flow.Task{{ID: "T1", Title: "feat: demo task"}},
			Asks:     asks,
		}
		if err := factory.SaveState(paths.State("proj-demo"), fs); err != nil {
			t.Fatal(err)
		}
	}

	f := &browserEnrolFixture{t: t, dir: dir, now: now, log: elog, paths: paths}
	eng := factory.New(paths, nil)
	eng.Now = func() time.Time { return f.now }
	f.eng = eng

	srv, err := New(Deps{
		Computer: identity.Computer{ID: "AAAAAAAAAAAAAAAAAAAAAA", Name: "Test Mac"},
		Log:      elog,
		Store:    st,
		DataDir:  dir,
		Factory:  eng,
		Now:      func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.srv = srv
	return f
}

func (f *browserEnrolFixture) doLoopback(method, path string, body any, ticket string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	f.t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.RemoteAddr, r.Host = "127.0.0.1:5000", "127.0.0.1:7777"
	if ticket != "" {
		r.Header.Set("Authorization", "Bearer "+ticketPrefix+ticket)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, r)
	return w
}

func (f *browserEnrolFixture) doRemote(method, path string, body any, ticket string) *httptest.ResponseRecorder {
	f.t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.RemoteAddr, r.Host = "192.0.2.50:5000", "192.0.2.20:7777"
	r.TLS = &tls.ConnectionState{}
	if ticket != "" {
		r.Header.Set("Authorization", "Bearer "+ticketPrefix+ticket)
	}
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, r)
	return w
}

// enrollAndSignInApp enrolls a paired app device (e.g. phone or Mac menu-bar app)
// via the phone pairing store and returns its device ID and Bearer ticket.
func (f *browserEnrolFixture) enrollAndSignInApp(name, kind string) (string, string, *ecdsa.PrivateKey) {
	f.t.Helper()
	key, pub := newDeviceKey(f.t)
	code, _, _, err := f.srv.enroll.current(f.now, true)
	if err != nil {
		f.t.Fatal(err)
	}
	w := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": code, "public_key": pub, "name": name, "kind": kind,
	}, "")
	if w.Code != http.StatusOK {
		f.t.Fatalf("enroll app = %d %s", w.Code, w.Body.String())
	}
	var er EnrollResponse
	if err := json.Unmarshal(w.Body.Bytes(), &er); err != nil {
		f.t.Fatal(err)
	}
	ticket := f.signInApp(key, er.DeviceID)
	return er.DeviceID, ticket, key
}

func (f *browserEnrolFixture) signInApp(key *ecdsa.PrivateKey, deviceID string) string {
	f.t.Helper()
	wc := f.doLoopback(http.MethodGet, "/api/auth/challenge", nil, "")
	if wc.Code != http.StatusOK {
		f.t.Fatalf("challenge = %d %s", wc.Code, wc.Body.String())
	}
	var ch ChallengeResponse
	if err := json.Unmarshal(wc.Body.Bytes(), &ch); err != nil {
		f.t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(SignedText(f.srv.deps.Computer.ID, f.srv.deps.TLS.Pin(), deviceID, ch.Nonce, f.now.Unix())))
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	ws := f.doLoopback(http.MethodPost, "/api/auth/session", map[string]any{
		"device_id": deviceID, "nonce": ch.Nonce, "time": f.now.Unix(),
		"signature": base64.StdEncoding.EncodeToString(sig), "client": "app",
	}, "")
	if ws.Code != http.StatusOK {
		f.t.Fatalf("session = %d %s", ws.Code, ws.Body.String())
	}
	var sr SessionResponse
	if err := json.Unmarshal(ws.Body.Bytes(), &sr); err != nil || sr.Ticket == "" {
		f.t.Fatalf("session decode = %s (err=%v)", ws.Body.String(), err)
	}
	return sr.Ticket
}

func (f *browserEnrolFixture) signInBrowser(key *ecdsa.PrivateKey, deviceID string) *http.Cookie {
	f.t.Helper()
	wc := f.doLoopback(http.MethodGet, "/api/auth/challenge", nil, "")
	if wc.Code != http.StatusOK {
		f.t.Fatalf("browser challenge = %d %s", wc.Code, wc.Body.String())
	}
	var ch ChallengeResponse
	if err := json.Unmarshal(wc.Body.Bytes(), &ch); err != nil {
		f.t.Fatal(err)
	}
	// Browsers sign an empty pin line (auth.go SignedText).
	sum := sha256.Sum256([]byte(SignedText(f.srv.deps.Computer.ID, "", deviceID, ch.Nonce, f.now.Unix())))
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	ws := f.doLoopback(http.MethodPost, "/api/auth/session", map[string]any{
		"device_id": deviceID, "nonce": ch.Nonce, "time": f.now.Unix(),
		"signature": base64.StdEncoding.EncodeToString(sig), "client": "browser",
	}, "")
	if ws.Code != http.StatusOK {
		f.t.Fatalf("browser session = %d %s", ws.Code, ws.Body.String())
	}
	for _, c := range ws.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	f.t.Fatalf("browser session returned no %s cookie", sessionCookie)
	return nil
}

// Decision 1 (D17 / O20): the short browser-enrolment code is issued only over
// an authenticated device session, is short, single-use, expires after 5 minutes,
// and is never written to the event log.
func TestBrowserEnrollCodeAuthAndLifecycle(t *testing.T) {
	f := newBrowserEnrolFixture(t, nil)

	// Ticketless loopback caller cannot mint a browser enrolment code.
	wLocal := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, "")
	if wLocal.Code != http.StatusForbidden {
		t.Fatalf("ticketless loopback POST /api/pair/browser-code = %d %s, want 403", wLocal.Code, wLocal.Body.String())
	}

	// Unauthenticated remote caller gets 401.
	wRemote := f.doRemote(http.MethodPost, "/api/pair/browser-code", nil, "")
	if wRemote.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated remote POST /api/pair/browser-code = %d %s, want 401", wRemote.Code, wRemote.Body.String())
	}

	// Authenticated paired device (e.g. Mac menu-bar app) gets a short 5-minute code.
	_, appTicket, _ := f.enrollAndSignInApp("Test Menu Bar", DeviceIOS)
	wCode := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, appTicket)
	if wCode.Code != http.StatusOK {
		t.Fatalf("authenticated POST /api/pair/browser-code = %d %s, want 200", wCode.Code, wCode.Body.String())
	}
	var issued struct {
		Code      string    `json:"code"`
		Expires   time.Time `json:"expires"`
		ExpiresIn int       `json:"expires_in"`
	}
	if err := json.Unmarshal(wCode.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode browser-code: %v (%s)", err, wCode.Body.String())
	}
	if issued.Code == "" || len(issued.Code) > 12 {
		t.Fatalf("code = %q (len %d), want non-empty short code (<= 12 chars)", issued.Code, len(issued.Code))
	}
	if issued.ExpiresIn != 300 || !issued.Expires.Equal(f.now.Add(5*time.Minute)) {
		t.Fatalf("expiry = %v (expires_in=%d), want +5m (300s)", issued.Expires, issued.ExpiresIn)
	}
	for _, ev := range f.log.Tail(10000) {
		if strings.Contains(string(ev.Payload), issued.Code) {
			t.Fatalf("event %s leaked browser enroll code", ev.Type)
		}
	}

	// Enrolling a browser with the short code succeeds and allows cookie sign-in.
	browserKey, browserPub := newDeviceKey(t)
	wEnroll := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": issued.Code, "public_key": browserPub, "name": "Test Browser", "kind": DeviceBrowser,
	}, "")
	if wEnroll.Code != http.StatusOK {
		t.Fatalf("browser enroll with short code = %d %s, want 200", wEnroll.Code, wEnroll.Body.String())
	}
	var er EnrollResponse
	if err := json.Unmarshal(wEnroll.Body.Bytes(), &er); err != nil || er.DeviceID == "" {
		t.Fatalf("enroll response = %s (err=%v)", wEnroll.Body.String(), err)
	}
	cookie := f.signInBrowser(browserKey, er.DeviceID)
	if cookie == nil || cookie.Value == "" {
		t.Fatal("expected browser session cookie")
	}

	// Single-use: a second enroll attempt with the same short code returns 410 code_used.
	_, pub2 := newDeviceKey(t)
	wReuse := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": issued.Code, "public_key": pub2, "name": "Test Browser 2", "kind": DeviceBrowser,
	}, "")
	var reuseBody map[string]string
	_ = json.Unmarshal(wReuse.Body.Bytes(), &reuseBody)
	if wReuse.Code != http.StatusGone || reuseBody["reason"] != "code_used" {
		t.Fatalf("reused short code = %d %v, want 410 code_used", wReuse.Code, reuseBody)
	}

	// Expiry: a fresh short code expires after 5 minutes.
	wCode2 := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, appTicket)
	if wCode2.Code != http.StatusOK {
		t.Fatalf("second browser-code = %d %s", wCode2.Code, wCode2.Body.String())
	}
	var issued2 struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(wCode2.Body.Bytes(), &issued2)
	f.now = f.now.Add(5*time.Minute + time.Second)
	wExp := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": issued2.Code, "public_key": pub2, "name": "Test Browser 2", "kind": DeviceBrowser,
	}, "")
	var expBody map[string]string
	_ = json.Unmarshal(wExp.Body.Bytes(), &expBody)
	if wExp.Code != http.StatusGone || expBody["reason"] != "code_expired" {
		t.Fatalf("expired short code = %d %v, want 410 code_expired", wExp.Code, expBody)
	}
}

// Decision 1 (O20 compatibility): phone QR pairing (GET /api/pair and iOS enroll)
// keeps working unchanged alongside the new browser short-code endpoint.
func TestPhoneQRPairingUnchangedByBrowserCode(t *testing.T) {
	old := pairSysAddrs
	pairSysAddrs = func() ([]pair.IfaceAddr, error) {
		return []pair.IfaceAddr{{Iface: "en0", IP: net.ParseIP("192.168.1.20")}}, nil
	}
	t.Cleanup(func() { pairSysAddrs = old })

	f := newBrowserEnrolFixture(t, nil)
	if err := f.srv.Listen("0.0.0.0:0"); err != nil {
		t.Fatal(err)
	}
	_, appTicket, _ := f.enrollAndSignInApp("Test Menu Bar", DeviceIOS)

	// Loopback GET /api/pair issues a phone QR link.
	wPair := f.doLoopback(http.MethodGet, "/api/pair?new=1", nil, "")
	if wPair.Code != http.StatusOK {
		t.Fatalf("GET /api/pair = %d %s", wPair.Code, wPair.Body.String())
	}
	var qr PairResponse
	if err := json.Unmarshal(wPair.Body.Bytes(), &qr); err != nil || qr.Link == "" {
		t.Fatalf("GET /api/pair body = %s (err=%v)", wPair.Body.String(), err)
	}
	parsed, err := pair.ParseLink(qr.Link)
	if err != nil || parsed.Code == "" {
		t.Fatalf("ParseLink(%q) = %+v, %v", qr.Link, parsed, err)
	}

	// Issuing a browser short code does not disturb the active phone QR code.
	wBrowserCode := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, appTicket)
	if wBrowserCode.Code != http.StatusOK {
		t.Fatalf("POST /api/pair/browser-code = %d %s, want 200", wBrowserCode.Code, wBrowserCode.Body.String())
	}

	wPairAgain := f.doLoopback(http.MethodGet, "/api/pair", nil, "")
	var qrAgain PairResponse
	_ = json.Unmarshal(wPairAgain.Body.Bytes(), &qrAgain)
	if qrAgain.Link != qr.Link {
		t.Fatalf("issuing browser code changed phone QR link: got %q, want %q", qrAgain.Link, qr.Link)
	}

	// A phone can still enrol using the QR link's code.
	_, phonePub := newDeviceKey(t)
	wPhone := f.doRemote(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": parsed.Code, "public_key": phonePub, "name": "Test Phone", "kind": DeviceIOS,
	}, "")
	if wPhone.Code != http.StatusOK {
		t.Fatalf("phone enroll with QR code = %d %s, want 200", wPhone.Code, wPhone.Body.String())
	}
}

// Decision 2: POST /api/devices/enroll on loopback is rate-limited: the 6th wrong
// guess inside the window gets 429 rate_limited.
func TestEnrollLoopbackRateLimitedOnSixthGuess(t *testing.T) {
	f := newBrowserEnrolFixture(t, nil)
	_, pub := newDeviceKey(t)

	for i := 1; i <= 5; i++ {
		w := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
			"code": "WRONG-CODE", "public_key": pub, "name": "Test Browser", "kind": DeviceBrowser,
		}, "")
		if w.Code != http.StatusGone {
			t.Fatalf("loopback wrong guess %d = %d %s, want 410", i, w.Code, w.Body.String())
		}
	}

	// 6th wrong guess inside the window must return 429 rate_limited.
	w6 := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": "WRONG-CODE", "public_key": pub, "name": "Test Browser", "kind": DeviceBrowser,
	}, "")
	var body6 map[string]string
	_ = json.Unmarshal(w6.Body.Bytes(), &body6)
	if w6.Code != http.StatusTooManyRequests || body6["reason"] != "rate_limited" {
		t.Fatalf("6th loopback wrong guess = %d %v, want 429 rate_limited", w6.Code, body6)
	}

	// Even a valid code from loopback is blocked while rate-limited.
	validCode, _, _, err := f.srv.enroll.current(f.now, true)
	if err != nil {
		t.Fatal(err)
	}
	wBlocked := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": validCode, "public_key": pub, "name": "Test Browser", "kind": DeviceBrowser,
	}, "")
	if wBlocked.Code != http.StatusTooManyRequests {
		t.Fatalf("valid code while loopback rate-limited = %d %s, want 429", wBlocked.Code, wBlocked.Body.String())
	}
}

// Decision 3 (KB-ASK-19, D15): GET /api/factory/asks lists open asks;
// POST /api/factory/asks/{id}/answer requires a paired device session (ticketless
// loopback gets 403 proof_required) and logs the answering device_id.
func TestFactoryAsksListAndAnswerProof(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	f := newBrowserEnrolFixture(t, []flow.Ask{
		{
			ID:        "ask-open-1",
			Project:   "proj-demo",
			Kind:      flow.KindGate,
			Gated:     true,
			Question:  "Approve the plan for T1?",
			Options:   []string{"Approve", "Request changes"},
			Recommend: "Approve",
			Task:      "T1",
			At:        now,
			Status:    flow.AskStatusOpen,
		},
		{
			ID:        "ask-open-2",
			Project:   "proj-demo",
			Kind:      flow.KindRelease,
			Gated:     true,
			Question:  "Tag and publish v0.2.0?",
			Options:   []string{"Publish v0.2.0", "Hold"},
			Recommend: "Publish v0.2.0",
			At:        now.Add(time.Second),
			Status:    flow.AskStatusOpen,
		},
		{
			ID:        "ask-closed-1",
			Project:   "proj-demo",
			Kind:      flow.KindGate,
			Gated:     true,
			Question:  "Already answered ask",
			Options:   []string{"Yes", "No"},
			Recommend: "Yes",
			At:        now.Add(-time.Minute),
			Status:    flow.AskStatusAnswered,
			Answer:    "Yes",
		},
	})

	// Unauthenticated remote GET /api/factory/asks gets 401.
	if w := f.doRemote(http.MethodGet, "/api/factory/asks", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("remote GET /api/factory/asks = %d %s, want 401", w.Code, w.Body.String())
	}

	// Loopback GET /api/factory/asks lists only the 2 open asks.
	wList := f.doLoopback(http.MethodGet, "/api/factory/asks", nil, "")
	if wList.Code != http.StatusOK {
		t.Fatalf("GET /api/factory/asks = %d %s, want 200", wList.Code, wList.Body.String())
	}
	var listResp struct {
		Protocol int        `json:"protocol"`
		Asks     []flow.Ask `json:"asks"`
	}
	if err := json.Unmarshal(wList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode asks: %v (%s)", err, wList.Body.String())
	}
	if listResp.Protocol != 1 || len(listResp.Asks) != 2 || listResp.Asks[0].ID != "ask-open-1" || listResp.Asks[1].ID != "ask-open-2" {
		t.Fatalf("unexpected asks response: %+v", listResp)
	}

	// KB-ASK-19: ticketless loopback POST /api/factory/asks/{id}/answer gets 403 proof_required.
	wNoTicket := f.doLoopback(http.MethodPost, "/api/factory/asks/ask-open-1/answer", map[string]any{"option": "Approve"}, "")
	if wNoTicket.Code != http.StatusForbidden || !strings.Contains(wNoTicket.Body.String(), "proof_required") {
		t.Fatalf("ticketless loopback answer = %d %s, want 403 proof_required", wNoTicket.Code, wNoTicket.Body.String())
	}
	// Ask must remain open after refused ticketless answer.
	if open := f.eng.Asks(true); len(open) != 2 {
		t.Fatalf("open asks after refused answer = %d, want 2", len(open))
	}

	// Enrolled browser (cookie session) can answer ask-open-1; device_id is logged.
	browserKey, browserPub := newDeviceKey(t)
	phoneCode, _, _, err := f.srv.enroll.current(f.now, true)
	if err != nil {
		t.Fatal(err)
	}
	wBrowserEnroll := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": phoneCode, "public_key": browserPub, "name": "Test Browser", "kind": DeviceBrowser,
	}, "")
	if wBrowserEnroll.Code != http.StatusOK {
		t.Fatalf("browser enroll = %d %s", wBrowserEnroll.Code, wBrowserEnroll.Body.String())
	}
	var browserER EnrollResponse
	_ = json.Unmarshal(wBrowserEnroll.Body.Bytes(), &browserER)
	browserCookie := f.signInBrowser(browserKey, browserER.DeviceID)

	wAns := f.doLoopback(http.MethodPost, "/api/factory/asks/ask-open-1/answer", map[string]any{"option": "Approve"}, "", browserCookie)
	if wAns.Code != http.StatusOK {
		t.Fatalf("paired browser answer = %d %s, want 200", wAns.Code, wAns.Body.String())
	}

	// Verify ask record and factory log both carry the browser's device_id.
	var answered *flow.Ask
	for _, a := range f.eng.Asks(false) {
		if a.ID == "ask-open-1" {
			cp := a
			answered = &cp
		}
	}
	if answered == nil || answered.Status != flow.AskStatusAnswered || answered.DeviceID != browserER.DeviceID || answered.ProofID == "" {
		t.Fatalf("answered ask = %+v, want answered with device_id=%q and proof_id", answered, browserER.DeviceID)
	}
	logBytes, err := os.ReadFile(f.paths.Log("proj-demo"))
	if err != nil {
		t.Fatalf("read factory log: %v", err)
	}
	if !strings.Contains(string(logBytes), browserER.DeviceID) {
		t.Fatalf("factory log missing device_id %q: %s", browserER.DeviceID, string(logBytes))
	}

	// Answering an already-answered ask returns 409 already_closed.
	wAgain := f.doLoopback(http.MethodPost, "/api/factory/asks/ask-open-1/answer", map[string]any{"option": "Approve"}, "", browserCookie)
	if wAgain.Code != http.StatusConflict {
		t.Fatalf("second answer on closed ask = %d %s, want 409", wAgain.Code, wAgain.Body.String())
	}

	// A paired phone (Bearer ticket) can also answer ask-open-2 and logs its device_id.
	phoneDevID, phoneTicket, _ := f.enrollAndSignInApp("Test Phone", DeviceIOS)
	wPhoneAns := f.doRemote(http.MethodPost, "/api/factory/asks/ask-open-2/answer", map[string]any{"option": "Publish v0.2.0"}, phoneTicket)
	if wPhoneAns.Code != http.StatusOK {
		t.Fatalf("paired phone answer = %d %s, want 200", wPhoneAns.Code, wPhoneAns.Body.String())
	}
	logBytes, err = os.ReadFile(f.paths.Log("proj-demo"))
	if err != nil {
		t.Fatalf("read factory log after phone answer: %v", err)
	}
	if !strings.Contains(string(logBytes), phoneDevID) {
		t.Fatalf("factory log missing phone device_id %q: %s", phoneDevID, string(logBytes))
	}
}
