package voiceclient

import (
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

func frameOf(v int16) []int16 {
	f := make([]int16, FrameSamples)
	for i := range f {
		f[i] = v
	}
	return f
}

func msgTypes(ms []wsproto.Message) []string {
	var out []string
	for _, m := range ms {
		s := m.Type
		if m.On != nil {
			if *m.On {
				s += ":on"
			} else {
				s += ":off"
			}
		}
		out = append(out, s)
	}
	return out
}

func TestMicGateAndPreroll(t *testing.T) {
	m := &mic{sendSpeaking: true}
	// Loud speech opens the detector; the gate is open from the start.
	out := m.push(frameOf(2000), 2000)
	if got := msgTypes(out.msgs); len(got) != 1 || got[0] != "speaking:on" || len(out.frames) != 1 {
		t.Fatalf("first speech frame: %v, %d frames", got, len(out.frames))
	}
	// 300 ms of digital silence closes the gate (the 8th frame) and,
	// after 500 ms, the detector.
	var msgs []string
	sent := 0
	for i := 0; i < 15; i++ {
		o := m.push(frameOf(0), 0)
		msgs = append(msgs, msgTypes(o.msgs)...)
		sent += len(o.frames)
	}
	if len(msgs) != 2 || msgs[0] != "silence:on" || msgs[1] != "speaking:off" || sent != 7 {
		t.Fatalf("silence: msgs %v, %d frames sent", msgs, sent)
	}
	// The next loud frame reopens with the last 100 ms (2 frames) of
	// pre-roll, then itself.
	o := m.push(frameOf(500), 0)
	if got := msgTypes(o.msgs); len(got) != 1 || got[0] != "silence:off" || len(o.frames) != 3 {
		t.Fatalf("reopen: %v, %d frames", got, len(o.frames))
	}
	if m.speaking {
		t.Fatal("room noise opened the speech detector")
	}
}

func TestHeardText(t *testing.T) {
	frags := []Fragment{{Text: "Auth finished", AudioMS: 0}, {Text: " its tests and", AudioMS: 1000}, {Text: " all ten pass.", AudioMS: 2000}}
	if got := heardText(frags, 3000, 3000); got != "Auth finished its tests and all ten pass." {
		t.Fatalf("full = %q", got)
	}
	if got := heardText(frags, 1500, 3000); got != "Auth finished its tests" {
		t.Fatalf("cut = %q", got)
	}
	if got := heardText([]Fragment{{Text: "测试全部通过了", AudioMS: 0}}, 500, 1000); got != "测试全" {
		t.Fatalf("zh cut = %q", got)
	}
}
