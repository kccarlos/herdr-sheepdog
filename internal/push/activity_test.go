package push

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const tokAct = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccc777777"

func activityState() ActivityState {
	return ActivityState{Phase: PhaseAway, Waiting: 4, Working: 2, Done: 1, Rows: []ActivityRow{
		{Pane: "w1:p1", Agent: "auth-fix", Question: "Run npm test in webapp?"},
		{Pane: "w2:p1", Agent: "blog", Question: "Publish the draft?"},
		{Pane: "w3:p1", Agent: "db", Question: "Drop the table?"},
		{Pane: "w4:p1", Agent: "extra", Question: "Fourth?"},
	}}
}

func TestRegisterActivityChecks(t *testing.T) {
	s, _ := newRelaySender(t, newFakeRelay(t), "")
	if err := s.RegisterActivity("", tokAct, EnvProduction); err != ErrNoDevice {
		t.Errorf("no device: %v", err)
	}
	if err := s.RegisterActivity("dev-1", "abc", EnvProduction); err != ErrBadToken {
		t.Errorf("bad token: %v", err)
	}
	if err := s.RegisterActivity("dev-1", tokAct, "staging"); err != ErrBadEnvironment {
		t.Errorf("bad environment: %v", err)
	}
	if err := s.RegisterActivity("dev-1", strings.ToUpper(tokAct), EnvSandbox); err != nil {
		t.Fatal(err)
	}
	if a := s.Activities(); len(a) != 1 || a[0].Token != tokAct || a[0].DeviceID != "dev-1" {
		t.Errorf("activities = %+v", a)
	}
	// One per device: a new activity replaces the old one.
	if err := s.RegisterActivity("dev-1", tokB, EnvSandbox); err != nil {
		t.Fatal(err)
	}
	if a := s.Activities(); len(a) != 1 || a[0].Token != tokB {
		t.Errorf("activities = %+v", a)
	}
	// Revoking the device forgets it.
	if err := s.ForgetDevices([]string{"dev-1"}); err != nil {
		t.Fatal(err)
	}
	if a := s.Activities(); len(a) != 0 {
		t.Errorf("after revoke: %+v", a)
	}
}

// Activities survive a daemon restart (an update while the phone is
// away), with their relay tokens; one iOS has surely ended does not.
func TestActivitiesPersist(t *testing.T) {
	f := newFakeRelay(t)
	dir := t.TempDir()
	s, _ := newRelaySender(t, f, dir)
	if err := s.RegisterActivity("dev-1", tokAct, EnvProduction); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateActivity(context.Background(), "dev-1", ActivityUpdate{State: activityState()}); err != nil {
		t.Fatal(err)
	}
	s2, _ := newRelaySender(t, f, dir)
	a := s2.Activities()
	if len(a) != 1 || a[0].Token != tokAct || a[0].RelayToken == "" || a[0].DeviceID != "dev-1" {
		t.Fatalf("reloaded = %+v", a)
	}
	s2.ForgetActivity("dev-1")
	if s3, _ := newRelaySender(t, f, dir); len(s3.Activities()) != 0 {
		t.Error("a forgotten activity came back")
	}

	// Older than iOS keeps an activity: dropped on load.
	_ = s2.RegisterActivity("dev-2", tokAct, EnvProduction)
	later := time.Now().Add(ActivityLifetime + time.Minute)
	s4, err := New(Config{Mode: ModeRelay, RelayURL: f.srv.URL, DataDir: dir, Client: f.srv.Client(), Now: func() time.Time { return later }}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := s4.Activities(); len(a) != 0 {
		t.Errorf("stale activity loaded: %+v", a)
	}
}

// Through the relay, the rows go sealed with the phone's content key and
// the relay sees only the phase and the counts.
func TestActivityThroughRelaySealsTheRows(t *testing.T) {
	f := newFakeRelay(t)
	s, log := newRelaySender(t, f, "")
	dev, err := s.RegisterDevice(Registration{Token: tokA, Environment: EnvProduction, DeviceID: "dev-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterActivity("dev-1", tokAct, EnvProduction); err != nil {
		t.Fatal(err)
	}
	stale := time.Unix(1_800_000_900, 0)
	if err := s.UpdateActivity(context.Background(), "dev-1", ActivityUpdate{State: activityState(), Stale: stale, Urgent: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.registers) != 1 || f.registers[0]["device_token"] != tokAct {
		t.Fatalf("registers = %v", f.registers)
	}
	if len(f.activities) != 1 {
		t.Fatalf("activities = %d", len(f.activities))
	}
	a := f.activities[0]
	if a.Event != "update" || a.Priority != 10 || a.StaleDate != stale.Unix() || a.RelayToken == "" || a.Timestamp == 0 {
		t.Errorf("body = %+v", a)
	}
	cs := a.ContentState
	if cs.Phase != PhaseAway || cs.Waiting != 4 || cs.Working != 2 || cs.Done != 1 || len(cs.Rows) != 0 || cs.Sealed == nil {
		t.Fatalf("content state = %+v", cs)
	}
	wire, _ := json.Marshal(a)
	for _, secret := range []string{"auth-fix", "npm test", "w1:p1"} {
		if strings.Contains(string(wire), secret) {
			t.Errorf("the relay sees %q: %s", secret, wire)
		}
	}
	ct, _ := base64.StdEncoding.DecodeString(cs.Sealed.C)
	nonce, _ := base64.StdEncoding.DecodeString(cs.Sealed.N)
	plain, err := Open(dev.ContentKey, ct, nonce)
	if err != nil {
		t.Fatal(err)
	}
	var opened sealedRows
	if err := json.Unmarshal(plain, &opened); err != nil {
		t.Fatal(err)
	}
	if len(opened.Rows) != ActivityRowLimit || opened.Rows[0].Agent != "auth-fix" || opened.Rows[0].Question != "Run npm test in webapp?" {
		t.Errorf("opened = %+v", opened)
	}
	// The relay token is kept for the next update.
	if err := s.UpdateActivity(context.Background(), "dev-1", ActivityUpdate{State: activityState()}); err != nil {
		t.Fatal(err)
	}
	if len(f.registers) != 1 || f.activities[1].Priority != 5 {
		t.Errorf("second update: registers=%d priority=%d", len(f.registers), f.activities[1].Priority)
	}
	if len(log.of("push.activity.sent")) != 2 {
		t.Errorf("sent = %v", log.of("push.activity.sent"))
	}
	for _, e := range log.events {
		b, _ := json.Marshal(e.payload)
		if strings.Contains(string(b), tokAct) {
			t.Errorf("%s logs the whole token: %s", e.typ, b)
		}
	}
}

// Without the phone's content key the rows stay home: counts only.
func TestActivityWithoutContentKeySendsCountsOnly(t *testing.T) {
	f := newFakeRelay(t)
	s, _ := newRelaySender(t, f, "")
	if err := s.RegisterActivity("dev-1", tokAct, EnvProduction); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateActivity(context.Background(), "dev-1", ActivityUpdate{State: activityState()}); err != nil {
		t.Fatal(err)
	}
	cs := f.activities[0].ContentState
	if len(cs.Rows) != 0 || cs.Sealed != nil || cs.Waiting != 4 {
		t.Errorf("content state = %+v", cs)
	}
}

// A gone activity token (410) is forgotten; an end forgets it too.
func TestActivityGoneAndEnd(t *testing.T) {
	f := newFakeRelay(t)
	s, _ := newRelaySender(t, f, "")
	_ = s.RegisterActivity("dev-1", tokAct, EnvProduction)
	f.actStatus = []int{410}
	if err := s.UpdateActivity(context.Background(), "dev-1", ActivityUpdate{State: activityState()}); err == nil {
		t.Fatal("want an error")
	}
	if len(s.Activities()) != 0 {
		t.Error("a gone token is kept")
	}
	_ = s.RegisterActivity("dev-1", tokAct, EnvProduction)
	if err := s.UpdateActivity(context.Background(), "dev-1", ActivityUpdate{State: activityState(), End: true, Dismissal: time.Unix(5, 0)}); err != nil {
		t.Fatal(err)
	}
	last := f.activities[len(f.activities)-1]
	if last.Event != "end" || last.DismissalDate != 5 {
		t.Errorf("end = %+v", last)
	}
	if len(s.Activities()) != 0 {
		t.Error("an ended activity is kept")
	}
}

// Straight to APNs: the liveactivity push type and topic, and the aps
// payload iOS reads.
func TestActivityDirectAPNs(t *testing.T) {
	f := newFakeAPNs(t)
	s, _, _ := newTestSender(t, f, "")
	if _, err := s.RegisterDevice(Registration{Token: tokA, Environment: EnvProduction, DeviceID: "dev-1"}); err != nil {
		t.Fatal(err)
	}
	_ = s.RegisterActivity("dev-1", tokAct, EnvProduction)
	if err := s.UpdateActivity(context.Background(), "dev-1", ActivityUpdate{State: activityState(), Stale: time.Unix(99, 0)}); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	r := reqs[0]
	if r.path != "/3/device/"+tokAct || r.header.Get("apns-push-type") != "liveactivity" ||
		r.header.Get("apns-topic") != "com.example.app.push-type.liveactivity" || r.header.Get("apns-priority") != "5" {
		t.Errorf("request = %s %v", r.path, r.header)
	}
	aps, _ := r.body["aps"].(map[string]any)
	cs, _ := aps["content-state"].(map[string]any)
	if aps["event"] != "update" || aps["stale-date"] != float64(99) || cs["phase"] != PhaseAway || cs["sealed"] == nil {
		t.Errorf("aps = %v", aps)
	}
}

// The largest update stays well under APNs' 4 KB.
func TestActivityFitsAPNs(t *testing.T) {
	f := newFakeRelay(t)
	s, _ := newRelaySender(t, f, "")
	_, _ = s.RegisterDevice(Registration{Token: tokA, Environment: EnvProduction, DeviceID: "dev-1"})
	_ = s.RegisterActivity("dev-1", tokAct, EnvProduction)
	st := activityState()
	for i := range st.Rows {
		st.Rows[i].Agent = strings.Repeat("é", 32)
		st.Rows[i].Question = strings.Repeat("é", 90)
		st.Rows[i].Pane = strings.Repeat("p", 40)
	}
	if err := s.UpdateActivity(context.Background(), "dev-1", ActivityUpdate{State: st}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"aps": map[string]any{"timestamp": 1, "event": "update", "content-state": f.activities[0].ContentState, "stale-date": 1}})
	if len(b) > 3500 {
		t.Errorf("payload = %d bytes", len(b))
	}
}
