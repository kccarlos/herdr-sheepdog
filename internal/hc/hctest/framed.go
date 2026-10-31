package hctest

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
)

// The fake's framed mode (hello `framed`): channel 0 carries JSON-RPC, and
// terminal.attach opens a stream that gets the terminal's paint, then what
// Emit sends, within the viewer's credit.

// Frame kinds a viewer sends (and DATA/CLOSE the fake sends).
const (
	FrameData   = 0
	FrameCredit = 1
	FrameClose  = 2
	FrameResize = 3
	FrameFocus  = 4
	FrameFacts  = 5
)

// initialCredit is the bytes a stream may send before the viewer's CREDIT.
const initialCredit = 256 << 10

// StreamFrame is one frame a viewer sent on an attach stream.
type StreamFrame struct {
	Terminal string
	// Attach counts the attaches to Terminal, from 1.
	Attach  int
	Kind    byte
	Payload []byte
}

// framedConn is one framed connection.
type framedConn struct {
	conn net.Conn
	wmu  sync.Mutex // one frame at a time on the wire
	fmu  sync.Mutex // one flush at a time, so a stream's chunks keep their order

	streams map[uint32]*fstream // under Server.mu
	next    uint32
}

// fstream is one attach stream. A frame goes out only when the viewer's
// credit covers it, as hc's does.
type fstream struct {
	channel  uint32
	terminal string
	attach   int
	credit   int
	pending  [][]byte
}

// SetPaint sets the first paint an attach to the terminal receives.
func (s *Server) SetPaint(address, paint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paints == nil {
		s.paints = map[string]string{}
	}
	s.paints[address] = paint
}

// Emit sends program output to every stream attached to the terminal, as
// credit allows.
func (s *Server) Emit(address string, p []byte) {
	s.mu.Lock()
	var work []*framedConn
	for fc := range s.fconns {
		for _, st := range fc.streams {
			if st.terminal == address {
				st.pending = append(st.pending, append([]byte(nil), p...))
				work = append(work, fc)
			}
		}
	}
	s.mu.Unlock()
	for _, fc := range work {
		s.flush(fc)
	}
}

// EndStreams closes every stream attached to the terminal the way hc does
// when its program ends: CLOSE with a JSON reason.
func (s *Server) EndStreams(address, reason string, exitCode *int) {
	payload, _ := json.Marshal(map[string]any{"reason": reason, "exit_code": exitCode, "signal": nil})
	s.mu.Lock()
	type end struct {
		fc      *framedConn
		channel uint32
	}
	var ends []end
	for fc := range s.fconns {
		for ch, st := range fc.streams {
			if st.terminal == address {
				ends = append(ends, end{fc, ch})
				delete(fc.streams, ch)
			}
		}
	}
	s.mu.Unlock()
	for _, e := range ends {
		_ = e.fc.write(e.channel, FrameClose, payload)
	}
}

// StreamFrames returns every frame viewers sent on attach streams, in
// arrival order.
func (s *Server) StreamFrames() []StreamFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]StreamFrame(nil), s.frames...)
}

// Streams is how many attach streams are open on the terminal.
func (s *Server) Streams(address string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for fc := range s.fconns {
		for _, st := range fc.streams {
			if st.terminal == address {
				n++
			}
		}
	}
	return n
}

// helloFramed reports whether hello's params ask for framed mode.
func helloFramed(params json.RawMessage) bool {
	var p struct {
		Framed bool `json:"framed"`
	}
	return json.Unmarshal(params, &p) == nil && p.Framed
}

// serveFramed answers a framed hello and then serves frames until the
// connection ends. in is the reader that already read the hello line.
func (s *Server) serveFramed(conn net.Conn, in *bufio.Reader, request wireRequest) {
	result, err := s.handle("hello", request.Params)
	if err != nil {
		s.reply(conn, wireResponse{JSONRPC: "2.0", ID: request.ID, Error: hcError(err)})
		return
	}
	fc := &framedConn{conn: conn, streams: map[uint32]*fstream{}}
	s.mu.Lock()
	if s.fconns == nil {
		s.fconns = map[*framedConn]struct{}{}
	}
	s.fconns[fc] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.fconns, fc)
		s.mu.Unlock()
	}()
	s.reply(conn, wireResponse{JSONRPC: "2.0", ID: request.ID, Result: result})

	var hdr [9]byte
	for {
		if _, err := io.ReadFull(in, hdr[:]); err != nil {
			return
		}
		channel := binary.LittleEndian.Uint32(hdr[0:])
		kind := hdr[4]
		payload := make([]byte, binary.LittleEndian.Uint32(hdr[5:]))
		if _, err := io.ReadFull(in, payload); err != nil {
			return
		}
		if channel == 0 {
			if kind == FrameData {
				s.framedCall(fc, payload)
			}
			continue
		}
		s.streamFrame(fc, channel, kind, payload)
	}
}

// framedCall handles one JSON-RPC request on channel 0.
func (s *Server) framedCall(fc *framedConn, payload []byte) {
	var request wireRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		ID:     request.ID,
		Method: request.Method,
		Params: append(json.RawMessage(nil), request.Params...),
	})
	s.mu.Unlock()

	var result any
	var callErr error
	attached := false
	if request.Method == "terminal.attach" {
		result, callErr = s.attach(fc, request.Params)
		attached = callErr == nil
	} else {
		result, callErr = s.handle(request.Method, request.Params)
	}
	response := wireResponse{JSONRPC: "2.0", ID: request.ID, Result: result}
	if callErr != nil {
		response = wireResponse{JSONRPC: "2.0", ID: request.ID, Error: hcError(callErr)}
	}
	data, err := json.Marshal(response)
	if err != nil {
		return
	}
	_ = fc.write(0, FrameData, data)
	if attached {
		s.flush(fc)
	}
}

// attach opens a stream on a live terminal, with its paint queued.
func (s *Server) attach(fc *framedConn, params json.RawMessage) (any, error) {
	var p struct {
		Terminal string `json:"terminal"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.resolveLocked(p.Terminal)
	if !ok {
		return nil, notFound(p.Terminal)
	}
	if t.State == "finished" {
		return nil, kindError("finished", "terminal "+p.Terminal+" is finished")
	}
	if s.attaches == nil {
		s.attaches = map[string]int{}
	}
	s.attaches[t.Address]++
	fc.next++
	st := &fstream{channel: fc.next, terminal: t.Address, attach: s.attaches[t.Address], credit: initialCredit}
	if paint := s.paints[t.Address]; paint != "" {
		st.pending = append(st.pending, []byte(paint))
	}
	fc.streams[st.channel] = st
	return map[string]any{"channel": st.channel, "terminal": t}, nil
}

// streamFrame records a viewer's frame and acts on CREDIT and CLOSE.
func (s *Server) streamFrame(fc *framedConn, channel uint32, kind byte, payload []byte) {
	s.mu.Lock()
	st := fc.streams[channel]
	if st == nil {
		s.mu.Unlock()
		return
	}
	s.frames = append(s.frames, StreamFrame{Terminal: st.terminal, Attach: st.attach, Kind: kind, Payload: payload})
	switch kind {
	case FrameCredit:
		if len(payload) == 4 {
			st.credit += int(binary.LittleEndian.Uint32(payload))
		}
	case FrameClose:
		delete(fc.streams, channel)
	}
	s.mu.Unlock()
	switch kind {
	case FrameCredit:
		s.flush(fc)
	case FrameClose:
		_ = fc.write(channel, FrameClose, nil)
	}
}

// flush sends every pending chunk the credit covers, in order.
func (s *Server) flush(fc *framedConn) {
	fc.fmu.Lock()
	defer fc.fmu.Unlock()
	for {
		s.mu.Lock()
		var send []byte
		var channel uint32
		for ch, st := range fc.streams {
			if len(st.pending) > 0 && st.credit >= len(st.pending[0]) {
				send, channel = st.pending[0], ch
				st.pending = st.pending[1:]
				st.credit -= len(send)
				break
			}
		}
		s.mu.Unlock()
		if send == nil {
			return
		}
		if fc.write(channel, FrameData, send) != nil {
			return
		}
	}
}

func (fc *framedConn) write(channel uint32, kind byte, payload []byte) error {
	buf := make([]byte, 9+len(payload))
	binary.LittleEndian.PutUint32(buf[0:], channel)
	buf[4] = kind
	binary.LittleEndian.PutUint32(buf[5:], uint32(len(payload)))
	copy(buf[9:], payload)
	fc.wmu.Lock()
	defer fc.wmu.Unlock()
	_, err := fc.conn.Write(buf)
	return err
}

// dropFramed closes every framed connection.
func (s *Server) dropFramed() {
	s.mu.Lock()
	conns := make([]*framedConn, 0, len(s.fconns))
	for fc := range s.fconns {
		conns = append(conns, fc)
	}
	s.mu.Unlock()
	for _, fc := range conns {
		_ = fc.conn.Close()
	}
}
