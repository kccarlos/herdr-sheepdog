package gptlive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
)

// The /debug Context tab's record of this session (docs/architecture.md
// "Context capture"). A GPT-Live session keeps its context on the server
// and it cannot be read back, so it is reconstructed from what the daemon
// sent: every session is a fresh context made of session.start's
// instructions and input items (the recap and the silent context held
// in standby), then every client event sent on it, and what the model
// answered (its transcripts and delegations).

// recConn records one GPT-Live WebSocket's traffic.
type recConn struct {
	Conn
	rec *ctxcap.VoiceSession
	mu  sync.Mutex
	// links are the link ids of the next push (set right before it is
	// sent by deliverPush).
	links []string
}

// recordConn wraps conn (nil recorder: conn as is).
func (s *Session) recordConn(conn Conn) Conn {
	if s.rec == nil {
		return conn
	}
	return &recConn{Conn: conn, rec: s.rec}
}

// setPushLinks sets the link ids of the next push sent on lc.
func setPushLinks(lc *liveConn, links []string) {
	if rc, ok := lc.conn.(*recConn); ok {
		rc.mu.Lock()
		rc.links = links
		rc.mu.Unlock()
	}
}

func (c *recConn) Send(ctx context.Context, ev any) error {
	if err := c.Conn.Send(ctx, ev); err != nil {
		return err
	}
	switch e := ev.(type) {
	case startEvent:
		c.start(e)
	case audioAppend:
		c.rec.Audio(base64.StdEncoding.DecodedLen(len(e.Audio)), SampleRate)
	case appendEvent:
		c.appended(e)
	case closeEvent:
		c.rec.Marker("session close sent", "session.close: the daemon asked the server to end this session.", ctxcap.Raw(e))
	}
	return nil
}

// start records session.start: a new context with its instructions, the
// delegation setting (GPT-Live declares no functions: the model hands
// requests to the client) and its input items.
func (c *recConn) start(e startEvent) {
	cfg := e.Session
	c.rec.Epoch("session open (fresh)", fmt.Sprintf("session.start on %s: every GPT-Live session starts a new context; what it knows of the conversation is the input items below.", cfg.Model), ctxcap.Raw(e))
	c.rec.Instructions(cfg.Instructions, ctxcap.Raw(map[string]any{"instructions": cfg.Instructions}))
	c.rec.Tools("delegation: "+cfg.Delegation.Type+" (no function declarations: the model delegates a request to the client, which relays the shepherd's words to the supervisor)\n", ctxcap.Raw(map[string]any{"delegation": cfg.Delegation}))
	for i, it := range cfg.Input {
		var text []string
		for _, p := range it.Content {
			text = append(text, p.Text)
		}
		label, class := "recap: "+it.Role, ctxcap.ClassShepherd
		switch it.Role {
		case "assistant":
			class = ctxcap.ClassVoice
		case "developer":
			label, class = "context (developer)", ctxcap.ClassSystem
			if strings.HasPrefix(strings.Join(text, ""), "Background from the supervisor") {
				label, class = "held silent context", ctxcap.ClassPush
			}
		}
		c.rec.Item(ctxcap.VoiceItem{Kind: ctxcap.VKindContext, Class: class, Role: it.Role, Label: label, Text: strings.Join(text, "\n"), Raw: ctxcap.Raw(map[string]any{"input_index": i, "item": it})})
	}
}

// appended records a session.*.append: an answer to a delegation, or a
// push.
func (c *recConn) appended(e appendEvent) {
	how := strings.TrimSuffix(strings.TrimPrefix(e.Type, "session."), ".append")
	if e.DelegationID != nil {
		link := ctxcap.CallLink(*e.DelegationID)
		c.rec.Item(ctxcap.VoiceItem{Kind: ctxcap.VKindToolResponse, Class: ctxcap.ClassToolResult, Role: "client", Label: "delegation result (" + how + ")",
			Text: "← " + e.Content, Links: []string{link}, Raw: ctxcap.Raw(e)})
		return
	}
	c.mu.Lock()
	links := c.links
	c.links = nil
	c.mu.Unlock()
	it := ctxcap.VoiceItem{Kind: ctxcap.VKindPush, Class: ctxcap.ClassPush, Role: "client", Label: "push (" + how + ")", Text: e.Content, Links: links, Raw: ctxcap.Raw(e)}
	if how == "thinking" {
		it.Kind, it.Label = ctxcap.VKindContext, "silent context (thinking)"
	}
	c.rec.Item(it)
}

func (c *recConn) Receive(ctx context.Context) ([]byte, error) {
	data, err := c.Conn.Receive(ctx)
	if err == nil {
		c.observe(data)
	}
	return data, err
}

// observe records what the model produced: what it heard, what it said,
// the delegations it made.
func (c *recConn) observe(data []byte) {
	var ev struct {
		Type       string `json:"type"`
		Delta      string `json:"delta"`
		Reason     string `json:"reason"`
		Delegation *struct {
			ID     string `json:"id"`
			Target string `json:"target"`
		} `json:"delegation"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	switch ev.Type {
	case evInputTranscript:
		c.rec.Heard(ev.Delta, false)
	case evOutputTranscript:
		c.rec.Said(ev.Delta)
	case evDelegationCreated:
		if ev.Delegation == nil {
			return
		}
		link := ctxcap.CallLink(ev.Delegation.ID)
		c.rec.Claim(link)
		c.rec.Item(ctxcap.VoiceItem{Kind: ctxcap.VKindToolCall, Class: ctxcap.ClassToolCall, Role: "model", Label: "delegation to " + ev.Delegation.Target,
			Text: "→ delegation " + ev.Delegation.ID + " (the client relays what the shepherd said to the supervisor)", Links: []string{link}, Raw: json.RawMessage(data)})
	case evSessionClosed:
		c.rec.Marker("session closed", "session.closed ("+ev.Reason+").", json.RawMessage(data))
	}
}
