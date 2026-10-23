package push

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Live Activity push updates (docs/dev/push.md "Live Activity updates").
// While the app runs it updates the voice session's lock-screen banner
// itself; once it has been away (suspended, closed) for a while, the daemon
// keeps the banner current by pushing updates to the activity's push token.
// The agents' names and questions are sealed with the phone's content key
// (the widget opens them); the phase and the counts stay in the clear.

// ActivityRow is one waiting agent, as the app's LiveActivityContent.Row
// encodes it.
type ActivityRow struct {
	Pane     string `json:"pane"`
	Agent    string `json:"agent"`
	Question string `json:"question"`
}

// ActivityState is the activity's content state, as the app's
// LiveActivityContent encodes it. Rows is what the daemon knows; the
// update carries them sealed (Sealed) when the phone has a content key and
// leaves them out when it has none.
type ActivityState struct {
	Phase   string        `json:"phase"`
	Muted   bool          `json:"muted"`
	Rows    []ActivityRow `json:"rows"`
	Waiting int           `json:"waiting"`
	Working int           `json:"working"`
	Done    int           `json:"done"`
	Sealed  *SealedBox    `json:"sealed,omitempty"`
}

// SealedBox is AES-256-GCM output, as notifications carry it: c is the
// ciphertext with the tag appended, n the 12-byte nonce, both standard
// base64.
type SealedBox struct {
	C string `json:"c"`
	N string `json:"n"`
}

// PhaseAway is the phase the daemon pushes: the app is not connected, so
// the voice is not listening.
const PhaseAway = "away"

// ActivityRowLimit is how many rows an update carries (the banner shows
// three at most).
const ActivityRowLimit = 3

// sealedRows is the plaintext of ActivityState.Sealed.
type sealedRows struct {
	Rows []ActivityRow `json:"rows"`
}

// Activity is a Live Activity's push token, registered by a paired
// device. One per device: a new activity replaces the old one.
type Activity struct {
	Token       string    `json:"token"`
	RelayToken  string    `json:"relay_token,omitempty"`
	Environment string    `json:"environment"`
	DeviceID    string    `json:"device_id"`
	At          time.Time `json:"at"`
}

// ActivitiesFile holds the registered activities under the data
// directory, so a daemon restart (an update while the phone is away)
// keeps updating the banner.
const ActivitiesFile = "push_activities.json"

// ActivityLifetime is how long iOS keeps an activity running: a token
// older than this is dropped.
const ActivityLifetime = 8 * time.Hour

// ErrNoDevice is a Live Activity registration without a paired device.
var ErrNoDevice = errors.New("a Live Activity needs a paired device")

// activities holds the registered activities, persisted to path (""
// keeps them in memory only). A token lives at most as long as the
// activity (ActivityLifetime).
type activities struct {
	mu   sync.Mutex
	path string
	m    map[string]Activity // by device id
}

// load reads path, dropping activities iOS has surely ended; a missing
// or damaged file is an empty set (the app registers again with its next
// activity).
func (a *activities) load(path string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.path, a.m = path, map[string]Activity{}
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var list []Activity
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, x := range list {
		if x.DeviceID != "" && now.Sub(x.At) < ActivityLifetime {
			a.m[x.DeviceID] = x
		}
	}
}

// saveLocked writes the set atomically, mode 0600.
func (a *activities) saveLocked() {
	if a.path == "" {
		return
	}
	list := make([]Activity, 0, len(a.m))
	for _, x := range a.m {
		list = append(list, x)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].DeviceID < list[j].DeviceID })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(a.path), 0o700) != nil {
		return
	}
	tmp := a.path + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o600) == nil {
		_ = os.Rename(tmp, a.path)
	}
}

// RegisterActivity stores the activity push token of a paired device's
// Live Activity.
func (s *Sender) RegisterActivity(deviceID, token, environment string) error {
	if s == nil {
		return errors.New("push not available")
	}
	if deviceID == "" {
		return ErrNoDevice
	}
	tok, err := NormalizeToken(token)
	if err != nil {
		return err
	}
	if environment != EnvProduction && environment != EnvSandbox {
		return ErrBadEnvironment
	}
	s.acts.mu.Lock()
	defer s.acts.mu.Unlock()
	if s.acts.m == nil {
		s.acts.m = map[string]Activity{}
	}
	old, had := s.acts.m[deviceID]
	if had && old.Token == tok && old.Environment == environment {
		return nil
	}
	s.acts.m[deviceID] = Activity{Token: tok, Environment: environment, DeviceID: deviceID, At: s.cfg.Now()}
	s.acts.saveLocked()
	s.log.Log(logSource, "push.activity.registered", "", map[string]any{"token_tail": tokenTail(tok), "environment": environment})
	return nil
}

// ForgetActivity drops a device's activity (it ended).
func (s *Sender) ForgetActivity(deviceID string) {
	if s == nil {
		return
	}
	s.acts.mu.Lock()
	defer s.acts.mu.Unlock()
	if _, ok := s.acts.m[deviceID]; ok {
		delete(s.acts.m, deviceID)
		s.acts.saveLocked()
	}
}

// Activities lists the registered activities.
func (s *Sender) Activities() []Activity {
	if s == nil {
		return nil
	}
	s.acts.mu.Lock()
	defer s.acts.mu.Unlock()
	out := make([]Activity, 0, len(s.acts.m))
	for _, a := range s.acts.m {
		out = append(out, a)
	}
	return out
}

// ActivityUpdate is one push to an activity.
type ActivityUpdate struct {
	State ActivityState
	// End ends the activity (event "end"), dismissing it at Dismissal.
	End       bool
	Dismissal time.Time
	// Stale is when the banner dims as out of date.
	Stale time.Time
	// Urgent sends at priority 10 (a new question); otherwise 5, which
	// iOS does not count against the activity's update budget.
	Urgent bool
}

// UpdateActivity pushes u to the device's activity, now (the caller runs
// it off the hot path). A token APNs says is gone is forgotten.
func (s *Sender) UpdateActivity(ctx context.Context, deviceID string, u ActivityUpdate) error {
	if !s.Enabled() {
		return errors.New("push not enabled")
	}
	s.acts.mu.Lock()
	a, ok := s.acts.m[deviceID]
	s.acts.mu.Unlock()
	if !ok {
		return errors.New("no Live Activity registered")
	}
	state, err := s.sealActivity(deviceID, u.State)
	if err != nil {
		return err
	}
	now := s.cfg.Now()
	body := activityBody{Event: "update", ContentState: state, Timestamp: now.Unix(), Priority: 5}
	if u.End {
		body.Event = "end"
		if !u.Dismissal.IsZero() {
			body.DismissalDate = u.Dismissal.Unix()
		}
	}
	if !u.Stale.IsZero() {
		body.StaleDate = u.Stale.Unix()
	}
	if u.Urgent {
		body.Priority = 10
	}
	p := map[string]any{"mode": s.cfg.Mode, "token_tail": tokenTail(a.Token), "event": body.Event, "priority": body.Priority, "waiting": state.Waiting}
	var status int
	if s.cfg.Mode == ModeRelay {
		status, err = s.activityRelay(ctx, &a, body)
	} else {
		status, err = s.activityAPNs(ctx, a, body)
	}
	if err != nil {
		p["reason"] = err.Error()
		if status != 0 {
			p["status"] = status
		}
		s.log.Log(logSource, "push.activity.failed", "", p)
		if status == http.StatusGone {
			s.ForgetActivity(deviceID)
		}
		return err
	}
	s.log.Log(logSource, "push.activity.sent", "", p)
	if u.End {
		s.ForgetActivity(deviceID)
	}
	return nil
}

// sealActivity moves the rows into Sealed under the device's content key,
// or drops them when the device has none: names and questions never go
// out in the clear.
func (s *Sender) sealActivity(deviceID string, st ActivityState) (ActivityState, error) {
	rows := st.Rows
	if len(rows) > ActivityRowLimit {
		rows = rows[:ActivityRowLimit]
	}
	st.Rows = []ActivityRow{}
	st.Sealed = nil
	if len(rows) == 0 {
		return st, nil
	}
	key := ""
	for _, d := range s.devs.list() {
		if d.DeviceID == deviceID && d.ContentKey != "" {
			key = d.ContentKey
		}
	}
	if key == "" {
		return st, nil
	}
	plain, err := json.Marshal(sealedRows{Rows: rows})
	if err != nil {
		return st, err
	}
	ct, nonce, err := Seal(key, plain)
	if err != nil {
		return st, err
	}
	st.Sealed = &SealedBox{C: base64.StdEncoding.EncodeToString(ct), N: base64.StdEncoding.EncodeToString(nonce)}
	return st, nil
}

// activityBody is the relay's POST /v1/activity body.
type activityBody struct {
	RelayToken    string        `json:"relay_token"`
	Event         string        `json:"event"`
	ContentState  ActivityState `json:"content_state"`
	Timestamp     int64         `json:"timestamp"`
	StaleDate     int64         `json:"stale_date,omitempty"`
	DismissalDate int64         `json:"dismissal_date,omitempty"`
	Priority      int           `json:"priority"`
}

// activityRelay sends through the relay, getting a relay token for the
// activity token first (the relay seals it like a device token).
func (s *Sender) activityRelay(ctx context.Context, a *Activity, body activityBody) (int, error) {
	if a.RelayToken == "" {
		rt, err := s.relay.register(ctx, a.Token, a.Environment, s.cfg.BundleID)
		if err != nil {
			var re *relayError
			if errors.As(err, &re) {
				return re.status, err
			}
			return 0, err
		}
		a.RelayToken = rt
		s.acts.mu.Lock()
		if cur, ok := s.acts.m[a.DeviceID]; ok && cur.Token == a.Token {
			cur.RelayToken = rt
			s.acts.m[a.DeviceID] = cur
			s.acts.saveLocked()
		}
		s.acts.mu.Unlock()
	}
	body.RelayToken = a.RelayToken
	_, err := s.relay.do(ctx, "/v1/activity", body)
	var re *relayError
	if errors.As(err, &re) {
		return re.status, err
	}
	return 0, err
}

// activityAPNs sends straight to APNs (mode apns).
func (s *Sender) activityAPNs(ctx context.Context, a Activity, body activityBody) (int, error) {
	aps := map[string]any{"timestamp": body.Timestamp, "event": body.Event, "content-state": body.ContentState}
	if body.StaleDate != 0 {
		aps["stale-date"] = body.StaleDate
	}
	if body.DismissalDate != 0 {
		aps["dismissal-date"] = body.DismissalDate
	}
	payload, err := json.Marshal(map[string]any{"aps": aps})
	if err != nil {
		return 0, err
	}
	base := s.baseURL[a.Environment]
	if base == "" {
		base = s.cfg.ProductionURL
	}
	for fresh := false; ; fresh = true {
		jwt, err := s.token(fresh)
		if err != nil {
			return 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+a.Token, bytes.NewReader(payload))
		if err != nil {
			return 0, err
		}
		req.Header.Set("authorization", "bearer "+jwt)
		req.Header.Set("apns-topic", s.cfg.Topic+".push-type.liveactivity")
		req.Header.Set("apns-push-type", "liveactivity")
		req.Header.Set("apns-priority", fmt.Sprint(body.Priority))
		req.Header.Set("content-type", "application/json")
		resp, err := s.client.Do(req)
		if err != nil {
			return 0, fmt.Errorf("apns: %w", err)
		}
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return 0, nil
		}
		var r struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(rb, &r)
		if resp.StatusCode == http.StatusForbidden && r.Reason == "ExpiredProviderToken" && !fresh {
			continue
		}
		return resp.StatusCode, fmt.Errorf("apns: HTTP %d: %s", resp.StatusCode, r.Reason)
	}
}
