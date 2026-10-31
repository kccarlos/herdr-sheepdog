package hc

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// Framed mode (hc.openrpc.json's hello `framed`, "Framed mode" in its schema
// README): after the hello reply the connection carries frames, `[channel
// u32 LE][kind u8][len u32 LE][payload]`. Channel 0 is JSON-RPC, one message
// per DATA frame; every other channel is an attach stream.

// Frame kinds.
const (
	kindData   = 0
	kindCredit = 1
	kindClose  = 2
	kindResize = 3
)

// maxFramePayload is the largest payload a frame may carry.
const maxFramePayload = 16 << 20

const frameHeader = 9

// ErrFramedClosed is a call or stream write on a framed connection that
// has ended.
var ErrFramedClosed = errors.New("hc: framed connection closed")

// AttachParams are terminal.attach's params. Observe never types or
// resizes. Cols and Rows are the viewer's size.
type AttachParams struct {
	Terminal string `json:"terminal"`
	Cols     uint16 `json:"cols"`
	Rows     uint16 `json:"rows"`
	Observe  bool   `json:"observe,omitempty"`
	History  int    `json:"history,omitempty"`
}

// AttachResult is the terminal.attach result.
type AttachResult struct {
	Channel  uint32   `json:"channel"`
	Terminal Terminal `json:"terminal"`
}

// StreamEnd is how an attach stream ended. Reason is hc's (exited, killed,
// server_stop, ...), empty when the viewer detached; Err is set when the
// connection broke instead.
type StreamEnd struct {
	Reason   string
	ExitCode *int
	Err      error
}

// StreamHandlers receive a stream's bytes and its end. Both run on the
// connection's reader goroutine, in order: OnData must not block (hc
// drops a viewer that falls 4 MiB behind), and the bytes are only valid
// during the call. OnEnd runs once, after the last OnData.
type StreamHandlers struct {
	OnData func(p []byte)
	OnEnd  func(StreamEnd)
}

// Framed is a connection in framed mode. Safe for concurrent use.
type Framed struct {
	conn net.Conn
	in   *bufio.Reader

	wmu sync.Mutex // one frame at a time on the wire

	mu      sync.Mutex
	nextID  uint64
	calls   map[uint64]*framedCall
	streams map[uint32]*Stream
	err     error // why the connection ended; set once, under mu
	done    chan struct{}
}

// framedCall is a JSON-RPC call waiting for its reply. onResult, when set,
// runs on the reader goroutine before any later frame is read: an attach
// uses it to register its stream before the first paint can arrive.
type framedCall struct {
	reply    chan wireResponse
	onResult func(json.RawMessage) error
}

// Stream is one attach stream.
type Stream struct {
	f        *Framed
	channel  uint32
	handlers StreamHandlers
	ended    bool // under f.mu
}

// Framed opens a connection and switches it to framed mode. Close it when
// finished: that ends every stream on it.
func (c *Client) Framed(ctx context.Context) (*Framed, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(callCtx, "unix", c.Socket)
	if err != nil {
		return nil, fmt.Errorf("hc: framed: dial %s: %w", c.Socket, err)
	}
	stop := context.AfterFunc(callCtx, func() { _ = conn.Close() })
	defer stop()

	fail := func(format string, err error) (*Framed, error) {
		_ = conn.Close()
		if callCtx.Err() != nil {
			err = callCtx.Err()
		}
		return nil, fmt.Errorf(format, err)
	}
	const requestID uint64 = 1
	type helloParams struct {
		Generation int  `json:"generation"`
		Framed     bool `json:"framed"`
	}
	request := wireRequest{
		JSONRPC: "2.0",
		ID:      requestID,
		Method:  "hello",
		Params:  helloParams{Generation: 1, Framed: true},
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return fail("hc: framed: write hello: %w", err)
	}
	// The reply is one plain line; frames follow it on the same reader.
	in := bufio.NewReader(conn)
	line, err := in.ReadBytes('\n')
	if err != nil {
		return fail("hc: framed: read hello: %w", err)
	}
	var response wireResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return fail("hc: framed: decode hello: %w", err)
	}
	if response.Error != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("hc: framed: hello: %w", response.Error.toError())
	}
	if err := callCtx.Err(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("hc: framed: %w", err)
	}
	f := &Framed{
		conn:    conn,
		in:      in,
		nextID:  requestID,
		calls:   map[uint64]*framedCall{},
		streams: map[uint32]*Stream{},
		done:    make(chan struct{}),
	}
	go f.read()
	return f, nil
}

// Done is closed when the connection has ended.
func (f *Framed) Done() <-chan struct{} { return f.done }

// Close ends the connection and every stream on it.
func (f *Framed) Close() {
	f.shutdown(ErrFramedClosed)
	_ = f.conn.Close()
}

// Call invokes a JSON-RPC method on channel 0 and decodes its result into
// out (nil: ignored). Calls run side by side.
func (f *Framed) Call(ctx context.Context, method string, params, out any) error {
	return f.call(ctx, method, params, func(raw json.RawMessage) error {
		if out == nil {
			return nil
		}
		return json.Unmarshal(raw, out)
	})
}

// Attach opens an attach stream (terminal.attach). The handlers are live
// from the first byte of the first paint.
func (f *Framed) Attach(ctx context.Context, p AttachParams, h StreamHandlers) (*Stream, Terminal, error) {
	var s *Stream
	var term Terminal
	err := f.call(ctx, "terminal.attach", p, func(raw json.RawMessage) error {
		var res AttachResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return err
		}
		f.mu.Lock()
		s = &Stream{f: f, channel: res.Channel, handlers: h}
		term = res.Terminal
		f.streams[res.Channel] = s
		f.mu.Unlock()
		return nil
	})
	if err != nil {
		f.mu.Lock()
		registered := s
		f.mu.Unlock()
		if registered != nil {
			// Registered, but the caller gave up before the reply: let go.
			_ = registered.Detach()
		}
		return nil, Terminal{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return s, term, nil
}

func (f *Framed) call(ctx context.Context, method string, params any, onResult func(json.RawMessage) error) error {
	fc := &framedCall{reply: make(chan wireResponse, 1), onResult: onResult}
	f.mu.Lock()
	if f.err != nil {
		f.mu.Unlock()
		return fmt.Errorf("hc: %s: %w", method, f.err)
	}
	f.nextID++
	id := f.nextID
	f.calls[id] = fc
	f.mu.Unlock()
	forget := func() {
		f.mu.Lock()
		delete(f.calls, id)
		f.mu.Unlock()
	}

	payload, err := json.Marshal(wireRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		forget()
		return fmt.Errorf("hc: %s: encode request: %w", method, err)
	}
	if err := f.write(0, kindData, payload); err != nil {
		forget()
		return fmt.Errorf("hc: %s: write request: %w", method, err)
	}
	select {
	case response := <-fc.reply:
		if response.Error != nil {
			return fmt.Errorf("hc: %s: %w", method, response.Error.toError())
		}
		return nil
	case <-ctx.Done():
		forget()
		return fmt.Errorf("hc: %s: %w", method, ctx.Err())
	case <-f.done:
		forget()
		return fmt.Errorf("hc: %s: %w", method, f.cause())
	}
}

// Write sends bytes to the program (DATA on the stream's channel).
func (s *Stream) Write(p []byte) error {
	for len(p) > 0 {
		n := min(len(p), maxFramePayload)
		if err := s.f.write(s.channel, kindData, p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// Resize reports the viewer's size (RESIZE). A viewer that is not
// observing becomes the active one, which sets the terminal's size.
func (s *Stream) Resize(cols, rows uint16) error {
	var b [4]byte
	binary.LittleEndian.PutUint16(b[0:], cols)
	binary.LittleEndian.PutUint16(b[2:], rows)
	return s.f.write(s.channel, kindResize, b[:])
}

// Detach leaves the terminal (CLOSE); hc answers with an empty CLOSE,
// which ends the stream with an empty Reason.
func (s *Stream) Detach() error {
	return s.f.write(s.channel, kindClose, nil)
}

func (f *Framed) write(channel uint32, kind byte, payload []byte) error {
	buf := make([]byte, frameHeader+len(payload))
	binary.LittleEndian.PutUint32(buf[0:], channel)
	buf[4] = kind
	binary.LittleEndian.PutUint32(buf[5:], uint32(len(payload)))
	copy(buf[frameHeader:], payload)
	f.wmu.Lock()
	defer f.wmu.Unlock()
	select {
	case <-f.done:
		return f.cause()
	default:
	}
	if _, err := f.conn.Write(buf); err != nil {
		return fmt.Errorf("%w: %w", ErrFramedClosed, err)
	}
	return nil
}

// read is the reader goroutine: it routes frames until the connection ends.
func (f *Framed) read() {
	var hdr [frameHeader]byte
	var payload []byte
	for {
		if _, err := io.ReadFull(f.in, hdr[:]); err != nil {
			f.shutdown(fmt.Errorf("%w: %w", ErrFramedClosed, err))
			return
		}
		channel := binary.LittleEndian.Uint32(hdr[0:])
		kind := hdr[4]
		n := binary.LittleEndian.Uint32(hdr[5:])
		if n > maxFramePayload {
			f.shutdown(fmt.Errorf("%w: frame of %d bytes", ErrFramedClosed, n))
			_ = f.conn.Close()
			return
		}
		if cap(payload) < int(n) {
			payload = make([]byte, n)
		}
		payload = payload[:n]
		if _, err := io.ReadFull(f.in, payload); err != nil {
			f.shutdown(fmt.Errorf("%w: %w", ErrFramedClosed, err))
			return
		}
		switch {
		case channel == 0 && kind == kindData:
			f.reply(payload)
		case channel == 0:
		case kind == kindData:
			f.data(channel, payload)
		case kind == kindClose:
			f.closed(channel, payload)
		}
	}
}

// reply hands a JSON-RPC response to its call. Notifications (no id) and
// replies nobody waits for are dropped.
func (f *Framed) reply(payload []byte) {
	var response wireResponse
	if json.Unmarshal(payload, &response) != nil || response.ID == nil {
		return
	}
	f.mu.Lock()
	fc := f.calls[*response.ID]
	delete(f.calls, *response.ID)
	f.mu.Unlock()
	if fc == nil {
		return
	}
	if response.Error == nil && fc.onResult != nil {
		if err := fc.onResult(response.Result); err != nil {
			response.Error = &wireError{Message: "decode result: " + err.Error()}
		}
	}
	fc.reply <- response
}

// data delivers stream bytes, then returns the credit for them: the
// handler has consumed them by the time it returns.
func (f *Framed) data(channel uint32, p []byte) {
	f.mu.Lock()
	s := f.streams[channel]
	f.mu.Unlock()
	if s == nil {
		return
	}
	if s.handlers.OnData != nil {
		s.handlers.OnData(p)
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(len(p)))
	_ = f.write(channel, kindCredit, b[:])
}

// closed ends a stream: hc's CLOSE carries a JSON reason, or is empty when
// answering the viewer's own.
func (f *Framed) closed(channel uint32, payload []byte) {
	var end StreamEnd
	if len(payload) > 0 {
		var r struct {
			Reason   string `json:"reason"`
			ExitCode *int   `json:"exit_code"`
		}
		if json.Unmarshal(payload, &r) == nil {
			end.Reason, end.ExitCode = r.Reason, r.ExitCode
		}
	}
	f.mu.Lock()
	s := f.streams[channel]
	delete(f.streams, channel)
	f.mu.Unlock()
	s.finish(end)
}

// finish reports a stream's end once.
func (s *Stream) finish(end StreamEnd) {
	if s == nil {
		return
	}
	s.f.mu.Lock()
	already := s.ended
	s.ended = true
	s.f.mu.Unlock()
	if !already && s.handlers.OnEnd != nil {
		s.handlers.OnEnd(end)
	}
}

// shutdown records why the connection ended and ends every stream and
// call, once.
func (f *Framed) shutdown(err error) {
	f.mu.Lock()
	if f.err != nil {
		f.mu.Unlock()
		return
	}
	f.err = err
	streams := make([]*Stream, 0, len(f.streams))
	for _, s := range f.streams {
		streams = append(streams, s)
	}
	f.streams = map[uint32]*Stream{}
	close(f.done)
	f.mu.Unlock()
	for _, s := range streams {
		s.finish(StreamEnd{Err: err})
	}
}

func (f *Framed) cause() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	return ErrFramedClosed
}
