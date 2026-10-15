package eventlog

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// protected reports whether dir must never be pruned: the repository's
// recorded benchmark and eval data (docs/bench, docs/eval, testdata)
// is kept whatever retention says, even if a data_dir points into it.
func protected(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	abs = filepath.ToSlash(abs) + "/"
	for _, p := range []string{"/docs/bench/", "/docs/eval/", "/testdata/"} {
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

// Prune deletes dataDir's event day files older than days days (UTC days
// before today's, so days = 14 keeps today and the 14 days before it),
// and the orchestrator transcripts (<data_dir>/brain) of runs that
// started before then and were last written before then, and returns
// the deleted paths. days <= 0 deletes nothing (keep
// forever). It never recurses, follows a symlink or touches a file that
// is not a day file, and never runs under docs/bench, docs/eval or
// testdata.
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
	return removed, nil
}

// Clear deletes every event day file and orchestrator transcript of
// dataDir (sheepdog data clear --events) and returns the deleted paths.
// Same guards as Prune.
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
	var removed []string
	for _, f := range append(files, brain...) {
		if err := os.Remove(f); err != nil {
			return removed, err
		}
		removed = append(removed, f)
	}
	return removed, nil
}
