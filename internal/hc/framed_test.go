package hc_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// framedFixture is a fake core with one live terminal and a framed
// connection to it.
type framedFixture struct {
	srv    *hctest.Server
	framed *hc.Framed
	data   chan []byte
	ends   chan hc.StreamEnd
}

func newFramedFixture(t *testing.T) *framedFixture {
	t.Helper()
	srv := hctest.New(t)
	srv.Add(hc.Terminal{Address: "t1", State: "live", Argv: []string{"claude"}})
	framed, err := hc.NewClient(srv.Path).Framed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(framed.Close)
	return &framedFixture{srv: srv, framed: framed, data: make(chan []byte, 64), ends: make(chan hc.StreamEnd, 4)}
}

func (f *framedFixture) attach(t *testing.T, p hc.AttachParams) *hc.Stream {
	t.Helper()
	s, term, err := f.framed.Attach(context.Background(), p, hc.StreamHandlers{
		OnData: func(b []byte) { f.data <- append([]byte(nil), b...) },
		OnEnd:  func(e hc.StreamEnd) { f.ends <- e },
	})
	if err != nil {
		t.Fatal(err)
	}
	if term.Address != "t1" {
		t.Fatalf("attached to %q", term.Address)
	}
	return s
}

func (f *framedFixture) nextData(t *testing.T) string {
	t.Helper()
	select {
	case b := <-f.data:
		return string(b)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stream data")
	}
	return ""
}

func (f *framedFixture) nextEnd(t *testing.T) hc.StreamEnd {
	t.Helper()
	select {
	case e := <-f.ends:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the stream to end")
	}
	return hc.StreamEnd{}
}

// viewerFrames waits until the fake has seen n frames of kind from the
// viewer and returns them.
func viewerFrames(t *testing.T, srv *hctest.Server, kind byte, n int) []hctest.StreamFrame {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var got []hctest.StreamFrame
		for _, f := range srv.StreamFrames() {
			if f.Kind == kind {
				got = append(got, f)
			}
		}
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d frames of kind %d, have %d", n, kind, len(got))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFramedAttachGetsPaintThenOutput(t *testing.T) {
	f := newFramedFixture(t)
	f.srv.SetPaint("t1", "first paint")
	f.attach(t, hc.AttachParams{Terminal: "t1", Cols: 58, Rows: 30, Observe: true})

	if got := f.nextData(t); got != "first paint" {
		t.Fatalf("first data = %q", got)
	}
	f.srv.Emit("t1", []byte("live output"))
	if got := f.nextData(t); got != "live output" {
		t.Fatalf("second data = %q", got)
	}

	reqs := f.srv.Requests()
	var hello, attach map[string]any
	for _, r := range reqs {
		switch r.Method {
		case "hello":
			_ = json.Unmarshal(r.Params, &hello)
		case "terminal.attach":
			_ = json.Unmarshal(r.Params, &attach)
		}
	}
	if hello["framed"] != true || hello["generation"] != float64(1) {
		t.Errorf("hello params = %v", hello)
	}
	if attach["terminal"] != "t1" || attach["cols"] != float64(58) || attach["rows"] != float64(30) || attach["observe"] != true {
		t.Errorf("attach params = %v", attach)
	}
}

func TestFramedStreamSendsInputAndResize(t *testing.T) {
	f := newFramedFixture(t)
	s := f.attach(t, hc.AttachParams{Terminal: "t1", Cols: 58, Rows: 30})

	if err := s.Write([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if err := s.Resize(100, 40); err != nil {
		t.Fatal(err)
	}
	in := viewerFrames(t, f.srv, hctest.FrameData, 1)[0]
	if string(in.Payload) != "ls\r" || in.Terminal != "t1" || in.Attach != 1 {
		t.Errorf("input frame = %+v", in)
	}
	rz := viewerFrames(t, f.srv, hctest.FrameResize, 1)[0].Payload
	if len(rz) != 4 || binary.LittleEndian.Uint16(rz) != 100 || binary.LittleEndian.Uint16(rz[2:]) != 40 {
		t.Errorf("resize payload = %v, want 100x40 little endian", rz)
	}
}

func TestFramedReturnsCreditForWhatItConsumed(t *testing.T) {
	f := newFramedFixture(t)
	f.attach(t, hc.AttachParams{Terminal: "t1", Cols: 58, Rows: 30})

	// 8 x 100 KiB is three times the 256 KiB the stream starts with: it
	// only all arrives if the viewer hands the credit back.
	chunk := bytes.Repeat([]byte("x"), 100<<10)
	for range 8 {
		f.srv.Emit("t1", chunk)
	}
	total := 0
	for range 8 {
		total += len(f.nextData(t))
	}
	if total != 8*len(chunk) {
		t.Fatalf("received %d bytes, want %d", total, 8*len(chunk))
	}
	var credited uint32
	deadline := time.Now().Add(3 * time.Second)
	for credited < uint32(total) && time.Now().Before(deadline) {
		credited = 0
		for _, fr := range f.srv.StreamFrames() {
			if fr.Kind == hctest.FrameCredit {
				credited += binary.LittleEndian.Uint32(fr.Payload)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if credited != uint32(total) {
		t.Errorf("credited %d bytes, want %d", credited, total)
	}
}

func TestFramedDetachEndsTheStream(t *testing.T) {
	f := newFramedFixture(t)
	s := f.attach(t, hc.AttachParams{Terminal: "t1", Cols: 58, Rows: 30})
	if err := s.Detach(); err != nil {
		t.Fatal(err)
	}
	if end := f.nextEnd(t); end.Reason != "" || end.Err != nil {
		t.Errorf("detach ended as %+v, want an empty reason", end)
	}
	if n := f.srv.Streams("t1"); n != 0 {
		t.Errorf("%d streams still open", n)
	}
	viewerFrames(t, f.srv, hctest.FrameClose, 1)
}

func TestFramedProgramEndAndLostConnection(t *testing.T) {
	f := newFramedFixture(t)
	f.attach(t, hc.AttachParams{Terminal: "t1", Cols: 58, Rows: 30})
	code := 3
	f.srv.EndStreams("t1", "exited", &code)
	end := f.nextEnd(t)
	if end.Reason != "exited" || end.ExitCode == nil || *end.ExitCode != 3 {
		t.Errorf("program end = %+v", end)
	}

	f.attach(t, hc.AttachParams{Terminal: "t1", Cols: 58, Rows: 30})
	f.srv.Close()
	if end := f.nextEnd(t); !errors.Is(end.Err, hc.ErrFramedClosed) {
		t.Errorf("lost connection ended as %+v", end)
	}
	select {
	case <-f.framed.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("connection not done")
	}
	if err := f.framed.Call(context.Background(), "terminal.list", nil, nil); !errors.Is(err, hc.ErrFramedClosed) {
		t.Errorf("call on a closed connection = %v", err)
	}
}

func TestFramedCallsRunBesideAStream(t *testing.T) {
	f := newFramedFixture(t)
	f.attach(t, hc.AttachParams{Terminal: "t1", Cols: 58, Rows: 30})
	var list hc.ListResult
	if err := f.framed.Call(context.Background(), "terminal.list", map[string]any{"all": false}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Terminals) != 1 || list.Terminals[0].Address != "t1" {
		t.Errorf("list = %+v", list)
	}
	_, _, err := f.framed.Attach(context.Background(), hc.AttachParams{Terminal: "nope", Cols: 1, Rows: 1}, hc.StreamHandlers{})
	if !errors.Is(err, hc.ErrNotFound) {
		t.Errorf("attach to a missing terminal = %v, want ErrNotFound", err)
	}
	f.srv.Finish("t1", "exited")
	_, _, err = f.framed.Attach(context.Background(), hc.AttachParams{Terminal: "t1", Cols: 1, Rows: 1}, hc.StreamHandlers{})
	if !errors.Is(err, hc.ErrFinished) {
		t.Errorf("attach to a finished terminal = %v, want ErrFinished", err)
	}
}
