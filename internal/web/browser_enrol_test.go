package web

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
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
func (f *browserEnrolFixture) enrollAndSignInApp(name, kind string) (string, string) {
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
	return er.DeviceID, ticket
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
	_, appTicket := f.enrollAndSignInApp("Test Menu Bar", DeviceIOS)
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
	_, appTicket := f.enrollAndSignInApp("Test Menu Bar", DeviceIOS)

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
	phoneDevID, phoneTicket := f.enrollAndSignInApp("Test Phone", DeviceIOS)
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

// Round 2, Major 1: a browser short code can be tested only by an unproxied
// loopback request. Any loopback request carrying X-Forwarded-For (rotated
// every guess) or any other proxy or Funnel signal, or any non-loopback
// request, is refused before checking the short code and does not burn it.
// Every unproxied loopback guess shares one rate-limit bucket (6th wrong guess
// is 429 however headers vary), and phone QR pairing keeps working.
func TestBrowserShortCodeUnproxiedLoopbackOnly(t *testing.T) {
	t.Run("proxied_and_remote_refused_without_burning", func(t *testing.T) {
		f := newBrowserEnrolFixture(t, nil)
		_, appTicket := f.enrollAndSignInApp("Test Menu Bar", DeviceIOS)

		issueShort := func() string {
			t.Helper()
			w := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, appTicket)
			if w.Code != http.StatusOK {
				t.Fatalf("POST /api/pair/browser-code = %d %s, want 200", w.Code, w.Body.String())
			}
			var out struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Code == "" {
				t.Fatalf("decode browser-code = %v (%s)", err, w.Body.String())
			}
			return out.Code
		}

		// Issue one short code and replace it so retiredCode is expired and activeCode is live.
		retiredCode := issueShort()
		f.now = f.now.Add(time.Second)
		activeCode := issueShort()

		_, pub := newDeviceKey(t)
		postEnroll := func(remoteAddr, host string, tlsState *tls.ConnectionState, hdrs map[string]string, code string) *httptest.ResponseRecorder {
			t.Helper()
			b, err := json.Marshal(map[string]any{
				"code": code, "public_key": pub, "name": "Test Browser", "kind": DeviceBrowser,
			})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "/api/devices/enroll", bytes.NewReader(b))
			r.Header.Set("Content-Type", "application/json")
			r.RemoteAddr, r.Host, r.TLS = remoteAddr, host, tlsState
			for k, v := range hdrs {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			f.srv.Handler().ServeHTTP(w, r)
			return w
		}

		assertRefusedBeforeCheck := func(desc string, w *httptest.ResponseRecorder) {
			t.Helper()
			body := w.Body.String()
			if w.Code != http.StatusForbidden && w.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d %s, want 403 or 400 (refused before checking short code)", desc, w.Code, body)
			}
			if strings.Contains(body, "code_used") || strings.Contains(body, "code_expired") {
				t.Fatalf("%s: response leaked short-code check result: %s", desc, body)
			}
		}

		// 1. Loopback requests rotating X-Forwarded-For on every guess (wrong, retired, and live codes).
		guesses := []string{
			"2345-6789", "3456-789A", "4567-89AB", "5678-9ABC",
			"6789-ABCD", "789A-BCDE", "89AB-CDEF", retiredCode,
			activeCode, strings.ToLower(strings.ReplaceAll(activeCode, "-", " ")),
		}
		for i, g := range guesses {
			xff := fmt.Sprintf("203.0.113.%d", i+1)
			w := postEnroll("127.0.0.1:5000", "127.0.0.1:7777", nil, map[string]string{"X-Forwarded-For": xff}, g)
			assertRefusedBeforeCheck(fmt.Sprintf("loopback X-Forwarded-For=%s code=%q", xff, g), w)
		}

		// 2. Loopback requests carrying any other proxy or Funnel header.
		proxyHeaders := []struct{ k, v string }{
			{"X-Forwarded-Host", "tunnel.example.test"},
			{"X-Forwarded-Proto", "https"},
			{"X-Forwarded-Port", "443"},
			{"Forwarded", "for=203.0.113.10;proto=https"},
			{"X-Real-IP", "203.0.113.10"},
			{"Tailscale-Funnel-Request", "?1"},
			{"Tailscale-User-Login", "owner@example.test"},
			{"Tailscale-User-Name", "Test Owner"},
			{"Tailscale-Headers-Info", "https://tailscale.com/s/ih"},
			{"CF-Connecting-IP", "203.0.113.10"},
			{"True-Client-IP", "203.0.113.10"},
		}
		for _, h := range proxyHeaders {
			for _, c := range []string{retiredCode, activeCode} {
				w := postEnroll("127.0.0.1:5000", "127.0.0.1:7777", nil, map[string]string{h.k: h.v}, c)
				assertRefusedBeforeCheck(fmt.Sprintf("loopback %s=%s", h.k, h.v), w)
			}
		}

		// Active Tailscale Funnel (including TCPForward Funnel with no HTTP headers).
		f.srv.deps.TailscaleFunnel = func(context.Context, int) (bool, error) { return true, nil }
		wFunnel := postEnroll("127.0.0.1:5000", "127.0.0.1:7777", nil, nil, activeCode)
		assertRefusedBeforeCheck("loopback with active TailscaleFunnel", wFunnel)
		f.srv.deps.TailscaleFunnel = nil
		f.now = f.now.Add(10 * time.Second) // expire funnelCache TTL (5s) within 5m code lifetime

		// 3. Non-loopback requests (IPv4 and IPv6 over TLS).
		for _, peer := range []struct{ remote, host string }{
			{"192.0.2.50:5000", "192.0.2.20:7777"},
			{"[2001:db8::50]:5000", "[2001:db8::20]:7777"},
		} {
			for _, c := range []string{retiredCode, activeCode, "2345-6789"} {
				w := postEnroll(peer.remote, peer.host, &tls.ConnectionState{}, nil, c)
				assertRefusedBeforeCheck("remote "+peer.remote, w)
			}
		}

		// None of the refused attempts enrolled a browser or burned activeCode.
		if got := len(f.srv.devices.active()); got != 1 {
			t.Fatalf("active devices after refused attempts = %d, want 1", got)
		}
		wOK := postEnroll("127.0.0.1:5000", "127.0.0.1:7777", nil, nil, activeCode)
		if wOK.Code != http.StatusOK {
			t.Fatalf("unproxied loopback enroll with unburned short code = %d %s, want 200", wOK.Code, wOK.Body.String())
		}
	})

	t.Run("loopback_single_bucket_and_phone_pairing_preserved", func(t *testing.T) {
		old := pairSysAddrs
		pairSysAddrs = func() ([]pair.IfaceAddr, error) {
			return []pair.IfaceAddr{{Iface: "en0", IP: net.ParseIP("192.168.1.20")}}, nil
		}
		t.Cleanup(func() { pairSysAddrs = old })

		f := newBrowserEnrolFixture(t, nil)
		if err := f.srv.Listen("0.0.0.0:0"); err != nil {
			t.Fatal(err)
		}
		_, appTicket := f.enrollAndSignInApp("Test Menu Bar", DeviceIOS)
		wShort := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, appTicket)
		if wShort.Code != http.StatusOK {
			t.Fatalf("POST /api/pair/browser-code = %d %s", wShort.Code, wShort.Body.String())
		}
		var issued struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(wShort.Body.Bytes(), &issued)

		_, pub := newDeviceKey(t)
		sendLoopbackGuess := func(remoteAddr, host, code string, hdrs map[string]string) *httptest.ResponseRecorder {
			t.Helper()
			b, _ := json.Marshal(map[string]any{
				"code": code, "public_key": pub, "name": "Test Browser", "kind": DeviceBrowser,
			})
			r := httptest.NewRequest(http.MethodPost, "/api/devices/enroll", bytes.NewReader(b))
			r.Header.Set("Content-Type", "application/json")
			r.RemoteAddr, r.Host = remoteAddr, host
			for k, v := range hdrs {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			f.srv.Handler().ServeHTTP(w, r)
			return w
		}

		// 5 unproxied wrong guesses varying loopback IP, Host, Origin, User-Agent, custom headers, and blank XFF.
		variations := []struct {
			remote, host, code string
			hdrs               map[string]string
		}{
			{"127.0.0.1:5001", "127.0.0.1:7777", "2345-6789", map[string]string{"User-Agent": "Guess/1"}},
			{"[::1]:5002", "[::1]:7777", "3456-789A", map[string]string{"Origin": "http://[::1]:7777", "User-Agent": "Guess/2"}},
			{"127.0.0.2:5003", "localhost:7777", "4567-89AB", map[string]string{"Origin": "http://localhost:7777", "X-Client-IP": "198.51.100.3"}},
			{"127.0.0.1:5004", "127.0.0.1:7777", "5678-9ABC", map[string]string{"Via": "1.1 local", "X-Forwarded-For": "   "}},
			{"[::1]:5005", "localhost:7777", "6789-ABCD", map[string]string{"Accept-Language": "en-GB", "X-Request-ID": "req-5"}},
		}
		for i, v := range variations {
			w := sendLoopbackGuess(v.remote, v.host, v.code, v.hdrs)
			if w.Code != http.StatusGone {
				t.Fatalf("unproxied wrong guess %d = %d %s, want 410", i+1, w.Code, w.Body.String())
			}
		}

		// Spoofing X-Forwarded-For after 5 wrong guesses cannot test or burn issued.Code.
		wSpoof := sendLoopbackGuess("127.0.0.1:5099", "127.0.0.1:7777", issued.Code, map[string]string{"X-Forwarded-For": "203.0.113.99"})
		if wSpoof.Code != http.StatusForbidden && wSpoof.Code != http.StatusBadRequest {
			t.Fatalf("spoofed XFF guess = %d %s, want 403 or 400", wSpoof.Code, wSpoof.Body.String())
		}

		// 6th unproxied wrong guess (even from another loopback IP/headers) hits 429 rate_limited.
		w6 := sendLoopbackGuess("127.0.0.42:5006", "127.0.0.1:7777", "789A-BCDE", map[string]string{
			"User-Agent": "Guess/6", "X-Client-IP": "203.0.113.42",
		})
		var body6 map[string]string
		_ = json.Unmarshal(w6.Body.Bytes(), &body6)
		if w6.Code != http.StatusTooManyRequests || body6["reason"] != "rate_limited" {
			t.Fatalf("6th unproxied wrong guess = %d %v, want 429 rate_limited", w6.Code, body6)
		}

		// Even the live short code is blocked on loopback while rate-limited.
		wBlocked := sendLoopbackGuess("127.0.0.1:5007", "127.0.0.1:7777", issued.Code, nil)
		if wBlocked.Code != http.StatusTooManyRequests {
			t.Fatalf("live short code while loopback blocked = %d %s, want 429", wBlocked.Code, wBlocked.Body.String())
		}

		// Phone QR pairing through normal paths (remote TLS and reverse proxy) keeps working.
		nextQRCode := func() string {
			t.Helper()
			wPair := f.doLoopback(http.MethodGet, "/api/pair?new=1", nil, "")
			if wPair.Code != http.StatusOK {
				t.Fatalf("GET /api/pair?new=1 = %d %s", wPair.Code, wPair.Body.String())
			}
			var qr PairResponse
			if err := json.Unmarshal(wPair.Body.Bytes(), &qr); err != nil || qr.Link == "" {
				t.Fatalf("decode pair response = %v (%s)", err, wPair.Body.String())
			}
			parsed, err := pair.ParseLink(qr.Link)
			if err != nil || parsed.Code == "" {
				t.Fatalf("ParseLink(%q) = %v", qr.Link, err)
			}
			return parsed.Code
		}

		_, phonePub := newDeviceKey(t)
		wPhone := f.doRemote(http.MethodPost, "/api/devices/enroll", map[string]any{
			"code": nextQRCode(), "public_key": phonePub, "name": "Test Phone", "kind": DeviceIOS,
		}, "")
		if wPhone.Code != http.StatusOK {
			t.Fatalf("remote TLS phone enroll = %d %s, want 200", wPhone.Code, wPhone.Body.String())
		}

		_, padPub := newDeviceKey(t)
		padBody, _ := json.Marshal(map[string]any{
			"code": nextQRCode(), "public_key": padPub, "name": "Test Pad", "kind": DeviceIPadOS,
		})
		rPad := httptest.NewRequest(http.MethodPost, "/api/devices/enroll", bytes.NewReader(padBody))
		rPad.RemoteAddr, rPad.Host = "127.0.0.1:54321", "127.0.0.1:7777"
		rPad.Header.Set("Content-Type", "application/json")
		rPad.Header.Set("X-Forwarded-For", "100.64.0.44")
		rPad.Header.Set("X-Forwarded-Proto", "https")
		wPad := httptest.NewRecorder()
		f.srv.Handler().ServeHTTP(wPad, rPad)
		if wPad.Code != http.StatusOK {
			t.Fatalf("reverse-proxied iPadOS QR enroll = %d %s, want 200", wPad.Code, wPad.Body.String())
		}
	})
}

// Round 2, Major 2 (server side): a revoked or unknown session cookie is
// cleared (Max-Age=-1 / Max-Age=0 or past Expires) instead of being sent
// forever by the browser, while owner answers still require device proof.
func TestSessionCookieClearedWhenRevokedOrUnknown(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	f := newBrowserEnrolFixture(t, []flow.Ask{{
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
	}})

	_, appTicket := f.enrollAndSignInApp("Test Pad App", DeviceIPadOS)
	wCode := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, appTicket)
	if wCode.Code != http.StatusOK {
		t.Fatalf("POST /api/pair/browser-code = %d %s", wCode.Code, wCode.Body.String())
	}
	var issued struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(wCode.Body.Bytes(), &issued)

	browserKey, browserPub := newDeviceKey(t)
	wEnroll := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": issued.Code, "public_key": browserPub, "name": "Test Browser", "kind": DeviceBrowser,
	}, "")
	if wEnroll.Code != http.StatusOK {
		t.Fatalf("browser enroll = %d %s", wEnroll.Code, wEnroll.Body.String())
	}
	var browserER EnrollResponse
	_ = json.Unmarshal(wEnroll.Body.Bytes(), &browserER)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	baseURL, err := url.Parse("http://127.0.0.1:7777/api/factory/asks")
	if err != nil {
		t.Fatal(err)
	}

	doJar := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var req *http.Request
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			req = httptest.NewRequest(method, "http://127.0.0.1:7777"+path, bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
		} else {
			req = httptest.NewRequest(method, "http://127.0.0.1:7777"+path, nil)
		}
		req.RemoteAddr, req.Host = "127.0.0.1:5000", "127.0.0.1:7777"
		if method != http.MethodGet && method != http.MethodHead {
			req.Header.Set("Origin", "http://127.0.0.1:7777")
		}
		for _, c := range jar.Cookies(req.URL) {
			req.AddCookie(c)
		}
		w := httptest.NewRecorder()
		f.srv.Handler().ServeHTTP(w, req)
		jar.SetCookies(req.URL, w.Result().Cookies())
		return w
	}

	assertCookieCleared := func(desc string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d %s, want 401", desc, w.Code, w.Body.String())
		}
		cleared := false
		for _, c := range w.Result().Cookies() {
			if c.Name == sessionCookie && c.Value == "" && (c.MaxAge < 0 || (!c.Expires.IsZero() && !c.Expires.After(f.now))) {
				cleared = true
				break
			}
		}
		if !cleared {
			t.Fatalf("%s: 401 response did not clear %s cookie (Set-Cookie=%v)", desc, sessionCookie, w.Header().Values("Set-Cookie"))
		}
		if rem := jar.Cookies(baseURL); len(rem) != 0 {
			t.Fatalf("%s: cookie jar still holds %+v after clear", desc, rem)
		}
	}

	// 1. Daemon restart empties in-memory sessions -> stale session cookie is rejected 401 and cleared.
	jar.SetCookies(baseURL, []*http.Cookie{f.signInBrowser(browserKey, browserER.DeviceID)})
	if w := doJar(http.MethodGet, "/api/factory/asks", nil); w.Code != http.StatusOK {
		t.Fatalf("signed-in GET /api/factory/asks = %d %s, want 200", w.Code, w.Body.String())
	}
	f.srv.auth = newAuthState()
	assertCookieCleared("stale cookie after daemon restart", doJar(http.MethodGet, "/api/factory/asks", nil))
	if wNext := doJar(http.MethodGet, "/api/factory/asks", nil); wNext.Code != http.StatusOK {
		t.Fatalf("follow-up loopback GET /api/factory/asks after cookie cleared = %d %s, want 200", wNext.Code, wNext.Body.String())
	}

	// 2. Arbitrary unknown session cookie is cleared on first 401.
	jar.SetCookies(baseURL, []*http.Cookie{{Name: sessionCookie, Value: "unknown-session-ticket", Path: "/"}})
	assertCookieCleared("unknown session cookie", doJar(http.MethodGet, "/api/factory/projects", nil))
	if wNext := doJar(http.MethodGet, "/api/factory/projects", nil); wNext.Code != http.StatusOK {
		t.Fatalf("follow-up loopback GET /api/factory/projects = %d %s, want 200", wNext.Code, wNext.Body.String())
	}

	// 3. Revoked device session cookie (via setup DELETE /api/devices/{id}) is cleared.
	jar.SetCookies(baseURL, []*http.Cookie{f.signInBrowser(browserKey, browserER.DeviceID)})
	rRev := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1:7777/api/devices/"+browserER.DeviceID, strings.NewReader("{}"))
	rRev.RemoteAddr, rRev.Host = "127.0.0.1:5000", "127.0.0.1:7777"
	rRev.Header.Set("Content-Type", "application/json")
	rRev.Header.Set("Origin", "http://127.0.0.1:7777")
	wRev := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(wRev, rRev)
	if wRev.Code != http.StatusOK {
		t.Fatalf("revoke browser = %d %s", wRev.Code, wRev.Body.String())
	}
	assertCookieCleared("revoked browser cookie after DELETE /api/devices/{id}", doJar(http.MethodGet, "/api/factory/asks", nil))
	if wNext := doJar(http.MethodGet, "/api/factory/asks", nil); wNext.Code != http.StatusOK {
		t.Fatalf("follow-up loopback GET after revoke clear = %d %s, want 200", wNext.Code, wNext.Body.String())
	}

	// Also verify tickets still present in auth.tickets for a revoked or missing device clear the cookie.
	f.srv.auth.mu.Lock()
	f.srv.auth.tickets["ticket-revoked-dev"] = ticket{device: browserER.DeviceID, expires: f.now.Add(time.Hour)}
	f.srv.auth.tickets["ticket-unknown-dev"] = ticket{device: "dev-missing", expires: f.now.Add(time.Hour)}
	f.srv.auth.mu.Unlock()
	jar.SetCookies(baseURL, []*http.Cookie{{Name: sessionCookie, Value: "ticket-revoked-dev", Path: "/"}})
	assertCookieCleared("ticket for revoked device", doJar(http.MethodGet, "/api/factory/asks", nil))
	jar.SetCookies(baseURL, []*http.Cookie{{Name: sessionCookie, Value: "ticket-unknown-dev", Path: "/"}})
	assertCookieCleared("ticket for unknown device", doJar(http.MethodGet, "/api/factory/asks", nil))

	// Owner proof rail still holds: without signing in as an active paired device, answering is 403 proof_required.
	wNoProof := doJar(http.MethodPost, "/api/factory/asks/ask-open-1/answer", map[string]any{"option": "Approve"})
	if wNoProof.Code != http.StatusForbidden || !strings.Contains(wNoProof.Body.String(), "proof_required") {
		t.Fatalf("answer without device session after cookie clear = %d %s, want 403 proof_required", wNoProof.Code, wNoProof.Body.String())
	}
}

// Round 3, Major 1: POST /api/devices/reset-pairing must invalidate any active
// browser short code (as code_expired), while a new code minted after the
// reset works.
func TestResetPairingInvalidatesBrowserShortCode(t *testing.T) {
	f := newBrowserEnrolFixture(t, nil)
	_, appTicket := f.enrollAndSignInApp("Test Menu Bar", DeviceIOS)

	// Mint a browser short code before resetting pairing.
	wPreCode := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, appTicket)
	if wPreCode.Code != http.StatusOK {
		t.Fatalf("POST /api/pair/browser-code = %d %s, want 200", wPreCode.Code, wPreCode.Body.String())
	}
	var preIssued struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(wPreCode.Body.Bytes(), &preIssued); err != nil || preIssued.Code == "" {
		t.Fatalf("decode pre-reset browser-code = %v (%s)", err, wPreCode.Body.String())
	}

	// Owner resets pairing from setup within the 5-minute short-code window.
	f.now = f.now.Add(10 * time.Second)
	wReset := f.doLoopback(http.MethodPost, "/api/devices/reset-pairing", map[string]any{}, "")
	if wReset.Code != http.StatusOK {
		t.Fatalf("POST /api/devices/reset-pairing = %d %s, want 200", wReset.Code, wReset.Body.String())
	}

	// The pre-reset browser short code must no longer enrol (refused as expired).
	browserKey, browserPub := newDeviceKey(t)
	wPreEnroll := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": preIssued.Code, "public_key": browserPub, "name": "Test Browser", "kind": DeviceBrowser,
	}, "")
	var preEnrollBody map[string]string
	_ = json.Unmarshal(wPreEnroll.Body.Bytes(), &preEnrollBody)
	if wPreEnroll.Code != http.StatusGone || preEnrollBody["reason"] != "code_expired" {
		t.Fatalf("enroll with pre-reset browser short code = %d %s, want 410 code_expired", wPreEnroll.Code, wPreEnroll.Body.String())
	}

	// The revoked app session can no longer mint a browser short code.
	if wOldApp := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, appTicket); wOldApp.Code != http.StatusUnauthorized {
		t.Fatalf("pre-reset app session POST /api/pair/browser-code = %d %s, want 401", wOldApp.Code, wOldApp.Body.String())
	}

	// A newly paired app after the reset can mint a new browser short code that works.
	_, newAppTicket := f.enrollAndSignInApp("Test Menu Bar After Reset", DeviceIOS)
	wPostCode := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, newAppTicket)
	if wPostCode.Code != http.StatusOK {
		t.Fatalf("post-reset POST /api/pair/browser-code = %d %s, want 200", wPostCode.Code, wPostCode.Body.String())
	}
	var postIssued struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(wPostCode.Body.Bytes(), &postIssued); err != nil || postIssued.Code == "" {
		t.Fatalf("decode post-reset browser-code = %v (%s)", err, wPostCode.Body.String())
	}

	wPostEnroll := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": postIssued.Code, "public_key": browserPub, "name": "Test Browser", "kind": DeviceBrowser,
	}, "")
	if wPostEnroll.Code != http.StatusOK {
		t.Fatalf("enroll with post-reset browser short code = %d %s, want 200", wPostEnroll.Code, wPostEnroll.Body.String())
	}
	var er EnrollResponse
	if err := json.Unmarshal(wPostEnroll.Body.Bytes(), &er); err != nil || er.DeviceID == "" {
		t.Fatalf("post-reset enroll response = %s (err=%v)", wPostEnroll.Body.String(), err)
	}
	if cookie := f.signInBrowser(browserKey, er.DeviceID); cookie == nil || cookie.Value == "" {
		t.Fatal("expected valid browser session cookie after post-reset enroll")
	}
}

// Round 3, Major 2 (server side): GET /api/factory/asks lists only asks for
// the owner (open gated asks per design.md 7.1), excluding worker decision asks
// meant for the coordinator.
func TestFactoryAsksExcludesWorkerDecisionAsks(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	t.Run("mixed_open_asks", func(t *testing.T) {
		f := newBrowserEnrolFixture(t, []flow.Ask{
			{
				ID:        "ask-decision-worker",
				Project:   "proj-demo",
				Kind:      flow.KindDecision,
				Gated:     false,
				AskedBy:   flow.RoleWorker,
				Question:  "Should T1 use helper A or B?",
				Options:   []string{"Helper A", "Helper B"},
				Recommend: "Helper A",
				Task:      "T1",
				At:        now,
				Status:    flow.AskStatusOpen,
			},
			{
				ID:        "ask-gate-owner",
				Project:   "proj-demo",
				Kind:      flow.KindGate,
				Gated:     true,
				AskedBy:   flow.RoleCoordinator,
				Question:  "Approve the plan for T1?",
				Options:   []string{"Approve", "Request changes"},
				Recommend: "Approve",
				Task:      "T1",
				At:        now.Add(time.Second),
				Status:    flow.AskStatusOpen,
			},
			{
				ID:        "ask-release-owner",
				Project:   "proj-demo",
				Kind:      flow.KindRelease,
				Gated:     true,
				AskedBy:   flow.RoleCoordinator,
				Question:  "Tag and publish v0.2.0?",
				Options:   []string{"Publish v0.2.0", "Hold"},
				Recommend: "Publish v0.2.0",
				At:        now.Add(2 * time.Second),
				Status:    flow.AskStatusOpen,
			},
			{
				ID:        "ask-closed-owner",
				Project:   "proj-demo",
				Kind:      flow.KindGate,
				Gated:     true,
				AskedBy:   flow.RoleCoordinator,
				Question:  "Already answered ask",
				Options:   []string{"Yes", "No"},
				Recommend: "Yes",
				At:        now.Add(-time.Minute),
				Status:    flow.AskStatusAnswered,
				Answer:    "Yes",
			},
		})

		// Engine.Asks(true) still returns the worker decision ask for the coordinator.
		if engOpen := f.eng.Asks(true); len(engOpen) != 3 || engOpen[0].ID != "ask-decision-worker" {
			t.Fatalf("Engine.Asks(true) = %+v, want all 3 open asks including ask-decision-worker", engOpen)
		}

		// GET /api/factory/asks on loopback and for a paired device lists only the 2 open owner asks.
		_, phoneTicket := f.enrollAndSignInApp("Test Phone", DeviceIOS)
		for _, tc := range []struct {
			name string
			w    *httptest.ResponseRecorder
		}{
			{"loopback", f.doLoopback(http.MethodGet, "/api/factory/asks", nil, "")},
			{"paired_remote", f.doRemote(http.MethodGet, "/api/factory/asks", nil, phoneTicket)},
		} {
			if tc.w.Code != http.StatusOK {
				t.Fatalf("%s GET /api/factory/asks = %d %s, want 200", tc.name, tc.w.Code, tc.w.Body.String())
			}
			var resp struct {
				Protocol int        `json:"protocol"`
				Asks     []flow.Ask `json:"asks"`
			}
			if err := json.Unmarshal(tc.w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("%s decode asks: %v (%s)", tc.name, err, tc.w.Body.String())
			}
			if resp.Protocol != 1 || len(resp.Asks) != 2 || resp.Asks[0].ID != "ask-gate-owner" || resp.Asks[1].ID != "ask-release-owner" {
				t.Fatalf("%s asks = %+v, want only [ask-gate-owner ask-release-owner] (no worker decision ask)", tc.name, resp.Asks)
			}
		}
	})

	t.Run("only_decision_ask_open_returns_empty_array", func(t *testing.T) {
		f := newBrowserEnrolFixture(t, []flow.Ask{
			{
				ID:        "ask-decision-only",
				Project:   "proj-demo",
				Kind:      flow.KindDecision,
				Gated:     false,
				AskedBy:   flow.RoleWorker,
				Question:  "Should T1 use helper A or B?",
				Options:   []string{"Helper A", "Helper B"},
				Recommend: "Helper A",
				Task:      "T1",
				At:        now,
				Status:    flow.AskStatusOpen,
			},
		})

		w := f.doLoopback(http.MethodGet, "/api/factory/asks", nil, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/factory/asks = %d %s, want 200", w.Code, w.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decode raw asks: %v (%s)", err, w.Body.String())
		}
		if strings.TrimSpace(string(raw["asks"])) != "[]" {
			t.Fatalf("asks JSON = %s, want empty array [] (never null, no decision ask)", string(raw["asks"]))
		}
	})
}

// 6.5 round 2 (D17 / O20): default loopback-only 127.0.0.1 daemon (where
// GET /api/pair returns ErrLoopback and no link) allows unproxied loopback
// kind="macos" enrolment without a code, lists the Mac as kind="macos",
// refuses ticketless local browser-code and ask-answer requests with 403,
// and lets the enrolled Mac mint a browser short code to answer an ask.
func TestMacOSEnrollLoopbackOnlyHappyPath(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	f := newBrowserEnrolFixture(t, []flow.Ask{{
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
	}})
	if err := f.srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}

	// Default loopback-only daemon has no phone pairing link.
	wPair := f.doLoopback(http.MethodGet, "/api/pair", nil, "")
	if wPair.Code != http.StatusOK {
		t.Fatalf("GET /api/pair = %d %s, want 200", wPair.Code, wPair.Body.String())
	}
	var pr PairResponse
	if err := json.Unmarshal(wPair.Body.Bytes(), &pr); err != nil {
		t.Fatalf("decode /api/pair: %v", err)
	}
	if pr.Link != "" || pr.Error == "" {
		t.Fatalf("loopback-only /api/pair = %+v, want empty link and ErrLoopback error", pr)
	}

	// Ticketless loopback rails on a 127.0.0.1 daemon.
	wNoTicketCode := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, "")
	var codeErr map[string]string
	_ = json.Unmarshal(wNoTicketCode.Body.Bytes(), &codeErr)
	if wNoTicketCode.Code != http.StatusForbidden || codeErr["reason"] != "device_session_required" {
		t.Fatalf("ticketless loopback POST /api/pair/browser-code = %d %v, want 403 device_session_required", wNoTicketCode.Code, codeErr)
	}
	wNoTicketAns := f.doLoopback(http.MethodPost, "/api/factory/asks/ask-open-1/answer", map[string]any{"option": "Approve"}, "")
	if wNoTicketAns.Code != http.StatusForbidden || !strings.Contains(wNoTicketAns.Body.String(), "proof_required") {
		t.Fatalf("ticketless loopback answer = %d %s, want 403 proof_required", wNoTicketAns.Code, wNoTicketAns.Body.String())
	}

	// Unproxied loopback Mac enrolment with empty code succeeds and lists as kind="macos".
	macKey, macPub := newDeviceKey(t)
	wMac := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": "", "public_key": macPub, "name": "Studio Mac", "kind": "macos", "hardware": false,
	}, "")
	if wMac.Code != http.StatusOK {
		t.Fatalf("loopback macos enroll = %d %s, want 200", wMac.Code, wMac.Body.String())
	}
	var macER EnrollResponse
	if err := json.Unmarshal(wMac.Body.Bytes(), &macER); err != nil || macER.DeviceID == "" || macER.Name != "Studio Mac" {
		t.Fatalf("macos enroll response = %s (err=%v)", wMac.Body.String(), err)
	}

	wDevs := f.doLoopback(http.MethodGet, "/api/devices", nil, "")
	if wDevs.Code != http.StatusOK {
		t.Fatalf("GET /api/devices = %d %s", wDevs.Code, wDevs.Body.String())
	}
	var devList struct {
		Devices []Device `json:"devices"`
	}
	if err := json.Unmarshal(wDevs.Body.Bytes(), &devList); err != nil {
		t.Fatalf("decode /api/devices: %v", err)
	}
	if len(devList.Devices) != 1 || devList.Devices[0].ID != macER.DeviceID || devList.Devices[0].Kind != "macos" || devList.Devices[0].Name != "Studio Mac" {
		t.Fatalf("devices = %+v, want single macos device %q", devList.Devices, macER.DeviceID)
	}

	loggedEnroll := false
	for _, ev := range f.log.Tail(1000) {
		if ev.Type == "web.device.enroll" && strings.Contains(string(ev.Payload), macER.DeviceID) && strings.Contains(string(ev.Payload), `"macos"`) {
			loggedEnroll = true
			break
		}
	}
	if !loggedEnroll {
		t.Fatalf("missing web.device.enroll event for macos device %q", macER.DeviceID)
	}

	// Mac signs in over TLS pin and mints a browser short code; browser enrols and answers ask-open-1.
	macTicket := f.signInApp(macKey, macER.DeviceID)
	wShort := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, macTicket)
	if wShort.Code != http.StatusOK {
		t.Fatalf("Mac POST /api/pair/browser-code = %d %s, want 200", wShort.Code, wShort.Body.String())
	}
	var issued struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(wShort.Body.Bytes(), &issued); err != nil || issued.Code == "" {
		t.Fatalf("decode browser-code: %v (%s)", err, wShort.Body.String())
	}

	browserKey, browserPub := newDeviceKey(t)
	wBrowser := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": issued.Code, "public_key": browserPub, "name": "Local Browser", "kind": DeviceBrowser,
	}, "")
	if wBrowser.Code != http.StatusOK {
		t.Fatalf("browser enroll = %d %s, want 200", wBrowser.Code, wBrowser.Body.String())
	}
	var browserER EnrollResponse
	_ = json.Unmarshal(wBrowser.Body.Bytes(), &browserER)
	browserCookie := f.signInBrowser(browserKey, browserER.DeviceID)

	wAns := f.doLoopback(http.MethodPost, "/api/factory/asks/ask-open-1/answer", map[string]any{"option": "Approve"}, "", browserCookie)
	if wAns.Code != http.StatusOK {
		t.Fatalf("browser answer = %d %s, want 200", wAns.Code, wAns.Body.String())
	}
	logBytes, err := os.ReadFile(f.paths.Log("proj-demo"))
	if err != nil || !strings.Contains(string(logBytes), browserER.DeviceID) {
		t.Fatalf("factory log missing browser device_id %q (err=%v)", browserER.DeviceID, err)
	}
}

// 6.5 round 2 (O20): Mac loopback enrolment and browser short-code issuance
// leave an active phone QR link identical and usable by a remote iPhone.
func TestMacOSEnrollLeavesPhoneQRUntouched(t *testing.T) {
	old := pairSysAddrs
	pairSysAddrs = func() ([]pair.IfaceAddr, error) {
		return []pair.IfaceAddr{{Iface: "en0", IP: net.ParseIP("192.168.1.20")}}, nil
	}
	t.Cleanup(func() { pairSysAddrs = old })

	f := newBrowserEnrolFixture(t, nil)
	if err := f.srv.Listen("0.0.0.0:0"); err != nil {
		t.Fatal(err)
	}

	wPair := f.doLoopback(http.MethodGet, "/api/pair?new=1", nil, "")
	if wPair.Code != http.StatusOK {
		t.Fatalf("GET /api/pair?new=1 = %d %s", wPair.Code, wPair.Body.String())
	}
	var qr PairResponse
	if err := json.Unmarshal(wPair.Body.Bytes(), &qr); err != nil || qr.Link == "" {
		t.Fatalf("GET /api/pair body = %s (err=%v)", wPair.Body.String(), err)
	}
	parsed, err := pair.ParseLink(qr.Link)
	if err != nil || parsed.Code == "" {
		t.Fatalf("ParseLink(%q) = %+v, %v", qr.Link, parsed, err)
	}

	// Enrol Mac via loopback kind="macos" and issue a browser short code.
	macKey, macPub := newDeviceKey(t)
	wMac := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": "", "public_key": macPub, "name": "Studio Mac", "kind": "macos",
	}, "")
	if wMac.Code != http.StatusOK {
		t.Fatalf("loopback macos enroll = %d %s, want 200", wMac.Code, wMac.Body.String())
	}
	var macER EnrollResponse
	_ = json.Unmarshal(wMac.Body.Bytes(), &macER)
	macTicket := f.signInApp(macKey, macER.DeviceID)
	wShort := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, macTicket)
	if wShort.Code != http.StatusOK {
		t.Fatalf("POST /api/pair/browser-code = %d %s, want 200", wShort.Code, wShort.Body.String())
	}

	// Active phone QR link is unchanged and still enrols a remote iPhone.
	wPairAgain := f.doLoopback(http.MethodGet, "/api/pair", nil, "")
	var qrAgain PairResponse
	_ = json.Unmarshal(wPairAgain.Body.Bytes(), &qrAgain)
	if qrAgain.Link != qr.Link {
		t.Fatalf("macos enroll + browser-code changed phone QR link: got %q, want %q", qrAgain.Link, qr.Link)
	}
	_, phonePub := newDeviceKey(t)
	wPhone := f.doRemote(http.MethodPost, "/api/devices/enroll", map[string]any{
		"code": parsed.Code, "public_key": phonePub, "name": "Test Phone", "kind": DeviceIOS,
	}, "")
	if wPhone.Code != http.StatusOK {
		t.Fatalf("remote iPhone enroll after macos enroll = %d %s, want 200", wPhone.Code, wPhone.Body.String())
	}
}

// 6.5 round 2: code rails on kind="macos" (remote/proxied/Funnel/non-loopback
// Host refusal, non-empty code refusal without burning codes, empty name
// defaulting to "Mac", shared loopback rate limit, and key-reuse safety).
func TestMacOSEnrollRailsAndDefaults(t *testing.T) {
	t.Run("remote_proxied_funnel_and_non_loopback_host_refused", func(t *testing.T) {
		f := newBrowserEnrolFixture(t, nil)
		_, pub := newDeviceKey(t)

		post := func(remoteAddr, host string, tlsState *tls.ConnectionState, hdrs map[string]string) *httptest.ResponseRecorder {
			t.Helper()
			b, _ := json.Marshal(map[string]any{
				"code": "", "public_key": pub, "name": "Remote Mac", "kind": "macos",
			})
			r := httptest.NewRequest(http.MethodPost, "/api/devices/enroll", bytes.NewReader(b))
			r.Header.Set("Content-Type", "application/json")
			r.RemoteAddr, r.Host, r.TLS = remoteAddr, host, tlsState
			for k, v := range hdrs {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			f.srv.Handler().ServeHTTP(w, r)
			return w
		}

		// Remote TLS caller -> 403 loopback_only.
		wRemote := post("192.0.2.50:5000", "192.0.2.20:7777", &tls.ConnectionState{}, nil)
		var bodyRemote map[string]string
		_ = json.Unmarshal(wRemote.Body.Bytes(), &bodyRemote)
		if wRemote.Code != http.StatusForbidden || bodyRemote["reason"] != "loopback_only" {
			t.Fatalf("remote macos enroll = %d %s, want 403 loopback_only", wRemote.Code, wRemote.Body.String())
		}

		// Reverse-proxied loopback caller -> 403 loopback_only.
		wProxy := post("127.0.0.1:5000", "127.0.0.1:7777", nil, map[string]string{"X-Forwarded-For": "203.0.113.10"})
		var bodyProxy map[string]string
		_ = json.Unmarshal(wProxy.Body.Bytes(), &bodyProxy)
		if wProxy.Code != http.StatusForbidden || bodyProxy["reason"] != "loopback_only" {
			t.Fatalf("proxied macos enroll = %d %s, want 403 loopback_only", wProxy.Code, wProxy.Body.String())
		}

		// Active Tailscale Funnel -> 403 loopback_only.
		f.srv.deps.TailscaleFunnel = func(context.Context, int) (bool, error) { return true, nil }
		wFunnel := post("127.0.0.1:5000", "127.0.0.1:7777", nil, nil)
		var bodyFunnel map[string]string
		_ = json.Unmarshal(wFunnel.Body.Bytes(), &bodyFunnel)
		if wFunnel.Code != http.StatusForbidden || bodyFunnel["reason"] != "loopback_only" {
			t.Fatalf("Funnel macos enroll = %d %s, want 403 loopback_only", wFunnel.Code, wFunnel.Body.String())
		}
		f.srv.deps.TailscaleFunnel = nil
		f.now = f.now.Add(10 * time.Second) // expire funnelCache TTL (5s)

		// Non-loopback Host on loopback socket -> 403 (host_not_allowed or loopback_only).
		wHost := post("127.0.0.1:5000", "evil.example:7777", nil, nil)
		if wHost.Code != http.StatusForbidden {
			t.Fatalf("non-loopback Host macos enroll = %d %s, want 403", wHost.Code, wHost.Body.String())
		}

		if got := len(f.srv.devices.active()); got != 0 {
			t.Fatalf("active devices after refused macos attempts = %d, want 0", got)
		}
	})

	t.Run("non_empty_code_refused_without_burning", func(t *testing.T) {
		f := newBrowserEnrolFixture(t, nil)
		_, appTicket := f.enrollAndSignInApp("Existing Phone", DeviceIOS)

		phoneCode, _, _, err := f.srv.enroll.current(f.now, true)
		if err != nil {
			t.Fatal(err)
		}
		wShort := f.doLoopback(http.MethodPost, "/api/pair/browser-code", nil, appTicket)
		if wShort.Code != http.StatusOK {
			t.Fatalf("POST /api/pair/browser-code = %d %s", wShort.Code, wShort.Body.String())
		}
		var issued struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(wShort.Body.Bytes(), &issued)

		_, macPub := newDeviceKey(t)
		wWithPhone := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
			"code": phoneCode, "public_key": macPub, "name": "Studio Mac", "kind": "macos",
		}, "")
		if wWithPhone.Code != http.StatusBadRequest || !strings.Contains(strings.ToLower(wWithPhone.Body.String()), "code") {
			t.Fatalf("macos enroll with phone code = %d %s, want 400 rejecting code", wWithPhone.Code, wWithPhone.Body.String())
		}

		wWithShort := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
			"code": issued.Code, "public_key": macPub, "name": "Studio Mac", "kind": "macos",
		}, "")
		if wWithShort.Code != http.StatusBadRequest || !strings.Contains(strings.ToLower(wWithShort.Body.String()), "code") {
			t.Fatalf("macos enroll with browser code = %d %s, want 400 rejecting code", wWithShort.Code, wWithShort.Body.String())
		}

		// Neither code was burned.
		_, phonePub := newDeviceKey(t)
		wPhone := f.doRemote(http.MethodPost, "/api/devices/enroll", map[string]any{
			"code": phoneCode, "public_key": phonePub, "name": "Second Phone", "kind": DeviceIOS,
		}, "")
		if wPhone.Code != http.StatusOK {
			t.Fatalf("phone code after refused macos enroll = %d %s, want 200", wPhone.Code, wPhone.Body.String())
		}
		_, browserPub := newDeviceKey(t)
		wBrowser := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
			"code": issued.Code, "public_key": browserPub, "name": "Local Browser", "kind": DeviceBrowser,
		}, "")
		if wBrowser.Code != http.StatusOK {
			t.Fatalf("browser short code after refused macos enroll = %d %s, want 200", wBrowser.Code, wBrowser.Body.String())
		}
	})

	t.Run("empty_name_defaults_to_Mac", func(t *testing.T) {
		f := newBrowserEnrolFixture(t, nil)
		_, macPub := newDeviceKey(t)
		w := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
			"code": "", "public_key": macPub, "name": "   ", "kind": "macos",
		}, "")
		if w.Code != http.StatusOK {
			t.Fatalf("macos enroll with blank name = %d %s, want 200", w.Code, w.Body.String())
		}
		var er EnrollResponse
		if err := json.Unmarshal(w.Body.Bytes(), &er); err != nil || er.Name != "Mac" {
			t.Fatalf("default macos name = %q (body=%s, err=%v), want %q", er.Name, w.Body.String(), err, "Mac")
		}
	})

	t.Run("shared_loopback_rate_limit_blocks_macos", func(t *testing.T) {
		f := newBrowserEnrolFixture(t, nil)
		_, pub := newDeviceKey(t)
		for i := 1; i <= 5; i++ {
			w := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
				"code": "WRONG-CODE", "public_key": pub, "name": "Test Browser", "kind": DeviceBrowser,
			}, "")
			if w.Code != http.StatusGone {
				t.Fatalf("wrong guess %d = %d %s, want 410", i, w.Code, w.Body.String())
			}
		}
		w6 := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
			"code": "WRONG-CODE", "public_key": pub, "name": "Test Browser", "kind": DeviceBrowser,
		}, "")
		if w6.Code != http.StatusTooManyRequests {
			t.Fatalf("6th wrong guess = %d %s, want 429", w6.Code, w6.Body.String())
		}

		// macos enrolment shares the loopback enroll bucket and gets 429 rate_limited.
		_, macPub := newDeviceKey(t)
		wMac := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
			"code": "", "public_key": macPub, "name": "Studio Mac", "kind": "macos",
		}, "")
		var body map[string]string
		_ = json.Unmarshal(wMac.Body.Bytes(), &body)
		if wMac.Code != http.StatusTooManyRequests || body["reason"] != "rate_limited" {
			t.Fatalf("macos enroll while loopback rate-limited = %d %v, want 429 rate_limited", wMac.Code, body)
		}
	})

	t.Run("reusing_active_ios_public_key_gives_no_usable_session", func(t *testing.T) {
		f := newBrowserEnrolFixture(t, nil)
		_, iosPub := newDeviceKey(t)
		code, _, _, err := f.srv.enroll.current(f.now, true)
		if err != nil {
			t.Fatal(err)
		}
		wIOS := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
			"code": code, "public_key": iosPub, "name": "Owner iPhone", "kind": DeviceIOS,
		}, "")
		if wIOS.Code != http.StatusOK {
			t.Fatalf("ios enroll = %d %s", wIOS.Code, wIOS.Body.String())
		}

		// Re-enrolling that public key as macos does not let a caller without the private key sign in.
		wRelabel := f.doLoopback(http.MethodPost, "/api/devices/enroll", map[string]any{
			"code": "", "public_key": iosPub, "name": "Relabeled Mac", "kind": "macos",
		}, "")
		if wRelabel.Code != http.StatusOK {
			t.Fatalf("macos enroll with existing public key = %d %s, want 200", wRelabel.Code, wRelabel.Body.String())
		}
		var er EnrollResponse
		_ = json.Unmarshal(wRelabel.Body.Bytes(), &er)

		otherKey, _ := newDeviceKey(t)
		wc := f.doLoopback(http.MethodGet, "/api/auth/challenge", nil, "")
		var ch ChallengeResponse
		_ = json.Unmarshal(wc.Body.Bytes(), &ch)
		sum := sha256.Sum256([]byte(SignedText(f.srv.deps.Computer.ID, f.srv.deps.TLS.Pin(), er.DeviceID, ch.Nonce, f.now.Unix())))
		sig, err := ecdsa.SignASN1(rand.Reader, otherKey, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		ws := f.doLoopback(http.MethodPost, "/api/auth/session", map[string]any{
			"device_id": er.DeviceID, "nonce": ch.Nonce, "time": f.now.Unix(),
			"signature": base64.StdEncoding.EncodeToString(sig), "client": "app",
		}, "")
		var sBody map[string]string
		_ = json.Unmarshal(ws.Body.Bytes(), &sBody)
		if ws.Code != http.StatusUnauthorized || sBody["reason"] != "bad_signature" {
			t.Fatalf("session with wrong private key = %d %v, want 401 bad_signature", ws.Code, sBody)
		}
	})
}
