package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The changes view (docs/dev/ws-contract.md "Changes"): a read-only look at
// what an agent changed in its working directory, computed with git. git
// runs with an argument list (no shell), a timeout and output caps, never
// takes optional locks (so it cannot collide with the agent's own git) and
// never runs an external diff, textconv or fsmonitor program. A diff is
// served only for a path the status call itself just listed, so a client
// cannot name a file outside the repository.

const (
	// changesTimeout bounds one git call.
	changesTimeout = 5 * time.Second
	// changesMaxFiles caps the listed files.
	changesMaxFiles = 500
	// changesStatusMax caps git status and numstat output.
	changesStatusMax = 4 << 20
	// changesDiffMax caps one file's diff.
	changesDiffMax = 512 << 10
	// untrackedCountMax is the largest untracked file whose lines are
	// counted.
	untrackedCountMax = 1 << 20
	// emptyTree is git's empty tree, the base of a repository with no
	// commit yet.
	emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

// ChangedFile is one file in the changes list.
type ChangedFile struct {
	Path string `json:"path"`
	// OldPath is set for a rename.
	OldPath string `json:"old_path,omitempty"`
	// Status is modified, added, deleted, renamed, typechange,
	// untracked or conflicted.
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary,omitempty"`
}

// ChangesResponse is the body of GET /api/agents/{pane_id}/changes.
type ChangesResponse struct {
	// Repo is false when the agent's folder is not a git work tree;
	// Reason then says why: no_cwd, not_repo or no_git.
	Repo   bool   `json:"repo"`
	Reason string `json:"reason,omitempty"`
	Root   string `json:"root,omitempty"`
	// Branch is the checked-out branch ("" when detached).
	Branch    string        `json:"branch,omitempty"`
	Files     []ChangedFile `json:"files"`
	Added     int           `json:"added"`
	Deleted   int           `json:"deleted"`
	Truncated bool          `json:"truncated,omitempty"`
}

// DiffResponse is the body of GET /api/agents/{pane_id}/changes/{path}.
type DiffResponse struct {
	ChangedFile
	// Diff is git's unified diff for the file (empty for a binary file).
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated,omitempty"`
}

var errNoGit = errors.New("git not found")

// gitRunner runs read-only git commands in one repository.
type gitRunner struct {
	bin string
	dir string
}

// run runs git with args in dir and returns at most max bytes of stdout
// (truncated says it had more). Exit status 1 is accepted when ok1 (git
// diff --no-index reports "files differ" that way).
func (g gitRunner) run(ctx context.Context, max int, ok1 bool, args ...string) (out []byte, truncated bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, changesTimeout)
	defer cancel()
	full := append([]string{
		"-C", g.dir,
		"-c", "core.fsmonitor=false",
		"-c", "core.quotePath=false",
		"--no-pager", "--no-optional-locks",
	}, args...)
	cmd := exec.CommandContext(ctx, g.bin, full...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "LC_ALL=C")
	buf := &capWriter{max: max}
	cmd.Stdout = buf
	err = cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, false, errNoGit
	}
	var exit *exec.ExitError
	if ok1 && errors.As(err, &exit) && exit.ExitCode() == 1 {
		err = nil
	}
	return buf.buf.Bytes(), buf.over, err
}

// capWriter keeps the first max bytes and drops the rest.
type capWriter struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		c.over = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

// changesRepo finds the agent's repository: its runner rooted at the top
// level, or a ChangesResponse saying why there is none.
func (s *Server) changesRepo(ctx context.Context, pane string) (gitRunner, *ChangesResponse, int) {
	row, ok := s.deps.Store.Get(pane)
	if !ok {
		return gitRunner{}, nil, http.StatusNotFound
	}
	if row.Cwd == "" {
		return gitRunner{}, &ChangesResponse{Reason: "no_cwd", Files: []ChangedFile{}}, http.StatusOK
	}
	if info, err := os.Stat(row.Cwd); err != nil || !info.IsDir() {
		return gitRunner{}, &ChangesResponse{Reason: "no_cwd", Files: []ChangedFile{}}, http.StatusOK
	}
	bin := s.deps.GitBin
	if bin == "" {
		bin = "git"
	}
	out, _, err := gitRunner{bin: bin, dir: row.Cwd}.run(ctx, 64<<10, false, "rev-parse", "--show-toplevel")
	if errors.Is(err, errNoGit) {
		return gitRunner{}, &ChangesResponse{Reason: "no_git", Files: []ChangedFile{}}, http.StatusOK
	}
	root := strings.TrimSpace(string(out))
	if err != nil || root == "" {
		return gitRunner{}, &ChangesResponse{Reason: "not_repo", Files: []ChangedFile{}}, http.StatusOK
	}
	return gitRunner{bin: bin, dir: root}, nil, http.StatusOK
}

// listChanges reads status and numstat for the repository.
func listChanges(ctx context.Context, g gitRunner) (ChangesResponse, string, error) {
	resp := ChangesResponse{Repo: true, Root: g.dir, Files: []ChangedFile{}}
	out, trunc, err := g.run(ctx, changesStatusMax, false, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	if err != nil {
		return resp, "", err
	}
	resp.Truncated = trunc
	files, branch := parseStatusV2(out, trunc)
	resp.Branch = branch
	if len(files) > changesMaxFiles {
		files, resp.Truncated = files[:changesMaxFiles], true
	}
	base := "HEAD"
	if _, _, err := g.run(ctx, 1<<10, false, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		base = emptyTree
	}
	ns, nsTrunc, err := g.run(ctx, changesStatusMax, false, "diff", base, "--numstat", "-z", "-M", "--no-ext-diff", "--no-textconv", "--no-color", "--")
	if err != nil {
		return resp, base, err
	}
	resp.Truncated = resp.Truncated || nsTrunc
	counts := parseNumstat(ns)
	for i := range files {
		f := &files[i]
		if f.Status == "untracked" {
			f.Added, f.Binary = countUntracked(g.dir, f.Path)
		} else if c, ok := counts[f.Path]; ok {
			f.Added, f.Deleted, f.Binary = c.Added, c.Deleted, c.Binary
		}
		resp.Added += f.Added
		resp.Deleted += f.Deleted
	}
	resp.Files = files
	return resp, base, nil
}

// handleChanges answers the agent's changed files.
func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	pane := r.PathValue("pane_id")
	start := s.deps.Now()
	g, none, status := s.changesRepo(r.Context(), pane)
	if status != http.StatusOK {
		writeError(w, status, "not a tracked agent")
		return
	}
	if none != nil {
		s.log.Log(logSource, "web.changes", pane, map[string]any{"repo": false, "reason": none.Reason})
		writeJSON(w, http.StatusOK, none)
		return
	}
	resp, _, err := listChanges(r.Context(), g)
	if err != nil {
		writeError(w, http.StatusBadGateway, "git failed")
		return
	}
	s.log.Log(logSource, "web.changes", pane, map[string]any{"repo": true, "files": len(resp.Files), "truncated": resp.Truncated, "dur_ms": s.deps.Now().Sub(start).Milliseconds()})
	writeJSON(w, http.StatusOK, resp)
}

// handleChangeDiff answers one listed file's unified diff.
func (s *Server) handleChangeDiff(w http.ResponseWriter, r *http.Request) {
	pane := r.PathValue("pane_id")
	path := r.PathValue("path")
	g, none, status := s.changesRepo(r.Context(), pane)
	if status != http.StatusOK {
		writeError(w, status, "not a tracked agent")
		return
	}
	if none != nil {
		writeError(w, http.StatusNotFound, "not a git repository")
		return
	}
	list, base, err := listChanges(r.Context(), g)
	if err != nil {
		writeError(w, http.StatusBadGateway, "git failed")
		return
	}
	var file *ChangedFile
	for i := range list.Files {
		if list.Files[i].Path == path {
			file = &list.Files[i]
			break
		}
	}
	if file == nil {
		writeError(w, http.StatusNotFound, "no changes to that file")
		return
	}
	resp := DiffResponse{ChangedFile: *file}
	if !file.Binary {
		var args []string
		if file.Status == "untracked" {
			args = []string{"diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--", "/dev/null", file.Path}
		} else {
			args = []string{"diff", base, "-M", "--no-ext-diff", "--no-textconv", "--no-color", "--"}
			if file.OldPath != "" {
				args = append(args, file.OldPath)
			}
			args = append(args, file.Path)
		}
		out, trunc, err := g.run(r.Context(), changesDiffMax, file.Status == "untracked", args...)
		if err != nil {
			writeError(w, http.StatusBadGateway, "git failed")
			return
		}
		resp.Diff, resp.Truncated = string(bytes.ToValidUTF8(out, []byte("�"))), trunc
	}
	s.log.Log(logSource, "web.changes.diff", pane, map[string]any{"bytes": len(resp.Diff), "truncated": resp.Truncated, "status": file.Status})
	writeJSON(w, http.StatusOK, resp)
}

// parseStatusV2 reads `git status --porcelain=v2 -z --branch`. When the
// output was cut (trunc), the last, partial entry is dropped.
func parseStatusV2(out []byte, trunc bool) (files []ChangedFile, branch string) {
	fields := strings.Split(string(out), "\x00")
	if trunc && len(fields) > 0 {
		fields = fields[:len(fields)-1]
	}
	files = []ChangedFile{}
	for i := 0; i < len(fields); i++ {
		e := fields[i]
		if e == "" {
			continue
		}
		switch e[0] {
		case '#':
			if b, ok := strings.CutPrefix(e, "# branch.head "); ok && b != "(detached)" {
				branch = b
			}
		case '1':
			// 1 XY sub mH mI mW hH hI path
			if p := strings.SplitN(e, " ", 9); len(p) == 9 {
				files = append(files, ChangedFile{Path: p[8], Status: statusWord(p[1])})
			}
		case '2':
			// 2 XY sub mH mI mW hH hI Xscore path NUL orig
			if p := strings.SplitN(e, " ", 10); len(p) == 10 && i+1 < len(fields) {
				i++
				files = append(files, ChangedFile{Path: p[9], OldPath: fields[i], Status: "renamed"})
			}
		case 'u':
			// u XY sub m1 m2 m3 mW h1 h2 h3 path
			if p := strings.SplitN(e, " ", 11); len(p) == 11 {
				files = append(files, ChangedFile{Path: p[10], Status: "conflicted"})
			}
		case '?':
			if len(e) > 2 {
				files = append(files, ChangedFile{Path: e[2:], Status: "untracked"})
			}
		}
	}
	return files, branch
}

// statusWord turns a porcelain XY into the file's change against HEAD.
func statusWord(xy string) string {
	switch {
	case strings.ContainsRune(xy, 'D'):
		return "deleted"
	case strings.ContainsRune(xy, 'A'):
		return "added"
	case strings.ContainsRune(xy, 'T'):
		return "typechange"
	default:
		return "modified"
	}
}

type numstat struct {
	Added, Deleted int
	Binary         bool
}

// parseNumstat reads `git diff --numstat -z`, keyed by the new path.
func parseNumstat(out []byte) map[string]numstat {
	m := map[string]numstat{}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		p := strings.SplitN(fields[i], "\t", 3)
		if len(p) != 3 {
			continue
		}
		path := p[2]
		if path == "" && i+2 < len(fields) {
			// A rename: "a\td\t" NUL old NUL new.
			path = fields[i+2]
			i += 2
		}
		var n numstat
		if p[0] == "-" || p[1] == "-" {
			n.Binary = true
		} else {
			n.Added, _ = strconv.Atoi(p[0])
			n.Deleted, _ = strconv.Atoi(p[1])
		}
		m[path] = n
	}
	return m
}

// countUntracked counts an untracked file's lines; a symlink, a file over
// untrackedCountMax or one with a NUL byte counts as binary (no lines).
func countUntracked(root, rel string) (lines int, binary bool) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > untrackedCountMax {
		return 0, err == nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		return 0, true
	}
	lines = bytes.Count(b, []byte("\n"))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		lines++
	}
	return lines, false
}
