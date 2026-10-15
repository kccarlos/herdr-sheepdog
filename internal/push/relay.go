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
	"strings"
)

// DefaultBundleID is the App Store build's bundle id: the bundle_id the
// relay path registers device tokens under when [push.apns] names no
// topic.
const DefaultBundleID = "com.github.kccarlos.herdr-sheepdog-ios"

// relayPlaceholder marks the DefaultRelayURL constant before the shared
// relay's first deploy fills it in.
const relayPlaceholder = ".REPLACE."

// relayClient speaks the relay's API (relay/README.md).
type relayClient struct {
	base   string // no trailing slash
	client *http.Client
}

// relayAnswer is a relay's JSON answer: a relay token, or an error and
// APNs' reason.
type relayAnswer struct {
	RelayToken string `json:"relay_token"`
	Error      string `json:"error"`
	Reason     string `json:"reason"`
	Status     int    `json:"status"`
}

// relayError is a relay answer other than 200.
type relayError struct {
	status int
	reason string // APNs' reason, else the relay's error
}

func (e *relayError) Error() string {
	return fmt.Sprintf("relay: HTTP %d: %s", e.status, e.reason)
}

// do posts body to path and decodes the answer; a non-200 answer is a
// *relayError.
func (c relayClient) do(ctx context.Context, path string, body any) (relayAnswer, error) {
	var a relayAnswer
	b, err := json.Marshal(body)
	if err != nil {
		return a, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return a, err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return a, fmt.Errorf("relay: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(rb, &a)
	if resp.StatusCode != http.StatusOK {
		reason := a.Reason
		if reason == "" {
			reason = a.Error
		}
		if a.Status != 0 {
			reason = fmt.Sprintf("%s (APNs %d)", reason, a.Status)
		}
		return a, &relayError{status: resp.StatusCode, reason: reason}
	}
	return a, nil
}

// register trades a device token for a relay token.
func (c relayClient) register(ctx context.Context, token, environment, bundleID string) (string, error) {
	a, err := c.do(ctx, "/v1/register", map[string]string{"device_token": token, "environment": environment, "bundle_id": bundleID})
	if err != nil {
		return "", err
	}
	if _, err := checkRelayToken(a.RelayToken); err != nil {
		return "", errors.New("relay: register answered no relay_token")
	}
	return a.RelayToken, nil
}

// relaySend is POST /v1/send's body.
type relaySend struct {
	RelayToken string           `json:"relay_token"`
	Payload    relaySendPayload `json:"payload"`
	Priority   int              `json:"priority"`
}

type relaySendPayload struct {
	Ciphertext string `json:"ciphertext"`
	Nonce      string `json:"nonce"`
	Kind       string `json:"kind,omitempty"`
	CollapseID string `json:"collapse_id,omitempty"`
}

// send posts one sealed notification.
func (c relayClient) send(ctx context.Context, relayToken string, ciphertext, nonce []byte, n Notification) error {
	_, err := c.do(ctx, "/v1/send", relaySend{
		RelayToken: relayToken,
		Payload: relaySendPayload{
			Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
			Nonce:      base64.StdEncoding.EncodeToString(nonce),
			Kind:       relayKind(n.Kind),
			CollapseID: n.CollapseID(),
		},
		Priority: 10,
	})
	return err
}

// relayKind is the kind the relay sees (it picks the category and
// interruption level from it): a short lowercase word, else none.
func relayKind(k string) string {
	if k == "" || len(k) > 32 {
		return ""
	}
	for _, c := range k {
		if (c < 'a' || c > 'z') && c != '_' {
			return ""
		}
	}
	return k
}

// Health asks a relay's GET /v1/health (sheepdog doctor): nil when it
// answers ok, else why not.
func Health(ctx context.Context, client *http.Client, base string) (string, error) {
	base = strings.TrimRight(base, "/")
	if strings.Contains(base, relayPlaceholder) {
		return "", errors.New("the shared relay is not deployed yet (push.relay.url is a placeholder)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/health", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var h struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&h); err != nil || resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d, not a sheepdog relay", resp.StatusCode)
	}
	if !h.OK {
		return h.Version, errors.New("the relay answers but is missing its Apple key or secret")
	}
	return h.Version, nil
}

// sendRelay seals n with each device's content key and posts it through
// the relay. A device the app registered with only its device token gets
// a relay token from the relay first; a relay token the relay no longer
// accepts (401: another relay, or a rotated secret) is renewed once from
// the device token. A gone device (410) is removed, as on the direct path.
func (s *Sender) sendRelay(ctx context.Context, n Notification, devs []Device) {
	plain, err := n.SealedPayload()
	if err != nil {
		s.log.Log(logSource, "push.failed", n.PaneID, map[string]any{"mode": ModeRelay, "reason": err.Error(), "kind": n.Kind})
		return
	}
	for _, d := range devs {
		p := map[string]any{"mode": ModeRelay, "kind": n.Kind, "token_tail": d.tail(), "environment": d.Environment, "collapse_id": n.CollapseID()}
		if n.EventID != "" {
			p["event_id"] = n.EventID
		}
		if n.PendingID != "" {
			p["pending_id"] = n.PendingID
		}
		err := s.relayOne(ctx, &d, plain, n)
		var re *relayError
		switch {
		case err == nil:
			s.log.Log(logSource, "push.sent", n.PaneID, p)
		case errors.As(err, &re):
			p["status"], p["reason"] = re.status, re.reason
			s.log.Log(logSource, "push.failed", n.PaneID, p)
			if re.status == http.StatusGone || (re.status == http.StatusUnauthorized && d.Token == "") {
				s.drop(d, re.reason, re.status)
			}
		default:
			p["reason"] = err.Error()
			s.log.Log(logSource, "push.failed", n.PaneID, p)
		}
	}
}

// relayOne sends to one device, getting or renewing its relay token as
// needed. d is updated with what was stored.
func (s *Sender) relayOne(ctx context.Context, d *Device, plain []byte, n Notification) error {
	if d.ContentKey == "" {
		// A device registered before end-to-end keys: give it one now;
		// the app learns it at its next registration (every launch).
		k, err := NewContentKey()
		if err != nil {
			return err
		}
		if err := s.devs.update(d.id(), func(x *Device) {
			if x.ContentKey == "" {
				x.ContentKey = k
			}
			k = x.ContentKey
		}); err != nil {
			return err
		}
		d.ContentKey = k
	}
	renewed := false
	if d.RelayToken == "" {
		if err := s.renewRelayToken(ctx, d); err != nil {
			return err
		}
		renewed = true
	}
	ct, nonce, err := Seal(d.ContentKey, plain)
	if err != nil {
		return err
	}
	err = s.relay.send(ctx, d.RelayToken, ct, nonce, n)
	var re *relayError
	if errors.As(err, &re) && re.status == http.StatusUnauthorized && d.Token != "" && !renewed {
		if rerr := s.renewRelayToken(ctx, d); rerr != nil {
			return rerr
		}
		err = s.relay.send(ctx, d.RelayToken, ct, nonce, n)
	}
	return err
}

// renewRelayToken asks the relay for a relay token for d's device token
// and stores it.
func (s *Sender) renewRelayToken(ctx context.Context, d *Device) error {
	if d.Token == "" {
		return errors.New("no device token to register with the relay")
	}
	rt, err := s.relay.register(ctx, d.Token, d.Environment, s.cfg.BundleID)
	if err != nil {
		return err
	}
	if err := s.devs.update(d.id(), func(x *Device) { x.RelayToken = rt }); err != nil {
		return err
	}
	d.RelayToken = rt
	return nil
}
