package wav

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(f, 24000)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]byte, 4800) // 100 ms
	for i := range pcm {
		pcm[i] = byte(i)
	}
	if _, err := w.Write(pcm[:1000]); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(pcm[1000:]); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if w.Bytes() != 4800 {
		t.Errorf("Bytes = %d", w.Bytes())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 44+4800 {
		t.Fatalf("file size %d", len(data))
	}
	if binary.LittleEndian.Uint32(data[4:8]) != 36+4800 || binary.LittleEndian.Uint32(data[40:44]) != 4800 {
		t.Error("sizes not patched")
	}
	rate, got, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if rate != 24000 || !bytes.Equal(got, pcm) {
		t.Errorf("rate=%d len=%d", rate, len(got))
	}
}

func TestReadRejects(t *testing.T) {
	mk := func(format, channels, bits uint16, rate uint32, extra []byte) []byte {
		var b bytes.Buffer
		body := make([]byte, 16)
		binary.LittleEndian.PutUint16(body[0:2], format)
		binary.LittleEndian.PutUint16(body[2:4], channels)
		binary.LittleEndian.PutUint32(body[4:8], rate)
		binary.LittleEndian.PutUint16(body[14:16], bits)
		b.WriteString("RIFF")
		_ = binary.Write(&b, binary.LittleEndian, uint32(0))
		b.WriteString("WAVE")
		b.Write(extra)
		b.WriteString("fmt ")
		_ = binary.Write(&b, binary.LittleEndian, uint32(16))
		b.Write(body)
		b.WriteString("data")
		_ = binary.Write(&b, binary.LittleEndian, uint32(4))
		b.Write([]byte{1, 2, 3, 4})
		return b.Bytes()
	}
	// LIST chunk before fmt is skipped.
	list := append([]byte("LIST"), 3, 0, 0, 0, 'a', 'b', 'c', 0)
	if rate, pcm, err := Read(bytes.NewReader(mk(1, 1, 16, 16000, list))); err != nil || rate != 16000 || len(pcm) != 4 {
		t.Errorf("valid file with LIST chunk: rate=%d len=%d err=%v", rate, len(pcm), err)
	}
	cases := map[string][]byte{
		"float":    mk(3, 1, 16, 16000, nil),
		"stereo":   mk(1, 2, 16, 16000, nil),
		"8bit":     mk(1, 1, 8, 16000, nil),
		"not riff": []byte("RIFX\x00\x00\x00\x00WAVE"),
		"short":    []byte("RI"),
		"no data":  []byte("RIFF\x00\x00\x00\x00WAVE"),
	}
	for name, in := range cases {
		if _, _, err := Read(bytes.NewReader(in)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
