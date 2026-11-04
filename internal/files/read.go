package files

import (
	"bytes"
	"io"
	"os"
	"time"
	"unicode/utf8"
)

// The read limits (docs/dev/design/file-browser.md, section 3).
const (
	// TextMax caps the text one read returns; past it Truncated is set.
	TextMax = 1 << 20
	// ImageMax caps an image served as bytes; past it is ErrTooLarge.
	ImageMax = 16 << 20
	// sniffLen is how much of a file decides its kind.
	sniffLen = 8000
)

// The kinds of a read file.
const (
	KindText   = "text"
	KindImage  = "image"
	KindBinary = "binary"
)

// File is one read file. Only a text file carries Text.
type File struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text,omitempty"`
	Truncated bool      `json:"truncated"`
}

// Read reads the regular file at rel: its kind from its first bytes (an
// image by its format's signature, else text when it has no NUL and is
// UTF-8, else binary) and, for text, at most TextMax bytes of it. SVG is
// text.
func (r *Root) Read(rel string) (File, error) {
	f, out, err := r.open(rel)
	if err != nil {
		return File{}, err
	}
	defer func() { _ = f.Close() }()
	head, err := readUpTo(f, sniffLen)
	if err != nil {
		return File{}, err
	}
	if out.Kind = kindOf(head, len(head) == sniffLen); out.Kind != KindText {
		return out, nil
	}
	rest, err := readUpTo(f, TextMax+1-len(head))
	if err != nil {
		return File{}, err
	}
	text := append(head, rest...)
	if len(text) > TextMax {
		text, out.Truncated = trimRune(text[:TextMax]), true
	}
	out.Text = string(bytes.ToValidUTF8(text, []byte("\uFFFD")))
	return out, nil
}

// Image opens the image at rel to serve its bytes: the file's metadata,
// its content type (from its bytes, never its name) and the open file,
// which the caller closes and reads at most Size bytes of. A file that is
// not an image is ErrNotImage, one over ImageMax ErrTooLarge.
func (r *Root) Image(rel string) (File, string, *os.File, error) {
	f, out, err := r.open(rel)
	if err != nil {
		return File{}, "", nil, err
	}
	head := make([]byte, 16)
	n, _ := f.ReadAt(head, 0)
	ctype := imageType(head[:n])
	switch {
	case ctype == "":
		err = ErrNotImage
	case out.Size > ImageMax:
		err = ErrTooLarge
	}
	if err != nil {
		_ = f.Close()
		return File{}, "", nil, err
	}
	out.Kind = KindImage
	return out, ctype, f, nil
}

// open resolves rel and opens it, refusing anything but a regular file.
func (r *Root) open(rel string) (*os.File, File, error) {
	p, err := r.resolve(rel)
	if err != nil {
		return nil, File{}, err
	}
	f, info, err := r.openAt(p)
	if err != nil {
		return nil, File{}, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, File{}, ErrNotFile
	}
	return f, File{Path: rel, Size: info.Size(), Modified: modified(info)}, nil
}

// readUpTo reads at most n bytes, fewer at the end of the file.
func readUpTo(f io.Reader, n int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(f, int64(n)))
	if err != nil {
		return nil, osErr(err)
	}
	return b, nil
}

// kindOf decides a file's kind from its first bytes; cut says the file
// goes on past them.
func kindOf(head []byte, cut bool) string {
	switch {
	case imageType(head) != "":
		return KindImage
	case bytes.IndexByte(head, 0) >= 0:
		return KindBinary
	case cut:
		head = trimRune(head)
	}
	if !utf8.Valid(head) {
		return KindBinary
	}
	return KindText
}

// imageType names the image format b starts with ("" for none): the
// formats the app shows, by their signatures.
func imageType(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// trimRune drops a rune cut in half at the end of b.
func trimRune(b []byte) []byte {
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			return b
		}
	}
	return b
}
