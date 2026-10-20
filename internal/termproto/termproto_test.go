package termproto

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	f := Frame{Ch: 65535, Seq: 1<<32 - 1, Cols: 58, Rows: 30, Full: true, Data: []byte("\x1b[?2026hx")}
	b := EncodeFrame(f)
	want := []byte{0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0, 58, 0, 30, 1}
	if !bytes.Equal(b[:12], want) {
		t.Fatalf("header = % x, want % x", b[:12], want)
	}
	g, err := DecodeFrame(b)
	if err != nil || g.Ch != f.Ch || g.Seq != f.Seq || g.Cols != 58 || g.Rows != 30 || !g.Full || !bytes.Equal(g.Data, f.Data) {
		t.Fatalf("decoded = %+v, %v", g, err)
	}
	if _, err := DecodeFrame([]byte{0x02, 0, 1}); err == nil {
		t.Error("input decoded as a frame")
	}
}

func TestInput(t *testing.T) {
	b := EncodeInput(7, []byte("ls\r"))
	if !bytes.Equal(b, []byte{0x02, 0, 7, 'l', 's', '\r'}) {
		t.Fatalf("input = % x", b)
	}
	ch, data, err := DecodeInput(b)
	if err != nil || ch != 7 || string(data) != "ls\r" {
		t.Fatalf("decoded %d %q %v", ch, data, err)
	}
	if _, _, err := DecodeInput(EncodeInput(1, make([]byte, MaxInput+1))); err == nil {
		t.Error("oversized input accepted")
	}
	if _, _, err := DecodeInput([]byte{0x02, 0}); err == nil {
		t.Error("short input accepted")
	}
}

func TestValidateRejects(t *testing.T) {
	zero, p := 0, 1
	for name, m := range map[string]Message{
		"hello without protocol": {Type: TypeHello},
		"open ch 0":              {Type: TypeOpen, Target: &Target{PaneID: "w1:p1"}, Mode: ModeControl, Cols: 1, Rows: 1},
		"open both targets":      {Type: TypeOpen, Ch: 1, Target: &Target{PaneID: "w1:p1", Shell: &Shell{}}, Mode: ModeControl, Cols: 1, Rows: 1},
		"open bad mode":          {Type: TypeOpen, Ch: 1, Target: &Target{PaneID: "w1:p1"}, Mode: "write", Cols: 1, Rows: 1},
		"open too big":           {Type: TypeOpen, Ch: 1, Target: &Target{PaneID: "w1:p1"}, Mode: ModeControl, Cols: 1001, Rows: 1},
		"resize no rows":         {Type: TypeResize, Ch: 1, Cols: 10},
		"scroll sideways":        {Type: TypeScroll, Ch: 1, Dir: "left", Lines: 1},
		"scroll col only":        {Type: TypeScroll, Ch: 1, Dir: "up", Lines: 1, Col: &zero},
		"mouse no row":           {Type: TypeMouse, Ch: 1, Action: "down", Col: &zero},
		"mouse click":            {Type: TypeMouse, Ch: 1, Action: "click", Col: &zero, Row: &zero},
		"unknown":                {Type: "term-nope", Protocol: &p},
	} {
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
