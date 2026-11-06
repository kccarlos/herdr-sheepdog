package workerlog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const probeTimeout = 2 * time.Second

// ProcessRoots identifies a pane's foreground process tree.
type ProcessRoots struct {
	PID      int
	Pgrp     int
	ShellPID int
}

// HasPID reports whether any process root is known.
func (r ProcessRoots) HasPID() bool {
	return r.PID > 0 || r.Pgrp > 0 || r.ShellPID > 0
}

// Direct returns the non-zero root PIDs themselves, foreground first,
// deduplicated.
func (r ProcessRoots) Direct() []int {
	var out []int
	for _, p := range []int{r.PID, r.Pgrp, r.ShellPID} {
		if p > 0 && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// Resolved identifies a worker session and its transcript file.
type Resolved struct {
	Format  Format
	ID      string
	Source  string
	Path    string
	FromPID bool
}

type pidEntry struct {
	roots      ProcessRoots
	format     Format
	agentPID   int
	sessionID  string
	path       string
	reportedID string
}

// SupportsKind reports whether kind is an agent whose session transcripts
// this package can read.
func SupportsKind(kind string) bool {
	return Session{Agent: kind}.format() != ""
}

// Resolve locates sess's transcript and session id, probing sess.Roots'
// foreground process tree first when present and falling back to sess.Value.
func (r *Reader) Resolve(sess Session) (Resolved, error) {
	f := sess.format()
	if f == "" && (sess.Agent != "" || sess.Source != "") {
		if sess.Value == "" && !sess.Roots.HasPID() {
			return Resolved{}, missing(ReasonNotReported, nil)
		}
		return Resolved{}, missing(ReasonUnsupported, fmt.Errorf("agent %q", sess.Agent))
	}
	if sess.Roots.HasPID() {
		if res, handled, err := r.discoverByPID(sess, f); handled {
			return res, err
		}
	}
	return r.resolveByValue(sess, f)
}

// Discovered returns the last PID-discovered session for pane, if any.
func (r *Reader) Discovered(pane string) (Resolved, bool) {
	if pane == "" {
		return Resolved{}, false
	}
	r.mu.Lock()
	e, ok := r.pids[pane]
	r.mu.Unlock()
	if !ok || e.sessionID == "" {
		return Resolved{}, false
	}
	return Resolved{
		Format:  e.format,
		ID:      e.sessionID,
		Source:  "pid:" + string(e.format),
		Path:    e.path,
		FromPID: true,
	}, true
}

// Invalidate drops any cached PID probe for pane so the next read re-probes
// the pane's foreground process tree.
func (r *Reader) Invalidate(pane string) {
	if pane == "" {
		return
	}
	r.mu.Lock()
	r.gens[pane]++
	delete(r.pids, pane)
	r.mu.Unlock()
}

func (r *Reader) discoverByPID(sess Session, f Format) (Resolved, bool, error) {
	r.mu.Lock()
	gen := r.gens[sess.Pane]
	cached, hasCached := r.pids[sess.Pane]
	r.mu.Unlock()

	var pids []int
	havePIDs := false
	getPIDs := func() []int {
		if !havePIDs {
			pids = r.treePIDs(sess.Roots)
			havePIDs = true
		}
		return pids
	}

	if f == FormatClaude || f == "" {
		finish := func(pid int, sid, p string) (Resolved, bool, error) {
			r.storePIDCache(sess.Pane, gen, pidEntry{
				roots:      sess.Roots,
				format:     FormatClaude,
				agentPID:   pid,
				sessionID:  sid,
				path:       p,
				reportedID: sess.Value,
			})
			if p == "" {
				return Resolved{}, true, missing(ReasonNotFound, nil)
			}
			return Resolved{Format: FormatClaude, ID: sid, Source: "pid:claude", Path: p, FromPID: true}, true, nil
		}
		if sess.Pane != "" && hasCached && cached.roots == sess.Roots && cached.format == FormatClaude && cached.agentPID > 0 {
			if sid, docCwd, ok := r.readClaudePIDFile(cached.agentPID); ok {
				p := cached.path
				if sid != cached.sessionID || p == "" || !fileExists(p) {
					p = r.locateClaudeID(sid, sess.Cwd, docCwd)
				}
				return finish(cached.agentPID, sid, p)
			}
		}
		for _, pid := range getPIDs() {
			if sid, docCwd, ok := r.readClaudePIDFile(pid); ok {
				return finish(pid, sid, r.locateClaudeID(sid, sess.Cwd, docCwd))
			}
		}
	}

	if f == FormatCodex || f == "" {
		if sess.Pane != "" && hasCached && cached.roots == sess.Roots && cached.format == FormatCodex && cached.path != "" {
			reportedChanged := sess.Value != "" && sess.Value != cached.reportedID && sess.Value != cached.sessionID && sess.Value != cached.path
			if !reportedChanged && fileExists(cached.path) {
				return Resolved{Format: FormatCodex, ID: cached.sessionID, Source: "pid:codex", Path: cached.path, FromPID: true}, true, nil
			}
		}
		if list := getPIDs(); len(list) > 0 {
			bestPath := newestRolloutFile(r.openFiles(list))
			if bestPath != "" {
				sid := codexIDFromPath(bestPath)
				r.storePIDCache(sess.Pane, gen, pidEntry{
					roots:      sess.Roots,
					format:     FormatCodex,
					sessionID:  sid,
					path:       bestPath,
					reportedID: sess.Value,
				})
				return Resolved{Format: FormatCodex, ID: sid, Source: "pid:codex", Path: bestPath, FromPID: true}, true, nil
			}
		}
	}

	return Resolved{}, false, nil
}

func (r *Reader) storePIDCache(pane string, gen uint64, entry pidEntry) {
	if pane == "" {
		return
	}
	r.mu.Lock()
	if r.gens[pane] == gen {
		r.pids[pane] = entry
	}
	r.mu.Unlock()
}

func (r *Reader) readClaudePIDFile(pid int) (sessionID, cwd string, ok bool) {
	if pid <= 0 || r.opts.ClaudeSessions == "" {
		return "", "", false
	}
	data, err := os.ReadFile(filepath.Join(r.opts.ClaudeSessions, strconv.Itoa(pid)+".json"))
	if err != nil {
		return "", "", false
	}
	var doc struct {
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return "", "", false
	}
	sid := strings.TrimSpace(doc.SessionID)
	if sid == "" || strings.ContainsAny(sid, `/\*?[`) {
		return "", "", false
	}
	return sid, strings.TrimSpace(doc.Cwd), true
}

func (r *Reader) locateClaudeID(id, cwd, fallbackCwd string) string {
	for _, dir := range []string{cwd, fallbackCwd} {
		if dir == "" {
			continue
		}
		p := filepath.Join(r.opts.ClaudeProjects, ClaudeSlug(dir), id+".jsonl")
		if fileExists(p) {
			return p
		}
	}
	if m, _ := filepath.Glob(filepath.Join(r.opts.ClaudeProjects, "*", id+".jsonl")); len(m) > 0 {
		return m[0]
	}
	return ""
}

func (r *Reader) locateCodexID(id string) string {
	if m, _ := filepath.Glob(filepath.Join(r.opts.CodexSessions, "*", "*", "*", "*-"+id+".jsonl")); len(m) > 0 {
		return m[len(m)-1]
	}
	return ""
}

func (r *Reader) resolveByValue(sess Session, f Format) (Resolved, error) {
	if !sess.hasValueFor(f) {
		return Resolved{}, missing(ReasonNotReported, nil)
	}
	if sess.Kind == "path" {
		if !fileExists(sess.Value) {
			return Resolved{}, missing(ReasonNotFound, nil)
		}
		return Resolved{
			Format: f,
			ID:     idFromTranscriptPath(f, sess.Value),
			Source: sess.Source,
			Path:   sess.Value,
		}, nil
	}
	if strings.ContainsAny(sess.Value, `/\*?[`) {
		return Resolved{}, missing(ReasonNotFound, fmt.Errorf("session id %q is not a plain id", sess.Value))
	}
	var p string
	switch f {
	case FormatClaude:
		p = r.locateClaudeID(sess.Value, sess.Cwd, "")
	case FormatCodex:
		p = r.locateCodexID(sess.Value)
	}
	if p == "" {
		return Resolved{}, missing(ReasonNotFound, nil)
	}
	return Resolved{Format: f, ID: sess.Value, Source: sess.Source, Path: p}, nil
}

func newestRolloutFile(paths []string) string {
	var bestPath string
	var bestMod time.Time
	for _, p := range paths {
		base := filepath.Base(p)
		if !strings.HasPrefix(base, "rollout-") || !strings.HasSuffix(base, ".jsonl") {
			continue
		}
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		mt := st.ModTime()
		if bestPath == "" || mt.After(bestMod) || (mt.Equal(bestMod) && p > bestPath) {
			bestPath, bestMod = p, mt
		}
	}
	return bestPath
}

func idFromTranscriptPath(f Format, p string) string {
	if f == FormatCodex {
		return codexIDFromPath(p)
	}
	cand := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	if isUUIDLike(cand) {
		return cand
	}
	return ""
}

func codexIDFromPath(p string) string {
	base := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	if len(base) < 36 {
		return ""
	}
	cand := base[len(base)-36:]
	if isUUIDLike(cand) {
		return cand
	}
	return ""
}

func isUUIDLike(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

func (r *Reader) treePIDs(roots ProcessRoots) []int {
	if r.opts.TreePIDs != nil {
		return r.opts.TreePIDs(roots)
	}
	return defaultTreePIDs(roots)
}

func (r *Reader) openFiles(pids []int) []string {
	if r.opts.OpenFiles != nil {
		return r.opts.OpenFiles(pids)
	}
	return defaultOpenFiles(pids)
}

type procRow struct {
	pid  int
	ppid int
	pgid int
}

func defaultTreePIDs(roots ProcessRoots) []int {
	direct := roots.Direct()
	if len(direct) == 0 {
		return nil
	}
	if rows := readLinuxProcTable(); len(rows) > 0 {
		return walkProcessTree(roots, rows)
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	out, err := exec.CommandContext(ctx, "ps", "-ax", "-o", "pid=,ppid=,pgid=").Output()
	cancel()
	if err == nil {
		if rows := parsePsTable(out); len(rows) > 0 {
			return walkProcessTree(roots, rows)
		}
	}
	pgrp := roots.Pgrp
	if pgrp <= 0 {
		pgrp = roots.PID
	}
	if pgrp > 0 {
		lctx, lcancel := context.WithTimeout(context.Background(), probeTimeout)
		lout, _ := exec.CommandContext(lctx, "lsof", "-n", "-P", "-a", "-d", "cwd", "-Fp", "-g", strconv.Itoa(pgrp)).Output()
		lcancel()
		for _, p := range parseLsofPIDs(lout) {
			if !slices.Contains(direct, p) {
				direct = append(direct, p)
			}
		}
	}
	return direct
}

func readLinuxProcTable() []procRow {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var rows []procRow
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		if row, ok := parseProcStat(pid, data); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func parseProcStat(pid int, data []byte) (procRow, bool) {
	end := bytes.LastIndexByte(data, ')')
	if end < 0 || end+2 >= len(data) {
		return procRow{}, false
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 3 {
		return procRow{}, false
	}
	ppid, err1 := strconv.Atoi(fields[1])
	pgid, err2 := strconv.Atoi(fields[2])
	if err1 != nil || err2 != nil {
		return procRow{}, false
	}
	return procRow{pid: pid, ppid: ppid, pgid: pgid}, true
}

func parsePsTable(out []byte) []procRow {
	var rows []procRow
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		pgid, err3 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || err3 != nil || pid <= 0 {
			continue
		}
		rows = append(rows, procRow{pid: pid, ppid: ppid, pgid: pgid})
	}
	return rows
}

func walkProcessTree(roots ProcessRoots, rows []procRow) []int {
	children := make(map[int][]int, len(rows))
	var pgrpMembers []int
	for _, r := range rows {
		children[r.ppid] = append(children[r.ppid], r.pid)
		if roots.Pgrp > 0 && r.pgid == roots.Pgrp {
			pgrpMembers = append(pgrpMembers, r.pid)
		}
	}
	var out []int
	seen := map[int]bool{}
	visit := func(seed int) {
		if seed <= 0 {
			return
		}
		queue := []int{seed}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if cur <= 0 || seen[cur] {
				continue
			}
			seen[cur] = true
			out = append(out, cur)
			queue = append(queue, children[cur]...)
		}
	}
	visit(roots.PID)
	for _, p := range pgrpMembers {
		visit(p)
	}
	visit(roots.ShellPID)
	for _, p := range roots.Direct() {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func defaultOpenFiles(pids []int) []string {
	var parts []string
	var clean []int
	for _, p := range pids {
		if p > 0 && !slices.Contains(clean, p) {
			clean = append(clean, p)
			parts = append(parts, strconv.Itoa(p))
		}
	}
	if len(clean) == 0 {
		return nil
	}
	if st, err := os.Stat("/proc/self/fd"); err == nil && st.IsDir() {
		return readLinuxOpenFiles(clean)
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	out, _ := exec.CommandContext(ctx, "lsof", "-n", "-P", "-a", "-d", "0-999", "-Fn", "-p", strings.Join(parts, ",")).Output()
	cancel()
	return parseLsofFiles(out)
}

func readLinuxOpenFiles(pids []int) []string {
	var out []string
	seen := map[string]bool{}
	for _, pid := range pids {
		dir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			target, err := os.Readlink(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			target = strings.TrimSuffix(target, " (deleted)")
			if strings.HasPrefix(target, "/") && !seen[target] {
				seen[target] = true
				out = append(out, target)
			}
		}
	}
	return out
}

func parseLsofPIDs(out []byte) []int {
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 || line[0] != 'p' {
			continue
		}
		if p, err := strconv.Atoi(strings.TrimSpace(line[1:])); err == nil && p > 0 && !slices.Contains(pids, p) {
			pids = append(pids, p)
		}
	}
	return pids
}

func parseLsofFiles(out []byte) []string {
	var files []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 2 || line[0] != 'n' || line[1] != '/' {
			continue
		}
		p := line[1:]
		if !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	return files
}
