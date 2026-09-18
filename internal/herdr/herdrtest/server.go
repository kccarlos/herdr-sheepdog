// Package herdrtest is a fake herdr server for tests. It speaks the
// documented NDJSON protocol on a temporary unix socket: one request per
// connection (reply one line, then close) except events.subscribe, which
// stays open and streams events matching the connection's subscriptions.
//
// It implements the methods internal/herdr uses (ping, agent.list,
// agent.get, pane.read, agent.read, agent.prompt, pane.send_keys,
// agent.send_keys, session.snapshot, events.subscribe) over an in-memory
// pane/agent table that tests mutate through the Set*/Remove* methods, and
// it records every request so tests can assert on the wire.
package herdrtest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Request is one recorded client request.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type subscription struct {
	Type   string `json:"type"`
	PaneID string `json:"pane_id"`
}

type subConn struct {
	conn net.Conn
	subs []subscription
	mu   sync.Mutex
}

// Server is a fake herdr server. All methods are safe for concurrent use.
type Server struct {
	// Path is the unix socket path to hand to herdr.NewClient.
	Path string

	ln   net.Listener
	dir  string
	wg   sync.WaitGroup
	quit chan struct{}

	mu         sync.Mutex
	version    string
	workspaces map[string]herdr.WorkspaceInfo
	panes      map[string]herdr.PaneInfo
	agents     map[string]herdr.AgentInfo
	screens    map[string]string
	requests   []Request
	subs       map[*subConn]struct{}
	// Handlers lets a test override or add a method. The returned error may
	// be *herdr.Error to produce a protocol error.
	handlers map[string]Handler
}

// Handler serves one method. Return a *herdr.Error to send an error reply.
type Handler func(params json.RawMessage) (result any, err error)

// New starts a server on a fresh socket under a short temp dir (macOS
// limits sun_path to 104 bytes) and stops it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "hdt")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Path:       filepath.Join(dir, "h.sock"),
		dir:        dir,
		quit:       make(chan struct{}),
		version:    "0.9.0",
		workspaces: map[string]herdr.WorkspaceInfo{},
		panes:      map[string]herdr.PaneInfo{},
		agents:     map[string]herdr.AgentInfo{},
		screens:    map[string]string{},
		subs:       map[*subConn]struct{}{},
		handlers:   map[string]Handler{},
	}
	s.ln, err = net.Listen("unix", s.Path)
	if err != nil {
		t.Fatal(err)
	}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(s.Close)
	return s
}

// Close stops the listener, drops every connection and removes the socket.
func (s *Server) Close() {
	select {
	case <-s.quit:
		return
	default:
		close(s.quit)
	}
	_ = s.ln.Close()
	s.CloseSubscriptions()
	s.wg.Wait()
	_ = os.RemoveAll(s.dir)
}

// SetWorkspace adds or replaces a workspace.
func (s *Server) SetWorkspace(w herdr.WorkspaceInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaces[w.WorkspaceID] = w
}

// SetPane adds or replaces a pane without an agent.
func (s *Server) SetPane(p herdr.PaneInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Status == "" {
		p.Status = herdr.StateUnknown
	}
	s.panes[p.PaneID] = p
}

// SetAgent adds or replaces an agent (and its pane and workspace if they
// are not known yet).
func (s *Server) SetAgent(a herdr.AgentInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agents[a.PaneID] = a
	p := s.panes[a.PaneID]
	p.PaneID, p.WorkspaceID, p.TabID, p.Status, p.Agent = a.PaneID, a.WorkspaceID, a.TabID, a.Status, a.Agent
	s.panes[a.PaneID] = p
	if _, ok := s.workspaces[a.WorkspaceID]; !ok && a.WorkspaceID != "" {
		s.workspaces[a.WorkspaceID] = herdr.WorkspaceInfo{WorkspaceID: a.WorkspaceID, Label: a.WorkspaceID}
	}
}

// SetStatus changes an agent's status in the table only (no event).
func (s *Server) SetStatus(paneID string, st herdr.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.agents[paneID]; ok {
		a.Status = st
		a.StateChangeSeq++
		s.agents[paneID] = a
	}
	if p, ok := s.panes[paneID]; ok {
		p.Status = st
		s.panes[paneID] = p
	}
}

// RemoveAgent forgets the agent but keeps the pane (a shell remains).
func (s *Server) RemoveAgent(paneID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.agents, paneID)
	if p, ok := s.panes[paneID]; ok {
		p.Status, p.Agent = herdr.StateUnknown, ""
		s.panes[paneID] = p
	}
}

// RemovePane forgets the pane and any agent in it.
func (s *Server) RemovePane(paneID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.agents, paneID)
	delete(s.panes, paneID)
}

// SetScreen sets the text returned by pane.read / agent.read for a pane.
func (s *Server) SetScreen(paneID, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.screens[paneID] = text
}

// Handle overrides the handler for a method.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// Requests returns a copy of every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// RequestsFor returns the recorded requests for one method.
func (s *Server) RequestsFor(method string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

// SubscriptionCount returns the number of open subscription connections.
func (s *Server) SubscriptionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// CloseSubscriptions drops every open subscription connection, as a server
// restart would.
func (s *Server) CloseSubscriptions() {
	s.mu.Lock()
	conns := make([]*subConn, 0, len(s.subs))
	for c := range s.subs {
		conns = append(conns, c)
	}
	s.subs = map[*subConn]struct{}{}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.conn.Close()
	}
}

// EmitStatus updates the table and broadcasts a pane.agent_status_changed
// event exactly as herdr formats it.
func (s *Server) EmitStatus(paneID string, st herdr.State) {
	s.SetStatus(paneID, st)
	s.mu.Lock()
	a := s.agents[paneID]
	p := s.panes[paneID]
	s.mu.Unlock()
	data := map[string]any{"pane_id": paneID, "workspace_id": p.WorkspaceID, "agent_status": st}
	if a.Agent != "" {
		data["agent"] = a.Agent
	}
	s.EmitRaw(map[string]any{"event": "pane.agent_status_changed", "data": data})
}

// EmitAgentDetected broadcasts pane_agent_detected. With released it
// carries released:true and final_status like a real exit.
func (s *Server) EmitAgentDetected(paneID, workspaceID, agent string, released bool, final herdr.State) {
	data := map[string]any{"type": "pane_agent_detected", "pane_id": paneID, "workspace_id": workspaceID, "agent": agent}
	if released {
		data["released"] = true
		data["final_status"] = final
	}
	s.EmitRaw(map[string]any{"event": "pane_agent_detected", "data": data})
}

// EmitPaneClosed broadcasts pane_closed.
func (s *Server) EmitPaneClosed(paneID, workspaceID string) {
	s.EmitRaw(map[string]any{"event": "pane_closed", "data": map[string]any{"type": "pane_closed", "pane_id": paneID, "workspace_id": workspaceID}})
}

// EmitWorkspaceClosed broadcasts workspace_closed.
func (s *Server) EmitWorkspaceClosed(workspaceID string) {
	s.EmitRaw(map[string]any{"event": "workspace_closed", "data": map[string]any{"type": "workspace_closed", "workspace_id": workspaceID}})
}

// EmitRaw broadcasts one event object (or a pre-encoded JSON line as
// []byte/string) to every subscription whose filters match it. Like the
// real server, the table stays consistent with the stream: a status event
// updates the pane's status and a pane_agent_detected updates its kind.
func (s *Server) EmitRaw(ev any) {
	var line []byte
	switch v := ev.(type) {
	case []byte:
		line = v
	case string:
		line = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		line = b
	}
	line = append(bytesTrimNewline(line), '\n')

	var raw struct {
		Event string `json:"event"`
		Data  struct {
			PaneID   string      `json:"pane_id"`
			Status   herdr.State `json:"agent_status"`
			Agent    string      `json:"agent"`
			Released bool        `json:"released"`
		} `json:"data"`
	}
	_ = json.Unmarshal(line, &raw)
	s.syncTable(raw.Event, raw.Data.PaneID, raw.Data.Status, raw.Data.Agent, raw.Data.Released)

	s.mu.Lock()
	conns := make([]*subConn, 0, len(s.subs))
	for c := range s.subs {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		if !c.matches(raw.Event, raw.Data.PaneID) {
			continue
		}
		c.mu.Lock()
		_, _ = c.conn.Write(line)
		c.mu.Unlock()
	}
}

func (s *Server) syncTable(event, paneID string, st herdr.State, agent string, released bool) {
	if paneID == "" {
		return
	}
	switch event {
	case "pane.agent_status_changed":
		if st.Valid() {
			s.SetStatus(paneID, st)
		}
	case "pane_agent_detected":
		s.mu.Lock()
		defer s.mu.Unlock()
		if a, ok := s.agents[paneID]; ok && !released {
			a.Agent = agent
			s.agents[paneID] = a
		}
		if p, ok := s.panes[paneID]; ok {
			if released {
				p.Agent = ""
			} else {
				p.Agent = agent
			}
			s.panes[paneID] = p
		}
	}
}

// Replay broadcasts every event line of a captured events-*.jsonl fixture,
// skipping the subscription_started ack, sleeping gap between lines (real
// herdr polls at 100 ms, so events never arrive back to back). It returns
// the number of events sent.
func (s *Server) Replay(t testing.TB, path string, gap time.Duration) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" || strings.Contains(line, `"subscription_started"`) {
			continue
		}
		if n > 0 {
			time.Sleep(gap)
		}
		s.EmitRaw(line)
		n++
	}
	return n
}

// dottedToSnake maps subscription type names onto the snake_case event kind
// that lifecycle events carry on the wire.
func dottedToSnake(t string) string { return strings.ReplaceAll(t, ".", "_") }

func (c *subConn) matches(event, paneID string) bool {
	for _, sub := range c.subs {
		switch sub.Type {
		case "pane.agent_status_changed":
			if event == "pane.agent_status_changed" && (sub.PaneID == "" || sub.PaneID == paneID) {
				return true
			}
		default:
			if dottedToSnake(sub.Type) == event {
				return true
			}
		}
	}
	return false
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(conn)
		}()
	}
}

type response struct {
	ID     string       `json:"id"`
	Result any          `json:"result,omitempty"`
	Error  *herdr.Error `json:"error,omitempty"`
}

func (s *Server) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		_ = conn.Close()
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(conn, response{ID: "", Error: &herdr.Error{Code: "invalid_request", Message: "invalid request: " + err.Error()}})
		_ = conn.Close()
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	h, custom := s.handlers[req.Method]
	s.mu.Unlock()

	if req.Method == "events.subscribe" && !custom {
		s.subscribe(conn, req)
		return
	}
	defer func() { _ = conn.Close() }()
	if !custom {
		h = s.builtin(req.Method)
	}
	if h == nil {
		s.reply(conn, response{ID: "", Error: &herdr.Error{Code: "invalid_request", Message: fmt.Sprintf("invalid request: unknown variant `%s`", req.Method)}})
		return
	}
	res, err := h(req.Params)
	if err != nil {
		var he *herdr.Error
		if !errors.As(err, &he) {
			he = &herdr.Error{Code: "internal_error", Message: err.Error()}
		}
		s.reply(conn, response{ID: req.ID, Error: he})
		return
	}
	s.reply(conn, response{ID: req.ID, Result: res})
}

func (s *Server) reply(conn net.Conn, resp response) {
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(b, '\n'))
}

func (s *Server) subscribe(conn net.Conn, req Request) {
	var params struct {
		Subscriptions []subscription `json:"subscriptions"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params.Subscriptions) == 0 {
		s.reply(conn, response{ID: req.ID, Error: &herdr.Error{Code: "invalid_params", Message: "subscriptions required"}})
		_ = conn.Close()
		return
	}
	s.mu.Lock()
	for _, sub := range params.Subscriptions {
		if sub.Type == "pane.agent_status_changed" {
			if sub.PaneID == "" {
				s.mu.Unlock()
				s.reply(conn, response{ID: req.ID, Error: &herdr.Error{Code: "invalid_params", Message: "pane_id required"}})
				_ = conn.Close()
				return
			}
			if _, ok := s.panes[sub.PaneID]; !ok {
				s.mu.Unlock()
				s.reply(conn, response{ID: req.ID, Error: &herdr.Error{Code: "pane_not_found", Message: "pane " + sub.PaneID + " not found"}})
				_ = conn.Close()
				return
			}
		}
	}
	c := &subConn{conn: conn, subs: params.Subscriptions}
	s.subs[c] = struct{}{}
	s.mu.Unlock()

	s.reply(conn, response{ID: req.ID, Result: map[string]any{"type": "subscription_started"}})

	// Hold the connection until the client goes away or the server drops it.
	_, _ = io.Copy(io.Discard, conn)
	s.mu.Lock()
	delete(s.subs, c)
	s.mu.Unlock()
	_ = conn.Close()
}

func (s *Server) builtin(method string) Handler {
	switch method {
	case "ping":
		return func(json.RawMessage) (any, error) {
			return map[string]any{"type": "pong", "version": s.version, "protocol": 22}, nil
		}
	case "agent.list":
		return func(json.RawMessage) (any, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			agents := make([]herdr.AgentInfo, 0, len(s.agents))
			for _, a := range s.agents {
				agents = append(agents, a)
			}
			return map[string]any{"type": "agent_list", "agents": agents}, nil
		}
	case "agent.get":
		return func(p json.RawMessage) (any, error) {
			a, err := s.resolveAgent(p)
			if err != nil {
				return nil, err
			}
			return map[string]any{"type": "agent_info", "agent": a}, nil
		}
	case "session.snapshot":
		return func(json.RawMessage) (any, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			snap := herdr.Snapshot{Version: s.version, Protocol: 22,
				Workspaces: []herdr.WorkspaceInfo{}, Panes: []herdr.PaneInfo{}, Agents: []herdr.AgentInfo{}}
			for _, w := range s.workspaces {
				snap.Workspaces = append(snap.Workspaces, w)
			}
			for _, p := range s.panes {
				snap.Panes = append(snap.Panes, p)
			}
			for _, a := range s.agents {
				snap.Agents = append(snap.Agents, a)
			}
			return map[string]any{"type": "session_snapshot", "snapshot": snap}, nil
		}
	case "pane.read", "agent.read":
		return func(p json.RawMessage) (any, error) {
			var params struct {
				PaneID string `json:"pane_id"`
				Target string `json:"target"`
				Source string `json:"source"`
				Lines  int    `json:"lines"`
			}
			_ = json.Unmarshal(p, &params)
			if params.Source == "" {
				return nil, &herdr.Error{Code: "invalid_params", Message: "missing field `source`"}
			}
			paneID := params.PaneID
			if method == "agent.read" {
				a, err := s.resolveAgent(p)
				if err != nil {
					return nil, err
				}
				paneID = a.PaneID
			}
			s.mu.Lock()
			pane, ok := s.panes[paneID]
			text := s.screens[paneID]
			s.mu.Unlock()
			if !ok {
				return nil, &herdr.Error{Code: "pane_not_found", Message: "pane " + paneID + " not found"}
			}
			truncated := false
			if params.Lines > 0 {
				lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
				if len(lines) > params.Lines {
					lines = lines[len(lines)-params.Lines:]
					truncated = true
				}
				text = strings.Join(lines, "\n") + "\n"
			}
			return map[string]any{"type": "pane_read", "read": map[string]any{
				"pane_id": paneID, "workspace_id": pane.WorkspaceID, "tab_id": pane.TabID,
				"source": params.Source, "format": "text", "text": text, "revision": 0, "truncated": truncated,
			}}, nil
		}
	case "agent.prompt":
		return func(p json.RawMessage) (any, error) {
			a, err := s.resolveAgent(p)
			if err != nil {
				return nil, err
			}
			if a.Status == herdr.StateBlocked {
				return nil, &herdr.Error{Code: "agent_blocked", Message: fmt.Sprintf("agent %s is blocked and requires interactive input", a.PaneID)}
			}
			return map[string]any{"type": "agent_prompted", "agent": a}, nil
		}
	case "pane.send_keys", "agent.send_keys":
		return func(p json.RawMessage) (any, error) {
			var params struct {
				PaneID string   `json:"pane_id"`
				Keys   []string `json:"keys"`
			}
			_ = json.Unmarshal(p, &params)
			if method == "agent.send_keys" {
				a, err := s.resolveAgent(p)
				if err != nil {
					return nil, err
				}
				params.PaneID = a.PaneID
			}
			s.mu.Lock()
			_, ok := s.panes[params.PaneID]
			s.mu.Unlock()
			if !ok {
				return nil, &herdr.Error{Code: "pane_not_found", Message: "pane " + params.PaneID + " not found"}
			}
			for _, k := range params.Keys {
				if k == "" || strings.ContainsAny(k, " \t") {
					return nil, &herdr.Error{Code: "invalid_key", Message: "unsupported key " + k}
				}
			}
			return map[string]any{"type": "ok"}, nil
		}
	}
	return nil
}

// resolveAgent looks up params.target (name or pane id) like herdr does.
func (s *Server) resolveAgent(p json.RawMessage) (herdr.AgentInfo, error) {
	var params struct {
		Target string `json:"target"`
	}
	_ = json.Unmarshal(p, &params)
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.agents[params.Target]; ok {
		return a, nil
	}
	for _, a := range s.agents {
		if a.Name != "" && a.Name == params.Target {
			return a, nil
		}
	}
	return herdr.AgentInfo{}, &herdr.Error{Code: "agent_not_found", Message: "agent target " + params.Target + " not found"}
}

func bytesTrimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
