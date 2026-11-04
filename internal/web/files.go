package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/files"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// The file browser (docs/dev/ws-contract.md "Files", feature files): a
// read-only look at an agent's or a space's folder. internal/files keeps
// the rails (os.Root, the sensitive names, regular files only); this layer
// finds the folder the daemon knows, maps each error to a status and a
// reason, and logs every read as files.read with metadata, never contents.
// A list logs nothing.

var (
	errNoAgent  = errors.New("not a tracked agent")
	errNoCwd    = errors.New("no folder yet")
	errNoSpaces = errors.New("spaces are not available here")
)

// filesRoot opens the folder a files route serves: the agent's (pane_id)
// or the space's (id), which name it in the audit.
func (s *Server) filesRoot(r *http.Request) (root *files.Root, pane, space string, err error) {
	var dir string
	if pane = r.PathValue("pane_id"); pane != "" {
		row, ok := s.deps.Store.Get(pane)
		if !ok {
			return nil, "", "", errNoAgent
		}
		dir = row.Cwd
	} else {
		a, ok := s.deps.Supervisor.(AgentActions)
		if !ok || s.deps.Supervisor == nil {
			return nil, "", "", errNoSpaces
		}
		space = r.PathValue("id")
		if dir, err = a.SpaceFolder(r.Context(), space); err != nil {
			return nil, "", "", err
		}
	}
	if dir == "" {
		return nil, "", "", errNoCwd
	}
	if s.privateRoot(dir) {
		return nil, pane, space, files.ErrSensitive
	}
	root, err = files.Open(dir)
	return root, pane, space, err
}

// filesStatus is an error's status, reason and message. The message is
// fixed words, never the error's own text, which can carry a path on the
// computer or a system error; an error not listed here is 500 "failed".
func filesStatus(err error) (status int, reason, message string) {
	switch {
	case errors.Is(err, errNoAgent):
		return http.StatusNotFound, "no_agent", "not a tracked agent"
	case errors.Is(err, supervisor.ErrNoSuchSpace):
		return http.StatusNotFound, "no_space", "no such space"
	case errors.Is(err, errNoCwd):
		return http.StatusNotFound, "no_cwd", "no folder yet"
	case errors.Is(err, files.ErrNotFound):
		return http.StatusNotFound, "not_found", "not found"
	case errors.Is(err, files.ErrOutside):
		return http.StatusForbidden, "outside", "that leaves the folder"
	case errors.Is(err, files.ErrSensitive):
		return http.StatusForbidden, "sensitive", "hidden for safety"
	case errors.Is(err, files.ErrNotFile):
		return http.StatusForbidden, "not_file", "not a regular file"
	case errors.Is(err, files.ErrDenied):
		return http.StatusForbidden, "denied", "the system refused"
	case errors.Is(err, files.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, "too_large", "too large to show"
	case errors.Is(err, files.ErrNotImage):
		return http.StatusUnsupportedMediaType, "not_image", "not an image"
	case errors.Is(err, files.ErrBadPath):
		return http.StatusBadRequest, "bad_path", "not a clean relative path"
	case errors.Is(err, errNoSpaces):
		return http.StatusServiceUnavailable, "", "spaces are not available here"
	}
	return http.StatusInternalServerError, "failed", "could not be read"
}

// filesError answers {"error", "reason"}.
func filesError(w http.ResponseWriter, err error) {
	status, reason, message := filesStatus(err)
	body := map[string]string{"error": message}
	if reason != "" {
		body["reason"] = reason
	}
	writeJSON(w, status, body)
}

// filesListTimeout bounds one list or search, as changesTimeout bounds git.
const filesListTimeout = changesTimeout

// handleFilesList is GET …/files?path=&q=: a folder's list, or with q a
// search of the names under it.
func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	root, _, _, err := s.filesRoot(r)
	if err != nil {
		filesError(w, err)
		return
	}
	defer func() { _ = root.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), filesListTimeout)
	defer cancel()
	rel := r.URL.Query().Get("path")
	var list files.Listing
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		list, err = root.Search(ctx, rel, q)
	} else {
		list, err = root.List(ctx, rel)
	}
	if err != nil {
		filesError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleFileRead is GET …/files/{path}: one file as JSON, or with ?raw=1
// an image's bytes under its own content type.
func (s *Server) handleFileRead(w http.ResponseWriter, r *http.Request) {
	root, pane, space, err := s.filesRoot(r)
	if err != nil {
		filesError(w, err)
		return
	}
	defer func() { _ = root.Close() }()
	rel := r.PathValue("path")
	raw := r.URL.Query().Get("raw") == "1"
	audit := map[string]any{"path": rel, "raw": raw}
	if space != "" {
		audit["space"] = space
	}
	if !raw {
		f, err := root.Read(rel)
		s.logRead(pane, audit, f.Size, err)
		if err != nil {
			filesError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, f)
		return
	}
	f, ctype, body, err := root.Image(rel)
	s.logRead(pane, audit, f.Size, err)
	if err != nil {
		filesError(w, err)
		return
	}
	defer func() { _ = body.Close() }()
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.FormatInt(f.Size, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(w, body, f.Size)
}

// logRead logs files.read: the path and the size, or why it was refused.
func (s *Server) logRead(pane string, fields map[string]any, size int64, err error) {
	if err != nil {
		_, fields["reason"], _ = filesStatus(err)
	} else {
		fields["size"] = size
	}
	s.log.Log(logSource, "files.read", pane, fields)
}

// privateRoot refuses roots that contain home or data, and the data tree itself.
func (s *Server) privateRoot(dir string) bool {
	resolve := func(p string) string {
		if p == "" {
			return ""
		}
		p, _ = filepath.Abs(p)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	contains := func(parent, child string) bool {
		if parent == "" || child == "" {
			return false
		}
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	root := resolve(dir)
	home, _ := os.UserHomeDir()
	home = resolve(home)
	data := resolve(s.deps.DataDir)
	return contains(root, home) || contains(root, data) || contains(data, root)
}
