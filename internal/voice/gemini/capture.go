package gemini

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The /debug Context tab's record of this session (docs/architecture.md
// "Context capture"). Gemini Live keeps the conversation on the server
// and it cannot be read back, so the voice context is reconstructed from
// what the daemon sent: every connection is wrapped in a recConn that
// records each message sent, in wire order (every Send* runs under
// sendMu), and what the model answered (its transcripts and tool calls).

// recConn records one Live connection's traffic into the session's
// voice context log.
type recConn struct {
	Conn
	rec *ctxcap.VoiceSession
	// links are the link ids of the next client content (a push's tags),
	// set under sendMu right before it is sent.
	links []string
}

// record wraps conn, first recording the connection: a fresh one starts
// a new context (its system instructions and tools), a resumed one
// continues the last.
func (s *Session) record(conn Conn, resumed bool, prompt string, cfg *genai.LiveConnectConfig) Conn {
	if s.rec == nil {
		return conn
	}
	if resumed {
		s.rec.Marker("session resumed", "A new Live connection resumed the conversation with a resumption handle: the context continues on the server (its sliding-window compression is not visible here).", ctxcap.Raw(map[string]any{"setup": cfg}))
	} else {
		s.rec.Epoch("session open (fresh)", fmt.Sprintf("A new Live connection to %s without a resumption handle: the model starts with no memory of earlier turns.", s.cfg.Model), ctxcap.Raw(map[string]any{"setup": cfg}))
		s.rec.Instructions(prompt, ctxcap.Raw(map[string]any{"systemInstruction": cfg.SystemInstruction}))
		s.rec.Tools(toolLines(cfg.Tools), ctxcap.Raw(map[string]any{"tools": cfg.Tools}))
	}
	return &recConn{Conn: conn, rec: s.rec}
}

// toolLines renders the declarations one per line.
func toolLines(tools []*genai.Tool) string {
	var b strings.Builder
	for _, t := range tools {
		if t == nil {
			continue
		}
		for _, d := range t.FunctionDeclarations {
			if d == nil {
				continue
			}
			var params []string
			if d.Parameters != nil {
				for name := range d.Parameters.Properties {
					params = append(params, name)
				}
			}
			sort.Strings(params)
			fmt.Fprintf(&b, "%s(%s) [%s] — %s\n", d.Name, strings.Join(params, ", "), strings.ToLower(string(d.Behavior)), d.Description)
		}
	}
	return b.String()
}

// setLinks sets the link ids of the next client content (under sendMu).
func setLinks(c Conn, links []string) {
	if rc, ok := c.(*recConn); ok {
		rc.links = links
	}
}

func (c *recConn) SendRealtimeInput(in genai.LiveRealtimeInput) error {
	if err := c.Conn.SendRealtimeInput(in); err != nil {
		return err
	}
	switch {
	case in.Audio != nil:
		c.rec.Audio(len(in.Audio.Data), 16000)
	case in.AudioStreamEnd:
		c.rec.AudioEnd()
	case in.Text != "":
		c.rec.Item(ctxcap.VoiceItem{Kind: ctxcap.VKindUserText, Class: ctxcap.ClassShepherd, Role: "user", Label: "shepherd typed (realtime text)", Text: in.Text,
			Raw: ctxcap.Raw(map[string]any{"realtimeInput": map[string]any{"text": in.Text}})})
	}
	return nil
}

func (c *recConn) SendClientContent(in genai.LiveClientContentInput) error {
	links := c.links
	c.links = nil
	if err := c.Conn.SendClientContent(in); err != nil {
		return err
	}
	var text []string
	for _, t := range in.Turns {
		if t == nil {
			continue
		}
		for _, p := range t.Parts {
			if p != nil && p.Text != "" {
				text = append(text, p.Text)
			}
		}
	}
	it := ctxcap.VoiceItem{Kind: ctxcap.VKindPush, Class: ctxcap.ClassPush, Role: "user", Label: "push (client content, turn complete)", Text: strings.Join(text, "\n"), Links: links,
		Raw: ctxcap.Raw(map[string]any{"clientContent": in})}
	if in.TurnComplete != nil && !*in.TurnComplete {
		it.Kind, it.Label = ctxcap.VKindContext, "silent context (client content, no turn)"
	}
	c.rec.Item(it)
	return nil
}

func (c *recConn) SendToolResponse(in genai.LiveToolResponseInput) error {
	if err := c.Conn.SendToolResponse(in); err != nil {
		return err
	}
	for _, fr := range in.FunctionResponses {
		if fr == nil {
			continue
		}
		body, _ := json.Marshal(fr.Response)
		sched := strings.ToLower(string(fr.Scheduling))
		c.rec.Item(ctxcap.VoiceItem{Kind: ctxcap.VKindToolResponse, Class: ctxcap.ClassToolResult, Role: "user", Label: "tool response " + fr.Name + " (" + sched + ")",
			Text: "← " + fr.Name + ": " + string(body), Links: linkOf(fr.ID), Raw: ctxcap.Raw(map[string]any{"toolResponse": map[string]any{"functionResponses": []*genai.FunctionResponse{fr}}})})
	}
	return nil
}

func (c *recConn) Receive() (*genai.LiveServerMessage, error) {
	msg, err := c.Conn.Receive()
	if err == nil && msg != nil {
		c.observe(msg)
	}
	return msg, err
}

// observe records what the model produced that stays in its context:
// what it heard, what it said, the tools it called.
func (c *recConn) observe(msg *genai.LiveServerMessage) {
	if tc := msg.ToolCall; tc != nil {
		for _, fc := range tc.FunctionCalls {
			if fc == nil {
				continue
			}
			args, _ := json.Marshal(fc.Args)
			if len(fc.Args) == 0 {
				args = []byte("{}")
			}
			link := ctxcap.CallLink(fc.ID)
			if fc.Name == voice.ToolToSupervisor {
				c.rec.Claim(link)
			}
			c.rec.Item(ctxcap.VoiceItem{Kind: ctxcap.VKindToolCall, Class: ctxcap.ClassToolCall, Role: "model", Label: "tool call " + fc.Name,
				Text: "→ " + fc.Name + "(" + string(args) + ")", Links: linkOf(fc.ID), Raw: ctxcap.Raw(map[string]any{"toolCall": map[string]any{"functionCalls": []*genai.FunctionCall{fc}}})})
		}
	}
	if cc := msg.ToolCallCancellation; cc != nil {
		c.rec.Marker("tool calls cancelled", "The model cancelled "+strings.Join(cc.IDs, ", ")+" (the shepherd barged in).", ctxcap.Raw(map[string]any{"toolCallCancellation": cc}))
	}
	if sc := msg.ServerContent; sc != nil {
		if t := sc.InputTranscription; t != nil && (t.Text != "" || t.Finished) {
			c.rec.Heard(t.Text, t.Finished)
		}
		if t := sc.OutputTranscription; t != nil && t.Text != "" {
			c.rec.Said(t.Text)
		}
		if sc.TurnComplete {
			c.rec.ModelDone()
		}
	}
}

func linkOf(callID string) []string {
	if l := ctxcap.CallLink(callID); l != "" {
		return []string{l}
	}
	return nil
}
