package web

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Attachments (docs/dev/ws-contract.md "Attachments"): the app uploads a
// photo or a file for a tracked agent, the daemon keeps it under
// <data_dir>/attachments/<pane>/ and answers its path on the computer, and
// the app puts that path into the prompt. Claude Code and Codex read image
// and file paths given in a prompt.

const (
	// MaxAttachmentBytes is the largest file the daemon accepts.
	MaxAttachmentBytes = 20 << 20
	// attachmentMaxAge is how long an attachment is kept.
	attachmentMaxAge = 7 * 24 * time.Hour
	// attachmentNameMax caps the kept part of the client's file name.
	attachmentNameMax = 64
)

// AttachmentResponse is the body of POST /api/agents/{pane_id}/attachments.
type AttachmentResponse struct {
	// Path is the file's absolute path on the computer.
	Path  string `json:"path"`
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	// Type is the sniffed media type (image/png, application/pdf,
	// text/plain).
	Type string `json:"type"`
}

// attachmentTypes are the accepted sniffed types and the extension a file
// of that type is saved with ("" keeps the client's, for text).
var attachmentTypes = map[string]string{
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"application/pdf": ".pdf",
	"text/plain":      "",
}

// attachmentsRoot is <data_dir>/attachments ("" without a data dir).
func (s *Server) attachmentsRoot() string {
	if s.deps.DataDir == "" {
		return ""
	}
	return filepath.Join(s.deps.DataDir, "attachments")
}

// handleAttachment stores one uploaded file (multipart field "file") for a
// tracked agent. It logs the size and type, never the file's name.
func (s *Server) handleAttachment(w http.ResponseWriter, r *http.Request) {
	if !s.shellOn.Load() {
		// Attachments write files on the computer: the shell switch
		// covers them too.
		writeError(w, http.StatusForbidden, "attachments are off on this computer, like the shell (the setup page switches it)")
		return
	}
	root := s.attachmentsRoot()
	if root == "" {
		writeError(w, http.StatusServiceUnavailable, "attachments need a data directory")
		return
	}
	pane := r.PathValue("pane_id")
	if _, ok := s.deps.Store.Get(pane); !ok {
		writeError(w, http.StatusNotFound, "not a tracked agent")
		return
	}
	// The multipart headers and boundary fit easily in the extra 64 KB.
	r.Body = http.MaxBytesReader(w, r.Body, MaxAttachmentBytes+64<<10)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "want multipart/form-data with a file field")
		return
	}
	var part io.Reader
	var clientName string
	for {
		p, err := mr.NextPart()
		if err != nil {
			writeError(w, attachmentReadStatus(err), "no file field")
			return
		}
		if p.FormName() == "file" {
			part, clientName = p, p.FileName()
			break
		}
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(part, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		writeError(w, attachmentReadStatus(err), "upload failed")
		return
	}
	head = head[:n]
	if n == 0 {
		writeError(w, http.StatusBadRequest, "empty file")
		return
	}
	typ, _, _ := mime.ParseMediaType(http.DetectContentType(head))
	ext, ok := attachmentTypes[typ]
	if !ok {
		writeError(w, http.StatusUnsupportedMediaType, "only images, text and PDF files can be attached")
		return
	}
	// Prune first: it removes empty pane folders, this one included.
	s.pruneAttachments()
	dir := filepath.Join(root, paneDirName(pane))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, "cannot create the attachments folder")
		return
	}
	name := attachmentName(clientName, ext, s.deps.Now())
	path := filepath.Join(dir, name)
	if rel, err := filepath.Rel(dir, path); err != nil || rel != name {
		writeError(w, http.StatusBadRequest, "bad file name")
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot save the file")
		return
	}
	written, err := io.Copy(f, io.MultiReader(bytes.NewReader(head), part))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil || written > MaxAttachmentBytes {
		_ = os.Remove(path)
		status := attachmentReadStatus(err)
		if written > MaxAttachmentBytes {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, attachmentReadMessage(status))
		return
	}
	s.log.Log(logSource, "web.attachment", pane, map[string]any{"bytes": written, "type": typ, "remote_kind": remoteKind(r.RemoteAddr)})
	writeJSON(w, http.StatusCreated, AttachmentResponse{Path: path, Name: name, Bytes: written, Type: typ})
}

// attachmentReadStatus is 413 for a body over the cap, else 400.
func attachmentReadStatus(err error) int {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func attachmentReadMessage(status int) string {
	if status == http.StatusRequestEntityTooLarge {
		return "too big (max 20 MB)"
	}
	return "upload failed"
}

// paneDirName makes a pane id ("w1:p2") a safe directory name ("w1_p2").
func paneDirName(pane string) string {
	var b strings.Builder
	for _, r := range pane {
		if r < 128 && (r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "pane"
	}
	return out
}

// attachmentName is the saved file's name: a time and random prefix, then
// the client's base name reduced to [A-Za-z0-9._-] (no spaces, so the path
// reads as one word in a prompt) and capped, then ext when the type has a
// fixed one.
func attachmentName(client, ext string, now time.Time) string {
	base := filepath.Base(strings.ReplaceAll(client, `\`, "/"))
	var b strings.Builder
	dash := false
	for _, r := range base {
		ok := r < 128 && (r == '.' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
		if !ok {
			if !dash {
				b.WriteByte('-')
				dash = true
			}
			continue
		}
		b.WriteRune(r)
		dash = false
	}
	clean := strings.Trim(b.String(), ".-_")
	if ext != "" {
		clean = strings.Trim(strings.TrimSuffix(clean, filepath.Ext(clean)), ".-_")
	}
	if len(clean) > attachmentNameMax {
		clean = strings.Trim(clean[:attachmentNameMax], ".-_")
	}
	if clean == "" {
		clean = "file"
	}
	if ext == "" && filepath.Ext(clean) == "" {
		ext = ".txt"
	}
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(rnd[:]) + "-" + clean + ext
}

// pruneAttachments removes attachments older than attachmentMaxAge and
// the pane folders left empty. It runs at start-up and on every upload.
func (s *Server) pruneAttachments() {
	root := s.attachmentsRoot()
	if root == "" {
		return
	}
	panes, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := s.deps.Now().Add(-attachmentMaxAge)
	removed := 0
	for _, p := range panes {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(root, p.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		left, here := len(files), 0
		for _, f := range files {
			info, err := f.Info()
			if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
				continue
			}
			if os.Remove(filepath.Join(dir, f.Name())) == nil {
				removed++
				left--
				here++
			}
		}
		if left == 0 && here > 0 {
			_ = os.Remove(dir)
		}
	}
	if removed > 0 {
		s.log.Log(logSource, "web.attachment.prune", "", map[string]any{"removed": removed})
	}
}
