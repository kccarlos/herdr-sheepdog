package config

import (
	"strings"
)

// Push configures phone notifications (docs/dev/push.md).
type Push struct {
	// Mode is how notifications reach APNs: "relay" (sheepdog's shared
	// relay, or your own: [push.relay]), "apns" (directly, with your own
	// key: [push.apns]) or "off". Empty picks: apns when [push.apns] is
	// fully set, else relay (EffectiveMode).
	Mode  string    `toml:"mode"`
	Relay PushRelay `toml:"relay"`
	APNs  APNs      `toml:"apns"`
}

// The push.mode values.
const (
	PushModeRelay = "relay"
	PushModeAPNs  = "apns"
	PushModeOff   = "off"
)

// PushModes are the valid push.mode values ("" is also accepted: auto).
var PushModes = []string{PushModeRelay, PushModeAPNs, PushModeOff}

// DefaultRelayURL is sheepdog's shared push relay (relay/README.md).
const DefaultRelayURL = "https://sheepdog-relay.kccarlos-an.workers.dev"

// PushRelay configures the push relay.
type PushRelay struct {
	// URL is the relay's base URL (default DefaultRelayURL).
	URL string `toml:"url"`
}

// Complete reports whether all four [push.apns] values are set.
func (a APNs) Complete() bool {
	return strings.TrimSpace(a.KeyPath) != "" && strings.TrimSpace(a.KeyID) != "" && strings.TrimSpace(a.TeamID) != "" && strings.TrimSpace(a.Topic) != ""
}

// EffectiveMode is push.mode, or with it unset: apns when [push.apns] is
// fully configured (a self-built app with its own key keeps working
// unchanged), else relay.
func (p Push) EffectiveMode() string {
	if p.Mode != "" {
		return p.Mode
	}
	if p.APNs.Complete() {
		return PushModeAPNs
	}
	return PushModeRelay
}

// APNs configures the Apple Push Notification service sender for the
// iOS companion app. All four are needed; any one empty leaves sending
// disabled (push.disabled at startup says which). The key file's
// contents are never logged: config show prints key_path as set/unset.
type APNs struct {
	// KeyPath is the .p8 auth key (ES256) from the Apple Developer
	// account.
	KeyPath string `toml:"key_path"`
	// KeyID is the auth key's 10-character id.
	KeyID string `toml:"key_id"`
	// TeamID is the Apple Developer team id the key belongs to.
	TeamID string `toml:"team_id"`
	// Topic is the iOS app's bundle id.
	Topic string `toml:"topic"`
}
