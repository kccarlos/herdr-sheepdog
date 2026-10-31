// Package hctest provides an in-memory fake herdr-core server for tests.
package hctest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// Request is one JSON-RPC request received by the fake server.
type Request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Server is an in-memory fake herdr-core server. Its methods are safe for
// concurrent use.
type Server struct {
	// Path is the Unix socket to give to hc.NewClient.
	Path string

	ln   net.Listener
	dir  string
	quit chan struct{}
	wg   sync.WaitGroup

	closeOnce sync.Once
	emitMu    sync.Mutex

	mu          sync.Mutex
	seq         uint64
	nextID      uint64
	terminals   map[string]hc.Terminal
	screens     map[string]string
	requests    []Request
	subscribers map[*subscriber]struct{}

	// Framed mode (framed.go).
	fconns   map[*framedConn]struct{}
	paints   map[string]string
	attaches map[string]int
	frames   []StreamFrame
}

type subscriber struct {
	conn      net.Conn
	filter    hc.SubscribeFilter
	writeMu   sync.Mutex
	closeOnce sync.Once
}

type wireRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type wireResponse struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      uint64     `json:"id"`
	Result  any        `json:"result,omitempty"`
	Error   *wireError `json:"error,omitempty"`
}

type wireError struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *errorData `json:"data,omitempty"`
}

func (e *wireError) Error() string {
	return e.Message
}

type errorData struct {
	Kind string `json:"kind"`
	Hint string `json:"hint,omitempty"`
}

// New starts a fake server on a short temporary Unix socket and removes it
// when the test ends. The short path keeps macOS's sockaddr_un limit in mind.
func New(t testing.TB) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "hct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	s := &Server{
		Path:        filepath.Join(dir, "hc.sock"),
		dir:         dir,
		quit:        make(chan struct{}),
		terminals:   make(map[string]hc.Terminal),
		screens:     make(map[string]string),
		subscribers: make(map[*subscriber]struct{}),
	}
	s.ln, err = net.Listen("unix", s.Path)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatal(err)
	}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(s.Close)
	return s
}

// Close stops the server, drops subscriptions, waits for handlers, and
// removes its temporary directory.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.quit)
		_ = s.ln.Close()
		s.DropSubscribers()
		s.dropFramed()
		s.wg.Wait()
		_ = os.RemoveAll(s.dir)
	})
}

// Add adds or replaces a terminal without emitting an event.
func (s *Server) Add(t hc.Terminal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.Labels == nil {
		t.Labels = map[string]string{}
	}
	if t.Argv == nil {
		t.Argv = []string{}
	}
	s.terminals[t.Address] = t
	s.noteAddressLocked(t.Address)
}

// SetStatus updates a terminal's root status and emits a status event.
func (s *Server) SetStatus(address string, status *hc.Status) {
	s.mu.Lock()
	t, ok := s.terminals[address]
	if !ok {
		s.mu.Unlock()
		return
	}
	if status != nil {
		t.Reported = true
	}
	t.Status = status
	s.terminals[address] = t
	s.mu.Unlock()

	s.emitEvent(address, &t, "status", map[string]any{
		"status":   status,
		"records":  map[string]any{},
		"reported": t.Reported,
	})
}

// SetScreen sets the text returned by terminal.read.
func (s *Server) SetScreen(address, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.screens[address] = text
}

// Finish marks a terminal finished and emits a finished event.
func (s *Server) Finish(address, reason string) {
	s.mu.Lock()
	t, ok := s.terminals[address]
	if !ok {
		s.mu.Unlock()
		return
	}
	t.State = "finished"
	t.Reason = &reason
	t.Status = nil
	t.Foreground = nil
	s.terminals[address] = t
	s.mu.Unlock()

	s.emitEvent(address, &t, "finished", map[string]any{
		"reason":    reason,
		"exit_code": t.ExitCode,
	})
}

// Remove deletes a terminal and emits a removed event.
func (s *Server) Remove(address string) {
	s.mu.Lock()
	t, ok := s.terminals[address]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.terminals, address)
	delete(s.screens, address)
	s.mu.Unlock()
	s.emitEvent(address, &t, "removed", map[string]any{})
}

// Missed sends every current subscription a fresh snapshot as a missed
// notification.
func (s *Server) Missed() {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()

	s.mu.Lock()
	type target struct {
		sub       *subscriber
		terminals []hc.Terminal
	}
	targets := make([]target, 0, len(s.subscribers))
	for sub := range s.subscribers {
		targets = append(targets, target{
			sub:       sub,
			terminals: s.matchingLocked(sub.filter),
		})
	}
	seq := s.seq
	s.mu.Unlock()

	for _, target := range targets {
		s.writeNotification(target.sub, "missed", map[string]any{
			"seq":       seq,
			"terminals": target.terminals,
		})
	}
}

// Heartbeat sends every current subscription a heartbeat notification.
func (s *Server) Heartbeat() {
	s.mu.Lock()
	subs := make([]*subscriber, 0, len(s.subscribers))
	for sub := range s.subscribers {
		subs = append(subs, sub)
	}
	seq := s.seq
	s.mu.Unlock()
	for _, sub := range subs {
		s.writeNotification(sub, "heartbeat", map[string]any{"seq": seq})
	}
}

// DropSubscribers closes every open subscription connection.
func (s *Server) DropSubscribers() {
	s.mu.Lock()
	subs := make([]*subscriber, 0, len(s.subscribers))
	for sub := range s.subscribers {
		subs = append(subs, sub)
	}
	s.subscribers = make(map[*subscriber]struct{})
	s.mu.Unlock()
	for _, sub := range subs {
		sub.close()
	}
}

// Requests returns a copy of every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
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

func (s *Server) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	in := bufio.NewReader(conn)
	line, err := in.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}

	var request wireRequest
	if err := json.Unmarshal(line, &request); err != nil {
		s.reply(conn, wireResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Error:   protocolError(-32700, "parse error", "", ""),
		})
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		ID:     request.ID,
		Method: request.Method,
		Params: append(json.RawMessage(nil), request.Params...),
	})
	s.mu.Unlock()

	if request.Method == "events.subscribe" {
		s.subscribe(conn, request)
		return
	}
	if request.Method == "hello" && helloFramed(request.Params) {
		s.serveFramed(conn, in, request)
		return
	}

	result, callErr := s.handle(request.Method, request.Params)
	if callErr != nil {
		s.reply(conn, wireResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Error:   hcError(callErr),
		})
		return
	}
	s.reply(conn, wireResponse{JSONRPC: "2.0", ID: request.ID, Result: result})
}

func (s *Server) handle(method string, params json.RawMessage) (any, error) {
	switch method {
	case "hello":
		return hc.HelloResult{
			Version:    "0.1.15",
			Generation: 1,
			Features:   []string{"events"},
			Methods: []string{
				"hello", "terminal.list", "terminal.get", "terminal.read",
				"terminal.send", "terminal.keys", "terminal.interrupt",
				"terminal.run", "terminal.kill", "terminal.attach",
				"events.subscribe",
			},
			Core: hc.CoreInfo{ID: "fake-core", Name: "hctest"},
		}, nil
	case "terminal.list":
		var p struct {
			All bool `json:"all"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		return s.list(p.All), nil
	case "terminal.get":
		var p terminalParams
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		t, ok := s.get(p.Terminal)
		if !ok {
			return nil, notFound(p.Terminal)
		}
		return t, nil
	case "terminal.read":
		var p struct {
			Terminal string `json:"terminal"`
			Lines    int    `json:"lines"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		return s.read(p.Terminal, p.Lines)
	case "terminal.send":
		var p struct {
			Terminal string `json:"terminal"`
			Text     string `json:"text"`
			Force    bool   `json:"force"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		return s.send(p.Terminal, p.Text, p.Force)
	case "terminal.keys":
		var p struct {
			Terminal string   `json:"terminal"`
			Keys     []string `json:"keys"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		return s.keys(p.Terminal)
	case "terminal.interrupt":
		var p terminalParams
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		return s.interrupt(p.Terminal)
	case "terminal.run":
		var p hc.RunParams
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		return s.run(p), nil
	case "terminal.kill":
		var p terminalParams
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		return s.kill(p.Terminal)
	default:
		return nil, &wireError{
			Code:    -32601,
			Message: "method not found",
		}
	}
}

func (s *Server) list(all bool) hc.ListResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := hc.ListResult{Terminals: make([]hc.Terminal, 0, len(s.terminals))}
	for _, t := range s.terminals {
		if t.State != "live" && !all {
			result.HiddenFinished++
			continue
		}
		result.Terminals = append(result.Terminals, t)
	}
	sort.Slice(result.Terminals, func(i, j int) bool {
		return result.Terminals[i].Address < result.Terminals[j].Address
	})
	return result
}

func (s *Server) get(target string) (hc.Terminal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolveLocked(target)
}

func (s *Server) read(target string, lines int) (hc.ReadResult, error) {
	s.mu.Lock()
	t, ok := s.resolveLocked(target)
	text := s.screens[t.Address]
	s.mu.Unlock()
	if !ok {
		return hc.ReadResult{}, notFound(target)
	}
	if lines > 0 {
		parts := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		if len(parts) > lines {
			text = strings.Join(parts[len(parts)-lines:], "\n")
		}
	}
	return hc.ReadResult{Text: text, Complete: true, How: "screen"}, nil
}

func (s *Server) send(target, _ string, force bool) (hc.SendResult, error) {
	s.mu.Lock()
	t, ok := s.resolveLocked(target)
	s.mu.Unlock()
	if !ok {
		return hc.SendResult{}, notFound(target)
	}
	if t.State == "finished" {
		return hc.SendResult{}, kindError("finished", fmt.Sprintf("terminal %s is finished", target))
	}
	if t.Status != nil && t.Status.State == "blocked" && !force {
		return hc.SendResult{}, kindError("blocked", fmt.Sprintf("terminal %s is blocked", target))
	}
	var started *bool
	var status *string
	if t.Status != nil {
		value := true
		started = &value
		word := t.Status.State
		status = &word
	}
	return hc.SendResult{Started: started, Status: status, Turn: t.Turn}, nil
}

func (s *Server) keys(target string) (hc.KeysResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.resolveLocked(target)
	if !ok {
		return hc.KeysResult{}, notFound(target)
	}
	if t.State == "finished" {
		return hc.KeysResult{}, kindError("finished", fmt.Sprintf("terminal %s is finished", target))
	}
	var status *string
	if t.Status != nil {
		word := t.Status.State
		status = &word
	}
	return hc.KeysResult{Status: status, Turn: t.Turn}, nil
}

func (s *Server) interrupt(target string) (hc.InterruptResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.resolveLocked(target)
	if !ok {
		return hc.InterruptResult{}, notFound(target)
	}
	if t.State == "finished" {
		return hc.InterruptResult{}, kindError("finished", fmt.Sprintf("terminal %s is finished", target))
	}
	if t.Status != nil {
		return hc.InterruptResult{Key: "esc", Outcome: "stopped"}, nil
	}
	return hc.InterruptResult{Key: "ctrl+c", Outcome: "unknown"}, nil
}

func (s *Server) run(p hc.RunParams) hc.Terminal {
	s.mu.Lock()
	address := s.nextAddressLocked()
	argv := append([]string(nil), p.Argv...)
	if len(argv) == 0 && p.Line != "" {
		argv = []string{"sh", "-c", p.Line}
	}
	labels := cloneMap(p.Labels)
	t := hc.Terminal{
		Address:   address,
		Name:      p.Name,
		Argv:      argv,
		Cwd:       p.Cwd,
		State:     "live",
		Labels:    labels,
		CreatedMS: uint64(time.Now().UnixMilli()),
		Cols:      80,
		Rows:      24,
		Keep:      p.Keep,
	}
	s.terminals[address] = t
	s.mu.Unlock()
	s.emitEvent(address, &t, "created", t)
	return t
}

func (s *Server) kill(target string) (hc.Terminal, error) {
	s.mu.Lock()
	t, ok := s.resolveLocked(target)
	if !ok {
		s.mu.Unlock()
		return hc.Terminal{}, notFound(target)
	}
	reason := "killed"
	t.State = "finished"
	t.Reason = &reason
	t.Status = nil
	t.Foreground = nil
	s.terminals[t.Address] = t
	s.mu.Unlock()

	s.emitEvent(t.Address, &t, "finished", map[string]any{
		"reason":    reason,
		"exit_code": t.ExitCode,
	})
	return t, nil
}

func (s *Server) subscribe(conn net.Conn, request wireRequest) {
	var filter hc.SubscribeFilter
	if err := decodeParams(request.Params, &filter); err != nil {
		s.reply(conn, wireResponse{JSONRPC: "2.0", ID: request.ID, Error: hcError(err)})
		return
	}

	sub := &subscriber{conn: conn, filter: filter}
	sub.writeMu.Lock()

	s.mu.Lock()
	if filter.Terminal != "" {
		if _, ok := s.resolveLocked(filter.Terminal); !ok {
			s.mu.Unlock()
			sub.writeMu.Unlock()
			s.reply(conn, wireResponse{
				JSONRPC: "2.0",
				ID:      request.ID,
				Error:   hcError(notFound(filter.Terminal)),
			})
			return
		}
	}
	snapshot := hc.SubscribeResult{
		Seq:       s.seq,
		Terminals: s.matchingLocked(filter),
	}
	s.subscribers[sub] = struct{}{}
	s.mu.Unlock()

	s.reply(conn, wireResponse{JSONRPC: "2.0", ID: request.ID, Result: snapshot})
	sub.writeMu.Unlock()

	_, _ = io.Copy(io.Discard, conn)
	s.mu.Lock()
	delete(s.subscribers, sub)
	s.mu.Unlock()
}

func (s *Server) emitEvent(terminal string, record *hc.Terminal, eventType string, data any) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()

	s.mu.Lock()
	s.seq++
	seq := s.seq
	subs := make([]*subscriber, 0, len(s.subscribers))
	for sub := range s.subscribers {
		if s.subscriberMatchesLocked(sub, terminal, record) {
			subs = append(subs, sub)
		}
	}
	s.mu.Unlock()

	for _, sub := range subs {
		s.writeNotification(sub, "event", map[string]any{
			"seq":      seq,
			"type":     eventType,
			"terminal": terminal,
			"data":     data,
		})
	}
}

func (s *Server) writeNotification(sub *subscriber, method string, params any) {
	message := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(message)
	if err != nil {
		return
	}
	data = append(data, '\n')
	sub.writeMu.Lock()
	_, _ = sub.conn.Write(data)
	sub.writeMu.Unlock()
}

func (s *Server) matchingLocked(filter hc.SubscribeFilter) []hc.Terminal {
	terminals := make([]hc.Terminal, 0, len(s.terminals))
	for _, t := range s.terminals {
		if matchesFilter(filter, t) {
			terminals = append(terminals, t)
		}
	}
	sort.Slice(terminals, func(i, j int) bool {
		return terminals[i].Address < terminals[j].Address
	})
	return terminals
}

func (s *Server) subscriberMatchesLocked(sub *subscriber, terminal string, record *hc.Terminal) bool {
	if sub.filter.Terminal != "" && !terminalMatch(sub.filter.Terminal, terminal, record) {
		return false
	}
	if len(sub.filter.Labels) == 0 {
		return true
	}
	if record == nil {
		return false
	}
	for key, value := range sub.filter.Labels {
		if record.Labels[key] != value {
			return false
		}
	}
	return true
}

func matchesFilter(filter hc.SubscribeFilter, t hc.Terminal) bool {
	if filter.Terminal != "" && !terminalMatch(filter.Terminal, t.Address, &t) {
		return false
	}
	for key, value := range filter.Labels {
		if t.Labels[key] != value {
			return false
		}
	}
	return true
}

func terminalMatch(filter, address string, t *hc.Terminal) bool {
	if filter == address {
		return true
	}
	return t != nil && filter == t.Name
}

func (s *Server) resolveLocked(target string) (hc.Terminal, bool) {
	if t, ok := s.terminals[target]; ok {
		return t, true
	}
	for _, t := range s.terminals {
		if t.Name != "" && t.Name == target {
			return t, true
		}
	}
	return hc.Terminal{}, false
}

func (s *Server) nextAddressLocked() string {
	for {
		s.nextID++
		address := fmt.Sprintf("t%d", s.nextID)
		if _, exists := s.terminals[address]; !exists {
			return address
		}
	}
}

func (s *Server) noteAddressLocked(address string) {
	var n uint64
	if _, err := fmt.Sscanf(address, "t%d", &n); err == nil && n > s.nextID {
		s.nextID = n
	}
}

func (s *Server) reply(conn net.Conn, response wireResponse) {
	data, err := json.Marshal(response)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(data, '\n'))
}

func (sub *subscriber) close() {
	sub.closeOnce.Do(func() {
		_ = sub.conn.Close()
	})
}

type terminalParams struct {
	Terminal string `json:"terminal"`
}

func decodeParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &wireError{
			Code:    -32602,
			Message: "invalid params",
			Data:    &errorData{Kind: "invalid_params"},
		}
	}
	return nil
}

func notFound(target string) error {
	return &hc.Error{
		Code:    -32000,
		Message: fmt.Sprintf("no terminal %s", target),
		Kind:    "not_found",
		Hint:    "list terminals to find a live address",
	}
}

func kindError(kind, message string) error {
	return &hc.Error{Code: -32000, Message: message, Kind: kind}
}

func hcError(err error) *wireError {
	var serverErr *hc.Error
	if errors.As(err, &serverErr) {
		data := &errorData{Kind: serverErr.Kind, Hint: serverErr.Hint}
		return &wireError{Code: serverErr.Code, Message: serverErr.Message, Data: data}
	}
	var protocolErr *wireError
	if errors.As(err, &protocolErr) {
		return protocolErr
	}
	return protocolError(-32603, err.Error(), "internal_error", "")
}

func protocolError(code int, message, kind, hint string) *wireError {
	var data *errorData
	if kind != "" || hint != "" {
		data = &errorData{Kind: kind, Hint: hint}
	}
	return &wireError{Code: code, Message: message, Data: data}
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
