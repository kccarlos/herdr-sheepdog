// Package wav reads and writes the one WAV flavour sheepdog cares about:
// RIFF/WAVE, PCM (format 1), 16-bit, mono. It exists for voice-test and
// tests; the daemon itself never touches files.
package wav

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Read parses a WAV stream and returns its sample rate and raw PCM16 LE
// mono samples. Anything but PCM/16-bit/mono is rejected.
func Read(r io.Reader) (rate int, pcm []byte, err error) {
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil {
		return 0, nil, fmt.Errorf("wav: header: %w", err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return 0, nil, errors.New("wav: not a RIFF/WAVE file")
	}
	var (
		gotFmt   bool
		channels uint16
		bits     uint16
	)
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, nil, errors.New("wav: no data chunk")
			}
			return 0, nil, fmt.Errorf("wav: chunk header: %w", err)
		}
		id := string(hdr[0:4])
		size := binary.LittleEndian.Uint32(hdr[4:8])
		switch id {
		case "fmt ":
			if size < 16 {
				return 0, nil, errors.New("wav: short fmt chunk")
			}
			body := make([]byte, size+size%2)
			if _, err := io.ReadFull(r, body); err != nil {
				return 0, nil, fmt.Errorf("wav: fmt chunk: %w", err)
			}
			format := binary.LittleEndian.Uint16(body[0:2])
			channels = binary.LittleEndian.Uint16(body[2:4])
			rate = int(binary.LittleEndian.Uint32(body[4:8]))
			bits = binary.LittleEndian.Uint16(body[14:16])
			if format != 1 {
				return 0, nil, fmt.Errorf("wav: format %d is not PCM", format)
			}
			if channels != 1 {
				return 0, nil, fmt.Errorf("wav: %d channels, want mono", channels)
			}
			if bits != 16 {
				return 0, nil, fmt.Errorf("wav: %d bits per sample, want 16", bits)
			}
			gotFmt = true
		case "data":
			if !gotFmt {
				return 0, nil, errors.New("wav: data chunk before fmt chunk")
			}
			pcm = make([]byte, size)
			n, err := io.ReadFull(r, pcm)
			if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, nil, fmt.Errorf("wav: data chunk: %w", err)
			}
			pcm = pcm[:n&^1] // ignore a truncated trailing byte
			return rate, pcm, nil
		default:
			if _, err := io.CopyN(io.Discard, r, int64(size+size%2)); err != nil {
				return 0, nil, fmt.Errorf("wav: skip %q chunk: %w", id, err)
			}
		}
	}
}

// Writer streams PCM16 mono samples into a WAV file. The header is
// written with placeholder sizes and patched by Close, so the underlying
// writer must support Seek (an *os.File does).
type Writer struct {
	w    io.WriteSeeker
	rate int
	n    int64
	err  error
}

// NewWriter writes the header and returns a Writer for the given rate.
func NewWriter(w io.WriteSeeker, rate int) (*Writer, error) {
	wr := &Writer{w: w, rate: rate}
	if err := wr.writeHeader(0); err != nil {
		return nil, err
	}
	return wr, nil
}

func (wr *Writer) writeHeader(dataLen uint32) error {
	var h [44]byte
	copy(h[0:4], "RIFF")
	binary.LittleEndian.PutUint32(h[4:8], 36+dataLen)
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	binary.LittleEndian.PutUint32(h[16:20], 16)
	binary.LittleEndian.PutUint16(h[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:24], 1) // mono
	binary.LittleEndian.PutUint32(h[24:28], uint32(wr.rate))
	binary.LittleEndian.PutUint32(h[28:32], uint32(wr.rate)*2) // byte rate
	binary.LittleEndian.PutUint16(h[32:34], 2)                 // block align
	binary.LittleEndian.PutUint16(h[34:36], 16)                // bits
	copy(h[36:40], "data")
	binary.LittleEndian.PutUint32(h[40:44], dataLen)
	_, err := wr.w.Write(h[:])
	return err
}

// Write appends PCM16 samples.
func (wr *Writer) Write(pcm []byte) (int, error) {
	if wr.err != nil {
		return 0, wr.err
	}
	n, err := wr.w.Write(pcm)
	wr.n += int64(n)
	if err != nil {
		wr.err = err
	}
	return n, err
}

// Bytes returns how many PCM bytes were written so far.
func (wr *Writer) Bytes() int64 { return wr.n }

// Close patches the RIFF and data sizes. It does not close the underlying
// file.
func (wr *Writer) Close() error {
	if wr.err != nil {
		return wr.err
	}
	if _, err := wr.w.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if wr.n > 0xFFFFFFFF-36 {
		return errors.New("wav: file too large")
	}
	return wr.writeHeader(uint32(wr.n))
}
