package web

import (
	"fmt"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// The /ws protocol handshake (docs/dev/ws-contract.md "Protocol version and
// handshake"). A protocol-aware client sends client-ready as its first
// frame and the daemon answers bot-ready. A client whose first frame is
// anything else, or that sends nothing for Deps.HandshakeTimeout, is a
// legacy protocol-1 client (the iOS app 0.1.0, the /voice page before
// the handshake, voice-test): it is served exactly as before and never
// sees a bot-ready, which it could not parse.

// handshake is a client's handshake state, under client.mu.
type handshake struct {
	// resolved: the client's protocol is known (a client-ready arrived,
	// or it was taken for legacy).
	resolved bool
	protocol int
	legacy   bool
	timer    *time.Timer
}

// features lists the optional parts of the contract this daemon serves
// right now (bot-ready "features").
func (s *Server) features() []string {
	f := []string{wsproto.FeaturePlayback, wsproto.FeatureSpeaking, wsproto.FeatureSilence}
	if s.deps.VoiceSwitch != nil {
		f = append(f, wsproto.FeatureProvider)
	}
	f = append(f, wsproto.FeatureHistory)
	if s.deps.Push != nil {
		f = append(f, wsproto.FeaturePush)
	}
	if s.deps.Brain != nil {
		f = append(f, wsproto.FeatureModels)
	}
	if s.deps.Supervisor != nil {
		f = append(f, wsproto.FeatureScreen)
	}
	if s.term.available() {
		f = append(f, wsproto.FeatureTerm)
	}
	return append(f, wsproto.FeatureFeedback)
}

// startHandshake arms the legacy timer for a new client.
func (c *client) startHandshake(timeout time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hs.timer = time.AfterFunc(timeout, func() { _ = c.legacyIfUnresolved("timeout") })
}

// stopHandshake disarms the legacy timer (the connection ended).
func (c *client) stopHandshake() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hs.timer != nil {
		c.hs.timer.Stop()
	}
}

// legacyIfUnresolved takes a client that has not sent client-ready for a
// legacy protocol-1 client: why is "timeout" (nothing within
// HandshakeTimeout) or "first_frame" (it sent something else first). It
// reports false when the client was refused just now (the frame that
// prompted it must not be handled).
func (c *client) legacyIfUnresolved(why string) bool {
	c.mu.Lock()
	if c.hs.resolved {
		c.mu.Unlock()
		return true
	}
	c.hs.resolved, c.hs.legacy, c.hs.protocol = true, true, wsproto.LegacyProtocol
	if c.hs.timer != nil {
		c.hs.timer.Stop()
	}
	closed := c.closing
	c.mu.Unlock()
	if closed {
		return false
	}
	ok := c.checkProtocol(wsproto.LegacyProtocol)
	c.h.s.log.Log(logSource, "web.client.hello", "", map[string]any{
		"client_id": c.id, "protocol": wsproto.LegacyProtocol, "client": nil, "legacy": true, "why": why, "accepted": ok,
	})
	if ok {
		c.attachIfPending()
	}
	return ok
}

// onClientReady answers a client-ready: bot-ready when its protocol is
// served, otherwise the rejection. A client-ready after the client was
// taken for legacy (late, past the timeout) is still answered; the log
// line says late.
func (c *client) onClientReady(m wsproto.Message) {
	c.mu.Lock()
	late := c.hs.resolved
	c.hs.resolved, c.hs.legacy, c.hs.protocol = true, false, *m.Protocol
	if c.hs.timer != nil {
		c.hs.timer.Stop()
	}
	c.mu.Unlock()
	var info any
	if m.Client != nil {
		info = map[string]any{"name": m.Client.Name, "version": m.Client.Version, "platform": m.Client.Platform}
	}
	ok := c.checkProtocol(*m.Protocol)
	payload := map[string]any{"client_id": c.id, "protocol": *m.Protocol, "client": info, "legacy": false, "accepted": ok}
	if late {
		payload["late"] = true
	}
	c.h.s.log.Log(logSource, "web.client.hello", "", payload)
	if !ok {
		return
	}
	c.attachIfPending()
	// The daemon speaks its own protocol; a newer client steps down to
	// it (or closes, if it cannot).
	c.enqueue(controlFrame(wsproto.BotReady(wsproto.ProtocolVersion, c.h.s.deps.MinProtocol, c.h.s.deps.Version, c.h.s.features())))
}

// attachIfPending attaches a client that connected while another was
// attached, now that its handshake is accepted: only then does it replace
// that one. It is sent a fresh status if the hub's differs from the one
// it got on connect.
func (c *client) attachIfPending() {
	c.mu.Lock()
	pending := c.pendingAttach
	c.pendingAttach = false
	first := c.firstStatus
	c.mu.Unlock()
	if !pending {
		return
	}
	replaced := c.h.attach(c)
	c.h.s.log.Log(logSource, "web.client.attach", "", map[string]any{"client_id": c.id, "replaced": replaced})
	if now := controlFrame(c.h.statusMsg()); string(now.data) != string(first) {
		c.enqueue(now)
	}
	c.h.sendAccounts(c)
}

// checkProtocol refuses a client below the daemon's minimum: an error a
// person can read (an old app shows it as is), a closed status with
// reason protocol_too_old, and a close with CloseProtocolUnsupported.
// It reports whether the protocol is served.
func (c *client) checkProtocol(protocol int) bool {
	min := c.h.s.deps.MinProtocol
	if protocol >= min {
		return true
	}
	c.enqueue(controlFrame(wsproto.Error(fmt.Sprintf(
		"This app speaks /ws protocol %d; the daemon (version %s) needs protocol %d or newer. Update the app.",
		protocol, c.h.s.deps.Version, min))))
	c.enqueue(controlFrame(c.h.closedStatus(wsproto.ReasonProtocolTooOld)))
	c.closeAfterFlush(websocket.StatusCode(wsproto.CloseProtocolUnsupported),
		fmt.Sprintf("update the app: /ws protocol %d < %d", protocol, min))
	return false
}
