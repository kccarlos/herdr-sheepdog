package wsproto

import (
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want string // substrings that must appear on the wire
	}{
		{"mute", Mute(true), `"type":"mute","on":true`},
		{"cancel", Cancel(), `{"type":"cancel"}`},
		{"text", SimulateText("yes go ahead"), `"text":"yes go ahead"`},
		{"ping", Ping(), `{"type":"ping"}`},
		{"flush", Flush(), `{"type":"flush"}`},
		{"status", Status(VoiceReconnecting, true, ""), `"voice":"reconnecting","muted":true`},
		{"status-reason", Status(VoiceClosed, false, "replaced"), `"reason":"replaced"`},
		{"transcript", Transcript(DirOut, "hello", false), `"dir":"out","final":false`},
		{"error", Error("boom"), `"message":"boom"`},
		{"pong", Pong(), `{"type":"pong"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := tc.msg.Encode()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if !strings.Contains(string(b), tc.want) {
				t.Fatalf("wire %s does not contain %s", b, tc.want)
			}
			back, err := Parse(b)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if back.Type != tc.msg.Type || back.Text != tc.msg.Text || back.Voice != tc.msg.Voice || back.Dir != tc.msg.Dir {
				t.Fatalf("round trip mismatch: %+v vs %+v", back, tc.msg)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	bad := []string{
		`{}`,
		`{"type":"mute"}`,
		`{"type":"text"}`,
		`{"type":"status","voice":"weird"}`,
		`{"type":"transcript","dir":"sideways"}`,
		`{"type":"error"}`,
		`{"type":"nope"}`,
		`not json`,
	}
	for _, in := range bad {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%s) accepted", in)
		}
	}
}
