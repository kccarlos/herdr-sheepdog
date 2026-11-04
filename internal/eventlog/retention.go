package eventlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// protected reports whether dir must never be pruned: the repository's
// recorded benchmark and eval data (docs/dev/bench, docs/dev/eval, testdata)
// is kept whatever retention says, even if a data_dir points into it.
func protected(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	abs = filepath.ToSlash(abs) + "/"
	for _, p := range []string{"/docs/dev/bench/", "/docs/dev/eval/", "/docs/bench/", "/docs/eval/", "/testdata/"} {
		if strings.Contains(abs, p) {
			return true
		}
	}
	return false
}

// DayFiles lists dataDir's event day files (<data_dir>/events/
// YYYY-MM-DD.jsonl), oldest first. Other files, directories and symlinks
// are not listed.
func DayFiles(dataDir string) ([]string, error) {
	dir := filepath.Join(dataDir, "events")
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.Type().IsRegular() || !dayFile.MatchString(e.Name()) {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

// brainFile matches an orchestrator transcript or request log under
// <data_dir>/brain: <run id>[-resetN][.requests].jsonl, the run id being
// its UTC start time (20260918T162804Z). They hold the conversation too.
var brainFile = regexp.MustCompile(`^(\d{8})T\d{6}Z(-reset\d+)?(\.requests)?\.jsonl$`)

// BrainFiles lists dataDir's orchestrator transcript files, oldest first.
func BrainFiles(dataDir string) ([]string, error) {
	dir := filepath.Join(dataDir, "brain")
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && brainFile.MatchString(e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// CardsFile returns <data_dir>/cards.jsonl when it exists as a regular
// file (not a symlink), or "".
func CardsFile(dataDir string) string {
	p := filepath.Join(dataDir, "cards.jsonl")
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	return p
}

// Prune deletes dataDir's event day files older than days days (UTC days
// before today's, so days = 14 keeps today and the 14 days before it),
// the orchestrator transcripts (<data_dir>/brain) of runs that started
// before then and were last written before then, and card records in
// <data_dir>/cards.jsonl older than then, and returns the deleted/pruned
// paths. days <= 0 deletes nothing (keep forever). It never recurses,
// follows a symlink or touches a file that is not a day file, and never
// runs under docs/dev/bench, docs/dev/eval or testdata.
func Prune(dataDir string, days int, now time.Time) ([]string, error) {
	if days <= 0 || protected(dataDir) {
		return nil, nil
	}
	files, err := DayFiles(dataDir)
	if err != nil {
		return nil, err
	}
	cutoff := now.UTC().AddDate(0, 0, -days).Format("2006-01-02")
	var removed []string
	for _, f := range files {
		day := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		if day >= cutoff {
			continue
		}
		if err := os.Remove(f); err != nil {
			return removed, err
		}
		removed = append(removed, f)
	}
	brain, err := BrainFiles(dataDir)
	if err != nil {
		return removed, err
	}
	cutoffTime := now.UTC().AddDate(0, 0, -days).Truncate(24 * time.Hour)
	for _, f := range brain {
		m := brainFile.FindStringSubmatch(filepath.Base(f))
		day := m[1][:4] + "-" + m[1][4:6] + "-" + m[1][6:]
		st, err := os.Stat(f)
		if day >= cutoff || err != nil || !st.ModTime().Before(cutoffTime) {
			continue
		}
		if err := os.Remove(f); err != nil {
			return removed, err
		}
		removed = append(removed, f)
	}
	if cards := CardsFile(dataDir); cards != "" {
		pruned, err := pruneCards(cards, cutoffTime)
		if err != nil {
			return removed, err
		}
		if pruned {
			removed = append(removed, cards)
		}
	}
	return removed, nil
}

// pruneCards drops lines in path whose JSON "at" timestamp is before
// cutoff, rewriting path atomically (or removing it when no cards remain).
func pruneCards(path string, cutoff time.Time) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	var kept [][]byte
	dropped := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var rec struct {
			At time.Time `json:"at"`
		}
		if json.Unmarshal(line, &rec) == nil && !rec.At.IsZero() && rec.At.Before(cutoff) {
			dropped = true
			continue
		}
		kept = append(kept, append([]byte(nil), line...))
	}
	scanErr := sc.Err()
	_ = f.Close()
	if scanErr != nil {
		return false, scanErr
	}
	if !dropped {
		return false, nil
	}
	if len(kept) == 0 {
		return true, os.Remove(path)
	}
	tmp := path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false, err
	}
	w := bufio.NewWriter(out)
	for _, line := range kept {
		_, _ = w.Write(line)
		_ = w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		_ = out.Close()
		return false, err
	}
	if err := out.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// Clear deletes every event day file, orchestrator transcript and card
// store of dataDir (sheepdog data clear --events) and returns the
// deleted paths. Same guards as Prune.
func Clear(dataDir string) ([]string, error) {
	if protected(dataDir) {
		return nil, nil
	}
	files, err := DayFiles(dataDir)
	if err != nil {
		return nil, err
	}
	brain, err := BrainFiles(dataDir)
	if err != nil {
		return nil, err
	}
	all := append(files, brain...)
	if cards := CardsFile(dataDir); cards != "" {
		all = append(all, cards)
	}
	var removed []string
	for _, f := range all {
		if err := os.Remove(f); err != nil {
			return removed, err
		}
		removed = append(removed, f)
	}
	return removed, nil
}
