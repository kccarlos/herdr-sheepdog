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
		{"silence", Silence(true), `"type":"silence","on":true`},
		{"speaking", Speaking(true), `{"type":"speaking","on":true}`},
		{"playback", Playback(PlaybackFlushed, 0), `{"type":"playback","state":"flushed","played_ms":0}`},
		{"playback-drained", Playback(PlaybackDrained, 4200), `"state":"drained","played_ms":4200`},
		{"cancel", Cancel(), `{"type":"cancel"}`},
		{"text", SimulateText("yes go ahead"), `"text":"yes go ahead"`},
		{"ping", Ping(), `{"type":"ping"}`},
		{"flush", Flush(), `{"type":"flush"}`},
		{"status", Status(VoiceReconnecting, true, ""), `"voice":"reconnecting","muted":true`},
		{"status-reason", Status(VoiceClosed, false, "replaced"), `"reason":"replaced"`},
		{"transcript", Transcript(DirOut, "hello", false), `"dir":"out","final":false`},
		{"error", Error("boom"), `"message":"boom"`},
		{"pong", Pong(), `{"type":"pong"}`},
		{"client-ready", ClientReady(1, ClientInfo{Name: "sheepdog-ios", Version: "0.2.0", Platform: "ios"}), `{"type":"client-ready","protocol":1,"client":{"name":"sheepdog-ios","version":"0.2.0","platform":"ios"}}`},
		{"bot-ready", BotReady(1, 1, "0.4.2", []string{FeaturePlayback}), `{"type":"bot-ready","protocol":1,"min_protocol":1,"daemon":{"version":"0.4.2"},"features":["playback"]}`},
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
			if back.Type != tc.msg.Type || back.Text != tc.msg.Text || back.Voice != tc.msg.Voice || back.Dir != tc.msg.Dir || back.State != tc.msg.State {
				t.Fatalf("round trip mismatch: %+v vs %+v", back, tc.msg)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	bad := []string{
		`{}`,
		`{"type":"mute"}`,
		`{"type":"silence"}`,
		`{"type":"speaking"}`,
		`{"type":"playback"}`,
		`{"type":"playback","state":"paused","played_ms":1}`,
		`{"type":"playback","state":"drained"}`,
		`{"type":"playback","state":"flushed","played_ms":-5}`,
		`{"type":"text"}`,
		`{"type":"status","voice":"weird"}`,
		`{"type":"transcript","dir":"sideways"}`,
		`{"type":"error"}`,
		`{"type":"nope"}`,
		`{"type":"client-ready"}`,
		`{"type":"bot-ready","protocol":1}`,
		`not json`,
	}
	for _, in := range bad {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%s) accepted", in)
		}
	}
}

func TestParsePlaybackStartedNeedsNoPlayed(t *testing.T) {
	m, err := Parse([]byte(`{"type":"playback","state":"started"}`))
	if err != nil || m.State != PlaybackStarted || m.PlayedMs != nil {
		t.Fatalf("started without played_ms: %+v %v", m, err)
	}
}

func TestPingAEC(t *testing.T) {
	m, err := Parse([]byte(`{"type":"ping","aec":"false"}`))
	if err != nil || m.AEC != AECOff {
		t.Fatalf("Parse = %+v, %v", m, err)
	}
	for v, want := range map[string]bool{AECOn: true, AECOff: true, AECUnknown: true, "": false, "yes": false} {
		if ValidAEC(v) != want {
			t.Errorf("ValidAEC(%q) = %v", v, !want)
		}
	}
}
