package wsproto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// contractDoc is docs/dev/ws-contract.md, relative to this package.
var contractDoc = filepath.Join("..", "..", "..", "docs", "dev", "ws-contract.md")

// docExample is one line of a fenced json block in the contract.
type docExample struct {
	line int
	text string
}

// contractExamples returns every line of every ```json block in the
// contract doc (fences may be indented, inside a list item).
func contractExamples(t *testing.T) []docExample {
	t.Helper()
	b, err := os.ReadFile(contractDoc)
	if err != nil {
		t.Fatal(err)
	}
	var out []docExample
	in := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case !in && line == "```json":
			in = true
		case in && line == "```":
			in = false
		case in && line != "":
			out = append(out, docExample{line: n, text: line})
		}
	}
	if in {
		t.Fatal("unterminated ```json block")
	}
	return out
}

// TestContractExamplesRoundTrip keeps docs/dev/ws-contract.md and this
// package from drifting: every example parses into Message with no
// unknown field, validates, and re-encodes to the same JSON (ignoring
// key order), so a field the type would drop fails too.
func TestContractExamplesRoundTrip(t *testing.T) {
	examples := contractExamples(t)
	if len(examples) < 20 {
		t.Fatalf("only %d json examples in %s", len(examples), contractDoc)
	}
	seen := map[string]bool{}
	for _, ex := range examples {
		typ, err := strictRoundTrip(ex.text)
		if err != nil {
			t.Errorf("ws-contract.md:%d: %v\n  %s", ex.line, err, ex.text)
			continue
		}
		seen[typ] = true
	}
	for _, typ := range []string{
		TypeClientReady, TypeBotReady, TypeMute, TypeSilence, TypeSpeaking, TypePlayback, TypeDrain, TypeCancel,
		TypeText, TypePing, TypeFlush, TypeStatus, TypeTranscript, TypeError, TypePong,
		TypeScreenSubscribe, TypeScreenUnsubscribe, TypeScreen, TypeScreenError, TypeVisibility, TypeCard, TypeVoiceCommand,
	} {
		if !seen[typ] {
			t.Errorf("ws-contract.md has no json example of %q", typ)
		}
	}
}

// strictRoundTrip parses one example strictly (no unknown fields, one
// value), validates and re-encodes it, and fails unless the result is the
// same JSON. It returns the message type.
func strictRoundTrip(text string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var m Message
	if err := dec.Decode(&m); err != nil {
		return "", err
	}
	if dec.More() {
		return "", errors.New("more than one value on the line")
	}
	b, err := m.Encode()
	if err != nil {
		return "", err
	}
	var want, got any
	if err := json.Unmarshal([]byte(text), &want); err != nil {
		return "", err
	}
	if err := json.Unmarshal(b, &got); err != nil {
		return "", err
	}
	if !reflect.DeepEqual(want, got) {
		return "", fmt.Errorf("round trip changed the message; the code sends %s", b)
	}
	return m.Type, nil
}

// The checker itself must catch drift: an unknown field, a field the
// type drops, a wrong type, a nested unknown field.
func TestContractCheckerCatchesDrift(t *testing.T) {
	for _, bad := range []string{
		`{"type":"pong","extra":1}`,
		`{"type":"status","voice":"connected","muted":false,"stalled":false}`,
		`{"type":"mute","on":"yes"}`,
		`{"type":"client-ready","protocol":1,"client":{"name":"x","version":"1","platform":"ios","os":"18"}}`,
		`{"type":"pong"} {"type":"pong"}`,
	} {
		if _, err := strictRoundTrip(bad); err == nil {
			t.Errorf("%s passed the round trip", bad)
		}
	}
	if typ, err := strictRoundTrip(`{"type":"bot-ready","protocol":1,"min_protocol":1,"daemon":{"version":"x"},"features":["playback"]}`); err != nil || typ != TypeBotReady {
		t.Errorf("good bot-ready: %q %v", typ, err)
	}
}

func TestAudioRunFramingAndMessages(t *testing.T) {
	pcm := []byte{0x10, 0x20, 0x30, 0x40}
	packed := PackAudioRun(0x01020304, pcm)
	if len(packed) != AudioRunHeaderBytes+len(pcm) {
		t.Fatalf("len(packed) = %d, want %d", len(packed), AudioRunHeaderBytes+len(pcm))
	}
	if packed[0] != 0x04 || packed[1] != 0x03 || packed[2] != 0x02 || packed[3] != 0x01 {
		t.Fatalf("header = %x, want little-endian 04030201", packed[:4])
	}
	runID, gotPCM, ok := UnpackAudioRun(packed)
	if !ok || runID != 0x01020304 || !bytes.Equal(gotPCM, pcm) {
		t.Fatalf("UnpackAudioRun = (%d, %x, %v)", runID, gotPCM, ok)
	}
	if _, _, ok := UnpackAudioRun([]byte{1, 2, 3}); ok {
		t.Fatal("UnpackAudioRun accepted < 4 byte frame")
	}

	fl := FlushRun(7)
	b, err := fl.Encode()
	if err != nil || string(b) != `{"type":"flush","run_id":7}` {
		t.Fatalf("FlushRun(7) = %s, %v", b, err)
	}
	if b0, err := FlushRun(0).Encode(); err != nil || string(b0) != `{"type":"flush"}` {
		t.Fatalf("FlushRun(0) = %s, %v", b0, err)
	}
	pb := PlaybackRun(PlaybackFlushed, 420, 7)
	parsed, err := Parse(mustEncode(t, pb))
	if err != nil || parsed.RunID == nil || *parsed.RunID != 7 || *parsed.PlayedMs != 420 {
		t.Fatalf("PlaybackRun round trip = %+v, %v", parsed, err)
	}
	cr := ClientReadyFeatures(ProtocolVersion, ClientInfo{Name: "x", Version: "1", Platform: "web"}, FeatureRunID)
	parsedCR, err := Parse(mustEncode(t, cr))
	if err != nil || len(parsedCR.Features) != 1 || parsedCR.Features[0] != FeatureRunID {
		t.Fatalf("ClientReadyFeatures round trip = %+v, %v", parsedCR, err)
	}
}

func mustEncode(t *testing.T, m Message) []byte {
	t.Helper()
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
