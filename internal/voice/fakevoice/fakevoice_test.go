package fakevoice

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

func TestStartReportsConnected(t *testing.T) {
	s := New()
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ev := <-s.Events()
	if st, ok := ev.(voice.EventStatus); !ok || st.Status != voice.StatusConnected {
		t.Fatalf("first event = %#v, want a connected status", ev)
	}
	if s.Speaking() {
		t.Fatal("speaking before any text")
	}
}

func TestSendTextSpeaksThenStops(t *testing.T) {
	s := New()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	if err := s.SendText("hello"); err != nil {
		t.Fatal(err)
	}
	if !s.Speaking() {
		t.Fatal("not speaking after text")
	}
	in, out := <-s.Events(), <-s.Events()
	if tr, ok := in.(voice.EventTranscript); !ok || tr.Dir != voice.DirIn || tr.Text != "hello" {
		t.Fatalf("in = %#v", in)
	}
	if tr, ok := out.(voice.EventTranscript); !ok || tr.Dir != voice.DirOut {
		t.Fatalf("out = %#v", out)
	}
	now = now.Add(SpeakFor + time.Second)
	if s.Speaking() {
		t.Fatal("still speaking after SpeakFor")
	}
}

func TestCloseClosesChannelsAndIsIdempotent(t *testing.T) {
	s := New()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-s.Events(); ok {
		t.Fatal("events not closed")
	}
	_ = s.SendText("after close") // must not panic
}
