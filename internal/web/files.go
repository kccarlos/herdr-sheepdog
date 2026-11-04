package web

import (
	"errors"
	"io"
	"net/http"
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
	root, err = files.Open(dir)
	return root, pane, space, err
}

// filesStatus is an error's status and reason.
func filesStatus(err error) (int, string) {
	switch {
	case errors.Is(err, errNoAgent):
		return http.StatusNotFound, "no_agent"
	case errors.Is(err, supervisor.ErrNoSuchSpace):
		return http.StatusNotFound, "no_space"
	case errors.Is(err, errNoCwd):
		return http.StatusNotFound, "no_cwd"
	case errors.Is(err, files.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, files.ErrOutside):
		return http.StatusForbidden, "outside"
	case errors.Is(err, files.ErrSensitive):
		return http.StatusForbidden, "sensitive"
	case errors.Is(err, files.ErrNotFile):
		return http.StatusForbidden, "not_file"
	case errors.Is(err, files.ErrDenied):
		return http.StatusForbidden, "denied"
	case errors.Is(err, files.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, "too_large"
	case errors.Is(err, files.ErrNotImage):
		return http.StatusUnsupportedMediaType, "not_image"
	case errors.Is(err, files.ErrBadPath):
		return http.StatusBadRequest, "bad_path"
	case errors.Is(err, errNoSpaces):
		return http.StatusServiceUnavailable, ""
	}
	// herdr could not say where the space is.
	return http.StatusBadGateway, ""
}

// filesError answers {"error", "reason"}.
func filesError(w http.ResponseWriter, err error) {
	status, reason := filesStatus(err)
	body := map[string]string{"error": err.Error()}
	if reason != "" {
		body["reason"] = reason
	}
	writeJSON(w, status, body)
}

// handleFilesList is GET …/files?path=&q=: a folder's list, or with q a
// search of the names under it.
func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	root, _, _, err := s.filesRoot(r)
	if err != nil {
		filesError(w, err)
		return
	}
	defer func() { _ = root.Close() }()
	rel := r.URL.Query().Get("path")
	var list files.Listing
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		list, err = root.Search(r.Context(), rel, q)
	} else {
		list, err = root.List(rel)
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
		_, fields["reason"] = filesStatus(err)
	} else {
		fields["size"] = size
	}
	s.log.Log(logSource, "files.read", pane, fields)
}
